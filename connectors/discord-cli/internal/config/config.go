// Package config finds the bot token. It is the only code that reads the
// home folder, the .env files, and the token env names. In Trebi mode
// (TREBI_STATE_DIR is set) it reads DISCORD_TOKEN only.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the token. EnvBotToken is read only outside Trebi.
const (
	EnvToken    = "DISCORD_TOKEN"
	EnvBotToken = "DISCORD_BOT_TOKEN"
)

// TokenLabel is the label of the token input in the catalog manifest.
const TokenLabel = "Bot token"

var (
	// ErrTrebiSets is a flag that selects a credential in Trebi mode.
	ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name
	// ErrSetInTrebi is a command that saves a setting in Trebi mode.
	ErrSetInTrebi = errors.New("Set this value in Trebi") //nolint:staticcheck // a sentence for the user
)

// File is the on-disk config schema.
type File struct {
	Token string `json:"token,omitempty"`
}

// Settings is the token and the place it came from.
type Settings struct {
	Token  string
	Source string
	Trebi  bool // Trebi mode
}

// Resolve finds the token. In Trebi mode it reads only DISCORD_TOKEN, and
// a --token flag is an error. Standalone, the order is --token,
// DISCORD_TOKEN, DISCORD_BOT_TOKEN, the nearest .env file from the working
// folder up, and ~/.cli-tools/discord-cli/config.json.
func Resolve(flagToken string) (Settings, error) {
	if _, ok := sdk.FromEnv(); ok {
		if flagToken != "" {
			return Settings{Trebi: true}, errors.New("--token: " + ErrTrebiSets.Error())
		}
		s := Settings{Token: os.Getenv(EnvToken), Trebi: true}
		if s.Token != "" {
			s.Source = "Trebi (" + EnvToken + ")"
		}
		return s, nil
	}
	if flagToken != "" {
		return Settings{Token: flagToken, Source: "flag (--token)"}, nil
	}
	for _, name := range []string{EnvToken, EnvBotToken} {
		if v := os.Getenv(name); v != "" {
			return Settings{Token: v, Source: "env (" + name + ")"}, nil
		}
	}
	if path, env := nearestDotEnv(); path != "" {
		for _, name := range []string{EnvToken, EnvBotToken} {
			if v := env[name]; v != "" {
				return Settings{Token: v, Source: ".env (" + path + ")"}, nil
			}
		}
	}
	cfg, err := Load()
	if err != nil {
		return Settings{}, err
	}
	if cfg.Token != "" {
		return Settings{Token: cfg.Token, Source: "config (" + Path() + ")"}, nil
	}
	return Settings{}, nil
}

// Dir returns the config directory (~/.cli-tools/discord-cli).
func Dir() string {
	home, _ := os.UserHomeDir() //nolint:errcheck // an empty home gives a relative path
	return filepath.Join(home, ".cli-tools", "discord-cli")
}

// Path returns the absolute path to the config file.
func Path() string {
	return filepath.Join(Dir(), "config.json")
}

// Load reads the config file. Returns an empty File if the file does not exist.
func Load() (*File, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, err
	}
	var cfg File
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes the bot token to the config file with mode 0600. It fails in
// Trebi mode: the token comes from the connection inputs.
func Save(token string) error {
	if _, ok := sdk.FromEnv(); ok {
		return ErrSetInTrebi
	}
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(File{Token: token}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o600)
}

// nearestDotEnv reads the first .env file from the working folder up.
func nearestDotEnv() (string, map[string]string) {
	wd, err := os.Getwd()
	if err != nil {
		return "", nil
	}
	for dir := wd; ; {
		path := filepath.Join(dir, ".env")
		if env, ok := ReadDotEnv(path); ok {
			return path, env
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// ReadDotEnv reads the KEY=value lines of a .env file.
func ReadDotEnv(path string) (map[string]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close() //nolint:errcheck // read only
	env := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimPrefix(strings.TrimSpace(k), "export ")] = v
	}
	return env, true
}
