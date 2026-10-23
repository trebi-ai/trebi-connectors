package serve_test

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

type rig struct {
	t     *testing.T
	srv   *vnctest.Server
	addr  string
	conn  *sdktest.Conn
	state string
	sock  string
}

// shortDir is a temp folder with a short path, so that the socket fits.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func start(t *testing.T, o vnctest.Options, s config.Settings) *rig {
	t.Helper()
	srv := vnctest.New(o)
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	r := &rig{t: t, srv: srv, addr: addr, state: shortDir(t)}
	s.Server = addr
	if s.Settle == "" {
		s.Settle = "200ms"
	}
	p, err := session.TrebiPaths(r.state)
	if err != nil {
		t.Fatal(err)
	}
	r.sock = p.Sock
	ln, err := session.Listen(p.Sock)
	if err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context) (*vnc.Conn, error) {
		return vnc.Dial(ctx, vnc.Options{Addr: addr, User: s.User, Password: s.Password})
	}
	h := session.NewHolder(dial, addr, s.User, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.conn = sdktest.Start(serve.New(h, ln, s, "test"), sdk.WithStateDir(r.state))
	t.Cleanup(func() { r.conn.Close() })
	if _, err := r.conn.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	return r
}

// next returns the next event or status note.
func (r *rig) next() (string, json.RawMessage) {
	r.t.Helper()
	for {
		m, line, err := r.conn.Read()
		if err != nil {
			r.t.Fatalf("read: %v %s", err, line)
		}
		if m.Method == sdk.MethodEvent || m.Method == sdk.MethodStatus {
			return m.Method, m.Params
		}
	}
}

func (r *rig) status(state string) sdk.Status {
	r.t.Helper()
	method, p := r.next()
	var st sdk.Status
	if err := json.Unmarshal(p, &st); method != sdk.MethodStatus || err != nil || st.State != state {
		r.t.Fatalf("got %s %s, want status %s", method, p, state)
	}
	return st
}

func (r *rig) event(typ string) sdk.Event {
	r.t.Helper()
	method, p := r.next()
	var ev sdk.Event
	if err := json.Unmarshal(p, &ev); method != sdk.MethodEvent || err != nil || ev.Type != typ {
		r.t.Fatalf("got %s %s, want event %s", method, p, typ)
	}
	return ev
}

func TestEvents(t *testing.T) {
	r := start(t, vnctest.Options{Name: "Lab desktop"}, config.Settings{Areas: "top=0,0,64,24; bottom=0,24,64,24"})
	r.status(sdk.StateConnecting)
	r.status(sdk.StateConnected)

	// A command of a job reaches the connection through the socket.
	cl, err := session.Open(r.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := cl.Info(ctx)
	cl.Close()
	if err != nil || info.Name != "Lab desktop" {
		t.Fatalf("info: %+v, %v", info, err)
	}

	r.srv.Paint(image.Rect(4, 30, 20, 40), color.RGBA{255, 255, 255, 255})
	ev := r.event(serve.EventChanged)
	if ev.Room.ID != r.addr || ev.Room.Name != "Lab desktop" || !strings.Contains(ev.Text, `"bottom"`) || len(ev.Attachments) != 0 {
		t.Fatalf("changed: %+v", ev)
	}
	ev = r.event(serve.EventStable)
	var data struct {
		Area string `json:"area"`
		Px   int    `json:"changed_px"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil || data.Area != "bottom" || data.Px != 160 {
		t.Fatalf("stable data: %s", ev.Data)
	}
	if len(ev.Attachments) != 1 {
		t.Fatalf("stable attachments: %+v", ev.Attachments)
	}
	at := ev.Attachments[0]
	if at.Mime != "image/png" || filepath.Dir(at.Path) != filepath.Join(r.state, "images") {
		t.Fatalf("attachment: %+v", at)
	}
	f, err := os.Open(at.Path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil || img.Bounds().Size() != image.Pt(64, 24) {
		t.Fatalf("picture: %v, %v", img, err)
	}

	r.srv.CutText("hello")
	if ev = r.event(serve.EventClipboard); !strings.HasSuffix(ev.Text, ": hello") {
		t.Fatalf("clipboard: %+v", ev)
	}
	r.srv.Bell()
	r.event(serve.EventBell)
	r.srv.Resize(128, 96)
	if ev = r.event(serve.EventResized); !strings.HasSuffix(ev.Text, "128x96.") {
		t.Fatalf("resized: %+v", ev)
	}

	// A drop: state.changed, then state.recovered when the holder connects
	// again.
	r.srv.Drop()
	r.status(sdk.StateError)
	ev = r.event(sdk.EventStateChanged)
	var ch sdk.StateChange
	if err := json.Unmarshal(ev.Data, &ch); err != nil || ch.Key != "reachable" {
		t.Fatalf("state.changed: %s", ev.Data)
	}
	r.status(sdk.StateConnected)
	r.event(sdk.EventStateRecovered)
}

func TestNoImages(t *testing.T) {
	r := start(t, vnctest.Options{}, config.Settings{Images: "no"})
	r.status(sdk.StateConnecting)
	r.status(sdk.StateConnected)
	r.srv.Paint(image.Rect(0, 0, 20, 20), color.RGBA{255, 255, 255, 255})
	r.event(serve.EventChanged)
	if ev := r.event(serve.EventStable); len(ev.Attachments) != 0 {
		t.Fatalf("attachments with no images: %+v", ev.Attachments)
	}
}

func TestWrongPassword(t *testing.T) {
	r := start(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "right"}, config.Settings{Password: "wrong"})
	r.status(sdk.StateConnecting)
	st := r.status(sdk.StateAuthRequired)
	if !strings.Contains(st.Message, config.PasswordLabel) || strings.Contains(st.Message, "wrong") {
		t.Fatalf("status: %+v", st)
	}
	r.event(sdk.EventStateChanged)
	time.Sleep(1500 * time.Millisecond)
	if n := r.srv.Served(); n != 1 {
		t.Fatalf("the server had %d logins, want 1", n)
	}
}

func TestNoPassword(t *testing.T) {
	r := start(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "right"}, config.Settings{})
	r.status(sdk.StateConnecting)
	if st := r.status(sdk.StateAuthRequired); st.Reason != sdk.ReasonMissingInput {
		t.Fatalf("status: %+v", st)
	}
}

func TestBadInputs(t *testing.T) {
	for _, s := range []config.Settings{{Areas: "x=1,2"}, {Settle: "soon"}, {Images: "maybe"}} {
		if _, _, err := serve.Spec(s); err == nil {
			t.Errorf("Spec(%+v) gave no error", s)
		}
	}
	r := start(t, vnctest.Options{}, config.Settings{Settle: "soon"})
	if st := r.status(sdk.StateError); !strings.Contains(st.Message, config.SettleLabel) {
		t.Fatalf("status: %+v", st)
	}
}

func TestMissingServer(t *testing.T) {
	conn := sdktest.Start(serve.New(nil, nil, config.Settings{}, "test"), sdk.WithStateDir(t.TempDir()))
	defer conn.Close()
	if _, err := conn.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	m, err := conn.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil || st.State != sdk.StateAuthRequired || !strings.Contains(st.Message, config.EnvServer) {
		t.Fatalf("status: %s", m.Params)
	}
}
