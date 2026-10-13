package serve_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/fakeweb"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

type rig struct {
	fake   *fakeweb.Server
	tick   chan time.Time
	conn   *sdktest.Conn
	state  string
	vars   map[string]string
	closed bool
}

func (r *rig) close(t *testing.T) {
	t.Helper()
	r.closed = true
	if err := r.conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func newRig(t *testing.T, state string, fake *fakeweb.Server) *rig {
	t.Helper()
	r := &rig{fake: fake, tick: make(chan time.Time), state: state}
	r.vars = map[string]string{"{{rss}}": fake.URL(fakeweb.RSSPath), "{{page}}": fake.URL(fakeweb.PagePath)}
	s := config.Settings{Targets: []config.Target{{URL: fake.URL(fakeweb.RSSPath)}, {URL: fake.URL(fakeweb.PagePath), Selector: "#price"}}, Every: time.Hour}
	a := serve.New(watch.NewFetcher(nil, "test"), s, "test", serve.WithTick(r.tick))
	r.conn = sdktest.Start(a, sdk.WithStateDir(state))
	t.Cleanup(func() {
		if !r.closed {
			r.conn.Close() //nolint:errcheck,gosec // the test checks the calls
		}
	})
	return r
}

// play runs a transcript from testdata with the addresses of the fake.
func (r *rig) play(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for k, v := range r.vars {
		s = strings.ReplaceAll(s, k, v)
	}
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

// round waits for the current round to end and starts the next one.
func (r *rig) round(t *testing.T) {
	t.Helper()
	select {
	case r.tick <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("the round did not end")
	}
}

func TestEntriesAndPageChange(t *testing.T) {
	fake := fakeweb.Start()
	t.Cleanup(fake.Close)
	r := newRig(t, t.TempDir(), fake)
	r.play(t, "start.jsonl")
	r.round(t) // the first round only records the entries and the text
	r.round(t) // no change gives no event
	fake.Add(fakeweb.Entry{ID: "e2", Title: "Second post", Link: "https://example.com/2", Published: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)})
	fake.SetPrice("12")
	r.round(t)
	r.play(t, "change.jsonl")
	r.round(t) // the same entry and text again give no event
	r.round(t)
	r.play(t, "ping.jsonl")
}

func TestRestartKeepsSeenEntries(t *testing.T) {
	fake := fakeweb.Start()
	t.Cleanup(fake.Close)
	state := t.TempDir()
	r := newRig(t, state, fake)
	r.play(t, "start.jsonl")
	r.round(t)
	r.close(t)
	// A restore copies the state folder to a new path.
	moved := filepath.Join(t.TempDir(), "state")
	if err := os.CopyFS(moved, os.DirFS(state)); err != nil {
		t.Fatal(err)
	}
	r = newRig(t, moved, fake)
	r.play(t, "start.jsonl")
	r.round(t)
	r.round(t)
	r.play(t, "ping.jsonl")
	b, err := os.ReadFile(filepath.Join(moved, serve.FeedsFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Version != 1 || strings.Contains(string(b), state) {
		t.Fatalf("state %s: %v", b, err)
	}
}

func TestNewerStateGivesError(t *testing.T) {
	fake := fakeweb.Start()
	t.Cleanup(fake.Close)
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, serve.FeedsFile), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, state, fake)
	r.play(t, "start.jsonl")
	m, err := r.conn.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil || st.State != sdk.StateError || !strings.Contains(st.Message, "format 99") {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestUnreachableGivesError(t *testing.T) {
	fake := fakeweb.Start()
	fake.Close()
	r := newRig(t, t.TempDir(), fake)
	r.play(t, "start.jsonl")
	m, err := r.conn.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil || st.State != sdk.StateError {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestMissingInput(t *testing.T) {
	c := sdktest.Start(serve.New(watch.NewFetcher(nil, "test"), config.Settings{}, "test"), sdk.WithStateDir(t.TempDir()))
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
		t.Fatalf("%+v %v", st, err)
	}
}
