package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mailManifest = goodManifest + `events:
  protocol: trebi-connector/1
  command: demo serve
  types: [{type: mail.received, schema: schemas/mail.received.json}]
`

func TestSharedSchemas(t *testing.T) {
	const shared = `{"type": "object"}` + "\n"
	for _, tc := range []struct {
		name, manifest, copy, want string
	}{
		{"same bytes", mailManifest, shared, ""},
		{"other bytes", mailManifest, `{"type":"object"}` + "\n", "differs from catalog/schemas/mail.received.json"},
		{"no schema", goodManifest + "events:\n  protocol: trebi-connector/1\n  command: demo serve\n  types: [{type: mail.received}]\n", "", "set schema to a copy"},
		{"other type", goodManifest + "events:\n  protocol: trebi-connector/1\n  command: demo serve\n  types: [{type: message, schema: schemas/mail.received.json}]\n", "{}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := writeEntry(t, root, "demo", tc.manifest, goodSkill)
			write(t, filepath.Join(root, "catalog", SharedDir, "mail.received.json"), shared)
			if tc.copy != "" {
				write(t, filepath.Join(dir, "schemas", "mail.received.json"), tc.copy)
			}
			s, err := LoadSharedSchemas(root)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := loadEntries(root, nil)
			if err != nil {
				t.Fatal(err) // the shared folder is not an entry
			}
			if len(entries) != 1 {
				t.Fatalf("entries %d", len(entries))
			}
			errs := s.Check(entries[0])
			switch {
			case tc.want == "" && len(errs) > 0:
				t.Fatalf("errors %v", errs)
			case tc.want != "" && (len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.want)):
				t.Fatalf("errors %v, want %q", errs, tc.want)
			}
		})
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
