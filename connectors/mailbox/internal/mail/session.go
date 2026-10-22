package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// session is one logged-in IMAP connection. It keeps the folder list and
// the selected folder so that an action asks the server for each once.
type session struct {
	c        *imapclient.Client
	m        *Mailbox
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
	return &session{c: c, m: m}, nil
}

func (s *session) close() {
	_ = s.c.Logout().Wait() // the session ends either way
	_ = s.c.Close()
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
