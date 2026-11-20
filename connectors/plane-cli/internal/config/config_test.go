package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	t.Setenv("PLANE_CONFIG", p)
	t.Setenv("PLANE_API_KEY", "")
	t.Setenv("PLANE_WORKSPACE", "")
	cfg := &Config{APIKey: "plane_api_123456789", Workspace: "acme", Project: "ENG"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v err = %v", st.Mode(), err)
	}
	got, err := Load()
	if err != nil || got.APIKey != cfg.APIKey || got.Workspace != "acme" || got.Project != "ENG" {
		t.Fatalf("got %+v err %v", got, err)
	}
	if got.BaseURL() != DefaultHost {
		t.Errorf("base url = %q", got.BaseURL())
	}
}

func TestEnvOverridesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("PLANE_CONFIG", p)
	if err := (&Config{APIKey: "file", Workspace: "a"}).Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLANE_API_KEY", "env")
	t.Setenv("PLANE_HOST", "https://plane.example.com/")
	cfg, err := Load()
	if err != nil || cfg.APIKey != "env" || cfg.Workspace != "a" || cfg.BaseURL() != "https://plane.example.com" {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
	file, _ := LoadFile()
	if file.APIKey != "file" {
		t.Errorf("LoadFile applied env: %q", file.APIKey)
	}
}

func TestMaskSecret(t *testing.T) {
	for in, want := range map[string]string{"": "", "short": "****", "plane_api_abcdef": "plan...cdef"} {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q", in, got)
		}
	}
}

func TestWorkspaceCredentials(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("PLANE_CONFIG", p)
	t.Setenv("PLANE_API_KEY", "")
	t.Setenv("PLANE_HOST", "")
	t.Setenv("PLANE_WORKSPACE", "")
	file := &Config{APIKey: "default", Workspace: "a"}
	file.SetWorkspace("b", Workspace{APIKey: "key-b", Host: "https://b.example.com"})
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	load := func() *Config {
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	cfg := load()
	cfg.UseWorkspaceCredentials()
	if cfg.APIKey != "default" || cfg.BaseURL() != DefaultHost {
		t.Errorf("workspace a: %q %q", cfg.APIKey, cfg.BaseURL())
	}
	cfg = load()
	cfg.Workspace = "b"
	cfg.UseWorkspaceCredentials()
	if cfg.APIKey != "key-b" || cfg.BaseURL() != "https://b.example.com" {
		t.Errorf("workspace b: %q %q", cfg.APIKey, cfg.BaseURL())
	}
	cfg = load()
	cfg.Workspace = "b"
	cfg.SetAPIKey("flag")
	cfg.UseWorkspaceCredentials()
	if cfg.APIKey != "flag" {
		t.Errorf("flag lost to workspace key: %q", cfg.APIKey)
	}
	t.Setenv("PLANE_API_KEY", "env")
	cfg = load()
	cfg.Workspace = "b"
	cfg.UseWorkspaceCredentials()
	if cfg.APIKey != "env" {
		t.Errorf("env lost to workspace key: %q", cfg.APIKey)
	}
	if red := cfg.Redacted(); red.Workspaces["b"].APIKey == "key-b" {
		t.Errorf("workspace key not masked")
	}
	t.Setenv("PLANE_API_KEY", "")
	cfg = load()
	cfg.Workspace = "b"
	cfg.UseWorkspaceCredentials()
	if a := cfg.ForWorkspace("a"); a.APIKey != "default" || a.BaseURL() != DefaultHost || a.Workspace != "a" {
		t.Errorf("ForWorkspace(a) after b: %q %q", a.APIKey, a.BaseURL())
	}
	if b := cfg.ForWorkspace("b"); b.APIKey != "key-b" {
		t.Errorf("ForWorkspace(b): %q", b.APIKey)
	}
}
