package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// session is one logged-in IMAP connection. It keeps the folder list and
// the selected folder so that an action asks the server for each once.
type session struct {
	c        *imapclient.Client
	m        *Mailbox
	ctx      context.Context //nolint:containedctx // the Gmail connection dials later in the same call
	gm       *gmailConn
	selected string
	writable bool
	sel      *imap.SelectData
	list     []*imap.ListData
}

// open logs in. The caller closes the session.
func (m *Mailbox) open(ctx context.Context) (*session, error) {
	c, err := m.dial(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &session{c: c, m: m, ctx: ctx}, nil
}

func (s *session) close() {
	s.gm.close()
	_ = s.c.Logout().Wait() // the session ends either way
	_ = s.c.Close()
}

// isGmail reports whether the server has the Gmail extensions.
func (s *session) isGmail() bool { return s.c.Caps().Has(capGmail) }

// gmail returns the Gmail connection. It dials on the first call.
func (s *session) gmail() (*gmailConn, error) {
	if s.gm != nil {
		return s.gm, nil
	}
	g, err := s.m.dialGmail(s.ctx)
	if err != nil {
		return nil, err
	}
	s.gm = g
	return g, nil
}

// allMail is the Gmail folder with all messages.
func (s *session) allMail() (string, error) {
	if f := s.special(imap.MailboxAttrAll); f != "" {
		return f, nil
	}
	return "", errors.New("this Gmail account shows no All Mail folder: turn on \"Show in IMAP\" for All Mail in the Gmail settings")
}

// gmailMeta returns the Gmail data of messages in a folder, or nil when
// the server is not Gmail.
func (s *session) gmailMeta(folder string, uids []imap.UID) (map[imap.UID]gmailMeta, error) {
	if !s.isGmail() || len(uids) == 0 {
		return nil, nil
	}
	g, err := s.gmail()
	if err != nil {
		return nil, err
	}
	if err := g.selectFolder(folder, false); err != nil {
		return nil, err
	}
	meta, err := g.meta(uids)
	if err != nil {
		return nil, err
	}
	if l := s.folderLabel(folder); l != "" {
		for u, m := range meta {
			if !slices.Contains(m.Labels, l) {
				m.Labels = append(m.Labels, l)
				meta[u] = m
			}
		}
	}
	return meta, nil
}

// folderLabel is the label of a Gmail folder. Gmail leaves the label of
// the open folder out of the labels of its messages.
func (s *session) folderLabel(folder string) string {
	if strings.EqualFold(folder, "INBOX") {
		return "Inbox"
	}
	list, _ := s.folders() //nolint:errcheck // with no list, the name is the label
	for _, l := range list {
		if l.Mailbox != folder {
			continue
		}
		for attr, name := range folderLabels {
			if slices.Contains(l.Attrs, attr) {
				return name
			}
		}
	}
	return folder
}

// folderLabels maps the special-use attributes to the Gmail labels. All
// Mail has no label.
var folderLabels = map[imap.MailboxAttr]string{
	imap.MailboxAttrAll: "", imap.MailboxAttrSent: "Sent", imap.MailboxAttrDrafts: "Drafts",
	imap.MailboxAttrFlagged: "Starred", imap.MailboxAttrImportant: "Important",
	imap.MailboxAttrJunk: "Spam", imap.MailboxAttrTrash: "Trash",
}

// folders lists all folders with their special-use attributes.
func (s *session) folders() ([]*imap.ListData, error) {
	if s.list != nil {
		return s.list, nil
	}
	var opts *imap.ListOptions
	if s.c.Caps().Has(imap.CapSpecialUse) {
		opts = &imap.ListOptions{ReturnSpecialUse: true}
	}
	list, err := s.c.List("", "*", opts).Collect()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	s.list = list
	return list, nil
}

// special returns the folder with the special-use attribute, or "".
func (s *session) special(attr imap.MailboxAttr) string {
	list, err := s.folders()
	if err != nil {
		return ""
	}
	for _, l := range list {
		if slices.Contains(l.Attrs, attr) {
			return l.Mailbox
		}
	}
	return ""
}

// drafts is the Drafts folder: the setting, the \Drafts folder, or
// "Drafts".
func (s *session) drafts() string {
	if s.m.cfg.Drafts != "" {
		return s.m.cfg.Drafts
	}
	if f := s.special(imap.MailboxAttrDrafts); f != "" {
		return f
	}
	return "Drafts"
}

// selectFolder opens a folder. A folder that is open read-only opens
// again when write is true.
func (s *session) selectFolder(folder string, write bool) (*imap.SelectData, error) {
	if s.selected == folder && (s.writable || !write) {
		return s.sel, nil
	}
	data, err := s.c.Select(folder, &imap.SelectOptions{ReadOnly: !write}).Wait()
	if err != nil {
		s.selected = ""
		return nil, fmt.Errorf("open folder %q: %w", folder, err)
	}
	s.selected, s.writable, s.sel = folder, write, data
	return data, nil
}

// status returns the UIDVALIDITY and the counts of a folder.
func (s *session) status(folder string) (*imap.StatusData, error) {
	data, err := s.c.Status(folder, &imap.StatusOptions{UIDValidity: true, UIDNext: true, NumMessages: true, NumUnseen: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("status of %q: %w", folder, err)
	}
	return data, nil
}

// fetchRaw returns the flags and the full bytes of one message in the
// selected folder. It does not set \Seen.
func (s *session) fetchRaw(uid imap.UID) ([]imap.Flag, []byte, error) {
	part := &imap.FetchItemBodySection{Peek: true}
	msgs, err := s.c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Flags: true, BodySection: []*imap.FetchItemBodySection{part}}).Collect()
	if err != nil {
		return nil, nil, fmt.Errorf("fetch: %w", err)
	}
	if len(msgs) == 0 {
		return nil, nil, errNoMessage
	}
	return msgs[0].Flags, msgs[0].FindBodySection(part), nil
}

var errNoMessage = errors.New("no such message")

// appendMsg saves a message in a folder and returns its UID, or 0 when
// the server has no UIDPLUS.
func (s *session) appendMsg(folder string, raw []byte, flags []imap.Flag, t time.Time) (imap.UID, error) {
	cmd := s.c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: t})
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

// store changes the flags of messages in the selected folder.
func (s *session) store(uids []imap.UID, op imap.StoreFlagsOp, flags ...imap.Flag) error {
	return s.c.Store(imap.UIDSetNum(uids...), &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close()
}

// remove deletes messages from the selected folder.
func (s *session) remove(uids []imap.UID) error {
	if err := s.store(uids, imap.StoreFlagsAdd, imap.FlagDeleted); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	var err error
	if s.c.Caps().Has(imap.CapUIDPlus) {
		err = s.c.UIDExpunge(imap.UIDSetNum(uids...)).Close()
	} else {
		err = s.c.Expunge().Close()
	}
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// move moves messages from the selected folder. With no MOVE, it copies
// and deletes.
func (s *session) move(uids []imap.UID, dest string) error {
	if s.c.Caps().Has(imap.CapMove) {
		if _, err := s.c.Move(imap.UIDSetNum(uids...), dest).Wait(); err != nil {
			return fmt.Errorf("move to %q: %w", dest, err)
		}
		return nil
	}
	if _, err := s.c.Copy(imap.UIDSetNum(uids...), dest).Wait(); err != nil {
		return fmt.Errorf("copy to %q: %w", dest, err)
	}
	return s.remove(uids)
}
