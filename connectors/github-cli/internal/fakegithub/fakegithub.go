// Package fakegithub is an in-process GitHub for the sandbox and the
// tests: the device flow, repositories, events, Actions runs,
// notifications, repository webhooks, and the calls of the operations. A
// new hook gets one signed push delivery, one review comment delivery, then
// a ping. The first notifications read adds one review request.
package fakegithub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
)

// Values of the fake.
const (
	Token       = "gho_sandbox"
	FineGrained = "github_pat_sandbox" // no scopes header, no notifications
	Revoked     = "revoked"            // a token that gives 401
	NoHookScope = "gho_nohooks"        // a classic token with read:org only
	Idle        = "gho_idle"           // an account with no issues or pull requests
	Scopes      = "repo, read:org, admin:repo_hook"
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
	runs    map[string][]client.Run
	notifs  []client.Notification
	nreads  int
	stamp   int // the Last-Modified of the notifications, in seconds
	calls   []string
	nextObj int64
	refuse  bool // hook creation gives 404
}

// RefuseHooks makes each new hook fail with 404, as for a login whose
// organization blocks the OAuth app.
func (s *Server) RefuseHooks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = true
}

// Start runs the fake on a local port.
func Start() *Server {
	s := &Server{
		client:  &http.Client{Timeout: 5 * time.Second},
		hooks:   map[string][]client.Hook{},
		events:  map[string][]client.Event{},
		deliver: true,
		every:   60,
		runs:    map[string][]client.Run{},
		nextObj: 100,
	}
	s.events[AdminRepo] = []client.Event{s.event("1", "PushEvent", AdminRepo)}
	s.runs[AdminRepo] = []client.Run{run(AdminRepo, 1, "completed", "success")}
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
	s.mu.Lock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	switch r.URL.Path {
	case "/user":
		switch token {
		case FineGrained:
		case NoHookScope:
			w.Header().Set("X-OAuth-Scopes", "read:org")
		default:
			w.Header().Set("X-OAuth-Scopes", Scopes)
		}
		writeJSON(w, http.StatusOK, client.User{ID: 1, Login: "octo", Name: "Octo Cat", Type: "User"})
		return
	case "/user/repos":
		repos := make([]map[string]any, 0, len(repoList))
		for _, name := range repoList {
			repos = append(repos, repoJSON(name))
		}
		writeJSON(w, http.StatusOK, repos)
		return
	case "/notifications":
		if token == FineGrained {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by personal access token"})
			return
		}
		s.serveNotifications(w, r)
		return
	case "/search/issues":
		if token == Idle {
			writeJSON(w, http.StatusOK, map[string]any{"total_count": 0, "items": []any{}})
			return
		}
		s.serveSearch(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/repos/") || len(parts) < 2 || !slices.Contains(repoList, parts[0]+"/"+parts[1]) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	repo := parts[0] + "/" + parts[1]
	if len(parts) == 2 {
		writeJSON(w, http.StatusOK, repoJSON(repo))
		return
	}
	switch parts[2] {
	case "issues", "pulls", "commits", "git":
		s.serveObjects(w, r, repo, parts[2:])
	case "actions":
		if len(parts) > 4 && parts[3] == "runs" {
			s.serveRerun(w, r, repo, parts[4:])
			return
		}
		s.serveRuns(w, r, repo)
	case "events":
		s.serveEvents(w, r, repo)
	case "hooks":
		if repo == NoAdminRepo {
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
	case r.Method == http.MethodPost && id == 0 && s.refuse:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
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

// deliverNew posts one push, one review comment, and one ping to a new
// hook.
func (s *Server) deliverNew(repo string, h client.Hook) {
	defer s.wg.Done()
	push := fmt.Sprintf(`{"ref":"refs/heads/main","before":"000000","after":"abc123","repository":{"id":1,"full_name":%q,"html_url":"https://github.com/%s"},"pusher":{"name":"octo"},"sender":{"login":"octo","id":1,"type":"User"},"head_commit":{"id":"abc123","message":"Fix the build","url":"https://github.com/%s/commit/abc123"},"commits":[{"id":"abc123","message":"Fix the build"}]}`, repo, repo, repo)
	ping := fmt.Sprintf(`{"zen":"Keep it simple.","hook_id":%d,"repository":{"id":1,"full_name":%q},"sender":{"login":"octo","id":1,"type":"User"}}`, h.ID, repo)
	comment := fmt.Sprintf(`{"action":"created","repository":{"id":1,"full_name":%q},"sender":{"login":"hubot","id":2,"type":"User"},"pull_request":{"number":8,"title":"Add a cache","html_url":"https://github.com/%s/pull/8","state":"open","user":{"login":"hubot","id":2,"type":"User"}},"comment":{"id":31,"body":"Use a map here.","path":"cache.go","line":12,"html_url":"https://github.com/%s/pull/8#discussion_r31","user":{"login":"hubot","id":2,"type":"User"}}}`, repo, repo, repo)
	s.post(h.Config, "push", fmt.Sprintf("delivery-%d-push", h.ID), push)
	s.post(h.Config, "pull_request_review_comment", fmt.Sprintf("delivery-%d-comment", h.ID), comment)
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

// GHLogin is a gh login on this machine with the fake token.
type GHLogin struct{}

// Token returns the fake token.
func (GHLogin) Token(context.Context) (string, error) { return Token, nil }

// repoList holds the repositories of the account. octo/app and octo/docs
// are admin; other/lib is read only.
var repoList = []string{AdminRepo, OtherAdmin, NoAdminRepo} //nolint:gochecknoglobals // fixed list, never changed

func repoJSON(name string) map[string]any {
	admin := name != NoAdminRepo
	return map[string]any{
		"id": slices.Index(repoList, name) + 1, "full_name": name, "private": name == OtherAdmin, "default_branch": "main",
		"permissions": map[string]bool{"admin": admin, "push": admin, "pull": true},
	}
}

// Calls returns the "METHOD /path" of each API call with a token.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func run(repo string, id int64, status, conclusion string) client.Run {
	r := client.Run{ID: id, Name: "CI", Status: status, Conclusion: conclusion, HeadBranch: "main", Event: "push",
		HTMLURL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, id), UpdatedAt: "2026-10-08T12:00:00Z"}
	r.Actor = client.User{ID: 1, Login: "octo", Type: "User"}
	return r
}

// SetRun adds a workflow run or changes its status.
func (s *Server) SetRun(repo string, id int64, status, conclusion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := s.runs[repo]
	for i := range runs {
		if runs[i].ID == id {
			runs[i].Status, runs[i].Conclusion = status, conclusion
			return
		}
	}
	s.runs[repo] = append([]client.Run{run(repo, id, status, conclusion)}, runs...)
}

func (s *Server) serveRuns(w http.ResponseWriter, r *http.Request, repo string) {
	s.mu.Lock()
	runs := slices.Clone(s.runs[repo])
	s.mu.Unlock()
	if runs == nil {
		runs = []client.Run{}
	}
	data, _ := json.Marshal(runs) //nolint:errcheck // plain values
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "workflow_runs": runs})
}

func (s *Server) serveRerun(w http.ResponseWriter, r *http.Request, repo string, parts []string) {
	id, _ := strconv.ParseInt(parts[0], 10, 64) //nolint:errcheck // a bad id finds no run
	s.mu.Lock()
	found := slices.ContainsFunc(s.runs[repo], func(x client.Run) bool { return x.ID == id })
	s.mu.Unlock()
	switch {
	case !found:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case r.Method != http.MethodPost || len(parts) != 2 || (parts[1] != "rerun" && parts[1] != "rerun-failed-jobs"):
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case repo == NoAdminRepo:
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Must have admin rights to Repository."})
	default:
		w.WriteHeader(http.StatusCreated)
	}
}

// AddNotification adds a thread, or updates it, and moves Last-Modified.
func (s *Server) AddNotification(id, reason, repo string, number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addNotification(id, reason, repo, number)
}

func (s *Server) addNotification(id, reason, repo string, number int) {
	s.stamp++
	n := client.Notification{ID: id, Reason: reason, Unread: true, UpdatedAt: time.Unix(1791460800+int64(s.stamp), 0).UTC().Format(time.RFC3339)}
	n.Repository.FullName = repo
	n.Subject.Type = "PullRequest"
	n.Subject.Title = "Add a cache"
	n.Subject.URL = fmt.Sprintf("%s/repos/%s/pulls/%d", s.srv.URL, repo, number)
	if reason == "mention" {
		n.Subject.LatestCommentURL = fmt.Sprintf("%s/repos/%s/issues/comments/41", s.srv.URL, repo)
	}
	s.notifs = slices.DeleteFunc(s.notifs, func(x client.Notification) bool { return x.ID == id })
	s.notifs = append([]client.Notification{n}, s.notifs...)
}

func (s *Server) serveNotifications(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nreads++
	last := time.Unix(1791460800+int64(s.stamp), 0).UTC().Format(http.TimeFormat)
	if s.every > 0 {
		w.Header().Set("X-Poll-Interval", strconv.Itoa(s.every))
	}
	w.Header().Set("Last-Modified", last)
	if r.Header.Get("If-Modified-Since") == last {
		w.WriteHeader(http.StatusNotModified)
	} else {
		writeJSON(w, http.StatusOK, append([]client.Notification{}, s.notifs...))
	}
	if s.nreads == 1 {
		s.addNotification("9001", "review_requested", AdminRepo, 8)
	}
}

// The issues and pull requests of octo/app: 7 is an issue, 8 is a pull
// request of hubot, 9 is a pull request of octo that cannot merge.
func (s *Server) issue(repo string, number int) (map[string]any, bool) {
	if repo != AdminRepo || number < 7 || number > 9 {
		return nil, false
	}
	author, title := "hubot", "Add a cache"
	if number == 7 {
		title = "Crash on start"
	}
	if number == 9 {
		author, title = "octo", "Rename the flag"
	}
	m := map[string]any{
		"number": number, "title": title, "state": "open", "body": "Body of " + title, "comments": 2,
		"html_url":       fmt.Sprintf("https://github.com/%s/%s/%d", repo, map[bool]string{true: "issues", false: "pull"}[number == 7], number),
		"repository_url": s.srv.URL + "/repos/" + repo, "updated_at": "2026-10-08T12:00:00Z",
		"user": map[string]any{"login": author, "id": 2, "type": "User"}, "labels": []map[string]string{{"name": "bug"}},
		"assignees": []map[string]any{{"login": "octo", "id": 1}},
	}
	if number != 7 {
		m["pull_request"] = map[string]string{"url": fmt.Sprintf("%s/repos/%s/pulls/%d", s.srv.URL, repo, number)}
	}
	return m, true
}

func (s *Server) serveSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.Contains(q, "is:invalid") {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
		return
	}
	var items []map[string]any
	for n := 7; n <= 9; n++ {
		it, _ := s.issue(AdminRepo, n)
		switch {
		case strings.Contains(q, "review-requested:@me") && n != 8,
			strings.Contains(q, "author:@me") && n != 9,
			strings.Contains(q, "is:pr") && n == 7,
			strings.Contains(q, "assignee:@me") && n != 7:
			continue
		}
		items = append(items, it)
	}
	if per, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && per < len(items) {
		items = items[:per]
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(items), "items": append([]map[string]any{}, items...)})
}

// serveObjects answers the issues, pulls, commits, and git calls of the
// operations and of the activity reader.
func (s *Server) serveObjects(w http.ResponseWriter, r *http.Request, repo string, p []string) {
	notFound := func() { writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"}) }
	num := 0
	if len(p) > 1 {
		num, _ = strconv.Atoi(p[1]) //nolint:errcheck // a bad number finds nothing
	}
	s.mu.Lock()
	s.nextObj++
	obj := s.nextObj
	s.mu.Unlock()
	switch {
	case p[0] == "issues" && len(p) == 1 && r.Method == http.MethodPost:
		var in struct {
			Title string `json:"title"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Title == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": obj, "number": 10, "html_url": "https://github.com/" + repo + "/issues/10"})
	case p[0] == "issues" && len(p) == 3 && p[1] == "comments":
		writeJSON(w, http.StatusOK, map[string]any{"id": 41, "body": "@octo please look", "html_url": "https://github.com/" + repo + "/pull/8#issuecomment-41", "user": map[string]any{"login": "monalisa", "id": 3, "type": "User"}})
	case p[0] == "issues" && len(p) == 2:
		it, ok := s.issue(repo, num)
		if !ok {
			notFound()
			return
		}
		writeJSON(w, http.StatusOK, it)
	case p[0] == "issues" && len(p) == 3 && p[2] == "comments":
		if _, ok := s.issue(repo, num); !ok {
			notFound()
			return
		}
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusCreated, map[string]any{"id": obj, "html_url": fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", repo, num, obj)})
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{
			{"user": map[string]any{"login": "hubot"}, "created_at": "2026-10-08T12:00:00Z", "body": "First"},
			{"user": map[string]any{"login": "octo"}, "created_at": "2026-10-08T12:05:00Z", "body": strings.Repeat("x", 9000)},
		})
	case p[0] == "pulls" && len(p) == 2:
		it, ok := s.issue(repo, num)
		if !ok || num == 7 {
			notFound()
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"number": num, "title": it["title"], "html_url": it["html_url"], "user": it["user"], "draft": false, "merged": false, "mergeable_state": "clean",
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": repo, "default_branch": "main"}},
			"head": map[string]any{"ref": fmt.Sprintf("feature-%d", num), "sha": fmt.Sprintf("sha%d", num), "repo": map[string]any{"full_name": repo}},
		})
	case p[0] == "pulls" && len(p) == 3 && p[2] == "reviews":
		if _, ok := s.issue(repo, num); !ok || num == 7 {
			notFound()
			return
		}
		if r.Method == http.MethodPost {
			var in struct {
				Event string `json:"event"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in) //nolint:errcheck // an empty event fails below
			if num == 9 && in.Event == "APPROVE" {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Can not approve your own pull request"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": obj, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d#pullrequestreview-%d", repo, num, obj)})
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{
			{"user": map[string]any{"login": "monalisa"}, "state": "CHANGES_REQUESTED"},
			{"user": map[string]any{"login": "hubot"}, "state": "COMMENTED"},
			{"user": map[string]any{"login": "monalisa"}, "state": "APPROVED"},
		})
	case p[0] == "pulls" && len(p) == 3 && p[2] == "merge" && r.Method == http.MethodPut:
		if num == 9 {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Pull Request is not mergeable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sha": "merged1", "merged": true, "message": "Pull Request successfully merged"})
	case p[0] == "commits" && len(p) == 3 && p[2] == "check-runs":
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]string{
			{"name": "build", "status": "completed", "conclusion": "success"},
			{"name": "lint", "status": "completed", "conclusion": "failure"},
			{"name": "e2e", "status": "in_progress"},
		}})
	case p[0] == "git" && len(p) >= 4 && p[1] == "refs" && p[2] == "heads" && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound()
	}
}
