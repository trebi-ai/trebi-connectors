package watch_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/watch"
)

func TestFileStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	f := watch.NewFileStore(dir)
	if _, ok, err := f.Load(); ok || err != nil {
		t.Fatalf("empty: ok %v err %v", ok, err)
	}
	if err := f.Save(watch.Cursor{UIDValidity: 7, LastUID: 42}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := watch.NewFileStore(dir).Load()
	if !ok || err != nil || c.UIDValidity != 7 || c.LastUID != 42 {
		t.Fatalf("cursor %+v ok %v err %v", c, ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cursor.json"), []byte(`{"version":99,"uidvalidity":7,"last_uid":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Load(); !errors.Is(err, watch.ErrNewerState) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cursor.json")); err != nil {
		t.Fatal("a newer state was deleted")
	}
}
