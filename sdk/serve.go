// Package sdk is the Go SDK for trebi-connector/1: JSON-RPC 2.0, one object
// per line, over the stdin and stdout of one long-lived adapter process.
//
// An adapter implements Adapter plus the optional interfaces of the
// features it has. Serve owns the framing, ping, shutdown, the send dedupe
// store, the error mapping, and the feature list.
package sdk

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// Adapter is the one required interface.
type Adapter interface {
	Initialize(ctx context.Context, in InitializeParams) (InitializeResult, error)
}

// Runner receives the Emitter after initialized. Run holds the live
// connection and ends when ctx ends. An error from Run ends Serve.
type Runner interface {
	Run(ctx context.Context, e Emitter) error
}

// Optional interfaces. The SDK derives the feature list from the ones an
// adapter implements.
type (
	Sender interface {
		Send(ctx context.Context, m SendParams) (SendResult, error)
	}
	RoomLister interface {
		ListRooms(ctx context.Context, q RoomQuery) (RoomPage, error)
	}
	RoomGetter interface {
		GetRoom(ctx context.Context, id string) (Room, error)
	}
	RoomOpener interface {
		OpenRoom(ctx context.Context, user string) (Room, error)
	}
	ThreadLister interface {
		ListThreads(ctx context.Context, q ThreadQuery) (ThreadPage, error)
	}
	ThreadCreator interface {
		CreateThread(ctx context.Context, p CreateThreadParams) (Thread, error)
	}
	Replayer interface {
		Replay(ctx context.Context, after string, limit int) ([]Event, bool, error)
	}
	Historian interface {
		History(ctx context.Context, q HistoryQuery) (EventPage, error)
	}
	Typer interface {
		Typing(ctx context.Context, room, thread string) error
	}
	Seer interface {
		Seen(ctx context.Context, room, messageID string) error
	}
	Reactor interface {
		React(ctx context.Context, room, messageID, emoji string) error
	}
	Editor interface {
		Edit(ctx context.Context, p EditParams) error
	}
)

// Subscriber lets a user watch things in the platform. Sync gets the full
// list and returns one state for each subscription, in order.
type Subscriber interface {
	Options(ctx context.Context, q OptionQuery) (OptionPage, error)
	Sync(ctx context.Context, p SyncParams) (SyncResult, error)
}

// SubscriptionSubmitter takes the answers of a person for one subscription.
// An adapter without it must not list the mode manual.
type SubscriptionSubmitter interface {
	SubmitSubscription(ctx context.Context, p SubmitParams) (SubscriptionState, error)
}

// WebhookReceiver turns one platform delivery into events. It sends them
// with EmitterFrom(ctx). A bad signature returns Invalid.
type WebhookReceiver interface {
	ReceiveWebhook(ctx context.Context, req WebhookRequest) error
}

// StatusReporter reports the session state. The SDK sends it as the first
// status after initialized and answers auth/status with it.
type StatusReporter interface {
	AuthStatus(ctx context.Context) (AuthState, error)
}

// Authenticator runs interactive login flows.
//
// LoginMethods answers auth/methods with the live state of each method. The
// ids and types are the ones of the manifest setup.login entries with no
// input. An unavailable method has a reason that a person can read.
//
// BeginAuth runs one whole flow of one method. The SDK picks the method
// first: an empty id is the first available method, and an unknown or
// unavailable method is invalid. The first Step answers auth/begin, each
// later Step is an auth/step notification, and the return ends the flow
// (nil is a login). ctx ends on auth/cancel. The SDK sends auth/done and
// the status changes.
//
// SubmitAuth gets the answer to the last step that has fields. The SDK
// checks it first: each field of an input step has a value, a field
// without Multiple has one value, and a pick list gets only its choices.
type Authenticator interface {
	StatusReporter
	LoginMethods(ctx context.Context) ([]LoginMethod, error)
	BeginAuth(ctx context.Context, method string, steps StepSink) error
	SubmitAuth(ctx context.Context, flowID string, fields map[string]Value) error
	Logout(ctx context.Context) error
}

// StepSink sends the steps of one login flow.
type StepSink interface {
	FlowID() string
	Step(s Step) error
}

// Emitter is how an adapter sends events and status. Nothing goes out
// before initialized: the SDK queues it until then. SubscriptionsChanged
// asks the daemon for a new sync, for example when a state changes.
type Emitter interface {
	Event(e Event) error
	Status(s Status) error
	SubscriptionsChanged() error
}

type emitterKey struct{}

// EmitterFrom returns the Emitter of the session in the ctx of a request,
// or nil outside one.
func EmitterFrom(ctx context.Context) Emitter {
	e, _ := ctx.Value(emitterKey{}).(Emitter)
	return e
}

// Option changes how Serve runs.
type Option func(*config)

type config struct {
	in       io.Reader
	out      io.Writer
	stateDir string
	cacheDir string
	log      *slog.Logger
	maxSent  int
}

// WithIO replaces stdin and stdout.
func WithIO(r io.Reader, w io.Writer) Option {
	return func(c *config) { c.in, c.out = r, w }
}

// WithStateDir replaces TREBI_STATE_DIR. An empty dir keeps the dedupe
// store in memory.
func WithStateDir(dir string) Option { return func(c *config) { c.stateDir = dir } }

// WithCacheDir replaces TREBI_CACHE_DIR.
func WithCacheDir(dir string) Option { return func(c *config) { c.cacheDir = dir } }

// WithLogger replaces the stderr logger.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

var (
	// ErrLineTooLong is a line over MaxLine.
	ErrLineTooLong = errors.New("line over 4 MiB")

	errReplied = errors.New("replied")
)

// stopWait bounds the wait for adapter goroutines after shutdown, so the
// process exits inside the 5 s the daemon allows.
const stopWait = 3 * time.Second

// Serve runs the protocol on stdin and stdout until shutdown, the end of
// stdin, or the end of ctx.
func Serve(ctx context.Context, a Adapter, opts ...Option) error {
	env, _ := FromEnv()
	cfg := config{in: os.Stdin, out: os.Stdout, stateDir: env.StateDir, cacheDir: env.CacheDir, maxSent: defaultMaxSent}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.log == nil {
		cfg.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if fu, ok := a.(FolderUser); ok {
		if err := fu.UseFolders(Trebi{StateDir: cfg.stateDir, CacheDir: cfg.cacheDir}); err != nil {
			return fmt.Errorf("use folders: %w", err)
		}
	}
	sent, err := newSentStore(cfg.stateDir, cfg.maxSent)
	if err != nil {
		return err
	}
	s := &session{a: a, out: cfg.out, log: cfg.log.With("logger", "trebi_sdk"), sent: sent, flows: map[string]*flow{}, sending: map[string]chan struct{}{}}
	return s.run(ctx, cfg.in)
}

// session is one protocol session over one pair of streams.
type session struct {
	a    Adapter
	out  io.Writer
	log  *slog.Logger
	sent *sentStore

	wmu sync.Mutex

	sendMu sync.Mutex
	// sending has one slot per room: one send at a time keeps the order in
	// a room and the dedupe exact.
	sending map[string]chan struct{}

	mu       sync.Mutex
	init     *InitializeResult
	features map[string]bool
	ready    bool
	starting bool
	queue    [][]byte
	flows    map[string]*flow
	nflows   int
	missing  *MissingInput // set when Initialize reports a missing input

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	runErr chan error
}

type inMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type outMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func (s *session) run(parent context.Context, in io.Reader) error {
	s.ctx, s.cancel = context.WithCancel(parent)
	s.runErr = make(chan error, 1)
	defer s.stop()
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() { // ends at the end of the input or after stop
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64*1024), MaxLine)
		for sc.Scan() {
			select {
			case lines <- bytes.Clone(sc.Bytes()):
			case <-s.ctx.Done():
				return
			}
		}
		if err := sc.Err(); errors.Is(err, bufio.ErrTooLong) {
			readErr <- ErrLineTooLong
		} else {
			readErr <- err
		}
	}()
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case err := <-s.runErr:
			return fmt.Errorf("adapter run: %w", err)
		case line, ok := <-lines:
			if !ok {
				select {
				case err := <-readErr:
					return err
				default:
					return nil
				}
			}
			if s.dispatch(line) {
				return nil
			}
		}
	}
}

// stop ends every flow and the Runner, then waits a bounded time.
func (s *session) stop() {
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }() // ends when the adapter goroutines honor ctx
	select {
	case <-done:
	case <-time.After(stopWait):
		s.log.Warn("adapter goroutines still run after shutdown")
	}
}

// dispatch handles one line and reports a shutdown.
func (s *session) dispatch(line []byte) bool {
	if len(bytes.TrimSpace(line)) == 0 {
		return false
	}
	var m inMsg
	if err := json.Unmarshal(line, &m); err != nil || m.JSONRPC != "2.0" {
		s.log.Warn("drop a line that is not JSON-RPC 2.0", "err", err)
		return false
	}
	if len(m.ID) == 0 {
		if m.Method == MethodInitialized {
			s.wg.Add(1)
			go func() { // the status check of the adapter must not block the read loop
				defer s.wg.Done()
				s.initialized()
			}()
		}
		return false
	}
	switch m.Method {
	case MethodInitialize:
		s.initialize(m)
	case MethodPing:
		s.reply(m.ID, Empty{}, nil)
	case MethodShutdown:
		s.reply(m.ID, Empty{}, nil)
		return true
	default:
		s.wg.Add(1)
		go func() { // ends when the adapter call returns; its ctx ends at stop
			defer s.wg.Done()
			s.request(m)
		}()
	}
	return false
}

func (s *session) initialize(m inMsg) {
	var p InitializeParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.reply(m.ID, nil, Invalid("initialize params: "+err.Error()))
		return
	}
	if p.Protocol != Protocol {
		s.reply(m.ID, nil, Invalid("protocol mismatch: this adapter speaks "+Protocol))
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout(MethodInitialize))
	defer cancel()
	res, err := s.a.Initialize(ctx, p)
	mi, missing := asMissingInput(err)
	if err != nil && !missing {
		s.reply(m.ID, nil, AsError(err))
		return
	}
	res.Protocol = Protocol
	res.Features = deriveFeatures(s.a, res.Features)
	res.Events = orEmpty(res.Events)
	res.Features = orEmpty(res.Features)
	s.mu.Lock()
	if missing {
		s.missing = &mi
	}
	s.init = &res
	s.features = map[string]bool{}
	for _, f := range res.Features {
		s.features[f] = true
	}
	s.mu.Unlock()
	s.reply(m.ID, res, nil)
}

// initialized sends the first status, then what the adapter queued, then
// starts the Runner.
func (s *session) initialized() {
	s.mu.Lock()
	if s.ready || s.starting || s.init == nil {
		s.mu.Unlock()
		return
	}
	s.starting = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, timeout(MethodAuthStatus))
	st := s.status(ctx)
	cancel()
	s.mu.Lock()
	if err := s.write(outMsg{JSONRPC: "2.0", Method: MethodStatus, Params: st}); err != nil {
		s.log.Warn("write status", "err", err)
	}
	for _, line := range s.queue {
		if err := s.writeLine(line); err != nil {
			s.log.Warn("write queued line", "err", err)
		}
	}
	s.queue = nil
	s.ready = true
	missing := s.missing != nil
	s.mu.Unlock()
	if r, ok := s.a.(Runner); ok && !missing {
		s.wg.Add(1)
		go func() { // ends when Run returns; Run ends with s.ctx
			defer s.wg.Done()
			if err := r.Run(s.ctx, emitter{s}); err != nil && s.ctx.Err() == nil {
				s.runErr <- err
			}
		}()
	}
}

// status is the session state now.
func (s *session) status(ctx context.Context) Status {
	s.mu.Lock()
	mi := s.missing
	s.mu.Unlock()
	if mi != nil {
		return Status{State: StateAuthRequired, Reason: ReasonMissingInput, Message: mi.Error()}
	}
	sr, ok := s.a.(StatusReporter)
	if !ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		return Status{State: StateConnected, Account: s.init.Account}
	}
	st, err := sr.AuthStatus(ctx)
	if err != nil {
		return Status{State: StateError, Message: err.Error()}
	}
	if st.State == StateAuthRequired && st.Reason == "" {
		st.Reason = ReasonNone
	}
	return Status{State: st.State, Reason: st.Reason, Account: st.Account, Accounts: st.Accounts}
}

func (s *session) has(feature string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.features[feature]
}

func (s *session) request(m inMsg) {
	s.mu.Lock()
	started, mi := s.init != nil, s.missing
	s.mu.Unlock()
	if !started {
		s.reply(m.ID, nil, Invalid("send initialize first"))
		return
	}
	if mi != nil && m.Method != MethodAuthStatus {
		s.reply(m.ID, nil, AuthRequired(mi.Error()))
		return
	}
	if f := FeatureOf(m.Method); f != "" && !s.has(f) {
		s.reply(m.ID, nil, Unsupported(f+" is not a feature of this adapter"))
		return
	}
	ctx := context.WithValue(s.ctx, emitterKey{}, Emitter(emitter{s}))
	if d := timeout(m.Method); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	result, err := s.call(ctx, m)
	if errors.Is(err, errReplied) {
		return
	}
	if err != nil {
		s.reply(m.ID, nil, AsError(err))
		return
	}
	s.reply(m.ID, result, nil)
}

// call runs one request on the adapter. The feature gate already passed,
// so each type assertion of a feature method holds.
func (s *session) call(ctx context.Context, m inMsg) (any, error) {
	switch m.Method {
	case MethodMessagesSend:
		var p SendParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return s.send(ctx, p)
	case MethodMessagesEdit:
		var p EditParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return Empty{}, s.a.(Editor).Edit(ctx, p)
	case MethodMessagesHistory:
		q := HistoryQuery{Limit: 50}
		if err := decode(m.Params, &q); err != nil {
			return nil, err
		}
		page, err := s.a.(Historian).History(ctx, q)
		page.Events = orEmpty(page.Events)
		return page, err
	case MethodEventsReplay:
		p := ReplayParams{Limit: 500}
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		events, complete, err := s.a.(Replayer).Replay(ctx, p.After, p.Limit)
		return ReplayResult{Events: orEmpty(events), Complete: complete}, err
	case MethodRoomsList:
		q := RoomQuery{Limit: 100}
		if err := decode(m.Params, &q); err != nil {
			return nil, err
		}
		page, err := s.a.(RoomLister).ListRooms(ctx, q)
		page.Rooms = orEmpty(page.Rooms)
		return page, err
	case MethodRoomsGet:
		var p RoomParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		r, err := s.getRoom(ctx, p.Room)
		return RoomResult{Room: r}, err
	case MethodRoomsOpen:
		var p RoomOpenParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		r, err := s.a.(RoomOpener).OpenRoom(ctx, p.User)
		return RoomResult{Room: r}, err
	case MethodThreadsList:
		q := ThreadQuery{Limit: 100}
		if err := decode(m.Params, &q); err != nil {
			return nil, err
		}
		page, err := s.a.(ThreadLister).ListThreads(ctx, q)
		page.Threads = orEmpty(page.Threads)
		return page, err
	case MethodThreadsCreate:
		var p CreateThreadParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		t, err := s.a.(ThreadCreator).CreateThread(ctx, p)
		return ThreadResult{Thread: t}, err
	case MethodTyping:
		var p TypingParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return Empty{}, s.a.(Typer).Typing(ctx, p.Room, p.Thread)
	case MethodMessagesSeen:
		var p SeenParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return Empty{}, s.a.(Seer).Seen(ctx, p.Room, p.MessageID)
	case MethodReactionsAdd:
		var p ReactionParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return Empty{}, s.a.(Reactor).React(ctx, p.Room, p.MessageID, p.Emoji)
	case MethodSubscriptionsOptions:
		q := OptionQuery{Limit: 50}
		if err := decode(m.Params, &q); err != nil {
			return nil, err
		}
		page, err := s.a.(Subscriber).Options(ctx, q)
		page.Options = orEmpty(page.Options)
		return page, err
	case MethodSubscriptionsSync:
		var p SyncParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return s.sync(ctx, p)
	case MethodSubscriptionsSubmit:
		var p SubmitParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		sub, ok := s.a.(SubscriptionSubmitter)
		if !ok {
			return nil, Unsupported("this adapter takes no answers for a subscription")
		}
		st, err := sub.SubmitSubscription(ctx, p)
		if err == nil && st.ID == "" {
			st.ID = p.ID
		}
		return st, err
	case MethodWebhookReceive:
		var p WebhookRequest
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		return Empty{}, s.a.(WebhookReceiver).ReceiveWebhook(ctx, p)
	case MethodAuthStatus:
		st := s.status(ctx)
		return AuthStatusResult{State: st.State, Account: st.Account, Accounts: st.Accounts}, nil
	case MethodAuthMethods, MethodAuthBegin, MethodAuthSubmit, MethodAuthCancel, MethodAuthLogout:
		return s.auth(ctx, m)
	}
	return nil, Unsupported("unknown method " + m.Method)
}

// send drops a second send with the same key and checks max_text.
func (s *session) send(ctx context.Context, p SendParams) (any, error) {
	sender, ok := s.a.(Sender)
	if !ok {
		return nil, Unsupported("this adapter does not send messages")
	}
	unlock, err := s.lockRoom(ctx, p.Room)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if p.Key != "" {
		if r, ok := s.sent.get(p.Key); ok {
			return r, nil
		}
	}
	s.mu.Lock()
	max := s.init.Limits.MaxText
	s.mu.Unlock()
	if max > 0 && utf8.RuneCountInString(p.Text) > max {
		return nil, Invalid("text is longer than " + strconv.Itoa(max) + " characters")
	}
	r, err := sender.Send(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.Key != "" {
		if err := s.sent.put(p.Key, r); err != nil {
			s.log.Warn("save send key", "key", p.Key, "err", err)
		}
	}
	return r, nil
}

// lockRoom waits for the send slot of room until ctx ends.
func (s *session) lockRoom(ctx context.Context, room string) (func(), error) {
	s.sendMu.Lock()
	slot := s.sending[room]
	if slot == nil {
		slot = make(chan struct{}, 1)
		s.sending[room] = slot
	}
	s.sendMu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sync checks that the adapter returns one state for each subscription, in
// order.
func (s *session) sync(ctx context.Context, p SyncParams) (any, error) {
	res, err := s.a.(Subscriber).Sync(ctx, p)
	if err != nil {
		return nil, err
	}
	res.Subscriptions = orEmpty(res.Subscriptions)
	if len(res.Subscriptions) != len(p.Subscriptions) {
		return nil, Permanent(fmt.Sprintf("the adapter returns %d states for %d subscriptions", len(res.Subscriptions), len(p.Subscriptions)))
	}
	for i, st := range res.Subscriptions {
		if st.ID != p.Subscriptions[i].ID {
			return nil, Permanent("the adapter returns the states in another order")
		}
	}
	return res, nil
}

// getRoom asks a RoomGetter, else pages through the room list.
func (s *session) getRoom(ctx context.Context, id string) (Room, error) {
	if g, ok := s.a.(RoomGetter); ok {
		return g.GetRoom(ctx, id)
	}
	q := RoomQuery{Limit: 100}
	for range 50 {
		page, err := s.a.(RoomLister).ListRooms(ctx, q)
		if err != nil {
			return Room{}, err
		}
		for _, r := range page.Rooms {
			if r.ID == id {
				return r, nil
			}
		}
		if page.Next == "" {
			break
		}
		q.Cursor = page.Next
	}
	return Room{}, NotFound("unknown room")
}

// flow is one active login flow. step is the last step with fields.
// submit is held while auth/submit runs, so its reply goes out before
// auth/done.
type flow struct {
	id         string
	reqID      json.RawMessage
	cancel     context.CancelFunc
	started    bool
	superseded bool
	step       *Step
	submit     sync.Mutex
}

func (s *session) auth(ctx context.Context, m inMsg) (any, error) {
	a, ok := s.a.(Authenticator)
	if !ok {
		if m.Method == MethodAuthMethods {
			return AuthMethodsResult{Methods: []LoginMethod{}}, nil
		}
		return nil, Unsupported("login is not a feature of this adapter")
	}
	switch m.Method {
	case MethodAuthMethods:
		methods, err := loginMethods(ctx, a)
		if err != nil {
			return nil, err
		}
		return AuthMethodsResult{Methods: methods}, nil
	case MethodAuthBegin:
		var p AuthBeginParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		mctx, cancel := context.WithTimeout(ctx, timeout(MethodAuthMethods))
		method, err := pickMethod(mctx, a, p.Method)
		cancel()
		if err != nil {
			return nil, err
		}
		s.beginAuth(a, m.ID, method)
		return nil, errReplied
	case MethodAuthSubmit:
		var p AuthSubmitParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		f := s.flow(p.FlowID)
		if f == nil {
			return nil, NotFound("unknown login flow")
		}
		s.mu.Lock()
		step := f.step
		s.mu.Unlock()
		if step == nil {
			return nil, Invalid("this login step takes no answer")
		}
		if err := step.checkSubmit(p.Fields); err != nil {
			return nil, err
		}
		f.submit.Lock()
		defer f.submit.Unlock()
		if err := a.SubmitAuth(ctx, p.FlowID, p.Fields); err != nil {
			return nil, err
		}
		s.reply(m.ID, AuthSubmitResult{}, nil)
		return nil, errReplied
	case MethodAuthCancel:
		var p AuthFlowParams
		if err := decode(m.Params, &p); err != nil {
			return nil, err
		}
		if f := s.flow(p.FlowID); f != nil {
			f.cancel()
		}
		return Empty{}, nil
	default: // auth/logout
		if err := a.Logout(ctx); err != nil {
			return nil, err
		}
		s.notify(MethodStatus, Status{State: StateAuthRequired, Reason: ReasonLoggedOut})
		return Empty{}, nil
	}
}

// loginMethods asks the adapter for its methods and checks them: a known
// type, one row per id, and a reason for each unavailable method.
func loginMethods(ctx context.Context, a Authenticator) ([]LoginMethod, error) {
	methods, err := a.LoginMethods(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, m := range methods {
		switch {
		case m.ID == "":
			return nil, Permanent("the adapter returns a login method with no id")
		case seen[m.ID]:
			return nil, Permanent("the adapter returns the login method " + strconv.Quote(m.ID) + " two times")
		case !slices.Contains(LoginTypes(), m.Type):
			return nil, Permanent("the login method " + strconv.Quote(m.ID) + " has the unknown type " + strconv.Quote(m.Type))
		case !m.Available && m.Reason == "":
			return nil, Permanent("the login method " + strconv.Quote(m.ID) + " is not available and has no reason")
		}
		seen[m.ID] = true
	}
	return orEmpty(methods), nil
}

// pickMethod returns the id of the named method, or of the first available
// one for an empty id. An unknown or unavailable method is invalid.
func pickMethod(ctx context.Context, a Authenticator, id string) (string, error) {
	methods, err := loginMethods(ctx, a)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(methods, func(m LoginMethod) bool {
		return m.ID == id || (id == "" && m.Available)
	})
	switch {
	case i < 0 && id == "":
		return "", Invalid("no login method is available")
	case i < 0:
		return "", Invalid("unknown login method " + strconv.Quote(id))
	case !methods[i].Available:
		return "", Invalid(methods[i].Reason)
	}
	return methods[i].ID, nil
}

// checkSubmit checks an answer to the fields of the step.
func (st Step) checkSubmit(fields map[string]Value) error {
	for _, f := range st.Fields {
		v := fields[f.Name]
		if st.Kind == StepInput && v.String() == "" && len(v) <= 1 {
			return Invalid(cmp.Or(f.Label, f.Name) + " is required")
		}
		if len(v) > 1 && !f.Multiple {
			return Invalid(cmp.Or(f.Label, f.Name) + " takes one value")
		}
		for _, x := range v {
			if len(f.Choices) > 0 && !slices.ContainsFunc(f.Choices, func(c Choice) bool { return c.Value == x }) {
				return Invalid(strconv.Quote(x) + " is not a choice of " + cmp.Or(f.Label, f.Name))
			}
		}
	}
	return nil
}

func (s *session) flow(id string) *flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flows[id]
}

// beginAuth runs one flow to its end. A new flow supersedes the old one.
func (s *session) beginAuth(a Authenticator, reqID json.RawMessage, method string) {
	fctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	for _, old := range s.flows {
		old.superseded = true
		old.cancel()
	}
	s.nflows++
	f := &flow{id: "f" + strconv.Itoa(s.nflows), reqID: reqID, cancel: cancel}
	s.flows[f.id] = f
	s.mu.Unlock()
	err := a.BeginAuth(fctx, method, &stepSink{s: s, f: f, ctx: fctx})
	cancel()
	f.submit.Lock()
	f.submit.Unlock() //nolint:staticcheck // waits for the reply of a running auth/submit
	s.mu.Lock()
	delete(s.flows, f.id)
	started, superseded := f.started, f.superseded
	s.mu.Unlock()
	if !started {
		if err == nil {
			err = Permanent("the login flow ended before its first step")
		}
		s.reply(reqID, nil, AsError(err))
		return
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(s.ctx, timeout(MethodAuthStatus))
		st := s.status(ctx)
		cancel()
		s.notify(MethodAuthDone, AuthDoneParams{FlowID: f.id, OK: true, Account: st.Account})
		s.notify(MethodStatus, st)
		return
	}
	msg := err.Error()
	if fctx.Err() != nil {
		msg = "The login was canceled."
	}
	s.notify(MethodAuthDone, AuthDoneParams{FlowID: f.id, OK: false, Error: msg})
	if !superseded {
		s.notify(MethodStatus, Status{State: StateAuthRequired, Reason: ReasonNone})
	}
}

type stepSink struct {
	s   *session
	f   *flow
	ctx context.Context
}

func (k *stepSink) FlowID() string { return k.f.id }

// Step answers auth/begin with the first step and sends each later step.
func (k *stepSink) Step(st Step) error {
	if err := k.ctx.Err(); err != nil {
		return err
	}
	k.s.mu.Lock()
	first := !k.f.started
	k.f.started = true
	if len(st.Fields) > 0 {
		k.f.step = &st
	}
	k.s.mu.Unlock()
	if first {
		k.s.reply(k.f.reqID, AuthBeginResult{FlowID: k.f.id, Step: st}, nil)
		k.s.notify(MethodStatus, Status{State: StateAuthPending})
		return nil
	}
	return k.s.notify(MethodAuthStep, AuthStepParams{FlowID: k.f.id, Step: st})
}

// emitter is the Emitter of one session.
type emitter struct{ s *session }

func (e emitter) Event(ev Event) error {
	if ev.ID == "" || ev.Type == "" {
		return Invalid("an event needs id and type")
	}
	if ev.TS == "" {
		ev.TS = FormatTime(time.Now())
	}
	e.s.mu.Lock()
	declared := e.s.init == nil || slices.ContainsFunc(e.s.init.Events, func(d EventDecl) bool { return d.Type == ev.Type })
	e.s.mu.Unlock()
	if !declared {
		return Invalid("event type " + strconv.Quote(ev.Type) + " is not in initialize")
	}
	return e.s.notify(MethodEvent, ev)
}

func (e emitter) Status(st Status) error { return e.s.notify(MethodStatus, st) }

func (e emitter) SubscriptionsChanged() error {
	return e.s.notify(MethodSubscriptionsChanged, Empty{})
}

// notify writes a notification, or queues it before initialized.
func (s *session) notify(method string, params any) error {
	b, err := marshalLine(outMsg{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !s.ready && (method == MethodEvent || method == MethodStatus || method == MethodSubscriptionsChanged) {
		s.queue = append(s.queue, b)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.writeLine(b)
}

// reply answers one request. A result over the line limit becomes an
// invalid error.
func (s *session) reply(id json.RawMessage, result any, perr *Error) {
	m := outMsg{JSONRPC: "2.0", ID: id, Result: result, Error: perr}
	if perr != nil {
		m.Result = nil
	}
	err := s.write(m)
	if errors.Is(err, ErrLineTooLong) {
		err = s.write(outMsg{JSONRPC: "2.0", ID: id, Error: Invalid("the result is over 4 MiB")})
	}
	if err != nil {
		s.log.Warn("write reply", "err", err)
	}
}

func (s *session) write(m outMsg) error {
	b, err := marshalLine(m)
	if err != nil {
		return err
	}
	return s.writeLine(b)
}

func (s *session) writeLine(b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.out.Write(b)
	return err
}

func marshalLine(m outMsg) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxLine {
		return nil, ErrLineTooLong
	}
	return append(b, '\n'), nil
}

// deriveFeatures keeps the declared features that the adapter can serve.
// With none declared, it lists every method feature the adapter implements.
func deriveFeatures(a Adapter, declared []string) []string {
	impl := map[string]bool{}
	for _, f := range Features() {
		impl[f] = implements(a, f)
	}
	if len(declared) == 0 {
		var out []string
		for _, f := range Features() {
			if impl[f] && f != FeatureAttachmentsIn && f != FeatureAttachmentsOut && f != FeatureReplies {
				out = append(out, f)
			}
		}
		return out
	}
	var out []string
	for _, f := range declared {
		if impl[f] && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// implements reports whether a has the interface of a feature. The
// attachment features and replies have no method, so any adapter may
// declare them.
func implements(a Adapter, feature string) bool {
	var ok bool
	switch feature {
	case FeatureRoomsList:
		_, ok = a.(RoomLister)
	case FeatureRoomsOpen:
		_, ok = a.(RoomOpener)
	case FeatureThreads:
		_, ok = a.(ThreadLister)
	case FeatureThreadsCreate:
		_, ok = a.(ThreadCreator)
	case FeatureHistory:
		_, ok = a.(Historian)
	case FeatureReplay:
		_, ok = a.(Replayer)
	case FeatureTyping:
		_, ok = a.(Typer)
	case FeatureSeen:
		_, ok = a.(Seer)
	case FeatureReactions:
		_, ok = a.(Reactor)
	case FeatureEdit:
		_, ok = a.(Editor)
	case FeatureSubscriptions:
		_, ok = a.(Subscriber)
	case FeatureWebhooks:
		_, ok = a.(WebhookReceiver)
	case FeatureAttachmentsIn, FeatureAttachmentsOut, FeatureReplies:
		ok = true
	}
	return ok
}

// timeout is the request timeout of the protocol. Zero means none.
func timeout(method string) time.Duration {
	switch method {
	case MethodMessagesSend:
		return 30 * time.Second
	case MethodEventsReplay, MethodSubscriptionsSync:
		return 60 * time.Second
	case MethodSubscriptionsSubmit:
		return 30 * time.Second
	case MethodAuthBegin:
		return 0
	}
	return 10 * time.Second
}

func decode(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return Invalid("params: " + err.Error())
	}
	return nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
