package listen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
)

// SocketSource streams Events API payloads over Socket Mode. It needs an
// app-level token (xapp-...) and a Slack app with Socket Mode on. The app's
// bot token is used for the connection.
type SocketSource struct {
	Workspace string
	TeamID    string
	AppClient *client.Client // a client whose token is the xapp- app token
}

// Run opens a Socket Mode connection and sends events. It acknowledges each
// envelope so Slack keeps delivering.
func (s *SocketSource) Run(ctx context.Context, out chan<- Event) error {
	var open struct {
		URL string `json:"url"`
	}
	if err := s.AppClient.CallJSON(ctx, "apps.connections.open", url.Values{}, &open); err != nil {
		return fmt.Errorf("apps.connections.open: %w", err)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, open.URL, nil)
	if err != nil {
		return fmt.Errorf("socket mode connect: %w", err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	const timeout = 70 * time.Second
	conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		conn.SetReadDeadline(time.Now().Add(timeout))
		var frame struct {
			Type       string          `json:"type"`
			EnvelopeID string          `json:"envelope_id"`
			Payload    json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(msg, &frame) != nil {
			continue
		}
		if frame.EnvelopeID != "" {
			ack, _ := json.Marshal(map[string]string{"envelope_id": frame.EnvelopeID})
			_ = conn.WriteMessage(websocket.TextMessage, ack)
		}
		switch frame.Type {
		case "hello", "disconnect":
			continue
		}
		var p struct {
			Event json.RawMessage `json:"event"`
		}
		if json.Unmarshal(frame.Payload, &p) != nil || len(p.Event) == 0 {
			continue
		}
		if e, ok := parseEvent(s.Workspace, s.TeamID, p.Event); ok {
			select {
			case out <- e:
			case <-ctx.Done():
			}
		}
	}
}

// Name returns the workspace label.
func (s *SocketSource) Name() string { return s.Workspace }
