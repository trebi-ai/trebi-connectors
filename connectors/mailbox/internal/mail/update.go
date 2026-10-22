package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// MaxIDs caps the ids of one write action.
const MaxIDs = 100

// UpdateResult is the output of a write action on messages. One bad id
// does not stop the others.
type UpdateResult struct {
	Changed []string  `json:"changed"`
	Failed  []Failure `json:"failed"`
}

// Failure is one id that a write action did not change.
type Failure struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// MarkInput is the input of the mark operation. A nil field stays as it
// is.
type MarkInput struct {
	IDs     []string `json:"ids"`
	Read    *bool    `json:"read,omitempty"`
	Starred *bool    `json:"starred,omitempty"`
}

// LabelInput is the input of the label operation.
type LabelInput struct {
	IDs    []string `json:"ids"`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	Create bool     `json:"create,omitempty"`
}

// batch is the ids of one write action, grouped by folder.
type batch struct {
	res     UpdateResult
	folders []string
	uids    map[string][]imap.UID
	ids     map[MessageRef]string
}

// group finds each id and keeps the ones that exist, by folder.
func (s *session) group(ids []string) (*batch, error) {
	if len(ids) == 0 {
		return nil, errors.New("ids: give at least one message id")
	}
	if len(ids) > MaxIDs {
		return nil, fmt.Errorf("ids: give at most %d ids in one call", MaxIDs)
	}
	b := &batch{res: UpdateResult{Changed: []string{}, Failed: []Failure{}}, uids: map[string][]imap.UID{}, ids: map[MessageRef]string{}}
	for _, id := range ids {
		ref, err := s.locate(id)
		if err != nil {
			b.fail(id, err.Error())
			continue
		}
		if _, dup := b.ids[ref]; dup {
			continue
		}
		if _, ok := b.uids[ref.Folder]; !ok {
			b.folders = append(b.folders, ref.Folder)
		}
		b.uids[ref.Folder] = append(b.uids[ref.Folder], ref.UID)
		b.ids[ref] = id
	}
	for _, f := range b.folders {
		if _, err := s.selectFolder(f, true); err != nil {
			b.failAll(f, err.Error())
			continue
		}
		data, err := s.c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(b.uids[f]...)}}, nil).Wait()
		if err != nil {
			b.failAll(f, err.Error())
			continue
		}
		found := data.AllUIDs()
		var keep []imap.UID
		for _, u := range b.uids[f] {
			if slices.Contains(found, u) {
				keep = append(keep, u)
			} else {
				b.fail(b.ids[MessageRef{f, u}], "no such message")
			}
		}
		b.uids[f] = keep
	}
	return b, nil
}

func (b *batch) fail(id, reason string) {
	b.res.Failed = append(b.res.Failed, Failure{ID: id, Reason: reason})
}

func (b *batch) failAll(folder, reason string) {
	for _, u := range b.uids[folder] {
		b.fail(b.ids[MessageRef{folder, u}], reason)
	}
	b.uids[folder] = nil
}

func (b *batch) done(folder string) {
	for _, u := range b.uids[folder] {
		b.res.Changed = append(b.res.Changed, b.ids[MessageRef{folder, u}])
	}
}

// each runs fn once for each folder that has messages, with the folder
// selected for write.
func (s *session) each(b *batch, fn func(folder string, uids []imap.UID) error) UpdateResult {
	for _, f := range b.folders {
		if len(b.uids[f]) == 0 {
			continue
		}
		if _, err := s.selectFolder(f, true); err != nil {
			b.failAll(f, err.Error())
			continue
		}
		if err := fn(f, b.uids[f]); err != nil {
			b.failAll(f, err.Error())
			continue
		}
		b.done(f)
	}
	return b.res
}

// update opens a session, groups the ids, and runs fn for each folder.
func (m *Mailbox) update(ctx context.Context, ids []string, fn func(s *session, folder string, uids []imap.UID) error) (UpdateResult, error) {
	s, err := m.open(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	defer s.close()
	b, err := s.group(ids)
	if err != nil {
		return UpdateResult{}, err
	}
	return s.each(b, func(f string, uids []imap.UID) error { return fn(s, f, uids) }), nil
}

// Mark sets or clears the read and starred flags.
func (m *Mailbox) Mark(ctx context.Context, in MarkInput) (UpdateResult, error) {
	if in.Read == nil && in.Starred == nil {
		return UpdateResult{}, errors.New("give read, starred, or both")
	}
	return m.update(ctx, in.IDs, func(s *session, _ string, uids []imap.UID) error {
		for _, ch := range []struct {
			v    *bool
			flag imap.Flag
		}{{in.Read, imap.FlagSeen}, {in.Starred, imap.FlagFlagged}} {
			if ch.v == nil {
				continue
			}
			op := imap.StoreFlagsDel
			if *ch.v {
				op = imap.StoreFlagsAdd
			}
			if err := s.store(uids, op, ch.flag); err != nil {
				return err
			}
		}
		return nil
	})
}

// Label adds and removes labels. A server with no labels has folders
// instead: add copies the messages to the folder, and remove is an error.
func (m *Mailbox) Label(ctx context.Context, in LabelInput) (UpdateResult, error) {
	if len(in.Add)+len(in.Remove) == 0 {
		return UpdateResult{}, errors.New("give add, remove, or both")
	}
	s, err := m.open(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	defer s.close()
	if s.isGmail() {
		return s.gmailLabels(in)
	}
	if len(in.Remove) > 0 {
		return UpdateResult{}, errors.New("remove: this mail server has folders, not labels; use archive or trash to take a message out of a folder")
	}
	if err := s.needFolders(in.Add, in.Create); err != nil {
		return UpdateResult{}, err
	}
	b, err := s.group(in.IDs)
	if err != nil {
		return UpdateResult{}, err
	}
	return s.each(b, func(_ string, uids []imap.UID) error {
		for _, dest := range in.Add {
			if _, err := s.c.Copy(imap.UIDSetNum(uids...), dest).Wait(); err != nil {
				return fmt.Errorf("copy to %q: %w", dest, err)
			}
		}
		return nil
	}), nil
}

// gmailLabels adds and removes Gmail labels. Inbox, Starred, Important,
// and the other system labels need no folder.
func (s *session) gmailLabels(in LabelInput) (UpdateResult, error) {
	var user []string
	labels := func(names []string) []string {
		out := make([]string, 0, len(names))
		for _, n := range names {
			raw, system := gmailLabel(strings.TrimSpace(n))
			if !system {
				user = append(user, raw)
			}
			out = append(out, raw)
		}
		return out
	}
	add, remove := labels(in.Add), labels(in.Remove)
	if err := s.needFolders(user, in.Create); err != nil {
		return UpdateResult{}, err
	}
	b, err := s.group(in.IDs)
	if err != nil {
		return UpdateResult{}, err
	}
	g, err := s.gmail()
	if err != nil {
		return UpdateResult{}, err
	}
	return s.each(b, func(f string, uids []imap.UID) error {
		if err := g.selectFolder(f, true); err != nil {
			return err
		}
		if len(add) > 0 {
			if err := g.storeLabels(uids, true, add); err != nil {
				return err
			}
		}
		if len(remove) > 0 {
			return g.storeLabels(uids, false, remove)
		}
		return nil
	}), nil
}

// needFolders checks that each folder exists. With create, it makes the
// missing ones.
func (s *session) needFolders(names []string, create bool) error {
	list, err := s.folders()
	if err != nil {
		return err
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return errors.New("a label name is empty")
		}
		if slices.ContainsFunc(list, func(l *imap.ListData) bool { return l.Mailbox == n }) {
			continue
		}
		if !create {
			return fmt.Errorf("no label %q: use folders to see the labels, or set create to make it", n)
		}
		if err := s.c.Create(n, nil).Wait(); err != nil {
			return fmt.Errorf("make label %q: %w", n, err)
		}
		s.list = nil
	}
	return nil
}

// Archive takes messages out of the inbox. A server with no labels moves
// them to the \Archive folder.
func (m *Mailbox) Archive(ctx context.Context, ids []string) (UpdateResult, error) {
	s, err := m.open(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	defer s.close()
	if s.isGmail() {
		return s.gmailLabels(LabelInput{IDs: ids, Remove: []string{"Inbox"}})
	}
	dest := s.special(imap.MailboxAttrArchive)
	if dest == "" {
		return UpdateResult{}, errors.New("this mail server has no archive folder: use label to move the messages to a folder")
	}
	return s.moveAll(ids, dest)
}

// Trash moves messages to the \Trash folder.
func (m *Mailbox) Trash(ctx context.Context, ids []string) (UpdateResult, error) {
	s, err := m.open(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	defer s.close()
	dest := s.special(imap.MailboxAttrTrash)
	if dest == "" {
		return UpdateResult{}, errors.New("this mail server has no trash folder")
	}
	return s.moveAll(ids, dest)
}

func (s *session) moveAll(ids []string, dest string) (UpdateResult, error) {
	b, err := s.group(ids)
	if err != nil {
		return UpdateResult{}, err
	}
	return s.each(b, func(f string, uids []imap.UID) error {
		if f == dest {
			return nil
		}
		return s.move(uids, dest)
	}), nil
}

// Folder is one row of the folders operation.
type Folder struct {
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
	Total  uint32 `json:"total"`
	Unread uint32 `json:"unread"`
}

// roles maps the special-use attributes to the role names.
var roles = map[imap.MailboxAttr]string{
	imap.MailboxAttrAll:     "all",
	imap.MailboxAttrSent:    "sent",
	imap.MailboxAttrDrafts:  "drafts",
	imap.MailboxAttrTrash:   "trash",
	imap.MailboxAttrJunk:    "spam",
	imap.MailboxAttrArchive: "archive",
	imap.MailboxAttrFlagged: "starred",
}

// Folders lists the folders with their role and counts.
func (m *Mailbox) Folders(ctx context.Context) ([]Folder, error) {
	s, err := m.open(ctx)
	if err != nil {
		return nil, err
	}
	defer s.close()
	list, err := s.folders()
	if err != nil {
		return nil, err
	}
	out := []Folder{}
	for _, l := range list {
		if slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) || slices.Contains(l.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		f := Folder{Name: l.Mailbox}
		if strings.EqualFold(l.Mailbox, "INBOX") {
			f.Role = "inbox"
		}
		for _, a := range l.Attrs {
			if r, ok := roles[a]; ok {
				f.Role = r
			}
		}
		if st, err := s.status(l.Mailbox); err == nil {
			if st.NumMessages != nil {
				f.Total = *st.NumMessages
			}
			if st.NumUnseen != nil {
				f.Unread = *st.NumUnseen
			}
		}
		out = append(out, f)
	}
	return out, nil
}
