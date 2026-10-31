// Package ghshim is the gh of a run. It runs the real gh (gh-bin, next to
// this program) with the token of the Trebi connection, never with the
// personal login of the user. Outside Trebi it runs gh-bin with no change.
package ghshim

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// BinName is the real gh in the program folder.
const BinName = "gh-bin"

// Exit codes. 4 is the gh code for a missing login.
const (
	ExitRefused = 2
	ExitNoLogin = 4
)

// MsgNoLogin is the error of a run with no GitHub login.
const MsgNoLogin = "GitHub is not logged in. Log in on the GitHub connection in Trebi."

// dropEnv are the env names that would give gh another token or host.
var dropEnv = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR"} //nolint:gochecknoglobals // fixed list, never changed

// Shim runs gh-bin.
type Shim struct {
	bin     string // the path of gh-bin
	env     []string
	stderr  io.Writer
	resolve func(ctx context.Context) (config.Settings, error)
	exec    func(bin string, argv, env []string) error
}

// New finds gh-bin next to this program and replaces the process with it.
func New() *Shim {
	bin := BinName
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		bin = filepath.Join(filepath.Dir(exe), BinName)
	}
	return &Shim{
		bin: bin, env: os.Environ(), stderr: os.Stderr,
		resolve: func(ctx context.Context) (config.Settings, error) { return config.Resolve(ctx, "") },
		exec:    syscall.Exec,
	}
}

// NewWith builds a shim with a given gh-bin, env, and exec (tests). The
// token still comes from the process env.
func NewWith(bin string, env []string, stderr io.Writer, exec func(bin string, argv, env []string) error) *Shim {
	s := New()
	s.bin, s.env, s.stderr, s.exec = bin, env, stderr, exec
	return s
}

// Run runs gh-bin with args and returns the exit code. A good exec does
// not return.
func (s *Shim) Run(ctx context.Context, args []string) int {
	state := lookup(s.env, sdk.EnvStateDir)
	if state == "" {
		return s.run(args, s.env)
	}
	if why := refused(args); why != "" {
		fmt.Fprintln(s.stderr, "gh: "+why)
		return ExitRefused
	}
	set, err := s.resolve(ctx)
	if err != nil {
		fmt.Fprintln(s.stderr, "gh: "+err.Error())
		return 1
	}
	if set.Token == "" && !infoOnly(args) {
		fmt.Fprintln(s.stderr, MsgNoLogin)
		return ExitNoLogin
	}
	dir := filepath.Join(state, "gh")
	if err := writeConfig(dir); err != nil {
		fmt.Fprintln(s.stderr, "gh: "+err.Error())
		return 1
	}
	return s.run(args, s.childEnv(set.Token, dir))
}

func (s *Shim) run(args, env []string) int {
	if _, err := os.Stat(s.bin); err != nil {
		fmt.Fprintf(s.stderr, "gh: the GitHub program is missing (%s). Update the GitHub connector.\n", s.bin)
		return 1
	}
	if err := s.exec(s.bin, append([]string{"gh"}, args...), env); err != nil {
		fmt.Fprintln(s.stderr, "gh: "+err.Error())
		return 1
	}
	return 0
}

// childEnv is the env of gh-bin: the token of the connection, a config
// folder in the state folder, no prompts, no colors, no pager, and a git
// credential helper that is this gh.
func (s *Shim) childEnv(token, dir string) []string {
	var env []string
	count := 0
	for _, kv := range s.env {
		name, val, _ := strings.Cut(kv, "=")
		if slices.Contains(dropEnv, name) {
			continue
		}
		if name == "GIT_CONFIG_COUNT" {
			count, _ = strconv.Atoi(val) //nolint:errcheck // a bad count starts at 0
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GH_TOKEN="+token,
		"GH_CONFIG_DIR="+dir,
		"GH_HOST=github.com",
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"GH_NO_EXTENSION_UPDATE_NOTIFIER=1",
		"GH_SPINNER_DISABLED=1",
		"GH_PAGER=cat",
		"NO_COLOR=1",
	)
	// git clone and push through gh ask this gh for the credential, not
	// the helper of the user. An empty value clears the helper list.
	helper := "!'" + strings.ReplaceAll(s.bin, "'", `'\''`) + "' auth git-credential"
	for i, kv := range [][2]string{
		{"credential.https://github.com.helper", ""},
		{"credential.https://github.com.helper", helper},
		{"credential.https://gist.github.com.helper", ""},
		{"credential.https://gist.github.com.helper", helper},
	} {
		n := strconv.Itoa(count + i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(count+4))
}

// refused returns why a command is not allowed in a run, or "". A flag
// value can come before the subcommand, so each later word counts.
func refused(args []string) string {
	first := slices.IndexFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
	if first < 0 {
		return ""
	}
	rest := args[first+1:]
	has := func(words ...string) string {
		for _, a := range rest {
			if slices.Contains(words, a) {
				return a
			}
		}
		return ""
	}
	switch args[first] {
	case "auth":
		if w := has("login", "logout", "refresh", "switch", "setup-git", "token"); w != "" {
			return "gh auth " + w + " is not allowed in Trebi. The GitHub connection holds the login; use gh auth status to see it."
		}
	case "extension", "extensions", "ext":
		if w := has("install", "upgrade"); w != "" {
			return "gh extension " + w + " is not allowed in Trebi."
		}
	case "codespace", "cs":
		return "gh codespace is not allowed in Trebi."
	}
	return ""
}

// infoOnly reports whether gh only shows help or its version, which needs
// no login.
func infoOnly(args []string) bool {
	if len(args) == 0 || args[0] == "help" || args[0] == "version" {
		return true
	}
	return slices.ContainsFunc(args, func(a string) bool { return a == "--help" || a == "-h" || a == "--version" })
}

// writeConfig writes the gh config of the connection once, with HTTPS for
// git.
func writeConfig(dir string) error {
	path := filepath.Join(dir, "config.yml")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return sdk.WriteFileAtomic(path, []byte("version: \"1\"\ngit_protocol: https\nprompt: disabled\n"), 0o600)
}

func lookup(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == name {
			return v
		}
	}
	return ""
}
