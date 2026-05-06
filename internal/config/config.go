package config

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// File is the on-disk config schema.
type File struct {
	Token string `json:"token,omitempty"`
}

// Dir returns the config directory (~/.cli-tools/discord-cli).
func Dir() string {
	home, _ := os.UserHomeDir()
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

// Save writes the bot token to the config file with mode 0600.
func Save(token string) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	cfg := File{Token: token}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0600)
}

// LoadDotEnv reads a .env file and sets unset environment variables.
// Returns true if the file was found and loaded.
func LoadDotEnv(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
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
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	return true
}
