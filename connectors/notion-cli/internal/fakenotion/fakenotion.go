// Package fakenotion is an in-process fake of the Notion API for `serve
// --sandbox` and the tests. It has one bot, one database, and two pages.
// Real Notion learns the webhook URL from a person; the fake learns it from
// POST /_sandbox/subscribe and then posts the verification request and one
// signed event to it.
package fakenotion

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/client"
)

// Fixed ids of the fake workspace.
const (
	BotID      = "bot-sandbox"
	DatabaseID = "db-tasks"
	PageID     = "page-task-1"
	WelcomeID  = "page-welcome"
)

// SignatureHeader is the header of a signed delivery.
const SignatureHeader = "X-Notion-Signature"

// Fake is a running fake Notion.
type Fake struct {
	srv  *httptest.Server
	post *http.Client

	mu    sync.Mutex
	pages []client.Page
	dbs   map[string]client.Database
	token string // the verification token of the last subscribe
	seq   int
}

// Start runs a fake on a local port. Close stops it.
func Start() *Fake {
	f := &Fake{
		post: &http.Client{Timeout: 5 * time.Second},
		dbs: map[string]client.Database{DatabaseID: {
			Object: "database", ID: DatabaseID, URL: "https://www.notion.so/" + DatabaseID,
			Title: []client.RichText{{PlainText: "Tasks"}}, Parent: client.Parent{Type: "workspace", Workspace: true},
			LastEditedTime: "2026-10-01T09:00:00.000Z",
		}},
	}
	f.pages = []client.Page{
		page(WelcomeID, "Welcome", client.Parent{Type: "workspace", Workspace: true}, "2026-10-01T08:00:00.000Z"),
		page(PageID, "Write the plan", client.Parent{Type: "database_id", DatabaseID: DatabaseID}, "2026-10-01T09:30:00.000Z"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/me", f.auth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, client.User{Object: "user", ID: BotID, Name: "Trebi sandbox", Type: "bot"})
	}))
	mux.HandleFunc("GET /v1/pages/{id}", f.auth(f.getPage))
	mux.HandleFunc("GET /v1/databases/{id}", f.auth(f.getDatabase))
	mux.HandleFunc("POST /v1/search", f.auth(f.search))
	mux.HandleFunc("POST /_sandbox/subscribe", f.subscribe)
	f.srv = httptest.NewServer(mux)
	return f
}

// URL is the API base URL.
func (f *Fake) URL() string { return f.srv.URL + "/v1" }

// SubscribeURL is the fake-only endpoint that takes the webhook URL.
func (f *Fake) SubscribeURL() string { return f.srv.URL + "/_sandbox/subscribe" }

// Close stops the fake.
func (f *Fake) Close() { f.srv.Close() }

// AddPage adds a page in the database, edited now.
func (f *Fake) AddPage(title string) client.Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	p := page(fmt.Sprintf("page-new-%d", f.seq), title, client.Parent{Type: "database_id", DatabaseID: DatabaseID},
		time.Now().UTC().Add(time.Duration(f.seq)*time.Millisecond).Format("2006-01-02T15:04:05.000Z"))
	f.pages = append(f.pages, p)
	return p
}

// Token is the verification token of the last subscribe.
func (f *Fake) Token() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

// Sign returns the signature header value of body with token.
func Sign(token string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func page(id, title string, parent client.Parent, edited string) client.Page {
	return client.Page{
		Object: "page", ID: id, CreatedTime: edited, LastEditedTime: edited, URL: "https://www.notion.so/" + id,
		LastEditedBy: &client.User{Object: "user", ID: "user-ana"}, Parent: parent,
		Properties: map[string]client.Property{"Name": {Type: "title", Title: []client.RichText{{PlainText: title}}}},
	}
}

func (f *Fake) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "" {
			writeJSON(w, http.StatusUnauthorized, client.APIError{Status: 401, Code: "unauthorized", Message: "API token is invalid."})
			return
		}
		h(w, r)
	}
}

func (f *Fake) getPage(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	i := slices.IndexFunc(f.pages, func(p client.Page) bool { return p.ID == r.PathValue("id") })
	var p client.Page
	if i >= 0 {
		p = f.pages[i]
	}
	f.mu.Unlock()
	if i < 0 {
		writeJSON(w, http.StatusNotFound, client.APIError{Status: 404, Code: "object_not_found", Message: "Could not find page."})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (f *Fake) getDatabase(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	d, ok := f.dbs[r.PathValue("id")]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, client.APIError{Status: 404, Code: "object_not_found", Message: "Could not find database."})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// search returns the pages whose title has the query, newest edit first.
func (f *Fake) search(w http.ResponseWriter, r *http.Request) {
	var p client.SearchParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, client.APIError{Status: 400, Code: "validation_error", Message: err.Error()})
		return
	}
	f.mu.Lock()
	var out []client.Page
	for _, pg := range f.pages {
		if strings.Contains(strings.ToLower(pg.Title()), strings.ToLower(p.Query)) {
			out = append(out, pg)
		}
	}
	f.mu.Unlock()
	slices.SortFunc(out, func(a, b client.Page) int { return strings.Compare(b.LastEditedTime, a.LastEditedTime) })
	writeJSON(w, http.StatusOK, client.SearchResult{Results: out})
}

// subscribe posts the verification request and then one signed event to
// the URL, before it answers, so both are at the hook when sync returns.
func (f *Fake) subscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	token := "secret_sandbox_" + randHex(16)
	f.mu.Lock()
	f.token = token
	f.mu.Unlock()
	verify, _ := json.Marshal(map[string]string{"verification_token": token}) //nolint:errchkjson // a map of strings
	if err := f.deliver(r, in.URL, verify, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	event, _ := json.Marshal(map[string]any{ //nolint:errchkjson // plain values
		"id": randHex(16), "timestamp": time.Now().UTC().Format(time.RFC3339), "workspace_id": "ws-sandbox",
		"subscription_id": "sub-sandbox", "integration_id": "int-sandbox", "type": "page.content_updated",
		"authors": []map[string]string{{"id": "user-ana", "type": "person"}},
		"entity":  map[string]string{"id": PageID, "type": "page"},
	})
	if err := f.deliver(r, in.URL, event, Sign(token, event)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) deliver(r *http.Request, url string, body []byte, sig string) error {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set(SignatureHeader, sig)
	}
	resp, err := f.post.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close() //nolint:errcheck,gosec // the status is all the fake reads
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post to the webhook: %s", resp.Status)
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) //nolint:errcheck // crypto/rand does not fail
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // the client sees a short body
}
