package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeOf runs `doctor --json` with args and returns the store folder that
// the command used.
func storeOf(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := execute(append([]string{"doctor", "--json"}, args...))
	os.Stdout = stdout
	w.Close()               //nolint:errcheck // test pipe
	out, _ := io.ReadAll(r) //nolint:errcheck // test pipe
	if runErr != nil {
		return "", runErr
	}
	var rep struct {
		Data struct {
			StoreDir string `json:"store_dir"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("doctor output %q: %v", out, err)
	}
	return rep.Data.StoreDir, nil
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	_ = filepath.WalkDir(dir, func(p string, _ os.DirEntry, _ error) error { //nolint:errcheck // test walk
		if p != dir {
			names = append(names, p)
		}
		return nil
	})
	return names
}

func TestTrebiMode(t *testing.T) {
	state, home, other := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".whatsapp-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("WHATSAPP_CLI_STORE_DIR="+other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("TREBI_STATE_DIR", state)
	t.Setenv("TREBI_CACHE_DIR", t.TempDir())
	t.Setenv("WHATSAPP_CLI_STORE_DIR", other)

	dir, err := storeOf(t)
	if err != nil || dir != state {
		t.Fatalf("store %q %v, want %q", dir, err, state)
	}
	if _, err := os.Stat(filepath.Join(state, "whatsapp-cli.db")); err != nil {
		t.Fatal(err)
	}
	if got := entries(t, filepath.Join(home, ".whatsapp-cli")); len(got) != 0 {
		t.Fatalf("the home store changed: %v", got)
	}
	if got := entries(t, other); len(got) != 0 {
		t.Fatalf("WHATSAPP_CLI_STORE_DIR changed: %v", got)
	}
	if _, err := storeOf(t, "--store", other); err == nil || !strings.Contains(err.Error(), "Trebi sets this value") {
		t.Fatalf("--store in Trebi mode: %v", err)
	}
}

func TestStandalone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"TREBI_STATE_DIR", "TREBI_CACHE_DIR"} {
		t.Setenv(name, "")
		os.Unsetenv(name) //nolint:errcheck // t.Setenv restores it
	}
	t.Setenv("WHATSAPP_CLI_STORE_DIR", "")

	if dir, err := storeOf(t); err != nil || dir != filepath.Join(home, ".whatsapp-cli") {
		t.Fatalf("default store %q %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".whatsapp-cli", "whatsapp-cli.db")); err != nil {
		t.Fatal(err)
	}
	env := t.TempDir()
	t.Setenv("WHATSAPP_CLI_STORE_DIR", env)
	if dir, err := storeOf(t); err != nil || dir != env {
		t.Fatalf("env store %q %v", dir, err)
	}
	flag := t.TempDir()
	if dir, err := storeOf(t, "--store", flag); err != nil || dir != flag {
		t.Fatalf("flag store %q %v", dir, err)
	}
}
