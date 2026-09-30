package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SandboxFile is the state file of the sandbox in TREBI_STATE_DIR.
const SandboxFile = "sandbox.json"

// sandboxKeep bounds the events that the sandbox keeps for replay and
// history.
const sandboxKeep = 500

// SandboxConfig describes the adapter that a sandbox stands in for. Use the
// values of the real adapter, so the sandbox passes the same conformance
// checks as the catalog manifest.
type SandboxConfig struct {
	Adapter  AdapterInfo
	Account  Account // the account after a login
	Events   []EventDecl
	Features []string
	Limits   Limits
	Login    []string
	// LoginDelay is the time a login flow waits before it completes. The
	// default is one second, so auth/cancel can end a flow first.
	LoginDelay time.Duration
}

// Sandbox is the reference adapter for `serve --sandbox` and for SDK tests:
// fixed rooms and a thread, sends that come back as self message events,
// and a fake login of each kind. It keeps its login, rooms, and events in
// TREBI_STATE_DIR, so a restart stays logged in. It needs no account, so
// catalog CI runs the conformance checks on it.
type Sandbox struct {
	cfg SandboxConfig

	mu      sync.Mutex
	folders Trebi
	st      sandboxState
	emit    Emitter
	inputs  map[string]chan map[string]string
}

// sandboxState is the durable part of a sandbox. Attachment paths in it
// are relative to the state folder.
type sandboxState struct {
	LoggedIn bool                `json:"logged_in"`
	Seq      int                 `json:"seq"`
	Rooms    []Room              `json:"rooms"`
	Threads  map[string][]Thread `json:"threads"`
	Events   []Event             `json:"events"`
}

var (
	_ Authenticator = (*Sandbox)(nil)
	_ Runner        = (*Sandbox)(nil)
	_ FolderUser    = (*Sandbox)(nil)
	_ Sender        = (*Sandbox)(nil)
	_ RoomLister    = (*Sandbox)(nil)
	_ RoomGetter    = (*Sandbox)(nil)
	_ RoomOpener    = (*Sandbox)(nil)
	_ ThreadLister  = (*Sandbox)(nil)
	_ ThreadCreator = (*Sandbox)(nil)
	_ Replayer      = (*Sandbox)(nil)
	_ Historian     = (*Sandbox)(nil)
	_ Typer         = (*Sandbox)(nil)
	_ Seer          = (*Sandbox)(nil)
	_ Reactor       = (*Sandbox)(nil)
	_ Editor        = (*Sandbox)(nil)
)

// NewSandbox builds a sandbox with three rooms and one thread.
func NewSandbox(cfg SandboxConfig) *Sandbox {
	if cfg.Account.ID == "" {
		cfg.Account = Account{ID: "sandbox", Name: "Sandbox"}
	}
	if cfg.LoginDelay == 0 {
		cfg.LoginDelay = time.Second
	}
	if len(cfg.Events) == 0 {
		cfg.Events = []EventDecl{{Type: "message"}}
	}
	return &Sandbox{
		cfg: cfg,
		st: sandboxState{
			Rooms: []Room{
				{ID: "sandbox-general", Name: "general", Kind: RoomChannel},
				{ID: "sandbox-team", Name: "team", Kind: RoomGroup},
				{ID: "sandbox-dm-ana", Name: "Ana", Kind: RoomDM},
			},
			Threads: map[string][]Thread{"sandbox-general": {{ID: "sandbox-thread-1", Title: "Sandbox thread"}}},
		},
		inputs: map[string]chan map[string]string{},
	}
}

// UseFolders loads the state of an earlier run. An empty or missing state
// file keeps the new state.
func (s *Sandbox) UseFolders(t Trebi) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folders = t
	if t.StateDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(t.StateDir, SandboxFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var st sandboxState
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("read %s: %w", SandboxFile, err)
	}
	if len(st.Rooms) > 0 {
		s.st.Rooms = st.Rooms
	}
	if st.Threads != nil {
		s.st.Threads = st.Threads
	}
	s.st.LoggedIn, s.st.Seq, s.st.Events = st.LoggedIn, st.Seq, st.Events
	return nil
}

// save writes the state with a temp file and a rename. The caller holds mu.
func (s *Sandbox) save() error {
	if s.folders.StateDir == "" {
		return nil
	}
	b, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	path := filepath.Join(s.folders.StateDir, SandboxFile)
	if err := os.MkdirAll(s.folders.StateDir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Initialize answers with the configured result.
func (s *Sandbox) Initialize(context.Context, InitializeParams) (InitializeResult, error) {
	res := InitializeResult{
		Adapter:  s.cfg.Adapter,
		Events:   slices.Clone(s.cfg.Events),
		Features: slices.Clone(s.cfg.Features),
		Limits:   s.cfg.Limits,
		Login:    slices.Clone(s.cfg.Login),
	}
	if st, _ := s.AuthStatus(context.Background()); st.Account != nil { //nolint:errcheck // AuthStatus of a sandbox never fails
		res.Account = st.Account
	}
	return res, nil
}

// Run keeps the Emitter for the self events of each send.
func (s *Sandbox) Run(ctx context.Context, e Emitter) error {
	s.mu.Lock()
	s.emit = e
	s.mu.Unlock()
	<-ctx.Done()
	s.mu.Lock()
	s.emit = nil
	s.mu.Unlock()
	return nil
}

// AuthStatus is connected after a login, or always with no login kinds.
func (s *Sandbox) AuthStatus(context.Context) (AuthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected() {
		return AuthState{State: StateAuthRequired, Reason: ReasonNone}, nil
	}
	acct := s.cfg.Account
	return AuthState{State: StateConnected, Account: &acct}, nil
}

func (s *Sandbox) connected() bool { return s.st.LoggedIn || len(s.cfg.Login) == 0 }

// BeginAuth sends a fake first step, waits LoginDelay (or the input of an
// input step), sends one more step, and logs in.
func (s *Sandbox) BeginAuth(ctx context.Context, kind string, steps StepSink) error {
	exp := FormatTime(time.Now().Add(20 * time.Second))
	var first, next Step
	switch kind {
	case StepQR:
		first = Step{Kind: StepQR, Data: "sandbox-qr-1", ExpiresAt: exp}
		next = Step{Kind: StepQR, Data: "sandbox-qr-2", ExpiresAt: exp}
	case StepDeviceCode:
		first = Step{Kind: StepDeviceCode, URL: "https://example.com/device", Code: "SANDBOX-1", ExpiresAt: exp}
		next = Step{Kind: StepWait, Message: "Enter the code on the device page"}
	case StepURL:
		first = Step{Kind: StepURL, URL: "https://example.com/login", ExpiresAt: exp}
		next = Step{Kind: StepWait, Message: "Finish the login in the browser"}
	case StepInput:
		first = Step{Kind: StepInput, Fields: []StepField{{Name: "code", Label: "Code", Secret: true, Help: "Any value works in the sandbox."}}}
	default:
		first = Step{Kind: StepWait, Message: "Wait for the sandbox login"}
	}
	in := make(chan map[string]string, 1)
	s.mu.Lock()
	s.inputs[steps.FlowID()] = in
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inputs, steps.FlowID())
		s.mu.Unlock()
	}()
	if err := steps.Step(first); err != nil {
		return err
	}
	if kind == StepInput {
		select {
		case fields := <-in:
			if fields["code"] == "" {
				return Invalid("the code is empty")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		select {
		case <-time.After(s.cfg.LoginDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := steps.Step(next); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.LoggedIn = true
	return s.save()
}

// SubmitAuth answers an input step.
func (s *Sandbox) SubmitAuth(_ context.Context, flowID string, fields map[string]string) error {
	s.mu.Lock()
	in, ok := s.inputs[flowID]
	s.mu.Unlock()
	if !ok {
		return NotFound("unknown login flow")
	}
	select {
	case in <- fields:
	default:
	}
	return nil
}

// Logout ends the fake session.
func (s *Sandbox) Logout(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.LoggedIn = false
	return s.save()
}

// Send stores the message and emits it as a self message event.
func (s *Sandbox) Send(_ context.Context, m SendParams) (SendResult, error) {
	if m.Text == "" && len(m.Attachments) == 0 {
		return SendResult{}, Invalid("text and attachments are empty")
	}
	s.mu.Lock()
	if !s.connected() {
		s.mu.Unlock()
		return SendResult{}, AuthRequired("log in first")
	}
	room, err := s.room(m.Room)
	if err != nil {
		s.mu.Unlock()
		return SendResult{}, err
	}
	if m.Thread != "" && !slices.ContainsFunc(s.st.Threads[m.Room], func(t Thread) bool { return t.ID == m.Thread }) {
		s.mu.Unlock()
		return SendResult{}, NotFound("unknown thread")
	}
	s.st.Seq++
	id := "sandbox-m" + strconv.Itoa(s.st.Seq)
	atts, err := s.keepAttachments(id, m.Attachments)
	if err != nil {
		s.mu.Unlock()
		return SendResult{}, err
	}
	ev := Event{
		ID: id, Type: "message", TS: FormatTime(time.Now()),
		Room: &room, Sender: &Author{ID: s.cfg.Account.ID, Name: s.cfg.Account.Name, Self: true},
		Text: m.Text, ReplyTo: m.ReplyTo, Attachments: atts,
	}
	if m.Thread != "" {
		ev.Thread = &Thread{ID: m.Thread}
	}
	s.st.Events = append(s.st.Events, ev)
	if n := len(s.st.Events); n > sandboxKeep {
		s.st.Events = slices.Clone(s.st.Events[n-sandboxKeep:])
	}
	err = s.save()
	emit, out := s.emit, s.public(ev)
	s.mu.Unlock()
	if err != nil {
		return SendResult{}, Transient("save the sandbox state: " + err.Error())
	}
	if emit != nil && s.declares("message") {
		emit.Event(out) //nolint:errcheck // the send is done; a lost event is not a send error
	}
	return SendResult{MessageID: id, Thread: m.Thread}, nil
}

func (s *Sandbox) declares(typ string) bool {
	return slices.ContainsFunc(s.cfg.Events, func(d EventDecl) bool { return d.Type == typ })
}

// keepAttachments copies each local file of a send to the state folder and
// returns the attachments with paths relative to it. The caller holds mu.
func (s *Sandbox) keepAttachments(id string, in []Attachment) ([]Attachment, error) {
	out := make([]Attachment, 0, len(in))
	for i, a := range in {
		if a.Path == "" {
			out = append(out, a)
			continue
		}
		if s.folders.StateDir == "" {
			if _, err := os.Stat(a.Path); err != nil {
				return nil, Invalid("attachment: " + err.Error())
			}
			a.Path = ""
			out = append(out, a)
			continue
		}
		name := a.Name
		if name == "" {
			name = filepath.Base(a.Path)
		}
		rel := filepath.Join("attachments", id+"-"+strconv.Itoa(i)+"-"+filepath.Base(name))
		size, err := copyFile(a.Path, filepath.Join(s.folders.StateDir, rel))
		if err != nil {
			return nil, Invalid("attachment: " + err.Error())
		}
		a.Path, a.Size = rel, size
		if a.Name == "" {
			a.Name = name
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close() //nolint:errcheck // read only
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// public returns ev with absolute attachment paths. The caller holds mu.
func (s *Sandbox) public(ev Event) Event {
	if len(ev.Attachments) == 0 {
		return ev
	}
	ev.Attachments = slices.Clone(ev.Attachments)
	for i, a := range ev.Attachments {
		if a.Path != "" && !filepath.IsAbs(a.Path) {
			ev.Attachments[i].Path = filepath.Join(s.folders.StateDir, a.Path)
		}
	}
	return ev
}

// room finds a room. The caller holds mu.
func (s *Sandbox) room(id string) (Room, error) {
	if id == "" {
		return Room{}, Invalid("room is empty")
	}
	for _, r := range s.st.Rooms {
		if r.ID == id {
			return r, nil
		}
	}
	return Room{}, NotFound("unknown room")
}

// message finds a message in a room. The caller holds mu.
func (s *Sandbox) message(room, id string) (int, error) {
	if _, err := s.room(room); err != nil {
		return 0, err
	}
	if id == "" {
		return 0, Invalid("message_id is empty")
	}
	for i, ev := range s.st.Events {
		if ev.ID == id && ev.Room != nil && ev.Room.ID == room {
			return i, nil
		}
	}
	return 0, NotFound("unknown message")
}

// Edit changes the text of a sent message.
func (s *Sandbox) Edit(_ context.Context, p EditParams) error {
	if p.Text == "" {
		return Invalid("text is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.message(p.Room, p.MessageID)
	if err != nil {
		return err
	}
	s.st.Events[i].Text = p.Text
	return s.save()
}

// History returns the newest sent messages of a room, oldest first.
func (s *Sandbox) History(_ context.Context, q HistoryQuery) (EventPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.room(q.Room); err != nil {
		return EventPage{}, err
	}
	var out []Event
	for _, ev := range s.st.Events {
		if ev.ID == q.Before {
			break
		}
		if ev.Room != nil && ev.Room.ID == q.Room && (q.Thread == "" || (ev.Thread != nil && ev.Thread.ID == q.Thread)) {
			out = append(out, s.public(ev))
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return EventPage{Events: out}, nil
}

// Replay returns the events after an event id. An unknown id is a gap.
func (s *Sandbox) Replay(_ context.Context, after string, limit int) ([]Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if after != "" {
		i := slices.IndexFunc(s.st.Events, func(ev Event) bool { return ev.ID == after })
		if i < 0 {
			return nil, false, nil
		}
		start = i + 1
	}
	var out []Event
	for _, ev := range s.st.Events[start:] {
		if limit > 0 && len(out) == limit {
			return out, false, nil
		}
		out = append(out, s.public(ev))
	}
	return out, true, nil
}

// ListRooms filters the rooms by name and kind. The cursor is an offset.
func (s *Sandbox) ListRooms(_ context.Context, q RoomQuery) (RoomPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var match []Room
	for _, r := range s.st.Rooms {
		if (q.Kind == "" || r.Kind == q.Kind) && strings.Contains(strings.ToLower(r.Name), strings.ToLower(q.Query)) {
			match = append(match, r)
		}
	}
	start, _ := strconv.Atoi(q.Cursor) //nolint:errcheck // a bad cursor starts at the first page
	start = min(max(start, 0), len(match))
	end := len(match)
	if q.Limit > 0 {
		end = min(start+q.Limit, len(match))
	}
	page := RoomPage{Rooms: match[start:end]}
	if end < len(match) {
		page.Next = strconv.Itoa(end)
	}
	return page, nil
}

// GetRoom finds one room.
func (s *Sandbox) GetRoom(_ context.Context, id string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.room(id)
}

// OpenRoom opens a DM room with any user.
func (s *Sandbox) OpenRoom(_ context.Context, user string) (Room, error) {
	if user == "" {
		return Room{}, Invalid("user is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Room{ID: "sandbox-dm-" + user, Name: user, Kind: RoomDM}
	if slices.ContainsFunc(s.st.Rooms, func(x Room) bool { return x.ID == r.ID }) {
		return r, nil
	}
	s.st.Rooms = append(s.st.Rooms, r)
	return r, s.save()
}

// ListThreads lists the threads of a room.
func (s *Sandbox) ListThreads(_ context.Context, q ThreadQuery) (ThreadPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.room(q.Room); err != nil {
		return ThreadPage{}, err
	}
	return ThreadPage{Threads: slices.Clone(s.st.Threads[q.Room])}, nil
}

// CreateThread adds a thread to a room.
func (s *Sandbox) CreateThread(_ context.Context, p CreateThreadParams) (Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.room(p.Room); err != nil {
		return Thread{}, err
	}
	if p.FromMessage != "" {
		if _, err := s.message(p.Room, p.FromMessage); err != nil {
			return Thread{}, err
		}
	}
	s.st.Seq++
	t := Thread{ID: "sandbox-t" + strconv.Itoa(s.st.Seq), Title: p.Title}
	if s.st.Threads == nil {
		s.st.Threads = map[string][]Thread{}
	}
	s.st.Threads[p.Room] = append(s.st.Threads[p.Room], t)
	return t, s.save()
}

// Typing accepts any known room.
func (s *Sandbox) Typing(_ context.Context, room, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.room(room)
	return err
}

// Seen accepts any message id in a known room.
func (s *Sandbox) Seen(_ context.Context, room, messageID string) error {
	if messageID == "" {
		return Invalid("message_id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.room(room)
	return err
}

// React accepts any message id in a known room.
func (s *Sandbox) React(_ context.Context, room, messageID, emoji string) error {
	if messageID == "" || emoji == "" {
		return Invalid("message_id and emoji must be set")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.room(room)
	return err
}
