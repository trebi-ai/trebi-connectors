// Package fakeaxiom is an httptest server for the monitor part of the Axiom
// API: GET /v2/monitors and GET /v2/monitors/{id}/history. The tests and
// `serve --sandbox` use it.
package fakeaxiom

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Token is the only token that the server accepts.
const Token = "xaat-sandbox"

// Monitor is one monitor as the API gives it.
type Monitor struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Datasets      []string `json:"datasets,omitempty"`
	APLQuery      string   `json:"aplQuery,omitempty"`
	DisabledUntil string   `json:"disabledUntil,omitempty"`
}

// Check is one history entry.
type Check struct {
	CheckID   string `json:"checkId"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Timestamp string `json:"timestamp"`
}

// Server is a running fake.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	monitors []Monitor
	history  map[string][]Check
	auto     bool
	lists    int
	fail     int // status of each reply when not 0
}

// Start runs the fake with two monitors: "m-errors" (Threshold) and
// "m-panic" (MatchEvent).
func Start() *Server {
	s := &Server{
		monitors: []Monitor{
			{ID: "m-errors", Name: "Too many errors", Type: "Threshold", Datasets: []string{"app-logs"}, APLQuery: "['app-logs'] | where level == 'error' | summarize count()"},
			{ID: "m-panic", Name: "Panic in logs", Type: "MatchEvent", Datasets: []string{"app-logs"}, APLQuery: "['app-logs'] | where message contains 'panic'"},
		},
		history: map[string][]Check{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL is the base URL of the API.
func (s *Server) URL() string { return s.srv.URL }

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Auto adds a check to m-errors at each list after the first, so the
// state goes open, closed, open, and so on (sandbox).
func (s *Server) Auto() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auto = true
}

// Fail makes each reply have the status code; 0 stops it.
func (s *Server) Fail(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = code
}

// AddCheck adds a history entry. state is "open" or "closed".
func (s *Server) AddCheck(monitorID, state string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addCheck(monitorID, state, at)
}

func (s *Server) addCheck(monitorID, state string, at time.Time) {
	name := ""
	for _, m := range s.monitors {
		if m.ID == monitorID {
			name = m.Name
		}
	}
	word := "Triggered: "
	if state == "closed" {
		word = "Resolved: "
	}
	h := s.history[monitorID]
	s.history[monitorID] = append(h, Check{CheckID: monitorID + "-" + at.UTC().Format("20060102T150405.000"), Name: word + name, State: state, Timestamp: at.UTC().Format(time.RFC3339Nano)})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != 0 {
		http.Error(w, `{"message":"fake failure"}`, s.fail)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"invalid token"}`)) //nolint:errcheck,gosec // test server
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/monitors":
		s.lists++
		if s.auto && s.lists > 1 {
			state := "open"
			if s.lists%2 == 1 {
				state = "closed"
			}
			s.addCheck("m-errors", state, time.Now())
		}
		reply(w, s.monitors)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/monitors/") && strings.HasSuffix(r.URL.Path, "/history"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/monitors/"), "/history")
		from, err1 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("startTime"))
		to, err2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("endTime"))
		if err1 != nil || err2 != nil {
			http.Error(w, `{"message":"startTime and endTime are required"}`, http.StatusBadRequest)
			return
		}
		out := []Check{}
		for _, c := range s.history[id] {
			at, _ := time.Parse(time.RFC3339Nano, c.Timestamp) //nolint:errcheck // the fake writes it
			if !at.Before(from) && !at.After(to) {
				out = append(out, c)
			}
		}
		// The API gives the newest first.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		reply(w, out)
	default:
		http.NotFound(w, r)
	}
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck,gosec // test server
}
