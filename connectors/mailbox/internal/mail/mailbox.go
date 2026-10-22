// Package mail reads a mailbox over IMAP and sends over SMTP.
package mail

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
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

// Check logs in and out.
func (m *Mailbox) Check(ctx context.Context) error {
	s, err := m.open(ctx)
	if err != nil {
		return err
	}
	s.close()
	return nil
}

// SearchQuery is the input of the search operation.
type SearchQuery struct {
	Query  string `json:"query"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Since  string `json:"since,omitempty"`  // YYYY-MM-DD
	Before string `json:"before,omitempty"` // YYYY-MM-DD
	Unread bool   `json:"unread,omitempty"`
	Folder string `json:"folder,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// SearchResult is the output of the search operation.
type SearchResult struct {
	Messages   []Summary `json:"messages"`
	NextCursor string    `json:"next_cursor"`
}

// Summary is one message in a search result.
type Summary struct {
	ID        string   `json:"id"`
	ThreadID  string   `json:"thread_id,omitempty"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Date      string   `json:"date"`
	Snippet   string   `json:"snippet"`
	MessageID string   `json:"message_id"`
	Unread    bool     `json:"unread"`
	Labels    []string `json:"labels,omitempty"`
}

func (q SearchQuery) criteria() (*imap.SearchCriteria, error) {
	c := &imap.SearchCriteria{}
	if s := strings.TrimSpace(q.Query); s != "" {
		c.Text = []string{s}
	}
	for k, v := range map[string]string{"From": q.From, "To": q.To} {
		if s := strings.TrimSpace(v); s != "" {
			c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: k, Value: s})
		}
	}
	slices.SortFunc(c.Header, func(a, b imap.SearchCriteriaHeaderField) int { return strings.Compare(a.Key, b.Key) })
	if q.Since != "" {
		t, err := time.Parse(time.DateOnly, q.Since)
		if err != nil {
			return nil, fmt.Errorf("since: use the form YYYY-MM-DD")
		}
		c.Since = t
	}
	if q.Before != "" {
		t, err := time.Parse(time.DateOnly, q.Before)
		if err != nil {
			return nil, fmt.Errorf("before: use the form YYYY-MM-DD")
		}
		c.Before = t
	}
	if q.Unread {
		c.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	return c, nil
}

// pageCursor is where the next page of a search starts: the UIDs below
// UID in the folder.
type pageCursor struct {
	Folder      string   `json:"f"`
	UIDValidity uint32   `json:"v"`
	UID         imap.UID `json:"u"`
}

func (p pageCursor) String() string {
	b, _ := json.Marshal(p) //nolint:errchkjson // plain fields
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseCursor(s string) (pageCursor, error) {
	var p pageCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &p) != nil || p.Folder == "" || p.UID == 0 {
		return p, errors.New("cursor: use the next_cursor of the last page")
	}
	return p, nil
}

// Search finds messages in one folder, newest first, one page at a time.
func (m *Mailbox) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	res := SearchResult{Messages: []Summary{}}
	crit, err := q.criteria()
	if err != nil {
		return res, err
	}
	folder := q.Folder
	var cur pageCursor
	if q.Cursor != "" {
		if cur, err = parseCursor(q.Cursor); err != nil {
			return res, err
		}
		if folder != "" && folder != cur.Folder {
			return res, fmt.Errorf("cursor: it is for the folder %q, not %q", cur.Folder, folder)
		}
		folder = cur.Folder
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	s, err := m.open(ctx)
	if err != nil {
		return res, err
	}
	defer s.close()
	if folder == "" {
		folder = "INBOX"
	}
	sel, err := s.selectFolder(folder, false)
	if err != nil {
		return res, err
	}
	if cur.UID != 0 && cur.UIDValidity != sel.UIDValidity {
		return res, errors.New("cursor: the folder changed since the last page; search again with no cursor")
	}
	data, err := s.c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return res, fmt.Errorf("search: %w", err)
	}
	uids := data.AllUIDs()
	slices.Sort(uids)
	if cur.UID != 0 {
		uids = uids[:sort.Search(len(uids), func(i int) bool { return uids[i] >= cur.UID })]
	}
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
		res.NextCursor = pageCursor{Folder: folder, UIDValidity: sel.UIDValidity, UID: uids[0]}.String()
	}
	if res.Messages, err = s.summaries(folder, uids); err != nil {
		return res, err
	}
	return res, nil
}

// summaries fetches the messages in the selected folder, newest first.
func (s *session) summaries(folder string, uids []imap.UID) ([]Summary, error) {
	out := []Summary{}
	if len(uids) == 0 {
		return out, nil
	}
	part := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: snippetBytes}}
	msgs, err := s.c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true, BodySection: []*imap.FetchItemBodySection{part},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	slices.SortFunc(msgs, func(a, b *imapclient.FetchMessageBuffer) int { return cmp.Compare(b.UID, a.UID) })
	for _, msg := range msgs {
		sum := Summary{ID: MessageRef{Folder: folder, UID: msg.UID}.String(), To: []string{}, Unread: !slices.Contains(msg.Flags, imap.FlagSeen)}
		if env := msg.Envelope; env != nil {
			sum.Subject, sum.MessageID = env.Subject, angle(env.MessageID)
			if len(env.From) > 0 {
				sum.From = formatAddr(env.From[0])
			}
			for _, a := range env.To {
				sum.To = append(sum.To, formatAddr(a))
			}
			if !env.Date.IsZero() {
				sum.Date = env.Date.UTC().Format(time.RFC3339)
			}
		}
		if raw := msg.FindBodySection(part); raw != nil {
			sum.Snippet = snippet(raw)
		}
		out = append(out, sum)
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

// locate finds the folder and UID of a message id.
func (s *session) locate(id string) (MessageRef, error) {
	return ParseRef(id)
}

// Read fetches one message and parses it.
func (m *Mailbox) Read(ctx context.Context, id string, withHTML bool) (Message, error) {
	s, err := m.open(ctx)
	if err != nil {
		return Message{}, err
	}
	defer s.close()
	return s.read(id, withHTML)
}

func (s *session) read(id string, withHTML bool) (Message, error) {
	ref, err := s.locate(id)
	if err != nil {
		return Message{}, err
	}
	if _, err := s.selectFolder(ref.Folder, false); err != nil {
		return Message{}, err
	}
	_, raw, err := s.fetchRaw(ref.UID)
	if errors.Is(err, errNoMessage) {
		return Message{}, fmt.Errorf("no message %s", id)
	}
	if err != nil {
		return Message{}, err
	}
	msg, err := Parse(raw)
	if err != nil {
		return Message{}, err
	}
	msg.ID = id
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
	s, err := m.open(ctx)
	if err != nil {
		return DraftResult{}, err
	}
	defer s.close()
	return s.saveDraft(out)
}

func (s *session) saveDraft(out Built) (DraftResult, error) {
	folder := s.drafts()
	uid, err := s.appendMsg(folder, out.Raw, []imap.Flag{imap.FlagDraft, imap.FlagSeen}, s.m.now())
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
	return m.sendBuilt(ctx, out)
}

func (m *Mailbox) sendBuilt(ctx context.Context, out Built) (SendResult, error) {
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
	s, err := m.open(ctx)
	if err != nil {
		return res, fmt.Errorf("the message is sent, but the sent copy failed: %w", err)
	}
	defer s.close()
	if _, err := s.appendMsg(m.cfg.Sent, out.Raw, []imap.Flag{imap.FlagSeen}, m.now()); err != nil {
		return res, fmt.Errorf("the message is sent, but the sent copy failed: %w", err)
	}
	res.SentCopy = m.cfg.Sent
	return res, nil
}

// draft finds a saved draft and returns its bytes. A message that is not
// in the Drafts folder or has no \Draft flag is an error.
func (s *session) draft(id string) (MessageRef, []byte, error) {
	ref, err := s.locate(id)
	if err != nil {
		return ref, nil, err
	}
	if ref.Folder != s.drafts() {
		return ref, nil, fmt.Errorf("%s is not a draft: give the draft_id from draft or reply", id)
	}
	if _, err := s.selectFolder(ref.Folder, true); err != nil {
		return ref, nil, err
	}
	flags, raw, err := s.fetchRaw(ref.UID)
	if errors.Is(err, errNoMessage) {
		return ref, nil, fmt.Errorf("no draft %s", id)
	}
	if err != nil {
		return ref, nil, err
	}
	if !slices.Contains(flags, imap.FlagDraft) {
		return ref, nil, fmt.Errorf("%s is not a draft: give the draft_id from draft or reply", id)
	}
	return ref, raw, nil
}

// SendDraft sends a saved draft without its Bcc header, and then deletes
// the draft.
func (m *Mailbox) SendDraft(ctx context.Context, id string) (SendResult, error) {
	s, err := m.open(ctx)
	if err != nil {
		return SendResult{}, err
	}
	defer s.close()
	ref, raw, err := s.draft(id)
	if err != nil {
		return SendResult{}, err
	}
	out, err := finish(raw, m.cfg.Address, m.now())
	if err != nil {
		return SendResult{}, err
	}
	res, err := m.sendBuilt(ctx, out)
	if err != nil {
		return res, err
	}
	if err := s.remove([]imap.UID{ref.UID}); err != nil {
		return res, fmt.Errorf("the message is sent, but the draft stays: %w", err)
	}
	return res, nil
}

// DeleteDraft deletes a saved draft.
func (m *Mailbox) DeleteDraft(ctx context.Context, id string) error {
	s, err := m.open(ctx)
	if err != nil {
		return err
	}
	defer s.close()
	ref, _, err := s.draft(id)
	if err != nil {
		return err
	}
	return s.remove([]imap.UID{ref.UID})
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
