package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/watch"
)

// FeedsFile keeps the seen entry ids of each feed in the state folder.
const FeedsFile = "web-feeds.json"

const (
	feedsVersion = 1
	// minSeen is the least count of ids that a feed keeps.
	minSeen = 500
)

type feedsDoc struct {
	Version int                   `json:"version"`
	Feeds   map[string]*feedEntry `json:"feeds"`
}

type feedEntry struct {
	Seen []string `json:"seen"` // newest last
}

// feedStore keeps the seen entry ids of each feed. With no folder it keeps
// them in memory.
type feedStore struct {
	path string
	doc  feedsDoc
}

func loadFeeds(dir string) (*feedStore, error) {
	fs := &feedStore{doc: feedsDoc{Version: feedsVersion, Feeds: map[string]*feedEntry{}}}
	if dir == "" {
		return fs, nil
	}
	fs.path = filepath.Join(dir, FeedsFile)
	b, err := os.ReadFile(fs.path)
	if errors.Is(err, os.ErrNotExist) {
		return fs, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &fs.doc); err != nil {
		return nil, fmt.Errorf("read %s: %w", FeedsFile, err)
	}
	if fs.doc.Version > feedsVersion {
		return nil, fmt.Errorf("%s has format %d; this program reads format %d or older", FeedsFile, fs.doc.Version, feedsVersion)
	}
	fs.doc.Version = feedsVersion
	if fs.doc.Feeds == nil {
		fs.doc.Feeds = map[string]*feedEntry{}
	}
	return fs, nil
}

// unseen returns the entries that the store has not seen. primed is false
// on the first read of the feed.
func (fs *feedStore) unseen(url string, entries []watch.Entry) (fresh []watch.Entry, primed bool) {
	f := fs.doc.Feeds[url]
	if f == nil {
		return nil, false
	}
	for _, e := range entries {
		if !slices.Contains(f.Seen, e.ID) {
			fresh = append(fresh, e)
		}
	}
	return fresh, true
}

// mark adds the ids of entries and saves the store. It keeps the newest
// ids: at least minSeen, and two times the feed size.
func (fs *feedStore) mark(url string, entries []watch.Entry) error {
	f := fs.doc.Feeds[url]
	if f == nil {
		f = &feedEntry{Seen: []string{}}
		fs.doc.Feeds[url] = f
	}
	changed := false
	for i := len(entries) - 1; i >= 0; i-- {
		if id := entries[i].ID; !slices.Contains(f.Seen, id) {
			f.Seen = append(f.Seen, id)
			changed = true
		}
	}
	if keep := max(minSeen, 2*len(entries)); len(f.Seen) > keep {
		f.Seen = slices.Clone(f.Seen[len(f.Seen)-keep:])
	}
	if !changed && len(f.Seen) > 0 {
		return nil
	}
	return fs.save()
}

func (fs *feedStore) save() error {
	if fs.path == "" {
		return nil
	}
	b, err := json.Marshal(fs.doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fs.path), 0o700); err != nil {
		return err
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}
