// Package fakegraph is an in-process fake of the Graph To Do API and of the
// device code login. The sandbox and the tests use it.
package fakegraph

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
)

// ClientID is the fixed app id of the sandbox.
const ClientID = "00000000-0000-0000-0000-00000000f00d"

// Tenant is the tenant of the sandbox login.
const Tenant = "consumers"

// The account of the sandbox login.
const (
	AccountID   = "user-sandbox"
	AccountName = "sandbox@example.com"
)

// PendingFor is how long the device code login stays pending.
const PendingFor = 200 * time.Millisecond

var (
	resourceRe = regexp.MustCompile(`^/?me/todo/lists/([^/]+)/tasks$`)
	filterRe   = regexp.MustCompile(`^status (eq|ne) '(\w+)'$`)
)

// kinds are the child collections of a task.
var kinds = []string{"checklistItems", "linkedResources", "attachments", "extensions"}

type task struct {
	client.Task
	list    string
	seq     int
	deleted bool
}

type list struct {
	client.List
	seq     int
	deleted bool
}

// upload is an attachment upload session.
type upload struct {
	ref               client.Ref
	name, contentType string
	data              []byte
}

// Fake is one fake Graph server.
type Fake struct {
	srv *httptest.Server
	out *http.Client
	// AutoNotify posts one change notification after each new subscription.
	AutoNotify bool

	mu       sync.Mutex
	wg       sync.WaitGroup
	closing  chan struct{}
	n        int
	seq      int
	lists    []*list
	tasks    []*task
	children map[string][]map[string]any // owner path + kind → items
	uploads  map[string]*upload
	subs     map[string]*client.Subscription
	devices  map[string]time.Time
}

// Start runs the fake with two lists: Groceries and Work.
func Start() *Fake {
	f := &Fake{
		out:      &http.Client{Timeout: 10 * time.Second},
		closing:  make(chan struct{}),
		children: map[string][]map[string]any{},
		uploads:  map[string]*upload{},
		subs:     map[string]*client.Subscription{},
		devices:  map[string]time.Time{},
		lists: []*list{
			{List: client.List{ID: "list-groceries", DisplayName: "Groceries", IsOwner: true}},
			{List: client.List{ID: "list-work", DisplayName: "Work", IsOwner: true}},
		},
	}
	f.AddTask("list-groceries", "Buy milk")
	f.AddTask("list-groceries", "Buy bread")
	f.AddTask("list-work", "Write the report")
	const lists, task = "/v1.0/me/todo/lists", "/v1.0/me/todo/lists/{list}/tasks/{id}"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{tenant}/oauth2/v2.0/devicecode", f.deviceCode)
	mux.HandleFunc("POST /{tenant}/oauth2/v2.0/token", f.token)
	mux.HandleFunc("GET "+lists, f.auth(f.getLists))
	mux.HandleFunc("POST "+lists, f.auth(f.createList))
	mux.HandleFunc("GET "+lists+"/delta", f.auth(f.listsDelta))
	mux.HandleFunc("GET "+lists+"/{list}", f.auth(f.getList))
	mux.HandleFunc("PATCH "+lists+"/{list}", f.auth(f.updateList))
	mux.HandleFunc("DELETE "+lists+"/{list}", f.auth(f.deleteList))
	mux.HandleFunc("GET "+lists+"/{list}/tasks", f.auth(f.getTasks))
	mux.HandleFunc("POST "+lists+"/{list}/tasks", f.auth(f.createTask))
	mux.HandleFunc("GET "+lists+"/{list}/tasks/delta", f.auth(f.delta))
	mux.HandleFunc("GET "+task, f.auth(f.getTask))
	mux.HandleFunc("PATCH "+task, f.auth(f.updateTask))
	mux.HandleFunc("DELETE "+task, f.auth(f.deleteTask))
	mux.HandleFunc("GET "+lists+"/{list}/{kind}", f.auth(f.listChildren))
	mux.HandleFunc("POST "+lists+"/{list}/{kind}", f.auth(f.addChild))
	mux.HandleFunc("GET "+lists+"/{list}/{kind}/{item}", f.auth(f.getChild))
	mux.HandleFunc("PATCH "+lists+"/{list}/{kind}/{item}", f.auth(f.updateChild))
	mux.HandleFunc("DELETE "+lists+"/{list}/{kind}/{item}", f.auth(f.deleteChild))
	mux.HandleFunc("GET "+task+"/{kind}", f.auth(f.listChildren))
	mux.HandleFunc("POST "+task+"/{kind}", f.auth(f.addChild))
	mux.HandleFunc("GET "+task+"/{kind}/{item}", f.auth(f.getChild))
	mux.HandleFunc("PATCH "+task+"/{kind}/{item}", f.auth(f.updateChild))
	mux.HandleFunc("DELETE "+task+"/{kind}/{item}", f.auth(f.deleteChild))
	mux.HandleFunc("GET "+task+"/attachments/{item}/$value", f.auth(f.attachmentValue))
	mux.HandleFunc("POST "+task+"/attachments/createUploadSession", f.auth(f.createUpload))
	mux.HandleFunc("PUT /v1.0/uploads/{id}/content", f.auth(f.putUpload))
	mux.HandleFunc("DELETE /v1.0/uploads/{id}", f.auth(f.deleteUpload))
	mux.HandleFunc("GET /v1.0/subscriptions", f.auth(f.listSubs))
	mux.HandleFunc("POST /v1.0/subscriptions", f.auth(f.createSub))
	mux.HandleFunc("GET /v1.0/subscriptions/{id}", f.auth(f.getSub))
	mux.HandleFunc("PATCH /v1.0/subscriptions/{id}", f.auth(f.renewSub))
	mux.HandleFunc("DELETE /v1.0/subscriptions/{id}", f.auth(f.deleteSub))
	f.srv = httptest.NewServer(mux)
	return f
}

// LoginBase is the base of the login endpoints.
func (f *Fake) LoginBase() string { return f.srv.URL }

// GraphURL is the Graph base.
func (f *Fake) GraphURL() string { return f.srv.URL + "/v1.0" }

// Close stops the server and waits for the notifications in flight.
func (f *Fake) Close() {
	close(f.closing)
	f.wg.Wait()
	f.srv.Close()
}

// stamp is the time format of Graph.
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// AddTask adds a task to a list and returns it.
func (f *Fake) AddTask(list, title string) client.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addTaskLocked(list, client.Task{Title: title})
}

func (f *Fake) addTaskLocked(list string, t client.Task) client.Task {
	f.n++
	f.seq++
	t.ID, t.CreatedDateTime = "task-"+strconv.Itoa(f.n), stamp()
	t.LastModifiedDateTime = t.CreatedDateTime
	if t.Status == "" {
		t.Status = "notStarted"
	}
	if t.Importance == "" {
		t.Importance = "normal"
	}
	f.tasks = append(f.tasks, &task{Task: t, list: list, seq: f.seq})
	return t
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

// LegacyRefreshToken starts a refresh token of a login without the openid
// consent, as v0.1.0 made. Microsoft refuses openid in its refresh.
const LegacyRefreshToken = refreshPrefix + "legacy-"

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
		rt := r.FormValue("refresh_token")
		if !strings.HasPrefix(rt, refreshPrefix) {
			oauthErr(w, "invalid_grant", "the refresh token is not valid")
			return
		}
		if strings.HasPrefix(rt, LegacyRefreshToken) && strings.Contains(r.FormValue("scope"), "openid") {
			oauthErr(w, "invalid_grant", "AADSTS70000: one or more scopes requested are unauthorized or expired")
			return
		}
	default:
		oauthErr(w, "unsupported_grant_type", "")
		return
	}
	claims, _ := json.Marshal(map[string]string{"oid": AccountID, "preferred_username": AccountName, "tid": r.PathValue("tenant")}) //nolint:errcheck // a fixed map
	enc := base64.RawURLEncoding.EncodeToString
	writeJSON(w, http.StatusOK, map[string]any{
		"token_type": "Bearer", "expires_in": 3600, "scope": client.Scopes,
		"access_token": accessPrefix + client.Secret(12), "refresh_token": refreshPrefix + client.Secret(12),
		"id_token": enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + ".",
	})
}

func (f *Fake) findList(id string) (*list, bool) {
	for _, l := range f.lists {
		if l.ID == id && !l.deleted {
			return l, true
		}
	}
	return nil, false
}

func (f *Fake) findTask(listID, id string) (*task, bool) {
	for _, t := range f.tasks {
		if t.list == listID && t.ID == id && !t.deleted {
			return t, true
		}
	}
	return nil, false
}

// decode reads a JSON body. A bad body answers 400.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		graphErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return false
	}
	return true
}

// merge applies a JSON patch body to v, as a PATCH does.
func merge(v any, patch map[string]any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	for k, x := range patch {
		m[k] = x
	}
	if raw, err = json.Marshal(m); err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func notFound(w http.ResponseWriter, what string) {
	graphErr(w, http.StatusNotFound, "ErrorItemNotFound", "The "+what+" is not found.")
}

func (f *Fake) getLists(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []client.List{}
	for _, l := range f.lists {
		if !l.deleted {
			out = append(out, l.List)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (f *Fake) createList(w http.ResponseWriter, r *http.Request) {
	var in client.List
	if !decode(w, r, &in) {
		return
	}
	if in.DisplayName == "" {
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "displayName is missing")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.seq++
	l := &list{List: client.List{ID: "list-" + strconv.Itoa(f.n), DisplayName: in.DisplayName, IsOwner: true}, seq: f.seq}
	f.lists = append(f.lists, l)
	writeJSON(w, http.StatusCreated, l.List)
}

func (f *Fake) getList(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.findList(r.PathValue("list"))
	if !ok {
		notFound(w, "list")
		return
	}
	writeJSON(w, http.StatusOK, l.List)
}

func (f *Fake) updateList(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if !decode(w, r, &patch) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.findList(r.PathValue("list"))
	if !ok {
		notFound(w, "list")
		return
	}
	if err := merge(&l.List, patch); err != nil {
		graphErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	f.seq++
	l.seq = f.seq
	writeJSON(w, http.StatusOK, l.List)
}

func (f *Fake) deleteList(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.findList(r.PathValue("list"))
	if !ok {
		notFound(w, "list")
		return
	}
	f.seq++
	l.deleted, l.seq = true, f.seq
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) listsDelta(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	after, _ := strconv.Atoi(r.URL.Query().Get("$deltatoken")) //nolint:errcheck // no token is the start
	out := []client.List{}
	for _, l := range f.lists {
		switch {
		case l.seq <= after && after > 0:
		case l.deleted && after > 0:
			out = append(out, client.List{ID: l.ID, Removed: json.RawMessage(`{"reason":"deleted"}`)})
		case !l.deleted:
			out = append(out, l.List)
		}
	}
	link := f.GraphURL() + "/me/todo/lists/delta?$deltatoken=" + strconv.Itoa(f.seq)
	writeJSON(w, http.StatusOK, map[string]any{"value": out, "@odata.deltaLink": link})
}

// getTasks knows $filter on the status, $top, and pages of $top tasks.
func (f *Fake) getTasks(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	listID := r.PathValue("list")
	if _, ok := f.findList(listID); !ok {
		notFound(w, "list")
		return
	}
	q := r.URL.Query()
	var op, status string
	if v := q.Get("$filter"); v != "" {
		m := filterRe.FindStringSubmatch(v)
		if m == nil {
			graphErr(w, http.StatusBadRequest, "InvalidRequest", "the sandbox knows only status eq and status ne filters")
			return
		}
		op, status = m[1], m[2]
	}
	out := []client.Task{}
	for _, t := range f.tasks {
		if t.list != listID || t.deleted || (op == "eq" && t.Status != status) || (op == "ne" && t.Status == status) {
			continue
		}
		out = append(out, t.Task)
	}
	skip, _ := strconv.Atoi(q.Get("$skip")) //nolint:errcheck // no skip is 0
	top, _ := strconv.Atoi(q.Get("$top"))   //nolint:errcheck // no top is all
	out = out[min(skip, len(out)):]
	body := map[string]any{"value": out}
	if top > 0 && len(out) > top {
		body["value"] = out[:top]
		q.Set("$skip", strconv.Itoa(skip+top))
		body["@odata.nextLink"] = f.GraphURL() + "/me/todo/lists/" + url.PathEscape(listID) + "/tasks?" + q.Encode()
	}
	writeJSON(w, http.StatusOK, body)
}

func (f *Fake) createTask(w http.ResponseWriter, r *http.Request) {
	var in client.Task
	if !decode(w, r, &in) {
		return
	}
	if in.Title == "" {
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "title is missing")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.findList(r.PathValue("list")); !ok {
		notFound(w, "list")
		return
	}
	writeJSON(w, http.StatusCreated, f.addTaskLocked(r.PathValue("list"), in))
}

func (f *Fake) getTask(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.findTask(r.PathValue("list"), r.PathValue("id"))
	if !ok {
		notFound(w, "task")
		return
	}
	writeJSON(w, http.StatusOK, t.Task)
}

func (f *Fake) updateTask(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if !decode(w, r, &patch) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.findTask(r.PathValue("list"), r.PathValue("id"))
	if !ok {
		notFound(w, "task")
		return
	}
	if err := merge(&t.Task, patch); err != nil {
		graphErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	f.touch(t)
	writeJSON(w, http.StatusOK, t.Task)
}

// touch marks a task as changed. The modified time is a little after the
// creation, so a change is not new.
func (f *Fake) touch(t *task) {
	f.seq++
	t.seq = f.seq
	c, err := time.Parse(time.RFC3339Nano, t.CreatedDateTime)
	m := time.Now().UTC()
	if err == nil && m.Sub(c) < 3*time.Second {
		m = c.Add(3 * time.Second)
	}
	t.LastModifiedDateTime = m.Format(time.RFC3339Nano)
}

func (f *Fake) deleteTask(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.findTask(r.PathValue("list"), r.PathValue("id"))
	if !ok {
		notFound(w, "task")
		return
	}
	f.seq++
	t.deleted, t.seq = true, f.seq
	w.WriteHeader(http.StatusNoContent)
}

// delta gives the changes after the $deltatoken, or all tasks without one.
func (f *Fake) delta(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	listID := r.PathValue("list")
	if _, ok := f.findList(listID); !ok {
		notFound(w, "list")
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("$deltatoken")) //nolint:errcheck // no token is the start
	out := []client.Task{}
	for _, t := range f.tasks {
		if t.list != listID || t.seq <= after || (t.deleted && after == 0) {
			continue
		}
		if t.deleted {
			out = append(out, client.Task{ID: t.ID, Removed: json.RawMessage(`{"reason":"deleted"}`)})
			continue
		}
		out = append(out, t.Task)
	}
	link := f.GraphURL() + "/me/todo/lists/" + url.PathEscape(listID) + "/tasks/delta?$deltatoken=" + strconv.Itoa(f.seq)
	writeJSON(w, http.StatusOK, map[string]any{"value": out, "@odata.deltaLink": link})
}

// owner finds the list or the task of a child request and returns the key
// of the collection. A list has only extensions.
func (f *Fake) owner(w http.ResponseWriter, r *http.Request) (string, *task, bool) {
	kind := r.PathValue("kind")
	listID, taskID := r.PathValue("list"), r.PathValue("id")
	if !slices.Contains(kinds, kind) || (taskID == "" && kind != "extensions") {
		graphErr(w, http.StatusBadRequest, "BadRequest", "Resource not found for the segment '"+kind+"'.")
		return "", nil, false
	}
	if _, ok := f.findList(listID); !ok {
		notFound(w, "list")
		return "", nil, false
	}
	if taskID == "" {
		return listID + "/" + kind, nil, true
	}
	t, ok := f.findTask(listID, taskID)
	if !ok {
		notFound(w, "task")
		return "", nil, false
	}
	return listID + "/" + taskID + "/" + kind, t, true
}

func (f *Fake) findChild(key, id string) (int, bool) {
	for i, it := range f.children[key] {
		if it["id"] == id {
			return i, true
		}
	}
	return 0, false
}

func (f *Fake) listChildren(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, _, ok := f.owner(w, r)
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, it := range f.children[key] {
		c := maps.Clone(it)
		delete(c, "contentBytes") // a list of attachments has no content
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (f *Fake) addChild(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if !decode(w, r, &in) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key, t, ok := f.owner(w, r)
	if !ok {
		return
	}
	f.n++
	in["id"] = r.PathValue("kind") + "-" + strconv.Itoa(f.n)
	switch r.PathValue("kind") {
	case "extensions":
		name, _ := in["extensionName"].(string) //nolint:errcheck // checked below
		if name == "" {
			graphErr(w, http.StatusBadRequest, "InvalidRequest", "extensionName is missing")
			return
		}
		if _, dup := f.findChild(key, name); dup {
			graphErr(w, http.StatusConflict, "NameAlreadyExists", "The extension already exists.")
			return
		}
		in["id"] = name
	case "checklistItems":
		in["createdDateTime"] = stamp()
	case "attachments":
		if in["@odata.type"] != "#microsoft.graph.taskFileAttachment" {
			graphErr(w, http.StatusBadRequest, "InvalidRequest", "only taskFileAttachment is supported")
			return
		}
		in["lastModifiedDateTime"] = stamp()
	}
	f.children[key] = append(f.children[key], in)
	if t != nil {
		f.syncAttachments(t)
		f.touch(t)
	}
	writeJSON(w, http.StatusCreated, in)
}

func (f *Fake) syncAttachments(t *task) {
	t.HasAttachments = len(f.children[t.list+"/"+t.ID+"/attachments"]) > 0
}

func (f *Fake) getChild(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, _, ok := f.owner(w, r)
	if !ok {
		return
	}
	i, ok := f.findChild(key, r.PathValue("item"))
	if !ok {
		notFound(w, "item")
		return
	}
	writeJSON(w, http.StatusOK, f.children[key][i])
}

func (f *Fake) updateChild(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if !decode(w, r, &patch) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key, t, ok := f.owner(w, r)
	if !ok {
		return
	}
	i, ok := f.findChild(key, r.PathValue("item"))
	if !ok || r.PathValue("kind") == "attachments" {
		notFound(w, "item")
		return
	}
	if _, ok := patch["isChecked"]; !ok && r.PathValue("kind") == "checklistItems" {
		patch["isChecked"] = false // as Graph does
	}
	for k, v := range patch {
		if k != "id" {
			f.children[key][i][k] = v
		}
	}
	if r.PathValue("kind") == "checklistItems" && patch["isChecked"] == true {
		f.children[key][i]["checkedDateTime"] = stamp()
	}
	if t != nil {
		f.touch(t)
	}
	if r.PathValue("kind") == "extensions" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, f.children[key][i])
}

func (f *Fake) deleteChild(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, t, ok := f.owner(w, r)
	if !ok {
		return
	}
	i, ok := f.findChild(key, r.PathValue("item"))
	if !ok {
		notFound(w, "item")
		return
	}
	f.children[key] = slices.Delete(f.children[key], i, i+1)
	if t != nil {
		f.syncAttachments(t)
		f.touch(t)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) attachmentValue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.PathValue("list") + "/" + r.PathValue("id") + "/attachments"
	i, ok := f.findChild(key, r.PathValue("item"))
	if !ok {
		notFound(w, "attachment")
		return
	}
	it := f.children[key][i]
	data, err := base64.StdEncoding.DecodeString(fmt.Sprint(it["contentBytes"]))
	if err != nil {
		graphErr(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.Header().Set("Content-Type", fmt.Sprint(it["contentType"]))
	_, _ = w.Write(data) //nolint:errcheck // the client reads it or not
}

func (f *Fake) createUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AttachmentInfo struct {
			Name        string `json:"name"`
			Size        int    `json:"size"`
			ContentType string `json:"contentType"`
		} `json:"attachmentInfo"`
	}
	if !decode(w, r, &in) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.findTask(r.PathValue("list"), r.PathValue("id")); !ok {
		notFound(w, "task")
		return
	}
	if in.AttachmentInfo.Size > client.MaxAttachment {
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "the file is too large")
		return
	}
	f.n++
	id := "upload-" + strconv.Itoa(f.n)
	f.uploads[id] = &upload{
		ref:  client.Ref{List: r.PathValue("list"), Task: r.PathValue("id")},
		name: in.AttachmentInfo.Name, contentType: in.AttachmentInfo.ContentType,
		data: make([]byte, 0, in.AttachmentInfo.Size),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadUrl": f.GraphURL() + "/uploads/" + id, "expirationDateTime": time.Now().Add(time.Hour).UTC(),
	})
}

// putUpload takes one chunk. The last chunk adds the attachment and
// answers 201 with its Location.
func (f *Fake) putUpload(w http.ResponseWriter, r *http.Request) {
	var start, end, total int
	if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil {
		graphErr(w, http.StatusBadRequest, "InvalidRequest", "Content-Range is not valid")
		return
	}
	chunk, err := io.ReadAll(r.Body)
	if err != nil {
		graphErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.uploads[r.PathValue("id")]
	switch {
	case !ok:
		notFound(w, "upload session")
		return
	case start != len(u.data) || end-start+1 != len(chunk):
		graphErr(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "the chunk is not the next range")
		return
	}
	u.data = append(u.data, chunk...)
	if len(u.data) < total {
		writeJSON(w, http.StatusOK, map[string]any{"nextExpectedRanges": []string{fmt.Sprintf("%d-", len(u.data))}})
		return
	}
	delete(f.uploads, r.PathValue("id"))
	t, ok := f.findTask(u.ref.List, u.ref.Task)
	if !ok {
		notFound(w, "task")
		return
	}
	f.n++
	id := "attachments-" + strconv.Itoa(f.n)
	key := u.ref.List + "/" + u.ref.Task + "/attachments"
	f.children[key] = append(f.children[key], map[string]any{
		"@odata.type": "#microsoft.graph.taskFileAttachment", "id": id, "name": u.name,
		"contentType": u.contentType, "size": len(u.data), "lastModifiedDateTime": stamp(),
		"contentBytes": base64.StdEncoding.EncodeToString(u.data),
	})
	f.syncAttachments(t)
	f.touch(t)
	w.Header().Set("Location", f.GraphURL()+"/users('"+AccountID+"')/todo/lists('"+u.ref.List+"')/tasks('"+u.ref.Task+"')/attachments('"+id+"')")
	w.WriteHeader(http.StatusCreated)
}

func (f *Fake) deleteUpload(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delete(f.uploads, r.PathValue("id"))
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) getSub(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[r.PathValue("id")]
	if !ok {
		graphErr(w, http.StatusNotFound, "ResourceNotFound", "The subscription is not found.")
		return
	}
	out := *s
	out.ClientState = ""
	writeJSON(w, http.StatusOK, out)
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
		Resource:     "todob2/graph/v1/users('" + AccountName + "')/todoApp/lists('" + list + "')/tasks", // the form of Graph
		ResourceData: map[string]string{"@odata.type": "#Microsoft.Graph.todoTask", "id": t.ID},
		ClientState:  s.ClientState,
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
