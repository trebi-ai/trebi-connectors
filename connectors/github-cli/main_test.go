package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// build makes the archive layout in a new folder: github-cli, its copies
// gh and github-op, and a fake gh-bin that prints its args and token.
func build(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "github-cli")
	if b, err := exec.Command("go", "build", "-o", out, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh", "github-op"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil { //nolint:gosec // a program
			t.Fatal(err)
		}
	}
	bin := "#!/bin/sh\necho \"args=$* token=$GH_TOKEN host=$GH_HOST\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh-bin"), []byte(bin), 0o755); err != nil { //nolint:gosec // a program
		t.Fatal(err)
	}
	return dir
}

func runIn(dir, name, stdin string, env ...string) (string, string, int) {
	cmd := exec.Command(filepath.Join(dir, name))
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + dir}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func TestProgramNames(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the program")
	}
	dir := build(t)
	state := t.TempDir()

	out, errs, code := runIn(dir, "gh", "", "TREBI_STATE_DIR="+state, "GITHUB_TOKEN=conn", "GH_TOKEN=personal")
	if code != 0 || strings.TrimSpace(out) != "args= token=conn host=github.com" {
		t.Fatalf("gh in Trebi: %d %q %q", code, out, errs)
	}
	cmd := exec.Command(filepath.Join(dir, "gh"), "pr", "list")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TREBI_STATE_DIR=" + state, "GITHUB_TOKEN=conn"}
	if b, err := cmd.Output(); err != nil || strings.TrimSpace(string(b)) != "args=pr list token=conn host=github.com" {
		t.Fatalf("gh args: %q %v", b, err)
	}

	cmd = exec.Command(filepath.Join(dir, "gh"), "auth", "token")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TREBI_STATE_DIR=" + state, "GITHUB_TOKEN=conn"}
	var ee *exec.ExitError
	if b, err := cmd.CombinedOutput(); !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(b), "not allowed in Trebi") {
		t.Fatalf("gh auth token must be refused: %q %v", b, err)
	}

	out, _, code = runIn(dir, "gh", "", "GH_TOKEN=personal")
	if code != 0 || strings.TrimSpace(out) != "args= token=personal host=" {
		t.Fatalf("gh outside Trebi runs unchanged: %d %q", code, out)
	}

	out, _, _ = runIn(dir, "github-op", "", "GITHUB_TOKEN=x")
	if !strings.Contains(out, "rerun_workflow") {
		t.Fatalf("github-op lists the operations: %q", out)
	}
	cmd = exec.Command(filepath.Join(dir, "github-op"), "search")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GITHUB_TOKEN=x"}
	cmd.Stdin = strings.NewReader(`{"limit":5}`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var line struct {
		Error string `json:"error"`
	}
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || json.Unmarshal(stderr.Bytes(), &line) != nil || line.Error != "query is required" {
		t.Fatalf("github-op search with no query: %v %q", err, stderr.String())
	}
}
