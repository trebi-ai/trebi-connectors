// Package config loads and saves the slack-cli config file. It is the only
// code that picks between the two modes. In Trebi mode (TREBI_STATE_DIR is
// set) the config file is in the state folder, and the home folder and
// SLACK_CLI_CONFIG are not read.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

var (
	// ErrTrebiSets is a flag that selects a store or a credential in Trebi mode.
	ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name
	// ErrSetInTrebi is a command that saves a setting in Trebi mode.
	ErrSetInTrebi = errors.New("Set this value in Trebi") //nolint:staticcheck // a sentence for the user
)

// Trebi reports whether the program runs in Trebi mode.
func Trebi() bool {
	_, ok := sdk.FromEnv()
	return ok
}

// LoginHint tells the user how to add a workspace or log in again.
func LoginHint() string {
	if Trebi() {
		return "log in to Slack in Trebi"
	}
	return "run `slack-cli auth import` on this computer"
}

// Token sources.
const (
	SourceDesktop = "desktop" // Slack desktop app session
	SourceSlk     = "slk"     // gammons/slk token file
	SourceToken   = "token"   // pasted token
)

// Config holds the saved workspaces and the default workspace.
type Config struct {
	// Workspace is the name of the default workspace.
	Workspace  string               `toml:"workspace,omitempty"`
	Workspaces map[string]Workspace `toml:"workspaces,omitempty"`

	path string `toml:"-"`
}

// Workspace holds the credentials of one Slack workspace.
type Workspace struct {
	// Token is xoxp- (user), xoxb- (bot), or xoxc- (desktop/browser session).
	Token string `toml:"token,omitempty"`
	// Cookie is the "d" cookie (xoxd-...). xoxc- tokens need it.
	Cookie string `toml:"cookie,omitempty"`
	// AppToken is an app-level token (xapp-...) for Socket Mode.
	AppToken string `toml:"app_token,omitempty"`
	TeamID   string `toml:"team_id,omitempty"`
	TeamName string `toml:"team_name,omitempty"`
	Domain   string `toml:"domain,omitempty"`
	// URL is the team URL from auth.test, for example https://acme.slack.com/.
	URL    string `toml:"url,omitempty"`
	UserID string `toml:"user_id,omitempty"`
	User   string `toml:"user,omitempty"`
	Source string `toml:"source,omitempty"`
}

// Kind returns the token kind: user, bot, session, app, or "".
func (w Workspace) Kind() string { return TokenKind(w.Token) }

// TokenKind returns the kind of a token from its prefix.
func TokenKind(t string) string {
	switch {
	case strings.HasPrefix(t, "xoxp-"):
		return "user"
	case strings.HasPrefix(t, "xoxb-"):
		return "bot"
	case strings.HasPrefix(t, "xoxc-"):
		return "session"
	case strings.HasPrefix(t, "xapp-"):
		return "app"
	}
	return ""
}

// Dir returns the config folder: the state folder in Trebi mode, else
// ~/.cli-tools/slack-cli.
func Dir() (string, error) {
	if t, ok := sdk.FromEnv(); ok {
		return t.StateDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cli-tools", "slack-cli"), nil
}

// CacheDir returns the folder of the name caches: TREBI_CACHE_DIR (else the
// state folder) in Trebi mode, else the config folder.
func CacheDir() string {
	if t, ok := sdk.FromEnv(); ok {
		if t.CacheDir != "" {
			return t.CacheDir
		}
		return t.StateDir
	}
	d, _ := Dir() //nolint:errcheck // an empty folder turns the cache off
	return d
}

// Path returns the config file path. SLACK_CLI_CONFIG overrides it outside
// Trebi mode.
func Path() (string, error) {
	if p := os.Getenv("SLACK_CLI_CONFIG"); p != "" && !Trebi() {
		return p, nil
	}
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// Load reads the config file. A missing file is not an error.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{path: p}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	cfg.path = p
	return cfg, nil
}

// File returns the config file path.
func (c *Config) File() string { return c.path }

// Save writes the config with mode 0600 through a temp file and a rename.
func (c *Config) Save() error {
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

// Names returns the workspace names in order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Workspaces))
	for n := range c.Workspaces {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Set saves a workspace under name.
func (c *Config) Set(name string, w Workspace) {
	if c.Workspaces == nil {
		c.Workspaces = map[string]Workspace{}
	}
	c.Workspaces[name] = w
}

// Remove deletes a workspace. It clears the default when it was that workspace.
func (c *Config) Remove(name string) bool {
	if _, ok := c.Workspaces[name]; !ok {
		return false
	}
	delete(c.Workspaces, name)
	if c.Workspace == name {
		c.Workspace = ""
	}
	return true
}

// Find returns the workspace whose name, team id, domain, or team name is
// key (case-insensitive).
func (c *Config) Find(key string) (string, Workspace, bool) {
	if w, ok := c.Workspaces[key]; ok {
		return key, w, true
	}
	k := strings.ToLower(strings.TrimSpace(key))
	for _, n := range c.Names() {
		w := c.Workspaces[n]
		for _, v := range []string{n, w.TeamID, w.Domain, w.TeamName} {
			if v != "" && strings.ToLower(v) == k {
				return n, w, true
			}
		}
	}
	return "", Workspace{}, false
}

// FindTeam returns the workspace with the team id.
func (c *Config) FindTeam(teamID string) (string, bool) {
	for _, n := range c.Names() {
		if c.Workspaces[n].TeamID == teamID {
			return n, true
		}
	}
	return "", false
}

// Select returns the workspace for key. An empty key selects the default
// workspace, or the only workspace.
func (c *Config) Select(key string) (string, Workspace, error) {
	if key == "" {
		key = c.Workspace
	}
	if key == "" {
		switch len(c.Workspaces) {
		case 0:
			if Trebi() {
				return "", Workspace{}, errors.New("no Slack login in this connection: " + LoginHint())
			}
			return "", Workspace{}, errors.New("no workspace configured: run `slack-cli auth import` (Slack desktop app) or `slack-cli auth add`")
		case 1:
			n := c.Names()[0]
			return n, c.Workspaces[n], nil
		}
		return "", Workspace{}, fmt.Errorf("more than one workspace (%s): pass --workspace/-w or run `slack-cli auth use <name>`", strings.Join(c.Names(), ", "))
	}
	n, w, ok := c.Find(key)
	if !ok {
		return "", Workspace{}, fmt.Errorf("no workspace %q (known: %s)", key, strings.Join(c.Names(), ", "))
	}
	return n, w, nil
}

// Pick returns the workspace names for a list of keys, separated by commas
// or spaces. "*" or "all" picks every workspace; an empty list picks the
// default workspace. missing holds the keys that match no workspace.
func (c *Config) Pick(list string) (names, missing []string) {
	keys := strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' })
	if len(keys) == 0 {
		if n, _, err := c.Select(""); err == nil {
			return []string{n}, nil
		}
		return nil, nil
	}
	for _, k := range keys {
		if k == "*" || strings.EqualFold(k, "all") {
			return c.Names(), nil
		}
		n, _, ok := c.Find(k)
		switch {
		case !ok:
			missing = append(missing, k)
		case !slices.Contains(names, n):
			names = append(names, n)
		}
	}
	return names, missing
}

// MaskSecret returns a masked form of a secret for display.
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 12 {
		return "****"
	}
	return s[:5] + "..." + s[len(s)-4:]
}
