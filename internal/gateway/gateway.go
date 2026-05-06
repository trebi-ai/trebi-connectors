package gateway

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/flarco/cli-tools/discord-cli/internal/client"
)

const gatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"

// Payload is the top-level gateway WebSocket message.
type Payload struct {
	Op int              `json:"op"`
	D  *json.RawMessage `json:"d,omitempty"`
	S  *int             `json:"s,omitempty"`
	T  string           `json:"t,omitempty"`
}

// Hello is the op=10 HELLO payload.
type Hello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// IdentifyData is the data for op=2 IDENTIFY.
type IdentifyData struct {
	Token      string             `json:"token"`
	Intents    int                `json:"intents"`
	Properties IdentifyProperties `json:"properties"`
}

// IdentifyProperties identifies the client.
type IdentifyProperties struct {
	OS      string `json:"os"`
	Browser string `json:"browser"`
	Device  string `json:"device"`
}

// Ready is the READY event payload.
type Ready struct {
	V         int         `json:"v"`
	User      client.User `json:"user"`
	SessionID string      `json:"session_id"`
	Guilds    []struct {
		ID          string `json:"id"`
		Unavailable bool   `json:"unavailable"`
	} `json:"guilds"`
}

// Gateway is a Discord WebSocket Gateway client.
type Gateway struct {
	Token     string
	Intents   int
	conn      *websocket.Conn
	seq       *int
	sessionID string
	stopCh    chan struct{}
	mu        sync.Mutex
}

// New creates a new gateway client.
func New(token string, intents int) *Gateway {
	return &Gateway{
		Token:   token,
		Intents: intents,
		stopCh:  make(chan struct{}),
	}
}

// Connect dials the gateway, completes the handshake, and returns the READY payload.
func (g *Gateway) Connect() (*Ready, error) {
	conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial gateway: %w", err)
	}
	g.conn = conn

	var hello Payload
	if err := conn.ReadJSON(&hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read HELLO: %w", err)
	}
	if hello.Op != 10 {
		conn.Close()
		return nil, fmt.Errorf("expected op=10 HELLO, got op=%d", hello.Op)
	}

	var helloData Hello
	if err := json.Unmarshal(*hello.D, &helloData); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse HELLO: %w", err)
	}

	interval := time.Duration(helloData.HeartbeatInterval) * time.Millisecond
	go g.heartbeatLoop(interval)

	identify := Payload{
		Op: 2,
		D: rawJSON(IdentifyData{
			Token:   g.Token,
			Intents: g.Intents,
			Properties: IdentifyProperties{
				OS:      "linux",
				Browser: "discord-cli",
				Device:  "discord-cli",
			},
		}),
	}
	if err := conn.WriteJSON(identify); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send IDENTIFY: %w", err)
	}

	var readyPayload Payload
	if err := conn.ReadJSON(&readyPayload); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read READY: %w", err)
	}
	if readyPayload.Op != 0 || readyPayload.T != "READY" {
		conn.Close()
		return nil, fmt.Errorf("expected READY, got op=%d t=%s", readyPayload.Op, readyPayload.T)
	}
	if readyPayload.S != nil {
		g.seq = readyPayload.S
	}

	var ready Ready
	if err := json.Unmarshal(*readyPayload.D, &ready); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse READY: %w", err)
	}
	g.sessionID = ready.SessionID

	return &ready, nil
}

// Listen reads events from the gateway and calls handler for each dispatch event.
// Blocks until the connection closes or Close() is called.
func (g *Gateway) Listen(handler func(eventType string, data json.RawMessage)) error {
	for {
		select {
		case <-g.stopCh:
			return nil
		default:
		}

		var payload Payload
		if err := g.conn.ReadJSON(&payload); err != nil {
			select {
			case <-g.stopCh:
				return nil
			default:
				return fmt.Errorf("read: %w", err)
			}
		}

		if payload.S != nil {
			g.mu.Lock()
			g.seq = payload.S
			g.mu.Unlock()
		}

		switch payload.Op {
		case 0: // DISPATCH
			if handler != nil && payload.D != nil {
				handler(payload.T, json.RawMessage(*payload.D))
			}
		case 1: // HEARTBEAT request
			g.sendHeartbeat()
		case 11: // HEARTBEAT_ACK
			// no-op
		}
	}
}

// Close cleanly shuts down the gateway connection.
func (g *Gateway) Close() {
	select {
	case <-g.stopCh:
	default:
		close(g.stopCh)
	}
	if g.conn != nil {
		g.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		g.conn.Close()
	}
}

func (g *Gateway) heartbeatLoop(interval time.Duration) {
	jitter := time.Duration(rand.Float64() * float64(interval))
	select {
	case <-time.After(jitter):
	case <-g.stopCh:
		return
	}
	g.sendHeartbeat()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.sendHeartbeat()
		case <-g.stopCh:
			return
		}
	}
}

func (g *Gateway) sendHeartbeat() {
	g.mu.Lock()
	seq := g.seq
	g.mu.Unlock()

	payload := map[string]any{"op": 1, "d": seq}
	g.conn.WriteJSON(payload)
}

func rawJSON(v any) *json.RawMessage {
	data, _ := json.Marshal(v)
	raw := json.RawMessage(data)
	return &raw
}
