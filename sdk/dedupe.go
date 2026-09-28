package sdk

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SentFile is the dedupe store of messages/send in TREBI_STATE_DIR.
const SentFile = "trebi-sdk-sent.jsonl"

// defaultMaxSent bounds the keys the store keeps. The daemon retries a send
// within minutes, so the newest keys are the ones that matter.
const defaultMaxSent = 10000

// sentStore remembers the result of each send by key, so a second send with
// the same key returns the first result. With a path it appends one JSON
// line per key and compacts the file when it holds twice the bound.
type sentStore struct {
	path  string
	max   int
	mu    sync.Mutex
	byKey map[string]SendResult
	order []string
	lines int
}

type sentLine struct {
	Key    string     `json:"key"`
	Result SendResult `json:"result"`
}

// newSentStore loads the store in dir. An empty dir keeps it in memory.
func newSentStore(dir string, max int) (*sentStore, error) {
	s := &sentStore{max: max, byKey: map[string]SendResult{}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	s.path = filepath.Join(dir, SentFile)
	data, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), MaxLine)
	for sc.Scan() {
		var l sentLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Key == "" {
			continue // a torn last line after a crash
		}
		s.remember(l.Key, l.Result)
		s.lines++
	}
	return s, nil
}

func (s *sentStore) get(key string) (SendResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byKey[key]
	return r, ok
}

func (s *sentStore) put(key string, r SendResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remember(key, r)
	if s.path == "" {
		return nil
	}
	if s.lines+1 >= 2*s.max {
		return s.compact()
	}
	b, err := json.Marshal(sentLine{Key: key, Result: r})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close() //nolint:errcheck // the write error wins
		return err
	}
	s.lines++
	return f.Close()
}

// remember adds a key and drops the oldest keys over the bound.
func (s *sentStore) remember(key string, r SendResult) {
	if _, ok := s.byKey[key]; !ok {
		s.order = append(s.order, key)
	}
	s.byKey[key] = r
	for len(s.order) > s.max {
		delete(s.byKey, s.order[0])
		s.order = s.order[1:]
	}
}

// compact rewrites the file with the kept keys only.
func (s *sentStore) compact() error {
	var buf bytes.Buffer
	for _, k := range s.order {
		b, err := json.Marshal(sentLine{Key: k, Result: s.byKey[k]})
		if err != nil {
			return err
		}
		buf.Write(append(b, '\n'))
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.lines = len(s.order)
	return nil
}
