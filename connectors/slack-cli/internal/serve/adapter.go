// Package serve is the trebi-connector/1 adapter of slack-cli. The listen
// transports give the events of each workspace; the adapter maps them to
// protocol events.
package serve

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/listen"
)

// Name is the adapter name in initialize.
const Name = "slack-cli"

// Team is one Slack workspace of the connection.
type Team struct {
	Workspace string // config name
	ID        string // team id
	Title     string // team name
	SelfID    string // user id of the token owner
	Names     Names
	Client    *client.Client

	source  listen.Source // nil for a token that cannot stream
	revoked bool
	problem string      // why the team cannot stream
	onAuth  func(error) // gets the result of each token check in Run
	cancel  context.CancelFunc
}

// NewTeam builds a team from a config workspace.
func NewTeam(name string, w config.Workspace, cl *client.Client, names Names) *Team {
	return &Team{Workspace: name, ID: w.TeamID, Title: w.TeamName, SelfID: w.UserID, Client: cl, Names: names}
}

// Messages for the person.
const (
	liveNote  = "Live events need the Slack app login or a browser session."
	rejectFmt = "Slack rejected the login for %s. Log in again."
)

// Listen picks the transport of the team. A session token streams the
// desktop client socket (wsURL replaces its host when set); an app token
// uses Socket Mode. Other tokens cannot stream every channel: the team
// gives actions only.
func (t *Team) Listen(w config.Workspace, wsURL string) {
	switch w.Kind() {
	case "session":
		if w.TeamID == "" {
			t.problem = fmt.Sprintf(rejectFmt, t.label())
			return
		}
		t.source = &listen.WSSource{Workspace: t.Workspace, TeamID: w.TeamID, Client: t.Client, BaseURL: wsURL, Eager: true}
	case "app":
		t.source = &listen.SocketSource{Workspace: t.Workspace, TeamID: w.TeamID, AppClient: t.Client}
	}
}

func (t *Team) label() string { return cmpOr(t.Title, t.Workspace) }

// state is the account row of the team.
func (t *Team) state() sdk.AccountState {
	st := sdk.AccountState{ID: cmpOr(t.ID, t.Workspace), Name: t.label(), State: sdk.StateConnected}
	switch {
	case t.revoked:
		st.State, st.Message = sdk.StateAuthRequired, fmt.Sprintf(rejectFmt, t.label())
	case t.problem != "":
		st.State, st.Message = sdk.StateAuthRequired, t.problem
	case t.source == nil:
		st.Message = liveNote
	}
	return st
}

// Run checks the token, which mints a new session token when it expired,
// then streams events until ctx ends or the stream drops.
func (t *Team) Run(ctx context.Context, out chan<- listen.Event) error {
	_, err := t.Client.Call(ctx, "auth.test", nil)
	if t.onAuth != nil {
		t.onAuth(err)
	}
	if err != nil {
		return err
	}
	return t.source.Run(ctx, out)
}

// Name returns the workspace label.
func (t *Team) Name() string { return t.Workspace }

// check reads the account of the token with auth.test.
func (t *Team) check(ctx context.Context) error {
	var who struct {
		UserID string `json:"user_id"`
		TeamID string `json:"team_id"`
		Team   string `json:"team"`
	}
	err := t.Client.CallJSON(ctx, "auth.test", nil, &who)
	if rejected(err) {
		t.revoked = true
		return nil
	}
	if err != nil {
		return err
	}
	t.revoked = false
	t.SelfID = cmpOr(t.SelfID, who.UserID)
	t.ID = cmpOr(t.ID, who.TeamID)
	t.Title = cmpOr(t.Title, who.Team)
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Options are the parts of the adapter that the command picks: the real
// Slack or the fake of the sandbox.
type Options struct {
	Version string
	Store   *Store
	// Client returns a client for w on the API root.
	Client func(w config.Workspace) *client.Client
	// Mint gets a new session token from the cookie; nil turns refresh off.
	Mint     func(ctx context.Context, hc *http.Client, w config.Workspace) (string, error)
	Local    map[string]Local // logins on this computer by method id
	CacheDir string
	WSURL    string // replaces the desktop socket host when set
}

// Adapter serves the workspaces of the store.
type Adapter struct {
	opts  Options
	login *Login

	mu    sync.Mutex
	teams map[string]*Team
	run   *running // nil until Run
}

type running struct {
	ctx context.Context
	in  chan listen.Event
	e   sdk.Emitter
}

var (
	_ sdk.Runner        = (*Adapter)(nil)
	_ sdk.Authenticator = (*Adapter)(nil)
)

// New builds the adapter with a team for each saved workspace.
func New(opts Options) *Adapter {
	a := &Adapter{opts: opts, teams: map[string]*Team{}}
	a.login = NewLogin(opts.Store, opts.Local, func(ctx context.Context, w *config.Workspace) error {
		return opts.Client(*w).Verify(ctx, w)
	})
	for name, w := range opts.Store.Workspaces() {
		a.teams[name] = a.newTeam(name, w)
	}
	return a
}

// newTeam builds the client, the name cache, and the transport of one
// workspace. A session token gets a new token from its cookie when it
// expires.
func (a *Adapter) newTeam(name string, w config.Workspace) *Team {
	cl := a.opts.Client(w)
	if a.opts.Mint != nil && w.Kind() == "session" && w.Cookie != "" && (w.Domain != "" || w.URL != "") {
		var refresh func(ctx context.Context) (config.Workspace, error)
		refresh = func(ctx context.Context) (config.Workspace, error) {
			cl.Refresh = refresh // Call clears it; serve runs for days
			token, err := a.opts.Mint(ctx, cl.HTTP(), w)
			if err != nil {
				return config.Workspace{}, fmt.Errorf("mint a session token for %s: %w", name, err)
			}
			w.Token = token
			return w, a.opts.Store.Put(name, w)
		}
		cl.Refresh = refresh
	}
	t := NewTeam(name, w, cl, client.NewDirectory(cl, a.opts.CacheDir, name))
	t.Listen(w, a.opts.WSURL)
	return t
}

// Initialize checks each token. A network error is not fatal: the stream
// tries again.
func (a *Adapter) Initialize(ctx context.Context, _ sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: Name, Version: a.opts.Version}, Events: Events}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.teams {
		if err := t.check(ctx); err != nil {
			slog.Warn("slack auth.test", "workspace", t.Workspace, "err", err)
		}
	}
	res.Account = a.account()
	return res, nil
}

// AuthStatus needs a login while the store is empty or Slack rejects one
// workspace. The other workspaces keep running.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authState(), nil
}

func (a *Adapter) authState() sdk.AuthState {
	if len(a.teams) == 0 {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonLoggedOut}
	}
	st := sdk.AuthState{State: sdk.StateConnected, Account: a.account()}
	for _, t := range a.sorted() {
		row := t.state()
		if row.State == sdk.StateAuthRequired {
			st.State, st.Reason = sdk.StateAuthRequired, sdk.ReasonRevoked
		}
		st.Accounts = append(st.Accounts, row)
	}
	return st
}

// message joins the messages of the workspaces that need a login.
func (a *Adapter) message() string {
	var msgs []string
	for _, t := range a.sorted() {
		if row := t.state(); row.State == sdk.StateAuthRequired {
			msgs = append(msgs, row.Message)
		}
	}
	return strings.Join(msgs, " ")
}

func (a *Adapter) sorted() []*Team {
	out := make([]*Team, 0, len(a.teams))
	for _, n := range slices.Sorted(maps.Keys(a.teams)) {
		out = append(out, a.teams[n])
	}
	return out
}

// account names the token owner and the workspaces, for example
// "Big Time Data, Ocral".
func (a *Adapter) account() *sdk.Account {
	var id string
	var names []string
	for _, t := range a.sorted() {
		id = cmpOr(id, t.SelfID)
		names = append(names, t.label())
	}
	if len(names) == 0 {
		return nil
	}
	return &sdk.Account{ID: id, Name: strings.Join(names, ", ")}
}

// LoginMethods returns the live state of each login method.
func (a *Adapter) LoginMethods(context.Context) ([]sdk.LoginMethod, error) {
	return a.login.Methods(), nil
}

// BeginAuth runs one login flow, then starts the new workspaces.
func (a *Adapter) BeginAuth(ctx context.Context, method string, steps sdk.StepSink) error {
	ch, err := a.login.Begin(ctx, method, steps)
	if len(ch.Put) > 0 || len(ch.Removed) > 0 {
		a.apply(ch)
	}
	return err
}

// SubmitAuth answers the step of a flow.
func (a *Adapter) SubmitAuth(_ context.Context, flowID string, fields map[string]sdk.Value) error {
	return a.login.Submit(flowID, fields)
}

// Logout stops every workspace and removes them all from the store.
func (a *Adapter) Logout(context.Context) error {
	a.mu.Lock()
	for name := range a.teams {
		a.stop(name)
	}
	a.mu.Unlock()
	return a.opts.Store.Remove()
}

// apply replaces the teams of a login change. A checked login is not
// revoked.
func (a *Adapter) apply(ch Change) {
	ws := a.opts.Store.Workspaces()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, name := range ch.Removed {
		a.stop(name)
	}
	for _, name := range ch.Put {
		w, ok := ws[name]
		if !ok {
			continue
		}
		a.stop(name)
		t := a.newTeam(name, w)
		a.teams[name] = t
		a.start(t)
	}
}

// start streams t while Run runs. The caller holds a.mu.
func (a *Adapter) start(t *Team) {
	if a.run == nil || t.source == nil || t.problem != "" {
		return
	}
	ctx, cancel := context.WithCancel(a.run.ctx)
	t.cancel = cancel
	t.onAuth = func(err error) { a.authResult(t, err) }
	in := a.run.in
	go func() {
		_ = listen.Stream(ctx, t, in, func(name string, err error) { //nolint:errcheck // Stream ends only with ctx
			slog.Warn("slack stream", "workspace", name, "err", err)
		})
	}()
}

// stop ends the stream of a team and forgets it. The caller holds a.mu.
func (a *Adapter) stop(name string) {
	if t := a.teams[name]; t != nil && t.cancel != nil {
		t.cancel()
	}
	delete(a.teams, name)
}

// Run streams every team and emits the mapped events. One loop maps all
// events, so the name caches need no lock.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	a.run = &running{ctx: ctx, in: make(chan listen.Event, 256), e: e}
	for _, t := range a.teams {
		a.start(t)
	}
	in := a.run.in
	st, msg := a.authState(), a.message()
	a.mu.Unlock()
	if msg != "" {
		if err := e.Status(sdk.Status{State: st.State, Reason: st.Reason, Account: st.Account, Accounts: st.Accounts, Message: msg}); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-in:
			a.mu.Lock()
			t := a.teams[ev.Workspace]
			a.mu.Unlock()
			if t == nil {
				continue
			}
			for _, out := range t.Events(ctx, ev.Raw) {
				if err := e.Event(out); err != nil {
					return err
				}
			}
		}
	}
}

// authResult keeps the token state of t and sends a status when the
// state of t changes.
func (a *Adapter) authResult(t *Team, err error) {
	bad := rejected(err)
	if err != nil && !bad {
		return // a network error only reconnects
	}
	a.mu.Lock()
	if a.teams[t.Workspace] != t || t.revoked == bad {
		a.mu.Unlock()
		return
	}
	t.revoked = bad
	st, msg, e := a.authState(), a.message(), a.run.e
	a.mu.Unlock()
	if serr := e.Status(sdk.Status{State: st.State, Reason: st.Reason, Account: st.Account, Accounts: st.Accounts, Message: msg}); serr != nil {
		slog.Warn("status", "err", serr)
	}
}
