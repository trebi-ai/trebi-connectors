package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run runs the program in-process and returns its stdout.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := newApp().Run(append([]string{"discord-cli"}, args...))
	os.Stdout = stdout
	w.Close() //nolint:errcheck // a pipe close does not fail
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

// home makes a HOME with a config file and a .env file that hold tokens,
// and makes it the working folder.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	cfg := filepath.Join(h, ".cli-tools", "discord-cli")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"token":"from-config-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".env"), []byte("DISCORD_BOT_TOKEN=from-dotenv-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(h)
	for _, k := range []string{"TREBI_STATE_DIR", "TREBI_CACHE_DIR", "DISCORD_TOKEN", "DISCORD_BOT_TOKEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k) //nolint:errcheck // t.Setenv restores it
	}
	return h
}

func TestTrebiMode(t *testing.T) {
	home(t)
	t.Setenv("TREBI_STATE_DIR", t.TempDir())
	t.Setenv("DISCORD_BOT_TOKEN", "from-second-env-name")

	out, err := run(t, "auth", "show")
	if err != nil || !strings.Contains(out, "No token configured") {
		t.Fatalf("auth show must not find the home, .env, or second env name token: %q %v", out, err)
	}
	if _, err := run(t, "--token", "from-flag", "auth", "show"); err == nil || !strings.Contains(err.Error(), "Trebi sets this value") {
		t.Fatalf("--token in Trebi mode: %v", err)
	}
	if _, err := run(t, "auth", "set", "new-token"); err == nil || err.Error() != "Set this value in Trebi" {
		t.Fatalf("auth set in Trebi mode: %v", err)
	}
	if _, err := run(t, "auth", "test"); err == nil || err.Error() != "Bot token is not set in Trebi" {
		t.Fatalf("auth test with no input: %v", err)
	}

	t.Setenv("DISCORD_TOKEN", "from-trebi-input")
	out, err = run(t, "auth", "show")
	if err != nil || !strings.Contains(out, "Source: Trebi (DISCORD_TOKEN)") {
		t.Fatalf("auth show with the input: %q %v", out, err)
	}
}

func TestStandalone(t *testing.T) {
	h := home(t)
	source := func(args ...string) string {
		t.Helper()
		out, err := run(t, append(args, "auth", "show")...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := source(); !strings.Contains(out, ".env ("+filepath.Join(h, ".env")+")") {
		t.Fatalf("the .env file wins over the config file: %q", out)
	}
	os.Remove(filepath.Join(h, ".env")) //nolint:errcheck // the next check fails when it stays
	if out := source(); !strings.Contains(out, "config (") {
		t.Fatalf("the config file: %q", out)
	}
	t.Setenv("DISCORD_BOT_TOKEN", "from-second-env-name")
	if out := source(); !strings.Contains(out, "env (DISCORD_BOT_TOKEN)") {
		t.Fatalf("DISCORD_BOT_TOKEN: %q", out)
	}
	t.Setenv("DISCORD_TOKEN", "from-first-env-name")
	if out := source(); !strings.Contains(out, "env (DISCORD_TOKEN)") {
		t.Fatalf("DISCORD_TOKEN: %q", out)
	}
	if out := source("--token", "from-flag-value"); !strings.Contains(out, "flag (--token)") {
		t.Fatalf("--token: %q", out)
	}

	if _, err := run(t, "auth", "set", "saved-token"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(h, ".cli-tools", "discord-cli", "config.json"))
	if err != nil || !strings.Contains(string(b), "saved-token") {
		t.Fatalf("auth set must write the config file: %s %v", b, err)
	}
}
