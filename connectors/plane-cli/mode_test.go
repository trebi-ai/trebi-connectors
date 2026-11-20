package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
)

// runMode runs the CLI as main does and returns what it prints on stdout.
func runMode(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r) //nolint:errcheck // the test reads what it gets
		done <- string(b)
	}()
	err = run(context.Background(), append([]string{"plane-cli"}, args...))
	os.Stdout = stdout
	w.Close() //nolint:errcheck // a pipe
	return <-done, err
}

// writeConfig saves a config with one API key at path.
func writeConfig(t *testing.T, path, key string) {
	t.Helper()
	t.Setenv("PLANE_CONFIG", path)
	os.Unsetenv("TREBI_STATE_DIR") //nolint:errcheck // t.Setenv restores it
	c, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	c.APIKey, c.Workspace = key, "fromfile"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}

// home makes a HOME and a PLANE_CONFIG file that each hold a key.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	for _, k := range []string{"TREBI_STATE_DIR", "TREBI_CACHE_DIR", "PLANE_API_KEY", "PLANE_HOST", "PLANE_WORKSPACE", "PLANE_PROJECT", "PLANE_PROJECTS"} {
		t.Setenv(k, "")
		os.Unsetenv(k) //nolint:errcheck // t.Setenv restores it
	}
	writeConfig(t, filepath.Join(h, ".cli-tools", "plane-cli", "config.toml"), "key-from-home")
	writeConfig(t, filepath.Join(h, "other.toml"), "key-from-env")
	return h
}

func TestTrebiMode(t *testing.T) {
	home(t)
	state := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", state)

	out, err := runMode(t, "auth", "show")
	if err != nil || strings.Contains(out, "home") || strings.Contains(out, "-env") || strings.Contains(out, "fromfile") || !strings.Contains(out, "API key:     \n") {
		t.Fatalf("auth show must not read the home or PLANE_CONFIG file: %q %v", out, err)
	}
	if _, err := runMode(t, "auth", "status"); err == nil || !strings.Contains(err.Error(), "set the API key in Trebi") {
		t.Fatalf("auth status with no key: %v", err)
	}
	for _, args := range [][]string{
		{"auth", "login", "--api-key", "k"},
		{"auth", "logout"},
		{"auth", "workspace", "add", "acme", "--api-key", "k", "--no-verify"},
		{"auth", "workspace", "remove", "acme"},
		{"config", "set", "workspace", "acme"},
	} {
		if _, err := runMode(t, args...); err == nil || err.Error() != "Set this value in Trebi" {
			t.Fatalf("%v in Trebi mode: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"--api-key", "k", "auth", "show"},
		{"--host", "https://plane.example.com", "auth", "show"},
	} {
		if _, err := runMode(t, args...); err == nil || err.Error() != "Trebi sets this value" {
			t.Fatalf("%v in Trebi mode: %v", args, err)
		}
	}

	t.Setenv("PLANE_API_KEY", "key-from-input")
	t.Setenv("PLANE_PROJECTS", "zeta/*, acme/ENG")
	out, err = runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "API key:     key-...nput") || !strings.Contains(out, "Workspace:   acme\n") {
		t.Fatalf("auth show must use the inputs: %q %v", out, err)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatalf("a CLI command wrote the state folder: %v", entries)
	}
}

func TestStandalone(t *testing.T) {
	home(t)
	out, err := runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "API key:     key-...-env") || !strings.Contains(out, "Workspace:   fromfile") {
		t.Fatalf("PLANE_CONFIG wins outside Trebi: %q %v", out, err)
	}
	os.Unsetenv("PLANE_CONFIG") //nolint:errcheck // t.Setenv restores it
	out, err = runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "API key:     key-...home") {
		t.Fatalf("the home file outside Trebi: %q %v", out, err)
	}
	out, err = runMode(t, "--api-key", "key-from-flag", "auth", "show")
	if err != nil || !strings.Contains(out, "API key:     key-...flag") {
		t.Fatalf("the flag wins outside Trebi: %q %v", out, err)
	}
	t.Setenv("PLANE_API_KEY", "key-from-envvars")
	out, err = runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "API key:     key-...vars") {
		t.Fatalf("PLANE_API_KEY wins over the file: %q %v", out, err)
	}
}
