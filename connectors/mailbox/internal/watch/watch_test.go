package watch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/fakemail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

const wait = 2 * time.Second

// rig is one fake account, one cursor store, and the events of a watcher.
type rig struct {
	t      *testing.T
	fake   *fakemail.Server
	mb     *mail.Mailbox
	store  *watch.MemStore
	events chan sdk.Event
	status chan sdk.Status
	fail   atomic.Bool
	n      int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	fake, err := fakemail.Start("me@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	s := config.Settings{Address: "me@example.com", Password: "pw", IMAPHost: fake.IMAPAddr(), SMTPHost: fake.SMTPAddr()}
	return &rig{
		t: t, fake: fake, mb: mail.New(s, mail.WithTLS(fake.ClientTLS())), store: &watch.MemStore{},
		events: make(chan sdk.Event, 32), status: make(chan sdk.Status, 64),
	}
}

// run starts a watcher and returns the function that stops it.
func (r *rig) run(now func() time.Time, opts ...watch.Option) func() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	opts = append([]watch.Option{
		watch.WithBackoff(10*time.Millisecond, 50*time.Millisecond),
		watch.WithStatus(func(st sdk.Status) { r.status <- st }),
	}, opts...)
	w := watch.NewWatcher(r.mb, r.store, now, opts...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx, func(e sdk.Event) error { //nolint:errcheck // Run returns nil at the end
			if r.fail.Load() {
				return errors.New("daemon is gone")
			}
			r.events <- e
			return nil
		})
	}()
	r.connected()
	stop := func() { cancel(); <-done }
	r.t.Cleanup(stop)
	return stop
}

// connected waits until the watcher has a connection and a cursor.
func (r *rig) connected() {
	r.t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case st := <-r.status:
			if st.State == sdk.StateConnected {
				r.settle()
				return
			}
		case <-deadline:
			r.t.Fatal("no connected status")
		}
	}
}

// settle waits until the first sync saved a cursor.
func (r *rig) settle() {
	r.t.Helper()
	for end := time.Now().Add(wait); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, ok, _ := r.store.Load(); ok { //nolint:errcheck // MemStore has no errors
			time.Sleep(50 * time.Millisecond) // the watcher enters IDLE
			return
		}
	}
	r.t.Fatal("no cursor")
}

func (r *rig) deliver(from string) {
	r.t.Helper()
	r.n++
	raw := fmt.Sprintf("From: %s\r\nTo: me@example.com\r\nSubject: Note %d\r\nMessage-Id: <n%d@example.com>\r\n\r\nBody %d\r\n", from, r.n, r.n, r.n)
	if err := r.fake.Deliver("INBOX", []byte(raw)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) next() sdk.Event {
	r.t.Helper()
	select {
	case e := <-r.events:
		return e
	case <-time.After(wait):
		r.t.Fatal("no event")
		return sdk.Event{}
	}
}

func (r *rig) none(d time.Duration) {
	r.t.Helper()
	select {
	case e := <-r.events:
		r.t.Fatalf("unexpected event %s %q", e.ID, e.Text)
	case <-time.After(d):
	}
}

func TestIdleSendsEvent(t *testing.T) {
	r := newRig(t)
	r.run(time.Now)
	r.deliver("Ann <ann@example.com>")
	e := r.next()
	if e.Type != watch.EventType || e.Sender.ID != "ann@example.com" || e.Sender.Name != "Ann" || !strings.HasPrefix(e.Text, "Note 1") {
		t.Fatalf("event %+v", e)
	}
	var d watch.Data
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if e.Room == nil || e.Room.ID != "INBOX" || d.MessageID != "<n1@example.com>" || d.ID != "INBOX:1" || d.Labels == nil {
		t.Fatalf("event %+v data %s", e, e.Data)
	}
}

func TestOldMailNoEventAndNoDuplicate(t *testing.T) {
	r := newRig(t)
	r.deliver("ann@example.com")
	r.deliver("ann@example.com")
	stop := r.run(time.Now)
	r.none(300 * time.Millisecond)
	r.deliver("bob@example.com")
	if e := r.next(); e.Sender.ID != "bob@example.com" {
		t.Fatalf("event %+v", e)
	}
	stop()
	r.run(time.Now)
	r.none(300 * time.Millisecond)
}

func TestReconnectCatchesUp(t *testing.T) {
	r := newRig(t)
	r.run(time.Now)
	r.fake.DropConnections()
	for range 3 {
		r.deliver("ann@example.com")
	}
	var ids []string
	for range 3 {
		ids = append(ids, r.next().Text)
	}
	for i, want := range []string{"Note 1", "Note 2", "Note 3"} {
		if !strings.HasPrefix(ids[i], want) {
			t.Fatalf("order %q", ids)
		}
	}
	r.none(200 * time.Millisecond)
}

func TestResetFolderSetsBaseline(t *testing.T) {
	r := newRig(t)
	stop := r.run(time.Now)
	stop()
	before, _, _ := r.store.Load() //nolint:errcheck // MemStore has no errors
	if err := r.fake.ResetFolder("INBOX"); err != nil {
		t.Fatal(err)
	}
	r.deliver("ann@example.com")
	r.run(time.Now)
	r.none(300 * time.Millisecond)
	after, _, _ := r.store.Load() //nolint:errcheck // MemStore has no errors
	if after.UIDValidity == before.UIDValidity || after.LastUID == 0 {
		t.Fatalf("cursor %+v then %+v", before, after)
	}
}

func TestOwnMailNoEvent(t *testing.T) {
	r := newRig(t)
	r.run(time.Now)
	r.deliver("Me <ME@example.com>")
	r.deliver("ann@example.com")
	if e := r.next(); e.Sender.ID != "ann@example.com" {
		t.Fatalf("event %+v", e)
	}
	r.none(200 * time.Millisecond)
}

func TestEmitErrorKeepsCursor(t *testing.T) {
	r := newRig(t)
	r.run(time.Now)
	start, _, _ := r.store.Load() //nolint:errcheck // MemStore has no errors
	r.fail.Store(true)
	r.deliver("ann@example.com")
	time.Sleep(300 * time.Millisecond)
	if c, _, _ := r.store.Load(); c.LastUID != start.LastUID { //nolint:errcheck // MemStore has no errors
		t.Fatalf("cursor moved from %d to %d", start.LastUID, c.LastUID)
	}
	r.fail.Store(false)
	if e := r.next(); e.Sender.ID != "ann@example.com" {
		t.Fatalf("event %+v", e)
	}
}

// clock is a wall clock that a test can move forward.
type clock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *clock) jump(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

func TestClockJumpReconnects(t *testing.T) {
	r := newRig(t)
	clk := &clock{}
	r.run(clk.now, watch.WithClockTick(20*time.Millisecond))
	clk.jump(time.Hour)
	deadline := time.After(wait)
	for {
		select {
		case st := <-r.status:
			if st.State == sdk.StateConnecting && strings.Contains(st.Message, "clock") {
				r.connected()
				r.deliver("ann@example.com")
				r.next()
				return
			}
		case <-deadline:
			t.Fatal("no reconnect after the clock jump")
		}
	}
}
