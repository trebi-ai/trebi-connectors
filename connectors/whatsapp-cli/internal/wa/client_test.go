package wa

import (
	"bytes"
	"path/filepath"
	"regexp"
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestParseUserOrJID(t *testing.T) {
	j, err := ParseUserOrJID("1234567890")
	if err != nil {
		t.Fatalf("ParseUserOrJID: %v", err)
	}
	if j.Server != types.DefaultUserServer || j.User != "1234567890" {
		t.Fatalf("unexpected jid: %+v", j)
	}

	j, err = ParseUserOrJID("123@g.us")
	if err != nil {
		t.Fatalf("ParseUserOrJID group: %v", err)
	}
	if !IsGroupJID(j) {
		t.Fatalf("expected group jid, got %+v", j)
	}
}

func TestBestContactName(t *testing.T) {
	if BestContactName(types.ContactInfo{Found: false, FullName: "x"}) != "" {
		t.Fatalf("expected empty for not found")
	}
	if BestContactName(types.ContactInfo{Found: true, FullName: "Full"}) != "Full" {
		t.Fatalf("expected full name")
	}
	if BestContactName(types.ContactInfo{Found: true, FirstName: "First"}) != "First" {
		t.Fatalf("expected first name")
	}
	if BestContactName(types.ContactInfo{Found: true, BusinessName: "Biz"}) != "Biz" {
		t.Fatalf("expected business name")
	}
	if BestContactName(types.ContactInfo{Found: true, PushName: "Push"}) != "Push" {
		t.Fatalf("expected push name")
	}
}

func TestRenewKeepsHandlers(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	var got []interface{}
	c.AddEventHandler(func(evt interface{}) { got = append(got, evt) })
	old := c.client
	old.Store.Deleted = true // what a logout leaves behind

	c.renewIfDeleted()
	if c.client == old || c.client.Store.Deleted {
		t.Fatal("a deleted device was not renewed")
	}
	c.client.DangerousInternals().DispatchEvent(&events.Connected{})
	if len(got) != 1 {
		t.Fatalf("handler got %d events after renew", len(got))
	}
}

func TestBaseLoggerTrebiMode(t *testing.T) {
	var buf bytes.Buffer
	l := newBaseLogger(&buf, true)
	l.Warn().Msg("x")
	if regexp.MustCompile(`\d{2}:\d{2}`).MatchString(buf.String()) || regexp.MustCompile(`\d{4}-\d{2}-\d{2}`).MatchString(buf.String()) {
		t.Fatalf("Trebi mode line has a time: %q", buf.String())
	}
	if want := "WRN x"; !regexp.MustCompile(want).MatchString(buf.String()) {
		t.Fatalf("line %q lacks %q", buf.String(), want)
	}

	buf.Reset()
	l = newBaseLogger(&buf, false)
	l.Warn().Msg("x")
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} `).MatchString(buf.String()) {
		t.Fatalf("standalone line has no time: %q", buf.String())
	}
}
