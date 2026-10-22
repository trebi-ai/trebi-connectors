package mail

import (
	"bufio"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestUTF7(t *testing.T) {
	for plain, enc := range map[string]string{
		"Work":           "Work",
		"A&B":            "A&-B",
		"Entrée":         "Entr&AOk-e",
		"日本語":            "&ZeVnLIqe-",
		"[Gmail]/Lixo":   "[Gmail]/Lixo",
		"Ação & Volta":   "A&AOcA4w-o &- Volta",
		"~peter/mail/台北": "~peter/mail/&U,BTFw-",
	} {
		if got := encodeUTF7(plain); got != enc {
			t.Errorf("encode %q = %q, want %q", plain, got, enc)
		}
		if got := decodeUTF7(enc); got != plain {
			t.Errorf("decode %q = %q, want %q", enc, got, plain)
		}
	}
	if got := decodeUTF7("bad&***-x"); got != "bad&***-x" {
		t.Errorf("bad sequence = %q", got)
	}
}

func TestParseGmailID(t *testing.T) {
	if n, ok := parseGmailID("gm:1278455344230334865"); !ok || n != 1278455344230334865 {
		t.Fatalf("gm id = %d %v", n, ok)
	}
	for _, id := range []string{"gm:", "gm:0", "gm:x", "INBOX:4", "gm:-1"} {
		if _, ok := parseGmailID(id); ok {
			t.Errorf("%q parsed", id)
		}
	}
	if _, err := ParseRef("gm:5"); err == nil {
		t.Error("ParseRef took a Gmail id")
	}
}

func TestGmailLabel(t *testing.T) {
	for in, want := range map[string]string{"Inbox": `\Inbox`, "INBOX": `\Inbox`, "starred": `\Starred`, `\Important`: `\Important`} {
		if got, ok := gmailLabel(in); !ok || got != want {
			t.Errorf("%q = %q %v", in, got, ok)
		}
	}
	if got, ok := gmailLabel("Work/2026"); ok || got != "Work/2026" {
		t.Errorf("user label = %q %v", got, ok)
	}
}

func TestReadGmailLine(t *testing.T) {
	in := "* 12 FETCH (X-GM-THRID 1266894439832287888 X-GM-MSGID 1266894439832287889 X-GM-LABELS (\\Inbox \\Important \"Work Stuff\" &ZeVnLIqe- {4}\r\nA\"B) NIL) UID 42)\r\n" +
		"* SEARCH 3 9 4\r\n" +
		"* OK [READ-ONLY] done\r\n" +
		"+ go ahead\r\n" +
		"g1 NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)\r\n"
	r := bufio.NewReader(strings.NewReader(in))
	l, err := readGmailLine(r)
	if err != nil {
		t.Fatal(err)
	}
	if l.tag != "*" || l.resp.kind != "FETCH" || l.resp.num != 12 {
		t.Fatalf("fetch line = %+v", l)
	}
	uid, m, err := parseGmailFetch(l.resp.fields[0].([]any))
	if err != nil {
		t.Fatal(err)
	}
	want := gmailMeta{MsgID: 1266894439832287889, ThrID: 1266894439832287888, Labels: []string{"Inbox", "Important", "Work Stuff", "日本語", `A"B)`}}
	if uid != 42 || !reflect.DeepEqual(m, want) {
		t.Fatalf("fetch = %d %+v", uid, m)
	}
	if m.GmailID() != "gm:1266894439832287889" || m.ThreadID() != "1266894439832287888" {
		t.Fatalf("ids = %s %s", m.GmailID(), m.ThreadID())
	}
	l, _ = readGmailLine(r)
	if l.resp.kind != "SEARCH" || !reflect.DeepEqual(l.resp.fields, []any{"3", "9", "4"}) {
		t.Fatalf("search line = %+v", l)
	}
	l, _ = readGmailLine(r)
	if l.status != "OK" || l.text != "[READ-ONLY] done" {
		t.Fatalf("status line = %+v", l)
	}
	l, _ = readGmailLine(r)
	if !l.cont {
		t.Fatalf("continuation = %+v", l)
	}
	l, _ = readGmailLine(r)
	if l.tag != "g1" || l.status != "NO" || !strings.Contains(l.text, "Invalid credentials") {
		t.Fatalf("tagged line = %+v", l)
	}
}

// gmailScript runs a fake server: for each expected command line, it
// writes the answer. "TAG" in an answer is the tag of the command.
func gmailScript(t *testing.T, steps [][2]string) *gmailConn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		r := bufio.NewReader(server)
		tag := ""
		for _, st := range steps {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			rest := line
			if line != st[0] { // a literal goes on a line with no tag
				tag, rest, _ = strings.Cut(line, " ")
			}
			if rest != st[0] {
				_, _ = server.Write([]byte(tag + " BAD unexpected: " + rest + "\r\n"))
				continue
			}
			_, _ = server.Write([]byte(strings.ReplaceAll(st[1], "TAG", tag)))
		}
	}()
	return &gmailConn{c: client, r: bufio.NewReader(client), stop: func() bool { return true }}
}

func TestGmailConversation(t *testing.T) {
	g := gmailScript(t, [][2]string{
		{`EXAMINE "[Gmail]/All Mail"`, "* 40 EXISTS\r\nTAG OK [READ-ONLY] EXAMINE done\r\n"},
		{`UID SEARCH X-GM-RAW "from:ann has:attachment"`, "* SEARCH 7 3\r\nTAG OK done\r\n"},
		{`UID SEARCH CHARSET UTF-8 X-GM-RAW {14}`, "+ go\r\n"},
		{`assunto:ação`, "* SEARCH 5\r\nTAG OK done\r\n"},
		{`UID SEARCH X-GM-MSGID 99`, "* SEARCH\r\nTAG OK done\r\n"},
		{`UID FETCH 3,7 (UID X-GM-MSGID X-GM-THRID X-GM-LABELS)`, "* 1 FETCH (UID 3 X-GM-MSGID 11 X-GM-THRID 10 X-GM-LABELS ())\r\n* 2 FETCH (X-GM-LABELS (\\Starred) UID 7 X-GM-MSGID 12 X-GM-THRID 10)\r\nTAG OK done\r\n"},
		{`SELECT "INBOX"`, "TAG OK [READ-WRITE] done\r\n"},
		{`UID STORE 3 -X-GM-LABELS.SILENT (\Inbox "My Label")`, "TAG OK done\r\n"},
		{`UID SEARCH X-GM-THRID 10`, "TAG NO [CANNOT] nope\r\n"},
	})
	if err := g.selectFolder("[Gmail]/All Mail", false); err != nil {
		t.Fatal(err)
	}
	if err := g.selectFolder("[Gmail]/All Mail", false); err != nil { // no second EXAMINE
		t.Fatal(err)
	}
	uids, err := g.searchRaw("from:ann has:attachment")
	if err != nil || !reflect.DeepEqual(uids, []imap.UID{3, 7}) {
		t.Fatalf("search = %v %v", uids, err)
	}
	if uids, err = g.searchRaw("assunto:ação"); err != nil || !reflect.DeepEqual(uids, []imap.UID{5}) {
		t.Fatalf("literal search = %v %v", uids, err)
	}
	if uid, err := g.findMsgID(99); err != nil || uid != 0 {
		t.Fatalf("find = %d %v", uid, err)
	}
	meta, err := g.meta([]imap.UID{3, 7})
	if err != nil {
		t.Fatal(err)
	}
	if meta[3].MsgID != 11 || meta[7].MsgID != 12 || meta[7].ThrID != 10 || !reflect.DeepEqual(meta[7].Labels, []string{"Starred"}) || len(meta[3].Labels) != 0 {
		t.Fatalf("meta = %+v", meta)
	}
	if err := g.selectFolder("INBOX", true); err != nil {
		t.Fatal(err)
	}
	if err := g.storeLabels([]imap.UID{3}, false, []string{`\Inbox`, "My Label"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.threadUIDs(10); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("NO answer = %v", err)
	}
}
