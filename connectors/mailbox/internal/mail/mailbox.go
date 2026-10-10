// Package mail reads a mailbox over IMAP and sends over SMTP.
package mail

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
)

// ErrAuth is a login that the server refused.
var ErrAuth = errors.New("the server refused the address or the app password")

// Search limits.
const (
	DefaultLimit = 20
	MaxLimit     = 100
	snippetLen   = 200
	snippetBytes = 8192
	dialTimeout  = 20 * time.Second
)

// Mailbox is one account.
type Mailbox struct {
	cfg config.Settings
	tls *tls.Config
	now func() time.Time
}

// Option changes a Mailbox.
type Option func(*Mailbox)

// WithTLS replaces the TLS config of both servers (the sandbox and tests).
func WithTLS(c *tls.Config) Option { return func(m *Mailbox) { m.tls = c } }

// New returns a Mailbox for the settings.
func New(cfg config.Settings, opts ...Option) *Mailbox {
	m := &Mailbox{cfg: cfg, now: time.Now}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Address is the account address.
func (m *Mailbox) Address() string { return m.cfg.Address }

func (m *Mailbox) tlsFor(addr string) *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.tls != nil {
		c = m.tls.Clone()
	}
	if c.ServerName == "" {
		c.ServerName, _, _ = net.SplitHostPort(addr)
	}
	return c
}

// dial opens a logged-in IMAP session. The caller closes it.
func (m *Mailbox) dial(ctx context.Context) (*imapclient.Client, error) {
	addr := m.cfg.IMAPHost
	d := &net.Dialer{Timeout: dialTimeout}
	opts := &imapclient.Options{TLSConfig: m.tlsFor(addr), Dialer: d}
	var c *imapclient.Client
	var err error
	if strings.HasSuffix(addr, ":143") {
		c, err = imapclient.DialStartTLS(addr, opts)
	} else {
		c, err = imapclient.DialTLS(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() }) // ends a hung session
	if err := c.Login(m.cfg.Address, m.cfg.Password).Wait(); err != nil {
		stop()
		_ = c.Close()
		var ie *imap.Error
		if errors.As(err, &ie) && ie.Type == imap.StatusResponseTypeNo {
			return nil, fmt.Errorf("%w: %s", ErrAuth, ie.Text)
		}
		return nil, fmt.Errorf("log in to %s: %w", addr, err)
	}
	return c, nil
}

func logout(c *imapclient.Client) {
	_ = c.Logout().Wait() // the session ends either way
	_ = c.Close()
}

// Check logs in and out.
func (m *Mailbox) Check(ctx context.Context) error {
	c, err := m.dial(ctx)
	if err != nil {
		return err
	}
	logout(c)
	return nil
}

// SearchQuery is the input of the search operation.
type SearchQuery struct {
	Query  string `json:"query"`
	From   string `json:"from,omitempty"`
	Since  string `json:"since,omitempty"` // YYYY-MM-DD
	Unread bool   `json:"unread,omitempty"`
	Folder string `json:"folder,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Summary is one message in a search result.
type Summary struct {
	ID        string   `json:"id"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Date      string   `json:"date"`
	Snippet   string   `json:"snippet"`
	MessageID string   `json:"message_id"`
	Unread    bool     `json:"unread"`
}

func (q SearchQuery) criteria() (*imap.SearchCriteria, error) {
	c := &imap.SearchCriteria{}
	if s := strings.TrimSpace(q.Query); s != "" {
		c.Text = []string{s}
	}
	if s := strings.TrimSpace(q.From); s != "" {
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: s})
	}
	if q.Since != "" {
		t, err := time.Parse(time.DateOnly, q.Since)
		if err != nil {
			return nil, fmt.Errorf("since: use the form YYYY-MM-DD")
		}
		c.Since = t
	}
	if q.Unread {
		c.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	return c, nil
}

// Search finds messages in one folder, newest first.
func (m *Mailbox) Search(ctx context.Context, q SearchQuery) ([]Summary, error) {
	crit, err := q.criteria()
	if err != nil {
		return nil, err
	}
	folder := cmp.Or(q.Folder, "INBOX")
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	c, err := m.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer logout(c)
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("open folder %q: %w", folder, err)
	}
	data, err := c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	uids := data.AllUIDs()
	slices.Sort(uids)
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}
	out := []Summary{}
	if len(uids) == 0 {
		return out, nil
	}
	part := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: snippetBytes}}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true, BodySection: []*imap.FetchItemBodySection{part},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	slices.SortFunc(msgs, func(a, b *imapclient.FetchMessageBuffer) int { return int(b.UID) - int(a.UID) })
	for _, msg := range msgs {
		s := Summary{ID: MessageRef{Folder: folder, UID: msg.UID}.String(), To: []string{}, Unread: !slices.Contains(msg.Flags, imap.FlagSeen)}
		if env := msg.Envelope; env != nil {
			s.Subject, s.MessageID = env.Subject, angle(env.MessageID)
			if len(env.From) > 0 {
				s.From = formatAddr(env.From[0])
			}
			for _, a := range env.To {
				s.To = append(s.To, formatAddr(a))
			}
			if !env.Date.IsZero() {
				s.Date = env.Date.UTC().Format(time.RFC3339)
			}
		}
		if raw := msg.FindBodySection(part); raw != nil {
			s.Snippet = snippet(raw)
		}
		out = append(out, s)
	}
	return out, nil
}

// MessageRef is the id of a message: "<folder>:<uid>".
type MessageRef struct {
	Folder string
	UID    imap.UID
}

func (r MessageRef) String() string { return r.Folder + ":" + strconv.FormatUint(uint64(r.UID), 10) }

// ParseRef reads an id. A bare number is a UID in INBOX.
func ParseRef(id string) (MessageRef, error) {
	folder, num := "INBOX", id
	if i := strings.LastIndex(id, ":"); i >= 0 {
		folder, num = id[:i], id[i+1:]
	}
	n, err := strconv.ParseUint(num, 10, 32)
	if err != nil || n == 0 || folder == "" {
		return MessageRef{}, fmt.Errorf("id %q: use an id from search, for example INBOX:42", id)
	}
	return MessageRef{Folder: folder, UID: imap.UID(n)}, nil
}

// Read fetches one message and parses it.
func (m *Mailbox) Read(ctx context.Context, id string, withHTML bool) (Message, error) {
	ref, err := ParseRef(id)
	if err != nil {
		return Message{}, err
	}
	c, err := m.dial(ctx)
	if err != nil {
		return Message{}, err
	}
	defer logout(c)
	if _, err := c.Select(ref.Folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return Message{}, fmt.Errorf("open folder %q: %w", ref.Folder, err)
	}
	part := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(ref.UID), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{part}}).Collect()
	if err != nil {
		return Message{}, fmt.Errorf("fetch: %w", err)
	}
	if len(msgs) == 0 {
		return Message{}, fmt.Errorf("no message %s", ref)
	}
	msg, err := Parse(msgs[0].FindBodySection(part))
	if err != nil {
		return Message{}, err
	}
	msg.ID = ref.String()
	if !withHTML {
		msg.HTML = ""
	}
	return msg, nil
}

// Draft saves the message in the Drafts folder with the \Draft flag.
func (m *Mailbox) Draft(ctx context.Context, d Compose) (DraftResult, error) {
	out, err := d.Build(m.cfg.Address, m.now())
	if err != nil {
		return DraftResult{}, err
	}
	c, err := m.dial(ctx)
	if err != nil {
		return DraftResult{}, err
	}
	defer logout(c)
	folder := m.cfg.Drafts
	if folder == "" {
		folder = specialFolder(c, imap.MailboxAttrDrafts, "Drafts")
	}
	uid, err := appendMsg(c, folder, out.Raw, []imap.Flag{imap.FlagDraft, imap.FlagSeen}, m.now())
	if err != nil {
		return DraftResult{}, err
	}
	res := DraftResult{MessageID: out.MessageID, Folder: folder}
	if uid != 0 {
		res.DraftID = MessageRef{Folder: folder, UID: uid}.String()
	}
	return res, nil
}

// DraftResult is the output of the draft operation.
type DraftResult struct {
	DraftID   string `json:"draft_id,omitempty"`
	MessageID string `json:"message_id"`
	Folder    string `json:"folder"`
}

// SendResult is the output of the send operation.
type SendResult struct {
	MessageID  string   `json:"message_id"`
	Recipients []string `json:"recipients"`
	SentCopy   string   `json:"sent_copy,omitempty"`
}

// Send sends the message over SMTP. With a Sent folder, it also saves a
// copy there.
func (m *Mailbox) Send(ctx context.Context, d Compose) (SendResult, error) {
	out, err := d.Build(m.cfg.Address, m.now())
	if err != nil {
		return SendResult{}, err
	}
	if len(out.Recipients) == 0 {
		return SendResult{}, errors.New("the message has no recipient")
	}
	if err := m.smtpSend(ctx, out); err != nil {
		return SendResult{}, err
	}
	res := SendResult{MessageID: out.MessageID, Recipients: out.Recipients}
	if m.cfg.Sent == "" {
		return res, nil
	}
	c, err := m.dial(ctx)
	if err != nil {
		return res, fmt.Errorf("the message is sent, but the sent copy failed: %w", err)
	}
	defer logout(c)
	if _, err := appendMsg(c, m.cfg.Sent, out.Raw, []imap.Flag{imap.FlagSeen}, m.now()); err != nil {
		return res, fmt.Errorf("the message is sent, but the sent copy failed: %w", err)
	}
	res.SentCopy = m.cfg.Sent
	return res, nil
}

func appendMsg(c *imapclient.Client, folder string, raw []byte, flags []imap.Flag, t time.Time) (imap.UID, error) {
	cmd := c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: t})
	if _, err := io.Copy(cmd, bytes.NewReader(raw)); err != nil {
		_ = cmd.Close()
		return 0, fmt.Errorf("save to %q: %w", folder, err)
	}
	if err := cmd.Close(); err != nil {
		return 0, fmt.Errorf("save to %q: %w", folder, err)
	}
	data, err := cmd.Wait()
	if err != nil {
		return 0, fmt.Errorf("save to %q: %w", folder, err)
	}
	return data.UID, nil
}

// specialFolder finds the folder with the special-use attribute, or
// returns def.
func specialFolder(c *imapclient.Client, attr imap.MailboxAttr, def string) string {
	var opts *imap.ListOptions
	if c.Caps().Has(imap.CapSpecialUse) {
		opts = &imap.ListOptions{ReturnSpecialUse: true}
	}
	list, err := c.List("", "*", opts).Collect()
	if err != nil {
		return def
	}
	for _, l := range list {
		if slices.Contains(l.Attrs, attr) {
			return l.Mailbox
		}
	}
	return def
}

func formatAddr(a imap.Address) string {
	addr := a.Addr()
	if a.Name == "" {
		return addr
	}
	return a.Name + " <" + addr + ">"
}

// angle puts a message id in angle brackets.
func angle(id string) string {
	if id == "" || strings.HasPrefix(id, "<") {
		return id
	}
	return "<" + id + ">"
}
