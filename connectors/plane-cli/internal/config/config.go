// Package config loads and saves the plane-cli config file. It is the only
// code that picks between the modes. In Trebi mode (TREBI_STATE_DIR is
// set) the settings come only from the connection inputs PLANE_API_KEY and
// PLANE_HOST, and the home folder and PLANE_CONFIG are not read. In managed
// mode (PLANE_MANAGED_BY is set, for example by OverSire) the host app sets
// the values in the env and the CLI reads no config file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Trebi reports whether the program runs in Trebi mode.
func Trebi() bool {
	_, ok := sdk.FromEnv()
	return ok
}

// ManagedBy gives the name of the host app that manages the settings: "Trebi"
// in Trebi mode, the value of PLANE_MANAGED_BY in managed mode, else "".
func ManagedBy() string {
	if Trebi() {
		return "Trebi"
	}
	return strings.TrimSpace(os.Getenv("PLANE_MANAGED_BY"))
}

// RefuseFlag gives the error for a flag that selects a credential or a host
// when the host app manages the settings. It gives nil outside managed mode.
func RefuseFlag() error {
	if Trebi() {
		return sdk.ErrTrebiSets
	}
	if by := ManagedBy(); by != "" {
		return fmt.Errorf("%s sets this value", by)
	}
	return nil
}

// RefuseSave gives the error for a command that writes a setting when the
// host app manages the settings. It gives nil outside managed mode.
func RefuseSave() error {
	if Trebi() {
		return sdk.ErrSetInTrebi
	}
	if by := ManagedBy(); by != "" {
		return fmt.Errorf("Set this value in %s", by) //nolint:staticcheck // a sentence for the user
	}
	return nil
}

// RefuseLongRun gives the error for a command that does not end (listen,
// serve) when a host app other than Trebi manages the settings. Trebi starts
// serve itself. It gives nil in the other modes.
func RefuseLongRun(command string) error {
	if by := ManagedBy(); by != "" && !Trebi() {
		return fmt.Errorf("%s does not run %s", by, command)
	}
	return nil
}

// DefaultHost is the Plane Cloud API host. Self-hosted instances use their own URL.
const DefaultHost = "https://api.plane.so"

// Config holds the credentials and defaults for plane-cli.
type Config struct {
	APIKey    string `toml:"api_key,omitempty"`
	Host      string `toml:"host,omitempty"`
	Workspace string `toml:"workspace,omitempty"`
	Project   string `toml:"project,omitempty"`
	// Workspaces holds credentials for one workspace slug. A Plane token
	// works only in the workspace that made it.
	Workspaces map[string]Workspace `toml:"workspaces,omitempty"`

	path string `toml:"-"`
	// keySet and hostSet are true when a flag or env var set the value.
	keySet, hostSet bool
	// baseKey and baseHost keep the values before a workspace entry replaced them.
	baseKey, baseHost string
	based             bool
}

// Workspace is the credentials of one workspace. Empty fields use the top-level values.
type Workspace struct {
	APIKey string `toml:"api_key,omitempty"`
	Host   string `toml:"host,omitempty"`
}

// Path returns ~/.cli-tools/plane-cli/config.toml. PLANE_CONFIG overrides it.
// Trebi and managed mode have no config file: Path returns "".
func Path() (string, error) {
	if ManagedBy() != "" {
		return "", nil
	}
	if p := os.Getenv("PLANE_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cli-tools", "plane-cli", "config.toml"), nil
}

// Load reads the config file (a missing file is not an error) and applies env overrides.
func Load() (*Config, error) {
	cfg, err := LoadFile()
	if err != nil {
		return nil, err
	}
	cfg.applyEnv()
	return cfg, nil
}

// LoadFile reads only the config file, without env overrides. Use it before Save
// so that env values do not leak into the file.
func LoadFile() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{path: p}
	if p == "" {
		return cfg, nil
	}
	if data, err := os.ReadFile(p); err == nil {
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cfg.path = p
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("PLANE_API_KEY"); v != "" {
		c.SetAPIKey(v)
	}
	if v := os.Getenv("PLANE_HOST"); v != "" {
		c.SetHost(v)
	}
	if v := os.Getenv("PLANE_WORKSPACE"); v != "" {
		c.Workspace = v
	}
	if v := os.Getenv("PLANE_PROJECT"); v != "" {
		c.Project = v
	}
}

// SetAPIKey sets the API key from a flag or env var. It then has priority
// over the workspace key in the file.
func (c *Config) SetAPIKey(v string) { c.APIKey, c.keySet = v, true }

// SetHost sets the host from a flag or env var.
func (c *Config) SetHost(v string) { c.Host, c.hostSet = v, true }

// UseWorkspaceCredentials applies the [workspaces.<slug>] values of the
// current workspace, when no flag or env var set them. Order: flag → env →
// workspace entry → top-level file value. Call it after the flags are applied.
func (c *Config) UseWorkspaceCredentials() {
	if !c.based {
		c.baseKey, c.baseHost, c.based = c.APIKey, c.Host, true
	}
	w, ok := c.Workspaces[c.Workspace]
	if !ok {
		return
	}
	if w.APIKey != "" && !c.keySet {
		c.APIKey = w.APIKey
	}
	if w.Host != "" && !c.hostSet {
		c.Host = w.Host
	}
}

// ForWorkspace returns a copy for the workspace slug, with its credentials applied.
func (c *Config) ForWorkspace(slug string) *Config {
	cp := *c
	if cp.based {
		if !cp.keySet {
			cp.APIKey = cp.baseKey
		}
		if !cp.hostSet {
			cp.Host = cp.baseHost
		}
	}
	cp.Workspace = slug
	cp.UseWorkspaceCredentials()
	return &cp
}

// SetWorkspace saves credentials for a workspace slug.
func (c *Config) SetWorkspace(slug string, w Workspace) {
	if c.Workspaces == nil {
		c.Workspaces = map[string]Workspace{}
	}
	c.Workspaces[slug] = w
}

// BaseURL returns the API host without a trailing slash.
func (c *Config) BaseURL() string {
	h := strings.TrimRight(strings.TrimSpace(c.Host), "/")
	if h == "" {
		return DefaultHost
	}
	return h
}

// File returns the config file path.
func (c *Config) File() string { return c.path }

// Save writes the config with mode 0600 through a temp file and a rename.
func (c *Config) Save() error {
	if err := RefuseSave(); err != nil {
		return err
	}
	if c.path == "" {
		p, err := Path()
		if err != nil {
			return err
		}
		c.path = p
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return sdk.WriteFileAtomic(c.path, buf.Bytes(), 0o600)
}

// Delete removes the config file. A missing file is not an error.
func (c *Config) Delete() error {
	if err := RefuseSave(); err != nil {
		return err
	}
	if c.path == "" {
		return nil
	}
	if err := os.Remove(c.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Redacted returns a copy with the API key masked.
func (c *Config) Redacted() *Config {
	cp := *c
	cp.APIKey = MaskSecret(cp.APIKey)
	cp.Workspaces = map[string]Workspace{}
	for slug, w := range c.Workspaces {
		w.APIKey = MaskSecret(w.APIKey)
		cp.Workspaces[slug] = w
	}
	return &cp
}

// Validate checks that an API key is present.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.APIKey) == "" {
		if by := ManagedBy(); by != "" {
			return fmt.Errorf("no API key in this connection: set the API key in %s", by)
		}
		return errors.New("no api_key configured (run `plane-cli auth login` or set PLANE_API_KEY)")
	}
	return nil
}

// MaskSecret returns a masked form of a secret for display.
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
