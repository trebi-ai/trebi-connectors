package serve_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/fakemail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/serve"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

func start(t *testing.T, a *serve.Adapter, dir string) (*sdktest.Conn, sdk.InitializeResult, sdk.Status) {
	t.Helper()
	c := sdktest.Start(a, sdk.WithStateDir(dir))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	res, err := c.Initialize(sdk.InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	return c, res, nextStatus(t, c)
}

func nextStatus(t *testing.T, c *sdktest.Conn) sdk.Status {
	t.Helper()
	m, err := c.WaitNote(sdk.MethodStatus)
	if err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	if err := json.Unmarshal(m.Params, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func account(t *testing.T) (*fakemail.Server, config.Settings, *mail.Mailbox) {
	t.Helper()
	fake, err := fakemail.Start("me@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	s := config.Settings{Address: "me@example.com", Password: "pw", IMAPHost: fake.IMAPAddr(), SMTPHost: fake.SMTPAddr()}
	return fake, s, mail.New(s, mail.WithTLS(fake.ClientTLS()))
}

func TestConnectedSendsEvents(t *testing.T) {
	fake, s, mb := account(t)
	c, res, st := start(t, serve.New(mb, s, "test"), t.TempDir())
	if st.State != sdk.StateConnected || res.Account == nil || res.Account.ID != "me@example.com" {
		t.Fatalf("status %+v, account %+v", st, res.Account)
	}
	if len(res.Events) != 1 || res.Events[0].Type != watch.EventType {
		t.Fatalf("events %v", res.Events)
	}
	time.Sleep(200 * time.Millisecond) // let the baseline land first
	raw := "From: Ann <ann@example.com>\r\nTo: me@example.com\r\nSubject: Hi\r\nMessage-Id: <a1@example.com>\r\n\r\nHello there\r\n"
	if err := fake.Deliver("INBOX", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	m, err := c.WaitNote(sdk.MethodEvent)
	if err != nil {
		t.Fatal(err)
	}
	var e sdk.Event
	if err := json.Unmarshal(m.Params, &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != watch.EventType || e.Sender == nil || e.Sender.ID != "ann@example.com" {
		t.Fatalf("event %+v", e)
	}
}

func TestMissingInput(t *testing.T) {
	s := config.Settings{Address: "me@example.com", IMAPHost: "127.0.0.1:1", SMTPHost: "127.0.0.1:1"}
	c, _, st := start(t, serve.New(mail.New(s), s, "test", serve.WithWatchOptions(watch.WithBackoff(time.Hour, time.Hour))), t.TempDir())
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput {
		t.Fatalf("status %+v", st)
	}
	if err := c.Call(sdk.MethodPing, sdk.Empty{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBadPassword(t *testing.T) {
	fake, s, _ := account(t)
	s.Password = "wrong"
	mb := mail.New(s, mail.WithTLS(fake.ClientTLS()))
	c, _, st := start(t, serve.New(mb, s, "test", serve.WithWatchOptions(watch.WithBackoff(time.Hour, time.Hour))), t.TempDir())
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonRevoked {
		t.Fatalf("status %+v", st)
	}
	// The watcher adds the message for the user.
	for st.Message == "" {
		if st = nextStatus(t, c); st.State != sdk.StateAuthRequired {
			t.Fatalf("status %+v", st)
		}
	}
}

func TestNewerStateFails(t *testing.T) {
	_, s, mb := account(t)
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/cursor.json", []byte(`{"version":99,"uidvalidity":1,"last_uid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := sdktest.Start(serve.New(mb, s, "test"), sdk.WithStateDir(dir))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck,gosec // the test checks the calls
	if _, err := c.Initialize(sdk.InitializeParams{}); err == nil {
		t.Fatal("initialize: no error for a newer state")
	}
}
