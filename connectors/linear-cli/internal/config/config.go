// Package config finds the API key. It is the only code that reads the
// home folder, the .env files, and the key env name. In Trebi mode
// (TREBI_STATE_DIR is set) it reads LINEAR_API_KEY only.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// EnvKey is the env name of the API key and the name of the catalog input.
const EnvKey = "LINEAR_API_KEY"

// KeyLabel is the label of the key input in the catalog manifest.
const KeyLabel = "API key"

// ErrTrebiSets is a flag that selects a credential in Trebi mode.
var ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name

// File is the on-disk config schema.
type File struct {
	Key string `json:"key,omitempty"`
}

// Settings is the key and the place it came from.
type Settings struct {
	Key    string
	Source string
	Trebi  bool // Trebi mode
}

// Resolve finds the key. In Trebi mode it reads only LINEAR_API_KEY, and a
// --key flag is an error. Standalone, the order is --key, LINEAR_API_KEY,
// the nearest .env file from the working folder up, and
// ~/.cli-tools/linear-cli/config.json.
func Resolve(flagKey string) (Settings, error) {
	if _, ok := sdk.FromEnv(); ok {
		if flagKey != "" {
			return Settings{Trebi: true}, fmt.Errorf("--key: %w", ErrTrebiSets)
		}
		s := Settings{Key: os.Getenv(EnvKey), Trebi: true}
		if s.Key != "" {
			s.Source = "Trebi (" + EnvKey + ")"
		}
		return s, nil
	}
	if flagKey != "" {
		return Settings{Key: flagKey, Source: "flag (--key)"}, nil
	}
	if v := os.Getenv(EnvKey); v != "" {
		return Settings{Key: v, Source: "env (" + EnvKey + ")"}, nil
	}
	if path, env := nearestDotEnv(); env[EnvKey] != "" {
		return Settings{Key: env[EnvKey], Source: ".env (" + path + ")"}, nil
	}
	b, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return Settings{}, err
	}
	if f.Key != "" {
		return Settings{Key: f.Key, Source: "config (" + Path() + ")"}, nil
	}
	return Settings{}, nil
}

// Path returns ~/.cli-tools/linear-cli/config.json.
func Path() string {
	home, _ := os.UserHomeDir() //nolint:errcheck // an empty home gives a relative path
	return filepath.Join(home, ".cli-tools", "linear-cli", "config.json")
}

func nearestDotEnv() (string, map[string]string) {
	wd, err := os.Getwd()
	if err != nil {
		return "", nil
	}
	for dir := wd; ; {
		path := filepath.Join(dir, ".env")
		if env, ok := readDotEnv(path); ok {
			return path, env
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func readDotEnv(path string) (map[string]string, bool) {
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
