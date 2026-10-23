package vnc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

// dialWebSocket opens RFB over a WebSocket, as noVNC, websockify, Proxmox,
// and Selenoid serve it. The path and the query name the session.
func dialWebSocket(ctx context.Context, rawURL string, o *Options) (net.Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Path == "" {
		u.Path = "/"
	}
	origin := "http://" + u.Host
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if u.Scheme == "wss" {
		origin = "https://" + u.Host
		if o.TLS != nil {
			tr.TLSClientConfig = o.TLS.Clone()
		}
	}
	// Go x/net/websocket servers (Selenoid) refuse more than one
	// subprotocol and a handshake with no Origin.
	ws, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: tr},
		HTTPHeader:      http.Header{"Origin": {origin}},
		Subprotocols:    []string{"binary"},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// The error holds the URL, and a query can hold a password.
		if u.RawQuery != "" {
			return nil, errors.New(strings.ReplaceAll(err.Error(), u.RawQuery, "<redacted>"))
		}
		return nil, err
	}
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}
