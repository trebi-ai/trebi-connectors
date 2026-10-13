package config_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/config"
)

func TestParseTargets(t *testing.T) {
	got := config.ParseTargets("https://a.example/feed, https://b.example/p #price .x, y\n\n  http://c.example\nnot-a-url")
	want := []config.Target{
		{URL: "https://a.example/feed"},
		{URL: "https://b.example/p", Selector: "#price .x, y"},
		{URL: "http://c.example"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestParseEvery(t *testing.T) {
	for in, want := range map[string]time.Duration{"": config.DefaultEvery, "1m": config.MinEvery, "2h": 2 * time.Hour} {
		if got, err := config.ParseEvery(in); err != nil || got != want {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	if _, err := config.ParseEvery("often"); err == nil {
		t.Fatal("want an error")
	}
}

func TestResolveTrebi(t *testing.T) {
	state := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", state)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.EnvURLs, "")
	t.Setenv(config.EnvEvery, "")
	s, err := config.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Trebi || s.StateDir != state || s.Missing() == nil || s.Missing().Name != config.EnvURLs {
		t.Fatalf("%+v", s)
	}
}

func TestResolveStandalone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv(config.EnvURLs, "https://a.example")
	t.Setenv(config.EnvEvery, "1h")
	s, err := config.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if s.Trebi || s.StateDir != filepath.Join(home, ".cli-tools", "web-cli") || len(s.Targets) != 1 || s.Every != time.Hour {
		t.Fatalf("%+v", s)
	}
}
