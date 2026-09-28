package sdk

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

// Sandbox is an in-memory adapter for `serve --sandbox`: fixed rooms and a
// thread, sends that go to a local history, and a fake login of each kind.
// It needs no account, so catalog CI runs the conformance checks on it.
type Sandbox struct {
	cfg SandboxConfig

	mu       sync.Mutex
	loggedIn bool
	rooms    []Room
	threads  map[string][]Thread
	history  map[string][]Event
	seq      int
	inputs   map[string]chan map[string]string
}

var (
	_ Authenticator = (*Sandbox)(nil)
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
		rooms: []Room{
			{ID: "sandbox-general", Name: "general", Kind: RoomChannel},
			{ID: "sandbox-team", Name: "team", Kind: RoomGroup},
			{ID: "sandbox-dm-ana", Name: "Ana", Kind: RoomDM},
		},
		threads: map[string][]Thread{"sandbox-general": {{ID: "sandbox-thread-1", Title: "Sandbox thread"}}},
		history: map[string][]Event{},
		inputs:  map[string]chan map[string]string{},
	}
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

func (s *Sandbox) connected() bool { return s.loggedIn || len(s.cfg.Login) == 0 }

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
	s.loggedIn = true
	s.mu.Unlock()
	return nil
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
	s.loggedIn = false
	s.mu.Unlock()
	return nil
}

// Send stores the message in the history of its room.
func (s *Sandbox) Send(_ context.Context, m SendParams) (SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected() {
		return SendResult{}, AuthRequired("log in first")
	}
	s.seq++
	id := "sandbox-m" + strconv.Itoa(s.seq)
	ev := Event{
		ID: id, Type: "message", TS: FormatTime(time.Now()),
		Room: &Room{ID: m.Room}, Sender: &Author{ID: s.cfg.Account.ID, Name: s.cfg.Account.Name, Self: true},
		Text: m.Text, ReplyTo: m.ReplyTo, Attachments: m.Attachments,
	}
	if m.Thread != "" {
		ev.Thread = &Thread{ID: m.Thread}
	}
	s.history[m.Room] = append(s.history[m.Room], ev)
	return SendResult{MessageID: id, Thread: m.Thread}, nil
}

// Edit changes the text of a sent message.
func (s *Sandbox) Edit(_ context.Context, p EditParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ev := range s.history[p.Room] {
		if ev.ID == p.MessageID {
			s.history[p.Room][i].Text = p.Text
			return nil
		}
	}
	return NotFound("unknown message")
}

// History returns the newest sent messages of a room, oldest first.
func (s *Sandbox) History(_ context.Context, q HistoryQuery) (EventPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, ev := range s.history[q.Room] {
		if ev.ID == q.Before {
			break
		}
		if q.Thread == "" || (ev.Thread != nil && ev.Thread.ID == q.Thread) {
			out = append(out, ev)
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return EventPage{Events: out}, nil
}

// Replay has nothing to replay.
func (s *Sandbox) Replay(context.Context, string, int) ([]Event, bool, error) {
	return nil, true, nil
}

// ListRooms filters the rooms by name and kind. The cursor is an offset.
func (s *Sandbox) ListRooms(_ context.Context, q RoomQuery) (RoomPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var match []Room
	for _, r := range s.rooms {
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
	for _, r := range s.rooms {
		if r.ID == id {
			return r, nil
		}
	}
	return Room{}, NotFound("unknown room")
}

// OpenRoom opens a DM room with any user.
func (s *Sandbox) OpenRoom(_ context.Context, user string) (Room, error) {
	if user == "" {
		return Room{}, Invalid("user is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Room{ID: "sandbox-dm-" + user, Name: user, Kind: RoomDM}
	if !slices.ContainsFunc(s.rooms, func(x Room) bool { return x.ID == r.ID }) {
		s.rooms = append(s.rooms, r)
	}
	return r, nil
}

// ListThreads lists the threads of a room.
func (s *Sandbox) ListThreads(_ context.Context, q ThreadQuery) (ThreadPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ThreadPage{Threads: slices.Clone(s.threads[q.Room])}, nil
}

// CreateThread adds a thread to a room.
func (s *Sandbox) CreateThread(_ context.Context, p CreateThreadParams) (Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	t := Thread{ID: "sandbox-t" + strconv.Itoa(s.seq), Title: p.Title}
	s.threads[p.Room] = append(s.threads[p.Room], t)
	return t, nil
}

// Typing accepts any room.
func (s *Sandbox) Typing(context.Context, string, string) error { return nil }

// Seen accepts any message.
func (s *Sandbox) Seen(context.Context, string, string) error { return nil }

// React accepts any message.
func (s *Sandbox) React(context.Context, string, string, string) error { return nil }
