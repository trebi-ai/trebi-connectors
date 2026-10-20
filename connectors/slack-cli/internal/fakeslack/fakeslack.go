// Package fakeslack is an in-process Slack for `serve --sandbox` and tests:
// a Web API and a desktop client socket on local httptest servers. It has
// one fixed workspace and accepts any token, except the tokens of Reject.
// AddTeam gives a token its own team in auth.test. Each socket connection
// gets the same fixed events, so a restart gives the same event ids.
package fakeslack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/localauth"
)

// Fixed ids of the fake.
const (
	TeamID    = "T0SANDBOX"
	TeamName  = "Sandbox"
	SelfID    = "U0SELF"
	AnaID     = "U0ANA"
	BoID      = "U0BO"
	GeneralID = "C0GENERAL"
	TeamChID  = "C0TEAM"
	DMID      = "D0ANA"
	GroupID   = "G0GROUP"
)

var users = map[string]string{
	SelfID: `{"id":"U0SELF","name":"trebi","profile":{"display_name":"Trebi"}}`,
	AnaID:  `{"id":"U0ANA","name":"ana","profile":{"display_name":"Ana"}}`,
	BoID:   `{"id":"U0BO","name":"bo","real_name":"Bo"}`,
}

var channels = map[string]string{
	GeneralID: `{"id":"C0GENERAL","name":"general","is_channel":true}`,
	TeamChID:  `{"id":"C0TEAM","name":"team","is_channel":true,"is_private":true}`,
	DMID:      `{"id":"D0ANA","is_im":true,"user":"U0ANA"}`,
	GroupID:   `{"id":"G0GROUP","name":"mpdm-trebi--ana--bo-1","is_mpim":true}`,
}

// Events are the raw events that each socket connection gets, in order.
var Events = []string{
	`{"type":"hello"}`,
	`{"type":"message","channel":"C0GENERAL","user":"U0ANA","text":"Hello <@U0SELF>, the report is ready","ts":"1767225600.000100","team":"T0SANDBOX"}`,
	`{"type":"message","channel":"C0GENERAL","user":"U0BO","text":"Thanks!","ts":"1767225600.000200","thread_ts":"1767225600.000100"}`,
	`{"type":"message","channel":"D0ANA","user":"U0ANA","text":"Can you check the build?","ts":"1767225600.000300"}`,
	`{"type":"message","subtype":"bot_message","channel":"C0TEAM","bot_id":"B0CI","username":"CI","text":"","attachments":[{"title":"Build 42","text":"passed"}],"ts":"1767225600.000400"}`,
	`{"type":"user_typing","channel":"C0GENERAL","user":"U0ANA"}`,
	`{"type":"reaction_added","user":"U0BO","reaction":"+1","item":{"type":"message","channel":"C0GENERAL","ts":"1767225600.000100"},"item_user":"U0ANA","event_ts":"1767225600.000500"}`,
	`{"type":"member_joined_channel","user":"U0BO","channel":"C0TEAM","channel_type":"G","team":"T0SANDBOX","event_ts":"1767225600.000600"}`,
	`{"type":"message","subtype":"message_changed","hidden":true,"channel":"C0GENERAL","message":{"type":"message","user":"U0ANA","text":"Hello <@U0SELF>, the report is ready now","ts":"1767225600.000100","edited":{"user":"U0ANA","ts":"1767225600.000700"}},"previous_message":{"type":"message","user":"U0ANA","text":"Hello <@U0SELF>, the report is ready","ts":"1767225600.000100"},"ts":"1767225600.000700","event_ts":"1767225600.000700"}`,
	`{"type":"message","subtype":"message_deleted","hidden":true,"channel":"C0GENERAL","deleted_ts":"1767225600.000200","previous_message":{"type":"message","user":"U0BO","text":"Thanks!","ts":"1767225600.000200","thread_ts":"1767225600.000100"},"ts":"1767225600.000800","event_ts":"1767225600.000800"}`,
	`{"type":"message","channel":"G0GROUP","user":"U0SELF","text":"On it","ts":"1767225600.000900"}`,
}

// history holds the messages of each channel, newest first.
var history = map[string][]string{
	GeneralID: {
		`{"type":"message","user":"U0ANA","text":"Hello <@U0SELF>, the report is ready now","ts":"1767225600.000100","thread_ts":"1767225600.000100","reply_count":1}`,
	},
	TeamChID: {
		`{"type":"message","subtype":"bot_message","bot_id":"B0CI","username":"CI","text":"","attachments":[{"title":"Build 42","text":"passed"}],"ts":"1767225600.000400"}`,
	},
	DMID: {
		`{"type":"message","user":"U0ANA","text":"Can you check the build?","ts":"1767225600.000300"}`,
	},
}

// Server is one fake Slack.
type Server struct {
	api *httptest.Server
	ws  *httptest.Server

	mu       sync.Mutex
	teams    map[string]string // token -> auth.test answer
	rejected map[string]bool
}

// Start runs the fake. Close stops it.
func Start() *Server {
	s := &Server{teams: map[string]string{}, rejected: map[string]bool{}}
	s.api = httptest.NewServer(http.HandlerFunc(s.serveAPI))
	s.ws = httptest.NewServer(http.HandlerFunc(s.serveWS))
	return s
}

// Close stops the servers.
func (s *Server) Close() {
	s.ws.Close()
	s.api.Close()
}

// APIURL is the Web API root.
func (s *Server) APIURL() string { return s.api.URL + "/api/" }

// WSURL is the socket host for listen.WSSource.
func (s *Server) WSURL() string { return "ws" + strings.TrimPrefix(s.ws.URL, "http") }

// Config is a config with one session workspace "sandbox" on the fake.
func (s *Server) Config() *config.Config {
	cfg := &config.Config{Workspace: "sandbox"}
	cfg.Set("sandbox", config.Workspace{
		Token: "xoxc-sandbox", Cookie: "xoxd-sandbox", TeamID: TeamID, TeamName: TeamName,
		Domain: "sandbox", UserID: SelfID, User: "trebi",
	})
	return cfg
}

// Session is the login of the fake workspace on the computer.
func Session() localauth.Session {
	return localauth.Session{TeamID: TeamID, TeamName: TeamName, Domain: "sandbox", Token: "xoxc-sandbox", Cookie: "xoxd-sandbox"}
}

// Local is a Slack login on the computer with the fake workspace.
type Local struct{}

// Workspaces returns the fake workspace.
func (Local) Workspaces() ([]localauth.Session, error) { return []localauth.Session{Session()}, nil }

// Sessions returns the fake workspace.
func (Local) Sessions(context.Context) ([]localauth.Session, error) { return Local{}.Workspaces() }

// AddTeam makes auth.test of token answer with its own team.
func (s *Server) AddTeam(token, teamID, name, domain string) {
	b, _ := json.Marshal(map[string]any{ //nolint:errcheck // strings always encode
		"ok": true, "user_id": SelfID, "user": "trebi", "team_id": teamID, "team": name, "url": "https://" + domain + ".slack.com/",
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teams[token] = string(b)
}

// Reject makes every call with token fail with invalid_auth.
func (s *Server) Reject(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected[token] = true
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	w.Header().Set("Content-Type", "application/json")
	token := r.Form.Get("token")
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	s.mu.Lock()
	team, rejected := s.teams[token], s.rejected[token]
	s.mu.Unlock()
	if rejected {
		writeJSON(w, `{"ok":false,"error":"invalid_auth"}`)
		return
	}
	switch method {
	case "auth.test":
		if team != "" {
			writeJSON(w, team)
			return
		}
		writeJSON(w, `{"ok":true,"user_id":"U0SELF","user":"trebi","team_id":"T0SANDBOX","team":"Sandbox","url":"https://sandbox.slack.com/"}`)
	case "users.info":
		one(w, "user", users[r.Form.Get("user")], "user_not_found")
	case "conversations.info":
		one(w, "channel", channels[r.Form.Get("channel")], "channel_not_found")
	case "conversations.history":
		msgs, ok := history[r.Form.Get("channel")]
		if !ok {
			writeJSON(w, `{"ok":false,"error":"channel_not_found"}`)
			return
		}
		writeJSON(w, `{"ok":true,"has_more":false,"messages":[`+strings.Join(msgs, ",")+`]}`)
	case "conversations.replies":
		if r.Form.Get("channel") != GeneralID || r.Form.Get("ts") != "1767225600.000100" {
			writeJSON(w, `{"ok":false,"error":"thread_not_found"}`)
			return
		}
		writeJSON(w, `{"ok":true,"has_more":false,"messages":[`+history[GeneralID][0]+`,`+
			`{"type":"message","user":"U0BO","text":"Thanks!","ts":"1767225600.000200","thread_ts":"1767225600.000100"}]}`)
	case "search.messages":
		var matches []string
		q := strings.ToLower(r.Form.Get("query"))
		for ch, msgs := range history {
			for _, m := range msgs {
				if strings.Contains(strings.ToLower(m), q) {
					matches = append(matches, `{"channel":`+channels[ch]+`,`+strings.TrimPrefix(m, "{"))
				}
			}
		}
		b, _ := json.Marshal(len(matches)) //nolint:errcheck // an int always encodes
		writeJSON(w, `{"ok":true,"messages":{"total":`+string(b)+`,"matches":[`+strings.Join(matches, ",")+`]}}`)
	default:
		writeJSON(w, `{"ok":false,"error":"unknown_method"}`)
	}
}

func one(w http.ResponseWriter, key, obj, notFound string) {
	if obj == "" {
		writeJSON(w, `{"ok":false,"error":"`+notFound+`"}`)
		return
	}
	writeJSON(w, `{"ok":true,"`+key+`":`+obj+`}`)
}

func writeJSON(w http.ResponseWriter, body string) { _, _ = w.Write([]byte(body)) }

// The client sends the Origin of the Slack web app.
var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// serveWS sends the fixed events, then holds the socket open with pings.
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for _, ev := range Events {
		if conn.WriteMessage(websocket.TextMessage, []byte(ev)) != nil {
			return
		}
	}
	done := make(chan struct{})
	go func() { // reads until the client goes away
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}
