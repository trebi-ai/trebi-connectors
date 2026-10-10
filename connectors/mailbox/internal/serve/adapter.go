// Package serve is the trebi-connector/1 adapter of mailbox. It has no
// channel and no events: it reports whether the account can log in, so
// that Trebi shows the health of the connection. The daemon runs the
// operations as separate `mailbox op` processes.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "mailbox"

const checkEvery = 5 * time.Minute

var (
	_ sdk.Runner         = (*Adapter)(nil)
	_ sdk.StatusReporter = (*Adapter)(nil)
)

// Checker logs in to the account.
type Checker interface {
	Check(ctx context.Context) error
}

// Adapter serves one account.
type Adapter struct {
	mb       Checker
	settings config.Settings
	version  string
	every    time.Duration

	mu  sync.Mutex
	st  sdk.Status
	err error
}

// Option changes the adapter.
type Option func(*Adapter)

// WithCheckInterval replaces the health check interval (tests).
func WithCheckInterval(d time.Duration) Option { return func(a *Adapter) { a.every = d } }

// New builds the adapter.
func New(mb Checker, s config.Settings, version string, opts ...Option) *Adapter {
	a := &Adapter{mb: mb, settings: s, version: version, every: checkEvery}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Initialize reports a missing input, else logs in once.
func (a *Adapter) Initialize(ctx context.Context, _ sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: Name, Version: a.version}}
	if miss := a.settings.Missing(); miss != nil {
		return res, *miss
	}
	st := a.check(ctx)
	res.Account = st.Account
	return res, nil
}

// AuthStatus is the result of the last check.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return sdk.AuthState{}, a.err
	}
	return sdk.AuthState{State: a.st.State, Reason: a.st.Reason, Account: a.st.Account}, nil
}

// Run checks the login at an interval and reports each change.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	t := time.NewTicker(a.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		a.mu.Lock()
		before := a.st
		a.mu.Unlock()
		st := a.check(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if st.State != before.State || st.Reason != before.Reason || st.Message != before.Message {
			if err := e.Status(st); err != nil {
				return err
			}
		}
	}
}

// check logs in and keeps the status.
func (a *Adapter) check(ctx context.Context) sdk.Status {
	err := a.mb.Check(ctx)
	acct := &sdk.Account{ID: a.settings.Address, Name: a.settings.Address}
	var st sdk.Status
	switch {
	case err == nil:
		st = sdk.Status{State: sdk.StateConnected, Account: acct}
	case errors.Is(err, mail.ErrAuth):
		st = sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: "The server refused the address or the app password. Make a new app password and update the connection."}
	default:
		slog.Warn("mailbox check", "err", err)
		st = sdk.Status{State: sdk.StateError, Message: err.Error()}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st = st
	a.err = nil
	if st.State == sdk.StateError {
		a.err = errors.New(st.Message)
	}
	return st
}
