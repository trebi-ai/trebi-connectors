package vnc_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// listen starts srv on 127.0.0.1.
func listen(t *testing.T, srv *vnctest.Server) string {
	t.Helper()
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// dialTCP dials addr with o.
func dialTCP(t *testing.T, addr string, o vnc.Options) (*vnc.Conn, error) {
	t.Helper()
	o.Addr = addr
	c, err := vnc.Dial(testCtx(t), o)
	if err == nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, err
}

// checkScreen refreshes c and compares it with the screen of srv.
func checkScreen(t *testing.T, c *vnc.Conn, srv *vnctest.Server) {
	t.Helper()
	// The first update can come from before the paint, so wait for it first.
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	srv.Paint(image.Rect(3, 4, 20, 30), color.RGBA{10, 200, 30, 255})
	if err := c.Refresh(testCtx(t), true); err != nil {
		t.Fatal(err)
	}
	sameImage(t, c.Snapshot(image.Rectangle{}), srv.Screen(), 0)
}

// noSecret fails when err holds one of the secrets.
func noSecret(t *testing.T, err error, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error holds a secret: %v", err)
		}
	}
}

func testCA(t *testing.T, hosts ...string) (*vnctest.CA, *tls.Config) {
	t.Helper()
	ca, err := vnctest.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	srvTLS, err := ca.ServerTLS(hosts...)
	if err != nil {
		t.Fatal(err)
	}
	return ca, srvTLS
}

func TestARD(t *testing.T) {
	for _, ver := range []string{"3.3", "3.7", "3.8"} {
		t.Run(ver, func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{Version: ver, Security: []uint8{vnc.SecAppleARD}, User: "alice", Password: "pässwörd"})
			defer srv.Close()
			addr := listen(t, srv)
			c, err := dialTCP(t, addr, vnc.Options{User: "alice", Password: "pässwörd"})
			if err != nil {
				t.Fatal(err)
			}
			checkScreen(t, c, srv)
		})
	}
}

func TestARDFailures(t *testing.T) {
	srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecAppleARD}, User: "alice", Password: "right", FailReason: "Authentication failed"})
	defer srv.Close()
	addr := listen(t, srv)
	for name, o := range map[string]vnc.Options{
		"wrong password": {User: "alice", Password: "wrong-pw"},
		"no user":        {Password: "wrong-pw"},
		"long password":  {User: "alice", Password: "wrong-pw" + strings.Repeat("x", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := dialTCP(t, addr, o)
			if !errors.Is(err, vnc.ErrAuth) {
				t.Fatalf("err = %v", err)
			}
			noSecret(t, err, "wrong-pw")
		})
	}
}

func TestVeNCrypt(t *testing.T) {
	ca, srvTLS := testCA(t, "127.0.0.1")
	for _, ver := range []string{"3.7", "3.8"} {
		for _, sub := range []uint32{vnc.VeNCryptX509Vnc, vnc.VeNCryptX509Plain, vnc.VeNCryptX509None} {
			t.Run(ver+"/"+map[uint32]string{261: "X509Vnc", 262: "X509Plain", 260: "X509None"}[sub], func(t *testing.T) {
				srv := vnctest.New(vnctest.Options{Version: ver, Security: []uint8{vnc.SecVeNCrypt}, VeNCrypt: []uint32{sub}, TLS: srvTLS, User: "bob", Password: "s3kr1t"})
				defer srv.Close()
				addr := listen(t, srv)
				c, err := dialTCP(t, addr, vnc.Options{User: "bob", Password: "s3kr1t", TLS: &tls.Config{RootCAs: ca.Pool}})
				if err != nil {
					t.Fatal(err)
				}
				checkScreen(t, c, srv)
			})
		}
	}
}

func TestVeNCryptPreference(t *testing.T) {
	ca, srvTLS := testCA(t, "127.0.0.1")
	all := []uint32{vnc.VeNCryptPlain, vnc.VeNCryptTLSVnc, vnc.VeNCryptX509None, vnc.VeNCryptX509Plain, vnc.VeNCryptX509Vnc}
	for _, tc := range []struct {
		name string
		o    vnc.Options
	}{
		// X509Vnc needs only the password; X509Plain needs the user too.
		{"password", vnc.Options{Password: "s3kr1t"}},
		{"user", vnc.Options{User: "bob", Password: "s3kr1t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVeNCrypt}, VeNCrypt: all, TLS: srvTLS, User: "bob", Password: "s3kr1t"})
			defer srv.Close()
			addr := listen(t, srv)
			tc.o.TLS = &tls.Config{RootCAs: ca.Pool}
			if _, err := dialTCP(t, addr, tc.o); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVeNCryptWrongPassword(t *testing.T) {
	ca, srvTLS := testCA(t, "127.0.0.1")
	for _, sub := range []uint32{vnc.VeNCryptX509Vnc, vnc.VeNCryptX509Plain} {
		srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVeNCrypt}, VeNCrypt: []uint32{sub}, TLS: srvTLS, User: "bob", Password: "right"})
		addr := listen(t, srv)
		_, err := dialTCP(t, addr, vnc.Options{User: "bob", Password: "wrong-pw", TLS: &tls.Config{RootCAs: ca.Pool}})
		srv.Close()
		if !errors.Is(err, vnc.ErrAuth) {
			t.Fatalf("subtype %d: err = %v", sub, err)
		}
		noSecret(t, err, "wrong-pw")
	}
}

func TestVeNCryptCertificate(t *testing.T) {
	ca, srvTLS := testCA(t, "desk.example")
	srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVeNCrypt, vnc.SecVNC}, TLS: srvTLS, Password: "s3kr1t"})
	defer srv.Close()
	addr := listen(t, srv)

	_, err := dialTCP(t, addr, vnc.Options{Password: "s3kr1t", TLS: &tls.Config{RootCAs: ca.Pool}})
	var host x509.HostnameError
	if !errors.As(err, &host) || !strings.Contains(err.Error(), "does not match the server name") {
		t.Fatalf("name mismatch: err = %v", err)
	}
	noSecret(t, err, "s3kr1t")

	// The system roots do not know the test CA.
	_, err = dialTCP(t, addr, vnc.Options{Password: "s3kr1t"})
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("unknown CA: err = %v", err)
	}

	insecure, err := vnc.NewTLSConfig("", true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dialTCP(t, addr, vnc.Options{Password: "s3kr1t", TLS: insecure})
	if err != nil {
		t.Fatalf("skip verify: %v", err)
	}
	checkScreen(t, c, srv)

	f := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(f, ca.PEM, 0o600)
	named, err := vnc.NewTLSConfig(f, false)
	if err != nil {
		t.Fatal(err)
	}
	named.ServerName = "desk.example"
	if _, err := dialTCP(t, addr, vnc.Options{Password: "s3kr1t", TLS: named}); err != nil {
		t.Fatalf("CA file: %v", err)
	}

	if _, err := vnc.NewTLSConfig(filepath.Join(t.TempDir(), "none.pem"), false); err == nil {
		t.Fatal("a missing CA file gave no error")
	}
}

// Xvnc offers TLSVnc and VncAuth by default. Go has no anonymous TLS, so
// Dial connects again with VncAuth.
func TestVeNCryptFallback(t *testing.T) {
	_, srvTLS := testCA(t, "127.0.0.1")
	anon := []uint32{vnc.VeNCryptTLSVnc, vnc.VeNCryptTLSNone}
	srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVeNCrypt, vnc.SecVNC}, VeNCrypt: anon, TLS: srvTLS, Password: "s3kr1t"})
	defer srv.Close()
	addr := listen(t, srv)
	c, err := dialTCP(t, addr, vnc.Options{Password: "s3kr1t"})
	if err != nil {
		t.Fatal(err)
	}
	checkScreen(t, c, srv)

	only := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVeNCrypt}, VeNCrypt: anon, TLS: srvTLS, Password: "s3kr1t"})
	defer only.Close()
	onlyAddr := listen(t, only)
	_, err = dialTCP(t, onlyAddr, vnc.Options{Password: "s3kr1t"})
	if err == nil || !strings.Contains(err.Error(), "TLSVnc (258): anonymous TLS is not supported") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultPrefersTLS(t *testing.T) {
	_, srvTLS := testCA(t, "127.0.0.1")
	srv := vnctest.New(vnctest.Options{Security: []uint8{vnc.SecVNC, vnc.SecVeNCrypt}, TLS: srvTLS, Password: "s3kr1t"})
	defer srv.Close()
	addr := listen(t, srv)
	// VeNCrypt is chosen, so the unknown CA stops the login.
	if _, err := dialTCP(t, addr, vnc.Options{Password: "s3kr1t"}); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("err = %v", err)
	}
	// An explicit order wins.
	if _, err := dialTCP(t, addr, vnc.Options{Password: "s3kr1t", Security: []uint8{vnc.SecVNC}}); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocket(t *testing.T) {
	srv := vnctest.New(vnctest.Options{})
	defer srv.Close()
	var last atomic.Pointer[http.Request]
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.Store(r)
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		srv.Serve(websocket.NetConn(r.Context(), ws, websocket.MessageBinary))
	})
	for _, scheme := range []string{"ws", "wss"} {
		t.Run(scheme, func(t *testing.T) {
			ts := httptest.NewUnstartedServer(h)
			o := vnc.Options{}
			if scheme == "wss" {
				ts.StartTLS()
				pool := x509.NewCertPool()
				pool.AddCert(ts.Certificate())
				o.TLS = &tls.Config{RootCAs: pool}
			} else {
				ts.Start()
			}
			defer ts.Close()
			o.Addr = scheme + "://" + strings.TrimPrefix(strings.TrimPrefix(ts.URL, "https://"), "http://") + "/websockify?token=t0ken"
			c, err := vnc.Dial(testCtx(t), o)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			checkScreen(t, c, srv)
			got := last.Load()
			if got.URL.Path != "/websockify" || got.URL.RawQuery != "token=t0ken" {
				t.Errorf("request %v", got.URL)
			}
			if got.Header.Get("Sec-WebSocket-Protocol") != "binary" || got.Header.Get("Origin") == "" {
				t.Errorf("headers %v", got.Header)
			}
		})
	}
}

func TestWebSocketErrorHidesQuery(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	_, err := vnc.Dial(testCtx(t), vnc.Options{Addr: "ws" + strings.TrimPrefix(ts.URL, "http") + "/x?password=t0ken"})
	if err == nil {
		t.Fatal("no error")
	}
	noSecret(t, err, "t0ken")
	if !strings.Contains(err.Error(), "?<redacted>") {
		t.Errorf("err = %v", err)
	}
}
