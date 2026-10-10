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
	"mime/quotedprintable"
	netmail "net/mail"
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
	defer r.Close()
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
func snippet(raw []byte) string {
	msg, _ := parse(raw) // a partial body often ends inside a part
	s := strings.Join(strings.Fields(msg.Text), " ")
	if utf8.RuneCountInString(s) <= snippetLen {
		return s
	}
	r := []rune(s)
	return string(r[:snippetLen]) + "…"
}

// Compose is the input of the draft and send operations: a full MIME
// message, or the fields of a plain text message.
type Compose struct {
	MIMEBase64 string   `json:"mime_base64,omitempty"`
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	Bcc        []string `json:"bcc,omitempty"`
	Subject    string   `json:"subject,omitempty"`
	Text       string   `json:"text,omitempty"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
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
	var h textproto.Header
	var body []byte
	if c.MIMEBase64 != "" {
		if len(c.To)+len(c.Cc)+len(c.Bcc) > 0 || c.Subject != "" || c.Text != "" || c.InReplyTo != "" {
			return Built{}, errors.New("give mime_base64 or the message fields, not both")
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.MIMEBase64))
		if err != nil {
			return Built{}, fmt.Errorf("mime_base64: %w", err)
		}
		br := bufio.NewReader(bytes.NewReader(raw))
		if h, err = textproto.ReadHeader(br); err != nil {
			return Built{}, fmt.Errorf("mime_base64: %w", err)
		}
		if body, err = io.ReadAll(br); err != nil {
			return Built{}, err
		}
	} else {
		var err error
		if h, body, err = c.plain(); err != nil {
			return Built{}, err
		}
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
	out.Raw = render(mh.Header.Header, body)
	wire := mh.Header.Header.Copy()
	wire.Del("Bcc")
	out.Wire = render(wire, body)
	return out, nil
}

// plain builds the header and body of a text message from the fields.
func (c Compose) plain() (textproto.Header, []byte, error) {
	var h mail.Header
	if len(c.To) == 0 {
		return h.Header.Header, nil, errors.New("to: give at least one address")
	}
	for k, list := range map[string][]string{"To": c.To, "Cc": c.Cc, "Bcc": c.Bcc} {
		if len(list) == 0 {
			continue
		}
		var as []*mail.Address
		for _, s := range list {
			a, err := netmail.ParseAddress(s)
			if err != nil {
				return h.Header.Header, nil, fmt.Errorf("%s: %q is not an email address", strings.ToLower(k), s)
			}
			as = append(as, (*mail.Address)(a))
		}
		h.SetAddressList(k, as)
	}
	h.SetSubject(c.Subject)
	if r := strings.TrimSpace(c.InReplyTo); r != "" {
		if !strings.HasPrefix(r, "<") {
			r = "<" + r + ">"
		}
		h.Set("In-Reply-To", r)
		h.Set("References", r)
	}
	h.Set("MIME-Version", "1.0")
	h.Set("Content-Type", mime.FormatMediaType("text/plain", map[string]string{"charset": "utf-8"}))
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	var body bytes.Buffer
	w := quotedprintable.NewWriter(&body)
	text := strings.ReplaceAll(strings.ReplaceAll(c.Text, "\r\n", "\n"), "\n", "\r\n")
	if _, err := io.WriteString(w, text); err != nil {
		return h.Header.Header, nil, err
	}
	if err := w.Close(); err != nil {
		return h.Header.Header, nil, err
	}
	return h.Header.Header, body.Bytes(), nil
}

func render(h textproto.Header, body []byte) []byte {
	var b bytes.Buffer
	_ = textproto.WriteHeader(&b, h) // a bytes.Buffer write does not fail
	b.Write(body)
	return b.Bytes()
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
