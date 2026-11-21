package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/skill"
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
	for _, k := range []string{"TREBI_STATE_DIR", "TREBI_CACHE_DIR", "PLANE_API_KEY", "PLANE_HOST", "PLANE_WORKSPACE", "PLANE_PROJECT", "PLANE_PROJECTS", "PLANE_MANAGED_BY"} {
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

	for _, cmd := range []string{"listen", "serve"} {
		if err := config.RefuseLongRun(cmd); err != nil {
			t.Fatalf("Trebi mode must keep %s: %v", cmd, err)
		}
	}
	if config.ManagedBy() != "Trebi" {
		t.Fatalf("ManagedBy in Trebi mode: %q", config.ManagedBy())
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

// managed puts the CLI in managed mode, as OverSire does.
func managed(t *testing.T) {
	t.Helper()
	home(t)
	t.Setenv("PLANE_MANAGED_BY", "OverSire")
}

func TestManagedRefusesKeyAndHostFlags(t *testing.T) {
	managed(t)
	for _, args := range [][]string{
		{"--api-key", "k", "auth", "show"},
		{"-k", "k", "auth", "show"},
		{"--host", "https://plane.example.com", "auth", "show"},
	} {
		if _, err := runMode(t, args...); err == nil || err.Error() != "OverSire sets this value" {
			t.Fatalf("%v in managed mode: %v", args, err)
		}
	}
}

func TestManagedReadsNoConfigFile(t *testing.T) {
	managed(t)
	out, err := runMode(t, "auth", "show")
	if err != nil || strings.Contains(out, "home") || strings.Contains(out, "-env") || strings.Contains(out, "fromfile") {
		t.Fatalf("auth show must not read the home or PLANE_CONFIG file: %q %v", out, err)
	}
	if _, err := runMode(t, "auth", "status"); err == nil || !strings.Contains(err.Error(), "set the API key in OverSire") {
		t.Fatalf("auth status with no key: %v", err)
	}
	t.Setenv("PLANE_API_KEY", "key-from-managed")
	out, err = runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "API key:     key-...aged") {
		t.Fatalf("the env key must win: %q %v", out, err)
	}
}

func TestManagedRefusesSaves(t *testing.T) {
	managed(t)
	h := os.Getenv("HOME")
	before, err := os.ReadFile(filepath.Join(h, ".cli-tools", "plane-cli", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"auth", "login", "--api-key", "k"},
		{"auth", "logout"},
		{"auth", "workspace", "add", "acme", "--api-key", "k", "--no-verify"},
		{"auth", "workspace", "remove", "acme"},
		{"config", "set", "workspace", "acme"},
	} {
		if _, err := runMode(t, args...); err == nil || err.Error() != "Set this value in OverSire" {
			t.Fatalf("%v in managed mode: %v", args, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(h, ".cli-tools", "plane-cli", "config.toml"))
	if string(before) != string(after) {
		t.Fatal("a managed command changed the config file")
	}
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err == nil || c.File() != "" {
		t.Fatalf("Save must refuse with an empty path: %v %q", err, c.File())
	}
}

func TestManagedRefusesListenAndServe(t *testing.T) {
	managed(t)
	for _, name := range []string{"listen", "serve"} {
		if _, err := runMode(t, name); err == nil || err.Error() != "OverSire does not run "+name {
			t.Fatalf("%s in managed mode: %v", name, err)
		}
	}
}

func TestManagedWorkspaceAndProjectFromEnv(t *testing.T) {
	managed(t)
	t.Setenv("PLANE_API_KEY", "key-from-managed")
	t.Setenv("PLANE_HOST", "https://plane.example.com")
	t.Setenv("PLANE_WORKSPACE", "acme")
	t.Setenv("PLANE_PROJECT", "ENG")
	out, err := runMode(t, "auth", "show")
	if err != nil || !strings.Contains(out, "Workspace:   acme\n") || !strings.Contains(out, "Project:     ENG\n") || !strings.Contains(out, "https://plane.example.com") {
		t.Fatalf("auth show must use the env: %q %v", out, err)
	}
	out, err = runMode(t, "-w", "beta", "-p", "OPS", "auth", "show")
	if err != nil || !strings.Contains(out, "Workspace:   beta\n") || !strings.Contains(out, "Project:     OPS\n") {
		t.Fatalf("--workspace and --project stay open: %q %v", out, err)
	}
}

func TestSkillStandalone(t *testing.T) {
	home(t)
	out, err := runMode(t, "skill")
	if err != nil || !strings.Contains(out, "auth login") || !strings.Contains(out, "## Trebi connector") || strings.Contains(out, "<!-- managed") || strings.HasPrefix(out, "---") {
		t.Fatalf("standalone skill: %v\n%s", err, out)
	}
}

func TestSkillManaged(t *testing.T) {
	managed(t)
	out, err := runMode(t, "skill")
	if err != nil || !strings.Contains(out, "Managed mode (OverSire)") || !strings.Contains(out, "## Identifiers") {
		t.Fatalf("managed skill: %v\n%s", err, out)
	}
	for _, s := range []string{"auth login", "config.toml", "PLANE_CONFIG", "--api-key", "## Listen to changes", "## Trebi connector"} {
		if strings.Contains(out, s) {
			t.Errorf("managed skill has %q", s)
		}
	}
}

func TestSkillTrebiMode(t *testing.T) {
	home(t)
	state := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", state)
	t.Setenv("PLANE_MANAGED_BY", "OverSire")
	out, err := runMode(t, "skill")
	if err != nil || !strings.Contains(out, "auth login") || !strings.Contains(out, "## Trebi connector") {
		t.Fatalf("Trebi mode must print the full text: %v\n%s", err, out)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatalf("skill wrote the state folder: %v", entries)
	}
}

func TestSkillRaw(t *testing.T) {
	managed(t)
	out, err := runMode(t, "skill", "--raw")
	if err != nil || out != string(skill.Raw()) || !strings.HasPrefix(out, "---\n") {
		t.Fatalf("skill --raw: %v", err)
	}
}
