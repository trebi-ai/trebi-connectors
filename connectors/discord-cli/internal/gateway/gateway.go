package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
)

const gatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"

// maxIdentifyAttempts caps IDENTIFY retries after op=9 Invalid Session.
const maxIdentifyAttempts = 5

// errInvalidSession is returned when Discord rejects IDENTIFY with op=9.
var errInvalidSession = errors.New("invalid session")

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
	Token   string
	Intents int
	// DialURL overrides the Discord gateway URL (tests). Empty uses gatewayURL.
	DialURL string
	// InvalidSessionBackoff, if set, replaces the random 1–5s Discord backoff (tests).
	InvalidSessionBackoff func(attempt int) time.Duration
	conn                  *websocket.Conn
	seq                   *int
	sessionID             string
	stopCh                chan struct{}
	hbStop                chan struct{} // cancels heartbeat for the current connection
	mu                    sync.Mutex
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
// On Discord op=9 Invalid Session (common after a hard kill leaves a zombie session),
// it waits 1–5s and re-IDENTIFYs up to maxIdentifyAttempts times.
func (g *Gateway) Connect() (*Ready, error) {
	var lastErr error
	for attempt := 1; attempt <= maxIdentifyAttempts; attempt++ {
		ready, err := g.connectOnce()
		if err == nil {
			return ready, nil
		}
		lastErr = err
		if !errors.Is(err, errInvalidSession) {
			return nil, err
		}
		if attempt == maxIdentifyAttempts {
			break
		}
		// Discord: wait a random 1–5s before a fresh IDENTIFY after Invalid Session.
		delay := time.Duration(1+rand.Intn(5)) * time.Second
		if g.InvalidSessionBackoff != nil {
			delay = g.InvalidSessionBackoff(attempt)
		}
		fmt.Fprintf(os.Stderr, "gateway: invalid session, retrying in %s (attempt %d/%d)...\n",
			delay, attempt, maxIdentifyAttempts)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-g.stopCh:
				return nil, fmt.Errorf("gateway closed during invalid-session backoff")
			}
		}
	}
	return nil, fmt.Errorf("invalid session after %d attempts: %w", maxIdentifyAttempts, lastErr)
}

// connectOnce performs one dial → HELLO → IDENTIFY → READY attempt.
func (g *Gateway) connectOnce() (*Ready, error) {
	url := g.DialURL
	if url == "" {
		url = gatewayURL
	}

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial gateway: %w", err)
	}

	g.mu.Lock()
	g.conn = conn
	g.seq = nil
	g.sessionID = ""
	// Fresh heartbeat cancel for this attempt (stop any prior loop).
	g.stopHeartbeatLocked()
	g.hbStop = make(chan struct{})
	hbStop := g.hbStop
	g.mu.Unlock()

	fail := func(err error) (*Ready, error) {
		g.mu.Lock()
		g.stopHeartbeatLocked()
		if g.conn != nil {
			_ = g.conn.Close()
			g.conn = nil
		}
		g.mu.Unlock()
		return nil, err
	}

	var hello Payload
	if err := conn.ReadJSON(&hello); err != nil {
		return fail(fmt.Errorf("read HELLO: %w", err))
	}
	if hello.Op != 10 {
		return fail(fmt.Errorf("expected op=10 HELLO, got op=%d", hello.Op))
	}

	var helloData Hello
	if hello.D == nil {
		return fail(fmt.Errorf("parse HELLO: empty data"))
	}
	if err := json.Unmarshal(*hello.D, &helloData); err != nil {
		return fail(fmt.Errorf("parse HELLO: %w", err))
	}

	interval := time.Duration(helloData.HeartbeatInterval) * time.Millisecond
	go g.heartbeatLoop(interval, hbStop)

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
		return fail(fmt.Errorf("send IDENTIFY: %w", err))
	}

	var readyPayload Payload
	if err := conn.ReadJSON(&readyPayload); err != nil {
		return fail(fmt.Errorf("read READY: %w", err))
	}
	if readyPayload.Op == 9 {
		// Invalid Session — caller may retry after backoff.
		return fail(fmt.Errorf("%w (op=9)", errInvalidSession))
	}
	if readyPayload.Op != 0 || readyPayload.T != "READY" {
		return fail(fmt.Errorf("expected READY, got op=%d t=%s", readyPayload.Op, readyPayload.T))
	}
	if readyPayload.S != nil {
		g.mu.Lock()
		g.seq = readyPayload.S
		g.mu.Unlock()
	}
	if readyPayload.D == nil {
		return fail(fmt.Errorf("parse READY: empty data"))
	}

	var ready Ready
	if err := json.Unmarshal(*readyPayload.D, &ready); err != nil {
		return fail(fmt.Errorf("parse READY: %w", err))
	}
	g.mu.Lock()
	g.sessionID = ready.SessionID
	g.mu.Unlock()

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

		g.mu.Lock()
		conn := g.conn
		g.mu.Unlock()
		if conn == nil {
			return fmt.Errorf("read: not connected")
		}

		var payload Payload
		if err := conn.ReadJSON(&payload); err != nil {
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
		case 9: // INVALID_SESSION mid-stream — surface as error so supervisor can restart
			return fmt.Errorf("%w (op=9 mid-session)", errInvalidSession)
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
	g.mu.Lock()
	g.stopHeartbeatLocked()
	if g.conn != nil {
		_ = g.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = g.conn.Close()
		g.conn = nil
	}
	g.mu.Unlock()
}

// stopHeartbeatLocked closes hbStop if open. Caller must hold g.mu.
func (g *Gateway) stopHeartbeatLocked() {
	if g.hbStop == nil {
		return
	}
	select {
	case <-g.hbStop:
	default:
		close(g.hbStop)
	}
	g.hbStop = nil
}

func (g *Gateway) heartbeatLoop(interval time.Duration, hbStop chan struct{}) {
	jitter := time.Duration(rand.Float64() * float64(interval))
	select {
	case <-time.After(jitter):
	case <-hbStop:
		return
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
		case <-hbStop:
			return
		case <-g.stopCh:
			return
		}
	}
}

func (g *Gateway) sendHeartbeat() {
	g.mu.Lock()
	seq := g.seq
	conn := g.conn
	g.mu.Unlock()
	if conn == nil {
		return
	}

	payload := map[string]any{"op": 1, "d": seq}
	_ = conn.WriteJSON(payload)
}

func rawJSON(v any) *json.RawMessage {
	data, _ := json.Marshal(v)
	raw := json.RawMessage(data)
	return &raw
}
