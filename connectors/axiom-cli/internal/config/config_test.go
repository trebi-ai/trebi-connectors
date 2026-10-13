package config_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/config"
)

// homeWithFile makes a home folder with a saved config file.
func homeWithFile(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	if err := config.Save(&config.File{Token: "xaat-file", OrgID: "file-org", APIURL: "https://file.example"}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTrebiMode(t *testing.T) {
	homeWithFile(t)
	state := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", state)
	r, err := config.Resolve(config.ResolveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Token != "" || r.OrgID != "" || r.APIURL != config.DefaultAPIURL || !r.Trebi || r.Source != config.SourceNone {
		t.Fatalf("the home file must not count in Trebi mode: %+v", r)
	}
	r, err = config.Resolve(config.ResolveInput{EnvToken: "xaat-env", EnvTokenSet: true, EnvOrgID: "o"})
	if err != nil || r.Token != "xaat-env" || r.OrgID != "o" || r.Source != config.SourceTrebi {
		t.Fatalf("%+v %v", r, err)
	}
	for _, in := range []config.ResolveInput{{FlagSet: true, FlagToken: "x"}, {FlagOrgIDSet: true}, {FlagAPIURLSet: true}} {
		if _, err := config.Resolve(in); !errors.Is(err, config.ErrTrebiSets) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	if dir, err := config.StateDir(); err != nil || dir != state {
		t.Fatalf("%s %v", dir, err)
	}
}

func TestStandalone(t *testing.T) {
	h := homeWithFile(t)
	t.Setenv("TREBI_STATE_DIR", "")
	r, err := config.Resolve(config.ResolveInput{})
	if err != nil || r.Token != "xaat-file" || r.Source != config.SourceFile || r.APIURL != "https://file.example" || r.Trebi {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = config.Resolve(config.ResolveInput{FlagSet: true, FlagToken: "xaat-flag", EnvToken: "xaat-env", EnvTokenSet: true})
	if err != nil || r.Token != "xaat-flag" || r.Source != config.SourceFlag {
		t.Fatalf("%+v %v", r, err)
	}
	if dir, err := config.StateDir(); err != nil || dir != filepath.Join(h, ".cli-tools", "axiom-cli") {
		t.Fatalf("%s %v", dir, err)
	}
}

func TestEvery(t *testing.T) {
	for v, want := range map[string]time.Duration{"": time.Minute, "5m": 5 * time.Minute, "10s": time.Minute} {
		t.Setenv(config.EnvEvery, v)
		if d, err := config.Every(); err != nil || d != want {
			t.Fatalf("%q: %s %v", v, d, err)
		}
	}
	t.Setenv(config.EnvEvery, "soon")
	if _, err := config.Every(); err == nil {
		t.Fatal("want an error")
	}
}
