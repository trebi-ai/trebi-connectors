package ghshim

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// call is one exec of gh-bin.
type call struct {
	bin  string
	argv []string
	env  []string
}

func newShim(t *testing.T, env []string) (*Shim, *[]call, *bytes.Buffer) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), BinName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a test program
		t.Fatal(err)
	}
	var calls []call
	var stderr bytes.Buffer
	s := NewWith(bin, env, &stderr, func(bin string, argv, env []string) error {
		calls = append(calls, call{bin, argv, env})
		return nil
	})
	return s, &calls, &stderr
}

// trebi sets the env of a Trebi run in the process and returns it.
func trebi(t *testing.T, token string) (string, []string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv(sdk.EnvStateDir, state)
	t.Setenv(config.EnvToken, token)
	t.Setenv("PATH", t.TempDir()) // no gh login of the user
	return state, []string{sdk.EnvStateDir + "=" + state, config.EnvToken + "=" + token, "GH_TOKEN=personal", "GH_HOST=example.com", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=octo", "HOME=/home/octo"}
}

func TestOutsideTrebi(t *testing.T) {
	t.Setenv(sdk.EnvStateDir, "")
	env := []string{"GH_TOKEN=personal", "HOME=/home/octo"}
	s, calls, _ := newShim(t, env)
	if code := s.Run(context.Background(), []string{"auth", "token"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*calls) != 1 || !slices.Equal((*calls)[0].env, env) || !slices.Equal((*calls)[0].argv, []string{"gh", "auth", "token"}) {
		t.Fatalf("outside Trebi gh runs with no change: %+v", *calls)
	}
}

func TestRun(t *testing.T) {
	state, env := trebi(t, "tok")
	s, calls, _ := newShim(t, env)
	if code := s.Run(context.Background(), []string{"pr", "list", "-R", "octo/app"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	got := (*calls)[0]
	if got.bin != s.bin || !slices.Equal(got.argv, []string{"gh", "pr", "list", "-R", "octo/app"}) {
		t.Fatalf("exec %+v", got)
	}
	want := []string{
		"GH_TOKEN=tok", "GH_CONFIG_DIR=" + filepath.Join(state, "gh"), "GH_HOST=github.com", "GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_PAGER=cat", "NO_COLOR=1", "GH_SPINNER_DISABLED=1", "HOME=/home/octo",
		"GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_COUNT=5",
	}
	for _, kv := range want {
		if !slices.Contains(got.env, kv) {
			t.Errorf("env has no %s: %v", kv, got.env)
		}
	}
	for _, kv := range got.env {
		name, _, _ := strings.Cut(kv, "=")
		if name == config.EnvToken || kv == "GH_TOKEN=personal" || kv == "GH_HOST=example.com" {
			t.Errorf("env keeps %s", kv)
		}
	}
	if !slices.ContainsFunc(got.env, func(kv string) bool {
		return strings.HasPrefix(kv, "GIT_CONFIG_VALUE_2=!'") && strings.HasSuffix(kv, "' auth git-credential")
	}) {
		t.Errorf("no git credential helper: %v", got.env)
	}
	cfg, err := os.ReadFile(filepath.Join(state, "gh", "config.yml"))
	if err != nil || !strings.Contains(string(cfg), "git_protocol: https") {
		t.Fatalf("config.yml %s %v", cfg, err)
	}
}

func TestRefused(t *testing.T) {
	_, env := trebi(t, "tok")
	for _, args := range [][]string{
		{"auth", "login"}, {"auth", "token"}, {"auth", "--hostname", "github.com", "logout"}, {"auth", "refresh"}, {"auth", "switch"},
		{"auth", "setup-git"}, {"extension", "install", "x/y"}, {"ext", "upgrade", "--all"}, {"codespace", "list"},
	} {
		s, calls, stderr := newShim(t, env)
		if code := s.Run(context.Background(), args); code != ExitRefused || len(*calls) != 0 || !strings.Contains(stderr.String(), "not allowed in Trebi") {
			t.Errorf("%v: exit %d, calls %d, %q", args, code, len(*calls), stderr.String())
		}
	}
	for _, args := range [][]string{{"auth", "status"}, {"extension", "list"}, {"api", "user"}} {
		s, calls, _ := newShim(t, env)
		if code := s.Run(context.Background(), args); code != 0 || len(*calls) != 1 {
			t.Errorf("%v must run: exit %d", args, code)
		}
	}
}

func TestNoLogin(t *testing.T) {
	_, env := trebi(t, "")
	s, calls, stderr := newShim(t, env)
	if code := s.Run(context.Background(), []string{"repo", "list"}); code != ExitNoLogin || len(*calls) != 0 || strings.TrimSpace(stderr.String()) != MsgNoLogin {
		t.Fatalf("exit %d, %q", code, stderr.String())
	}
	for _, args := range [][]string{nil, {"--help"}, {"pr", "list", "-h"}, {"version"}} {
		if code := s.Run(context.Background(), args); code != 0 {
			t.Fatalf("%v needs no login: exit %d", args, code)
		}
	}
	if len(*calls) != 4 || slices.Contains((*calls)[0].env, "GH_TOKEN=tok") {
		t.Fatalf("calls: %+v", *calls)
	}
}

func TestMissingBin(t *testing.T) {
	_, env := trebi(t, "tok")
	s, calls, stderr := newShim(t, env)
	s.bin = filepath.Join(t.TempDir(), BinName)
	if code := s.Run(context.Background(), []string{"repo", "list"}); code != 1 || len(*calls) != 0 || !strings.Contains(stderr.String(), "missing") {
		t.Fatalf("exit %d, %q", code, stderr.String())
	}
}
