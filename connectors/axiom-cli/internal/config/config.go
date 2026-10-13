// Package config finds the settings of axiom-cli. It is the only code that
// reads the AXIOM_* env names and the config file in the home folder. In
// Trebi mode (TREBI_STATE_DIR is set) the values come only from the env that
// Trebi gives, and no file in the home folder counts.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the settings.
const (
	EnvToken  = "AXIOM_TOKEN"
	EnvOrgID  = "AXIOM_ORG_ID"
	EnvAPIURL = "AXIOM_URL"
	EnvEvery  = "AXIOM_EVERY"
)

// TokenLabel is the label of the AXIOM_TOKEN input in the catalog manifest.
const TokenLabel = "API token"

// DefaultAPIURL is the Axiom API.
const DefaultAPIURL = "https://api.axiom.co"

// Poll intervals of `serve`.
const (
	DefaultEvery = time.Minute
	MinEvery     = time.Minute
)

// Errors of Trebi mode.
var (
	// ErrTrebiSets is the error of a flag that selects a credential.
	ErrTrebiSets = errors.New("trebi sets this value: use the connection inputs")
	// ErrSetInTrebi is the error of a command that saves a setting.
	ErrSetInTrebi = errors.New("set this value in Trebi: the connection inputs hold the token")
)

// Trebi reports Trebi mode and the state folder.
func Trebi() (stateDir string, ok bool) {
	t, ok := sdk.FromEnv()
	return t.StateDir, ok
}

// Every reads AXIOM_EVERY, the poll interval of `serve`.
func Every() (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(EnvEvery))
	if v == "" {
		return DefaultEvery, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration such as 1m or 5m", EnvEvery, v)
	}
	return max(d, MinEvery), nil
}

// StateDir is the state folder of `serve`: TREBI_STATE_DIR in Trebi mode,
// else ~/.cli-tools/axiom-cli.
func StateDir() (string, error) {
	if dir, ok := Trebi(); ok {
		return dir, nil
	}
	p, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

type Source string

const (
	SourceFlag Source = "flag"
	SourceEnv  Source = "env"
	SourceFile Source = "file"
	SourceNone Source = "none"
	// SourceTrebi is the env that Trebi gives in Trebi mode.
	SourceTrebi Source = "trebi"
)

const (
	parentDir = ".cli-tools"
	subDir    = "axiom-cli"
	fileName  = "config.json"
)

// File is the on-disk shape of ~/.cli-tools/axiom-cli/config.json.
type File struct {
	Token  string `json:"token"`
	OrgID  string `json:"org_id,omitempty"`
	APIURL string `json:"api_url,omitempty"`
}

// Resolved is the effective configuration after applying flag/env/file precedence.
type Resolved struct {
	Token  string
	OrgID  string
	APIURL string
	Source Source
	File   *File
	Path   string
	Trebi  bool
}

// Path returns the absolute config file path: ~/.cli-tools/axiom-cli/config.json.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, parentDir, subDir, fileName), nil
}

// Load reads the config file. Returns (nil, path, nil) if the file does not exist.
func Load() (*File, string, error) {
	p, err := Path()
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, p, nil
		}
		return nil, p, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, p, err
	}
	return &f, p, nil
}

// Save writes the config file with mode 0600 (parent dir 0700).
func Save(f *File) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// Clear deletes the config file. No error if missing.
func Clear() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Mask returns a redacted form of the token suitable for display.
// Format: "<prefix>-************<last4>". If the token is short, the entire
// value is masked.
func Mask(token string) string {
	if len(token) <= 8 {
		return repeat("*", len(token))
	}
	last4 := token[len(token)-4:]
	// Find the prefix segment up to the first '-'. Many Axiom tokens look like
	// "xaat-..." or "xapt-..."; if there is no dash, fall back to the first 4 chars.
	prefixEnd := -1
	for i := 0; i < len(token) && i < 8; i++ {
		if token[i] == '-' {
			prefixEnd = i
			break
		}
	}
	var prefix string
	if prefixEnd > 0 {
		prefix = token[:prefixEnd+1]
	} else {
		prefix = token[:4] + "-"
	}
	return prefix + "************" + last4
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// ResolveInput captures the inputs needed to apply the precedence rules.
type ResolveInput struct {
	FlagSet       bool
	FlagToken     string
	FlagOrgID     string
	FlagAPIURL    string
	FlagOrgIDSet  bool
	FlagAPIURLSet bool
	EnvToken      string
	EnvTokenSet   bool
	EnvOrgID      string
	EnvAPIURL     string
}

// Resolve applies precedence: flag > env > file. The returned Source describes
// where Token came from. In Trebi mode, only the env counts, and a flag
// gives ErrTrebiSets.
func Resolve(in ResolveInput) (*Resolved, error) {
	if _, ok := Trebi(); ok {
		if in.FlagSet || in.FlagOrgIDSet || in.FlagAPIURLSet {
			return nil, ErrTrebiSets
		}
		r := &Resolved{Token: in.EnvToken, OrgID: in.EnvOrgID, APIURL: in.EnvAPIURL, Source: SourceNone, Trebi: true}
		if r.Token != "" {
			r.Source = SourceTrebi
		}
		if r.APIURL == "" {
			r.APIURL = DefaultAPIURL
		}
		return r, nil
	}
	file, path, err := Load()
	if err != nil {
		return nil, err
	}

	r := &Resolved{
		File:   file,
		Path:   path,
		Source: SourceNone,
	}

	switch {
	case in.FlagSet && in.FlagToken != "":
		r.Token = in.FlagToken
		r.Source = SourceFlag
	case in.EnvTokenSet && in.EnvToken != "":
		r.Token = in.EnvToken
		r.Source = SourceEnv
	case file != nil && file.Token != "":
		r.Token = file.Token
		r.Source = SourceFile
	}

	switch {
	case in.FlagOrgIDSet && in.FlagOrgID != "":
		r.OrgID = in.FlagOrgID
	case in.EnvOrgID != "":
		r.OrgID = in.EnvOrgID
	case file != nil && file.OrgID != "":
		r.OrgID = file.OrgID
	}

	switch {
	case in.FlagAPIURLSet && in.FlagAPIURL != "":
		r.APIURL = in.FlagAPIURL
	case in.EnvAPIURL != "":
		r.APIURL = in.EnvAPIURL
	case file != nil && file.APIURL != "":
		r.APIURL = file.APIURL
	default:
		r.APIURL = in.FlagAPIURL // urfave/cli surfaces the default via the flag
	}
	if r.APIURL == "" {
		r.APIURL = DefaultAPIURL
	}

	return r, nil
}
