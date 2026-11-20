// Package serve is the trebi-connector/1 adapter of plane-cli. It polls
// Plane and sends work item, comment, intake, and customer changes as events.
package serve

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// Name is the adapter name in initialize.
const Name = "plane-cli"

// Input names of the connection.
const (
	InputProjects = "PLANE_PROJECTS"
	InputAPIKey   = "PLANE_API_KEY"
)

// stateFile keeps the cursors and snapshots in TREBI_STATE_DIR.
const (
	stateFile    = "plane-state.json"
	stateVersion = 1
)

// Workspace is the API access of one workspace.
type Workspace struct {
	Client *client.Client
	HasKey bool
	Web    string // web app root for links, for example https://app.plane.so
}

// Options configure the adapter.
type Options struct {
	Scope     Scope
	ScopeErr  error // a bad PLANE_PROJECTS value
	Version   string
	Interval  time.Duration
	Workspace func(slug string) Workspace
	Log       *slog.Logger
}

type stateDoc struct {
	Version    int                 `json:"version"`
	Workspaces map[string]*wsState `json:"workspaces"`
}

// Adapter watches the workspaces of one scope.
type Adapter struct {
	opt      Options
	path     string
	st       stateDoc
	watchers []*watcher

	mu      sync.Mutex
	account *sdk.Account
	revoked bool
}

var (
	_ sdk.Runner         = (*Adapter)(nil)
	_ sdk.StatusReporter = (*Adapter)(nil)
	_ sdk.FolderUser     = (*Adapter)(nil)
)

// New builds the adapter.
func New(opt Options) *Adapter {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Interval <= 0 {
		opt.Interval = time.Minute
	}
	return &Adapter{opt: opt, st: stateDoc{Version: stateVersion, Workspaces: map[string]*wsState{}}}
}

// UseFolders loads the saved state. A state of another version starts again.
func (a *Adapter) UseFolders(t sdk.Trebi) error {
	if t.StateDir == "" {
		return nil
	}
	a.path = filepath.Join(t.StateDir, stateFile)
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc stateDoc
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != stateVersion {
		a.opt.Log.Warn("the saved state is not usable; start again", "file", a.path, "err", err)
		return nil
	}
	for _, s := range doc.Workspaces {
		if s.Items == nil {
			s.Items = map[string]*snap{}
		}
		if s.Intake == nil {
			s.Intake = map[string]intakeSnap{}
		}
		if s.IntakeBase == nil {
			s.IntakeBase = map[string]bool{}
		}
	}
	if doc.Workspaces != nil {
		a.st = doc
	}
	return nil
}

func (a *Adapter) save() error {
	if a.path == "" {
		return nil
	}
	data, err := json.Marshal(a.st)
	if err != nil {
		return err
	}
	return sdk.WriteFileAtomic(a.path, data, 0o600)
}

// Initialize checks the inputs and the key of each workspace. A rejected
// key is not an error: the status reports auth_required with reason revoked.
func (a *Adapter) Initialize(ctx context.Context, _ sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: Name, Version: a.opt.Version}, Events: Events}
	if a.opt.ScopeErr != nil {
		return res, sdk.Invalid(InputProjects + ": " + a.opt.ScopeErr.Error())
	}
	if len(a.opt.Scope) == 0 {
		return res, sdk.MissingInput{Name: InputProjects, Label: "Projects"}
	}
	a.watchers = nil
	for _, ws := range a.opt.Scope.Workspaces() {
		w := a.opt.Workspace(ws)
		if !w.HasKey {
			return res, sdk.MissingInput{Name: InputAPIKey, Label: "API key"}
		}
		var me struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
		}
		err := w.Client.GetJSON(ctx, "/api/v2/users/me/", nil, &me)
		var ae *client.APIError
		switch {
		case errors.As(err, &ae) && (ae.StatusCode == http.StatusUnauthorized || ae.StatusCode == http.StatusForbidden):
			a.setRevoked(true)
			return res, nil
		case err != nil:
			return res, sdk.Transient(fmt.Sprintf("workspace %s: %v", ws, err))
		}
		if a.account == nil {
			a.account = &sdk.Account{ID: me.ID, Name: cmp.Or(me.DisplayName, me.Email)}
		}
		st := a.st.Workspaces[ws]
		if st == nil {
			st = newWSState()
			a.st.Workspaces[ws] = st
		}
		a.watchers = append(a.watchers, newWatcher(ws, w.Client, a.opt.Scope, st, w.Web, a.opt.Log))
	}
	res.Account = a.account
	return res, nil
}

func (a *Adapter) setRevoked(v bool) {
	a.mu.Lock()
	a.revoked = v
	a.mu.Unlock()
}

// AuthStatus is connected, or auth_required when Plane rejects a key.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked || a.account == nil {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}, nil
	}
	return sdk.AuthState{State: sdk.StateConnected, Account: a.account}, nil
}

// Run polls each workspace at the interval until ctx ends.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	if len(a.watchers) == 0 {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(a.opt.Interval)
	defer t.Stop()
	fails := 0
	for {
		var errs []error
		for _, w := range a.watchers {
			if err := w.tick(ctx, e); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", w.ws, err))
			}
		}
		if err := a.save(); err != nil {
			a.opt.Log.Error("save state", "err", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		fails = a.report(e, errors.Join(errs...), fails)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// report logs a failed tick and moves the status. fails counts the failed
// ticks in sequence; report returns the new count.
func (a *Adapter) report(e sdk.Emitter, err error, fails int) int {
	var ae *client.APIError
	switch {
	case err == nil:
		if fails >= transientTicks {
			a.setRevoked(false)
			_ = e.Status(sdk.Status{State: sdk.StateConnected, Account: a.account}) //nolint:errcheck // the next tick tries again
		}
		return 0
	case errors.As(err, &ae) && ae.StatusCode == http.StatusUnauthorized:
		a.opt.Log.Error("Plane rejects the API key", "err", err)
		a.setRevoked(true)
		_ = e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: "Plane rejects the API key"}) //nolint:errcheck // see above
		return transientTicks
	default:
		a.opt.Log.Warn("poll", "err", err)
		fails++
		if fails == transientTicks || (fails < transientTicks && !transientOnly(err)) {
			_ = e.Status(sdk.Status{State: sdk.StateError, Message: short(err.Error(), 300)}) //nolint:errcheck // see above
			return transientTicks
		}
		return fails
	}
}

// transientTicks is the number of failed ticks in sequence before the
// status shows a network fault or a server error.
const transientTicks = 3

// transientOnly reports a network fault or a server error.
func transientOnly(err error) bool {
	var ae *client.APIError
	if errors.As(err, &ae) {
		return ae.StatusCode >= 500 || ae.StatusCode == http.StatusTooManyRequests
	}
	return true
}

// WebURL is the web app root for an API host.
func WebURL(apiHost string) string {
	u, err := url.Parse(apiHost)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Host == "api.plane.so" {
		return "https://app.plane.so"
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
}
