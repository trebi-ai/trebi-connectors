package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/localauth"
)

// Login method ids. They match setup.login of the catalog entry.
const (
	MethodDesktop = "desktop"
	MethodSlk     = "slk"
	MethodToken   = "token"
	MethodSession = "session"
)

// Local is a Slack login on this computer.
type Local interface {
	// Workspaces lists the signed-in workspaces. It reads no secret store.
	Workspaces() ([]localauth.Session, error)
	// Sessions returns the workspaces with their tokens.
	Sessions(ctx context.Context) ([]localauth.Session, error)
}

// Change is what a login did to the store.
type Change struct {
	Put     []string // added or replaced workspaces
	Removed []string
}

// Login runs the login flows. A flow adds or replaces workspaces in the
// store; it does not remove the workspaces of another login.
type Login struct {
	store  *Store
	local  map[string]Local
	verify func(ctx context.Context, w *config.Workspace) error

	mu    sync.Mutex
	flows map[string]*loginFlow
}

type loginFlow struct {
	method string
	in     chan map[string]sdk.Value
}

// NewLogin builds the flows. local has the logins on this computer by
// method id; verify checks a token with auth.test and fills w.
func NewLogin(store *Store, local map[string]Local, verify func(context.Context, *config.Workspace) error) *Login {
	return &Login{store: store, local: local, verify: verify, flows: map[string]*loginFlow{}}
}

// Methods returns the live state of each method.
func (l *Login) Methods() []sdk.LoginMethod {
	return []sdk.LoginMethod{
		l.existing(MethodDesktop, "Use the Slack app on this computer", "The Slack app on this computer has no signed-in workspace."),
		l.existing(MethodSlk, "Use the slk login on this computer", "No slk login is on this computer."),
		{ID: MethodToken, Type: sdk.LoginKey, Label: "Paste a token", Available: true},
		{ID: MethodSession, Type: sdk.LoginKey, Label: "Paste a browser session", Available: true},
	}
}

func (l *Login) existing(id, label, reason string) sdk.LoginMethod {
	m := sdk.LoginMethod{ID: id, Type: sdk.LoginExisting, Label: label, Reason: reason}
	if src := l.local[id]; src != nil {
		if ws, err := src.Workspaces(); err == nil && len(ws) > 0 {
			m.Available, m.Reason = true, ""
		}
	}
	return m
}

// Begin runs one flow: it sends the input step, waits for the answer, and
// saves the workspaces.
func (l *Login) Begin(ctx context.Context, method string, steps sdk.StepSink) (Change, error) {
	var step sdk.Step
	var choices []sdk.Choice
	switch method {
	case MethodDesktop, MethodSlk:
		var err error
		if step, choices, err = l.pickStep(method); err != nil {
			return Change{}, err
		}
	case MethodToken:
		step = sdk.Step{Kind: sdk.StepInput, Message: "Paste a Slack token.", Fields: []sdk.StepField{
			{Name: "token", Label: "Token", Secret: true, Help: "It starts with xoxp- or xoxb-."},
		}}
	case MethodSession:
		step = sdk.Step{Kind: sdk.StepInput, Message: "Copy the session of Slack from your web browser.", Fields: []sdk.StepField{
			{Name: "token", Label: "Session token", Secret: true, Help: "It starts with xoxc-."},
			{Name: "cookie", Label: "Cookie", Secret: true, Help: "The value of the d cookie. It starts with xoxd-."},
		}}
	default:
		return Change{}, sdk.Invalid("unknown login method " + method)
	}
	f := &loginFlow{method: method, in: make(chan map[string]sdk.Value, 1)}
	l.mu.Lock()
	l.flows[steps.FlowID()] = f
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.flows, steps.FlowID())
		l.mu.Unlock()
	}()
	if err := steps.Step(step); err != nil {
		return Change{}, err
	}
	var fields map[string]sdk.Value
	select {
	case fields = <-f.in:
	case <-ctx.Done():
		return Change{}, ctx.Err()
	}
	switch method {
	case MethodToken:
		return l.add(ctx, []config.Workspace{{Token: fields["token"].String(), Source: config.SourceToken}})
	case MethodSession:
		return l.add(ctx, []config.Workspace{{Token: fields["token"].String(), Cookie: fields["cookie"].String(), Source: config.SourceToken}})
	}
	return l.pick(ctx, method, choices, fields["workspaces"])
}

// pickStep lists the workspaces of a login on this computer. The saved
// workspaces are preselected; with none saved, all are.
func (l *Login) pickStep(method string) (sdk.Step, []sdk.Choice, error) {
	src := l.local[method]
	if src == nil {
		return sdk.Step{}, nil, sdk.Invalid("this login is not on this computer")
	}
	ws, err := src.Workspaces()
	if err != nil || len(ws) == 0 {
		return sdk.Step{}, nil, sdk.Invalid("No signed-in Slack workspace is on this computer.")
	}
	saved := map[string]bool{}
	for _, w := range l.store.Workspaces() {
		saved[w.TeamID] = true
	}
	var choices []sdk.Choice
	var picked, all sdk.Value
	for _, w := range ws {
		choices = append(choices, sdk.Choice{Value: w.TeamID, Label: cmpOr(w.TeamName, w.TeamID), Description: host(w)})
		all = append(all, w.TeamID)
		if saved[w.TeamID] {
			picked = append(picked, w.TeamID)
		}
	}
	if len(picked) == 0 {
		picked = all
	}
	return sdk.Step{Kind: sdk.StepInput, Message: "Pick the workspaces to follow.", Fields: []sdk.StepField{
		{Name: "workspaces", Label: "Workspaces", Choices: choices, Multiple: true, Default: picked},
	}}, choices, nil
}

// host is the address of a workspace, for example acme.slack.com.
func host(s localauth.Session) string {
	if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
		return u.Host
	}
	if s.Domain != "" {
		return s.Domain + ".slack.com"
	}
	return ""
}

// pick saves the picked workspaces and removes the saved workspaces of the
// list that the person did not pick.
func (l *Login) pick(ctx context.Context, method string, choices []sdk.Choice, picked sdk.Value) (Change, error) {
	sessions, err := l.local[method].Sessions(ctx)
	if err != nil {
		slog.Warn("slack local login", "method", method, "err", err)
		return Change{}, errors.New("Trebi cannot read the Slack login on this computer.") //nolint:staticcheck // a sentence for the user
	}
	source := config.SourceDesktop
	if method == MethodSlk {
		source = config.SourceSlk
	}
	var ws []config.Workspace
	for _, s := range sessions {
		if slices.Contains(picked, s.TeamID) {
			ws = append(ws, config.Workspace{
				Token: s.Token, Cookie: s.Cookie, TeamID: s.TeamID, TeamName: s.TeamName,
				Domain: s.Domain, URL: s.URL, Source: source,
			})
		}
	}
	ch, err := l.add(ctx, ws)
	if err != nil {
		return ch, err
	}
	for name, w := range l.store.Workspaces() {
		listed := slices.ContainsFunc(choices, func(c sdk.Choice) bool { return c.Value == w.TeamID })
		if listed && !slices.Contains(picked, w.TeamID) {
			ch.Removed = append(ch.Removed, name)
		}
	}
	if len(ch.Removed) > 0 {
		slices.Sort(ch.Removed)
		if err := l.store.Remove(ch.Removed...); err != nil {
			return ch, err
		}
	}
	return ch, nil
}

// add checks each workspace and saves the good ones. It fails when a
// workspace does not pass, and the message names the added ones too.
func (l *Login) add(ctx context.Context, ws []config.Workspace) (Change, error) {
	var ch Change
	var good, bad []string
	for _, w := range ws {
		label := cmpOr(w.TeamName, cmpOr(w.Domain, "the workspace"))
		if err := l.verify(ctx, &w); err != nil {
			slog.Warn("slack login check", "workspace", label, "err", err)
			bad = append(bad, label)
			continue
		}
		name, err := l.store.Add(w)
		if err != nil {
			return ch, err
		}
		ch.Put = append(ch.Put, name)
		good = append(good, cmpOr(w.TeamName, label))
	}
	switch {
	case len(bad) > 0 && len(good) > 0:
		return ch, fmt.Errorf("Trebi added %s. Slack rejected the login for %s.", strings.Join(good, ", "), strings.Join(bad, ", ")) //nolint:staticcheck // a sentence for the user
	case len(bad) > 0:
		return ch, fmt.Errorf("Slack rejected the login for %s.", strings.Join(bad, ", ")) //nolint:staticcheck // a sentence for the user
	case len(ch.Put) == 0:
		return ch, errors.New("Pick at least one workspace.") //nolint:staticcheck // a sentence for the user
	}
	return ch, nil
}

// Submit hands the answer to the flow. It checks the form of a pasted
// token at once, so that the person can fix it.
func (l *Login) Submit(flowID string, fields map[string]sdk.Value) error {
	l.mu.Lock()
	f, ok := l.flows[flowID]
	l.mu.Unlock()
	if !ok {
		return sdk.NotFound("unknown login flow")
	}
	token := strings.TrimSpace(fields["token"].String())
	switch f.method {
	case MethodToken:
		if k := config.TokenKind(token); k != "user" && k != "bot" {
			return sdk.Invalid("The token must start with xoxp- or xoxb-.")
		}
	case MethodSession:
		if config.TokenKind(token) != "session" {
			return sdk.Invalid("The session token must start with xoxc-.")
		}
		if !strings.HasPrefix(strings.TrimSpace(fields["cookie"].String()), "xoxd-") {
			return sdk.Invalid("The cookie must start with xoxd-.")
		}
	}
	clean := map[string]sdk.Value{}
	for k, v := range fields {
		if len(v) == 1 {
			v = sdk.Value{strings.TrimSpace(v[0])}
		}
		clean[k] = v
	}
	select {
	case f.in <- clean:
	default:
	}
	return nil
}

// rejected reports whether err says that Slack does not accept the token.
func rejected(err error) bool {
	var ae *client.APIError
	return errors.As(err, &ae) && ae.IsAuth()
}
