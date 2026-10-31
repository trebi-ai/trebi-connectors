// Package config finds the GitHub token. It is the only code that reads the
// home folder, the token env names, and the gh login of the user. In Trebi
// mode (TREBI_STATE_DIR is set) it reads GITHUB_TOKEN, then the login in
// the state folder.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the token. EnvGHToken is read only outside Trebi.
const (
	EnvToken   = "GITHUB_TOKEN"
	EnvGHToken = "GH_TOKEN"
)

// AuthFile keeps the login in the state folder.
const AuthFile = "auth.json"

// Login sources.
const (
	SourceInput  = "input"  // the GITHUB_TOKEN input of the connection
	SourceDevice = "device" // the device login; auth.json keeps the token
	SourceGH     = "gh"     // the gh login on this machine; read live
)

// ErrTrebiSets is a flag that selects a credential in Trebi mode.
var ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name

// ErrGHLoggedOut is a gh login that has no token for github.com.
var ErrGHLoggedOut = errors.New("GitHub is not logged in on this machine")

// Settings is the token and the place it came from.
type Settings struct {
	Token  string
	Source string // a Source* value in Trebi mode; a description outside
	Trebi  bool   // Trebi mode
	Login  *LoginStore
	GH     *GHLogin
}

// Input reports whether the token is the GITHUB_TOKEN input, which logout
// cannot remove.
func (s Settings) Input() bool { return s.Trebi && s.Source == SourceInput }

// Resolve finds the token. In Trebi mode a --token flag is an error, and
// the order is GITHUB_TOKEN, then auth.json: the device token, or a live
// read of the gh login. Standalone, the order is --token, GITHUB_TOKEN,
// GH_TOKEN, and ~/.cli-tools/github-cli/config.json.
func Resolve(ctx context.Context, flagToken string) (Settings, error) {
	return resolve(ctx, flagToken, true)
}

// ResolveStored is Resolve with no read of the gh login. serve reads it
// itself, and in the sandbox it must not run the gh of the user.
func ResolveStored(ctx context.Context, flagToken string) (Settings, error) {
	return resolve(ctx, flagToken, false)
}

func resolve(ctx context.Context, flagToken string, readGH bool) (Settings, error) {
	if t, ok := sdk.FromEnv(); ok {
		if flagToken != "" {
			return Settings{Trebi: true}, errors.New("--token: " + ErrTrebiSets.Error())
		}
		s := Settings{Trebi: true, Login: NewLoginStore(t.StateDir), GH: NewGHLogin()}
		if v := os.Getenv(EnvToken); v != "" {
			s.Token, s.Source = v, SourceInput
			return s, nil
		}
		rec, err := s.Login.Load()
		if err != nil {
			return s, err
		}
		s.Source = rec.Source
		switch rec.Source {
		case SourceDevice:
			s.Token = rec.Token
		case SourceGH:
			if readGH {
				// A gh that logged out gives no token; the status reports it.
				s.Token, _ = s.GH.Token(ctx) //nolint:errcheck // no token is the answer
			}
		}
		return s, nil
	}
	if flagToken != "" {
		return Settings{Token: flagToken, Source: "flag (--token)"}, nil
	}
	for _, name := range []string{EnvToken, EnvGHToken} {
		if v := os.Getenv(name); v != "" {
			return Settings{Token: v, Source: "env (" + name + ")"}, nil
		}
	}
	home, _ := os.UserHomeDir() //nolint:errcheck // an empty home gives a relative path
	path := filepath.Join(home, ".cli-tools", "github-cli", "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var f struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return Settings{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Token == "" {
		return Settings{}, nil
	}
	return Settings{Token: f.Token, Source: "config (" + path + ")"}, nil
}

// LoginRecord is the content of auth.json. A gh login keeps no token.
type LoginRecord struct {
	Source string `json:"source"`
	Token  string `json:"token,omitempty"`
}

// LoginStore keeps the login in the state folder.
type LoginStore struct{ dir string }

// NewLoginStore returns the store in dir.
func NewLoginStore(dir string) *LoginStore { return &LoginStore{dir: dir} }

func (l *LoginStore) path() string { return filepath.Join(l.dir, AuthFile) }

// Load returns the saved login, or the zero record. A file of 0.2 has only
// a token: it is a device login.
func (l *LoginStore) Load() (LoginRecord, error) {
	data, err := os.ReadFile(l.path())
	if errors.Is(err, os.ErrNotExist) {
		return LoginRecord{}, nil
	}
	if err != nil {
		return LoginRecord{}, fmt.Errorf("read login: %w", err)
	}
	var rec LoginRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return LoginRecord{}, fmt.Errorf("parse login: %w", err)
	}
	if rec.Source == "" && rec.Token != "" {
		rec.Source = SourceDevice
	}
	return rec, nil
}

// Save writes the login with mode 0600.
func (l *LoginStore) Save(rec LoginRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := sdk.WriteFileAtomic(l.path(), data, 0o600); err != nil {
		return fmt.Errorf("write login: %w", err)
	}
	return nil
}

// Clear removes the saved login.
func (l *LoginStore) Clear() error {
	if err := os.Remove(l.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ghDirs are the install folders of gh that a LaunchAgent PATH can miss.
var ghDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} //nolint:gochecknoglobals // fixed list, never changed

// ghTimeout bounds one read of the gh login.
const ghTimeout = 5 * time.Second

// GHLogin reads the token of the gh login of the user. It never runs the
// gh of the connector, which is a shim that would ask the connection.
type GHLogin struct {
	path string   // PATH to search
	dirs []string // known install folders, after PATH
	skip string   // the program folder of the connector
}

// NewGHLogin searches PATH and the known install folders, and skips the
// folder of this program.
func NewGHLogin() *GHLogin {
	g := &GHLogin{path: os.Getenv("PATH"), dirs: ghDirs}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		g.skip = filepath.Dir(exe)
	}
	return g
}

// NewGHLoginIn searches only path, and skips the folder skip (tests).
func NewGHLoginIn(path, skip string) *GHLogin { return &GHLogin{path: path, skip: skip} }

// Bin returns the gh program of the user, or "".
func (g *GHLogin) Bin() string {
	dirs := append(filepath.SplitList(g.path), g.dirs...)
	for _, d := range dirs {
		if d == "" || sameDir(d, g.skip) {
			continue
		}
		p := filepath.Join(d, "gh")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// Token runs gh auth token for github.com. The child gets no token env, so
// the answer is the stored login of the user.
func (g *GHLogin) Token(ctx context.Context) (string, error) {
	bin := g.Bin()
	if bin == "" {
		return "", ErrGHLoggedOut
	}
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "auth", "token", "-h", "github.com") //nolint:gosec // the gh of the user, fixed args
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case EnvToken, EnvGHToken, "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", sdk.EnvStateDir:
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", ErrGHLoggedOut
	}
	token := strings.TrimSpace(out.String())
	if token == "" {
		return "", ErrGHLoggedOut
	}
	return token, nil
}

func sameDir(a, b string) bool {
	if b == "" {
		return false
	}
	clean := func(p string) string {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return filepath.Clean(p)
	}
	return clean(a) == clean(b)
}
