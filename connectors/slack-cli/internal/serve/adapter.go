// Package serve is the trebi-connector/1 adapter of slack-cli. The listen
// transports give the events of each workspace; the adapter maps them to
// protocol events.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/listen"
)

// Name is the adapter name in initialize.
const Name = "slack-cli"

// Team is one Slack workspace that the adapter listens to.
type Team struct {
	Workspace string // config name
	ID        string // team id
	Title     string // team name
	SelfID    string // user id of the token owner
	Names     Names
	Client    *client.Client

	source  listen.Source
	revoked bool
	onAuth  func(error) // gets the result of each token check in Run
}

// NewTeam builds a team from a config workspace.
func NewTeam(name string, w config.Workspace, cl *client.Client, names Names) *Team {
	return &Team{Workspace: name, ID: w.TeamID, Title: w.TeamName, SelfID: w.UserID, Client: cl, Names: names}
}

// Listen picks the transport of the team. A session token streams the
// desktop client socket (wsURL replaces its host when set); an app token
// uses Socket Mode. Other tokens cannot stream every channel.
func (t *Team) Listen(w config.Workspace, wsURL string) error {
	switch w.Kind() {
	case "session":
		if w.TeamID == "" {
			return fmt.Errorf("workspace %s: the session token has no team id; run `slack-cli auth import` again", t.Workspace)
		}
		t.source = &listen.WSSource{Workspace: t.Workspace, TeamID: w.TeamID, Client: t.Client, BaseURL: wsURL, Eager: true}
	case "app":
		t.source = &listen.SocketSource{Workspace: t.Workspace, TeamID: w.TeamID, AppClient: t.Client}
	default:
		return fmt.Errorf("workspace %s: a %s token cannot stream events; use a session token (`slack-cli auth import`) or an app token", t.Workspace, w.Kind())
	}
	return nil
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
	var ae *client.APIError
	if errors.As(err, &ae) && ae.IsAuth() {
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

// Adapter serves the selected workspaces.
type Adapter struct {
	version string
	teams   []*Team
	problem string // why no team can stream, or a skipped workspace

	mu sync.Mutex
}

var (
	_ sdk.Runner         = (*Adapter)(nil)
	_ sdk.StatusReporter = (*Adapter)(nil)
)

// New builds the adapter. problem tells the user about workspaces that the
// adapter skips.
func New(version string, teams []*Team, problem string) *Adapter {
	return &Adapter{version: version, teams: teams, problem: problem}
}

// Initialize checks each token. A network error is not fatal: the stream
// tries again.
func (a *Adapter) Initialize(ctx context.Context, _ sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: Name, Version: a.version}, Events: Events}
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

// AuthStatus is connected while one workspace has a good token.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authState(), nil
}

func (a *Adapter) authState() sdk.AuthState {
	if len(a.teams) == 0 {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonLoggedOut}
	}
	for _, t := range a.teams {
		if !t.revoked {
			return sdk.AuthState{State: sdk.StateConnected, Account: a.account()}
		}
	}
	return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}
}

// account names the token owner and the workspaces.
func (a *Adapter) account() *sdk.Account {
	var id string
	var names []string
	for _, t := range a.teams {
		if t.revoked {
			continue
		}
		id = cmpOr(id, t.SelfID)
		names = append(names, cmpOr(t.Title, t.Workspace))
	}
	if len(names) == 0 {
		return nil
	}
	return &sdk.Account{ID: id, Name: strings.Join(names, ", ")}
}

// Run streams every team and emits the mapped events. One loop maps all
// events, so the name caches need no lock.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	if a.problem != "" {
		a.mu.Lock()
		st := a.authState()
		a.mu.Unlock()
		if err := e.Status(sdk.Status{State: st.State, Reason: st.Reason, Account: st.Account, Message: a.problem}); err != nil {
			return err
		}
	}
	if len(a.teams) == 0 {
		<-ctx.Done()
		return nil
	}
	in := make(chan listen.Event, 256)
	byName := map[string]*Team{}
	for _, t := range a.teams {
		byName[t.Workspace] = t
		t.onAuth = func(err error) { a.authResult(e, t, err) }
		go func() {
			_ = listen.Stream(ctx, t, in, func(name string, err error) { //nolint:errcheck // Stream ends only with ctx
				slog.Warn("slack stream", "workspace", name, "err", err)
			})
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-in:
			t := byName[ev.Workspace]
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
// state of the connection changes.
func (a *Adapter) authResult(e sdk.Emitter, t *Team, err error) {
	var ae *client.APIError
	bad := errors.As(err, &ae) && ae.IsAuth()
	if err != nil && !bad {
		return // a network error only reconnects
	}
	a.mu.Lock()
	before := a.authState()
	t.revoked = bad
	after := a.authState()
	a.mu.Unlock()
	if !bad && before.State == after.State {
		return
	}
	st := sdk.Status{State: after.State, Reason: after.Reason, Account: after.Account}
	if bad {
		st.Message = fmt.Sprintf("Slack rejects the login of %s; %s", cmpOr(t.Title, t.Workspace), config.LoginHint())
	}
	if serr := e.Status(st); serr != nil {
		slog.Warn("status", "err", serr)
	}
}
