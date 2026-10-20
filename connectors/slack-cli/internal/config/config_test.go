package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenKind(t *testing.T) {
	cases := map[string]string{
		"xoxp-1": "user",
		"xoxb-1": "bot",
		"xoxc-1": "session",
		"xapp-1": "app",
		"nope":   "",
		"":       "",
	}
	for tok, want := range cases {
		if got := TokenKind(tok); got != want {
			t.Errorf("TokenKind(%q) = %q, want %q", tok, got, want)
		}
	}
}

func TestSaveLoadRoundTripAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("SLACK_CLI_CONFIG", path)

	c := &Config{Workspace: "acme"}
	c.Set("acme", Workspace{Token: "xoxc-1", Cookie: "xoxd-1", TeamID: "T1", Domain: "acme", URL: "https://acme.slack.com/", Source: SourceDesktop})
	c.Set("beta", Workspace{Token: "xoxb-2", TeamID: "T2", Domain: "beta"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != "acme" || len(got.Workspaces) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Workspaces["acme"].Cookie != "xoxd-1" || got.Workspaces["acme"].Kind() != "session" {
		t.Errorf("acme workspace wrong: %+v", got.Workspaces["acme"])
	}
}

func TestSavePreservesModeOnExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("SLACK_CLI_CONFIG", path)
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	c.Set("a", Workspace{Token: "xoxb-1"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

func TestFind(t *testing.T) {
	c := &Config{}
	c.Set("acme", Workspace{TeamID: "T123", Domain: "acme-corp", TeamName: "Acme Corp"})
	for _, key := range []string{"acme", "T123", "acme-corp", "Acme Corp", "ACME-CORP"} {
		if n, _, ok := c.Find(key); !ok || n != "acme" {
			t.Errorf("Find(%q) = %q, %v", key, n, ok)
		}
	}
	if _, _, ok := c.Find("missing"); ok {
		t.Error("Find(missing) should fail")
	}
}

func TestSelect(t *testing.T) {
	c := &Config{}
	// Zero workspaces.
	if _, _, err := c.Select(""); err == nil {
		t.Error("empty config should error")
	}
	// One workspace: empty key picks it.
	c.Set("only", Workspace{Token: "xoxb-1"})
	if n, _, err := c.Select(""); err != nil || n != "only" {
		t.Errorf("single select = %q, %v", n, err)
	}
	// Two workspaces: empty key with no default errors.
	c.Set("two", Workspace{Token: "xoxb-2"})
	if _, _, err := c.Select(""); err == nil {
		t.Error("ambiguous select should error")
	}
	// Default resolves.
	c.Workspace = "two"
	if n, _, err := c.Select(""); err != nil || n != "two" {
		t.Errorf("default select = %q, %v", n, err)
	}
	// Explicit key.
	if n, _, err := c.Select("only"); err != nil || n != "only" {
		t.Errorf("explicit select = %q, %v", n, err)
	}
}

func TestRemoveClearsDefault(t *testing.T) {
	c := &Config{Workspace: "a"}
	c.Set("a", Workspace{})
	if !c.Remove("a") || c.Workspace != "" {
		t.Error("remove should clear default")
	}
	if c.Remove("a") {
		t.Error("remove of missing should be false")
	}
}

func TestMaskSecret(t *testing.T) {
	if MaskSecret("") != "" {
		t.Error("empty mask")
	}
	if MaskSecret("short") != "****" {
		t.Error("short mask")
	}
	m := MaskSecret("xoxc-123456789-abcdef")
	if m == "xoxc-123456789-abcdef" || len(m) == 0 {
		t.Errorf("long mask leaked: %q", m)
	}
}

func TestNameFor(t *testing.T) {
	c := &Config{}
	c.Set("btd", Workspace{TeamID: "T1"})
	cases := []struct {
		w    Workspace
		want string
	}{
		{Workspace{TeamID: "T1", Domain: "bigtimedata"}, "btd"},
		{Workspace{TeamID: "T2", Domain: "ocral"}, "ocral"},
		{Workspace{TeamID: "T3"}, "T3"},
		{Workspace{}, "workspace"},
	}
	for _, tc := range cases {
		if got := c.NameFor(tc.w); got != tc.want {
			t.Errorf("NameFor(%+v) = %q, want %q", tc.w, got, tc.want)
		}
	}
}
