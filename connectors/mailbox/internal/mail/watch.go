package mail

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// eventSnippetLen is the length of the snippet in a new mail event.
const eventSnippetLen = 500

// NewMail is one message that arrived in the watched folder.
type NewMail struct {
	UID            imap.UID
	ID             string // the id for read
	ThreadID       string
	Labels         []string
	From           string
	FromAddr       string
	FromName       string
	To             []string
	Cc             []string
	Subject        string
	Date           string
	MessageID      string
	InReplyTo      string
	Snippet        string
	Size           int64
	Attachments    []Attachment
	HasAttachments bool
}

// WatchConn is one connection with a folder selected, for IDLE.
type WatchConn struct {
	s           *session
	folder      string
	exists      chan struct{}
	UIDValidity uint32
	UIDNext     imap.UID
}

// Watch logs in and selects the folder read-only. An EXISTS response
// from the server signals Exists.
func (m *Mailbox) Watch(ctx context.Context, folder string) (*WatchConn, error) {
	w := &WatchConn{folder: folder, exists: make(chan struct{}, 1)}
	c, err := m.dial(ctx, &imapclient.UnilateralDataHandler{
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				w.signal()
			}
		},
	})
	if err != nil {
		return nil, err
	}
	w.s = &session{c: c, m: m}
	sel, err := w.s.selectFolder(folder, false)
	if err != nil {
		w.Close()
		return nil, err
	}
	w.UIDValidity, w.UIDNext = sel.UIDValidity, sel.UIDNext
	return w, nil
}

func (w *WatchConn) signal() {
	select {
	case w.exists <- struct{}{}:
	default:
	}
}

// Exists signals that the folder has more messages.
func (w *WatchConn) Exists() <-chan struct{} { return w.exists }

// Closed is closed when the connection ends.
func (w *WatchConn) Closed() <-chan struct{} { return w.s.c.Closed() }

// Close ends the connection with no logout.
func (w *WatchConn) Close() { _ = w.s.c.Close() }

// Idle starts IDLE. Call the returned stop before the next command.
func (w *WatchConn) Idle() (stop func() error, err error) {
	cmd, err := w.s.c.Idle()
	if err != nil {
		return nil, fmt.Errorf("idle: %w", err)
	}
	return func() error {
		if err := cmd.Close(); err != nil {
			return err
		}
		return cmd.Wait()
	}, nil
}

// Noop asks the server for news. With no answer in timeout, it closes the
// connection.
func (w *WatchConn) Noop(timeout time.Duration) error {
	t := time.AfterFunc(timeout, w.Close)
	defer t.Stop()
	if err := w.s.c.Noop().Wait(); err != nil {
		return fmt.Errorf("noop: %w", err)
	}
	return nil
}

// Fetch returns the messages with a UID after after, in UID order.
func (w *WatchConn) Fetch(after imap.UID) ([]NewMail, error) {
	part := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: snippetBytes}}
	set := imap.UIDSet{imap.UIDRange{Start: after + 1, Stop: 0}}
	msgs, err := w.s.c.Fetch(set, &imap.FetchOptions{
		UID: true, Envelope: true, RFC822Size: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{part},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch new mail: %w", err)
	}
	var out []NewMail
	for _, msg := range msgs {
		if msg.UID <= after { // "n:*" returns the last message when n is past the end
			continue
		}
		nm := NewMail{UID: msg.UID, ID: MessageRef{Folder: w.folder, UID: msg.UID}.String(), Size: msg.RFC822Size, To: []string{}, Cc: []string{}, Attachments: []Attachment{}}
		if env := msg.Envelope; env != nil {
			nm.Subject, nm.MessageID = env.Subject, angle(env.MessageID)
			if len(env.InReplyTo) > 0 {
				nm.InReplyTo = angle(env.InReplyTo[0])
			}
			if len(env.From) > 0 {
				nm.From, nm.FromAddr, nm.FromName = formatAddr(env.From[0]), env.From[0].Addr(), env.From[0].Name
			}
			for _, a := range env.To {
				nm.To = append(nm.To, formatAddr(a))
			}
			for _, a := range env.Cc {
				nm.Cc = append(nm.Cc, formatAddr(a))
			}
			if !env.Date.IsZero() {
				nm.Date = env.Date.UTC().Format(time.RFC3339)
			}
		}
		if raw := msg.FindBodySection(part); raw != nil {
			nm.Snippet = snippet(raw, eventSnippetLen)
			if h, _ := parse(raw); len(h.Headers.References) > 0 {
				nm.ThreadID = h.Headers.References[0]
			}
		}
		if bs := msg.BodyStructure; bs != nil {
			nm.Attachments = attachmentsOf(bs)
			nm.HasAttachments = len(nm.Attachments) > 0
		}
		if nm.ThreadID == "" {
			nm.ThreadID = cmp.Or(nm.InReplyTo, nm.MessageID)
		}
		out = append(out, nm)
	}
	slices.SortFunc(out, func(a, b NewMail) int { return cmp.Compare(a.UID, b.UID) })
	return out, nil
}

// attachmentsOf lists the parts that are attachments.
func attachmentsOf(bs imap.BodyStructure) []Attachment {
	out := []Attachment{}
	bs.Walk(func(_ []int, part imap.BodyStructure) bool {
		sp, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		d := sp.Disposition()
		name := sp.Filename()
		if (d != nil && strings.EqualFold(d.Value, "attachment")) || (name != "" && !strings.HasPrefix(sp.MediaType(), "text/")) {
			out = append(out, Attachment{Name: name, Type: sp.MediaType(), Size: int64(sp.Size)})
		}
		return true
	})
	return out
}

// LastUID is the highest UID in the folder: UIDNEXT - 1, or a search when
// the server gave no UIDNEXT.
func (w *WatchConn) LastUID() (imap.UID, error) {
	if w.UIDNext > 0 {
		return w.UIDNext - 1, nil
	}
	data, err := w.s.c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	return slices.Max(append(data.AllUIDs(), 0)), nil
}
