package mail

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

// Thread limits.
const (
	DefaultThreadLimit = 20
	MaxThreadLimit     = 50
	maxThreadText      = 64 << 10
	maxThreadRounds    = 5
)

// ThreadResult is the output of the thread operation.
type ThreadResult struct {
	ThreadID string    `json:"thread_id"`
	Messages []Message `json:"messages"`
}

// Thread reads the conversation of a message, oldest first. The text of
// each message has no quote of the message before it.
func (m *Mailbox) Thread(ctx context.Context, id string, limit int, withHTML bool) (ThreadResult, error) {
	if limit <= 0 {
		limit = DefaultThreadLimit
	}
	limit = min(limit, MaxThreadLimit)
	s, err := m.open(ctx)
	if err != nil {
		return ThreadResult{}, err
	}
	defer s.close()
	ref, err := s.locate(id)
	if err != nil {
		return ThreadResult{}, err
	}
	first, err := s.read(id, true)
	if err != nil {
		return ThreadResult{}, err
	}
	var msgs []Message
	if s.isGmail() {
		msgs, err = s.threadByGmail(first, limit)
	} else {
		msgs, err = s.threadByHeaders(ref, first)
	}
	if err != nil {
		return ThreadResult{}, err
	}
	res := ThreadResult{ThreadID: first.ThreadID, Messages: []Message{}}
	slices.SortStableFunc(msgs, func(a, b Message) int { return strings.Compare(a.Headers.Date, b.Headers.Date) })
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	for _, msg := range msgs {
		msg.Text = threadText(msg)
		if !withHTML {
			msg.HTML = ""
		}
		res.Messages = append(res.Messages, msg)
	}
	return res, nil
}

// threadRoot is the first id of the References chain, or the message id.
func threadRoot(h Headers) string {
	if len(h.References) > 0 {
		return h.References[0]
	}
	if h.InReplyTo != "" {
		return h.InReplyTo
	}
	return h.MessageID
}

// threadByGmail reads the newest limit messages of the Gmail thread of
// first from All Mail.
func (s *session) threadByGmail(first Message, limit int) ([]Message, error) {
	thrid, err := strconv.ParseUint(first.ThreadID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("thread id %q: %w", first.ThreadID, err)
	}
	all, err := s.allMail()
	if err != nil {
		return nil, err
	}
	g, err := s.gmail()
	if err != nil {
		return nil, err
	}
	if err := g.selectFolder(all, false); err != nil {
		return nil, err
	}
	uids, err := g.threadUIDs(thrid)
	if err != nil {
		return nil, err
	}
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}
	meta, err := s.gmailMeta(all, uids)
	if err != nil {
		return nil, err
	}
	if _, err := s.selectFolder(all, false); err != nil {
		return nil, err
	}
	part := &imap.FetchItemBodySection{Peek: true}
	bufs, err := s.c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{part}}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	msgs := []Message{}
	for _, b := range bufs {
		msg, err := Parse(b.FindBodySection(part))
		if err != nil {
			continue
		}
		msg.ID = MessageRef{Folder: all, UID: b.UID}.String()
		if m, ok := meta[b.UID]; ok {
			msg.ID, msg.ThreadID, msg.Labels = m.GmailID(), m.ThreadID(), m.Labels
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		msgs = append(msgs, first)
	}
	return msgs, nil
}

// threadByHeaders follows References and In-Reply-To in the folder of the
// message and in the Sent folder.
func (s *session) threadByHeaders(ref MessageRef, first Message) ([]Message, error) {
	folders := []string{ref.Folder}
	sent := s.m.cfg.Sent
	if sent == "" {
		sent = s.special(imap.MailboxAttrSent)
	}
	if sent != "" && sent != ref.Folder {
		folders = append(folders, sent)
	}
	ids := map[string]bool{}
	var todo []string
	add := func(h Headers) {
		for _, id := range append([]string{h.MessageID, h.InReplyTo}, h.References...) {
			if id != "" && !ids[id] {
				ids[id] = true
				todo = append(todo, id)
			}
		}
	}
	add(first.Headers)
	found := map[MessageRef]bool{ref: true}
	byID := map[string]Message{}
	key := func(msg Message, r MessageRef) string { return cmp.Or(msg.Headers.MessageID, r.String()) }
	byID[key(first, ref)] = first
	for round := 0; round < maxThreadRounds && len(todo) > 0; round++ {
		batch := todo
		todo = nil
		for _, f := range folders {
			if _, err := s.selectFolder(f, false); err != nil {
				continue
			}
			var uids []imap.UID
			for _, id := range batch {
				for _, field := range []string{"Message-Id", "References", "In-Reply-To"} {
					data, err := s.c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: field, Value: id}}}, nil).Wait()
					if err != nil {
						return nil, fmt.Errorf("search: %w", err)
					}
					uids = append(uids, data.AllUIDs()...)
				}
			}
			slices.Sort(uids)
			for _, u := range slices.Compact(uids) {
				r := MessageRef{Folder: f, UID: u}
				if found[r] {
					continue
				}
				found[r] = true
				msg, err := s.read(r.String(), true)
				if err != nil {
					continue
				}
				if _, dup := byID[key(msg, r)]; dup {
					continue
				}
				byID[key(msg, r)] = msg
				add(msg.Headers)
			}
		}
	}
	out := make([]Message, 0, len(byID))
	for _, msg := range byID {
		out = append(out, msg)
	}
	return out, nil
}

// threadText drops the quote and cuts a long text.
func threadText(msg Message) string {
	text := msg.Text
	if msg.HTML != "" && strings.Contains(msg.HTML, "gmail_quote") && text == HTMLText(msg.HTML) {
		text = HTMLText(StripHTMLQuotes(msg.HTML))
	} else {
		text = StripQuotes(text)
	}
	if len(text) > maxThreadText {
		cut := maxThreadText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n\n[The text is cut here. Use read with the id for the full message.]"
	}
	return text
}
