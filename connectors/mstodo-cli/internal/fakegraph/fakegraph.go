// Package fakegraph is an in-process fake of the Graph To Do API and of the
// device code login. The sandbox and the tests use it.
package fakegraph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
)

// ClientID is the fixed app id of the sandbox.
const ClientID = "00000000-0000-0000-0000-00000000f00d"

// PendingFor is how long the device code login stays pending.
const PendingFor = 200 * time.Millisecond

var resourceRe = regexp.MustCompile(`^/?me/todo/lists/([^/]+)/tasks$`)

type task struct {
	client.Task
	list    string
	seq     int
	deleted bool
}

// Fake is one fake Graph server.
type Fake struct {
	srv *httptest.Server
	out *http.Client
	// AutoNotify posts one change notification after each new subscription.
	AutoNotify bool

	mu      sync.Mutex
	wg      sync.WaitGroup
	closing chan struct{}
	n       int
	seq     int
	lists   []client.List
	tasks   []*task
	subs    map[string]*client.Subscription
	devices map[string]time.Time
}

// Start runs the fake with two lists: Groceries and Work.
func Start() *Fake {
	f := &Fake{
		out:     &http.Client{Timeout: 10 * time.Second},
		closing: make(chan struct{}),
		subs:    map[string]*client.Subscription{},
		devices: map[string]time.Time{},
		lists:   []client.List{{ID: "list-groceries", DisplayName: "Groceries"}, {ID: "list-work", DisplayName: "Work"}},
	}
	f.AddTask("list-groceries", "Buy milk")
	f.AddTask("list-groceries", "Buy bread")
	f.AddTask("list-work", "Write the report")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/devicecode", f.deviceCode)
	mux.HandleFunc("POST /oauth/token", f.token)
	mux.HandleFunc("GET /v1.0/me", f.auth(f.me))
	mux.HandleFunc("GET /v1.0/me/todo/lists", f.auth(f.getLists))
	mux.HandleFunc("GET /v1.0/me/todo/lists/{list}", f.auth(f.getList))
	mux.HandleFunc("GET /v1.0/me/todo/lists/{list}/tasks", f.auth(f.getTasks))
	mux.HandleFunc("GET /v1.0/me/todo/lists/{list}/tasks/delta", f.auth(f.delta))
	mux.HandleFunc("GET /v1.0/me/todo/lists/{list}/tasks/{id}", f.auth(f.getTask))
	mux.HandleFunc("GET /v1.0/subscriptions", f.auth(f.listSubs))
	mux.HandleFunc("POST /v1.0/subscriptions", f.auth(f.createSub))
	mux.HandleFunc("PATCH /v1.0/subscriptions/{id}", f.auth(f.renewSub))
	mux.HandleFunc("DELETE /v1.0/subscriptions/{id}", f.auth(f.deleteSub))
	f.srv = httptest.NewServer(mux)
	return f
}

// LoginURL is the base of the login endpoints.
func (f *Fake) LoginURL() string { return f.srv.URL + "/oauth" }

// GraphURL is the Graph base.
func (f *Fake) GraphURL() string { return f.srv.URL + "/v1.0" }

// Close stops the server and waits for the notifications in flight.
func (f *Fake) Close() {
	close(f.closing)
	f.wg.Wait()
	f.srv.Close()
}

// AddTask adds a task to a list and returns it.
func (f *Fake) AddTask(list, title string) client.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.seq++
	now := time.Now().UTC().Format(time.RFC3339Nano)
	t := &task{Task: client.Task{
		ID: "task-" + strconv.Itoa(f.n), Title: title, Status: "notStarted", Importance: "normal",
		CreatedDateTime: now, LastModifiedDateTime: now,
	}, list: list, seq: f.seq}
	f.tasks = append(f.tasks, t)
	return t.Task
}

// Subscriptions returns the subscriptions.
func (f *Fake) Subscriptions() []client.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]client.Subscription, 0, len(f.subs))
	for _, s := range f.subs {
		out = append(out, *s)
	}
	return out
}

// Remove deletes a subscription, as Graph does when it drops one.
func (f *Fake) Remove(id string) {
	f.mu.Lock()
	delete(f.subs, id)
	f.mu.Unlock()
}

// Tokens have a fixed prefix, so a token stays valid after a restart of
// the sandbox.
const (
	accessPrefix  = "sandbox-at-"
	refreshPrefix = "sandbox-rt-"
)

func (f *Fake) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(tok, accessPrefix) {
			graphErr(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token is empty or not valid.")
			return
		}
		h(w, r)
	}
}

func (f *Fake) deviceCode(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") == "" {
		oauthErr(w, "invalid_request", "client_id is missing")
		return
	}
	f.mu.Lock()
	f.n++
	code := "device-" + strconv.Itoa(f.n)
	f.devices[code] = time.Now()
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, client.DeviceCode{
		DeviceCode: code, UserCode: "SANDBOX" + strconv.Itoa(f.n), VerificationURI: "https://microsoft.com/devicelogin",
		ExpiresIn: 900, Message: "Sandbox login: it finishes by itself.",
	})
}

func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.FormValue("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		start, ok := f.devices[r.FormValue("device_code")]
		switch {
		case !ok:
			oauthErr(w, "expired_token", "unknown device code")
			return
		case time.Since(start) < PendingFor:
			oauthErr(w, "authorization_pending", "the user has not finished the login")
			return
		}
		delete(f.devices, r.FormValue("device_code"))
	case "refresh_token":
		if !strings.HasPrefix(r.FormValue("refresh_token"), refreshPrefix) {
			oauthErr(w, "invalid_grant", "the refresh token is not valid")
			return
		}
	default:
		oauthErr(w, "unsupported_grant_type", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token_type": "Bearer", "expires_in": 3600, "scope": client.Scopes,
		"access_token": accessPrefix + client.Secret(12), "refresh_token": refreshPrefix + client.Secret(12),
	})
}

func (f *Fake) me(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, client.User{ID: "user-sandbox", DisplayName: "Sandbox User", UserPrincipalName: "sandbox@example.com"})
}

func (f *Fake) findList(id string) (client.List, bool) {
	for _, l := range f.lists {
		if l.ID == id {
			return l, true
		}
	}
	return client.List{}, false
}

func (f *Fake) getLists(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"value": f.lists})
}

func (f *Fake) getList(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.findList(r.PathValue("list"))
	if !ok {
		graphErr(w, http.StatusNotFound, "ErrorItemNotFound", "The list is not found.")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (f *Fake) getTasks(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.findList(r.PathValue("list")); !ok {
		graphErr(w, http.StatusNotFound, "ErrorItemNotFound", "The list is not found.")
		return
	}
	out := []client.Task{}
	for _, t := range f.tasks {
		if t.list == r.PathValue("list") && !t.deleted {
			out = append(out, t.Task)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (f *Fake) getTask(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tasks {
		if t.list == r.PathValue("list") && t.ID == r.PathValue("id") && !t.deleted {
			writeJSON(w, http.StatusOK, t.Task)
			return
		}
	}
	graphErr(w, http.StatusNotFound, "ErrorItemNotFound", "The task is not found.")
}

// delta gives the changes after the $deltatoken, or all tasks without one.
func (f *Fake) delta(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := r.PathValue("list")
	if _, ok := f.findList(list); !ok {
		graphErr(w, http.StatusNotFound, "ErrorItemNotFound", "The list is not found.")
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("$deltatoken")) //nolint:errcheck // no token is the start
	out := []client.Task{}
	for _, t := range f.tasks {
		if t.list != list || t.seq <= after {
			continue
		}
		if t.deleted {
			out = append(out, client.Task{ID: t.ID, Removed: json.RawMessage(`{"reason":"deleted"}`)})
			continue
		}
		out = append(out, t.Task)
	}
	link := f.GraphURL() + "/me/todo/lists/" + url.PathEscape(list) + "/tasks/delta?$deltatoken=" + strconv.Itoa(f.seq)
	writeJSON(w, http.StatusOK, map[string]any{"value": out, "@odata.deltaLink": link})
}

func (f *Fake) listSubs(w http.ResponseWriter, _ *http.Request) {
	subs := f.Subscriptions()
	for i := range subs {
		subs[i].ClientState = "" // Graph does not return it
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": subs})
}

// createSub checks the notification URLs as Graph does, then stores the
// subscription. With AutoNotify, it then posts one change.
func (f *Fake) createSub(w http.ResponseWriter, r *http.Request) {
	var s client.Subscription
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		graphErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	m := resourceRe.FindStringSubmatch(s.Resource)
	f.mu.Lock()
	okList := false
	if m != nil {
		_, okList = f.findList(m[1])
	}
	f.mu.Unlock()
	switch {
	case !okList:
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "The resource is not a task list.")
		return
	case s.NotificationURL == "" || s.ChangeType == "" || s.ExpirationDateTime.IsZero():
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "A required field is missing.")
		return
	}
	for _, u := range []string{s.NotificationURL, s.LifecycleNotificationURL} {
		if u == "" {
			continue
		}
		if err := f.validate(r, u); err != nil {
			graphErr(w, http.StatusBadRequest, "ValidationError", "Subscription validation request failed: "+err.Error())
			return
		}
	}
	f.mu.Lock()
	f.n++
	s.ID = "sub-" + strconv.Itoa(f.n)
	stored := s
	f.subs[s.ID] = &stored
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, s)
	if f.AutoNotify {
		t := f.AddTask(m[1], "A new sandbox task")
		f.wg.Add(1)
		go f.notify(s, m[1], t) // ends after one post or on Close
	}
}

// validate posts a validationToken and expects it back as text/plain.
func (f *Fake) validate(r *http.Request, u string) error {
	token := "Validation: Testing client application reachability " + strconv.FormatInt(time.Now().UnixNano(), 36)
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, u+sep+"validationToken="+url.QueryEscape(token), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := f.out.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || string(body) != token {
		return fmt.Errorf("%s answers %d without the token", u, resp.StatusCode)
	}
	return nil
}

// notification puts clientState last, as Graph does.
type notification struct {
	SubscriptionID                 string            `json:"subscriptionId"`
	SubscriptionExpirationDateTime time.Time         `json:"subscriptionExpirationDateTime"`
	ChangeType                     string            `json:"changeType"`
	Resource                       string            `json:"resource"`
	ResourceData                   map[string]string `json:"resourceData"`
	TenantID                       string            `json:"tenantId"`
	ClientState                    string            `json:"clientState"`
}

func (f *Fake) notify(s client.Subscription, list string, t client.Task) {
	defer f.wg.Done()
	select {
	case <-f.closing:
		return
	case <-time.After(20 * time.Millisecond):
	}
	body, err := json.Marshal(map[string]any{"value": []notification{{
		SubscriptionID: s.ID, SubscriptionExpirationDateTime: s.ExpirationDateTime, ChangeType: "created",
		Resource:     "me/todo/lists/" + list + "/tasks/" + t.ID,
		ResourceData: map[string]string{"@odata.type": "#Microsoft.Graph.todoTask", "id": t.ID},
		TenantID:     "tenant-sandbox", ClientState: s.ClientState,
	}}})
	if err != nil {
		return
	}
	resp, err := f.out.Post(s.NotificationURL, "application/json", bytes.NewReader(body))
	if err == nil {
		resp.Body.Close() //nolint:errcheck // the answer does not matter
	}
}

func (f *Fake) renewSub(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ExpirationDateTime time.Time `json:"expirationDateTime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.ExpirationDateTime.IsZero() {
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "expirationDateTime is missing")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[r.PathValue("id")]
	if !ok {
		graphErr(w, http.StatusNotFound, "ResourceNotFound", "The subscription is not found.")
		return
	}
	s.ExpirationDateTime = p.ExpirationDateTime
	out := *s
	out.ClientState = ""
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) deleteSub(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[r.PathValue("id")]; !ok {
		graphErr(w, http.StatusNotFound, "ResourceNotFound", "The subscription is not found.")
		return
	}
	delete(f.subs, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // the client reads it or not
}

func graphErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func oauthErr(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": msg})
}
