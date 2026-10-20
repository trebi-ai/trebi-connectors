package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/spec"
)

// runMode loads the config as main does and runs the CLI.
func runMode(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	app := newApp(cfg, s)
	app.Writer = &buf
	err = app.Run(context.Background(), append([]string{"slack-cli"}, args...))
	return buf.String(), err
}

// writeConfig saves a config with one workspace at path.
func writeConfig(t *testing.T, path, name string) {
	t.Helper()
	t.Setenv("SLACK_CLI_CONFIG", path)
	os.Unsetenv("TREBI_STATE_DIR") //nolint:errcheck // t.Setenv restores it
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	c.Set(name, config.Workspace{Token: "xoxb-" + name, TeamID: "T" + name})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}

// home makes a HOME and a SLACK_CLI_CONFIG file that each hold a workspace.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	for _, k := range []string{"TREBI_STATE_DIR", "TREBI_CACHE_DIR", "SLACK_WORKSPACE"} {
		t.Setenv(k, "")
		os.Unsetenv(k) //nolint:errcheck // t.Setenv restores it
	}
	writeConfig(t, filepath.Join(h, ".cli-tools", "slack-cli", "config.toml"), "fromhome")
	writeConfig(t, filepath.Join(h, "other.toml"), "fromenv")
	return h
}

func TestTrebiMode(t *testing.T) {
	home(t)
	state := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", state)

	out, err := runMode(t, "auth", "list")
	if err != nil || strings.Contains(out, "fromhome") || strings.Contains(out, "fromenv") || !strings.Contains(out, "log in to Slack in Trebi") {
		t.Fatalf("auth list must not read the home or SLACK_CLI_CONFIG store: %q %v", out, err)
	}
	if _, err := runMode(t, "auth", "show"); err == nil || !strings.Contains(err.Error(), "log in to Slack in Trebi") {
		t.Fatalf("auth show with an empty state: %v", err)
	}
	for _, args := range [][]string{
		{"auth", "import"},
		{"auth", "slk", "--dir", t.TempDir()},
		{"auth", "add", "--token", "xoxb-1"},
		{"auth", "use", "x"},
		{"auth", "remove", "x"},
	} {
		if _, err := runMode(t, args...); err == nil || err.Error() != "Set this value in Trebi" {
			t.Fatalf("%v in Trebi mode: %v", args, err)
		}
	}

	t.Setenv("TREBI_STATE_DIR", "")
	writeConfig(t, filepath.Join(state, "config.toml"), "fromstate")
	t.Setenv("TREBI_STATE_DIR", state)
	out, err = runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "Name:      fromstate") || !strings.Contains(out, filepath.Join(state, "config.toml")) {
		t.Fatalf("auth show must read the state store: %q %v", out, err)
	}
	if got := config.CacheDir(); got != state {
		t.Fatalf("cache folder with no TREBI_CACHE_DIR = %q, want %q", got, state)
	}
}

func TestStandalone(t *testing.T) {
	h := home(t)
	out, err := runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "Name:      fromenv") {
		t.Fatalf("SLACK_CLI_CONFIG wins outside Trebi: %q %v", out, err)
	}
	os.Unsetenv("SLACK_CLI_CONFIG") //nolint:errcheck // t.Setenv restores it
	out, err = runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "Name:      fromhome") {
		t.Fatalf("the home store outside Trebi: %q %v", out, err)
	}
	if got, want := config.CacheDir(), filepath.Join(h, ".cli-tools", "slack-cli"); got != want {
		t.Fatalf("cache folder = %q, want %q", got, want)
	}
}
