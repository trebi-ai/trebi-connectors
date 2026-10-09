// Package serve is the trebi-connector/1 adapter of linear-cli. A Linear
// webhook gives the events when Trebi has a hosted hook; a poll of the
// issues and comments gives them when it has not.
package serve

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "linear-cli"

// stateFile keeps the teams, the webhooks, and the poll position.
const stateFile = "linear-state.json"

// stateVersion is the format of stateFile.
const stateVersion = 1

// timeLayout is how Linear writes a time. It sorts as a string.
const timeLayout = "2006-01-02T15:04:05.000Z"

const (
	fieldTeam      = "team"
	fieldResources = "resources"
	hookLabel      = "Trebi"
	pollLimit      = 50
	adminMessage   = "Only a Linear admin can let Trebi watch a team"
)

// Events and Features are what the adapter declares.
var (
	Events   = []sdk.EventDecl{{Type: "issue"}, {Type: "comment"}}
	Features = []string{sdk.FeatureSubscriptions, sdk.FeatureWebhooks}
)

var (
	_ sdk.Subscriber      = (*Adapter)(nil)
	_ sdk.WebhookReceiver = (*Adapter)(nil)
	_ sdk.Runner          = (*Adapter)(nil)
	_ sdk.FolderUser      = (*Adapter)(nil)
	_ sdk.StatusReporter  = (*Adapter)(nil)
)

// watch is what one subscription watches. An empty Team is every public team.
type watch struct {
	Team      string   `json:"team,omitempty"`
	Resources []string `json:"resources"`
}

func watchOf(s sdk.Subscription) watch {
	res := slices.Clone(s.Values[fieldResources])
	if len(res) == 0 {
		res = []string{"Issue"}
	}
	slices.SortFunc(res, func(x, y string) int { return cmp.Or(cmp.Compare(rank(x), rank(y)), strings.Compare(x, y)) })
	return watch{Team: strings.ToUpper(s.Value(fieldTeam)), Resources: slices.Compact(res)}
}

// resources is the order of the choices in the form.
var resources = []string{"Issue", "Comment", "Project", "Cycle"}

func rank(r string) int {
	if i := slices.Index(resources, r); i >= 0 {
		return i
	}
	return len(resources)
}

func (w watch) wants(team, resource string) bool {
	return (w.Team == "" || w.Team == team) && slices.Contains(w.Resources, resource)
}

// state is the content of stateFile. It holds no paths.
type state struct {
	Version int                 `json:"version"`
	Teams   map[string]teamInfo `json:"teams"` // team key → id and name
	Hooks   map[string]hook     `json:"hooks"` // subscription id → webhook
	Polls   map[string]watch    `json:"polls"` // subscription id → watch
	Cursor  string              `json:"cursor,omitempty"`
}

type teamInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// hook is one Linear webhook that Trebi made.
type hook struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Secret string `json:"secret"`
	watch
}

// Adapter serves one API key.
type Adapter struct {
	api      *client.Client
	version  string
	interval time.Duration
	log      *slog.Logger

	mu      sync.Mutex
	dir     string // empty keeps the state in memory
	st      state
	stErr   error
	hook    *sdk.Webhook
	viewer  *client.User
	revoked bool
}

// Option changes the adapter.
type Option func(*Adapter)

// WithInterval replaces the poll interval of two minutes.
func WithInterval(d time.Duration) Option { return func(a *Adapter) { a.interval = d } }

// New builds the adapter. A client with no key makes Initialize report the
// missing input.
func New(api *client.Client, version string, opts ...Option) *Adapter {
	a := &Adapter{
		api: api, version: version, interval: 2 * time.Minute,
		log: slog.Default().With("source", "linear"),
		st:  state{Version: stateVersion, Teams: map[string]teamInfo{}, Hooks: map[string]hook{}, Polls: map[string]watch{}},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// UseFolders loads the state. A state of a newer version makes Initialize
// fail with a clear message; the file stays as it is.
func (a *Adapter) UseFolders(t sdk.Trebi) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dir = t.StateDir
	if a.dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(a.dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	if st.Version > stateVersion {
		a.stErr = sdk.Permanent(fmt.Sprintf("The saved state has version %d. Update linear-cli to read it.", st.Version))
		return nil
	}
	st.Version = stateVersion
	if st.Teams == nil {
		st.Teams = map[string]teamInfo{}
	}
	if st.Hooks == nil {
		st.Hooks = map[string]hook{}
	}
	if st.Polls == nil {
		st.Polls = map[string]watch{}
	}
	a.st = st
	return nil
}

// save writes the state with a temp file and a rename. The caller holds mu.
func (a *Adapter) save() error {
	if a.dir == "" {
		return nil
	}
	data, err := json.MarshalIndent(a.st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(a.dir, stateFile+".*")
	if err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error wins
		return fmt.Errorf("save state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return os.Rename(tmp.Name(), filepath.Join(a.dir, stateFile))
}

// Initialize checks the key. A rejected key is not an error: the status
// reports auth_required with reason revoked.
func (a *Adapter) Initialize(ctx context.Context, p sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: Name, Version: a.version}, Events: Events, Features: Features}
	a.mu.Lock()
	a.hook = p.Webhook
	stErr := a.stErr
	a.mu.Unlock()
	if stErr != nil {
		return res, stErr
	}
	if !a.api.Key() {
		return res, sdk.MissingInput{Name: config.EnvKey, Label: config.KeyLabel}
	}
	me, err := a.api.Viewer(ctx)
	var le *client.Error
	switch {
	case errors.As(err, &le) && le.Unauthorized():
		a.mu.Lock()
		a.revoked = true
		a.mu.Unlock()
		return res, nil
	case err != nil:
		return res, sdk.Transient(err.Error())
	}
	a.mu.Lock()
	a.viewer = &me
	a.mu.Unlock()
	res.Account = account(me)
	return res, nil
}

func account(u client.User) *sdk.Account {
	return &sdk.Account{ID: u.ID, Name: cmp.Or(u.Email, u.Name)}
}

// AuthStatus is connected, or auth_required when Linear rejects the key.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked || a.viewer == nil {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}, nil
	}
	return sdk.AuthState{State: sdk.StateConnected, Account: account(*a.viewer)}, nil
}

// mapErr turns a Linear error into a protocol error.
func mapErr(err error) error {
	var le *client.Error
	if errors.As(err, &le) && le.Unauthorized() {
		return sdk.AuthRequired("Linear does not accept the API key")
	}
	return sdk.Transient(err.Error())
}

// loadTeams reads the teams into the state. The caller holds mu.
func (a *Adapter) loadTeams(ctx context.Context) ([]client.Team, error) {
	teams, err := a.api.Teams(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	for _, t := range teams {
		a.st.Teams[t.Key] = teamInfo{ID: t.ID, Name: t.Name}
	}
	return teams, nil
}

// Options lists the teams for the field team, filtered by name or key.
func (a *Adapter) Options(ctx context.Context, q sdk.OptionQuery) (sdk.OptionPage, error) {
	if q.Field != fieldTeam {
		return sdk.OptionPage{}, sdk.Invalid("linear has no options for the field " + q.Field)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	teams, err := a.loadTeams(ctx)
	if err != nil {
		return sdk.OptionPage{}, err
	}
	if err := a.save(); err != nil {
		return sdk.OptionPage{}, err
	}
	needle := strings.ToLower(q.Query)
	page := sdk.OptionPage{Options: []sdk.FieldOption{}}
	for _, t := range teams {
		if needle != "" && !strings.Contains(strings.ToLower(t.Name), needle) && !strings.Contains(strings.ToLower(t.Key), needle) {
			continue
		}
		if q.Limit > 0 && len(page.Options) == q.Limit {
			break
		}
		page.Options = append(page.Options, sdk.FieldOption{Value: t.Key, Label: t.Name, Description: t.Key})
	}
	return page, nil
}

// Sync makes the Linear webhooks match the list, or polls when Trebi has
// no hosted hook.
func (a *Adapter) Sync(ctx context.Context, p sdk.SyncParams) (sdk.SyncResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	watches := make([]watch, len(p.Subscriptions))
	for i, s := range p.Subscriptions {
		watches[i] = watchOf(s)
	}
	if slices.ContainsFunc(watches, func(w watch) bool { _, ok := a.st.Teams[w.Team]; return w.Team != "" && !ok }) {
		if _, err := a.loadTeams(ctx); err != nil {
			return sdk.SyncResult{}, err
		}
	}
	keep := map[string]bool{}
	res := sdk.SyncResult{Subscriptions: make([]sdk.SubscriptionState, len(p.Subscriptions))}
	a.st.Polls = map[string]watch{}
	for i, s := range p.Subscriptions {
		w := watches[i]
		st := sdk.SubscriptionState{ID: s.ID, Title: a.title(w), Room: a.room(w)}
		if _, ok := a.st.Teams[w.Team]; w.Team != "" && !ok {
			st.Mode, st.State, st.Message = sdk.ModePoll, sdk.SubscriptionError, "Linear has no team "+w.Team
			res.Subscriptions[i] = st
			continue
		}
		if a.hook == nil {
			a.st.Polls[s.ID] = w
			st.Mode, st.State = sdk.ModePoll, sdk.SubscriptionPolling
			res.Subscriptions[i] = st
			continue
		}
		keep[s.ID] = true
		st.Mode = sdk.ModeAPI
		if err := a.syncHook(ctx, s.ID, w); err != nil {
			var le *client.Error
			switch {
			case errors.As(err, &le) && le.Unauthorized():
				return sdk.SyncResult{}, mapErr(err)
			case errors.As(err, &le) && le.Forbidden():
				st.State, st.Message = sdk.SubscriptionError, adminMessage
			default:
				st.State, st.Message = sdk.SubscriptionError, err.Error()
			}
			res.Subscriptions[i] = st
			continue
		}
		st.State = sdk.SubscriptionActive
		res.Subscriptions[i] = st
	}
	for id, h := range a.st.Hooks {
		if !keep[id] {
			a.dropHook(ctx, id, h)
		}
	}
	if err := a.save(); err != nil {
		return sdk.SyncResult{}, err
	}
	return res, nil
}

// syncHook makes one webhook match its watch. The caller holds mu.
func (a *Adapter) syncHook(ctx context.Context, id string, w watch) error {
	h, ok := a.st.Hooks[id]
	if ok && (h.URL != a.hook.URL || h.Team != w.Team) {
		a.dropHook(ctx, id, h)
		ok = false
	}
	if ok {
		if slices.Equal(h.Resources, w.Resources) {
			return nil
		}
		if err := a.api.UpdateWebhook(ctx, h.ID, w.Resources); err != nil {
			return err
		}
		h.watch = w
		a.st.Hooks[id] = h
		return nil
	}
	created, err := a.api.CreateWebhook(ctx, client.WebhookInput{
		URL: a.hook.URL, TeamID: a.st.Teams[w.Team].ID, ResourceTypes: w.Resources, Secret: a.hook.Secret, Label: hookLabel,
	})
	if err != nil {
		return err
	}
	a.st.Hooks[id] = hook{ID: created.ID, URL: a.hook.URL, Secret: cmp.Or(created.Secret, a.hook.Secret), watch: w}
	return nil
}

// dropHook deletes a webhook. A failure is logged: the webhook may be gone
// already. The caller holds mu.
func (a *Adapter) dropHook(ctx context.Context, id string, h hook) {
	if err := a.api.DeleteWebhook(ctx, h.ID); err != nil {
		a.log.WarnContext(ctx, "delete webhook", "webhook_id", h.ID, "err", err)
	}
	delete(a.st.Hooks, id)
}

// title reads "ENG · Issues, Comments". The caller holds mu.
func (a *Adapter) title(w watch) string {
	plural := make([]string, len(w.Resources))
	for i, r := range w.Resources {
		plural[i] = r + "s"
	}
	return cmp.Or(w.Team, "Every public team") + " · " + strings.Join(plural, ", ")
}

// room is the team of a watch, or nil for every public team. The caller
// holds mu.
func (a *Adapter) room(w watch) *sdk.Room {
	if w.Team == "" {
		return nil
	}
	return &sdk.Room{ID: w.Team, Name: a.st.Teams[w.Team].Name}
}
