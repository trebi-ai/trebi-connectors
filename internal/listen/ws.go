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

// Run dials the socket and sends events until ctx is done or the socket drops.
func (s *WSSource) Run(ctx context.Context, out chan<- Event) error {
	wsURL := s.url()
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
