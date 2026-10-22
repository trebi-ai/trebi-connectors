package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// cursorVersion is the version of cursor.json that this program writes.
const cursorVersion = 1

// Cursor is the last message of the folder that the watcher sent.
type Cursor struct {
	Version     int    `json:"version"`
	UIDValidity uint32 `json:"uidvalidity"`
	LastUID     uint32 `json:"last_uid"`
}

// CursorStore keeps the cursor.
type CursorStore interface {
	// Load returns ok false when there is no cursor yet.
	Load() (c Cursor, ok bool, err error)
	Save(c Cursor) error
}

// ErrNewerState is a cursor from a newer version of the program.
var ErrNewerState = errors.New("the saved state is from a newer version of mailbox; update the program")

// FileStore keeps the cursor in cursor.json in a folder.
type FileStore struct{ path string }

// NewFileStore keeps the cursor in dir. It makes dir on the first save.
func NewFileStore(dir string) *FileStore {
	return &FileStore{path: filepath.Join(dir, "cursor.json")}
}

// Load reads the cursor. A newer version is ErrNewerState; the file stays
// as it is.
func (f *FileStore) Load() (Cursor, bool, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, fmt.Errorf("read cursor: %w", err)
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return Cursor{}, false, fmt.Errorf("parse cursor: %w", err)
	}
	if c.Version > cursorVersion {
		return Cursor{}, false, ErrNewerState
	}
	c.Version = cursorVersion
	return c, true, nil
}

// Save writes the cursor with a temp file and a rename.
func (f *FileStore) Save(c Cursor) error {
	c.Version = cursorVersion
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := sdk.WriteFileAtomic(f.path, b, 0o600); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	return nil
}

// MemStore keeps the cursor in memory, for a run with no state folder.
type MemStore struct {
	mu sync.Mutex
	c  Cursor
	ok bool
}

// Load returns the cursor.
func (m *MemStore) Load() (Cursor, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c, m.ok, nil
}

// Save keeps the cursor.
func (m *MemStore) Save(c Cursor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c, m.ok = c, true
	return nil
}
