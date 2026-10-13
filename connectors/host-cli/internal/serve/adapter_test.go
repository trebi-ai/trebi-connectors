package serve_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/fakessh"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/sshx"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

// values is the output of the fake server, which a test changes.
type values struct {
	mu sync.Mutex
	v  fakessh.Values
}

func (v *values) set(n fakessh.Values) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.v = n
}

func (v *values) reply(int, string) (string, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return fakessh.Output(v.v), 0
}

type rig struct {
	fake  *fakessh.Server
	vals  *values
	tick  chan time.Time
	conn  *sdktest.Conn
	state string
}

func newRig(t *testing.T, state string) *rig {
	t.Helper()
	vals := &values{v: fakessh.Values{CPU: 20, DiskPct: 50}}
	fake, err := fakessh.Start(vals.reply)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	r := &rig{fake: fake, vals: vals, tick: make(chan time.Time), state: state}
	r.start(t, config.Settings{})
	return r
}

// start runs the adapter. s gives the thresholds; the fake gives the server.
func (r *rig) start(t *testing.T, s config.Settings) {
	t.Helper()
	s.Addr, s.User, s.Key, s.HostKey = r.fake.Addr(), fakessh.User, r.fake.ClientKey(), r.fake.HostKey()
	s.Every, s.Samples = time.Hour, true
	if s.CPUAbove == 0 {
		s.CPUAbove, s.MemAbove, s.DiskAbove = 90, 90, 90
	}
	cl, err := sshx.New(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() }) //nolint:errcheck,gosec // test client
	n := int64(0)
	clock := func() time.Time {
		n++
		return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
	}
	a := serve.New(cl, s, "test", serve.WithTick(r.tick), serve.WithClock(clock))
	r.conn = sdktest.Start(a, sdk.WithStateDir(r.state))
	conn := r.conn
	t.Cleanup(func() {
		if r.conn == conn {
			conn.Close() //nolint:errcheck,gosec // the test checks the calls
		}
	})
}

// stop closes the adapter before a new start.
func (r *rig) stop(t *testing.T) {
	t.Helper()
	if err := r.conn.Close(); err != nil {
		t.Fatal(err)
	}
	r.conn = nil
}

func (r *rig) play(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := strings.Cut(r.fake.Addr(), ":")
	s := strings.ReplaceAll(string(b), "{{addr}}", host+":"+port)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := sdktest.LoadTranscript(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Play(lines); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func (r *rig) round(t *testing.T) {
	t.Helper()
	select {
	case r.tick <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("the round did not end")
	}
}

func TestSamplesAndThresholds(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.play(t, "start.jsonl")
	r.vals.set(fakessh.Values{CPU: 20, DiskPct: 95, FailedUnit: true})
	r.round(t)
	r.play(t, "breach.jsonl")
	r.vals.set(fakessh.Values{CPU: 20, DiskPct: 50})
	r.round(t)
	r.play(t, "recover.jsonl")
}

func TestDownAndUp(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.play(t, "start.jsonl")
	r.vals.set(fakessh.Values{})
	r.fake.SetReply(func(int, string) (string, int) { return "", 1 })
	r.round(t)
	r.play(t, "down.jsonl")
	r.fake.SetReply(r.vals.reply)
	r.vals.set(fakessh.Values{CPU: 20, DiskPct: 50})
	r.round(t)
	r.play(t, "up.jsonl")
}

func TestRefusedKey(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.play(t, "start.jsonl")
	r.fake.Close()
	fake, err := fakessh.Start(r.vals.reply)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	fake.Refuse(true)
	r.stop(t)
	r.fake = fake
	r.start(t, config.Settings{})
	if _, err := r.conn.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Notify(sdk.MethodInitialized, struct{}{}); err != nil {
		t.Fatal(err)
	}
	for {
		m, err := r.conn.WaitNote(sdk.MethodStatus)
		if err != nil {
			t.Fatal(err)
		}
		var st sdk.Status
		if err := json.Unmarshal(m.Params, &st); err != nil {
			t.Fatal(err)
		}
		if st.State == sdk.StateAuthRequired {
			if st.Reason != sdk.ReasonRevoked {
				t.Fatalf("%+v", st)
			}
			return
		}
	}
}

func TestRestartKeepsState(t *testing.T) {
	state := t.TempDir()
	r := newRig(t, state)
	r.play(t, "start.jsonl")
	r.vals.set(fakessh.Values{CPU: 20, DiskPct: 95, FailedUnit: true})
	r.round(t)
	r.play(t, "breach.jsonl")
	r.stop(t)
	// A restore copies the state folder to a new path. The breach is known, so no event repeats.
	moved := filepath.Join(t.TempDir(), "state")
	if err := os.CopyFS(moved, os.DirFS(state)); err != nil {
		t.Fatal(err)
	}
	r.state = moved
	r.start(t, config.Settings{})
	r.play(t, "restart.jsonl")
	r.vals.set(fakessh.Values{CPU: 20, DiskPct: 50})
	r.round(t)
	r.play(t, "recover.jsonl")
}

func TestMissingInput(t *testing.T) {
	c := sdktest.Start(serve.New(nil, config.Settings{Addr: "h:22"}, "test"), sdk.WithStateDir(t.TempDir()))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	m, err := c.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil || st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput || !strings.Contains(string(m.Params), config.EnvUser) {
		t.Fatalf("%s %v", m.Params, err)
	}
}
