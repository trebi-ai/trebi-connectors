package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// recorder is an sdk.Emitter that keeps the status notes and drops events.
type recorder struct {
	sdk.Emitter
	status []sdk.Status
}

func (r *recorder) Status(st sdk.Status) error { r.status = append(r.status, st); return nil }

func (r *recorder) Event(sdk.Event) error { return nil }

func newDecryptAdapter(t *testing.T) (*Adapter, *fakeClock, *recorder) {
	t.Helper()
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f
	if err := f.Connect(context.Background(), wa.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	d := NewAdapter(a)
	clk := &fakeClock{t: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)}
	d.decrypt = newDecryptWatch(clk.Now)
	return d, clk, &recorder{}
}

func undecryptable(chat types.JID) *events.UndecryptableMessage {
	return &events.UndecryptableMessage{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: peer},
		ID:            "U1", PushName: "Sam",
	}}
}

func (r *recorder) handle(t *testing.T, d *Adapter, evt any) {
	t.Helper()
	if _, err := d.handle(context.Background(), r, evt); err != nil {
		t.Fatal(err)
	}
}

func TestDecryptWatchWindow(t *testing.T) {
	d, clk, r := newDecryptAdapter(t)
	r.handle(t, d, undecryptable(peer))
	clk.Add(10 * time.Minute)
	r.handle(t, d, undecryptable(peer))
	if len(r.status) != 0 {
		t.Fatalf("2 failures: %+v", r.status)
	}
	clk.Add(10 * time.Minute)
	r.handle(t, d, undecryptable(peer))
	if len(r.status) != 1 || r.status[0].State != sdk.StateError || r.status[0].Reason != "" ||
		!strings.Contains(r.status[0].Message, "3 in the last 30 minutes, last in Sam)") {
		t.Fatalf("3 failures: %+v", r.status)
	}
	r.handle(t, d, undecryptable(peer))
	if len(r.status) != 1 {
		t.Fatalf("4th failure sends again: %+v", r.status)
	}
}

func TestDecryptWatchSpread(t *testing.T) {
	d, clk, r := newDecryptAdapter(t)
	for range 3 {
		r.handle(t, d, undecryptable(peer))
		clk.Add(20 * time.Minute)
	}
	if len(r.status) != 0 {
		t.Fatalf("3 failures across 40 minutes: %+v", r.status)
	}
}

func TestDecryptWatchIgnored(t *testing.T) {
	d, _, r := newDecryptAdapter(t)
	for range 3 {
		u := undecryptable(peer)
		u.UnavailableType = events.UnavailableTypeViewOnce
		r.handle(t, d, u)
		u = undecryptable(peer)
		u.Info.IsFromMe = true
		r.handle(t, d, u)
	}
	if len(r.status) != 0 {
		t.Fatalf("ignored failures: %+v", r.status)
	}
}

func TestDecryptWatchChatName(t *testing.T) {
	d, _, r := newDecryptAdapter(t)
	d.app.storeGroup(types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "Family"}})
	for range 3 {
		r.handle(t, d, undecryptable(group))
	}
	if len(r.status) != 1 || !strings.Contains(r.status[0].Message, "last in Family)") {
		t.Fatalf("chat name: %+v", r.status)
	}
}

func TestDecryptWatchClear(t *testing.T) {
	d, _, r := newDecryptAdapter(t)
	ctx := context.Background()
	for range 3 {
		r.handle(t, d, undecryptable(peer))
	}
	if st, err := d.AuthStatus(ctx); err != nil || st.State != sdk.StateError {
		t.Fatalf("auth status while active: %+v %v", st, err)
	}
	d.app.wa.(*fakewa.Client).Close()
	if st, err := d.AuthStatus(ctx); err != nil || st.State != sdk.StateConnecting {
		t.Fatalf("auth status while disconnected: %+v %v", st, err)
	}
	if err := d.app.wa.Connect(ctx, wa.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	mine := live("M0", peer, peer, time.Now(), &waE2E.Message{Conversation: proto.String("hi")})
	mine.Info.IsFromMe = true
	r.handle(t, d, mine)
	if len(r.status) != 1 {
		t.Fatalf("own message clears: %+v", r.status)
	}
	r.handle(t, d, live("M1", peer, peer, time.Now(), &waE2E.Message{Conversation: proto.String("hi")}))
	if len(r.status) != 2 || r.status[1].State != sdk.StateConnected {
		t.Fatalf("clear: %+v", r.status)
	}
	if st, err := d.AuthStatus(ctx); err != nil || st.State != sdk.StateConnected {
		t.Fatalf("auth status after clear: %+v %v", st, err)
	}
	r.handle(t, d, live("M2", peer, peer, time.Now(), &waE2E.Message{Conversation: proto.String("hi")}))
	if len(r.status) != 2 {
		t.Fatalf("second clear sends again: %+v", r.status)
	}
}

// TestDecryptReconnect checks that a disconnect wins over the decrypt error
// and that the run loop connects again with the error still active.
func TestDecryptReconnect(t *testing.T) {
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f
	d := NewAdapter(a)
	c := sdktest.Start(d, sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() })
	if _, err := c.Initialize(sdk.InitializeParams{Instance: sdk.InstanceInfo{Key: "k", Name: "wa"}}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, sdk.StateConnected)
	for range 3 {
		f.Emit(undecryptable(peer))
	}
	waitStatus(t, c, sdk.StateError)

	// Let the run loop wait again, so that it sees the disconnect first.
	time.Sleep(50 * time.Millisecond)
	f.Close()
	f.Emit(&events.Disconnected{})
	waitStatus(t, c, sdk.StateConnecting)
	if st := waitStatus(t, c, sdk.StateError); !strings.Contains(st.Message, "cannot be read") {
		t.Fatalf("after reconnect: %+v", st)
	}
	if !f.IsConnected() {
		t.Fatal("run loop did not connect again")
	}
}
