package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// fakeGH writes a gh script to a new folder. It prints token, or fails
// when token is empty. It also fails when it sees a token env, so a test
// proves that the child env has none.
func fakeGH(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\n[ -n \"$GH_TOKEN$GITHUB_TOKEN$TREBI_STATE_DIR\" ] && { echo leak >&2; exit 3; }\n[ \"$*\" = \"auth token -h github.com\" ] || exit 2\n"
	if token == "" {
		body += "echo 'not logged in' >&2\nexit 1\n"
	} else {
		body += "echo " + token + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0o755); err != nil { //nolint:gosec // a test script must run
		t.Fatal(err)
	}
	return dir
}

func TestResolveTrebiMode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(sdk.EnvStateDir, dir)
	t.Setenv(EnvToken, "")
	t.Setenv(EnvGHToken, "gh-standalone")
	t.Setenv("PATH", fakeGH(t, "from-gh"))
	if _, err := Resolve(ctx, "flag"); err == nil || !strings.Contains(err.Error(), ErrTrebiSets.Error()) {
		t.Fatalf("--token in Trebi mode: %v", err)
	}
	s, err := Resolve(ctx, "")
	if err != nil || s.Token != "" || !s.Trebi || s.Login == nil {
		t.Fatalf("empty state must give no token, and GH_TOKEN is standalone only: %+v %v", s, err)
	}
	if err := s.Login.Save(LoginRecord{Source: SourceDevice, Token: "from-device"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := Resolve(ctx, ""); s.Token != "from-device" || s.Source != SourceDevice || s.Input() {
		t.Fatalf("device login: %+v", s)
	}
	if err := s.Login.Save(LoginRecord{Source: SourceGH}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, AuthFile)); strings.Contains(string(data), "token") { //nolint:errcheck // checked by content
		t.Fatalf("a gh login must keep no token: %s", data)
	}
	if s, _ := Resolve(ctx, ""); s.Token != "from-gh" || s.Source != SourceGH {
		t.Fatalf("gh login: %+v", s)
	}
	t.Setenv("PATH", t.TempDir())
	if s, err := ResolveStored(ctx, ""); err != nil || s.Token != "" || s.Source != SourceGH {
		t.Fatalf("ResolveStored must not run gh: %+v %v", s, err)
	}
	t.Setenv(EnvToken, "from-input")
	if s, _ := Resolve(ctx, ""); s.Token != "from-input" || !s.Input() {
		t.Fatalf("the input wins over the login: %+v", s)
	}
}

func TestResolveGHLoggedOut(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(sdk.EnvStateDir, dir)
	t.Setenv(EnvToken, "")
	t.Setenv("PATH", fakeGH(t, ""))
	if err := NewLoginStore(dir).Save(LoginRecord{Source: SourceGH}); err != nil {
		t.Fatal(err)
	}
	s, err := Resolve(ctx, "")
	if err != nil || s.Token != "" || s.Source != SourceGH {
		t.Fatalf("a gh that logged out gives no token and no error: %+v %v", s, err)
	}
}

func TestLegacyLoginIsDevice(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, AuthFile), []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := NewLoginStore(dir).Load()
	if err != nil || rec.Source != SourceDevice || rec.Token != "old" {
		t.Fatalf("legacy: %+v %v", rec, err)
	}
}

func TestGHLoginSkipsProgramFolder(t *testing.T) {
	ctx := context.Background()
	own := fakeGH(t, "shim")
	user := fakeGH(t, "user")
	g := NewGHLoginIn(own+string(os.PathListSeparator)+user, own)
	if tok, err := g.Token(ctx); err != nil || tok != "user" {
		t.Fatalf("token: %q %v", tok, err)
	}
	links := t.TempDir()
	if err := os.Symlink(filepath.Join(own, "gh"), filepath.Join(links, "gh")); err != nil {
		t.Fatal(err)
	}
	if tok, err := NewGHLoginIn(links+string(os.PathListSeparator)+user, own).Token(ctx); err != nil || tok != "user" {
		t.Fatalf("a link to the program gh must not count: %q %v", tok, err)
	}
	if _, err := NewGHLoginIn(own, own).Token(ctx); !errors.Is(err, ErrGHLoggedOut) {
		t.Fatalf("only the program folder: %v", err)
	}
	t.Setenv(EnvGHToken, "env")
	if tok, err := g.Token(ctx); err != nil || tok != "user" {
		t.Fatalf("the child must get no token env: %q %v", tok, err)
	}
}

func TestResolveStandalone(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sdk.EnvStateDir, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvToken, "")
	t.Setenv(EnvGHToken, "gh")
	if s, _ := Resolve(ctx, "flag"); s.Token != "flag" {
		t.Fatalf("flag: %+v", s)
	}
	if s, _ := Resolve(ctx, ""); s.Token != "gh" || s.Login != nil {
		t.Fatalf("GH_TOKEN: %+v", s)
	}
}
