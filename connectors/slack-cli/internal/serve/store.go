package serve

import (
	"maps"
	"sync"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
)

// Store keeps the workspaces of the connection in the config file. The
// login and the token refresh both save it, so one lock guards it.
type Store struct {
	mu  sync.Mutex
	cfg *config.Config
}

// NewStore wraps a loaded config.
func NewStore(cfg *config.Config) *Store { return &Store{cfg: cfg} }

// Workspaces returns a copy of the saved workspaces by name.
func (s *Store) Workspaces() map[string]config.Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.cfg.Workspaces)
}

// Names returns the workspace names in order.
func (s *Store) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Names()
}

// Add saves w under the name of its team and returns the name. The first
// workspace becomes the default.
func (s *Store) Add(w config.Workspace) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.cfg.NameFor(w)
	s.cfg.Set(name, w)
	if s.cfg.Workspace == "" {
		s.cfg.Workspace = name
	}
	return name, s.cfg.Save()
}

// Put saves w under name.
func (s *Store) Put(name string, w config.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Set(name, w)
	return s.cfg.Save()
}

// Remove deletes the named workspaces. An empty list deletes all. When the
// default goes, the first workspace that stays is the new default.
func (s *Store) Remove(names ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(names) == 0 {
		names = s.cfg.Names()
	}
	for _, n := range names {
		s.cfg.Remove(n)
	}
	if left := s.cfg.Names(); s.cfg.Workspace == "" && len(left) > 0 {
		s.cfg.Workspace = left[0]
	}
	return s.cfg.Save()
}
