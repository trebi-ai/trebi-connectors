// Package config finds the server, the user, and the password. It is the
// only code that reads the VNC_* env names and the home folder. In Trebi
// mode (TREBI_STATE_DIR is set) the values come only from the inputs that
// Trebi gives.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the inputs.
const (
	EnvServer      = "VNC_SERVER"
	EnvUser        = "VNC_USER"
	EnvPassword    = "VNC_PASSWORD"
	EnvIdleTimeout = "VNC_IDLE_TIMEOUT"
)

// Labels of the inputs in the catalog manifest.
const (
	ServerLabel   = "Computer address"
	UserLabel     = "User name"
	PasswordLabel = "Password"
)

// DefaultIdleTimeout is the time a background session stays with no client.
const DefaultIdleTimeout = 5 * time.Minute

var (
	// ErrTrebiSets is a flag that selects a server or a credential in Trebi
	// mode.
	ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name
	// ErrSetInTrebi is a command that saves a setting in Trebi mode.
	ErrSetInTrebi = errors.New("Set this value in Trebi") //nolint:staticcheck // a sentence for the user
)

// Target is one named server in the config file.
type Target struct {
	Server      string `toml:"server"`
	User        string `toml:"user,omitempty"`
	PasswordEnv string `toml:"password_env,omitempty"`
}

// File is ~/.cli-tools/vnc-cli/config.toml.
type File struct {
	Default string            `toml:"default,omitempty"`
	Targets map[string]Target `toml:"targets"`
}

// Flags are the command line values that select the server.
type Flags struct {
	Server      string
	Target      string
	User        string
	PasswordEnv string
	IdleTimeout string
}

// Settings is the resolved configuration.
type Settings struct {
	Server   string
	User     string
	Password string
	// PasswordFrom names the source of the password for messages, never
	// the value: "VNC_PASSWORD" or "LAB_PASSWORD".
	PasswordFrom string
	Target       string
	IdleTimeout  time.Duration

	Trebi    bool
	StateDir string
	// Dir is the folder of the standalone tool, "" in Trebi mode.
	Dir string
}

// Resolve reads the settings. In Trebi mode, a flag that selects the
// server or a credential gives ErrTrebiSets. Standalone, the order is the
// flags, the target in the config file, then the VNC_* env names.
func Resolve(f Flags) (Settings, error) {
	t, trebi := sdk.FromEnv()
	s := Settings{Trebi: trebi, StateDir: t.StateDir}
	if trebi {
		for name, v := range map[string]string{"--server": f.Server, "--target": f.Target, "--user": f.User, "--password-env": f.PasswordEnv} {
			if v != "" {
				return s, fmt.Errorf("%s: %w", name, ErrTrebiSets)
			}
		}
		s.Server = strings.TrimSpace(os.Getenv(EnvServer))
		s.User = strings.TrimSpace(os.Getenv(EnvUser))
		s.Password, s.PasswordFrom = os.Getenv(EnvPassword), EnvPassword
		return s, nil
	}

	dir, err := Dir()
	if err != nil {
		return s, err
	}
	s.Dir = dir
	file, err := Load(dir)
	if err != nil {
		return s, err
	}
	var tg Target
	name := f.Target
	if name == "" && f.Server == "" && os.Getenv(EnvServer) == "" {
		name = file.Default
	}
	if name != "" {
		var ok bool
		if tg, ok = file.Targets[name]; !ok {
			return s, fmt.Errorf("target %q is not in %s", name, filepath.Join(dir, fileName))
		}
		s.Target = name
	}
	s.Server = firstOf(f.Server, tg.Server, os.Getenv(EnvServer))
	s.User = firstOf(f.User, tg.User, os.Getenv(EnvUser))
	switch env := firstOf(f.PasswordEnv, tg.PasswordEnv); {
	case env != "":
		s.Password, s.PasswordFrom = os.Getenv(env), env
	default:
		s.Password, s.PasswordFrom = os.Getenv(EnvPassword), EnvPassword
	}
	s.IdleTimeout = DefaultIdleTimeout
	if v := firstOf(f.IdleTimeout, os.Getenv(EnvIdleTimeout)); v != "" {
		if s.IdleTimeout, err = time.ParseDuration(v); err != nil || s.IdleTimeout < 0 {
			return s, fmt.Errorf("idle timeout: %q is not a duration such as 5m or 0", v)
		}
	}
	return s, nil
}

// Missing returns the missing server input, or nil.
func (s Settings) Missing() *sdk.MissingInput {
	if s.Server == "" {
		return &sdk.MissingInput{Name: EnvServer, Label: ServerLabel}
	}
	return nil
}

// Key is a short name of the server and the user, for the session socket.
func (s Settings) Key() string { return s.User + "@" + s.Server }

const fileName = "config.toml"

// Dir is the folder of the standalone tool: ~/.cli-tools/vnc-cli.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cli-tools", "vnc-cli"), nil
}

// Load reads the config file in dir. A missing file is an empty config.
func Load(dir string) (File, error) {
	var f File
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := toml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", filepath.Join(dir, fileName), err)
	}
	return f, nil
}

// SetTarget adds or replaces a target in the config file. It fails in
// Trebi mode.
func SetTarget(name string, t Target, makeDefault bool) error {
	if _, trebi := sdk.FromEnv(); trebi {
		return ErrSetInTrebi
	}
	if name == "" || t.Server == "" {
		return errors.New("a target needs a name and a server")
	}
	return edit(func(f *File) {
		if f.Targets == nil {
			f.Targets = map[string]Target{}
		}
		f.Targets[name] = t
		if makeDefault {
			f.Default = name
		}
	})
}

// RemoveTarget removes a target from the config file. It fails in Trebi
// mode.
func RemoveTarget(name string) error {
	if _, trebi := sdk.FromEnv(); trebi {
		return ErrSetInTrebi
	}
	return edit(func(f *File) {
		delete(f.Targets, name)
		if f.Default == name {
			f.Default = ""
		}
	})
}

// Names are the target names in order.
func (f File) Names() []string {
	out := make([]string, 0, len(f.Targets))
	for n := range f.Targets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func edit(change func(*File)) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	f, err := Load(dir)
	if err != nil {
		return err
	}
	change(&f)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		return err
	}
	return sdk.WriteFileAtomic(filepath.Join(dir, fileName), []byte(b.String()), 0o600)
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
