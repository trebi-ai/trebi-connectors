package listen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/config"
)

// TestSocketAckAndEvent starts a fake Socket Mode server: it serves
// apps.connections.open with a wss URL, then on the socket sends hello and one
// events_api envelope, and checks the client acks it and emits the event.
func TestSocketAckAndEvent(t *testing.T) {
	up := websocket.Upgrader{}
	acked := make(chan string, 1)

	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"hello"}`))
		env := `{"type":"events_api","envelope_id":"env-1","payload":{"event":{"type":"message","channel":"C1","user":"U1","ts":"1.1","text":"hi"}}}`
		conn.WriteMessage(websocket.TextMessage, []byte(env))
		// Read the ack.
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var ack struct {
			EnvelopeID string `json:"envelope_id"`
		}
		_ = json.Unmarshal(msg, &ack)
		acked <- ack.EnvelopeID
	}))
	defer ws.Close()
	wsURL := "ws" + strings.TrimPrefix(ws.URL, "http")

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/apps.connections.open") {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": wsURL})
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()
	t.Setenv("SLACK_API_URL", api.URL)

	cl := client.New(config.Workspace{Token: "xapp-1"})
	src := &SocketSource{Workspace: "acme", TeamID: "T1", AppClient: cl}

	out := make(chan Event, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go src.Run(ctx, out)

	select {
	case id := <-acked:
		if id != "env-1" {
			t.Errorf("acked envelope = %q", id)
		}
	case <-ctx.Done():
		t.Fatal("no ack received")
	}
	select {
	case e := <-out:
		if e.Type != "message" || e.Channel != "C1" || e.Text != "hi" {
			t.Errorf("event = %+v", e)
		}
	case <-ctx.Done():
		t.Fatal("no event received")
	}
}
