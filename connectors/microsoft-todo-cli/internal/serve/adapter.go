// Package serve is the trebi-connector/1 adapter of microsoft-todo-cli. Graph
// change notifications give the events through the hosted webhook; a delta
// query every few minutes is the fallback.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "microsoft-todo-cli"

// TypeTask is the one event type.
const TypeTask = "task"

const (
	stateFile    = "microsoft-todo-state.json"
	stateVersion = 1
	// lifetime is the longest expiry that Graph allows for To Do.
	lifetime = 4230 * time.Minute
)

// entry is one Trebi subscription. A Graph subscription in creation has a
// ClientState and no GraphID.
type entry struct {
	Mode        string    `json:"mode"`
	ListID      string    `json:"list_id"`
	ListName    string    `json:"list_name,omitempty"`
	GraphID     string    `json:"graph_id,omitempty"`
	URL         string    `json:"url,omitempty"`
	ClientState string    `json:"client_state,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"`
}

func (e entry) room() *sdk.Room { return &sdk.Room{ID: e.ListID, Name: e.ListName} }

// state is the content of the state file.
type state struct {
	Version int               `json:"version"`
	Subs    map[string]*entry `json:"subscriptions"`
	Delta   map[string]string `json:"delta"` // list id → deltaLink
}

// Adapter serves one Microsoft account.
type Adapter struct {
	cl        *client.Client
	version   string
	stateDir  string
	tick      time.Duration
	pollEvery time.Duration
	now       func() time.Time
	log       *slog.Logger

	syncMu sync.Mutex // one Graph change at a time

	mu       sync.Mutex
	st       state
	stateErr error
	hook     string
	expired  bool // auth_required is sent
}

var (
	_ sdk.Runner          = (*Adapter)(nil)
	_ sdk.Authenticator   = (*Adapter)(nil)
	_ sdk.Subscriber      = (*Adapter)(nil)
	_ sdk.WebhookReceiver = (*Adapter)(nil)
)

// Option changes the adapter.
type Option func(*Adapter)

// WithTick sets how often the renewal check runs.
func WithTick(d time.Duration) Option { return func(a *Adapter) { a.tick = d } }

// WithPollEvery sets how often the delta query runs without a webhook.
func WithPollEvery(d time.Duration) Option { return func(a *Adapter) { a.pollEvery = d } }

// WithNow sets the clock.
func WithNow(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

// New builds the adapter on the state folder. It loads auth.json into cl
// and saves each refreshed token there.
func New(cl *client.Client, version, stateDir string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		cl: cl, version: version, stateDir: stateDir,
		tick: time.Minute, pollEvery: 5 * time.Minute, now: time.Now,
		log: slog.Default().With("source", "microsoft-todo"),
		st:  state{Version: stateVersion, Subs: map[string]*entry{}, Delta: map[string]string{}},
	}
	for _, o := range opts {
		o(a)
	}
	sess, err := client.LoadSession(a.authPath())
	if err != nil {
		return nil, err
	}
	cl.SetSession(sess)
	cl.OnRefresh = func(s client.Session) error { return s.Save(a.authPath()) }
	data, err := os.ReadFile(filepath.Join(stateDir, stateFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return a, nil
	case err != nil:
		return nil, fmt.Errorf("read state: %w", err)
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if st.Version > stateVersion {
		a.stateErr = fmt.Errorf("the state is from a newer microsoft-todo-cli (version %d); update microsoft-todo-cli", st.Version)
		return a, nil
	}
	if st.Subs != nil {
		a.st.Subs = st.Subs
	}
	if st.Delta != nil {
		a.st.Delta = st.Delta
	}
	return a, nil
}

func (a *Adapter) authPath() string { return filepath.Join(a.stateDir, config.AuthFile) }

// saveLocked writes the state file. The caller holds mu.
func (a *Adapter) saveLocked() error {
	data, err := json.MarshalIndent(a.st, "", "  ")
	if err != nil {
		return err
	}
	return client.WriteFile(filepath.Join(a.stateDir, stateFile), data)
}

// Initialize keeps the webhook. A missing login is not an error: the
// status reports auth_required.
func (a *Adapter) Initialize(_ context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	a.mu.Lock()
	a.hook = ""
	if in.Webhook != nil {
		a.hook = in.Webhook.URL
	}
	a.mu.Unlock()
	res := sdk.InitializeResult{
		Adapter: sdk.AdapterInfo{Name: Name, Version: a.version},
		Events:  []sdk.EventDecl{{Type: TypeTask}},
		Login:   []string{sdk.StepDeviceCode},
	}
	res.Account = account(a.cl.Session())
	return res, nil
}

// Run renews the Graph subscriptions and polls the lists without a
// webhook, until ctx ends.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	stateErr := a.stateErr
	a.mu.Unlock()
	if stateErr != nil {
		return e.Status(sdk.Status{State: sdk.StateError, Message: stateErr.Error()})
	}
	tick := time.NewTicker(a.tick)
	defer tick.Stop()
	poll := time.NewTicker(a.pollEvery)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			a.report(e, a.renewDue(ctx, e))
		case <-poll.C:
			a.report(e, a.pollAll(ctx, e))
		}
	}
}

// report logs an error of the background work. A lost login sends
// auth_required once.
func (a *Adapter) report(e sdk.Emitter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, client.ErrAuth) {
		a.mu.Lock()
		first := !a.expired
		a.expired = true
		a.mu.Unlock()
		if first {
			e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonExpired, Message: err.Error()}) //nolint:errcheck // a closed session stops Run
		}
		return
	}
	a.log.Warn("background", "err", err)
}

// AuthStatus checks the token with one cheap To Do read. The account comes
// from the login, because a To Do token cannot read /me.
func (a *Adapter) AuthStatus(ctx context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	stateErr := a.stateErr
	a.mu.Unlock()
	if stateErr != nil {
		return sdk.AuthState{State: sdk.StateError}, nil
	}
	s := a.cl.Session()
	if s.AccessToken == "" {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonNone}, nil
	}
	acct := account(s)
	err := a.cl.Ping(ctx)
	switch {
	case errors.Is(err, client.ErrAuth):
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonExpired}, nil
	case err != nil:
		return sdk.AuthState{State: sdk.StateConnecting, Account: acct}, nil
	}
	a.mu.Lock()
	a.expired = false
	a.mu.Unlock()
	return sdk.AuthState{State: sdk.StateConnected, Account: acct}, nil
}

// account is the account of the login, or nil when it has none.
func account(s client.Session) *sdk.Account {
	if s.AccountID == "" && s.AccountName == "" {
		return nil
	}
	return &sdk.Account{ID: s.AccountID, Name: s.AccountName}
}

// BeginAuth runs the device code login and saves auth.json.
func (a *Adapter) BeginAuth(ctx context.Context, _ string, steps sdk.StepSink) error {
	if a.cl.ClientID == "" {
		return sdk.Permanent("no Microsoft app id: set the Microsoft app id of the connection")
	}
	dc, err := a.cl.BeginDeviceCode(ctx)
	if err != nil {
		return wireErr(err)
	}
	err = steps.Step(sdk.Step{
		Kind: sdk.StepDeviceCode, URL: dc.VerificationURI, Code: dc.UserCode,
		ExpiresAt: sdk.FormatTime(time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)),
	})
	if err != nil {
		return err
	}
	sess, err := a.cl.WaitDeviceCode(ctx, dc)
	if err != nil {
		return err
	}
	a.cl.SetSession(sess)
	if err := a.cl.Ping(ctx); err != nil {
		return wireErr(err)
	}
	a.mu.Lock()
	a.expired = false
	a.mu.Unlock()
	return sess.Save(a.authPath())
}

// SubmitAuth is not used: the device code login has no input.
func (a *Adapter) SubmitAuth(context.Context, string, map[string]string) error {
	return sdk.Invalid("the device code login has no input")
}

// Logout forgets the login.
func (a *Adapter) Logout(context.Context) error {
	a.cl.SetSession(client.Session{})
	if err := os.Remove(a.authPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// wireErr turns a client error into a protocol error.
func wireErr(err error) error {
	var apiErr *client.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, client.ErrAuth), errors.Is(err, client.ErrNoLogin):
		return sdk.AuthRequired(err.Error())
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		return sdk.NotFound(apiErr.Error())
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests:
		return sdk.RateLimited(apiErr.Error(), 0)
	case errors.As(err, &apiErr) && apiErr.Status >= 500:
		return sdk.Transient(apiErr.Error())
	}
	return err
}
