package mail_test

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
)

var now = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

func TestBuildTextOnly(t *testing.T) {
	b, err := mail.Compose{To: []string{"ann@example.com"}, Subject: "Hi", Text: "one\ntwo"}.Build(addr, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b.Raw), "Content-Type: text/plain; charset=utf-8") || strings.Contains(string(b.Raw), "multipart") {
		t.Fatalf("%s", b.Raw)
	}
	msg, _ := mail.Parse(b.Raw)
	if msg.Text != "one\r\ntwo" {
		t.Fatalf("text %q", msg.Text)
	}
}

func TestBuildHTMLMakesText(t *testing.T) {
	b, err := mail.Compose{To: []string{"ann@example.com"}, Subject: "Hi", HTML: "<p>Hello <b>Ann</b></p>"}.Build(addr, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b.Raw), "multipart/alternative") {
		t.Fatalf("%s", b.Raw)
	}
	msg, err := mail.Parse(b.Raw)
	if err != nil || msg.Text != "Hello Ann" || !strings.Contains(msg.HTML, "<b>Ann</b>") {
		t.Fatalf("%+v %v", msg, err)
	}
}

func TestBuildTextAndHTMLWithAttachments(t *testing.T) {
	c := mail.Compose{
		To: []string{"ann@example.com"}, Subject: "Report", Text: "See the file.", HTML: "<p>See the <i>file</i>.</p>",
		Attachments: []mail.NewAttach{
			{Name: "relatório ção.pdf", MIME: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4"))},
			{Name: "notes.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("a note"))},
		},
	}
	b, err := c.Build(addr, now)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b.Raw)
	if !strings.Contains(raw, "multipart/mixed") || !strings.Contains(raw, "multipart/alternative") {
		t.Fatalf("%s", raw)
	}
	msg, err := mail.Parse(b.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "See the file." || !strings.Contains(msg.HTML, "<i>file</i>") {
		t.Fatalf("%+v", msg)
	}
	if len(msg.Attachments) != 2 || msg.Attachments[0].Name != "relatório ção.pdf" || msg.Attachments[0].Type != "application/pdf" || msg.Attachments[0].Size != 8 {
		t.Fatalf("attachments %+v", msg.Attachments)
	}
	if msg.Attachments[1].Type != "text/plain" {
		t.Fatalf("type from the extension: %+v", msg.Attachments[1])
	}
}

func TestBuildBadAttachment(t *testing.T) {
	c := mail.Compose{To: []string{"ann@example.com"}, Text: "x", Attachments: []mail.NewAttach{{Name: "a.bin", ContentBase64: "not base64!"}}}
	if _, err := c.Build(addr, now); err == nil || !strings.Contains(err.Error(), "content_base64") {
		t.Fatalf("err %v", err)
	}
	c.Attachments = []mail.NewAttach{{Name: "", ContentBase64: "eA=="}}
	if _, err := c.Build(addr, now); err == nil {
		t.Fatal("want an error for no name")
	}
}

func TestSearchToBeforeAndPages(t *testing.T) {
	fake, mb := setup(t, "")
	for i := 1; i <= 45; i++ {
		to := "me@example.com"
		if i%5 == 0 {
			to = "team@example.com"
		}
		raw := fmt.Sprintf("From: x@example.com\r\nTo: %s\r\nSubject: n%d\r\nDate: Mon, 01 Sep 2025 10:00:00 +0000\r\nMessage-ID: <n%d@example.com>\r\n\r\nbody %d\r\n", to, i, i, i)
		if err := fake.Deliver("INBOX", []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := search(t, mb, mail.SearchQuery{To: "team@example.com", Limit: 100})
	if err != nil || len(got) != 9 {
		t.Fatalf("to: %d %v", len(got), err)
	}
	got, err = search(t, mb, mail.SearchQuery{Before: "2099-01-01", Limit: 100})
	if err != nil || len(got) != 47 {
		t.Fatalf("before: %d %v", len(got), err)
	}
	var seen []string
	q := mail.SearchQuery{From: "x@example.com", Before: "2099-01-01", Limit: 20}
	for page := 0; ; page++ {
		res, err := mb.Search(ctx(t), q)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range res.Messages {
			seen = append(seen, m.Subject)
		}
		if res.NextCursor == "" {
			if page != 2 {
				t.Fatalf("want 3 pages, got %d", page+1)
			}
			break
		}
		q.Cursor = res.NextCursor
	}
	if len(seen) != 45 || seen[0] != "n45" || seen[44] != "n1" || len(slices.Compact(slices.Clone(seen))) != 45 {
		t.Fatalf("pages: %v", seen)
	}
	if _, err := mb.Search(ctx(t), mail.SearchQuery{Cursor: q.Cursor, Folder: "Sent"}); err == nil {
		t.Fatal("want an error for a cursor of another folder")
	}
	if _, err := mb.Search(ctx(t), mail.SearchQuery{Cursor: "junk"}); err == nil {
		t.Fatal("want an error for a bad cursor")
	}
	if err := fake.ResetFolder("INBOX"); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.Search(ctx(t), mail.SearchQuery{Cursor: q.Cursor}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("want an error for an old UIDVALIDITY, got %v", err)
	}
}

func TestReplyHeaders(t *testing.T) {
	orig := mail.Headers{
		From: "Ann <ann@example.com>", ReplyTo: []string{"list@example.com"},
		To: []string{"me@example.com", "Bob <bob@example.com>"}, Cc: []string{"ME@example.com", "cat@example.com", "bob@example.com"},
		Subject: "RE: Lunch", MessageID: "<3@x>", References: []string{"<1@x>", "<2@x>"},
	}
	c, err := mail.ReplyHeaders(orig, "me@example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.To, ",") != "list@example.com" || c.Subject != "RE: Lunch" || c.InReplyTo != "<3@x>" || len(c.Cc) != 0 {
		t.Fatalf("%+v", c)
	}
	c, err = mail.ReplyHeaders(orig, "me@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.To, ",") != "list@example.com,Bob <bob@example.com>" || strings.Join(c.Cc, ",") != "cat@example.com" {
		t.Fatalf("all: to %v cc %v", c.To, c.Cc)
	}
	orig.ReplyTo, orig.Subject = nil, "Lunch"
	c, _ = mail.ReplyHeaders(orig, "me@example.com", false)
	if c.To[0] != "Ann <ann@example.com>" || c.Subject != "Re: Lunch" {
		t.Fatalf("%+v", c)
	}
	b, err := c.Build(addr, now)
	if err != nil || !strings.Contains(string(b.Raw), "References: <1@x> <2@x> <3@x>\r\n") {
		t.Fatalf("%v\n%s", err, b.Raw)
	}
	var long []string
	for i := range 400 {
		long = append(long, fmt.Sprintf("<%d-some-long-message-id@example.com>", i))
	}
	orig.References = long
	c, _ = mail.ReplyHeaders(orig, "me@example.com", false)
	b, err = c.Build(addr, now)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := mail.Parse(b.Raw)
	refs := msg.Headers.References
	if n := len(strings.Join(refs, " ")); n > 8<<10 || refs[len(refs)-1] != "<3@x>" || refs[0] == long[0] {
		t.Fatalf("cut: %d bytes, first %s, last %s", n, refs[0], refs[len(refs)-1])
	}
	if _, err := mail.ReplyHeaders(mail.Headers{From: "me@example.com"}, "me@example.com", false); err == nil {
		t.Fatal("want an error with no one to answer")
	}
}

func TestReplyDraftAndSend(t *testing.T) {
	fake, mb := setup(t, "")
	res, err := mb.Reply(ctx(t), mail.ReplyInput{ID: "INBOX:1", Text: "Yes, noon."}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent || res.DraftID != "Drafts:1" || strings.Join(res.Recipients, ",") != "ann@example.com" {
		t.Fatalf("%+v", res)
	}
	_, raw := fetchOne(t, fake, "Drafts")
	msg, _ := mail.Parse(raw)
	if msg.Headers.Subject != "Re: Lunch on Friday" || msg.Headers.InReplyTo != "<lunch-1@example.com>" || strings.Join(msg.Headers.References, " ") != "<lunch-1@example.com>" {
		t.Fatalf("draft %+v", msg.Headers)
	}
	if len(fake.Sent()) != 0 {
		t.Fatal("reply must not send")
	}
	res, err = mb.Reply(ctx(t), mail.ReplyInput{ID: "INBOX:1", Text: "Yes.", Bcc: []string{"boss@example.com"}}, true)
	if err != nil || !res.Sent {
		t.Fatalf("%+v %v", res, err)
	}
	sent := fake.Sent()
	if len(sent) != 1 || strings.Join(sent[0].To, ",") != "ann@example.com,boss@example.com" || strings.Contains(string(sent[0].Data), "boss@") {
		t.Fatalf("%+v", sent)
	}
	if _, err := mb.Reply(ctx(t), mail.ReplyInput{ID: "INBOX:1"}, false); err == nil {
		t.Fatal("want an error with no text")
	}
}

func TestSendDraftAndDeleteDraft(t *testing.T) {
	fake, mb := setup(t, "")
	d, err := mb.Draft(ctx(t), mail.Compose{To: []string{"ann@example.com"}, Bcc: []string{"dan@example.com"}, Subject: "Plan", Text: "The plan."})
	if err != nil {
		t.Fatal(err)
	}
	_, draftRaw := fetchOne(t, fake, "Drafts")
	if _, err := mb.SendDraft(ctx(t), "INBOX:1"); err == nil || !strings.Contains(err.Error(), "not a draft") {
		t.Fatalf("want not a draft, got %v", err)
	}
	res, err := mb.SendDraft(ctx(t), d.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent()
	if len(sent) != 1 || strings.Join(sent[0].To, ",") != "ann@example.com,dan@example.com" || res.MessageID != d.MessageID {
		t.Fatalf("%+v %+v", sent, res)
	}
	want := strings.Replace(string(draftRaw), "Bcc: <dan@example.com>\r\n", "", 1)
	if strings.TrimRight(string(sent[0].Data), "\r\n") != strings.TrimRight(want, "\r\n") {
		t.Fatalf("sent bytes:\n%q\nwant\n%q", sent[0].Data, want)
	}
	if _, raws := fetchAll(t, fake, "Drafts"); len(raws) != 0 {
		t.Fatal("the draft must be gone")
	}
	// A message in Drafts with no \Draft flag is not a draft.
	if err := fake.Deliver("Drafts", []byte(plainMsg)); err != nil {
		t.Fatal(err)
	}
	if err := mb.DeleteDraft(ctx(t), "Drafts:2"); err == nil || !strings.Contains(err.Error(), "not a draft") {
		t.Fatalf("want not a draft, got %v", err)
	}
	d, err = mb.Draft(ctx(t), mail.Compose{To: []string{"ann@example.com"}, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mb.DeleteDraft(ctx(t), d.DraftID); err != nil {
		t.Fatal(err)
	}
	if _, raws := fetchAll(t, fake, "Drafts"); len(raws) != 1 {
		t.Fatalf("want only the message with no flag, got %d", len(raws))
	}
}

func TestMark(t *testing.T) {
	fake, mb := setup(t, "")
	yes, no := true, false
	res, err := mb.Mark(ctx(t), mail.MarkInput{IDs: []string{"INBOX:1", "INBOX:2", "INBOX:9", "bad"}, Read: &yes, Starred: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Changed, ",") != "INBOX:1,INBOX:2" || len(res.Failed) != 2 {
		t.Fatalf("%+v", res)
	}
	flags, _ := fetchAll(t, fake, "INBOX")
	if !hasFlag(flags[0], imap.FlagSeen) || !hasFlag(flags[1], imap.FlagFlagged) {
		t.Fatalf("flags %v", flags)
	}
	if _, err := mb.Mark(ctx(t), mail.MarkInput{IDs: []string{"INBOX:1"}, Read: &no}); err != nil {
		t.Fatal(err)
	}
	flags, _ = fetchAll(t, fake, "INBOX")
	if hasFlag(flags[0], imap.FlagSeen) || !hasFlag(flags[0], imap.FlagFlagged) {
		t.Fatalf("flags %v", flags[0])
	}
	if _, err := mb.Mark(ctx(t), mail.MarkInput{IDs: []string{"INBOX:1"}}); err == nil {
		t.Fatal("want an error with no change")
	}
}

func TestTrashAndArchive(t *testing.T) {
	fake, mb := setup(t, "")
	res, err := mb.Trash(ctx(t), []string{"INBOX:1"})
	if err != nil || strings.Join(res.Changed, ",") != "INBOX:1" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, raw := fetchOne(t, fake, "Trash"); !strings.Contains(string(raw), "Lunch on Friday") {
		t.Fatal("not in trash")
	}
	res, err = mb.Archive(ctx(t), []string{"INBOX:2"})
	if err != nil || len(res.Changed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, raws := fetchAll(t, fake, "INBOX"); len(raws) != 0 {
		t.Fatal("inbox must be empty")
	}
	fetchOne(t, fake, "Archive")
	if err := fake.DeleteFolder("Archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.Archive(ctx(t), []string{"Trash:1"}); err == nil || !strings.Contains(err.Error(), "no archive folder") {
		t.Fatalf("want no archive folder, got %v", err)
	}
}

func TestLabelWithFolders(t *testing.T) {
	fake, mb := setup(t, "")
	if _, err := mb.Label(ctx(t), mail.LabelInput{IDs: []string{"INBOX:1"}, Add: []string{"Clients"}}); err == nil || !strings.Contains(err.Error(), "no label") {
		t.Fatalf("want no label, got %v", err)
	}
	res, err := mb.Label(ctx(t), mail.LabelInput{IDs: []string{"INBOX:1"}, Add: []string{"Clients"}, Create: true})
	if err != nil || len(res.Changed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	fetchOne(t, fake, "Clients")
	if _, raws := fetchAll(t, fake, "INBOX"); len(raws) != 2 {
		t.Fatal("label must copy, not move")
	}
	if _, err := mb.Label(ctx(t), mail.LabelInput{IDs: []string{"INBOX:1"}, Remove: []string{"Clients"}}); err == nil {
		t.Fatal("want an error for remove on a server with folders")
	}
}

func TestFolders(t *testing.T) {
	_, mb := setup(t, "")
	list, err := mb.Folders(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]mail.Folder{}
	for _, f := range list {
		got[f.Name] = f
	}
	if got["INBOX"].Role != "inbox" || got["INBOX"].Total != 2 || got["INBOX"].Unread != 2 {
		t.Fatalf("inbox %+v", got["INBOX"])
	}
	for name, role := range map[string]string{"Drafts": "drafts", "Sent": "sent", "Trash": "trash", "Archive": "archive", "Junk": "spam"} {
		if got[name].Role != role {
			t.Fatalf("%s: %+v", name, got[name])
		}
	}
}

func TestThreadOverInboxAndSent(t *testing.T) {
	fake, mb := setup(t, "Sent")
	reply := "From: me@example.com\r\nTo: ann@example.com\r\nSubject: Re: Lunch on Friday\r\nDate: Mon, 06 Oct 2026 11:00:00 +0000\r\nMessage-ID: <lunch-2@example.com>\r\nIn-Reply-To: <lunch-1@example.com>\r\nReferences: <lunch-1@example.com>\r\n\r\nYes, noon works.\r\n\r\nOn Mon, Oct 6, 2026 at 10:00 AM Ann <ann@example.com> wrote:\r\n> Can we meet at noon on Friday?\r\n"
	third := "From: Ann <ann@example.com>\r\nTo: me@example.com\r\nSubject: Re: Lunch on Friday\r\nDate: Mon, 06 Oct 2026 12:00:00 +0000\r\nMessage-ID: <lunch-3@example.com>\r\nIn-Reply-To: <lunch-2@example.com>\r\nReferences: <lunch-1@example.com> <lunch-2@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<div>Great, see you.</div><div class=\"gmail_quote\"><div>On Mon wrote:</div><blockquote>Yes, noon works.</blockquote></div>\r\n"
	if err := fake.Deliver("Sent", []byte(reply)); err != nil {
		t.Fatal(err)
	}
	if err := fake.Deliver("INBOX", []byte(third)); err != nil {
		t.Fatal(err)
	}
	res, err := mb.Thread(ctx(t), "INBOX:3", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ThreadID != "<lunch-1@example.com>" || len(res.Messages) != 3 {
		t.Fatalf("%+v", res)
	}
	var texts []string
	for _, m := range res.Messages {
		texts = append(texts, m.Text)
	}
	want := []string{"Can we meet at noon on Friday?", "Yes, noon works.", "Great, see you."}
	if !slices.Equal(texts, want) {
		t.Fatalf("texts %q", texts)
	}
	if res.Messages[1].ID != "Sent:1" {
		t.Fatalf("ids %s", res.Messages[1].ID)
	}
	res, err = mb.Thread(ctx(t), "INBOX:1", 2, false)
	if err != nil || len(res.Messages) != 2 || res.Messages[1].Text != "Great, see you." {
		t.Fatalf("limit: %+v %v", res, err)
	}
}

func TestStripQuotes(t *testing.T) {
	cases := map[string]string{
		"Yes.\n\nOn Mon, Oct 6, 2026 at 10:00 AM Ann <ann@example.com> wrote:\n> Can we?\n> Sure?": "Yes.",
		"Yes.\nOn Mon, Oct 6, 2026 at 10:00 AM Ann Long Name\n<ann@example.com> wrote:\n\n> Can we?":  "Yes.",
		"No quote here.\nTwo lines.": "No quote here.\nTwo lines.",
		"> only a quote":             "> only a quote",
		"Top\n> mid quote\nBottom":    "Top\nBottom",
	}
	for in, want := range cases {
		if got := mail.StripQuotes(in); got != want {
			t.Errorf("StripQuotes(%q) = %q, want %q", in, got, want)
		}
	}
	h := `<div>New<br>text</div><div class="gmail_quote gmail_quote_container"><div dir="ltr">On Mon wrote:<br></div><blockquote class="gmail_quote"><div>old</div></blockquote></div><p>tail</p>`
	if got := mail.HTMLText(mail.StripHTMLQuotes(h)); got != "New\ntext\n\ntail" {
		t.Errorf("html: %q", got)
	}
	if got := mail.StripHTMLQuotes("<p>plain</p>"); got != "<p>plain</p>" {
		t.Errorf("no quote: %q", got)
	}
}
