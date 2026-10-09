package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

func TestResolveTrebiMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(sdk.EnvStateDir, dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvTenant, "")
	if _, err := Resolve(Flags{ClientID: "flag"}, "build"); err == nil || !strings.Contains(err.Error(), ErrTrebiSets.Error()) {
		t.Fatalf("--client-id in Trebi mode: %v", err)
	}
	if _, err := Resolve(Flags{Tenant: "consumers"}, "build"); err == nil {
		t.Fatal("--tenant in Trebi mode must fail")
	}
	s, err := Resolve(Flags{}, "build")
	if err != nil || !s.Trebi || s.StateDir != dir || s.ClientID != "build" || s.Tenant != DefaultTenant {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	t.Setenv(EnvClientID, "input")
	t.Setenv(EnvTenant, "consumers")
	if s, _ := Resolve(Flags{}, "build"); s.ClientID != "input" || s.Tenant != "consumers" {
		t.Fatalf("the input wins over the build: %+v", s)
	}
}

func TestResolveStandalone(t *testing.T) {
	home := t.TempDir()
	t.Setenv(sdk.EnvStateDir, "")
	t.Setenv("HOME", home)
	t.Setenv(EnvClientID, "env")
	t.Setenv(EnvTenant, "")
	s, err := Resolve(Flags{}, "build")
	if err != nil || s.Trebi || s.ClientID != "env" || s.Tenant != DefaultTenant {
		t.Fatalf("env: %+v %v", s, err)
	}
	if s.AuthPath() != filepath.Join(home, ".cli-tools", "mstodo-cli", AuthFile) {
		t.Fatalf("auth path: %s", s.AuthPath())
	}
	if s, _ := Resolve(Flags{ClientID: "flag", Tenant: "consumers"}, "build"); s.ClientID != "flag" || s.Tenant != "consumers" {
		t.Fatalf("flag: %+v", s)
	}
	os.Unsetenv(EnvClientID) //nolint:errcheck // t.Setenv restores it
	if s, _ := Resolve(Flags{}, "build"); s.ClientID != "build" || s.Source != "build" {
		t.Fatalf("build: %+v", s)
	}
}
