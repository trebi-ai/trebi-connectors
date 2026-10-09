// Package serve is the trebi-connector/1 adapter of notion-cli. It watches
// one workspace: through a Notion webhook when Trebi gives a hosted URL,
// else through a poll of the search API.
package serve

import (
	"bytes"
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

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "notion-cli"

// IntegrationsURL is the Notion page that manages integrations.
const IntegrationsURL = "https://www.notion.so/profile/integrations"

// SignatureHeader is the header of a signed Notion delivery.
const SignatureHeader = "x-notion-signature"

const (
	stateFile     = "notion-state.json"
	stateVersion  = 1
	pollEvery     = 5 * time.Minute
	pollPage      = 100
	roomWorkspace = "workspace"
)

// Events and Features are what the adapter declares.
var (
	Events   = []sdk.EventDecl{{Type: "page"}, {Type: "database"}}
	Features = []string{sdk.FeatureSubscriptions, sdk.FeatureWebhooks}
)

var (
	_ sdk.Runner                = (*Adapter)(nil)
	_ sdk.StatusReporter        = (*Adapter)(nil)
	_ sdk.FolderUser            = (*Adapter)(nil)
	_ sdk.Subscriber            = (*Adapter)(nil)
	_ sdk.SubscriptionSubmitter = (*Adapter)(nil)
	_ sdk.WebhookReceiver       = (*Adapter)(nil)
)

// state is the durable data in notion-state.json.
type state struct {
	Version           int      `json:"version"`
	HookURL           string   `json:"hook_url,omitempty"`
	VerificationToken string   `json:"verification_token,omitempty"`
	Verified          bool     `json:"verified,omitempty"`
	PollCursor        string   `json:"poll_cursor,omitempty"` // last_edited_time of the newest seen edit
	PollSeen          []string `json:"poll_seen,omitempty"`   // page ids at the cursor
}

// Adapter serves one integration secret.
type Adapter struct {
	api       *client.Client
	version   string
	stateDir  string
	interval  time.Duration
	subscribe string // fake-only subscribe URL in sandbox mode
	post      *http.Client

	mu         sync.Mutex
	st         state
	hook       *sdk.Webhook
	subscribed bool
	asked      bool // the sandbox fake has the hook URL
	self       *client.User
	revoked    bool
}

// Option changes the adapter.
type Option func(*Adapter)

// WithPollInterval replaces the poll interval (tests).
func WithPollInterval(d time.Duration) Option { return func(a *Adapter) { a.interval = d } }

// WithSandbox gives the fake-only URL that takes the hook URL. Only `serve
// --sandbox` sets it.
func WithSandbox(subscribeURL string) Option { return func(a *Adapter) { a.subscribe = subscribeURL } }

// New builds the adapter. stateDir keeps the webhook token and the poll
// cursor; empty keeps them in memory. A client with no token makes
// Initialize report the missing input.
func New(api *client.Client, version, stateDir string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		api: api, version: version, interval: pollEvery,
		post: &http.Client{Timeout: 10 * time.Second},
		st:   state{Version: stateVersion},
	}
	for _, o := range opts {
		o(a)
	}
	return a, a.UseFolders(sdk.Trebi{StateDir: stateDir})
}

// UseFolders reads the state of the folder. Old state is upgraded; state
// from a newer version is an error.
func (a *Adapter) UseFolders(t sdk.Trebi) error {
	if t.StateDir == "" || t.StateDir == a.stateDir {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stateDir = t.StateDir
	data, err := os.ReadFile(filepath.Join(t.StateDir, stateFile))
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
		return fmt.Errorf("state version %d is newer than this program (%d): update notion-cli", st.Version, stateVersion)
	}
	st.Version = stateVersion
	a.st = st
	return nil
}

// save writes the state. The caller holds mu.
func (a *Adapter) save() error {
	if a.stateDir == "" {
		return nil
	}
	data, err := json.MarshalIndent(a.st, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(a.stateDir, stateFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Initialize checks the secret and keeps the hosted hook. A new hook URL
// clears the webhook token: Notion must verify the new URL.
func (a *Adapter) Initialize(ctx context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{
		Adapter:  sdk.AdapterInfo{Name: Name, Version: a.version},
		Events:   Events,
		Features: Features,
	}
	a.mu.Lock()
	a.hook = in.Webhook
	if in.Webhook != nil && in.Webhook.URL != a.st.HookURL {
		a.st.HookURL = in.Webhook.URL
		a.st.VerificationToken, a.st.Verified = "", false
		if err := a.save(); err != nil {
			a.mu.Unlock()
			return res, err
		}
	}
	a.mu.Unlock()
	if !a.api.HasToken() {
		return res, sdk.MissingInput{Name: config.EnvToken, Label: config.TokenLabel}
	}
	me, err := a.api.Me(ctx)
	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		a.mu.Lock()
		a.revoked = true
		a.mu.Unlock()
		return res, nil
	case err != nil:
		return res, a.wrap(err)
	}
	a.mu.Lock()
	a.self = &me
	a.mu.Unlock()
	res.Account = &sdk.Account{ID: me.ID, Name: me.Name}
	return res, nil
}

// AuthStatus is connected, or auth_required when Notion rejects the secret.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked || a.self == nil {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}, nil
	}
	return sdk.AuthState{State: sdk.StateConnected, Account: &sdk.Account{ID: a.self.ID, Name: a.self.Name}}, nil
}

// Options is empty: the subscription has no fields.
func (a *Adapter) Options(context.Context, sdk.OptionQuery) (sdk.OptionPage, error) {
	return sdk.OptionPage{Options: []sdk.FieldOption{}}, nil
}

// Sync gives each subscription the state of the one workspace watch. An
// empty list stops the poll and clears the webhook token, so a person can
// connect Notion again from the start.
func (a *Adapter) Sync(ctx context.Context, p sdk.SyncParams) (sdk.SyncResult, error) {
	a.mu.Lock()
	a.subscribed = len(p.Subscriptions) > 0
	if !a.subscribed {
		if a.st.VerificationToken != "" || a.st.Verified {
			a.st.VerificationToken, a.st.Verified = "", false
			if err := a.save(); err != nil {
				a.mu.Unlock()
				return sdk.SyncResult{}, err
			}
		}
		a.mu.Unlock()
		return sdk.SyncResult{Subscriptions: []sdk.SubscriptionState{}}, nil
	}
	if a.hook == nil && a.st.PollCursor == "" {
		a.st.PollCursor = notionTime(time.Now())
		if err := a.save(); err != nil {
			a.mu.Unlock()
			return sdk.SyncResult{}, err
		}
	}
	ask := a.hook != nil && a.st.VerificationToken == "" && a.subscribe != "" && !a.asked
	hookURL := ""
	if ask {
		a.asked = true
		hookURL = a.hook.URL
	}
	a.mu.Unlock()
	if ask {
		if err := a.askSandbox(ctx, hookURL); err != nil {
			slog.Warn("sandbox subscribe", "err", err)
		}
	}
	out := make([]sdk.SubscriptionState, len(p.Subscriptions))
	for i, s := range p.Subscriptions {
		out[i] = a.current(s.ID)
	}
	return sdk.SyncResult{Subscriptions: out}, nil
}

// SubmitSubscription takes no answers. It returns the current state.
func (a *Adapter) SubmitSubscription(_ context.Context, p sdk.SubmitParams) (sdk.SubscriptionState, error) {
	return a.current(p.ID), nil
}

// current is the state of one subscription now.
func (a *Adapter) current(id string) sdk.SubscriptionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := sdk.SubscriptionState{ID: id, Title: "Notion workspace", Room: &sdk.Room{ID: roomWorkspace, Name: "Workspace"}}
	switch {
	case a.hook == nil:
		s.Mode, s.State = sdk.ModePoll, sdk.SubscriptionPolling
		s.Message = "Trebi checks Notion for changes every few minutes."
	case a.st.Verified:
		s.Mode, s.State = sdk.ModeManual, sdk.SubscriptionActive
	case a.st.VerificationToken == "":
		s.Mode, s.State = sdk.ModeManual, sdk.SubscriptionActionRequired
		s.Action = &sdk.Action{
			Text: "Open the integration settings in Notion. Open the Webhooks tab. Add a subscription with this URL, and select the events to watch.",
			URL:  IntegrationsURL,
			Show: []sdk.ShowValue{{Label: "URL", Value: a.hook.URL}},
		}
	default:
		s.Mode, s.State = sdk.ModeManual, sdk.SubscriptionActionRequired
		s.Action = &sdk.Action{
			Text: "Paste this code into Notion to finish.",
			URL:  IntegrationsURL,
			Show: []sdk.ShowValue{
				{Label: "URL", Value: a.hook.URL},
				{Label: "Code", Value: a.st.VerificationToken, Secret: true},
			},
		}
	}
	return s
}

// askSandbox gives the hook URL to the fake Notion, which then posts the
// verification and one signed event to it.
func (a *Adapter) askSandbox(ctx context.Context, hookURL string) error {
	body, err := json.Marshal(map[string]string{"url": hookURL})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.subscribe, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.post.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close() //nolint:errcheck,gosec // the status is enough
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sandbox subscribe: %s", resp.Status)
	}
	return nil
}

// delivery is one Notion webhook body: the verification request or an
// event.
type delivery struct {
	VerificationToken string `json:"verification_token"`
	ID                string `json:"id"`
	Timestamp         string `json:"timestamp"`
	Type              string `json:"type"`
	Authors           []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"authors"`
	Entity struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"entity"`
	Data struct {
		Parent struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"parent"`
	} `json:"data"`
}

// ReceiveWebhook handles one Notion delivery. The verification request
// stores the token; an event must carry a signature made with it.
func (a *Adapter) ReceiveWebhook(ctx context.Context, req sdk.WebhookRequest) error {
	var d delivery
	if err := json.Unmarshal([]byte(req.Body), &d); err != nil {
		return sdk.Invalid("the body is not a Notion delivery: " + err.Error())
	}
	if d.VerificationToken != "" {
		return a.verify(ctx, d.VerificationToken)
	}
	a.mu.Lock()
	token := a.st.VerificationToken
	a.mu.Unlock()
	if token == "" {
		return sdk.Invalid("no webhook token yet: Notion must send the verification request first")
	}
	if !req.VerifyHMAC([]byte(token), SignatureHeader, "sha256=", sha256.New, sdk.EncodingHex) {
		return sdk.Invalid("bad " + SignatureHeader)
	}
	if err := a.markVerified(ctx); err != nil {
		return err
	}
	if d.Entity.Type != "page" && d.Entity.Type != "database" {
		return nil
	}
	ev, err := a.webhookEvent(ctx, d)
	if err != nil {
		return err
	}
	return sdk.EmitterFrom(ctx).Event(ev)
}

// verify stores the token of the verification request. The same token
// again keeps the hook verified. Another token after the verification is
// refused: only a new hook URL or an empty sync starts again.
func (a *Adapter) verify(ctx context.Context, token string) error {
	a.mu.Lock()
	switch {
	case token == a.st.VerificationToken:
		a.mu.Unlock()
		return nil
	case a.st.Verified:
		a.mu.Unlock()
		return sdk.Invalid("the webhook is verified already")
	}
	a.st.VerificationToken = token
	err := a.save()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return sdk.EmitterFrom(ctx).SubscriptionsChanged()
}

// markVerified makes the hook active at the first signed delivery.
func (a *Adapter) markVerified(ctx context.Context) error {
	a.mu.Lock()
	if a.st.Verified {
		a.mu.Unlock()
		return nil
	}
	a.st.Verified = true
	err := a.save()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return sdk.EmitterFrom(ctx).SubscriptionsChanged()
}

// webhookEvent reads the entity and builds the event. A deleted or a
// missing entity gives an event with the ids only.
func (a *Adapter) webhookEvent(ctx context.Context, d delivery) (sdk.Event, error) {
	_, change, _ := strings.Cut(d.Type, ".")
	ts := sdk.FormatTime(time.Now())
	if t, err := time.Parse(time.RFC3339Nano, d.Timestamp); err == nil {
		ts = sdk.FormatTime(t)
	}
	ev := sdk.Event{ID: d.ID, Type: d.Entity.Type, TS: ts, Room: &sdk.Room{ID: roomWorkspace, Name: "Workspace"}}
	if len(d.Authors) > 0 && d.Authors[0].ID != "" {
		ev.Sender = &sdk.Author{ID: d.Authors[0].ID, Bot: d.Authors[0].Type == "bot"}
	}
	if d.Data.Parent.Type == "database" && d.Data.Parent.ID != "" {
		ev.Room = &sdk.Room{ID: d.Data.Parent.ID, Kind: "database"}
	}
	gone := strings.HasSuffix(d.Type, ".deleted")
	var data any
	switch d.Entity.Type {
	case "page":
		pd := pageData{PageID: d.Entity.ID, Change: change}
		if !gone {
			p, err := a.api.Page(ctx, d.Entity.ID)
			switch {
			case isNotFound(err):
			case err != nil:
				return sdk.Event{}, a.wrap(err)
			default:
				pd = newPageData(p, change)
				ev.Text = pd.Title
				ev.Room = pageRoom(p)
			}
		}
		data = pd
	default:
		dd := databaseData{DatabaseID: d.Entity.ID, Change: change}
		if !gone {
			db, err := a.api.Database(ctx, d.Entity.ID)
			switch {
			case isNotFound(err):
			case err != nil:
				return sdk.Event{}, a.wrap(err)
			default:
				dd = databaseData{DatabaseID: db.ID, Title: db.Name(), URL: db.URL, Change: change}
				ev.Text = dd.Title
			}
		}
		data = dd
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return sdk.Event{}, err
	}
	ev.Data = raw
	return ev, nil
}

// Run polls Notion search while a subscription exists and Trebi gives no
// hook.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		a.mu.Lock()
		on := a.subscribed && a.hook == nil
		a.mu.Unlock()
		if !on {
			continue
		}
		if err := a.poll(ctx, e); err != nil {
			slog.Warn("poll notion", "err", err)
		}
	}
}

// poll emits a page event for each edit after the cursor, oldest first.
// Notion rounds last_edited_time, so the poll reads from the cursor itself
// and drops the pages it saw at the cursor.
func (a *Adapter) poll(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	cursor, seen := a.st.PollCursor, slices.Clone(a.st.PollSeen)
	a.mu.Unlock()
	since, err := time.Parse(time.RFC3339Nano, cursor)
	if err != nil {
		since = time.Now()
	}
	var fresh []client.Page
	next := ""
	for done := false; !done; {
		res, err := a.api.EditedPages(ctx, "", next, pollPage)
		if err != nil {
			return a.wrap(err)
		}
		for _, p := range res.Results {
			t, err := time.Parse(time.RFC3339Nano, p.LastEditedTime)
			if err != nil || t.Before(since) {
				done = true
				break
			}
			if t.Equal(since) && slices.Contains(seen, p.ID) {
				continue
			}
			fresh = append(fresh, p)
		}
		if !res.HasMore || res.NextCursor == "" {
			break
		}
		next = res.NextCursor
	}
	if len(fresh) == 0 {
		return nil
	}
	slices.Reverse(fresh)
	for _, p := range fresh {
		pd := newPageData(p, "edited")
		raw, err := json.Marshal(pd)
		if err != nil {
			return err
		}
		ev := sdk.Event{
			ID: "poll:" + p.ID + ":" + p.LastEditedTime, Type: "page", TS: p.LastEditedTime,
			Room: pageRoom(p), Text: pd.Title, Data: raw,
		}
		if t, err := time.Parse(time.RFC3339Nano, p.LastEditedTime); err == nil {
			ev.TS = sdk.FormatTime(t)
		}
		if p.LastEditedBy != nil && p.LastEditedBy.ID != "" {
			ev.Sender = &sdk.Author{ID: p.LastEditedBy.ID}
		}
		if err := e.Event(ev); err != nil {
			return err
		}
		t, _ := time.Parse(time.RFC3339Nano, p.LastEditedTime) //nolint:errcheck // checked in the loop above
		a.mu.Lock()
		if t.After(since) {
			since, a.st.PollSeen = t, nil
		}
		a.st.PollCursor = notionTime(since)
		a.st.PollSeen = append(a.st.PollSeen, p.ID)
		err = a.save()
		a.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// pageData is the data of a page event (catalog/notion/schemas/page.json).
type pageData struct {
	PageID         string `json:"page_id"`
	Title          string `json:"title,omitempty"`
	URL            string `json:"url,omitempty"`
	Change         string `json:"change,omitempty"`
	DatabaseID     string `json:"database_id,omitempty"`
	LastEditedTime string `json:"last_edited_time,omitempty"`
}

func newPageData(p client.Page, change string) pageData {
	return pageData{
		PageID: p.ID, Title: p.Title(), URL: p.URL, Change: change,
		DatabaseID: p.Parent.DatabaseID, LastEditedTime: p.LastEditedTime,
	}
}

// databaseData is the data of a database event
// (catalog/notion/schemas/database.json).
type databaseData struct {
	DatabaseID string `json:"database_id"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Change     string `json:"change,omitempty"`
}

// pageRoom is the parent database of a page, or the workspace.
func pageRoom(p client.Page) *sdk.Room {
	if p.Parent.DatabaseID != "" {
		return &sdk.Room{ID: p.Parent.DatabaseID, Kind: "database"}
	}
	return &sdk.Room{ID: roomWorkspace, Name: "Workspace"}
}

// notionTime writes t in the Notion time format.
func notionTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func isNotFound(err error) bool {
	var e *client.APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// wrap maps a Notion API error to a protocol error.
func (a *Adapter) wrap(err error) error {
	var e *client.APIError
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.Status == http.StatusUnauthorized:
		a.mu.Lock()
		a.revoked = true
		a.mu.Unlock()
		return sdk.AuthRequired("Notion rejects the integration secret")
	case e.Status == http.StatusTooManyRequests:
		return sdk.RateLimited("Notion rate limit", time.Second)
	case e.Status == http.StatusNotFound:
		return sdk.NotFound(e.Error())
	case e.Status >= 500:
		return sdk.Transient(e.Error())
	}
	return sdk.Permanent(cmp.Or(e.Message, e.Error()))
}
