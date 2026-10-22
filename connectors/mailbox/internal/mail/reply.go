package mail

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
)

// maxReferences caps the References header of a reply.
const maxReferences = 8 << 10

// ReplyInput is the input of the reply operations.
type ReplyInput struct {
	ID          string      `json:"id"`
	Text        string      `json:"text,omitempty"`
	HTML        string      `json:"html,omitempty"`
	All         bool        `json:"all,omitempty"`
	Cc          []string    `json:"cc,omitempty"`
	Bcc         []string    `json:"bcc,omitempty"`
	Attachments []NewAttach `json:"attachments,omitempty"`
}

// ReplyResult is the output of the reply operations: a draft or a sent
// message.
type ReplyResult struct {
	DraftResult
	Recipients []string `json:"recipients"`
	Sent       bool     `json:"sent"`
	SentCopy   string   `json:"sent_copy,omitempty"`
}

// Reply answers a message. With send false, it saves a draft.
func (m *Mailbox) Reply(ctx context.Context, in ReplyInput, send bool) (ReplyResult, error) {
	if in.ID == "" {
		return ReplyResult{}, errors.New("id: give the id of the message to answer")
	}
	if strings.TrimSpace(in.Text) == "" && strings.TrimSpace(in.HTML) == "" {
		return ReplyResult{}, errors.New("text: give the text of the answer")
	}
	s, err := m.open(ctx)
	if err != nil {
		return ReplyResult{}, err
	}
	defer s.close()
	orig, err := s.read(in.ID, false)
	if err != nil {
		return ReplyResult{}, err
	}
	c, err := ReplyHeaders(orig.Headers, m.cfg.Address, in.All)
	if err != nil {
		return ReplyResult{}, err
	}
	c.Text, c.HTML, c.Attachments = in.Text, in.HTML, in.Attachments
	c.Cc = appendNew(c.Cc, in.Cc, c.To)
	c.Bcc = in.Bcc
	out, err := c.Build(m.cfg.Address, m.now())
	if err != nil {
		return ReplyResult{}, err
	}
	res := ReplyResult{Recipients: out.Recipients}
	if !send {
		res.DraftResult, err = s.saveDraft(out)
		return res, err
	}
	sent, err := m.sendBuilt(ctx, out)
	res.MessageID, res.Sent, res.SentCopy = sent.MessageID, err == nil, sent.SentCopy
	return res, err
}

// ReplyHeaders makes the recipients, the subject, and the thread headers
// of an answer to orig. self is the account address; it never gets the
// answer. With all, the other To and Cc addresses get it too. An answer to
// a message from self goes to its To addresses, as in Gmail.
func ReplyHeaders(orig Headers, self string, all bool) (Compose, error) {
	var c Compose
	to := orig.ReplyTo
	if len(to) == 0 && orig.From != "" {
		to = []string{orig.From}
	}
	skip := []string{self}
	c.To = appendNew(nil, to, skip)
	if len(c.To) == 0 && len(to) > 0 {
		c.To = appendNew(nil, orig.To, skip)
	}
	if all {
		c.To = appendNew(c.To, orig.To, skip)
		c.Cc = appendNew(nil, orig.Cc, append(skip, c.To...))
	}
	if len(c.To) == 0 {
		if len(c.Cc) == 0 {
			return c, errors.New("the message has no sender to answer")
		}
		c.To, c.Cc = c.Cc, nil
	}
	c.Subject = strings.TrimSpace(orig.Subject)
	if !hasRePrefix(c.Subject) {
		c.Subject = strings.TrimSpace("Re: " + c.Subject)
	}
	if orig.MessageID != "" {
		id, err := msgID(orig.MessageID)
		if err != nil {
			return c, fmt.Errorf("the message id of the message: %w", err)
		}
		c.InReplyTo = id
		refs := append([]string(nil), orig.References...)
		if len(refs) == 0 && orig.InReplyTo != "" {
			if r, err := msgID(orig.InReplyTo); err == nil {
				refs = append(refs, r)
			}
		}
		refs = append(refs, id)
		for len(refs) > 1 && len(strings.Join(refs, " ")) > maxReferences {
			refs = refs[1:]
		}
		c.references = refs
	}
	return c, nil
}

func hasRePrefix(s string) bool {
	return len(s) >= 3 && strings.EqualFold(s[:3], "re:")
}

// appendNew appends the addresses of add that are not in dst or in skip.
// It compares the address part only, with no case.
func appendNew(dst, add, skip []string) []string {
	seen := map[string]bool{}
	for _, list := range [][]string{dst, skip} {
		for _, a := range list {
			seen[addrKey(a)] = true
		}
	}
	for _, a := range add {
		k := addrKey(a)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		dst = append(dst, a)
	}
	return dst
}

func addrKey(s string) string {
	if a, err := netmail.ParseAddress(s); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.TrimSpace(s))
}
