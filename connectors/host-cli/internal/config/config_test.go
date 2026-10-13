package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
)

// home makes a home folder with a key and a known_hosts file.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"id_ed25519", "known_hosts"} {
		if err := os.WriteFile(filepath.Join(h, ".ssh", f), []byte("home "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", h)
	return h
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{config.EnvHost, config.EnvUser, config.EnvKey, config.EnvAgent, config.EnvHostKey, config.EnvEvery, config.EnvCPUAbove, config.EnvMemAbove, config.EnvDiskAbove, config.EnvSamples, config.EnvKeyFile} {
		t.Setenv(k, "")
	}
}

func TestTrebiMode(t *testing.T) {
	clearEnv(t)
	home(t)
	state := t.TempDir()
	t.Setenv("TREBI_STATE_DIR", state)
	t.Setenv(config.EnvHost, "srv.example")
	t.Setenv(config.EnvUser, "ops")
	s, err := config.Resolve(config.Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr != "srv.example:22" || s.Key != nil || s.KnownHosts != filepath.Join(state, config.KnownHostsFile) || !s.TOFU || !s.Samples || s.CPUAbove != 90 {
		t.Fatalf("the home folder must not count in Trebi mode: %+v", s)
	}
	if m := s.Missing(); m == nil || m.Name != config.EnvKey {
		t.Fatalf("missing %v", m)
	}
	for _, f := range []config.Flags{{Host: "x"}, {User: "x"}, {KeyFile: "x"}} {
		if _, err := config.Resolve(f); !errors.Is(err, config.ErrTrebiSets) {
			t.Fatalf("%+v: %v", f, err)
		}
	}
	t.Setenv(config.EnvKey, "-----BEGIN KEY-----")
	t.Setenv(config.EnvHostKey, "ssh-ed25519 AAAA")
	t.Setenv(config.EnvDiskAbove, "80%")
	t.Setenv(config.EnvSamples, "no")
	t.Setenv(config.EnvEvery, "1s")
	s, err = config.Resolve(config.Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Missing() != nil || s.TOFU || s.KnownHosts != "" || s.DiskAbove != 80 || s.Samples || s.Every != config.MinEvery {
		t.Fatalf("%+v", s)
	}
	t.Setenv(config.EnvCPUAbove, "200")
	if _, err := config.Resolve(config.Flags{}); err == nil {
		t.Fatal("want an error for 200%")
	}
}

func TestStandalone(t *testing.T) {
	clearEnv(t)
	h := home(t)
	t.Setenv("TREBI_STATE_DIR", "")
	s, err := config.Resolve(config.Flags{Host: "[::1]:2222", User: "me"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr != "[::1]:2222" || string(s.Key) != "home id_ed25519" || s.KnownHosts != filepath.Join(h, ".ssh", "known_hosts") || s.TOFU || s.Missing() != nil {
		t.Fatalf("%+v", s)
	}
	keyFile := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(keyFile, []byte("file key"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = config.Resolve(config.Flags{Host: "h", User: "me", KeyFile: keyFile})
	if err != nil || string(s.Key) != "file key" {
		t.Fatalf("%+v %v", s, err)
	}
	t.Setenv(config.EnvAgent, "/tmp/agent.sock")
	if err := os.Remove(filepath.Join(h, ".ssh", "id_ed25519")); err != nil {
		t.Fatal(err)
	}
	s, err = config.Resolve(config.Flags{Host: "h", User: "me"})
	if err != nil || s.Key != nil || s.Agent != "/tmp/agent.sock" || s.Missing() != nil {
		t.Fatalf("%+v %v", s, err)
	}
}
