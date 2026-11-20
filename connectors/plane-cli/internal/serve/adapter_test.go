package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/fakeplane"
)

// recorder is an Emitter that keeps the events.
type recorder struct {
	mu     sync.Mutex
	events []sdk.Event
	status []sdk.Status
}

func (r *recorder) Event(e sdk.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) Status(s sdk.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = append(r.status, s)
	return nil
}

func (r *recorder) SubscriptionsChanged() error { return nil }

// take returns the events since the last call.
func (r *recorder) take() []sdk.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func fakeAccess(f *fakeplane.Plane, key string) func(string) Workspace {
	cfg := &config.Config{Host: f.URL(), APIKey: key}
	return func(string) Workspace {
		return Workspace{Client: client.New(cfg), HasKey: key != "", Web: f.URL()}
	}
}

func newTestAdapter(t *testing.T, f *fakeplane.Plane, dir string) *Adapter {
	t.Helper()
	a := New(Options{
		Scope: Scope{fakeplane.Workspace: nil}, Version: "test", Workspace: fakeAccess(f, "k"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := a.UseFolders(sdk.Trebi{StateDir: dir}); err != nil {
		t.Fatal(err)
	}
	res, err := a.Initialize(context.Background(), sdk.InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Account == nil || res.Account.ID != fakeplane.UserID {
		t.Fatalf("account = %+v", res.Account)
	}
	return a
}

// tick polls once and saves the state.
func tick(t *testing.T, a *Adapter, r *recorder) []sdk.Event {
	t.Helper()
	for _, w := range a.watchers {
		if err := w.tick(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	return r.take()
}

func one(t *testing.T, evs []sdk.Event, typ string) map[string]any {
	t.Helper()
	if len(evs) != 1 || evs[0].Type != typ {
		var got []string
		for _, e := range evs {
			got = append(got, e.Type+" "+e.Text)
		}
		t.Fatalf("events = %q, want one %s", got, typ)
	}
	if evs[0].Room == nil || evs[0].Room.ID != "sandbox/SANDBOX" && typ != TypeCustomer {
		t.Errorf("room = %+v", evs[0].Room)
	}
	var d map[string]any
	if err := json.Unmarshal(evs[0].Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAdapterPoll(t *testing.T) {
	f := fakeplane.Start()
	defer f.Close()
	old := f.Create("Old task")
	f.Comment(old.ID, "old comment")
	f.AddIntake("Old request")
	dir := t.TempDir()
	a := newTestAdapter(t, f, dir)
	r := &recorder{}

	if evs := tick(t, a, r); len(evs) != 0 {
		t.Fatalf("the first poll sends %d events, want none", len(evs))
	}

	it := f.Create("New task", "agent")
	d := one(t, tick(t, a, r), TypeCreated)
	if d["identifier"] != "SANDBOX-3" || d["labels"].([]any)[0] != "agent" || d["state_group"] != "backlog" {
		t.Errorf("created = %v", d)
	}

	f.SetState(it.ID, "In Progress")
	d = one(t, tick(t, a, r), TypeStateChanged)
	if d["old"] != "Backlog" || d["new"] != "In Progress" || d["old_state_group"] != "backlog" || d["state_group"] != "started" {
		t.Errorf("state = %v", d)
	}

	f.SetPriority(it.ID, "high")
	d = one(t, tick(t, a, r), TypeUpdated)
	if d["field"] != "priority" || d["old"] != "none" || d["new"] != "high" {
		t.Errorf("priority = %v", d)
	}

	f.Comment(it.ID, "Please start")
	d = one(t, tick(t, a, r), TypeComment)
	if d["comment"] != "Please start" || d["identifier"] != "SANDBOX-3" {
		t.Errorf("comment = %v", d)
	}
	if evs := tick(t, a, r); len(evs) != 0 {
		t.Fatalf("a quiet poll sends %d events", len(evs))
	}

	in := f.AddIntake("Login fails")
	d = one(t, tick(t, a, r), TypeIntakeCreated) // no work_item.created for the held item
	if d["status"] != "pending" || d["identifier"] != "SANDBOX-4" {
		t.Errorf("intake = %v", d)
	}
	f.SetIntake(in, 1)
	d = one(t, tick(t, a, r), TypeIntakeAccepted)
	if d["old"] != "pending" || d["new"] != "accepted" {
		t.Errorf("accepted = %v", d)
	}

	f.UpdateCustomer("Acme", "lead")
	d = one(t, tick(t, a, r), TypeCustomer)
	if d["name"] != "Acme" {
		t.Errorf("customer = %v", d)
	}

	// A restart keeps the cursors: nothing comes again, and new changes come.
	b := newTestAdapter(t, f, dir)
	if evs := tick(t, b, r); len(evs) != 0 {
		t.Fatalf("a restart sends %d old events again: %s %s", len(evs), evs[0].Type, evs[0].Text)
	}
	f.SetState(it.ID, "Done")
	d = one(t, tick(t, b, r), TypeStateChanged)
	if d["old"] != "In Progress" || d["state_group"] != "completed" {
		t.Errorf("after restart = %v", d)
	}
}

func TestAdapterScope(t *testing.T) {
	f := fakeplane.Start()
	defer f.Close()
	a := New(Options{
		Scope: Scope{fakeplane.Workspace: {"OTHER"}}, Workspace: fakeAccess(f, "k"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := a.Initialize(context.Background(), sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	tick(t, a, r)
	f.Create("Not in the scope")
	if evs := tick(t, a, r); len(evs) != 0 {
		t.Fatalf("a project outside the scope sends %d events", len(evs))
	}
}

func TestAdapterInputs(t *testing.T) {
	f := fakeplane.Start()
	defer f.Close()
	cases := []struct {
		opt  Options
		want string
	}{
		{Options{Workspace: fakeAccess(f, "k")}, InputProjects},
		{Options{Scope: Scope{"sandbox": nil}, Workspace: fakeAccess(f, "")}, InputAPIKey},
	}
	for _, c := range cases {
		_, err := New(c.opt).Initialize(context.Background(), sdk.InitializeParams{})
		var mi sdk.MissingInput
		if !errors.As(err, &mi) || mi.Name != c.want {
			t.Errorf("err = %v, want missing %s", err, c.want)
		}
	}
}

// TestServe runs the adapter through the protocol with the scripted fake.
func TestServe(t *testing.T) {
	f := fakeplane.Start()
	defer f.Close()
	f.Auto(50 * time.Millisecond)
	a := New(Options{
		Scope: Scope{fakeplane.Workspace: nil}, Workspace: fakeAccess(f, "k"), Interval: 30 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	conn := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
	defer conn.Close()
	res, err := conn.Initialize(sdk.InitializeParams{Protocol: "trebi-connector/1", Instance: sdk.InstanceInfo{Key: "t", Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != len(Events) || res.Account == nil {
		t.Fatalf("initialize = %+v", res)
	}
	seen := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for len(seen) < 5 && time.Now().Before(deadline) {
		msg, err := conn.WaitNote("event")
		if err != nil {
			t.Fatal(err)
		}
		var e sdk.Event
		if err := json.Unmarshal(msg.Params, &e); err != nil {
			t.Fatal(err)
		}
		seen[e.Type] = true
	}
	for _, typ := range []string{TypeCreated, TypeStateChanged, TypeComment, TypeIntakeCreated, TypeIntakeAccepted} {
		if !seen[typ] {
			t.Errorf("no %s event; saw %v", typ, seen)
		}
	}
}

func TestOps(t *testing.T) {
	f := fakeplane.Start()
	defer f.Close()
	f.Create("Fix login", "agent")
	f.Create("Write docs")
	o := Ops{Scope: Scope{fakeplane.Workspace: nil}, Workspace: fakeAccess(f, "k")}
	run := func(name, in string) string {
		t.Helper()
		var out bytes.Buffer
		if err := o.Run(context.Background(), name, strings.NewReader(in), &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out.String()
	}
	var rows []Row
	if err := json.Unmarshal([]byte(run("search", `{"query":"login"}`)), &rows); err != nil || len(rows) != 1 || rows[0].Key != "SANDBOX-1" {
		t.Errorf("search = %+v %v", rows, err)
	}
	var it map[string]any
	if err := json.Unmarshal([]byte(run("get", `{"key":"sandbox-2"}`)), &it); err != nil || it["name"] != "Write docs" || it["key"] != "SANDBOX-2" {
		t.Errorf("get = %v %v", it, err)
	}
	if err := json.Unmarshal([]byte(run("list", `{"project":"SANDBOX","label":"agent"}`)), &rows); err != nil || len(rows) != 1 || rows[0].Labels[0] != "agent" {
		t.Errorf("list = %+v %v", rows, err)
	}
	o.Scope = Scope{fakeplane.Workspace: {"OTHER"}}
	if err := o.Run(context.Background(), "get", strings.NewReader(`{"key":"SANDBOX-1"}`), io.Discard); err == nil {
		t.Error("get outside the scope has no error")
	}
	if err := o.Run(context.Background(), "get", strings.NewReader(`{"key":"X-1","workspace":"elsewhere"}`), io.Discard); err == nil {
		t.Error("a workspace outside the scope has no error")
	}
}
