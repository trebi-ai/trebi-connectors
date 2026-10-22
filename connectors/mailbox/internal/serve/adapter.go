// Package serve is the trebi-connector/1 adapter of mailbox. It sends a
// mail.received event for each new message in INBOX (see package watch),
// and it reports whether the account can log in. The daemon runs the
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
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "mailbox"

// Events are the event types of the adapter.
var Events = []sdk.EventDecl{{Type: watch.EventType}}

var (
	_ sdk.Runner         = (*Adapter)(nil)
	_ sdk.StatusReporter = (*Adapter)(nil)
	_ sdk.FolderUser     = (*Adapter)(nil)
)

// Adapter serves one account.
type Adapter struct {
	mb       *mail.Mailbox
	settings config.Settings
	version  string
	opts     []watch.Option
	now      func() time.Time

	mu    sync.Mutex
	store watch.CursorStore
	st    sdk.Status
	err   error
}

// Option changes the adapter.
type Option func(*Adapter)

// WithWatchOptions passes options to the watcher (tests).
func WithWatchOptions(o ...watch.Option) Option {
	return func(a *Adapter) { a.opts = append(a.opts, o...) }
}

// WithClock replaces the wall clock (tests).
func WithClock(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

// New builds the adapter.
func New(mb *mail.Mailbox, s config.Settings, version string, opts ...Option) *Adapter {
	a := &Adapter{mb: mb, settings: s, version: version, now: time.Now, store: &watch.MemStore{}}
	for _, o := range opts {
		o(a)
	}
	return a
}

// UseFolders keeps the cursor in the state folder.
func (a *Adapter) UseFolders(t sdk.Trebi) error {
	if t.StateDir != "" {
		a.mu.Lock()
		a.store = watch.NewFileStore(t.StateDir)
		a.mu.Unlock()
	}
	return nil
}

// Initialize reports a missing input or a newer state, else logs in once.
func (a *Adapter) Initialize(ctx context.Context, _ sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: Name, Version: a.version}, Events: Events}
	if miss := a.settings.Missing(); miss != nil {
		return res, *miss
	}
	a.mu.Lock()
	store := a.store
	a.mu.Unlock()
	if _, _, err := store.Load(); errors.Is(err, watch.ErrNewerState) {
		return res, sdk.Permanent(err.Error())
	}
	st := a.check(ctx)
	res.Account = st.Account
	return res, nil
}

// AuthStatus is the last status.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return sdk.AuthState{}, a.err
	}
	return sdk.AuthState{State: a.st.State, Reason: a.st.Reason, Account: a.st.Account}, nil
}

// Run watches INBOX and reports each change of the status.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	store := a.store
	a.mu.Unlock()
	// The first status after initialize has no message, so Run compares
	// with what it sent itself.
	var sent *sdk.Status
	report := func(st sdk.Status) {
		a.set(st)
		if sent != nil && same(*sent, st) {
			return
		}
		sent = &st
		if err := e.Status(st); err != nil {
			slog.Warn("mailbox status", "err", err)
		}
	}
	opts := append([]watch.Option{watch.WithStatus(report)}, a.opts...)
	return watch.NewWatcher(a.mb, store, a.now, opts...).Run(ctx, e.Event)
}

// check logs in and keeps the status.
func (a *Adapter) check(ctx context.Context) sdk.Status {
	err := a.mb.Check(ctx)
	var st sdk.Status
	switch {
	case err == nil:
		st = sdk.Status{State: sdk.StateConnected, Account: &sdk.Account{ID: a.settings.Address, Name: a.settings.Address}}
	case errors.Is(err, mail.ErrAuth):
		st = sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: a.mb.AuthMessage(err)}
	default:
		slog.Warn("mailbox check", "err", err)
		st = sdk.Status{State: sdk.StateError, Message: err.Error()}
	}
	a.set(st)
	return st
}

func same(x, y sdk.Status) bool {
	return x.State == y.State && x.Reason == y.Reason && x.Message == y.Message
}

// set keeps the status.
func (a *Adapter) set(st sdk.Status) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st = st
	a.err = nil
	if st.State == sdk.StateError {
		a.err = errors.New(st.Message)
	}
}
