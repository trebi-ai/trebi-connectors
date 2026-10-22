package mail

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decodes non-UTF-8 parts
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
)

// maxTextPart caps the bytes of one text part that Read returns.
const maxTextPart = 1 << 20

// Headers are the main headers of a message.
type Headers struct {
	From       string   `json:"from"`
	ReplyTo    []string `json:"reply_to,omitempty"`
	To         []string `json:"to"`
	Cc         []string `json:"cc,omitempty"`
	Subject    string   `json:"subject"`
	Date       string   `json:"date,omitempty"`
	MessageID  string   `json:"message_id,omitempty"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References []string `json:"references,omitempty"`
}

// Attachment describes one attachment. Read never returns its bytes.
type Attachment struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Message is the output of the read operation.
type Message struct {
	ID          string       `json:"id"`
	Headers     Headers      `json:"headers"`
	Text        string       `json:"text"`
	HTML        string       `json:"html,omitempty"`
	Attachments []Attachment `json:"attachments"`
}

// Parse reads a full RFC 5322 message. Text is the plain part, or the HTML
// part as text.
func Parse(raw []byte) (Message, error) {
	msg, err := parse(raw)
	if err != nil && msg.Text == "" && msg.HTML == "" {
		return msg, fmt.Errorf("parse message: %w", err)
	}
	return msg, nil
}

// parse returns what it could read, also with an error.
func parse(raw []byte) (Message, error) {
	msg := Message{Attachments: []Attachment{}, Headers: Headers{To: []string{}}}
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return msg, err
	}
	defer r.Close() //nolint:errcheck // a read-only reader
	msg.Headers = readHeaders(r.Header)
	var plain, html string
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			msg.Text = textOf(plain, html)
			msg.HTML = html
			return msg, err
		}
		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			b, _ := io.ReadAll(io.LimitReader(p.Body, maxTextPart)) // a cut part still has text
			switch {
			case ct == "text/html" && html == "":
				html = string(b)
			case (ct == "text/plain" || ct == "") && plain == "":
				plain = string(b)
			}
		case *mail.AttachmentHeader:
			name, _ := h.Filename()
			ct, _, _ := h.ContentType()
			n, _ := io.Copy(io.Discard, p.Body)
			msg.Attachments = append(msg.Attachments, Attachment{Name: name, Type: ct, Size: n})
		}
	}
	msg.Text = textOf(plain, html)
	msg.HTML = html
	return msg, nil
}

func textOf(plain, html string) string {
	if strings.TrimSpace(plain) != "" {
		return strings.TrimSpace(plain)
	}
	if html != "" {
		return HTMLText(html)
	}
	return ""
}

func readHeaders(h mail.Header) Headers {
	out := Headers{To: addrs(h, "To"), Cc: addrs(h, "Cc")}
	if rt := addrs(h, "Reply-To"); len(rt) > 0 {
		out.ReplyTo = rt
	}
	if from := addrs(h, "From"); len(from) > 0 {
		out.From = from[0]
	}
	out.Subject, _ = h.Subject()
	if d, err := h.Date(); err == nil && !d.IsZero() {
		out.Date = d.UTC().Format(time.RFC3339)
	}
	out.MessageID = h.Get("Message-Id")
	out.InReplyTo = h.Get("In-Reply-To")
	if refs, err := h.MsgIDList("References"); err == nil && len(refs) > 0 {
		for _, r := range refs {
			out.References = append(out.References, "<"+r+">")
		}
	}
	return out
}

func addrs(h mail.Header, key string) []string {
	list, err := h.AddressList(key)
	out := []string{}
	if err != nil {
		if v := h.Get(key); v != "" {
			out = append(out, v)
		}
		return out
	}
	for _, a := range list {
		out = append(out, a.String())
	}
	return out
}

// snippet reads the start of a message and returns a short text.
func snippet(raw []byte, n int) string {
	msg, _ := parse(raw) // a partial body often ends inside a part
	s := strings.Join(strings.Fields(msg.Text), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// maxMessage caps a built message. Gmail refuses a message larger than
// 25 MB.
const maxMessage = 25 << 20

// Compose is the input of the draft and send operations: a full MIME
// message, or the fields of a message.
type Compose struct {
	MIMEBase64  string      `json:"mime_base64,omitempty"`
	To          []string    `json:"to,omitempty"`
	Cc          []string    `json:"cc,omitempty"`
	Bcc         []string    `json:"bcc,omitempty"`
	Subject     string      `json:"subject,omitempty"`
	Text        string      `json:"text,omitempty"`
	HTML        string      `json:"html,omitempty"`
	InReplyTo   string      `json:"in_reply_to,omitempty"`
	Attachments []NewAttach `json:"attachments,omitempty"`

	// references is the References chain that Reply sets.
	references []string
}

// NewAttach is one attachment of a new message: a file on this computer,
// or the content in base64.
type NewAttach struct {
	Path          string `json:"path,omitempty"`
	Name          string `json:"name,omitempty"`
	MIME          string `json:"mime,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

// Built is a message ready to save or send.
type Built struct {
	Raw        []byte // with Bcc, for Drafts and Sent
	Wire       []byte // without Bcc, for SMTP
	MessageID  string
	Recipients []string // To, Cc, and Bcc addresses
}

// Build makes the message. from is the account address; it is the From of
// a message with no From.
func (c Compose) Build(from string, now time.Time) (Built, error) {
	var raw []byte
	if c.MIMEBase64 != "" {
		if len(c.To)+len(c.Cc)+len(c.Bcc)+len(c.Attachments) > 0 || c.Subject != "" || c.Text != "" || c.HTML != "" || c.InReplyTo != "" {
			return Built{}, errors.New("give mime_base64 or the message fields, not both")
		}
		var err error
		if raw, err = base64.StdEncoding.DecodeString(strings.TrimSpace(c.MIMEBase64)); err != nil {
			return Built{}, fmt.Errorf("mime_base64: %w", err)
		}
	} else {
		var err error
		if raw, err = c.fields(); err != nil {
			return Built{}, err
		}
	}
	return finish(raw, from, now)
}

// finish adds the missing From, Date, and Message-ID to a message and
// makes the copy without Bcc.
func finish(raw []byte, from string, now time.Time) (Built, error) {
	if len(raw) > maxMessage {
		return Built{}, fmt.Errorf("the message is %d MB; the limit is 25 MB", len(raw)>>20)
	}
	br := bufio.NewReader(bytes.NewReader(raw))
	h, err := textproto.ReadHeader(br)
	if err != nil {
		return Built{}, fmt.Errorf("message header: %w", err)
	}
	body, err := io.ReadAll(br)
	if err != nil {
		return Built{}, err
	}
	mh := mail.Header{Header: message.Header{Header: h}}
	if !mh.Has("From") {
		mh.SetAddressList("From", []*mail.Address{{Address: from}})
	}
	if !mh.Has("Date") {
		mh.SetDate(now)
	}
	id := strings.Trim(mh.Get("Message-Id"), "<>")
	if id == "" {
		id = newMessageID(from)
		mh.SetMessageID(id)
	}
	var rcpts []string
	for _, k := range []string{"To", "Cc", "Bcc"} {
		list, err := mh.AddressList(k)
		if err != nil {
			return Built{}, fmt.Errorf("%s: %w", k, err)
		}
		for _, a := range list {
			rcpts = append(rcpts, a.Address)
		}
	}
	out := Built{MessageID: "<" + id + ">", Recipients: rcpts}
	if out.Raw, err = render(mh.Header.Header, body); err != nil {
		return Built{}, err
	}
	wire := mh.Header.Header.Copy()
	wire.Del("Bcc")
	if out.Wire, err = render(wire, body); err != nil {
		return Built{}, err
	}
	return out, nil
}

// fields builds a message from the fields. A message with HTML has a text
// part too; a message with attachments is multipart/mixed.
func (c Compose) fields() ([]byte, error) {
	var h mail.Header
	if len(c.To) == 0 {
		return nil, errors.New("to: give at least one address")
	}
	for _, k := range []string{"To", "Cc", "Bcc"} {
		list := map[string][]string{"To": c.To, "Cc": c.Cc, "Bcc": c.Bcc}[k]
		if len(list) == 0 {
			continue
		}
		var as []*mail.Address
		for _, s := range list {
			a, err := netmail.ParseAddress(s)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not an email address", strings.ToLower(k), s)
			}
			as = append(as, (*mail.Address)(a))
		}
		h.SetAddressList(k, as)
	}
	h.SetSubject(c.Subject)
	if r := strings.TrimSpace(c.InReplyTo); r != "" {
		id, err := msgID(r)
		if err != nil {
			return nil, fmt.Errorf("in_reply_to: %q is not a message id", c.InReplyTo)
		}
		h.Set("In-Reply-To", id)
		refs := c.references
		if len(refs) == 0 {
			refs = []string{id}
		}
		h.Set("References", strings.Join(refs, " "))
	}
	h.Set("MIME-Version", "1.0")
	files, err := c.decodeAttachments()
	if err != nil {
		return nil, err
	}
	text := c.Text
	if text == "" && c.HTML != "" {
		text = HTMLText(c.HTML)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var b bytes.Buffer
	if c.HTML == "" && len(files) == 0 {
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := mail.CreateSingleInlineWriter(&b, h)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, text); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	w, err := mail.CreateWriter(&b, h)
	if err != nil {
		return nil, err
	}
	if err := c.writeBody(w, text); err != nil {
		return nil, err
	}
	for _, f := range files {
		var ah mail.AttachmentHeader
		ah.SetContentType(f.mime, map[string]string{"name": f.name})
		ah.SetFilename(f.name)
		aw, err := w.CreateAttachment(ah)
		if err != nil {
			return nil, err
		}
		if _, err := aw.Write(f.data); err != nil {
			return nil, err
		}
		if err := aw.Close(); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeBody writes the text part, or the text and HTML parts as
// multipart/alternative.
func (c Compose) writeBody(w *mail.Writer, text string) error {
	textHeader := func() mail.InlineHeader {
		var ih mail.InlineHeader
		ih.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		return ih
	}
	if c.HTML == "" {
		tw, err := w.CreateSingleInline(textHeader())
		if err != nil {
			return err
		}
		if _, err := io.WriteString(tw, text); err != nil {
			return err
		}
		return tw.Close()
	}
	iw, err := w.CreateInline()
	if err != nil {
		return err
	}
	var hh mail.InlineHeader
	hh.SetContentType("text/html", map[string]string{"charset": "utf-8"})
	for _, p := range []struct {
		h    mail.InlineHeader
		body string
	}{{textHeader(), text}, {hh, c.HTML}} {
		pw, err := iw.CreatePart(p.h)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(pw, p.body); err != nil {
			return err
		}
		if err := pw.Close(); err != nil {
			return err
		}
	}
	return iw.Close()
}

type attachFile struct {
	name, mime string
	data       []byte
}

func (c Compose) decodeAttachments() ([]attachFile, error) {
	var out []attachFile
	for i, a := range c.Attachments {
		name := strings.TrimSpace(a.Name)
		if name == "" && a.Path != "" {
			name = filepath.Base(a.Path)
		}
		if name == "" || strings.ContainsAny(name, "\r\n") {
			return nil, fmt.Errorf("attachments[%d]: give a file name", i)
		}
		var data []byte
		var err error
		switch {
		case (a.Path == "") == (a.ContentBase64 == ""):
			return nil, fmt.Errorf("attachments[%d] %q: give path or content_base64", i, name)
		case a.Path != "":
			if data, err = readAttachFile(a.Path); err != nil {
				return nil, fmt.Errorf("attachments[%d]: %w", i, err)
			}
		default:
			if data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(a.ContentBase64)); err != nil {
				return nil, fmt.Errorf("attachments[%d] %q: content_base64 is not valid base64", i, name)
			}
		}
		typ := strings.TrimSpace(a.MIME)
		if typ == "" {
			typ = mime.TypeByExtension(path.Ext(name))
		}
		if mt, _, err := mime.ParseMediaType(typ); err != nil || !strings.Contains(mt, "/") {
			typ = "application/octet-stream"
		} else {
			typ = mt
		}
		out = append(out, attachFile{name: name, mime: typ, data: data})
	}
	return out, nil
}

// msgID checks one message id and puts it in angle brackets.
func msgID(s string) (string, error) {
	id := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "<"), ">")
	if id == "" || strings.ContainsAny(id, "<>\r\n\t ") {
		return "", fmt.Errorf("%q is not a message id", s)
	}
	return "<" + id + ">", nil
}

// render writes the header and the body. A header value with CR or LF
// is an error.
func render(h textproto.Header, body []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := textproto.WriteHeader(&b, h); err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	b.Write(body)
	return b.Bytes(), nil
}

func newMessageID(from string) string {
	domain := "mailbox.local"
	if i := strings.LastIndex(from, "@"); i >= 0 && i < len(from)-1 {
		domain = from[i+1:]
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b) + "@" + domain
}
