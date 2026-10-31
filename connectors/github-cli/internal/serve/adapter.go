// Package serve is the trebi-connector/1 adapter of github-cli. A watched
// repository gets a webhook when the connection has a hosted hook and the
// account is an admin of it, and a poll of its events otherwise. The
// notifications of the account give the "your activity" events.
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "github-cli"

// Scope is the OAuth scope of the device login. read:org lets gh read the
// teams and the repositories of an org.
const Scope = "repo read:org admin:repo_hook"

// FieldRepository is the one field of the watch form.
const FieldRepository = "repository"

// Login methods. The manifest setup.login has the same ids and types.
const (
	LoginGH     = "gh"
	LoginDevice = "device"
)

// hooksFile keeps the hooks that the adapter made, so that it can delete
// them when the subscription goes.
const hooksFile = "github-hooks.json"

// minPoll is the shortest poll interval that GitHub allows.
const minPoll = 60 * time.Second

// maxRuns bounds the seen workflow runs of one repository.
const maxRuns = 200

// Repository events. Each is a GitHub webhook event name.
var repoEvents = []string{ //nolint:gochecknoglobals // fixed list, never changed
	"push", "pull_request", "pull_request_review", "pull_request_review_comment",
	"issues", "issue_comment", "release", "workflow_run", "check_suite", "status",
	"create", "delete", "deployment_status", "discussion", "discussion_comment",
}

// Events are the event types: the repository events, then the activity
// events of the notifications.
var Events = func() []sdk.EventDecl { //nolint:gochecknoglobals // fixed list, never changed
	var out []sdk.EventDecl
	for _, t := range append(slices.Clone(repoEvents), activityTypes...) {
		out = append(out, sdk.EventDecl{Type: t})
	}
	return out
}()

// TokenSource reads the gh login of the user.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Adapter serves one GitHub account.
type Adapter struct {
	version  string
	clientID string
	login    *config.LoginStore // nil outside Trebi
	ghLogin  TokenSource        // the gh login on this machine; nil outside Trebi
	stateDir string
	every    time.Duration
	log      *slog.Logger
	wake     chan struct{}
	activity *ActivityReader

	mu       sync.Mutex
	api      client.Client
	source   string // a config.Source* value
	emit     sdk.Emitter
	hook     *sdk.Webhook
	id       *client.Identity
	subs     []sdk.Subscription
	state    hookState
	pollRepo map[string]bool // the watched repositories in poll mode
	polls    map[string]*pollCursor
	flows    map[string]chan map[string]sdk.Value
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

// pollCursor is the poll position of one repository: the events API, and
// the Actions runs with the last status of each.
type pollCursor struct {
	etag   string
	lastID string
	primed bool

	runETag    string
	runs       map[int64]string
	runOrder   []int64
	runsPrimed bool
}

// Option changes the adapter.
type Option func(*Adapter)

// WithPollEvery replaces the poll interval (tests).
func WithPollEvery(d time.Duration) Option { return func(a *Adapter) { a.every = d } }

// WithLogin turns on the logins. clientID is the OAuth app of the device
// login; an empty one makes it unavailable. gh reads the gh login on this
// machine; nil makes it unavailable.
func WithLogin(store *config.LoginStore, clientID string, gh TokenSource) Option {
	return func(a *Adapter) { a.login, a.clientID, a.ghLogin = store, clientID, gh }
}

// WithSource tells where the token came from: a config.Source* value.
func WithSource(source string) Option { return func(a *Adapter) { a.source = source } }

// New builds the adapter. stateDir keeps the hooks and the activity
// position; empty keeps them in memory.
func New(api *client.Client, version, stateDir string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		version: version, stateDir: stateDir, every: minPoll,
		log:      slog.Default().With("source", "github"),
		wake:     make(chan struct{}, 1),
		api:      *api,
		state:    hookState{Hooks: map[string]hookRec{}},
		pollRepo: map[string]bool{},
		polls:    map[string]*pollCursor{},
		flows:    map[string]chan map[string]sdk.Value{},
	}
	for _, o := range opts {
		o(a)
	}
	a.activity = NewActivityReader(a, stateDir)
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

func (a *Adapter) identity() *client.Identity {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.id
}

// refresh reads the gh login again after a 401 of the token failed, or at
// a start with no token. It reports whether there is now a token that is
// not failed. A gh that logged out clears the token.
func (a *Adapter) refresh(ctx context.Context, failed string) bool {
	a.mu.Lock()
	src, gh, old := a.source, a.ghLogin, a.api.Token
	a.mu.Unlock()
	if src != config.SourceGH || gh == nil {
		return false
	}
	token, err := gh.Token(ctx)
	switch {
	case errors.Is(err, config.ErrGHSlow):
		return false
	case err != nil:
		token = ""
	}
	a.mu.Lock()
	a.api.Token = token
	if token != old {
		a.id = nil
	}
	a.mu.Unlock()
	return token != "" && token != failed
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
	}
	if st, err := a.AuthStatus(ctx); err == nil {
		res.Account = st.Account
	}
	return res, nil
}

// AuthStatus asks GitHub who the token is, and keeps its scopes.
func (a *Adapter) AuthStatus(ctx context.Context) (sdk.AuthState, error) {
	if a.gh().Token == "" {
		a.refresh(ctx, "")
	}
	gh := a.gh()
	if gh.Token == "" {
		a.mu.Lock()
		src := a.source
		a.mu.Unlock()
		if src == config.SourceGH {
			return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonLoggedOut}, nil
		}
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonNone}, nil
	}
	id, err := gh.Whoami(ctx)
	if client.StatusOf(err) == http.StatusUnauthorized && a.refresh(ctx, gh.Token) {
		id, err = a.gh().Whoami(ctx)
	}
	if client.StatusOf(err) == http.StatusUnauthorized {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}, nil
	}
	if err != nil {
		return sdk.AuthState{}, err
	}
	a.mu.Lock()
	a.id = &id
	a.mu.Unlock()
	return sdk.AuthState{State: sdk.StateConnected, Account: &sdk.Account{ID: id.Login, Name: cmp.Or(id.Name, id.Login)}}, nil
}

// LoginMethods has the gh login on this machine and the device flow.
// Outside Trebi neither is available.
func (a *Adapter) LoginMethods(ctx context.Context) ([]sdk.LoginMethod, error) {
	gh := sdk.LoginMethod{ID: LoginGH, Type: sdk.LoginExisting, Label: "Use the GitHub login on this machine"}
	dev := sdk.LoginMethod{ID: LoginDevice, Type: sdk.LoginBrowser, Label: "Sign in with GitHub", Available: true}
	if a.login == nil {
		const why = "Log in works only in Trebi. Use --token or GITHUB_TOKEN."
		gh.Reason, dev.Available, dev.Reason = why, false, why
		return []sdk.LoginMethod{gh, dev}, nil
	}
	gh.Reason = config.ErrGHLoggedOut.Error() + "."
	if a.ghLogin != nil {
		if _, err := a.ghLogin.Token(ctx); err == nil {
			gh.Available, gh.Reason = true, ""
		}
	}
	if a.clientID == "" {
		dev.Available, dev.Reason = false, "This build has no GitHub login. Paste a token."
	}
	return []sdk.LoginMethod{gh, dev}, nil
}

// BeginAuth runs one login method and saves the login. The SDK starts it
// only when LoginMethods says it is available.
func (a *Adapter) BeginAuth(ctx context.Context, method string, steps sdk.StepSink) error {
	switch method {
	case LoginGH:
		return a.beginGH(ctx, steps)
	case LoginDevice:
		return a.beginDevice(ctx, steps)
	}
	return sdk.Invalid("unknown login method " + strconv.Quote(method))
}

// beginGH shows the account of the gh login and waits for the person to
// pick it. auth.json keeps only the source: the token stays in gh.
func (a *Adapter) beginGH(ctx context.Context, steps sdk.StepSink) error {
	if a.ghLogin == nil {
		return sdk.Invalid(config.ErrGHLoggedOut.Error())
	}
	token, err := a.ghLogin.Token(ctx)
	if err != nil {
		return sdk.Invalid(err.Error())
	}
	c := a.gh()
	c.Token = token
	id, err := c.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("read the GitHub account: %w", err)
	}
	in := make(chan map[string]sdk.Value, 1)
	a.mu.Lock()
	a.flows[steps.FlowID()] = in
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.flows, steps.FlowID())
		a.mu.Unlock()
	}()
	desc := "github.com"
	if id.Name != "" {
		desc = id.Name + ", github.com"
	}
	if err := steps.Step(sdk.Step{Kind: sdk.StepInput, Message: "Trebi uses the GitHub login on this machine. It does not copy the token.", Fields: []sdk.StepField{{
		Name: "account", Label: "Account", Choices: []sdk.Choice{{Value: id.Login, Label: id.Login, Description: desc}}, Default: sdk.Value{id.Login},
	}}}); err != nil {
		return err
	}
	var fields map[string]sdk.Value
	select {
	case fields = <-in:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !slices.Contains(fields["account"], id.Login) {
		return errors.New("pick the account to log in")
	}
	if err := a.login.Save(config.LoginRecord{Source: config.SourceGH}); err != nil {
		return err
	}
	a.loggedIn(token, config.SourceGH, &id)
	return nil
}

// beginDevice runs the device flow and saves the token.
func (a *Adapter) beginDevice(ctx context.Context, steps sdk.StepSink) error {
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
		if errors.Is(err, client.ErrSlowDown) {
			wait += 5 * time.Second
			continue
		}
		if errors.Is(err, client.ErrPending) {
			continue
		}
		if err != nil {
			return fmt.Errorf("GitHub login: %w", err)
		}
		if err := a.login.Save(config.LoginRecord{Source: config.SourceDevice, Token: token}); err != nil {
			return err
		}
		a.loggedIn(token, config.SourceDevice, nil)
		return nil
	}
}

// loggedIn uses the new token, and starts the activity read again.
func (a *Adapter) loggedIn(token, source string, id *client.Identity) {
	a.mu.Lock()
	a.api.Token, a.source, a.id = token, source, id
	emit := a.emit
	a.mu.Unlock()
	a.activity.Reset()
	if emit != nil {
		_ = emit.SubscriptionsChanged() //nolint:errcheck // the daemon also syncs on the next status
	}
	a.poke()
}

// SubmitAuth answers the account pick of the gh login.
func (a *Adapter) SubmitAuth(_ context.Context, flowID string, fields map[string]sdk.Value) error {
	a.mu.Lock()
	in := a.flows[flowID]
	a.mu.Unlock()
	if in == nil {
		return sdk.Invalid("this login step has no input")
	}
	select {
	case in <- fields:
		return nil
	default:
		return sdk.Invalid("the login has the answer already")
	}
}

// Logout removes the saved login. It never logs out the gh of the user.
func (a *Adapter) Logout(context.Context) error {
	a.mu.Lock()
	src := a.source
	a.mu.Unlock()
	if src == config.SourceInput {
		return sdk.Permanent("The token comes from the connection settings. Remove it there.")
	}
	if a.login != nil {
		if err := a.login.Clear(); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.api.Token, a.id, a.source = "", nil, ""
	a.mu.Unlock()
	return nil
}

// Options lists the repositories that the account can read.
func (a *Adapter) Options(ctx context.Context, q sdk.OptionQuery) (sdk.OptionPage, error) {
	if q.Field != FieldRepository {
		return sdk.OptionPage{}, sdk.Invalid("unknown field " + q.Field)
	}
	gh := a.gh()
	if gh.Token == "" {
		return sdk.OptionPage{}, sdk.AuthRequired("Log in to GitHub first.")
	}
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
			if !strings.Contains(strings.ToLower(r.FullName), needle) {
				continue
			}
			desc := "Read only"
			if r.Permissions.Admin {
				desc = "Admin"
			}
			if r.Private {
				desc += ", private"
			}
			out.Options = append(out.Options, sdk.FieldOption{Value: r.FullName, Label: r.FullName, Description: desc})
		}
		if !more {
			return out, nil
		}
		page++
		if len(out.Options) >= q.Limit {
			out.Next = strconv.Itoa(page)
			return out, nil
		}
	}
}

// repoMode is the plan for one watched repository: a webhook (api), or a
// poll with the reason in msg. fail is an error message.
type repoMode struct {
	api  bool
	msg  string
	fail string
}

// Sync makes GitHub match the list: one hook for each watched repository
// that can have one, and a poll for the others.
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
	modes := map[string]repoMode{}
	authLost := a.gh().Token == ""
	if !authLost {
		modes, authLost = a.plan(ctx, hook, want)
	}
	if !authLost {
		authLost = a.reconcile(ctx, hook, want, modes)
	}
	if authLost {
		if e := sdk.EmitterFrom(ctx); e != nil {
			_ = e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}) //nolint:errcheck // the next sync reports it again
		}
	}
	polled := map[string]bool{}
	res := sdk.SyncResult{Subscriptions: make([]sdk.SubscriptionState, 0, len(p.Subscriptions))}
	for _, s := range p.Subscriptions {
		repo := s.Value(FieldRepository)
		m := modes[repo]
		st := sdk.SubscriptionState{ID: s.ID, Title: repo, Mode: sdk.ModeAPI, State: sdk.SubscriptionActive}
		if !m.api {
			st.Mode, st.State, st.Message = sdk.ModePoll, sdk.SubscriptionPolling, m.msg
		}
		switch {
		case repo == "":
			st.State, st.Message = sdk.SubscriptionError, "Choose a repository."
		case authLost:
			st.State, st.Message = sdk.SubscriptionError, "Log in to GitHub again."
		case m.fail != "":
			st.State, st.Message = sdk.SubscriptionError, m.fail
		case !m.api:
			polled[repo] = true
		}
		if repo != "" {
			st.Room = &sdk.Room{ID: repo, Name: repo, Kind: sdk.RoomChannel}
		}
		res.Subscriptions = append(res.Subscriptions, st)
	}
	a.mu.Lock()
	a.pollRepo = polled
	a.mu.Unlock()
	a.poke()
	return res, nil
}

// plan picks the mode of each repository. A webhook needs the hosted hook,
// a login that can manage hooks, and admin access to the repository. It
// reports whether GitHub rejected the token.
func (a *Adapter) plan(ctx context.Context, hook *sdk.Webhook, want map[string][]string) (map[string]repoMode, bool) {
	modes := map[string]repoMode{}
	id := a.identity()
	noScope := id != nil && id.Scoped && !id.Has("repo", "admin:repo_hook", "write:repo_hook")
	gh := a.gh()
	for _, repo := range sortedKeys(want) {
		r, err := gh.Repo(ctx, repo)
		switch client.StatusOf(err) {
		case 0:
		case http.StatusUnauthorized:
			return modes, true
		case http.StatusNotFound, http.StatusForbidden:
			modes[repo] = repoMode{fail: "Trebi cannot find " + repo + ", or this login cannot read it."}
			continue
		}
		switch {
		case err != nil:
			modes[repo] = repoMode{fail: err.Error()}
		case hook == nil:
			modes[repo] = repoMode{}
		case noScope:
			modes[repo] = repoMode{msg: "This login cannot add webhooks, so Trebi checks " + repo + " every minute."}
		case !r.Permissions.Admin:
			modes[repo] = repoMode{msg: "You are not an admin of " + repo + ", so Trebi checks it every minute."}
		default:
			modes[repo] = repoMode{api: true}
		}
	}
	return modes, false
}

// reconcile deletes the hooks that no api repository uses, then makes one
// hook for each api repository. A repository whose hook GitHub refuses
// moves to poll. It reports whether GitHub rejected the token.
func (a *Adapter) reconcile(ctx context.Context, hook *sdk.Webhook, want map[string][]string, modes map[string]repoMode) bool {
	gh := a.gh()
	a.mu.Lock()
	state := hookState{Hooks: map[string]hookRec{}, Old: slices.Clone(a.state.Old)}
	for k, v := range a.state.Hooks {
		state.Hooks[k] = v
	}
	a.mu.Unlock()
	for repo, rec := range state.Hooks {
		if modes[repo].api && hook != nil && rec.URL == hook.URL {
			continue
		}
		err := gh.DeleteHook(ctx, repo, rec.ID)
		switch client.StatusOf(err) {
		case http.StatusUnauthorized:
			return true
		case 0:
			if err != nil {
				// No answer from GitHub: keep the record, so the next sync tries again.
				a.log.Warn("delete hook", "repo", repo, "err", err)
				continue
			}
		case http.StatusForbidden:
			a.log.Warn("delete hook", "repo", repo, "err", err)
		}
		delete(state.Hooks, repo)
	}
	for _, repo := range sortedKeys(want) {
		if !modes[repo].api {
			continue
		}
		rec, err := a.ensureHook(ctx, gh, hook, repo, want[repo])
		switch client.StatusOf(err) {
		case 0:
		case http.StatusUnauthorized:
			return true
		case http.StatusForbidden, http.StatusNotFound:
			modes[repo] = repoMode{msg: "This login cannot add webhooks to " + repo + ", so Trebi checks it every minute."}
			continue
		}
		if err != nil {
			modes[repo] = repoMode{fail: err.Error()}
			continue
		}
		if old := state.Hooks[repo].Secret; old != "" && old != rec.Secret && !slices.Contains(state.Old, old) {
			state.Old = append(state.Old, old)
			state.Old = state.Old[max(0, len(state.Old)-3):]
		}
		state.Hooks[repo] = rec
	}
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
	if err := a.saveState(state); err != nil {
		a.log.Warn("save hooks", "err", err)
	}
	return false
}

// ensureHook finds the hook of the URL, then creates or updates it. A hook
// of an older version with other events gets the new list.
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
	return sdk.WriteFileAtomic(filepath.Join(a.stateDir, hooksFile), data, 0o600)
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
	subs, id := a.subs, a.id
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
	if typ == "ping" || !slices.Contains(repoEvents, typ) {
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
	var sender client.User
	if p.Sender != nil {
		sender = *p.Sender
	}
	ev := toEvent(cmp.Or(req.Header("x-github-delivery"), req.ID), typ, cmp.Or(req.ReceivedAt, time.Now().UTC().Format(time.RFC3339)), repo, sender, selfOf(id), p, json.RawMessage(req.Body))
	e := sdk.EmitterFrom(ctx)
	if e == nil {
		return sdk.Permanent("no session")
	}
	return e.Event(ev)
}

// Run polls the repositories in poll mode and reads the activity of the
// account until ctx ends.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	a.emit = e
	a.mu.Unlock()
	every := a.every
	var activityDue time.Time
	for {
		if next := a.pollAll(ctx, e); next > every {
			every = next
		}
		if now := time.Now(); !now.Before(activityDue) {
			activityDue = now.Add(max(a.activity.Read(ctx, e), a.every))
		}
		wait := max(min(every, time.Until(activityDue)), a.every)
		select {
		case <-ctx.Done():
			return nil
		case <-a.wake:
		case <-time.After(wait):
		}
	}
}

func (a *Adapter) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// tokenLost handles a 401: it reads the gh login again, and else reports
// auth_required. It reports whether the token is gone.
func (a *Adapter) tokenLost(ctx context.Context, e sdk.Emitter, failed string) bool {
	if a.refresh(ctx, failed) {
		return false
	}
	_ = e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}) //nolint:errcheck // the next poll reports it again
	return true
}

// pollAll reads the events and the Actions runs of each repository in
// poll mode once. It returns the longest X-Poll-Interval that GitHub asked
// for.
func (a *Adapter) pollAll(ctx context.Context, e sdk.Emitter) time.Duration {
	a.mu.Lock()
	subs, self := a.subs, selfOf(a.id)
	repos := sortedKeys(a.pollRepo)
	a.mu.Unlock()
	gh := a.gh()
	if gh.Token == "" {
		return 0
	}
	var next time.Duration
	for _, repo := range repos {
		a.mu.Lock()
		cur := a.polls[repo]
		if cur == nil {
			cur = &pollCursor{runs: map[int64]string{}}
			a.polls[repo] = cur
		}
		etag := cur.etag
		a.mu.Unlock()
		page, err := gh.Events(ctx, repo, etag)
		if client.StatusOf(err) == http.StatusUnauthorized {
			a.tokenLost(ctx, e, gh.Token)
			return 0
		}
		if err != nil {
			a.log.Warn("poll events", "repo", repo, "err", err)
			continue
		}
		next = max(next, page.Interval)
		if !page.NotModified {
			a.emitEvents(e, repo, cur, page, subs, self)
		}
		if watched(subs, repo, "workflow_run") {
			a.pollRuns(ctx, e, gh, repo, cur, self)
		}
	}
	return next
}

// emitEvents sends the new events of one events API page. The first page
// only finds the position.
func (a *Adapter) emitEvents(e sdk.Emitter, repo string, cur *pollCursor, page client.EventPage, subs []sdk.Subscription, self *client.User) {
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
		return
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

// pollRuns sends a workflow_run event when a run appears and when it ends.
// The first read only finds the position.
func (a *Adapter) pollRuns(ctx context.Context, e sdk.Emitter, gh *client.Client, repo string, cur *pollCursor, self *client.User) {
	a.mu.Lock()
	etag := cur.runETag
	a.mu.Unlock()
	page, err := gh.Runs(ctx, repo, etag)
	if err != nil {
		if client.StatusOf(err) != http.StatusUnauthorized {
			a.log.Warn("poll runs", "repo", repo, "err", err)
		}
		return
	}
	if page.NotModified {
		return
	}
	var out []sdk.Event
	a.mu.Lock()
	cur.runETag = page.ETag
	for i := len(page.Runs) - 1; i >= 0; i-- {
		r := page.Runs[i]
		prev, seen := cur.runs[r.ID]
		if seen && prev == r.Status {
			continue
		}
		if !seen {
			cur.runOrder = append(cur.runOrder, r.ID)
		}
		cur.runs[r.ID] = r.Status
		if !cur.runsPrimed || (seen && r.Status != "completed") {
			continue
		}
		action := "requested"
		if r.Status == "completed" {
			action = "completed"
		}
		p := payload{Action: action, WorkflowRun: &workflowRun{ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, HeadBranch: r.HeadBranch, HTMLURL: r.HTMLURL}}
		raw, _ := json.Marshal(r) //nolint:errcheck // a struct of plain values
		out = append(out, toEvent(fmt.Sprintf("poll:run:%d:%s", r.ID, r.Status), "workflow_run", r.UpdatedAt, repo, r.Actor, self, p, raw))
	}
	cur.runsPrimed = true
	for len(cur.runOrder) > maxRuns {
		delete(cur.runs, cur.runOrder[0])
		cur.runOrder = cur.runOrder[1:]
	}
	a.mu.Unlock()
	for _, ev := range out {
		if err := e.Event(ev); err != nil {
			a.log.Warn("emit", "err", err)
		}
	}
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

// pollTypes maps the events API names to the webhook names. The other
// webhook types have no events API entry.
var pollTypes = map[string]string{ //nolint:gochecknoglobals // fixed map, never changed
	"PushEvent":                     "push",
	"PullRequestEvent":              "pull_request",
	"PullRequestReviewEvent":        "pull_request_review",
	"PullRequestReviewCommentEvent": "pull_request_review_comment",
	"IssuesEvent":                   "issues",
	"IssueCommentEvent":             "issue_comment",
	"ReleaseEvent":                  "release",
	"CreateEvent":                   "create",
	"DeleteEvent":                   "delete",
}

// typesOf returns the repository events of a subscription: its types, or
// all.
func typesOf(s sdk.Subscription) []string {
	var out []string
	for _, t := range s.Types {
		if slices.Contains(repoEvents, t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		out = slices.Clone(repoEvents)
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

func selfOf(id *client.Identity) *client.User {
	if id == nil {
		return nil
	}
	return &id.User
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
