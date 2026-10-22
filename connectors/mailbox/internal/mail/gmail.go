package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

// capGmail is the capability of the Gmail IMAP extensions.
const capGmail = imap.Cap("X-GM-EXT-1")

// gmailTimeout is the time limit of one command on the Gmail connection.
const gmailTimeout = time.Minute

// gmailConn is a second IMAP connection for the Gmail extensions
// (X-GM-MSGID, X-GM-THRID, X-GM-LABELS, X-GM-RAW). go-imap v2 can neither
// send nor parse them, so this connection speaks the few commands that
// need them and nothing else.
type gmailConn struct {
	c        net.Conn
	r        *bufio.Reader
	tag      int
	selected string
	writable bool
	stop     func() bool
}

// gmailMeta is the Gmail data of one message.
type gmailMeta struct {
	MsgID  uint64
	ThrID  uint64
	Labels []string
}

// GmailID is the id of the message in this program: "gm:<X-GM-MSGID>".
func (g gmailMeta) GmailID() string { return GmailPrefix + strconv.FormatUint(g.MsgID, 10) }

// ThreadID is the thread id in this program: the X-GM-THRID in decimal.
func (g gmailMeta) ThreadID() string { return strconv.FormatUint(g.ThrID, 10) }

// dialGmail opens and logs in the Gmail connection. The caller closes it.
func (m *Mailbox) dialGmail(ctx context.Context) (*gmailConn, error) {
	addr := m.cfg.IMAPHost
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: m.tlsFor(addr)}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	g := &gmailConn{c: c, r: bufio.NewReader(c)}
	g.stop = context.AfterFunc(ctx, func() { _ = c.Close() }) // ends a hung command
	_ = c.SetDeadline(time.Now().Add(gmailTimeout))           //nolint:errcheck // a deadline error shows on the read
	if _, err := g.read(); err != nil {
		g.close()
		return nil, fmt.Errorf("greeting of %s: %w", addr, err)
	}
	if _, err := g.cmd("LOGIN", astr(m.cfg.Address), astr(m.cfg.Password)); err != nil {
		g.close()
		var no *gmailNo
		if errors.As(err, &no) {
			return nil, fmt.Errorf("%w: %s", ErrAuth, no.text)
		}
		return nil, fmt.Errorf("log in to %s: %w", addr, err)
	}
	return g, nil
}

func (g *gmailConn) close() {
	if g == nil {
		return
	}
	_ = g.c.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck // the connection closes next
	_, _ = g.cmd("LOGOUT")                               //nolint:errcheck // the session ends either way
	g.stop()
	_ = g.c.Close()
}

// selectFolder opens a folder for the next commands.
func (g *gmailConn) selectFolder(folder string, write bool) error {
	if g.selected == folder && (g.writable || !write) {
		return nil
	}
	verb := "EXAMINE"
	if write {
		verb = "SELECT"
	}
	g.selected = ""
	if _, err := g.cmd(verb, astr(encodeUTF7(folder))); err != nil {
		return fmt.Errorf("open folder %q: %w", folder, err)
	}
	g.selected, g.writable = folder, write
	return nil
}

// search runs UID SEARCH with the Gmail criteria and returns the UIDs in
// order.
func (g *gmailConn) search(key string, value any) ([]imap.UID, error) {
	args := []any{"UID", "SEARCH"}
	if s, ok := value.(astr); ok && !isASCII(string(s)) {
		args = append(args, "CHARSET", "UTF-8")
	}
	args = append(args, key, value)
	resps, err := g.cmd(args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	var uids []imap.UID
	for _, r := range resps {
		if r.kind != "SEARCH" {
			continue
		}
		for _, v := range r.fields {
			s, _ := v.(string)
			if n, err := strconv.ParseUint(s, 10, 32); err == nil {
				uids = append(uids, imap.UID(n))
			}
		}
	}
	slices.Sort(uids)
	return slices.Compact(uids), nil
}

// searchRaw finds messages with the Gmail search syntax.
func (g *gmailConn) searchRaw(query string) ([]imap.UID, error) {
	return g.search("X-GM-RAW", astr(query))
}

// findMsgID returns the UID of a Gmail message id in the selected folder,
// or 0.
func (g *gmailConn) findMsgID(id uint64) (imap.UID, error) {
	uids, err := g.search("X-GM-MSGID", strconv.FormatUint(id, 10))
	if err != nil || len(uids) == 0 {
		return 0, err
	}
	return uids[0], nil
}

// threadUIDs returns the UIDs of a Gmail thread in the selected folder.
func (g *gmailConn) threadUIDs(thrid uint64) ([]imap.UID, error) {
	return g.search("X-GM-THRID", strconv.FormatUint(thrid, 10))
}

// meta fetches the Gmail data of messages in the selected folder.
func (g *gmailConn) meta(uids []imap.UID) (map[imap.UID]gmailMeta, error) {
	out := map[imap.UID]gmailMeta{}
	if len(uids) == 0 {
		return out, nil
	}
	resps, err := g.cmd("UID", "FETCH", imap.UIDSetNum(uids...).String(), "(UID X-GM-MSGID X-GM-THRID X-GM-LABELS)")
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	for _, r := range resps {
		if r.kind != "FETCH" || len(r.fields) == 0 {
			continue
		}
		list, ok := r.fields[0].([]any)
		if !ok {
			continue
		}
		uid, m, err := parseGmailFetch(list)
		if err != nil {
			return nil, err
		}
		if uid != 0 && slices.Contains(uids, uid) {
			out[uid] = m
		}
	}
	return out, nil
}

// storeLabels adds (add true) or removes labels of messages in the
// selected folder. The folder must be open for write.
func (g *gmailConn) storeLabels(uids []imap.UID, add bool, labels []string) error {
	op := "-X-GM-LABELS.SILENT"
	if add {
		op = "+X-GM-LABELS.SILENT"
	}
	vals := make([]any, len(labels))
	for i, l := range labels {
		vals[i] = astr(encodeUTF7(l))
		if _, system := systemLabels[l]; system {
			vals[i] = l // a system label is an atom
		}
	}
	if _, err := g.cmd("UID", "STORE", imap.UIDSetNum(uids...).String(), op, vals); err != nil {
		return fmt.Errorf("change labels: %w", err)
	}
	return nil
}

// astr is a value that goes as a quoted string, or as a literal when it
// has bytes that a quoted string cannot hold.
type astr string

// gmailNo is a NO or BAD answer of the server.
type gmailNo struct{ status, text string }

func (e *gmailNo) Error() string { return strings.TrimSpace(e.status + " " + e.text) }

// gmailResp is one untagged response: "* SEARCH 1 2", "* 3 FETCH (...)".
type gmailResp struct {
	kind   string
	num    uint32
	fields []any // string, nil (NIL), or []any (a list)
}

// cmd sends one command and returns the untagged responses. A string arg
// goes as it is, an astr as a string, and a []any as a list.
func (g *gmailConn) cmd(args ...any) ([]gmailResp, error) {
	g.tag++
	tag := "g" + strconv.Itoa(g.tag)
	_ = g.c.SetDeadline(time.Now().Add(gmailTimeout)) //nolint:errcheck // a deadline error shows on the read
	var resps []gmailResp
	var b bytes.Buffer
	b.WriteString(tag)
	var write func(a any) error
	write = func(a any) error {
		switch v := a.(type) {
		case string:
			b.WriteString(v)
		case astr:
			if s := string(v); quotable(s) {
				b.WriteString(strconv.Quote(s)) // only " and \ need a backslash here
			} else {
				fmt.Fprintf(&b, "{%d}\r\n", len(s))
				if _, err := g.c.Write(b.Bytes()); err != nil {
					return err
				}
				b.Reset()
				for {
					r, err := g.read()
					if err != nil {
						return err
					}
					if r.cont {
						break
					}
					if r.tag == tag {
						return &gmailNo{r.status, r.text}
					}
					resps = append(resps, r.resp)
				}
				b.WriteString(s)
			}
		case []any:
			b.WriteByte('(')
			for i, e := range v {
				if i > 0 {
					b.WriteByte(' ')
				}
				if err := write(e); err != nil {
					return err
				}
			}
			b.WriteByte(')')
		}
		return nil
	}
	for _, a := range args {
		b.WriteByte(' ')
		if err := write(a); err != nil {
			return nil, err
		}
	}
	b.WriteString("\r\n")
	if _, err := g.c.Write(b.Bytes()); err != nil {
		return nil, err
	}
	for {
		r, err := g.read()
		if err != nil {
			return nil, err
		}
		if r.tag == "*" {
			resps = append(resps, r.resp)
			continue
		}
		if r.tag != tag {
			continue
		}
		if r.status != "OK" {
			return resps, &gmailNo{r.status, r.text}
		}
		return resps, nil
	}
}

// gmailLine is one line from the server.
type gmailLine struct {
	tag    string // "*", a command tag, or "" for a continuation
	cont   bool
	status string
	text   string
	resp   gmailResp
}

// read reads one response.
func (g *gmailConn) read() (gmailLine, error) { return readGmailLine(g.r) }

func readGmailLine(r *bufio.Reader) (gmailLine, error) {
	var l gmailLine
	p := &imapParser{r: r}
	first, err := p.atom()
	if err != nil {
		return l, err
	}
	if first == "+" {
		l.cont = true
		l.text, err = p.rest()
		return l, err
	}
	l.tag = first
	word, err := p.atom()
	if err != nil {
		return l, err
	}
	if l.tag != "*" {
		l.status = strings.ToUpper(word)
		l.text, err = p.rest()
		return l, err
	}
	if n, err := strconv.ParseUint(word, 10, 32); err == nil {
		l.resp.num = uint32(n)
		if word, err = p.atom(); err != nil {
			return l, err
		}
	}
	l.resp.kind = strings.ToUpper(word)
	switch l.resp.kind {
	case "OK", "NO", "BAD", "BYE", "PREAUTH":
		l.status = l.resp.kind
		l.text, err = p.rest()
		return l, err
	}
	l.resp.fields, err = p.values()
	return l, err
}

// imapParser reads the values of one response line, literals included.
type imapParser struct{ r *bufio.Reader }

func (p *imapParser) skipSpaces() error {
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return err
		}
		if c != ' ' {
			return p.r.UnreadByte()
		}
	}
}

// atom reads a word up to a space or the end of the line.
func (p *imapParser) atom() (string, error) {
	if err := p.skipSpaces(); err != nil {
		return "", err
	}
	var b strings.Builder
	depth := 0
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return "", err
		}
		switch {
		case c == '[':
			depth++
		case c == ']' && depth > 0:
			depth--
		case depth == 0 && (c == ' ' || c == '(' || c == ')' || c == '\r' || c == '\n'):
			if err := p.r.UnreadByte(); err != nil {
				return "", err
			}
			return b.String(), nil
		}
		b.WriteByte(c)
	}
}

// rest reads the rest of the line as text.
func (p *imapParser) rest() (string, error) {
	s, err := p.r.ReadString('\n')
	return strings.TrimSpace(s), err
}

// values reads values to the end of the line.
func (p *imapParser) values() ([]any, error) {
	var out []any
	for {
		if err := p.skipSpaces(); err != nil {
			return nil, err
		}
		c, err := p.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if c == '\r' || c == '\n' {
			if c == '\r' {
				if _, err := p.r.ReadByte(); err != nil {
					return nil, err
				}
			}
			return out, nil
		}
		if err := p.r.UnreadByte(); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

// value reads one value: a list, a quoted string, a literal, NIL, or an
// atom.
func (p *imapParser) value() (any, error) {
	if err := p.skipSpaces(); err != nil {
		return nil, err
	}
	c, err := p.r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch c {
	case '(':
		list := []any{}
		for {
			if err := p.skipSpaces(); err != nil {
				return nil, err
			}
			n, err := p.r.ReadByte()
			if err != nil {
				return nil, err
			}
			if n == ')' {
				return list, nil
			}
			if n == '\r' || n == '\n' {
				return nil, errors.New("a list has no end")
			}
			if err := p.r.UnreadByte(); err != nil {
				return nil, err
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case '"':
		var b strings.Builder
		for {
			n, err := p.r.ReadByte()
			if err != nil {
				return nil, err
			}
			switch n {
			case '"':
				return b.String(), nil
			case '\\':
				if n, err = p.r.ReadByte(); err != nil {
					return nil, err
				}
			case '\r', '\n':
				return nil, errors.New("a quoted string has no end")
			}
			b.WriteByte(n)
		}
	case '{':
		num, err := p.r.ReadString('}')
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(num, "}"), "+"))
		if err != nil || n < 0 || n > maxMessage {
			return nil, fmt.Errorf("bad literal size %q", num)
		}
		if crlf, err := p.r.ReadString('\n'); err != nil || strings.TrimSpace(crlf) != "" {
			return nil, errors.New("bad literal")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(p.r, buf); err != nil {
			return nil, err
		}
		return string(buf), nil
	}
	if err := p.r.UnreadByte(); err != nil {
		return nil, err
	}
	a, err := p.atom()
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(a, "NIL") {
		return nil, nil
	}
	return a, nil
}

// parseGmailFetch reads the items of one FETCH response.
func parseGmailFetch(items []any) (imap.UID, gmailMeta, error) {
	var uid imap.UID
	m := gmailMeta{Labels: []string{}}
	for i := 0; i+1 < len(items); i += 2 {
		key, _ := items[i].(string)
		val := items[i+1]
		num := func() (uint64, error) {
			s, _ := val.(string)
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("fetch %s: %q is not a number", key, s)
			}
			return n, nil
		}
		var err error
		switch strings.ToUpper(key) {
		case "UID":
			var n uint64
			n, err = num()
			uid = imap.UID(n)
		case "X-GM-MSGID":
			m.MsgID, err = num()
		case "X-GM-THRID":
			m.ThrID, err = num()
		case "X-GM-LABELS":
			list, ok := val.([]any)
			if !ok && val != nil {
				return 0, m, errors.New("fetch X-GM-LABELS: not a list")
			}
			for _, l := range list {
				if s, ok := l.(string); ok {
					m.Labels = append(m.Labels, labelName(decodeUTF7(s)))
				}
			}
		}
		if err != nil {
			return 0, m, err
		}
	}
	return uid, m, nil
}

// systemLabels maps the Gmail system labels to the names that the user
// sees in Gmail.
var systemLabels = map[string]string{
	`\Inbox`: "Inbox", `\Sent`: "Sent", `\Draft`: "Drafts", `\Starred`: "Starred",
	`\Important`: "Important", `\Trash`: "Trash", `\Spam`: "Spam",
}

// labelName is the name of a label for the user.
func labelName(l string) string {
	if n, ok := systemLabels[l]; ok {
		return n
	}
	return l
}

// gmailLabel is a label name of the user for STORE: "Inbox" becomes
// \Inbox. ok is false for a user label.
func gmailLabel(name string) (string, bool) {
	for raw, n := range systemLabels {
		if strings.EqualFold(name, n) || strings.EqualFold(name, raw) || (raw == `\Inbox` && strings.EqualFold(name, "INBOX")) {
			return raw, true
		}
	}
	return name, false
}

// parseGmailID reads "gm:<number>".
func parseGmailID(id string) (uint64, bool) {
	s, ok := strings.CutPrefix(id, GmailPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil && n > 0
}

// quotable reports whether s can go as an IMAP quoted string.
func quotable(s string) bool {
	if len(s) > 1000 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7e {
			return false
		}
	}
	return true
}

// utf7 is the base64 alphabet of modified UTF-7 (RFC 3501 §5.1.3).
var utf7 = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,").WithPadding(base64.NoPadding)

// encodeUTF7 encodes a folder or label name in modified UTF-7.
func encodeUTF7(s string) string {
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		u := utf16.Encode(run)
		buf := make([]byte, 2*len(u))
		for i, c := range u {
			buf[2*i], buf[2*i+1] = byte(c>>8), byte(c)
		}
		b.WriteByte('&')
		b.WriteString(utf7.EncodeToString(buf))
		b.WriteByte('-')
		run = run[:0]
	}
	for _, r := range s {
		if r >= 0x20 && r <= 0x7e {
			flush()
			if r == '&' {
				b.WriteString("&-")
			} else {
				b.WriteRune(r)
			}
			continue
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}

// decodeUTF7 decodes modified UTF-7. A bad sequence stays as it is.
func decodeUTF7(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '&')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+1:]
		j := strings.IndexByte(s, '-')
		if j < 0 {
			b.WriteString("&" + s)
			return b.String()
		}
		enc := s[:j]
		s = s[j+1:]
		if enc == "" {
			b.WriteByte('&')
			continue
		}
		buf, err := utf7.DecodeString(enc)
		if err != nil || len(buf)%2 != 0 {
			b.WriteString("&" + enc + "-")
			continue
		}
		u := make([]uint16, len(buf)/2)
		for k := range u {
			u[k] = uint16(buf[2*k])<<8 | uint16(buf[2*k+1])
		}
		r := utf16.Decode(u)
		if !utf8.ValidString(string(r)) {
			b.WriteString("&" + enc + "-")
			continue
		}
		b.WriteString(string(r))
	}
}
