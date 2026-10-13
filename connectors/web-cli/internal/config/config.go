// Package config finds the settings of web-cli. It is the only code that
// reads the WEB_* env names and the home folder. In Trebi mode
// (TREBI_STATE_DIR is set) the values come only from the env that Trebi
// gives, and the state goes to TREBI_STATE_DIR.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the settings.
const (
	EnvURLs  = "WEB_URLS"
	EnvEvery = "WEB_EVERY"
)

// URLsLabel is the label of the WEB_URLS input in the catalog manifest.
const URLsLabel = "Web addresses"

// Check intervals.
const (
	DefaultEvery = 30 * time.Minute
	MinEvery     = 10 * time.Minute
)

// Target is one address to watch. Selector is a CSS selector for a page;
// empty means the whole text.
type Target struct {
	URL      string `json:"url"`
	Selector string `json:"selector,omitempty"`
}

// Settings is the resolved configuration.
type Settings struct {
	Targets []Target
	// URLsGiven is true when WEB_URLS has a value, also one with no address.
	URLsGiven bool
	Every     time.Duration
	StateDir  string // "" keeps the state in memory
	Trebi     bool
}

// Resolve reads the settings from the env. Outside Trebi, the state goes to
// ~/.cli-tools/web-cli.
func Resolve() (Settings, error) {
	t, trebi := sdk.FromEnv()
	urls := os.Getenv(EnvURLs)
	s := Settings{Trebi: trebi, StateDir: t.StateDir, Targets: ParseTargets(urls), URLsGiven: strings.TrimSpace(urls) != ""}
	every, err := ParseEvery(os.Getenv(EnvEvery))
	if err != nil {
		return s, err
	}
	s.Every = every
	if !trebi {
		home, err := os.UserHomeDir()
		if err != nil {
			return s, err
		}
		s.StateDir = filepath.Join(home, ".cli-tools", "web-cli")
	}
	return s, nil
}

// Missing returns the missing required input, or nil.
func (s Settings) Missing() *sdk.MissingInput {
	if len(s.Targets) == 0 {
		return &sdk.MissingInput{Name: EnvURLs, Label: URLsLabel}
	}
	return nil
}

// ParseTargets reads a list of addresses. A word that starts with http://
// or https:// starts a new target. The other words after it are the CSS
// selector of that target. Newlines and commas after an address separate
// targets.
func ParseTargets(v string) []Target {
	var out []Target
	for line := range strings.SplitSeq(v, "\n") {
		cur := -1
		var sel []string
		flush := func() {
			if cur >= 0 {
				out[cur].Selector = strings.TrimSpace(strings.Join(sel, " "))
			}
			sel = nil
		}
		for f := range strings.FieldsSeq(line) {
			if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
				flush()
				out = append(out, Target{URL: strings.TrimRight(f, ",;")})
				cur = len(out) - 1
				continue
			}
			if cur >= 0 {
				sel = append(sel, f)
			}
		}
		flush()
	}
	return out
}

// ParseEvery reads a check interval. Empty gives DefaultEvery. A value under
// MinEvery becomes MinEvery.
func ParseEvery(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultEvery, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration such as 30m or 1h", EnvEvery, v)
	}
	return max(d, MinEvery), nil
}
