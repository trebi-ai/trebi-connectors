package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

// mockGateway serves HELLO then a scripted reply after IDENTIFY.
// replies[i] is the payload sent after the i-th IDENTIFY (0-based).
func mockGateway(t *testing.T, replies []Payload) (wsURL string, identifies *atomic.Int32) {
	t.Helper()
	identifies = &atomic.Int32{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		helloD, _ := json.Marshal(Hello{HeartbeatInterval: 60000})
		raw := json.RawMessage(helloD)
		_ = conn.WriteJSON(Payload{Op: 10, D: &raw})

		var identify Payload
		if err := conn.ReadJSON(&identify); err != nil {
			return
		}
		if identify.Op != 2 {
			return
		}
		idx := int(identifies.Add(1) - 1)

		if idx >= len(replies) {
			return
		}
		_ = conn.WriteJSON(replies[idx])

		// Keep the socket open briefly so a successful Connect can finish.
		time.Sleep(50 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)

	wsURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	return wsURL, identifies
}

func readyPayload(user, session string) Payload {
	body, _ := json.Marshal(map[string]any{
		"v":          10,
		"user":       map[string]any{"id": "1", "username": user},
		"session_id": session,
		"guilds":     []any{},
	})
	raw := json.RawMessage(body)
	s := 1
	return Payload{Op: 0, T: "READY", S: &s, D: &raw}
}

func invalidSessionPayload() Payload {
	// d=false: session not resumable
	raw := json.RawMessage(`false`)
	return Payload{Op: 9, D: &raw}
}

func TestConnectRetriesInvalidSession(t *testing.T) {
	wsURL, identifies := mockGateway(t, []Payload{
		invalidSessionPayload(),
		invalidSessionPayload(),
		readyPayload("TestBot", "sess-ok"),
	})

	gw := New("fake-token", 1)
	gw.DialURL = wsURL
	gw.InvalidSessionBackoff = func(int) time.Duration { return 0 }

	ready, err := gw.Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer gw.Close()

	if ready.User.Username != "TestBot" {
		t.Fatalf("username = %q, want TestBot", ready.User.Username)
	}
	if ready.SessionID != "sess-ok" {
		t.Fatalf("session = %q, want sess-ok", ready.SessionID)
	}
	if got := identifies.Load(); got != 3 {
		t.Fatalf("identifies = %d, want 3", got)
	}
}

func TestConnectExhaustsInvalidSessionRetries(t *testing.T) {
	replies := make([]Payload, maxIdentifyAttempts)
	for i := range replies {
		replies[i] = invalidSessionPayload()
	}
	wsURL, identifies := mockGateway(t, replies)

	gw := New("fake-token", 1)
	gw.DialURL = wsURL
	gw.InvalidSessionBackoff = func(int) time.Duration { return 0 }

	_, err := gw.Connect()
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "invalid session") {
		t.Fatalf("error = %v, want invalid session", err)
	}
	if got := identifies.Load(); got != int32(maxIdentifyAttempts) {
		t.Fatalf("identifies = %d, want %d", got, maxIdentifyAttempts)
	}
}

func TestConnectNonRetryableError(t *testing.T) {
	// op=7 is RECONNECT — not READY and not invalid session; should fail immediately.
	wsURL, identifies := mockGateway(t, []Payload{{Op: 7}})

	gw := New("fake-token", 1)
	gw.DialURL = wsURL
	gw.InvalidSessionBackoff = func(int) time.Duration { return 0 }

	_, err := gw.Connect()
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "invalid session") {
		t.Fatalf("should not treat op=7 as invalid session: %v", err)
	}
	if got := identifies.Load(); got != 1 {
		t.Fatalf("identifies = %d, want 1 (no retry)", got)
	}
}
