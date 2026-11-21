package sdktest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
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
	Adapter  sdk.AdapterInfo
	Account  sdk.Account // the account after a login
	Events   []sdk.EventDecl
	Features []string
	Limits   sdk.Limits
	// Login are the login methods. A method with a Reason is not
	// available. The type sets the fake flow: phone shows QR codes, browser
	// a url step with a pasted fallback, existing a pick list of accounts,
	// and key one secret field.
	Login []sdk.LoginMethod
	// LoginDelay is the time a login flow waits before it completes. The
	// default is one second, so auth/cancel can end a flow first.
	LoginDelay time.Duration
	// Subscriptions adds the features subscriptions and webhooks: one
	// dynamic room field "target" with the options SandboxTargets.
	Subscriptions bool
}

// Sandbox targets. The api targets get a delivery when they become active.
// The manual target asks for a code first.
const (
	SandboxAlpha  = "sandbox/alpha"
	SandboxBeta   = "sandbox/beta"
	SandboxManual = "sandbox/manual"
	// SandboxSignature is the header of a sandbox delivery:
	// "sha256=<hex HMAC of the body>" with the webhook secret.
	SandboxSignature = "x-sandbox-signature"
)

// sandboxDelivery is the body of one sandbox delivery.
type sandboxDelivery struct {
	Target string `json:"target"`
	Text   string `json:"text"`
}

// Sandbox is the reference adapter for `serve --sandbox` and for SDK tests:
// fixed rooms and a thread, sends that come back as self message events,
// and a fake login of each method type. It keeps its login, rooms, and events in
// TREBI_STATE_DIR, so a restart stays logged in. It needs no account, so
// catalog CI runs the conformance checks on it.
type Sandbox struct {
	cfg SandboxConfig

	mu      sync.Mutex
	folders sdk.Trebi
	st      sandboxState
	emit    sdk.Emitter
	inputs  map[string]chan map[string]sdk.Value
	hook    *sdk.Webhook
	client  *http.Client
}

// sandboxState is the durable part of a sandbox. Attachment paths in it
// are relative to the state folder.
type sandboxState struct {
	LoggedIn bool `json:"logged_in"`
	// Accounts are the account ids that the existing login picked.
	Accounts []string                `json:"accounts,omitempty"`
	Seq      int                     `json:"seq"`
	Rooms    []sdk.Room              `json:"rooms"`
	Threads  map[string][]sdk.Thread `json:"threads"`
	Events   []sdk.Event             `json:"events"`

	Subscriptions []sdk.Subscription               `json:"subscriptions,omitempty"`
	States        map[string]sdk.SubscriptionState `json:"states,omitempty"`
	Answers       map[string]map[string]string     `json:"answers,omitempty"`
}

var (
	_ sdk.Authenticator = (*Sandbox)(nil)
	_ sdk.Runner        = (*Sandbox)(nil)
	_ sdk.FolderUser    = (*Sandbox)(nil)
	_ sdk.Sender        = (*Sandbox)(nil)
	_ sdk.RoomLister    = (*Sandbox)(nil)
	_ sdk.RoomGetter    = (*Sandbox)(nil)
	_ sdk.RoomOpener    = (*Sandbox)(nil)
	_ sdk.ThreadLister  = (*Sandbox)(nil)
	_ sdk.ThreadCreator = (*Sandbox)(nil)
	_ sdk.Replayer      = (*Sandbox)(nil)
	_ sdk.Historian     = (*Sandbox)(nil)
	_ sdk.Typer         = (*Sandbox)(nil)
	_ sdk.Seer          = (*Sandbox)(nil)
	_ sdk.Reactor       = (*Sandbox)(nil)
	_ sdk.Editor        = (*Sandbox)(nil)

	_ sdk.Subscriber            = (*Sandbox)(nil)
	_ sdk.SubscriptionSubmitter = (*Sandbox)(nil)
	_ sdk.WebhookReceiver       = (*Sandbox)(nil)
)

// NewSandbox builds a sandbox with three rooms and one thread.
func NewSandbox(cfg SandboxConfig) *Sandbox {
	if cfg.Account.ID == "" {
		cfg.Account = sdk.Account{ID: "sandbox", Name: "Sandbox"}
	}
	if cfg.LoginDelay == 0 {
		cfg.LoginDelay = time.Second
	}
	if len(cfg.Events) == 0 {
		cfg.Events = []sdk.EventDecl{{Type: "message"}}
	}
	return &Sandbox{
		cfg: cfg,
		st: sandboxState{
			Rooms: []sdk.Room{
				{ID: "sandbox-general", Name: "general", Kind: sdk.RoomChannel},
				{ID: "sandbox-team", Name: "team", Kind: sdk.RoomGroup},
				{ID: "sandbox-dm-ana", Name: "Ana", Kind: sdk.RoomDM},
			},
			Threads: map[string][]sdk.Thread{"sandbox-general": {{ID: "sandbox-thread-1", Title: "Sandbox thread"}}},
		},
		inputs: map[string]chan map[string]sdk.Value{},
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// UseFolders loads the state of an earlier run. An empty or missing state
// file keeps the new state.
func (s *Sandbox) UseFolders(t sdk.Trebi) error {
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
	s.st.LoggedIn, s.st.Accounts, s.st.Seq, s.st.Events = st.LoggedIn, st.Accounts, st.Seq, st.Events
	s.st.Subscriptions, s.st.States, s.st.Answers = st.Subscriptions, st.States, st.Answers
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
	return sdk.WriteFileAtomic(path, b, 0o600)
}

// Initialize answers with the configured result and keeps the webhook.
func (s *Sandbox) Initialize(_ context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{
		Adapter:  s.cfg.Adapter,
		Events:   slices.Clone(s.cfg.Events),
		Features: s.features(),
		Limits:   s.cfg.Limits,
	}
	s.mu.Lock()
	s.hook = in.Webhook
	s.mu.Unlock()
	if st, _ := s.AuthStatus(context.Background()); st.Account != nil { //nolint:errcheck // AuthStatus of a sandbox never fails
		res.Account = st.Account
	}
	return res, nil
}

// features are the configured features. With none configured, they are
// the channel features of the sandbox. Subscriptions adds the two
// subscription features.
func (s *Sandbox) features() []string {
	out := slices.Clone(s.cfg.Features)
	if len(out) == 0 {
		for _, f := range sdk.Features() {
			switch f {
			case sdk.FeatureAttachmentsIn, sdk.FeatureAttachmentsOut, sdk.FeatureReplies, sdk.FeatureSubscriptions, sdk.FeatureWebhooks:
			default:
				out = append(out, f)
			}
		}
	}
	out = slices.DeleteFunc(out, func(f string) bool { return f == sdk.FeatureSubscriptions || f == sdk.FeatureWebhooks })
	if s.cfg.Subscriptions {
		out = append(out, sdk.FeatureSubscriptions, sdk.FeatureWebhooks)
	}
	return out
}

// Run keeps the Emitter for the self events of each send.
func (s *Sandbox) Run(ctx context.Context, e sdk.Emitter) error {
	s.mu.Lock()
	s.emit = e
	s.mu.Unlock()
	<-ctx.Done()
	s.mu.Lock()
	s.emit = nil
	s.mu.Unlock()
	return nil
}

// sandboxAccounts are the accounts of the pick list of the existing login.
var sandboxAccounts = []sdk.Choice{
	{Value: "sandbox-a", Label: "Sandbox A", Description: "a.sandbox.example"},
	{Value: "sandbox-b", Label: "Sandbox B", Description: "b.sandbox.example"},
}

// AuthStatus is connected after a login, or always with no login methods.
// After an existing login it has one row per picked account.
func (s *Sandbox) AuthStatus(context.Context) (sdk.AuthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected() {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonNone}, nil
	}
	acct := s.cfg.Account
	st := sdk.AuthState{State: sdk.StateConnected, Account: &acct}
	for _, id := range s.st.Accounts {
		row := sdk.AccountState{ID: id, State: sdk.StateConnected}
		if i := slices.IndexFunc(sandboxAccounts, func(c sdk.Choice) bool { return c.Value == id }); i >= 0 {
			row.Name = sandboxAccounts[i].Label
		}
		st.Accounts = append(st.Accounts, row)
	}
	return st, nil
}

func (s *Sandbox) connected() bool { return s.st.LoggedIn || len(s.cfg.Login) == 0 }

// LoginMethods returns the configured methods. A method with a reason is
// not available.
func (s *Sandbox) LoginMethods(context.Context) ([]sdk.LoginMethod, error) {
	out := slices.Clone(s.cfg.Login)
	for i := range out {
		out[i].Available = out[i].Reason == ""
	}
	return out, nil
}

// BeginAuth sends a fake first step for the type of the method. A flow
// with fields waits for the answer; a url step also ends after LoginDelay.
// A QR flow sends a second code after LoginDelay. Then it logs in.
func (s *Sandbox) BeginAuth(ctx context.Context, method string, steps sdk.StepSink) error {
	i := slices.IndexFunc(s.cfg.Login, func(m sdk.LoginMethod) bool { return m.ID == method })
	if i < 0 {
		return sdk.Invalid("unknown login method " + strconv.Quote(method))
	}
	exp := sdk.FormatTime(time.Now().Add(20 * time.Second))
	var first sdk.Step
	var next *sdk.Step
	switch s.cfg.Login[i].Type {
	case sdk.LoginPhone:
		first = sdk.Step{Kind: sdk.StepQR, Data: "sandbox-qr-1", ExpiresAt: exp}
		next = &sdk.Step{Kind: sdk.StepQR, Data: "sandbox-qr-2", ExpiresAt: exp}
	case sdk.LoginBrowser:
		first = sdk.Step{Kind: sdk.StepURL, URL: "https://example.com/login", ExpiresAt: exp, Fields: []sdk.StepField{
			{Name: "redirect", Label: "Address of the page", Help: "Any value works in the sandbox."},
		}}
	case sdk.LoginExisting:
		first = sdk.Step{Kind: sdk.StepInput, Message: "Pick the accounts to follow.", Fields: []sdk.StepField{
			{Name: "accounts", Label: "Accounts", Choices: sandboxAccounts, Multiple: true, Default: sdk.Value{sandboxAccounts[0].Value, sandboxAccounts[1].Value}},
		}}
	default:
		first = sdk.Step{Kind: sdk.StepInput, Fields: []sdk.StepField{{Name: "key", Label: "Key", Secret: true, Help: "Any value works in the sandbox."}}}
	}
	in := make(chan map[string]sdk.Value, 1)
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
	var wait <-chan time.Time
	if first.Kind != sdk.StepInput {
		wait = time.After(s.cfg.LoginDelay)
	}
	var picked []string
	select {
	case fields := <-in:
		picked = fields["accounts"]
	case <-wait:
		if next != nil {
			if err := steps.Step(*next); err != nil {
				return err
			}
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.LoggedIn, s.st.Accounts = true, picked
	return s.save()
}

// SubmitAuth answers a step with fields. The SDK already checked the
// values against the step.
func (s *Sandbox) SubmitAuth(_ context.Context, flowID string, fields map[string]sdk.Value) error {
	s.mu.Lock()
	in, ok := s.inputs[flowID]
	s.mu.Unlock()
	if !ok {
		return sdk.NotFound("unknown login flow")
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
	s.st.LoggedIn, s.st.Accounts = false, nil
	return s.save()
}

// Send stores the message and emits it as a self message event.
func (s *Sandbox) Send(_ context.Context, m sdk.SendParams) (sdk.SendResult, error) {
	if m.Text == "" && len(m.Attachments) == 0 {
		return sdk.SendResult{}, sdk.Invalid("text and attachments are empty")
	}
	s.mu.Lock()
	if !s.connected() {
		s.mu.Unlock()
		return sdk.SendResult{}, sdk.AuthRequired("log in first")
	}
	room, err := s.room(m.Room)
	if err != nil {
		s.mu.Unlock()
		return sdk.SendResult{}, err
	}
	if m.Thread != "" && !slices.ContainsFunc(s.st.Threads[m.Room], func(t sdk.Thread) bool { return t.ID == m.Thread }) {
		s.mu.Unlock()
		return sdk.SendResult{}, sdk.NotFound("unknown thread")
	}
	s.st.Seq++
	id := "sandbox-m" + strconv.Itoa(s.st.Seq)
	atts, err := s.keepAttachments(id, m.Attachments)
	if err != nil {
		s.mu.Unlock()
		return sdk.SendResult{}, err
	}
	ev := sdk.Event{
		ID: id, Type: "message", TS: sdk.FormatTime(time.Now()),
		Room: &room, Sender: &sdk.Author{ID: s.cfg.Account.ID, Name: s.cfg.Account.Name, Self: true},
		Text: m.Text, ReplyTo: m.ReplyTo, Attachments: atts,
	}
	if m.Thread != "" {
		ev.Thread = &sdk.Thread{ID: m.Thread}
	}
	s.st.Events = append(s.st.Events, ev)
	if n := len(s.st.Events); n > sandboxKeep {
		s.st.Events = slices.Clone(s.st.Events[n-sandboxKeep:])
	}
	err = s.save()
	emit, out := s.emit, s.public(ev)
	s.mu.Unlock()
	if err != nil {
		return sdk.SendResult{}, sdk.Transient("save the sandbox state: " + err.Error())
	}
	if emit != nil && s.declares("message") {
		emit.Event(out) //nolint:errcheck // the send is done; a lost event is not a send error
	}
	return sdk.SendResult{MessageID: id, Thread: m.Thread}, nil
}

func (s *Sandbox) declares(typ string) bool {
	return slices.ContainsFunc(s.cfg.Events, func(d sdk.EventDecl) bool { return d.Type == typ })
}

// keepAttachments copies each local file of a send to the state folder and
// returns the attachments with paths relative to it. The caller holds mu.
func (s *Sandbox) keepAttachments(id string, in []sdk.Attachment) ([]sdk.Attachment, error) {
	out := make([]sdk.Attachment, 0, len(in))
	for i, a := range in {
		if a.Path == "" {
			out = append(out, a)
			continue
		}
		if s.folders.StateDir == "" {
			if _, err := os.Stat(a.Path); err != nil {
				return nil, sdk.Invalid("attachment: " + err.Error())
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
			return nil, sdk.Invalid("attachment: " + err.Error())
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
func (s *Sandbox) public(ev sdk.Event) sdk.Event {
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
func (s *Sandbox) room(id string) (sdk.Room, error) {
	if id == "" {
		return sdk.Room{}, sdk.Invalid("room is empty")
	}
	for _, r := range s.st.Rooms {
		if r.ID == id {
			return r, nil
		}
	}
	return sdk.Room{}, sdk.NotFound("unknown room")
}

// message finds a message in a room. The caller holds mu.
func (s *Sandbox) message(room, id string) (int, error) {
	if _, err := s.room(room); err != nil {
		return 0, err
	}
	if id == "" {
		return 0, sdk.Invalid("message_id is empty")
	}
	for i, ev := range s.st.Events {
		if ev.ID == id && ev.Room != nil && ev.Room.ID == room {
			return i, nil
		}
	}
	return 0, sdk.NotFound("unknown message")
}

// Edit changes the text of a sent message.
func (s *Sandbox) Edit(_ context.Context, p sdk.EditParams) error {
	if p.Text == "" {
		return sdk.Invalid("text is empty")
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
func (s *Sandbox) History(_ context.Context, q sdk.HistoryQuery) (sdk.EventPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.room(q.Room); err != nil {
		return sdk.EventPage{}, err
	}
	var out []sdk.Event
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
	return sdk.EventPage{Events: out}, nil
}

// Replay returns the events after an event id. An unknown id is a gap.
func (s *Sandbox) Replay(_ context.Context, after string, limit int) ([]sdk.Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if after != "" {
		i := slices.IndexFunc(s.st.Events, func(ev sdk.Event) bool { return ev.ID == after })
		if i < 0 {
			return nil, false, nil
		}
		start = i + 1
	}
	var out []sdk.Event
	for _, ev := range s.st.Events[start:] {
		if limit > 0 && len(out) == limit {
			return out, false, nil
		}
		out = append(out, s.public(ev))
	}
	return out, true, nil
}

// ListRooms filters the rooms by name and kind. The cursor is an offset.
func (s *Sandbox) ListRooms(_ context.Context, q sdk.RoomQuery) (sdk.RoomPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var match []sdk.Room
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
	page := sdk.RoomPage{Rooms: match[start:end]}
	if end < len(match) {
		page.Next = strconv.Itoa(end)
	}
	return page, nil
}

// GetRoom finds one room.
func (s *Sandbox) GetRoom(_ context.Context, id string) (sdk.Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.room(id)
}

// OpenRoom opens a DM room with any user.
func (s *Sandbox) OpenRoom(_ context.Context, user string) (sdk.Room, error) {
	if user == "" {
		return sdk.Room{}, sdk.Invalid("user is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := sdk.Room{ID: "sandbox-dm-" + user, Name: user, Kind: sdk.RoomDM}
	if slices.ContainsFunc(s.st.Rooms, func(x sdk.Room) bool { return x.ID == r.ID }) {
		return r, nil
	}
	s.st.Rooms = append(s.st.Rooms, r)
	return r, s.save()
}

// ListThreads lists the threads of a room.
func (s *Sandbox) ListThreads(_ context.Context, q sdk.ThreadQuery) (sdk.ThreadPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.room(q.Room); err != nil {
		return sdk.ThreadPage{}, err
	}
	return sdk.ThreadPage{Threads: slices.Clone(s.st.Threads[q.Room])}, nil
}

// CreateThread adds a thread to a room.
func (s *Sandbox) CreateThread(_ context.Context, p sdk.CreateThreadParams) (sdk.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.room(p.Room); err != nil {
		return sdk.Thread{}, err
	}
	if p.FromMessage != "" {
		if _, err := s.message(p.Room, p.FromMessage); err != nil {
			return sdk.Thread{}, err
		}
	}
	s.st.Seq++
	t := sdk.Thread{ID: "sandbox-t" + strconv.Itoa(s.st.Seq), Title: p.Title}
	if s.st.Threads == nil {
		s.st.Threads = map[string][]sdk.Thread{}
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
		return sdk.Invalid("message_id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.room(room)
	return err
}

// React accepts any message id in a known room.
func (s *Sandbox) React(_ context.Context, room, messageID, emoji string) error {
	if messageID == "" || emoji == "" {
		return sdk.Invalid("message_id and emoji must be set")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.room(room)
	return err
}

// Options lists the sandbox targets for any field.
func (s *Sandbox) Options(_ context.Context, q sdk.OptionQuery) (sdk.OptionPage, error) {
	var page sdk.OptionPage
	for _, t := range []string{SandboxAlpha, SandboxBeta, SandboxManual} {
		if strings.Contains(t, strings.ToLower(q.Query)) {
			page.Options = append(page.Options, sdk.FieldOption{Value: t, Label: strings.TrimPrefix(t, "sandbox/")})
		}
	}
	return page, nil
}

// Sync makes the api targets active with a webhook, asks for a code for the
// manual target, and polls without a webhook. Each api target that becomes
// active gets one delivery.
func (s *Sandbox) Sync(ctx context.Context, p sdk.SyncParams) (sdk.SyncResult, error) {
	s.mu.Lock()
	hook, old := s.hook, s.st.States
	states := map[string]sdk.SubscriptionState{}
	answers := map[string]map[string]string{}
	var res sdk.SyncResult
	var deliver []string
	for _, sub := range p.Subscriptions {
		st := s.subscriptionState(sub, hook)
		if st.State == sdk.SubscriptionActive && st.Mode == sdk.ModeAPI && old[sub.ID].State != sdk.SubscriptionActive {
			deliver = append(deliver, st.Room.ID)
		}
		if a, ok := s.st.Answers[sub.ID]; ok {
			answers[sub.ID] = a
		}
		states[sub.ID] = st
		res.Subscriptions = append(res.Subscriptions, st)
	}
	s.st.Subscriptions, s.st.States, s.st.Answers = slices.Clone(p.Subscriptions), states, answers
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		return sdk.SyncResult{}, sdk.Transient("save the sandbox state: " + err.Error())
	}
	for _, target := range deliver {
		if err := s.deliver(ctx, hook, target); err != nil {
			return sdk.SyncResult{}, err
		}
	}
	return res, nil
}

// subscriptionState is the state of one subscription now. The caller
// holds mu.
func (s *Sandbox) subscriptionState(sub sdk.Subscription, hook *sdk.Webhook) sdk.SubscriptionState {
	target := sub.Value("target")
	if target == "" {
		for _, v := range sub.Values {
			if len(v) > 0 && strings.HasPrefix(v[0], "sandbox/") {
				target = v[0]
			}
		}
	}
	st := sdk.SubscriptionState{ID: sub.ID, Title: target, Room: &sdk.Room{ID: target, Name: strings.TrimPrefix(target, "sandbox/")}}
	switch {
	case target != SandboxAlpha && target != SandboxBeta && target != SandboxManual:
		st.Room, st.Mode, st.State, st.Message = nil, sdk.ModePoll, sdk.SubscriptionError, "Choose a sandbox target."
	case hook == nil:
		st.Mode, st.State = sdk.ModePoll, sdk.SubscriptionPolling
	case target != SandboxManual:
		st.Mode, st.State = sdk.ModeAPI, sdk.SubscriptionActive
	case s.st.Answers[sub.ID]["code"] != "":
		st.Mode, st.State = sdk.ModeManual, sdk.SubscriptionActive
	default:
		st.Mode, st.State = sdk.ModeManual, sdk.SubscriptionActionRequired
		st.Action = &sdk.Action{
			Text: "Copy this address into the sandbox. Then type any code below.",
			Show: []sdk.ShowValue{{Label: "URL", Value: hook.URL}},
			Ask:  []sdk.AskField{{Name: "code", Label: "Code", Secret: true, Required: true}},
		}
	}
	return st
}

// SubmitSubscription keeps the code of the manual target, makes it active,
// and sends one delivery.
func (s *Sandbox) SubmitSubscription(ctx context.Context, p sdk.SubmitParams) (sdk.SubscriptionState, error) {
	if p.Fields["code"] == "" {
		return sdk.SubscriptionState{}, sdk.Invalid("the code is empty")
	}
	s.mu.Lock()
	hook := s.hook
	st, ok := s.st.States[p.ID]
	if !ok {
		s.mu.Unlock()
		return sdk.SubscriptionState{}, sdk.NotFound("unknown subscription")
	}
	if st.Mode != sdk.ModeManual || hook == nil {
		s.mu.Unlock()
		return sdk.SubscriptionState{}, sdk.Invalid("this subscription asks for no answer")
	}
	if s.st.Answers == nil {
		s.st.Answers = map[string]map[string]string{}
	}
	s.st.Answers[p.ID] = map[string]string{"code": p.Fields["code"]}
	st.State, st.Action = sdk.SubscriptionActive, nil
	s.st.States[p.ID] = st
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		return sdk.SubscriptionState{}, sdk.Transient("save the sandbox state: " + err.Error())
	}
	return st, s.deliver(ctx, hook, st.Room.ID)
}

// deliver posts one signed delivery for a target to the webhook URL.
func (s *Sandbox) deliver(ctx context.Context, hook *sdk.Webhook, target string) error {
	body, err := json.Marshal(sandboxDelivery{Target: target, Text: "hello"})
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(hook.Secret))
	mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return sdk.Invalid("webhook url: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SandboxSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := s.client.Do(req)
	if err != nil {
		return sdk.Transient("post the sandbox delivery: " + err.Error())
	}
	resp.Body.Close() //nolint:errcheck,gosec // the status is all the sandbox reads
	if resp.StatusCode >= 300 {
		return sdk.Transient("post the sandbox delivery: " + resp.Status)
	}
	return nil
}

// ReceiveWebhook checks the signature and emits one message event in the
// room of the target.
func (s *Sandbox) ReceiveWebhook(ctx context.Context, req sdk.WebhookRequest) error {
	s.mu.Lock()
	hook := s.hook
	s.mu.Unlock()
	if hook == nil || !req.VerifyHMAC([]byte(hook.Secret), SandboxSignature, "sha256=", sha256.New, sdk.EncodingHex) {
		return sdk.Invalid("bad signature")
	}
	var d sandboxDelivery
	if err := json.Unmarshal([]byte(req.Body), &d); err != nil || d.Target == "" {
		return sdk.Invalid("the body is not a sandbox delivery")
	}
	e := sdk.EmitterFrom(ctx)
	if e == nil || !s.declares("message") {
		return nil
	}
	return e.Event(sdk.Event{
		ID: "sandbox-hook-" + req.ID, Type: "message", TS: sdk.FormatTime(time.Now()),
		Room:   &sdk.Room{ID: d.Target, Name: strings.TrimPrefix(d.Target, "sandbox/")},
		Sender: &sdk.Author{ID: "sandbox-platform", Name: "Sandbox", Bot: true}, Text: d.Text,
	})
}
