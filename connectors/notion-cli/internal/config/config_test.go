package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTrebiMode(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TREBI_STATE_DIR", state)
	t.Setenv(EnvToken, "")
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"token":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(EnvToken+"=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Resolve("")
	if err != nil || s.Token != "" || !s.Trebi {
		t.Fatalf("trebi mode must ignore home files: %+v %v", s, err)
	}
	if _, err := Resolve("flag"); err == nil {
		t.Fatal("--token must fail in Trebi mode")
	}
	if err := Save("x"); err != ErrSetInTrebi {
		t.Fatalf("save: %v", err)
	}
	t.Setenv(EnvToken, "secret_a")
	if s, _ := Resolve(""); s.Token != "secret_a" {
		t.Fatalf("token %q", s.Token)
	}
}

func TestResolveStandalone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TREBI_STATE_DIR", "")
	t.Setenv(EnvToken, "")
	t.Chdir(home)
	if err := Save("from-file"); err != nil {
		t.Fatal(err)
	}
	if s, _ := Resolve(""); s.Token != "from-file" {
		t.Fatalf("config file: %+v", s)
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(EnvToken+"=\"from-dotenv\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, _ := Resolve(""); s.Token != "from-dotenv" {
		t.Fatalf(".env: %+v", s)
	}
	t.Setenv(EnvToken, "from-env")
	if s, _ := Resolve(""); s.Token != "from-env" {
		t.Fatalf("env: %+v", s)
	}
	if s, _ := Resolve("from-flag"); s.Token != "from-flag" {
		t.Fatalf("flag: %+v", s)
	}
}
