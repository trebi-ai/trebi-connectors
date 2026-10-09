package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/internal/fakegraph"
)

// cliRig runs the app against one fake Graph, in a temp home.
type cliRig struct {
	t    *testing.T
	fake *fakegraph.Fake
	home string
}

func newRig(t *testing.T) *cliRig {
	t.Helper()
	r := &cliRig{t: t, fake: fakegraph.Start(), home: t.TempDir()}
	t.Cleanup(r.fake.Close)
	t.Setenv("HOME", r.home)
	t.Setenv("TREBI_STATE_DIR", "")
	t.Setenv("MICROSOFT_TODO_CLIENT_ID", fakegraph.ClientID)
	t.Setenv("MICROSOFT_TODO_TENANT", fakegraph.Tenant)
	return r
}

// run runs the app and returns stdout. A failure stops the test.
func (r *cliRig) run(args ...string) string {
	r.t.Helper()
	out, err := r.try(args...)
	if err != nil {
		r.t.Fatalf("microsoft-todo-cli %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (r *cliRig) try(args ...string) (string, error) {
	app := newApp()
	var out, errOut bytes.Buffer
	app.Writer, app.ErrWriter = &out, &errOut
	app.Metadata[cmd.MetaGraphURL] = r.fake.GraphURL()
	app.Metadata[cmd.MetaLoginBase] = r.fake.LoginBase()
	err := app.Run(append([]string{"microsoft-todo-cli"}, args...))
	return out.String(), err
}

// jsonOf runs the app with --json and decodes stdout into v.
func (r *cliRig) jsonOf(v any, args ...string) {
	r.t.Helper()
	out := r.run(append(args, "--json")...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		r.t.Fatalf("microsoft-todo-cli %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestCLI(t *testing.T) {
	r := newRig(t)
	if _, err := r.try("lists", "list"); err == nil || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("no login: %v", err)
	}
	r.run("auth", "login")
	var st struct {
		Valid   bool
		Account string
	}
	r.jsonOf(&st, "auth", "status")
	if !st.Valid || st.Account != fakegraph.AccountName {
		t.Fatalf("status %+v", st)
	}

	var l client.List
	r.jsonOf(&l, "lists", "create", "--name", "Trip")
	if out := r.run("lists", "get", "--list", "trip"); !strings.Contains(out, l.ID) {
		t.Fatalf("list by name: %s", out)
	}
	var task client.Task
	r.jsonOf(&task, "tasks", "create", "--list", "Trip", "--title", "Pack", "--due", "2026-10-20",
		"--reminder", "2026-10-19T09:00", "--tz", "America/Sao_Paulo", "--importance", "high", "--category", "Red", "--note", "socks")
	if task.DueDateTime.DateTime != "2026-10-20T00:00:00" || task.DueDateTime.TimeZone != "America/Sao_Paulo" ||
		!task.IsReminderOn || task.Body.Content != "socks" || task.Categories[0] != "Red" {
		t.Fatalf("create %+v", task)
	}
	r.run("tasks", "complete", "--list", l.ID, "--task", task.ID)
	var tasks []client.Task
	r.jsonOf(&tasks, "tasks", "list", "--list", "Trip", "--status", "open")
	if len(tasks) != 0 {
		t.Fatalf("open tasks %+v", tasks)
	}
	id := task.ID
	task = client.Task{}
	r.jsonOf(&task, "tasks", "update", "--list", "Trip", "--task", id, "--due", "none", "--no-reminder", "--status", "inProgress")
	if task.DueDateTime != nil || task.IsReminderOn || task.Status != "inProgress" {
		t.Fatalf("update %+v", task)
	}
	if _, err := r.try("tasks", "get", "--list", "Trip", task.ID); err == nil {
		t.Fatal("a positional argument is accepted")
	}

	base := []string{"--list", "Trip", "--task", task.ID}
	var step client.ChecklistItem
	r.jsonOf(&step, append([]string{"checklist", "create", "--name", "socks"}, base...)...)
	r.jsonOf(&step, append([]string{"checklist", "check", "--id", step.ID}, base...)...)
	r.jsonOf(&step, append([]string{"checklist", "update", "--id", step.ID, "--name", "wool socks"}, base...)...)
	if !step.IsChecked || step.DisplayName != "wool socks" {
		t.Fatalf("check, then rename %+v", step)
	}
	var link client.LinkedResource
	r.jsonOf(&link, append([]string{"links", "create", "--app", "Web", "--name", "Map", "--url", "https://example.com"}, base...)...)

	file := filepath.Join(r.home, "list.txt")
	if err := os.WriteFile(file, []byte("passport"), 0o600); err != nil {
		t.Fatal(err)
	}
	var att client.Attachment
	r.jsonOf(&att, append([]string{"attachments", "add", "--file", file}, base...)...)
	if att.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("attachment %+v", att)
	}
	if out := r.run(append([]string{"attachments", "get", "--id", att.ID, "-o", "-"}, base...)...); out != "passport" {
		t.Fatalf("download %q", out)
	}
	r.run(append([]string{"extensions", "create", "--name", "com.example.trip", "--data", `{"days":3}`}, base...)...)
	if out := r.run("extensions", "get", "--list", "Trip", "--task", task.ID, "--name", "com.example.trip"); !strings.Contains(out, `"days": 3`) {
		t.Fatalf("extension %s", out)
	}

	var delta struct {
		Value     []client.Task
		DeltaLink string `json:"delta_link"`
	}
	r.jsonOf(&delta, "tasks", "delta", "--list", "Trip")
	r.run("tasks", "delete", "--list", "Trip", "--task", task.ID)
	r.jsonOf(&delta, "tasks", "delta", "--list", "Trip", "--link", delta.DeltaLink)
	if len(delta.Value) != 1 || delta.Value[0].Removed == nil {
		t.Fatalf("delta %+v", delta)
	}
	r.run("lists", "delete", "--list", "Trip")
	r.run("auth", "logout")
	if _, err := os.Stat(filepath.Join(r.home, ".cli-tools", "microsoft-todo-cli", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("auth.json after logout: %v", err)
	}
}

func TestTrebiMode(t *testing.T) {
	r := newRig(t)
	t.Setenv("TREBI_STATE_DIR", t.TempDir())
	for _, args := range [][]string{{"auth", "login"}, {"auth", "logout"}} {
		if _, err := r.try(args...); err == nil || !strings.Contains(err.Error(), "Set this value in Trebi") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := r.try("--tenant", "common", "lists", "list"); err == nil || !strings.Contains(err.Error(), "Trebi sets this value") {
		t.Fatalf("flag in Trebi mode: %v", err)
	}
	if _, err := r.try("lists", "list"); err == nil || !strings.Contains(err.Error(), "in Trebi") {
		t.Fatalf("no login in Trebi mode: %v", err)
	}
}

func TestWatch(t *testing.T) {
	r := newRig(t)
	r.run("auth", "login")
	app := newApp()
	var out syncBuffer
	app.Writer, app.ErrWriter = &out, io.Discard
	app.Metadata[cmd.MetaGraphURL] = r.fake.GraphURL()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.RunContext(ctx, []string{"microsoft-todo-cli", "watch", "--list", "Work", "--interval", "50ms"})
	}()
	time.Sleep(150 * time.Millisecond)
	var task client.Task
	r.jsonOf(&task, "tasks", "create", "--list", "Work", "--title", "Ship")
	time.Sleep(150 * time.Millisecond)
	r.run("tasks", "update", "--list", "Work", "--task", task.ID, "--importance", "high")
	time.Sleep(150 * time.Millisecond)
	r.run("tasks", "delete", "--list", "Work", "--task", task.ID)
	time.Sleep(150 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var c struct{ Change string }
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		got = append(got, c.Change)
	}
	if strings.Join(got, ",") != "created,updated,deleted" {
		t.Fatalf("changes %v", got)
	}
}

// syncBuffer is a bytes.Buffer that two goroutines can use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
