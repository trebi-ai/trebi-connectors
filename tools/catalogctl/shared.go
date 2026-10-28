package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SharedDir is the folder in catalog/ that holds the shared event schemas.
// It is not an entry.
const SharedDir = "schemas"

// SharedSchemas maps an event type to the bytes of its shared schema.
type SharedSchemas map[string][]byte

// LoadSharedSchemas reads catalog/schemas/<type>.json. A missing folder
// gives no shared schemas.
func LoadSharedSchemas(root string) (SharedSchemas, error) {
	dir := filepath.Join(root, "catalog", SharedDir)
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return SharedSchemas{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := SharedSchemas{}
	for _, d := range des {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, d.Name()))
		if err != nil {
			return nil, err
		}
		out[strings.TrimSuffix(d.Name(), ".json")] = b
	}
	return out, nil
}

// Check finds each event type of the entry that has a shared schema. The
// schema file of that type must be a byte-identical copy.
func (s SharedSchemas) Check(e *Entry) []error {
	ev := e.Manifest.Events
	if ev == nil {
		return nil
	}
	var errs []error
	for _, t := range ev.Types {
		want, ok := s[t.Type]
		if !ok {
			continue
		}
		shared := "catalog/" + SharedDir + "/" + t.Type + ".json"
		if t.Schema == "" {
			errs = append(errs, fmt.Errorf("events.types[%s]: set schema to a copy of %s", t.Type, shared))
			continue
		}
		got, err := os.ReadFile(filepath.Join(e.Dir, t.Schema))
		if err != nil {
			continue // Validate reports a missing file
		}
		if !bytes.Equal(got, want) {
			errs = append(errs, fmt.Errorf("events.types[%s].schema: %s differs from %s; copy the shared file", t.Type, t.Schema, shared))
		}
	}
	return errs
}
