// Package serve is the trebi-connector/1 adapter of github-cli. A watched
// repository gets a webhook when the connection has a hosted hook, and a
// poll of its events otherwise.
package serve

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "github-cli"

// Scope is the OAuth scope of the device login.
const Scope = "repo admin:repo_hook"

// FieldRepository is the one field of the watch form.
const FieldRepository = "repository"

// hooksFile keeps the hooks that the adapter made, so that it can delete
// them when the subscription goes.
const hooksFile = "github-hooks.json"

// minPoll is the shortest poll interval that GitHub allows.
const minPoll = 60 * time.Second

// Events are the event types. Each is a GitHub webhook event name.
var Events = []sdk.EventDecl{
	{Type: "push"}, {Type: "pull_request"}, {Type: "pull_request_review"}, {Type: "issues"},
	{Type: "issue_comment"}, {Type: "release"}, {Type: "workflow_run"},
}

// Adapter serves one GitHub account.
type Adapter struct {
	version  string
	clientID string
	login    *config.LoginStore // the device login; nil outside Trebi
	input    bool               // the token is a connection input
	stateDir string
	every    time.Duration
	log      *slog.Logger
	wake     chan struct{}

	mu    sync.Mutex
	api   client.Client
	emit  sdk.Emitter
	hook  *sdk.Webhook
	self  *client.User
	subs  []sdk.Subscription
	state hookState
	polls map[string]*pollCursor
}

var (
	_ sdk.Runner                = (*Adapter)(nil)
	_ sdk.Authenticator         = (*Adapter)(nil)
	_ sdk.Subscriber            = (*Adapter)(nil)
	_ sdk.WebhookReceiver       = (*Adapter)(nil)
	_ sdk.SubscriptionSubmitter = (*Adapter)(nil)
)

// hookState is the content of hooksFile. Old keeps the secrets of the last
// rotations, so that a delivery that waited in the cloud still verifies.
type hookState struct {
	Hooks map[string]hookRec `json:"hooks"`
	Old   []string           `json:"old_secrets,omitempty"`
}

type hookRec struct {
	ID     int64    `json:"id"`
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events"`
}

// pollCursor is the poll position of one repository.
type pollCursor struct {
	etag   string
	lastID string
	primed bool
}

// Option changes the adapter.
type Option func(*Adapter)

// WithPollEvery replaces the poll interval (tests).
func WithPollEvery(d time.Duration) Option { return func(a *Adapter) { a.every = d } }

// WithLogin turns on the device login. clientID is the OAuth app; an empty
// one makes BeginAuth explain that a token is necessary.
func WithLogin(store *config.LoginStore, clientID string) Option {
	return func(a *Adapter) { a.login, a.clientID = store, clientID }
}

// WithInputToken marks the token as a connection input, which logout
// cannot remove.
func WithInputToken() Option { return func(a *Adapter) { a.input = true } }

// New builds the adapter. stateDir keeps the hooks; empty keeps them in
// memory.
func New(api *client.Client, version, stateDir string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		version: version, stateDir: stateDir, every: minPoll,
		log:   slog.Default().With("source", "github"),
		wake:  make(chan struct{}, 1),
		api:   *api,
		state: hookState{Hooks: map[string]hookRec{}},
		polls: map[string]*pollCursor{},
	}
	for _, o := range opts {
		o(a)
	}
	if stateDir == "" {
		return a, nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, hooksFile))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hooks: %w", err)
	}
	if err := json.Unmarshal(data, &a.state); err != nil {
		return nil, fmt.Errorf("parse hooks: %w", err)
	}
	if a.state.Hooks == nil {
		a.state.Hooks = map[string]hookRec{}
	}
	return a, nil
}

// gh returns a client with the token of now.
func (a *Adapter) gh() *client.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.api
	return &c
}

// Initialize keeps the hook. A token that GitHub rejects is not an error:
// the status reports it.
func (a *Adapter) Initialize(ctx context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	a.mu.Lock()
	a.hook = in.Webhook
	a.mu.Unlock()
	res := sdk.InitializeResult{
		Adapter: sdk.AdapterInfo{Name: Name, Version: a.version},
		Events:  Events,
		Login:   []string{sdk.StepDeviceCode},
	}
	if st, err := a.AuthStatus(ctx); err == nil {
		res.Account = st.Account
	}
	return res, nil
}

// AuthStatus asks GitHub who the token is.
func (a *Adapter) AuthStatus(ctx context.Context) (sdk.AuthState, error) {
	gh := a.gh()
	if gh.Token == "" {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonNone}, nil
	}
	u, err := gh.Me(ctx)
	if client.StatusOf(err) == http.StatusUnauthorized {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}, nil
	}
	if err != nil {
		return sdk.AuthState{}, err
	}
	a.mu.Lock()
	a.self = &u
	a.mu.Unlock()
	return sdk.AuthState{State: sdk.StateConnected, Account: &sdk.Account{ID: u.Login, Name: cmp.Or(u.Name, u.Login)}}, nil
}

// BeginAuth runs the device flow and saves the token.
func (a *Adapter) BeginAuth(ctx context.Context, _ string, steps sdk.StepSink) error {
	switch {
	case a.login == nil:
		return sdk.Permanent("Log in works only in Trebi. Use --token or GITHUB_TOKEN.")
	case a.clientID == "":
		return sdk.Permanent("This build has no GitHub login. Add a personal access token to the connection.")
	}
	gh := a.gh()
	dc, err := gh.StartDevice(ctx, a.clientID, Scope)
	if err != nil {
		return fmt.Errorf("start the GitHub login: %w", err)
	}
	expires := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	if err := steps.Step(sdk.Step{
		Kind: sdk.StepDeviceCode, URL: dc.VerificationURI, Code: dc.UserCode,
		Message: "Open the page and type the code.", ExpiresAt: expires.UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	wait := max(time.Duration(dc.Interval)*time.Second, time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if time.Now().After(expires) {
			return errors.New("the code expired: log in again")
		}
		token, err := gh.PollDevice(ctx, a.clientID, dc.DeviceCode)
		if errors.Is(err, client.ErrPending) {
			continue
		}
		if err != nil {
			return fmt.Errorf("GitHub login: %w", err)
		}
		if err := a.login.Save(token); err != nil {
			return err
		}
		a.mu.Lock()
		a.api.Token, a.self, a.input = token, nil, false
		emit := a.emit
		a.mu.Unlock()
		if emit != nil {
			_ = emit.SubscriptionsChanged() //nolint:errcheck // the daemon also syncs on the next status
		}
		a.poke()
		return nil
	}
}

// SubmitAuth has no use: the device flow has no input step.
func (a *Adapter) SubmitAuth(context.Context, string, map[string]string) error {
	return sdk.Invalid("the GitHub login has no input step")
}

// Logout removes the saved token.
func (a *Adapter) Logout(context.Context) error {
	a.mu.Lock()
	input := a.input
	a.mu.Unlock()
	if input {
		return sdk.Permanent("The token comes from the connection settings. Remove it there.")
	}
	if a.login != nil {
		if err := a.login.Clear(); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.api.Token, a.self = "", nil
	a.mu.Unlock()
	return nil
}

// Options lists the repositories of the account. With a hook, only the
// repositories where the account is an admin can get a webhook.
func (a *Adapter) Options(ctx context.Context, q sdk.OptionQuery) (sdk.OptionPage, error) {
	if q.Field != FieldRepository {
		return sdk.OptionPage{}, sdk.Invalid("unknown field " + q.Field)
	}
	gh := a.gh()
	if gh.Token == "" {
		return sdk.OptionPage{}, sdk.AuthRequired("Log in to GitHub first.")
	}
	a.mu.Lock()
	admin := a.hook != nil
	a.mu.Unlock()
	page := 1
	if q.Cursor != "" {
		if _, err := fmt.Sscanf(q.Cursor, "%d", &page); err != nil || page < 1 {
			return sdk.OptionPage{}, sdk.Invalid("bad cursor")
		}
	}
	needle := strings.ToLower(q.Query)
	out := sdk.OptionPage{Options: []sdk.FieldOption{}}
	for {
		repos, more, err := gh.Repos(ctx, page)
		if err != nil {
			return sdk.OptionPage{}, a.apiErr(err)
		}
		for _, r := range repos {
			if (admin && !r.Permissions.Admin) || !strings.Contains(strings.ToLower(r.FullName), needle) {
				continue
			}
			o := sdk.FieldOption{Value: r.FullName, Label: r.FullName, Description: r.Description}
			if r.Private && o.Description == "" {
				o.Description = "Private"
			}
			out.Options = append(out.Options, o)
		}
		if !more {
			return out, nil
		}
		page++
		if len(out.Options) >= q.Limit {
			out.Next = fmt.Sprint(page)
			return out, nil
		}
	}
}

// Sync makes GitHub match the list: one hook for each watched repository
// with a hook, or a poll without one.
func (a *Adapter) Sync(ctx context.Context, p sdk.SyncParams) (sdk.SyncResult, error) {
	a.mu.Lock()
	a.subs = slices.Clone(p.Subscriptions)
	hook := a.hook
	a.mu.Unlock()
	want := map[string][]string{} // repo → GitHub events
	for _, s := range p.Subscriptions {
		if repo := s.Value(FieldRepository); repo != "" {
			want[repo] = union(want[repo], typesOf(s))
		}
	}
	failed := map[string]string{}
	authLost := false
	if a.gh().Token == "" {
		authLost = true
	} else {
		failed, authLost = a.reconcile(ctx, hook, want)
	}
	if authLost {
		if e := sdk.EmitterFrom(ctx); e != nil {
			_ = e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}) //nolint:errcheck // the next sync reports it again
		}
	}
	res := sdk.SyncResult{Subscriptions: make([]sdk.SubscriptionState, 0, len(p.Subscriptions))}
	for _, s := range p.Subscriptions {
		repo := s.Value(FieldRepository)
		st := sdk.SubscriptionState{ID: s.ID, Title: repo, Mode: sdk.ModeAPI, State: sdk.SubscriptionActive}
		if hook == nil {
			st.Mode, st.State = sdk.ModePoll, sdk.SubscriptionPolling
		}
		switch {
		case repo == "":
			st.State, st.Message = sdk.SubscriptionError, "Choose a repository."
		case authLost:
			st.State, st.Message = sdk.SubscriptionError, "Log in to GitHub again."
		case failed[repo] != "":
			st.State, st.Message = sdk.SubscriptionError, failed[repo]
		}
		if repo != "" {
			st.Room = &sdk.Room{ID: repo, Name: repo, Kind: sdk.RoomChannel}
		}
		res.Subscriptions = append(res.Subscriptions, st)
	}
	a.poke()
	return res, nil
}

// reconcile deletes the hooks that no subscription uses, then makes one
// hook for each repository. It returns a message for each repository that
// failed, and whether GitHub rejected the token.
func (a *Adapter) reconcile(ctx context.Context, hook *sdk.Webhook, want map[string][]string) (map[string]string, bool) {
	gh := a.gh()
	failed := map[string]string{}
	a.mu.Lock()
	state := hookState{Hooks: map[string]hookRec{}, Old: slices.Clone(a.state.Old)}
	for k, v := range a.state.Hooks {
		state.Hooks[k] = v
	}
	a.mu.Unlock()
	for repo, rec := range state.Hooks {
		if _, ok := want[repo]; ok && hook != nil && rec.URL == hook.URL {
			continue
		}
		err := gh.DeleteHook(ctx, repo, rec.ID)
		switch client.StatusOf(err) {
		case http.StatusUnauthorized:
			return failed, true
		case 0, http.StatusForbidden:
			if err != nil {
				a.log.Warn("delete hook", "repo", repo, "err", err)
			}
		}
		delete(state.Hooks, repo)
	}
	if hook != nil {
		for _, repo := range sortedKeys(want) {
			rec, err := a.ensureHook(ctx, gh, hook, repo, want[repo])
			switch client.StatusOf(err) {
			case 0:
			case http.StatusUnauthorized:
				return failed, true
			case http.StatusForbidden, http.StatusNotFound:
				failed[repo] = "Trebi needs admin access to " + repo + "."
				continue
			default:
				failed[repo] = err.Error()
				continue
			}
			if err != nil {
				failed[repo] = err.Error()
				continue
			}
			if old := state.Hooks[repo].Secret; old != "" && old != rec.Secret && !slices.Contains(state.Old, old) {
				state.Old = append(state.Old, old)
				state.Old = state.Old[max(0, len(state.Old)-3):]
			}
			state.Hooks[repo] = rec
		}
	}
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
	if err := a.saveState(state); err != nil {
		a.log.Warn("save hooks", "err", err)
	}
	return failed, false
}

// ensureHook finds the hook of the URL, then creates or updates it.
func (a *Adapter) ensureHook(ctx context.Context, gh *client.Client, hook *sdk.Webhook, repo string, events []string) (hookRec, error) {
	hooks, err := gh.Hooks(ctx, repo)
	if err != nil {
		return hookRec{}, err
	}
	a.mu.Lock()
	prev := a.state.Hooks[repo]
	a.mu.Unlock()
	body := client.Hook{Active: true, Events: events, Config: client.HookConfig{URL: hook.URL, ContentType: "json", Secret: hook.Secret, InsecureSSL: "0"}}
	for _, h := range hooks {
		if h.Config.URL != hook.URL {
			continue
		}
		if h.Active && sameSet(h.Events, events) && prev.ID == h.ID && prev.Secret == hook.Secret {
			return prev, nil
		}
		if _, err := gh.UpdateHook(ctx, repo, h.ID, body); err != nil {
			return hookRec{}, err
		}
		return hookRec{ID: h.ID, URL: hook.URL, Secret: hook.Secret, Events: events}, nil
	}
	h, err := gh.CreateHook(ctx, repo, body)
	if err != nil {
		return hookRec{}, err
	}
	return hookRec{ID: h.ID, URL: hook.URL, Secret: hook.Secret, Events: events}, nil
}

func (a *Adapter) saveState(st hookState) error {
	if a.stateDir == "" {
		return nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	path := filepath.Join(a.stateDir, hooksFile)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// SubmitSubscription has no use: no subscription asks a person.
func (a *Adapter) SubmitSubscription(context.Context, sdk.SubmitParams) (sdk.SubscriptionState, error) {
	return sdk.SubscriptionState{}, sdk.Invalid("a GitHub subscription asks for nothing")
}

// ReceiveWebhook checks the signature and sends the event. A ping and an
// event of a repository that no one watches give no event.
func (a *Adapter) ReceiveWebhook(ctx context.Context, req sdk.WebhookRequest) error {
	a.mu.Lock()
	var secrets []string
	if a.hook != nil {
		secrets = append(secrets, a.hook.Secret)
	}
	for _, rec := range a.state.Hooks {
		secrets = append(secrets, rec.Secret)
	}
	secrets = append(secrets, a.state.Old...)
	subs, self := a.subs, a.self
	a.mu.Unlock()
	ok := false
	for _, s := range secrets {
		if s != "" && req.VerifyHMAC([]byte(s), "x-hub-signature-256", "sha256=", sha256.New, sdk.EncodingHex) {
			ok = true
			break
		}
	}
	if !ok {
		return sdk.Invalid("the signature does not match")
	}
	typ := req.Header("x-github-event")
	if typ == "ping" || !slices.ContainsFunc(Events, func(d sdk.EventDecl) bool { return d.Type == typ }) {
		return nil
	}
	var p payload
	if err := json.Unmarshal([]byte(req.Body), &p); err != nil {
		return sdk.Invalid("the body is not JSON: " + err.Error())
	}
	repo := ""
	if p.Repository != nil {
		repo = p.Repository.FullName
	}
	if subs != nil && !watched(subs, repo, typ) {
		return nil
	}
	id := cmp.Or(req.Header("x-github-delivery"), req.ID)
	var sender client.User
	if p.Sender != nil {
		sender = *p.Sender
	}
	ev := toEvent(id, typ, cmp.Or(req.ReceivedAt, time.Now().UTC().Format(time.RFC3339)), repo, sender, self, p, json.RawMessage(req.Body))
	e := sdk.EmitterFrom(ctx)
	if e == nil {
		return sdk.Permanent("no session")
	}
	return e.Event(ev)
}

// Run polls the watched repositories when the connection has no hook.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	a.emit = e
	a.mu.Unlock()
	every := a.every
	for {
		if next := a.pollAll(ctx, e); next > every {
			every = next
		}
		select {
		case <-ctx.Done():
			return nil
		case <-a.wake:
		case <-time.After(every):
		}
	}
}

func (a *Adapter) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// pollAll reads the events of each watched repository once. It returns the
// longest X-Poll-Interval that GitHub asked for.
func (a *Adapter) pollAll(ctx context.Context, e sdk.Emitter) time.Duration {
	a.mu.Lock()
	hook, subs, self := a.hook, a.subs, a.self
	a.mu.Unlock()
	gh := a.gh()
	if hook != nil || gh.Token == "" {
		return 0
	}
	repos := map[string]bool{}
	for _, s := range subs {
		if r := s.Value(FieldRepository); r != "" {
			repos[r] = true
		}
	}
	var next time.Duration
	for _, repo := range sortedKeys(repos) {
		a.mu.Lock()
		cur := a.polls[repo]
		if cur == nil {
			cur = &pollCursor{}
			a.polls[repo] = cur
		}
		etag := cur.etag
		a.mu.Unlock()
		page, err := gh.Events(ctx, repo, etag)
		if client.StatusOf(err) == http.StatusUnauthorized {
			_ = e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}) //nolint:errcheck // the next poll reports it again
			return 0
		}
		if err != nil {
			a.log.Warn("poll events", "repo", repo, "err", err)
			continue
		}
		next = max(next, page.Interval)
		if page.NotModified {
			continue
		}
		a.mu.Lock()
		cur.etag = page.ETag
		last, primed := cur.lastID, cur.primed
		for _, ev := range page.Events {
			if newerID(ev.ID, cur.lastID) {
				cur.lastID = ev.ID
			}
		}
		cur.primed = true
		a.mu.Unlock()
		if !primed {
			continue // the first poll only finds the position
		}
		for i := len(page.Events) - 1; i >= 0; i-- {
			ev := page.Events[i]
			typ := pollTypes[ev.Type]
			if typ == "" || !newerID(ev.ID, last) || !watched(subs, repo, typ) {
				continue
			}
			var p payload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				continue
			}
			if err := e.Event(toEvent("poll:"+ev.ID, typ, ev.CreatedAt, repo, ev.Actor, self, p, ev.Payload)); err != nil {
				a.log.Warn("emit", "err", err)
			}
		}
	}
	return next
}

// apiErr maps a GitHub error to a protocol error.
func (a *Adapter) apiErr(err error) error {
	switch client.StatusOf(err) {
	case http.StatusUnauthorized:
		return sdk.AuthRequired("GitHub rejects the token. Log in again.")
	case http.StatusForbidden, http.StatusNotFound:
		return sdk.Permanent(err.Error())
	}
	return err
}

// pollTypes maps the events API names to the webhook names.
var pollTypes = map[string]string{
	"PushEvent":              "push",
	"PullRequestEvent":       "pull_request",
	"PullRequestReviewEvent": "pull_request_review",
	"IssuesEvent":            "issues",
	"IssueCommentEvent":      "issue_comment",
	"ReleaseEvent":           "release",
}

// typesOf returns the GitHub events of a subscription: its types, or all.
func typesOf(s sdk.Subscription) []string {
	var out []string
	for _, t := range s.Types {
		if slices.ContainsFunc(Events, func(d sdk.EventDecl) bool { return d.Type == t }) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		for _, d := range Events {
			out = append(out, d.Type)
		}
	}
	return out
}

// watched reports whether a subscription watches typ in repo.
func watched(subs []sdk.Subscription, repo, typ string) bool {
	for _, s := range subs {
		if s.Value(FieldRepository) == repo && slices.Contains(typesOf(s), typ) {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, v := range b {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// newerID compares two numeric event ids.
func newerID(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}
