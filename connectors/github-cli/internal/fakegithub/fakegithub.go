// Package fakegithub is an in-process GitHub for the sandbox and the
// tests: the device flow, repositories, events, and repository webhooks.
// A new hook gets one signed push delivery, then a ping.
package fakegithub

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
)

// Values of the fake.
const (
	Token       = "gho_sandbox"
	Revoked     = "revoked" // a token that gives 401
	UserCode    = "SAND-BOX1"
	AdminRepo   = "octo/app"
	OtherAdmin  = "octo/docs"
	NoAdminRepo = "other/lib"
)

// Server is the fake.
type Server struct {
	srv    *httptest.Server
	client *http.Client
	wg     sync.WaitGroup

	mu      sync.Mutex
	polls   int
	nextID  int64
	hooks   map[string][]client.Hook
	events  map[string][]client.Event
	deliver bool
	every   int // X-Poll-Interval in seconds
}

// Start runs the fake on a local port.
func Start() *Server {
	s := &Server{
		client:  &http.Client{Timeout: 5 * time.Second},
		hooks:   map[string][]client.Hook{},
		events:  map[string][]client.Event{},
		deliver: true,
		every:   60,
	}
	s.events[AdminRepo] = []client.Event{s.event("1", "PushEvent", AdminRepo)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the base URL of the API and of the device flow.
func (s *Server) URL() string { return s.srv.URL }

// Close waits for the deliveries and stops the fake.
func (s *Server) Close() {
	s.wg.Wait()
	s.srv.Close()
}

// NoDeliveries stops the deliveries to new hooks.
func (s *Server) NoDeliveries() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliver = false
}

// SetPollInterval changes the X-Poll-Interval answer. Zero sends none.
func (s *Server) SetPollInterval(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.every = seconds
}

// Hooks returns the hooks of a repository.
func (s *Server) Hooks(repo string) []client.Hook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]client.Hook(nil), s.hooks[repo]...)
}

// AddEvent puts a new event first in the events of a repository.
func (s *Server) AddEvent(repo, id, typ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[repo] = append([]client.Event{s.event(id, typ, repo)}, s.events[repo]...)
}

func (s *Server) event(id, typ, repo string) client.Event {
	e := client.Event{ID: id, Type: typ, Actor: client.User{ID: 1, Login: "octo"}, CreatedAt: "2026-10-08T12:00:00Z"}
	e.Repo.Name = repo
	switch typ {
	case "PushEvent":
		e.Payload = json.RawMessage(`{"ref":"refs/heads/main","head":"abc123","commits":[{"sha":"abc123","message":"Fix the build"}]}`)
	case "IssuesEvent":
		e.Payload = json.RawMessage(`{"action":"opened","issue":{"number":7,"title":"Crash on start","html_url":"https://github.com/` + repo + `/issues/7","state":"open","user":{"login":"octo","id":1,"type":"User"}}}`)
	default:
		e.Payload = json.RawMessage(`{}`)
	}
	return e
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/login/device/code":
		writeJSON(w, http.StatusOK, client.DeviceCode{DeviceCode: "dev-sandbox", UserCode: UserCode, VerificationURI: s.srv.URL + "/login/device", ExpiresIn: 900, Interval: 1})
		return
	case "/login/oauth/access_token":
		s.mu.Lock()
		s.polls++
		first := s.polls == 1
		s.mu.Unlock()
		if first {
			writeJSON(w, http.StatusOK, map[string]string{"error": "authorization_pending"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"access_token": Token, "token_type": "bearer"})
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == Revoked {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	if r.URL.Path == "/user" {
		writeJSON(w, http.StatusOK, client.User{ID: 1, Login: "octo", Name: "Octo Cat", Type: "User"})
		return
	}
	if r.URL.Path == "/user/repos" {
		repos := []map[string]any{
			{"id": 1, "full_name": AdminRepo, "private": false, "permissions": map[string]bool{"admin": true}},
			{"id": 2, "full_name": OtherAdmin, "private": true, "permissions": map[string]bool{"admin": true}},
			{"id": 3, "full_name": NoAdminRepo, "private": false, "permissions": map[string]bool{"admin": false}},
		}
		writeJSON(w, http.StatusOK, repos)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/repos/") || len(parts) < 3 {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	repo := parts[0] + "/" + parts[1]
	switch parts[2] {
	case "events":
		s.serveEvents(w, r, repo)
	case "hooks":
		if repo == NoAdminRepo || (repo != AdminRepo && repo != OtherAdmin) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		var id int64
		if len(parts) > 3 {
			id, _ = strconv.ParseInt(parts[3], 10, 64) //nolint:errcheck // a bad id finds no hook
		}
		s.serveHooks(w, r, repo, id)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request, repo string) {
	s.mu.Lock()
	evs := append([]client.Event(nil), s.events[repo]...)
	every := s.every
	s.mu.Unlock()
	etag := `"` + strconv.Itoa(len(evs)) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	if every > 0 {
		w.Header().Set("X-Poll-Interval", strconv.Itoa(every))
	}
	writeJSON(w, http.StatusOK, append([]client.Event{}, evs...))
}

func (s *Server) serveHooks(w http.ResponseWriter, r *http.Request, repo string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hooks := s.hooks[repo]
	idx := -1
	for i, h := range hooks {
		if h.ID == id {
			idx = i
		}
	}
	switch {
	case r.Method == http.MethodGet && id == 0:
		out := make([]client.Hook, 0, len(hooks))
		for _, h := range hooks {
			h.Config.Secret = "********"
			out = append(out, h)
		}
		writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodPost && id == 0:
		var h client.Hook
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil || h.Config.URL == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
			return
		}
		s.nextID++
		h.ID, h.Active = s.nextID, true
		s.hooks[repo] = append(hooks, h)
		if s.deliver {
			s.wg.Add(1)
			go s.deliverNew(repo, h) // ends after two posts with a 5 s timeout
		}
		writeJSON(w, http.StatusCreated, h)
	case idx < 0:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case r.Method == http.MethodPatch:
		var h client.Hook
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
			return
		}
		cur := &hooks[idx]
		if h.Events != nil {
			cur.Events = h.Events
		}
		if h.Config.URL != "" {
			cur.Config = h.Config
		}
		writeJSON(w, http.StatusOK, *cur)
	case r.Method == http.MethodDelete:
		s.hooks[repo] = append(hooks[:idx:idx], hooks[idx+1:]...)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

// deliverNew posts one push and one ping to a new hook, as GitHub does.
func (s *Server) deliverNew(repo string, h client.Hook) {
	defer s.wg.Done()
	push := fmt.Sprintf(`{"ref":"refs/heads/main","before":"000000","after":"abc123","repository":{"id":1,"full_name":%q,"html_url":"https://github.com/%s"},"pusher":{"name":"octo"},"sender":{"login":"octo","id":1,"type":"User"},"head_commit":{"id":"abc123","message":"Fix the build","url":"https://github.com/%s/commit/abc123"},"commits":[{"id":"abc123","message":"Fix the build"}]}`, repo, repo, repo)
	ping := fmt.Sprintf(`{"zen":"Keep it simple.","hook_id":%d,"repository":{"id":1,"full_name":%q},"sender":{"login":"octo","id":1,"type":"User"}}`, h.ID, repo)
	s.post(h.Config, "push", fmt.Sprintf("delivery-%d-push", h.ID), push)
	s.post(h.Config, "ping", fmt.Sprintf("delivery-%d-ping", h.ID), ping)
}

func (s *Server) post(cfg client.HookConfig, event, id, body string) {
	req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewBufferString(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", id)
	if cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write([]byte(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close() //nolint:errcheck,gosec // the answer has no use
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck,gosec // a test server
}
