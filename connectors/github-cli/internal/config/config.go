// Package config finds the GitHub token. It is the only code that reads the
// home folder and the token env names. In Trebi mode (TREBI_STATE_DIR is
// set) it reads GITHUB_TOKEN, then the login in the state folder.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the token. EnvGHToken is read only outside Trebi.
const (
	EnvToken   = "GITHUB_TOKEN"
	EnvGHToken = "GH_TOKEN"
)

// AuthFile keeps the token of the device login in the state folder.
const AuthFile = "auth.json"

// ErrTrebiSets is a flag that selects a credential in Trebi mode.
var ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name

// Settings is the token and the place it came from.
type Settings struct {
	Token  string
	Source string
	Trebi  bool        // Trebi mode
	Input  bool        // the token is the GITHUB_TOKEN input of the connection
	Login  *LoginStore // the device login; nil outside Trebi
}

// Resolve finds the token. In Trebi mode a --token flag is an error, and
// the order is GITHUB_TOKEN, then the device login. Standalone, the order
// is --token, GITHUB_TOKEN, GH_TOKEN, and
// ~/.cli-tools/github-cli/config.json.
func Resolve(flagToken string) (Settings, error) {
	if t, ok := sdk.FromEnv(); ok {
		if flagToken != "" {
			return Settings{Trebi: true}, errors.New("--token: " + ErrTrebiSets.Error())
		}
		s := Settings{Trebi: true, Login: &LoginStore{dir: t.StateDir}}
		if v := os.Getenv(EnvToken); v != "" {
			s.Token, s.Source, s.Input = v, "Trebi ("+EnvToken+")", true
			return s, nil
		}
		token, err := s.Login.Load()
		if err != nil {
			return s, err
		}
		if token != "" {
			s.Token, s.Source = token, "Trebi (login)"
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

// LoginStore keeps the token of the device login in the state folder.
type LoginStore struct{ dir string }

// NewLoginStore returns the store in dir.
func NewLoginStore(dir string) *LoginStore { return &LoginStore{dir: dir} }

func (l *LoginStore) path() string { return filepath.Join(l.dir, AuthFile) }

// Load returns the saved token, or "".
func (l *LoginStore) Load() (string, error) {
	data, err := os.ReadFile(l.path())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read login: %w", err)
	}
	var f struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("parse login: %w", err)
	}
	return f.Token, nil
}

// Save writes the token with mode 0600.
func (l *LoginStore) Save(token string) error {
	data, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return err
	}
	tmp := l.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write login: %w", err)
	}
	return os.Rename(tmp, l.path())
}

// Clear removes the saved token.
func (l *LoginStore) Clear() error {
	if err := os.Remove(l.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
