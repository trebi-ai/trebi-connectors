// Package config finds the folder of the login and the Microsoft app. It
// is the only code that reads the home folder and the settings env names.
// In Trebi mode (TREBI_STATE_DIR is set) the login is in the state folder,
// only `serve` writes it, and the app comes from the connection inputs.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// AuthFile is the login file in the state folder.
const AuthFile = "auth.json"

// Env names of the Microsoft app. They are also the connection inputs.
const (
	EnvClientID = "MICROSOFT_TODO_CLIENT_ID"
	EnvTenant   = "MICROSOFT_TODO_TENANT"
)

// DefaultTenant accepts work, school, and personal accounts.
const DefaultTenant = "common"

// ErrSetInTrebi is a command that saves a setting in Trebi mode.
var ErrSetInTrebi = errors.New("Set this value in Trebi") //nolint:staticcheck // a sentence for the user

// ErrTrebiSets is a flag that selects a setting in Trebi mode.
var ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name

// Settings tell where the login is and which Microsoft app logs in.
type Settings struct {
	StateDir string // the folder of auth.json and the adapter state
	Trebi    bool
	ClientID string // the Entra public client
	Tenant   string // common, consumers, organizations, or a tenant id
	Source   string // where ClientID came from
}

// AuthPath is the path of the login file.
func (s Settings) AuthPath() string { return filepath.Join(s.StateDir, AuthFile) }

// Flags are the flag values of the app. Empty means not set.
type Flags struct {
	ClientID string
	Tenant   string
}

// Resolve chooses the folder and the app. buildID is the app id of the
// build. In Trebi mode the order is the input, then the build, and a flag
// is an error. Standalone, the order is the flag, the env, then the build.
func Resolve(f Flags, buildID string) (Settings, error) {
	if t, ok := sdk.FromEnv(); ok {
		s := Settings{StateDir: t.StateDir, Trebi: true}
		if f.ClientID != "" || f.Tenant != "" {
			return s, errors.New("--client-id, --tenant: " + ErrTrebiSets.Error())
		}
		s.ClientID, s.Source = pick(
			source{os.Getenv(EnvClientID), "Trebi (" + EnvClientID + ")"},
			source{buildID, "build"},
		)
		s.Tenant, _ = pick(source{os.Getenv(EnvTenant), ""}, source{DefaultTenant, ""})
		return s, nil
	}
	home, _ := os.UserHomeDir() //nolint:errcheck // an empty home gives a relative path
	s := Settings{StateDir: filepath.Join(home, ".cli-tools", "microsoft-todo-cli")}
	s.ClientID, s.Source = pick(
		source{f.ClientID, "flag (--client-id)"},
		source{os.Getenv(EnvClientID), "env (" + EnvClientID + ")"},
		source{buildID, "build"},
	)
	s.Tenant, _ = pick(source{f.Tenant, ""}, source{os.Getenv(EnvTenant), ""}, source{DefaultTenant, ""})
	return s, nil
}

type source struct{ value, name string }

// pick returns the first value that is set.
func pick(in ...source) (string, string) {
	for _, s := range in {
		if s.value != "" {
			return s.value, s.name
		}
	}
	return "", ""
}
