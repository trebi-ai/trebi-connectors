package listen

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
)

// WSSource streams events over Slack's internal client WebSocket, the one the
// desktop/web app uses. It needs an xoxc- session token and the "d" cookie.
type WSSource struct {
	Workspace string
	TeamID    string
	Client    *client.Client
	// BaseURL is the wss host. Empty uses wss://wss-primary.slack.com.
	BaseURL string
	// Eager drops lazy_channels, so the server pushes events for every
	// channel at once instead of after a channel is first used.
	Eager bool
}

const defaultWSHost = "wss://wss-primary.slack.com"

// url builds the client WebSocket URL for the session token and team.
func (s *WSSource) url() string {
	host := s.BaseURL
	if host == "" {
		host = defaultWSHost
	}
	startArgs := "?agent=client&org_wide_aware=true&agent_version=&eac_cache_ts=true&cache_ts=0&name_tagging=true&only_self_subteams=true&connect_only=true&ms_latest=true"
	// lazy_channels lets the server defer channel subscriptions until a
	// channel is used. --eager drops it so every channel streams at once.
	lazy := "&lazy_channels=1"
	if s.Eager {
		lazy = ""
	}
	return fmt.Sprintf(
		"%s/?token=%s&sync_desync=1&slack_client=desktop&start_args=%s&no_query_on_subscribe=1&flannel=3%s&gateway_server=%s-1&batch_presence_aware=1",
		host, url.QueryEscape(s.Client.Token()), url.QueryEscape(startArgs), lazy, s.TeamID,
	)
}

// dialURL asks rtm.connect for a socket URL. The client URL of url() gives
// no frames to a session token, so it is only the fallback.
func (s *WSSource) dialURL(ctx context.Context) string {
	if s.BaseURL != "" {
		return s.url()
	}
	var r struct {
		URL string `json:"url"`
	}
	if err := s.Client.CallJSON(ctx, "rtm.connect", nil, &r); err != nil || r.URL == "" {
		return s.url()
	}
	return r.URL
}

// Run dials the socket and sends events until ctx is done or the socket drops.
func (s *WSSource) Run(ctx context.Context, out chan<- Event) error {
	wsURL := s.dialURL(ctx)
	jar := s.Client.HTTP().Jar
	dialer := &websocket.Dialer{
		Jar:             jar,
		TLSClientConfig: &tls.Config{NextProtos: []string{"http/1.1"}},
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, client.WebSocketHeaders())
	if err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	defer conn.Close()
	go keepAlive(ctx, conn)
	return readLoop(ctx, conn, func(raw json.RawMessage) {
		if e, ok := parseEvent(s.Workspace, s.TeamID, raw); ok {
			select {
			case out <- e:
			case <-ctx.Done():
			}
		}
	})
}

// Name returns the workspace label.
func (s *WSSource) Name() string { return s.Workspace }

// keepAlive sends the JSON ping of the desktop client. Slack sends no
// frames to an idle socket, so without it the read deadline expires.
func keepAlive(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for id := 1; ; id++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if conn.WriteJSON(map[string]any{"type": "ping", "id": id}) != nil {
				return
			}
		}
	}
}

// readLoop reads frames until the context ends or the socket errors. It keeps
// the connection alive with a read deadline reset on each frame and a pong
// reply to pings.
func readLoop(ctx context.Context, conn *websocket.Conn, handle func(json.RawMessage)) error {
	const timeout = 70 * time.Second
	conn.SetReadDeadline(time.Now().Add(timeout))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(timeout)); return nil })
	conn.SetPingHandler(func(m string) error {
		conn.SetReadDeadline(time.Now().Add(timeout))
		return conn.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(10*time.Second))
	})
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		conn.SetReadDeadline(time.Now().Add(timeout))
		handle(msg)
	}
}
