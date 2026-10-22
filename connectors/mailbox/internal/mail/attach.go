package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/emersion/go-message/mail"
)

// AttachmentInput selects one attachment of a message: by name, by its
// 1-based position in the attachments of read, or by both.
type AttachmentInput struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Index int    `json:"index,omitempty"`
}

// SavedAttachment is an attachment saved on this computer.
type SavedAttachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SaveAttachment saves one attachment in <state folder>/attachments/<id>/
// and returns its full path. A second call writes the file again.
func (m *Mailbox) SaveAttachment(ctx context.Context, in AttachmentInput) (SavedAttachment, error) {
	if in.Name == "" && in.Index < 1 {
		return SavedAttachment{}, errors.New("give name or index (1 is the first attachment)")
	}
	if m.cfg.StateDir == "" {
		return SavedAttachment{}, errors.New("no folder to save the file in")
	}
	s, err := m.open(ctx)
	if err != nil {
		return SavedAttachment{}, err
	}
	defer s.close()
	raw, err := s.raw(in.ID)
	if err != nil {
		return SavedAttachment{}, err
	}
	att, data, err := findAttachment(raw, in.Name, in.Index)
	if err != nil {
		return SavedAttachment{}, fmt.Errorf("message %s: %w", in.ID, err)
	}
	dir := filepath.Join(m.cfg.StateDir, "attachments", safeName(in.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SavedAttachment{}, err
	}
	name := safeName(att.Name)
	if in.Index > 0 {
		name = fmt.Sprintf("%d-%s", in.Index, name) // two parts can share a name
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return SavedAttachment{}, fmt.Errorf("save attachment: %w", err)
	}
	return SavedAttachment{Path: p, Name: att.Name, Type: att.Type, Size: int64(len(data))}, nil
}

// findAttachment returns the attachment with the name or the 1-based
// index, in the order of parse.
func findAttachment(raw []byte, name string, index int) (Attachment, []byte, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return Attachment{}, nil, err
	}
	defer r.Close() //nolint:errcheck // a read-only reader
	n := 0
	var names []string
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Attachment{}, nil, err
		}
		h, ok := p.Header.(*mail.AttachmentHeader)
		if !ok {
			continue
		}
		n++
		fn, _ := h.Filename() //nolint:errcheck // a bad name is an empty name
		names = append(names, fn)
		if (index > 0 && n != index) || (name != "" && fn != name) {
			continue
		}
		ct, _, _ := h.ContentType() //nolint:errcheck // a bad type is empty
		data, err := io.ReadAll(io.LimitReader(p.Body, maxMessage+1))
		if err != nil {
			return Attachment{}, nil, fmt.Errorf("read attachment: %w", err)
		}
		return Attachment{Name: fn, Type: ct, Size: int64(len(data))}, data, nil
	}
	if len(names) == 0 {
		return Attachment{}, nil, errors.New("it has no attachments")
	}
	return Attachment{}, nil, fmt.Errorf("no attachment matches; the attachments are %q", names)
}

// safeName makes a file name from a message id or an attachment name.
func safeName(s string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "_"), "._")
	if s == "" {
		return "attachment"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// readAttachFile reads an attachment of a new message from a file.
func readAttachFile(p string) ([]byte, error) {
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("path %q: give a full path", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("path %q: %w", p, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("path %q is not a file", p)
	}
	if st.Size() > maxMessage {
		return nil, fmt.Errorf("path %q is %d MB; the limit is 25 MB", p, st.Size()>>20)
	}
	return os.ReadFile(p)
}
