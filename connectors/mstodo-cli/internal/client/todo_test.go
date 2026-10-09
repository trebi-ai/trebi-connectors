package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/fakegraph"
)

// login runs the device code login on a fake and returns the client.
func login(t *testing.T) (*client.Client, *fakegraph.Fake) {
	t.Helper()
	fake := fakegraph.Start()
	t.Cleanup(fake.Close)
	cl := client.New(fakegraph.ClientID, fakegraph.Tenant, client.Session{})
	cl.GraphURL, cl.LoginBase = fake.GraphURL(), fake.LoginBase()
	ctx := context.Background()
	dc, err := cl.BeginDeviceCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := cl.WaitDeviceCode(ctx, dc)
	if err != nil {
		t.Fatal(err)
	}
	if sess.AccountID != fakegraph.AccountID || sess.AccountName != fakegraph.AccountName || sess.Tenant != fakegraph.Tenant || sess.ClientID != fakegraph.ClientID {
		t.Fatalf("session %+v", sess)
	}
	cl.SetSession(sess)
	return cl, fake
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestListsAndTasks(t *testing.T) {
	t.Parallel()
	cl, _ := login(t)
	ctx := context.Background()
	l := must[client.List](t)(cl.CreateList(ctx, "Home"))
	l = must[client.List](t)(cl.UpdateList(ctx, l.ID, map[string]string{"displayName": "House"}))
	if l.DisplayName != "House" {
		t.Fatalf("rename %+v", l)
	}
	for _, title := range []string{"a", "b", "c"} {
		must[client.Task](t)(cl.CreateTask(ctx, l.ID, map[string]any{"title": title}))
	}
	task := must[client.Task](t)(cl.CreateTask(ctx, l.ID, map[string]any{
		"title": "d", "importance": "high", "dueDateTime": client.DateTimeZone{DateTime: "2026-10-10T00:00:00", TimeZone: "UTC"},
	}))
	if task.Importance != "high" || task.DueDateTime == nil || !task.IsNew() {
		t.Fatalf("create %+v", task)
	}
	task = must[client.Task](t)(cl.UpdateTask(ctx, l.ID, task.ID, map[string]string{"status": "completed"}))
	if task.Status != "completed" || task.IsNew() {
		t.Fatalf("update %+v", task)
	}
	all := must[[]client.Task](t)(cl.Tasks(ctx, l.ID, client.Query{}))
	open := must[[]client.Task](t)(cl.Tasks(ctx, l.ID, client.Query{Filter: "status ne 'completed'"}))
	two := must[[]client.Task](t)(cl.Tasks(ctx, l.ID, client.Query{Limit: 2}))
	if len(all) != 4 || len(open) != 3 || len(two) != 2 {
		t.Fatalf("tasks %d open %d limit %d", len(all), len(open), len(two))
	}
	_, link := must2(t)(cl.Delta(ctx, l.ID, ""))
	check(t, cl.DeleteTask(ctx, l.ID, task.ID))
	changes, _ := must2(t)(cl.Delta(ctx, l.ID, link))
	if len(changes) != 1 || changes[0].ID != task.ID || changes[0].Removed == nil {
		t.Fatalf("delta %+v", changes)
	}
	if _, err := cl.Task(ctx, l.ID, task.ID); !client.IsNotFound(err) {
		t.Fatalf("deleted task: %v", err)
	}
	lists, llink, err := cl.ListsDelta(ctx, "")
	check(t, err)
	if len(lists) != 3 {
		t.Fatalf("lists delta %+v", lists)
	}
	check(t, cl.DeleteList(ctx, l.ID))
	lists, _, err = cl.ListsDelta(ctx, llink)
	check(t, err)
	if len(lists) != 1 || lists[0].Removed == nil {
		t.Fatalf("lists delta after delete %+v", lists)
	}
}

func must2(t *testing.T) func([]client.Task, string, error) ([]client.Task, string) {
	return func(v []client.Task, link string, err error) ([]client.Task, string) {
		t.Helper()
		check(t, err)
		return v, link
	}
}

func TestTaskChildren(t *testing.T) {
	t.Parallel()
	cl, fake := login(t)
	ctx := context.Background()
	task := fake.AddTask("list-work", "Plan")
	ref := client.Ref{List: "list-work", Task: task.ID}

	step := must[client.ChecklistItem](t)(cl.CreateChecklistItem(ctx, ref, map[string]string{"displayName": "one"}))
	step = must[client.ChecklistItem](t)(cl.UpdateChecklistItem(ctx, ref, step.ID, map[string]bool{"isChecked": true}))
	if !step.IsChecked || step.CheckedDateTime == "" {
		t.Fatalf("step %+v", step)
	}
	if got := must[[]client.ChecklistItem](t)(cl.ChecklistItems(ctx, ref)); len(got) != 1 {
		t.Fatalf("steps %+v", got)
	}
	check(t, cl.DeleteChecklistItem(ctx, ref, step.ID))

	link := must[client.LinkedResource](t)(cl.CreateLinkedResource(ctx, ref, map[string]string{"applicationName": "App", "displayName": "Doc", "webUrl": "https://example.com"}))
	link = must[client.LinkedResource](t)(cl.UpdateLinkedResource(ctx, ref, link.ID, map[string]string{"displayName": "Doc 2"}))
	if got := must[client.LinkedResource](t)(cl.LinkedResource(ctx, ref, link.ID)); got.DisplayName != "Doc 2" {
		t.Fatalf("link %+v", got)
	}
	check(t, cl.DeleteLinkedResource(ctx, ref, link.ID))

	for _, size := range []int{10, client.SmallAttachment + 1<<20} {
		data := bytes.Repeat([]byte("x"), size)
		a := must[client.Attachment](t)(cl.AddAttachment(ctx, ref, "f.txt", "text/plain", data))
		if a.ID == "" {
			t.Fatalf("attachment %+v", a)
		}
		got := must[[]byte](t)(cl.AttachmentContent(ctx, ref, a.ID))
		if !bytes.Equal(got, data) {
			t.Fatalf("content of %d bytes is %d bytes", size, len(got))
		}
		if full := must[client.Attachment](t)(cl.Attachment(ctx, ref, a.ID)); !bytes.Equal(full.ContentBytes, data) {
			t.Fatalf("contentBytes of %d bytes", size)
		}
	}
	if task := must[client.Task](t)(cl.Task(ctx, ref.List, ref.Task)); !task.HasAttachments {
		t.Fatal("hasAttachments is false")
	}
	as := must[[]client.Attachment](t)(cl.Attachments(ctx, ref))
	if len(as) != 2 || as[0].ContentBytes != nil {
		t.Fatalf("attachments %+v", as)
	}
	check(t, cl.DeleteAttachment(ctx, ref, as[0].ID))
	if _, err := cl.AddAttachment(ctx, ref, "big", "", make([]byte, client.MaxAttachment+1)); err == nil {
		t.Fatal("a file over 25 MB is accepted")
	}

	for _, owner := range []client.Ref{{List: "list-work"}, ref} {
		must[json.RawMessage](t)(cl.CreateExtension(ctx, owner, "com.example.x", map[string]any{"n": 1}))
		check(t, cl.UpdateExtension(ctx, owner, "com.example.x", map[string]any{"n": 2}))
		var x struct{ N int }
		check(t, json.Unmarshal(must[json.RawMessage](t)(cl.Extension(ctx, owner, "com.example.x")), &x))
		if x.N != 2 {
			t.Fatalf("extension on %+v: %+v", owner, x)
		}
		if l := must[[]json.RawMessage](t)(cl.Extensions(ctx, owner)); len(l) != 1 {
			t.Fatalf("extensions %s", l)
		}
		check(t, cl.DeleteExtension(ctx, owner, "com.example.x"))
	}
}

func TestSubscriptions(t *testing.T) {
	t.Parallel()
	cl, _ := login(t)
	ctx := context.Background()
	_, err := cl.CreateSubscription(ctx, client.Subscription{
		Resource: client.TaskResource("list-work"), ChangeType: "created", NotificationURL: "http://127.0.0.1:1/none",
		ExpirationDateTime: time.Now().Add(time.Hour),
	})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("a URL that does not answer: %v", err)
	}
	if _, err := cl.Subscription(ctx, "sub-none"); !client.IsNotFound(err) {
		t.Fatalf("missing subscription: %v", err)
	}
	check(t, cl.DeleteSubscription(ctx, "sub-none"))
}

func TestRefreshAndGuard(t *testing.T) {
	t.Parallel()
	cl, _ := login(t)
	ctx := context.Background()
	s := cl.Session()
	s.AccessToken = "stale"
	cl.SetSession(s)
	var saved client.Session
	cl.OnRefresh = func(s client.Session) error { saved = s; return nil }
	check(t, cl.Ping(ctx))
	if saved.AccessToken == "" || saved.ClientID != fakegraph.ClientID || saved.AccountID != fakegraph.AccountID {
		t.Fatalf("refresh %+v", saved)
	}
	s.AccessToken, s.RefreshToken = "stale", fakegraph.LegacyRefreshToken+"1"
	cl.SetSession(s)
	check(t, cl.Ping(ctx))
	if _, err := cl.Send(ctx, client.Request{Method: "GET", Path: "https://example.com/x"}); err == nil {
		t.Fatal("the token goes to another host")
	}
	s.RefreshToken = "bad"
	cl.SetSession(s)
	if err := cl.Ping(ctx); !errors.Is(err, client.ErrAuth) {
		t.Fatalf("bad refresh: %v", err)
	}
}
