// Package fakelinear is an in-process fake of the Linear GraphQL API for
// tests and `serve --sandbox`. It has two teams, one issue and one comment
// in each, and webhooks. A new webhook gets one signed delivery.
package fakelinear

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/client"
)

var opRE = regexp.MustCompile(`^\s*(?:query|mutation)\s+(\w+)`)

// Server is one fake Linear workspace.
type Server struct {
	srv  *httptest.Server
	post *http.Client
	wg   sync.WaitGroup // deliveries in flight

	mu       sync.Mutex
	admin    bool
	seq      int
	teams    []client.Team
	issues   []client.Issue
	comments []client.Comment
	hooks    []client.Webhook
	creates  int
	updates  int
	deletes  int
}

// Start runs a fake with the teams ENG and OPS.
func Start() *Server {
	old := "2026-01-01T00:00:00.000Z"
	s := &Server{
		admin: true,
		post:  &http.Client{Timeout: 5 * time.Second},
		teams: []client.Team{{ID: "team-eng", Key: "ENG", Name: "Engineering"}, {ID: "team-ops", Key: "OPS", Name: "Operations"}},
	}
	for i, t := range s.teams {
		t := t
		is := client.Issue{
			ID: "issue-" + t.Key, Identifier: t.Key + "-1", Title: "First issue of " + t.Name, URL: "https://linear.app/sandbox/issue/" + t.Key + "-1",
			CreatedAt: old, UpdatedAt: old, Team: &t, Creator: &client.User{ID: "u-sam", Name: "Sam"}, State: &client.State{Name: "Todo"},
		}
		s.issues = append(s.issues, is)
		s.comments = append(s.comments, client.Comment{
			ID: "comment-" + strconv.Itoa(i+1), Body: "Looks good.", CreatedAt: old, UpdatedAt: old,
			User: &client.User{ID: "u-sam", Name: "Sam"}, Issue: &is,
		})
	}
	s.srv = httptest.NewServer(s)
	return s
}

// URL is the GraphQL endpoint.
func (s *Server) URL() string { return s.srv.URL + "/graphql" }

// Close stops the server and waits for the deliveries.
func (s *Server) Close() {
	s.srv.Close()
	s.wg.Wait()
}

// SetAdmin sets whether the key may make webhooks.
func (s *Server) SetAdmin(admin bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admin = admin
}

// Webhooks returns the webhooks now.
func (s *Server) Webhooks() []client.Webhook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hooks)
}

// Counts returns the number of creates, updates, and deletes.
func (s *Server) Counts() (creates, updates, deletes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates, s.updates, s.deletes
}

// AddIssue adds an issue that changed now.
func (s *Server) AddIssue(teamKey, title string) client.Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	i := slices.IndexFunc(s.teams, func(t client.Team) bool { return t.Key == teamKey })
	t := s.teams[max(i, 0)]
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	is := client.Issue{
		ID: "issue-new-" + strconv.Itoa(s.seq), Identifier: t.Key + "-" + strconv.Itoa(100+s.seq), Title: title,
		CreatedAt: now, UpdatedAt: now, Team: &t, Creator: &client.User{ID: "u-sam", Name: "Sam"}, State: &client.State{Name: "Todo"},
	}
	s.issues = append(s.issues, is)
	return is
}

type gqlError struct {
	Message    string            `json:"message"`
	Extensions map[string]string `json:"extensions"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"errors": []gqlError{{Message: "Authentication required", Extensions: map[string]string{"type": "authentication error"}}}}) //nolint:errcheck,gosec // test fake
		return
	}
	var req struct {
		Query     string                     `json:"query"`
		Variables map[string]json.RawMessage `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := opRE.FindStringSubmatch(req.Query)
	if m == nil {
		http.Error(w, "no operation name", http.StatusBadRequest)
		return
	}
	data, gerr := s.op(m[1], req.Variables)
	if gerr != nil {
		json.NewEncoder(w).Encode(map[string]any{"errors": []gqlError{*gerr}}) //nolint:errcheck,gosec // test fake
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data}) //nolint:errcheck,gosec // test fake
}

func (s *Server) op(name string, vars map[string]json.RawMessage) (any, *gqlError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodes := func(v any) map[string]any { return map[string]any{"nodes": v} }
	switch name {
	case "Viewer":
		return map[string]any{"viewer": client.User{ID: "u-ana", Name: "Ana", Email: "ana@example.com"}}, nil
	case "Teams":
		return map[string]any{"teams": nodes(s.teams)}, nil
	case "Issues":
		after := gtAfter(vars["filter"])
		out := []client.Issue{}
		for _, is := range s.issues {
			if is.UpdatedAt > after {
				out = append(out, is)
			}
		}
		return map[string]any{"issues": nodes(out)}, nil
	case "Comments":
		after := gtAfter(vars["filter"])
		out := []client.Comment{}
		for _, c := range s.comments {
			if c.UpdatedAt > after {
				out = append(out, c)
			}
		}
		return map[string]any{"comments": nodes(out)}, nil
	case "Webhooks":
		out := make([]client.Webhook, len(s.hooks))
		for i, h := range s.hooks {
			h.Secret = ""
			out[i] = h
		}
		return map[string]any{"webhooks": nodes(out)}, nil
	case "WebhookCreate":
		return s.create(vars["input"])
	case "WebhookUpdate":
		var id string
		var in struct {
			ResourceTypes []string `json:"resourceTypes"`
		}
		json.Unmarshal(vars["id"], &id)    //nolint:errcheck,gosec // test fake
		json.Unmarshal(vars["input"], &in) //nolint:errcheck,gosec // test fake
		i := slices.IndexFunc(s.hooks, func(h client.Webhook) bool { return h.ID == id })
		if i < 0 {
			return nil, &gqlError{Message: "Entity not found", Extensions: map[string]string{"type": "invalid input"}}
		}
		s.hooks[i].ResourceTypes = in.ResourceTypes
		s.updates++
		return map[string]any{"webhookUpdate": map[string]any{"success": true}}, nil
	case "WebhookDelete":
		var id string
		json.Unmarshal(vars["id"], &id) //nolint:errcheck,gosec // test fake
		n := len(s.hooks)
		s.hooks = slices.DeleteFunc(s.hooks, func(h client.Webhook) bool { return h.ID == id })
		if len(s.hooks) == n {
			return nil, &gqlError{Message: "Entity not found", Extensions: map[string]string{"type": "invalid input"}}
		}
		s.deletes++
		return map[string]any{"webhookDelete": map[string]any{"success": true}}, nil
	}
	return nil, &gqlError{Message: "unknown operation " + name}
}

func gtAfter(raw json.RawMessage) string {
	var f struct {
		UpdatedAt struct {
			Gt string `json:"gt"`
		} `json:"updatedAt"`
	}
	json.Unmarshal(raw, &f) //nolint:errcheck,gosec // no filter is the zero value
	return f.UpdatedAt.Gt
}

// create makes a webhook and starts its delivery. The caller holds mu.
func (s *Server) create(raw json.RawMessage) (any, *gqlError) {
	var in struct {
		URL            string   `json:"url"`
		TeamID         string   `json:"teamId"`
		AllPublicTeams bool     `json:"allPublicTeams"`
		ResourceTypes  []string `json:"resourceTypes"`
		Secret         string   `json:"secret"`
		Label          string   `json:"label"`
	}
	json.Unmarshal(raw, &in) //nolint:errcheck,gosec // test fake
	if !s.admin {
		return nil, &gqlError{Message: "You need to be an admin to create webhooks", Extensions: map[string]string{"type": "forbidden"}}
	}
	h := client.Webhook{URL: in.URL, Label: in.Label, Enabled: true, AllPublicTeams: in.TeamID == "", ResourceTypes: in.ResourceTypes, Secret: in.Secret}
	team := s.teams[0]
	if in.TeamID != "" {
		i := slices.IndexFunc(s.teams, func(t client.Team) bool { return t.ID == in.TeamID })
		if i < 0 {
			return nil, &gqlError{Message: "Team not found", Extensions: map[string]string{"type": "invalid input"}}
		}
		team = s.teams[i]
		h.Team = &team
	}
	if h.Secret == "" {
		b := make([]byte, 16)
		rand.Read(b) //nolint:errcheck,gosec // crypto/rand does not fail
		h.Secret = "lin_wh_" + hex.EncodeToString(b)
	}
	s.seq++
	h.ID = "webhook-" + strconv.Itoa(s.seq)
	s.hooks = append(s.hooks, h)
	s.creates++
	s.deliver(h, team)
	return map[string]any{"webhookCreate": map[string]any{"success": true, "webhook": h}}, nil
}

// deliver posts one signed change to a new webhook. The caller holds mu.
func (s *Server) deliver(h client.Webhook, team client.Team) {
	var body map[string]any
	actor := map[string]any{"id": "u-sam", "name": "Sam", "type": "user"}
	now := time.Now().UTC().Format(time.RFC3339)
	is := map[string]any{
		"id": "sandbox-issue-" + strconv.Itoa(s.seq), "identifier": team.Key + "-" + strconv.Itoa(s.seq), "title": "Sandbox issue",
		"url": "https://linear.app/sandbox/issue/" + team.Key, "team": team, "teamId": team.ID, "state": map[string]any{"name": "Todo"},
	}
	switch {
	case slices.Contains(h.ResourceTypes, "Issue"):
		body = map[string]any{"action": "create", "type": "Issue", "createdAt": now, "data": is, "actor": actor, "webhookId": h.ID}
	case slices.Contains(h.ResourceTypes, "Comment"):
		body = map[string]any{"action": "create", "type": "Comment", "createdAt": now, "actor": actor, "webhookId": h.ID,
			"data": map[string]any{"id": "sandbox-comment-" + strconv.Itoa(s.seq), "body": "Hello from the sandbox.", "issue": is, "user": actor}}
	default:
		return
	}
	b, _ := json.Marshal(body) //nolint:errcheck // plain maps always encode
	mac := hmac.New(sha256.New, []byte(h.Secret))
	mac.Write(b)
	sig, delivery, event := hex.EncodeToString(mac.Sum(nil)), "sandbox-delivery-"+strconv.Itoa(s.seq), body["type"].(string)
	s.wg.Add(1)
	go func() { // ends after one POST, bounded by the client timeout
		defer s.wg.Done()
		req, err := http.NewRequest(http.MethodPost, h.URL, bytes.NewReader(b))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Linear-Signature", sig)
		req.Header.Set("Linear-Delivery", delivery)
		req.Header.Set("Linear-Event", event)
		if resp, err := s.post.Do(req); err == nil {
			resp.Body.Close() //nolint:errcheck,gosec // the status is not read
		}
	}()
}
