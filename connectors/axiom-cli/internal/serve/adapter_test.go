package serve_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/fakeaxiom"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// at is a time inside poll n (from 0): poll n reads up to t0 + n minutes.
func at(n int, sec int) time.Time {
	return t0.Add(time.Duration(n-1)*time.Minute + time.Duration(sec)*time.Second)
}

// clock gives t0 plus one minute for each poll.
type clock struct {
	mu sync.Mutex
	n  int
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := t0.Add(time.Duration(c.n) * time.Minute)
	c.n++
	return t
}

type rig struct {
	fake  *fakeaxiom.Server
	clock *clock
	tick  chan time.Time
	conn  *sdktest.Conn
}

func newRig(t *testing.T, state string, fake *fakeaxiom.Server, token string) *rig {
	t.Helper()
	r := &rig{fake: fake, clock: &clock{}, tick: make(chan time.Time)}
	r.start(t, state, token)
	return r
}

func (r *rig) start(t *testing.T, state, token string) {
	t.Helper()
	a := serve.New(client.New(r.fake.URL(), token, ""), time.Hour, "test", serve.WithTick(r.tick), serve.WithClock(r.clock.now))
	conn := sdktest.Start(a, sdk.WithStateDir(state))
	r.conn = conn
	t.Cleanup(func() {
		if r.conn == conn {
			conn.Close() //nolint:errcheck,gosec // the test checks the calls
		}
	})
}

func (r *rig) stop(t *testing.T) {
	t.Helper()
	if err := r.conn.Close(); err != nil {
		t.Fatal(err)
	}
	r.conn = nil
}

func (r *rig) play(t *testing.T, name string) {
	t.Helper()
	lines, err := sdktest.LoadTranscript(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Play(lines); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// round waits for the current poll to end and starts the next one.
func (r *rig) round(t *testing.T) {
	t.Helper()
	select {
	case r.tick <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("the poll did not end")
	}
}

func (r *rig) status(t *testing.T) sdk.Status {
	t.Helper()
	m, err := r.conn.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTriggerAndResolve(t *testing.T) {
	fake := fakeaxiom.Start()
	t.Cleanup(fake.Close)
	// An entry before the first poll is old: it gives no event.
	fake.AddCheck("m-errors", "open", t0.Add(-time.Hour))
	r := newRig(t, t.TempDir(), fake, fakeaxiom.Token)
	r.play(t, "start.jsonl") // poll 0 records the time of each monitor
	fake.AddCheck("m-errors", "open", at(1, 30))
	r.round(t)
	r.play(t, "trigger.jsonl")
	fake.AddCheck("m-errors", "closed", at(2, 10))
	fake.AddCheck("m-panic", "open", at(2, 20))
	r.round(t)
	r.play(t, "resolve.jsonl")
	r.round(t) // the same entries again give no event
	r.round(t)
	r.play(t, "ping.jsonl")
}

func TestRestartKeepsCursor(t *testing.T) {
	fake := fakeaxiom.Start()
	t.Cleanup(fake.Close)
	state := t.TempDir()
	r := newRig(t, state, fake, fakeaxiom.Token)
	r.play(t, "start.jsonl")
	fake.AddCheck("m-errors", "open", at(1, 30))
	r.round(t)
	r.play(t, "trigger.jsonl")
	r.stop(t)
	// A restore copies the state folder to a new path.
	moved := filepath.Join(t.TempDir(), "state")
	if err := os.CopyFS(moved, os.DirFS(state)); err != nil {
		t.Fatal(err)
	}
	r.start(t, moved, fakeaxiom.Token) // poll 2 sees the same entry
	fake.AddCheck("m-errors", "closed", at(3, 10))
	fake.AddCheck("m-panic", "open", at(3, 20))
	r.play(t, "restart.jsonl")
	r.round(t)
	r.play(t, "resolve.jsonl")
}

func TestRefusedToken(t *testing.T) {
	fake := fakeaxiom.Start()
	t.Cleanup(fake.Close)
	r := newRig(t, t.TempDir(), fake, "xaat-wrong")
	if _, err := r.conn.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Notify(sdk.MethodInitialized, nil); err != nil {
		t.Fatal(err)
	}
	for {
		if st := r.status(t); st.State == sdk.StateAuthRequired {
			if st.Reason != sdk.ReasonRevoked {
				t.Fatalf("%+v", st)
			}
			return
		}
	}
}

func TestErrorThenConnected(t *testing.T) {
	fake := fakeaxiom.Start()
	t.Cleanup(fake.Close)
	r := newRig(t, t.TempDir(), fake, fakeaxiom.Token)
	r.play(t, "start.jsonl")
	fake.Fail(http.StatusInternalServerError)
	r.round(t)
	if st := r.status(t); st.State != sdk.StateError {
		t.Fatalf("%+v", st)
	}
	fake.Fail(0)
	r.round(t)
	if st := r.status(t); st.State != sdk.StateConnected {
		t.Fatalf("%+v", st)
	}
}

func TestNewerStateGivesError(t *testing.T) {
	fake := fakeaxiom.Start()
	t.Cleanup(fake.Close)
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, serve.MonitorsFile), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, state, fake, fakeaxiom.Token)
	if _, err := r.conn.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Notify(sdk.MethodInitialized, nil); err != nil {
		t.Fatal(err)
	}
	for {
		if st := r.status(t); st.State == sdk.StateError {
			return
		}
	}
}

func TestMissingToken(t *testing.T) {
	c := sdktest.Start(serve.New(nil, time.Hour, "test"), sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	m, err := c.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil || st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput {
		t.Fatalf("%s %v", m.Params, err)
	}
}
