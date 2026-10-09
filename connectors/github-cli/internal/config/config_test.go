package config

import (
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

func TestResolveTrebiMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sdk.EnvStateDir, dir)
	t.Setenv(EnvToken, "")
	t.Setenv(EnvGHToken, "gh-standalone")
	if _, err := Resolve("flag"); err == nil || !strings.Contains(err.Error(), ErrTrebiSets.Error()) {
		t.Fatalf("--token in Trebi mode: %v", err)
	}
	s, err := Resolve("")
	if err != nil || s.Token != "" || !s.Trebi || s.Login == nil {
		t.Fatalf("empty state must give no token, and GH_TOKEN is standalone only: %+v %v", s, err)
	}
	if err := s.Login.Save("from-login"); err != nil {
		t.Fatal(err)
	}
	if s, _ := Resolve(""); s.Token != "from-login" || s.Input {
		t.Fatalf("login: %+v", s)
	}
	t.Setenv(EnvToken, "from-input")
	if s, _ := Resolve(""); s.Token != "from-input" || !s.Input {
		t.Fatalf("the input wins over the login: %+v", s)
	}
}

func TestResolveStandalone(t *testing.T) {
	t.Setenv(sdk.EnvStateDir, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvToken, "")
	t.Setenv(EnvGHToken, "gh")
	if s, _ := Resolve("flag"); s.Token != "flag" {
		t.Fatalf("flag: %+v", s)
	}
	if s, _ := Resolve(""); s.Token != "gh" || s.Login != nil {
		t.Fatalf("GH_TOKEN: %+v", s)
	}
}
