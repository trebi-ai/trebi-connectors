package serve_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/fakemail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"
)

func start(t *testing.T, a *serve.Adapter) (*sdktest.Conn, sdk.InitializeResult, sdk.Status) {
	t.Helper()
	c := sdktest.Start(a, sdk.WithStateDir(t.TempDir()))
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

func TestConnected(t *testing.T) {
	fake, err := fakemail.Start("me@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	s := config.Settings{Address: "me@example.com", Password: "pw", IMAPHost: fake.IMAPAddr(), SMTPHost: fake.SMTPAddr()}
	_, res, st := start(t, serve.New(mail.New(s, mail.WithTLS(fake.ClientTLS())), s, "test"))
	if st.State != sdk.StateConnected || res.Account == nil || res.Account.ID != "me@example.com" {
		t.Fatalf("status %+v, account %+v", st, res.Account)
	}
	if len(res.Features) != 0 || len(res.Events) != 0 {
		t.Fatalf("features %v, events %v", res.Features, res.Events)
	}
}

func TestMissingInput(t *testing.T) {
	s := config.Settings{Address: "me@example.com", IMAPHost: "x:993", SMTPHost: "x:587"}
	c, _, st := start(t, serve.New(&fakeChecker{}, s, "test"))
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonMissingInput {
		t.Fatalf("status %+v", st)
	}
	if err := c.Call(sdk.MethodPing, sdk.Empty{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStatusChanges(t *testing.T) {
	chk := &fakeChecker{err: mail.ErrAuth}
	s := config.Settings{Address: "me@example.com", Password: "pw", IMAPHost: "x:993", SMTPHost: "x:587"}
	c, _, st := start(t, serve.New(chk, s, "test", serve.WithCheckInterval(20*time.Millisecond)))
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonRevoked {
		t.Fatalf("status %+v", st)
	}
	chk.set(nil)
	if st := nextStatus(t, c); st.State != sdk.StateConnected {
		t.Fatalf("status %+v", st)
	}
	chk.set(errors.New("dial tcp: no route"))
	if st := nextStatus(t, c); st.State != sdk.StateError || st.Message == "" {
		t.Fatalf("status %+v", st)
	}
}

type fakeChecker struct {
	mu  sync.Mutex
	err error
}

func (f *fakeChecker) set(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeChecker) Check(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}
