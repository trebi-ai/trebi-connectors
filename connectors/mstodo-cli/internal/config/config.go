// Package config finds the folder of the login. It is the only code that
// reads the home folder. In Trebi mode (TREBI_STATE_DIR is set) the login
// is in the state folder, and only `serve` writes it.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// AuthFile is the login file in the state folder.
const AuthFile = "auth.json"

// ErrSetInTrebi is a command that saves a setting in Trebi mode.
var ErrSetInTrebi = errors.New("Set this value in Trebi") //nolint:staticcheck // a sentence for the user

// Settings tell where the login is.
type Settings struct {
	StateDir string // the folder of auth.json and the adapter state
	Trebi    bool
	ClientID string // the Entra public client; the build sets it
}

// AuthPath is the path of the login file.
func (s Settings) AuthPath() string { return filepath.Join(s.StateDir, AuthFile) }

// Resolve chooses the folder: TREBI_STATE_DIR in Trebi mode, else
// ~/.cli-tools/mstodo-cli.
func Resolve() Settings {
	if t, ok := sdk.FromEnv(); ok {
		return Settings{StateDir: t.StateDir, Trebi: true}
	}
	home, _ := os.UserHomeDir() //nolint:errcheck // an empty home gives a relative path
	return Settings{StateDir: filepath.Join(home, ".cli-tools", "mstodo-cli")}
}
