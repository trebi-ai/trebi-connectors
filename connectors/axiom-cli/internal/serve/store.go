package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// MonitorsFile keeps the history cursor of each monitor in the state folder.
const MonitorsFile = "axiom-monitors.json"

const monitorsVersion = 1

type monitorsDoc struct {
	Version  int                `json:"version"`
	Monitors map[string]*cursor `json:"monitors"`
}

// cursor is the place in the history of one monitor.
type cursor struct {
	// Since is the time of the newest sent entry, or of the first sight.
	Since string `json:"since"`
	// Seen holds the keys of the sent entries at Since.
	Seen []string `json:"seen,omitempty"`
	// State is the state of the newest sent entry: open or closed.
	State string `json:"state,omitempty"`
}

// store keeps the cursors. With no folder it keeps them in memory.
type store struct {
	path string
	doc  monitorsDoc
}

func loadStore(dir string) (*store, error) {
	s := &store{doc: monitorsDoc{Version: monitorsVersion, Monitors: map[string]*cursor{}}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s.path = filepath.Join(dir, MonitorsFile)
	os.Remove(s.path + ".tmp") //nolint:errcheck,gosec // a crash can leave it
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.doc); err != nil {
		return nil, fmt.Errorf("read %s: %w", MonitorsFile, err)
	}
	if s.doc.Version > monitorsVersion {
		return nil, fmt.Errorf("%s has format %d; this program reads format %d or older", MonitorsFile, s.doc.Version, monitorsVersion)
	}
	s.doc.Version = monitorsVersion
	if s.doc.Monitors == nil {
		s.doc.Monitors = map[string]*cursor{}
	}
	return s, nil
}

func (s *store) get(id string) *cursor {
	c := s.doc.Monitors[id]
	if c == nil {
		return nil
	}
	cp := *c
	cp.Seen = slices.Clone(c.Seen)
	return &cp
}

func (s *store) put(id string, c *cursor) error {
	cp := *c
	cp.Seen = slices.Clone(c.Seen)
	s.doc.Monitors[id] = &cp
	return s.save()
}

// keep drops the cursors of the monitors that are not in ids.
func (s *store) keep(ids []string) error {
	changed := false
	for id := range s.doc.Monitors {
		if !slices.Contains(ids, id) {
			delete(s.doc.Monitors, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.save()
}

// save writes the file.
func (s *store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.doc)
	if err != nil {
		return err
	}
	return sdk.WriteFileAtomic(s.path, b, 0o600)
}
