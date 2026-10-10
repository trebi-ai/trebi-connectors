package mail_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/fakemail"
	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
)

const (
	addr = "me@example.com"
	pass = "app-password"
)

const plainMsg = "From: Ann <ann@example.com>\r\nTo: me@example.com\r\nSubject: Lunch on Friday\r\nDate: Mon, 06 Oct 2026 10:00:00 +0000\r\nMessage-ID: <lunch-1@example.com>\r\n\r\nCan we meet at noon on Friday?\r\n"

const htmlMsg = "From: Bob <bob@example.com>\r\nTo: me@example.com\r\nSubject: Invoice 42\r\nDate: Tue, 07 Oct 2026 10:00:00 +0000\r\nMessage-ID: <inv-42@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n--XX\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><head><style>p{color:red}</style></head><body><p>Hello,</p><p>Your invoice is <b>ready</b>. <a href=\"https://pay.example.com/42\">Pay</a></p><script>alert(1)</script></body></html>\r\n--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"invoice.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--XX--\r\n"

func setup(t *testing.T, sent string) (*fakemail.Server, *mail.Mailbox) {
	t.Helper()
	fake, err := fakemail.Start(addr, pass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	for _, m := range []string{plainMsg, htmlMsg} {
		if err := fake.Deliver("INBOX", []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Settings{Address: addr, Password: pass, IMAPHost: fake.IMAPAddr(), SMTPHost: fake.SMTPAddr(), Drafts: "Drafts", Sent: sent}
	return fake, mail.New(cfg, mail.WithTLS(fake.ClientTLS()))
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestSearch(t *testing.T) {
	_, mb := setup(t, "")
	all, err := mb.Search(ctx(t), mail.SearchQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Subject != "Invoice 42" || all[1].Subject != "Lunch on Friday" {
		t.Fatalf("want newest first, got %+v", all)
	}
	if all[1].ID != "INBOX:1" || all[1].MessageID != "<lunch-1@example.com>" || all[1].From != "Ann <ann@example.com>" {
		t.Fatalf("summary: %+v", all[1])
	}
	if !strings.Contains(all[1].Snippet, "meet at noon") || !strings.Contains(all[0].Snippet, "invoice is ready") {
		t.Fatalf("snippets: %q / %q", all[1].Snippet, all[0].Snippet)
	}
	got, err := mb.Search(ctx(t), mail.SearchQuery{Query: "Friday", Limit: 5})
	if err != nil || len(got) != 1 || got[0].Subject != "Lunch on Friday" {
		t.Fatalf("text search: %+v %v", got, err)
	}
	got, err = mb.Search(ctx(t), mail.SearchQuery{From: "bob@example.com"})
	if err != nil || len(got) != 1 || got[0].Subject != "Invoice 42" {
		t.Fatalf("from search: %+v %v", got, err)
	}
	got, err = mb.Search(ctx(t), mail.SearchQuery{Limit: 1})
	if err != nil || len(got) != 1 || got[0].Subject != "Invoice 42" {
		t.Fatalf("limit: %+v %v", got, err)
	}
	if _, err := mb.Search(ctx(t), mail.SearchQuery{Since: "last week"}); err == nil {
		t.Fatal("want an error for a bad date")
	}
}

func TestReadHTMLAsText(t *testing.T) {
	_, mb := setup(t, "")
	msg, err := mb.Read(ctx(t), "INBOX:2", false)
	if err != nil {
		t.Fatal(err)
	}
	if msg.HTML != "" {
		t.Fatal("html must be empty by default")
	}
	want := "Hello,\n\nYour invoice is ready. Pay (https://pay.example.com/42)"
	if msg.Text != want {
		t.Fatalf("text:\n%q\nwant\n%q", msg.Text, want)
	}
	if strings.Contains(msg.Text, "alert") || strings.Contains(msg.Text, "color") {
		t.Fatal("script or style text leaked")
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Name != "invoice.pdf" || msg.Attachments[0].Type != "application/pdf" || msg.Attachments[0].Size != 9 {
		t.Fatalf("attachments: %+v", msg.Attachments)
	}
	if msg.Headers.Subject != "Invoice 42" || msg.Headers.MessageID != "<inv-42@example.com>" {
		t.Fatalf("headers: %+v", msg.Headers)
	}
	msg, err = mb.Read(ctx(t), "INBOX:2", true)
	if err != nil || !strings.Contains(msg.HTML, "<b>ready</b>") {
		t.Fatalf("html: %q %v", msg.HTML, err)
	}
	if _, err := mb.Read(ctx(t), "INBOX:99", false); err == nil {
		t.Fatal("want an error for an unknown message")
	}
	if _, err := mb.Read(ctx(t), "nope", false); err == nil {
		t.Fatal("want an error for a bad id")
	}
}

func TestDraftLandsInDrafts(t *testing.T) {
	fake, mb := setup(t, "")
	res, err := mb.Draft(ctx(t), mail.Compose{To: []string{"Ann <ann@example.com>"}, Subject: "Re: Lunch on Friday", Text: "Yes, noon works.\nSee you.", InReplyTo: "<lunch-1@example.com>"})
	if err != nil {
		t.Fatal(err)
	}
	if res.DraftID != "Drafts:1" || res.Folder != "Drafts" || !strings.HasSuffix(res.MessageID, "@example.com>") {
		t.Fatalf("result: %+v", res)
	}
	flags, raw := fetchOne(t, fake, "Drafts")
	if !hasFlag(flags, imap.FlagDraft) {
		t.Fatalf("flags %v have no \\Draft", flags)
	}
	msg, err := mail.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Headers.From != "<me@example.com>" || msg.Headers.InReplyTo != "<lunch-1@example.com>" || msg.Text != "Yes, noon works.\r\nSee you." {
		t.Fatalf("draft: %+v", msg)
	}
	if len(fake.Sent()) != 0 {
		t.Fatal("a draft must not send")
	}
}

func TestDraftFindsSpecialFolder(t *testing.T) {
	fake, err := fakemail.Start(addr, pass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	mb := mail.New(config.Settings{Address: addr, Password: pass, IMAPHost: fake.IMAPAddr(), SMTPHost: fake.SMTPAddr()}, mail.WithTLS(fake.ClientTLS()))
	res, err := mb.Draft(ctx(t), mail.Compose{To: []string{"ann@example.com"}, Text: "hi"})
	if err != nil || res.Folder != "Drafts" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSend(t *testing.T) {
	fake, mb := setup(t, "Sent")
	res, err := mb.Send(ctx(t), mail.Compose{To: []string{"ann@example.com"}, Cc: []string{"cat@example.com"}, Bcc: []string{"dan@example.com"}, Subject: "Hi", Text: "Hello Ann"})
	if err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].From != addr || strings.Join(sent[0].To, ",") != "ann@example.com,cat@example.com,dan@example.com" {
		t.Fatalf("smtp: %+v", sent)
	}
	if strings.Contains(string(sent[0].Data), "dan@example.com") {
		t.Fatal("the wire message must not hold Bcc")
	}
	if !strings.Contains(string(sent[0].Data), "Hello Ann") || res.SentCopy != "Sent" {
		t.Fatalf("data %q, result %+v", sent[0].Data, res)
	}
	flags, raw := fetchOne(t, fake, "Sent")
	if !hasFlag(flags, imap.FlagSeen) || !strings.Contains(string(raw), "Subject: Hi") {
		t.Fatalf("sent copy: %v %q", flags, raw)
	}
}

func TestSendMIMEWithNoSentCopy(t *testing.T) {
	fake, mb := setup(t, "")
	raw := "From: me@example.com\r\nTo: ann@example.com\r\nSubject: Report\r\nMessage-ID: <rep-1@example.com>\r\n\r\nThe report.\r\n"
	res, err := mb.Send(ctx(t), mail.Compose{MIMEBase64: base64.StdEncoding.EncodeToString([]byte(raw))})
	if err != nil {
		t.Fatal(err)
	}
	if res.MessageID != "<rep-1@example.com>" || res.SentCopy != "" || len(fake.Sent()) != 1 {
		t.Fatalf("%+v", res)
	}
	if _, raw := fetchAll(t, fake, "Sent"); len(raw) != 0 {
		t.Fatal("gmail mode must not save a sent copy")
	}
	if _, err := mb.Send(ctx(t), mail.Compose{MIMEBase64: "x", To: []string{"a@b.c"}}); err == nil {
		t.Fatal("want an error for both forms")
	}
}

func TestBadPassword(t *testing.T) {
	fake, _ := setup(t, "")
	mb := mail.New(config.Settings{Address: addr, Password: "wrong", IMAPHost: fake.IMAPAddr(), SMTPHost: fake.SMTPAddr()}, mail.WithTLS(fake.ClientTLS()))
	if err := mb.Check(ctx(t)); !errors.Is(err, mail.ErrAuth) {
		t.Fatalf("imap: want ErrAuth, got %v", err)
	}
	if _, err := mb.Send(ctx(t), mail.Compose{To: []string{"ann@example.com"}, Text: "x"}); !errors.Is(err, mail.ErrAuth) {
		t.Fatalf("smtp: want ErrAuth, got %v", err)
	}
}

func TestHTMLText(t *testing.T) {
	got := mail.HTMLText("<ul><li>one</li><li>two</li></ul><p>a&amp;b</p>")
	if got != "- one\n\n- two\n\na&b" {
		t.Fatalf("%q", got)
	}
}

func fetchOne(t *testing.T, fake *fakemail.Server, folder string) ([]imap.Flag, []byte) {
	t.Helper()
	flags, raws := fetchAll(t, fake, folder)
	if len(raws) != 1 {
		t.Fatalf("%s has %d messages, want 1", folder, len(raws))
	}
	return flags[0], raws[0]
}

func fetchAll(t *testing.T, fake *fakemail.Server, folder string) ([][]imap.Flag, [][]byte) {
	t.Helper()
	c, err := imapclient.DialTLS(fake.IMAPAddr(), &imapclient.Options{TLSConfig: fake.ClientTLS()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(addr, pass).Wait(); err != nil {
		t.Fatal(err)
	}
	sel, err := c.Select(folder, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if sel.NumMessages == 0 {
		return nil, nil
	}
	sec := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.SeqSetNum(1, sel.NumMessages), &imap.FetchOptions{Flags: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var flags [][]imap.Flag
	var raws [][]byte
	for _, m := range msgs {
		flags = append(flags, m.Flags)
		raws = append(raws, m.FindBodySection(sec))
	}
	return flags, raws
}

func hasFlag(flags []imap.Flag, f imap.Flag) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}
