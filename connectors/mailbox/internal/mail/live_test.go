//go:build live

// Live tests against a real account. Set MAILBOX_ADDRESS, MAILBOX_PASSWORD,
// and MAILBOX_PROVIDER (for example gmail), then run:
//
//	go test -tags live -run Live -v ./internal/mail
//
// They change only drafts that they make. MAILBOX_LIVE_SEND=1 also sends
// one message to the account, then marks and archives it.
package mail_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
)

func live(t *testing.T) (*mail.Mailbox, config.Settings, context.Context) {
	t.Helper()
	s, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if s.Missing() != nil {
		t.Skip("set MAILBOX_ADDRESS, MAILBOX_PASSWORD, and MAILBOX_PROVIDER")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return mail.New(s), s, ctx
}

func TestLiveRead(t *testing.T) {
	mb, _, ctx := live(t)
	if err := mb.Check(ctx); err != nil {
		t.Fatalf("check: %v (%s)", err, mb.AuthMessage(err))
	}
	folders, err := mb.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]bool{}
	for _, f := range folders {
		roles[f.Role] = true
	}
	if !roles["inbox"] || !roles["drafts"] {
		t.Fatalf("folders %+v", folders)
	}
	page, err := mb.Search(ctx, mail.SearchQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first page: %d messages, next %q", len(page.Messages), page.NextCursor)
	if page.NextCursor != "" {
		next, err := mb.Search(ctx, mail.SearchQuery{Limit: 2, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Messages) > 0 && next.Messages[0].ID == page.Messages[0].ID {
			t.Fatal("the second page repeats the first page")
		}
	}
	if len(page.Messages) == 0 {
		return
	}
	id := page.Messages[0].ID
	if _, err := mb.Read(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	th, err := mb.Thread(ctx, id, 10, false)
	if err != nil || len(th.Messages) == 0 {
		t.Fatalf("thread %+v err %v", th, err)
	}
	r, err := mb.Reply(ctx, mail.ReplyInput{ID: id, Text: "Live test of mailbox. Delete me."}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.DraftID == "" {
		t.Fatalf("reply %+v", r)
	}
	if err := mb.DeleteDraft(ctx, r.DraftID); err != nil {
		t.Fatal(err)
	}
}

func TestLiveGmail(t *testing.T) {
	mb, s, ctx := live(t)
	if !strings.HasPrefix(s.IMAPHost, "imap.gmail.com") {
		t.Skip("set MAILBOX_PROVIDER=gmail")
	}
	page, err := mb.Search(ctx, mail.SearchQuery{Gmail: "in:inbox", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) == 0 {
		t.Skip("the inbox is empty")
	}
	sum := page.Messages[0]
	if !strings.HasPrefix(sum.ID, mail.GmailPrefix) || sum.ThreadID == "" || !slices.Contains(sum.Labels, "Inbox") {
		t.Fatalf("summary %+v", sum)
	}
	msg, err := mb.Read(ctx, sum.ID, false)
	if err != nil || msg.ID != sum.ID || msg.ThreadID != sum.ThreadID {
		t.Fatalf("read %s: %+v %v", sum.ID, msg.ID, err)
	}
	th, err := mb.Thread(ctx, sum.ID, 10, false)
	if err != nil || th.ThreadID != sum.ThreadID || !slices.ContainsFunc(th.Messages, func(m mail.Message) bool { return m.ID == sum.ID }) {
		t.Fatalf("thread %+v err %v", th.ThreadID, err)
	}
	if _, err := mb.Search(ctx, mail.SearchQuery{Gmail: "in:inbox", Query: "the", Limit: 2}); err != nil {
		t.Fatalf("gmail_query with query: %v", err)
	}
}

func TestLiveWatchConnects(t *testing.T) {
	mb, _, ctx := live(t)
	conn, err := mb.Watch(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.UIDValidity == 0 {
		t.Fatal("no UIDVALIDITY")
	}
	stop, err := conn.Idle()
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveSendAndSort(t *testing.T) {
	if os.Getenv("MAILBOX_LIVE_SEND") != "1" {
		t.Skip("set MAILBOX_LIVE_SEND=1")
	}
	mb, s, ctx := live(t)
	conn, err := mb.Watch(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	last, err := conn.LastUID()
	if err != nil {
		t.Fatal(err)
	}
	subject := "mailbox live test " + time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := mb.Send(ctx, mail.Compose{To: []string{s.Address}, Subject: subject, HTML: "<p>Live test. <b>Delete me.</b></p>"}); err != nil {
		t.Fatal(err)
	}
	var id string
	for end := time.Now().Add(time.Minute); id == "" && time.Now().Before(end); time.Sleep(3 * time.Second) {
		res, err := mb.Search(ctx, mail.SearchQuery{Query: subject, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Messages) == 1 && strings.Contains(res.Messages[0].Subject, subject) {
			id = res.Messages[0].ID
		}
	}
	if id == "" {
		t.Fatal("the sent message did not arrive")
	}
	if err := conn.Noop(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	news, err := conn.Fetch(last)
	if err != nil || !slices.ContainsFunc(news, func(n mail.NewMail) bool { return n.ID == id || n.Subject == subject }) {
		t.Fatalf("new mail %+v err %v", news, err)
	}
	if strings.HasPrefix(s.IMAPHost, "imap.gmail.com") && !slices.ContainsFunc(news, func(n mail.NewMail) bool { return n.ID == id && slices.Contains(n.Labels, "Inbox") }) {
		t.Fatalf("new mail has no Gmail id %s: %+v", id, news)
	}
	read, starred := true, true
	for _, step := range []func() (mail.UpdateResult, error){
		func() (mail.UpdateResult, error) {
			return mb.Mark(ctx, mail.MarkInput{IDs: []string{id}, Read: &read, Starred: &starred})
		},
		func() (mail.UpdateResult, error) { return mb.Archive(ctx, []string{id}) },
	} {
		res, err := step()
		if err != nil || len(res.Failed) > 0 {
			t.Fatalf("result %+v err %v", res, err)
		}
	}
	if !strings.HasPrefix(s.IMAPHost, "imap.gmail.com") {
		return
	}
	msg, err := mb.Read(ctx, id, false)
	if err != nil || slices.Contains(msg.Labels, "Inbox") || !slices.Contains(msg.Labels, "Starred") {
		t.Fatalf("after archive %v err %v", msg.Labels, err)
	}
	res, err := mb.Label(ctx, mail.LabelInput{IDs: []string{id}, Remove: []string{"Starred"}})
	if err != nil || len(res.Failed) > 0 {
		t.Fatalf("label %+v err %v", res, err)
	}
}
