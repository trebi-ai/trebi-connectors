// Package config finds the settings of host-cli. It is the only code that
// reads the SSH_* and HOST_* env names and the home folder. In Trebi mode
// (TREBI_STATE_DIR is set) the values come only from the env that Trebi
// gives, and the server key is pinned or kept in TREBI_STATE_DIR.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the settings.
const (
	EnvHost      = "SSH_HOST"
	EnvUser      = "SSH_USER"
	EnvKey       = "SSH_KEY"
	EnvAgent     = "SSH_AUTH_SOCK"
	EnvHostKey   = "SSH_HOST_KEY"
	EnvEvery     = "HOST_EVERY"
	EnvCPUAbove  = "HOST_CPU_ABOVE"
	EnvMemAbove  = "HOST_MEMORY_ABOVE"
	EnvDiskAbove = "HOST_DISK_ABOVE"
	EnvSamples   = "HOST_SAMPLES"
	// EnvKeyFile is the path of a key file, outside Trebi only.
	EnvKeyFile = "SSH_KEY_FILE"
)

// Labels of the inputs in the catalog manifest.
const (
	HostLabel = "Server address"
	UserLabel = "User name"
	KeyLabel  = "Private key"
)

// KnownHostsFile keeps the first server key in the state folder (trust on
// first use), when no SSH_HOST_KEY is set in Trebi mode.
const KnownHostsFile = "known_hosts"

// Defaults.
const (
	DefaultEvery = time.Minute
	MinEvery     = 10 * time.Second
	DefaultAbove = 90.0
)

// ErrTrebiSets is the error of a flag that selects a credential in Trebi
// mode.
var ErrTrebiSets = errors.New("trebi sets this value: use the connection inputs")

// Settings is the resolved configuration.
type Settings struct {
	Addr    string // host:port
	User    string
	Key     []byte // PEM private key; nil uses the agent only
	Agent   string // agent socket path; "" uses no agent
	HostKey string // pinned server key in authorized_keys or known_hosts form
	// KnownHosts is the known_hosts file to check the server key against.
	// In Trebi mode with no HostKey, it is a file in the state folder that
	// gets the first key.
	KnownHosts string
	TOFU       bool

	Every     time.Duration
	CPUAbove  float64
	MemAbove  float64
	DiskAbove float64
	Samples   bool

	StateDir string
	Trebi    bool
}

// Flags are the command line values of the standalone tool.
type Flags struct {
	Host    string
	User    string
	KeyFile string
}

// Resolve reads the settings. In Trebi mode, a flag that selects the
// server, the user, or the key gives ErrTrebiSets.
func Resolve(f Flags) (Settings, error) {
	t, trebi := sdk.FromEnv()
	s := Settings{Trebi: trebi, StateDir: t.StateDir}
	if trebi && (f.Host != "" || f.User != "" || f.KeyFile != "") {
		return s, ErrTrebiSets
	}
	s.Addr = withPort(firstOf(f.Host, os.Getenv(EnvHost)))
	s.User = firstOf(f.User, os.Getenv(EnvUser))
	s.Agent = strings.TrimSpace(os.Getenv(EnvAgent))
	s.HostKey = strings.TrimSpace(os.Getenv(EnvHostKey))
	if k := strings.TrimSpace(os.Getenv(EnvKey)); k != "" {
		s.Key = []byte(k + "\n")
	}
	var err error
	if s.Every, err = duration(EnvEvery, DefaultEvery); err != nil {
		return s, err
	}
	for env, dst := range map[string]*float64{EnvCPUAbove: &s.CPUAbove, EnvMemAbove: &s.MemAbove, EnvDiskAbove: &s.DiskAbove} {
		if *dst, err = percent(env); err != nil {
			return s, err
		}
	}
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvSamples))); v {
	case "", "yes", "true", "on", "1":
		s.Samples = true
	case "no", "false", "off", "0":
	default:
		return s, fmt.Errorf("%s: %q is not yes or no", EnvSamples, v)
	}
	if trebi {
		if s.HostKey == "" && s.StateDir != "" {
			s.KnownHosts, s.TOFU = filepath.Join(s.StateDir, KnownHostsFile), true
		}
		return s, nil
	}
	return s.standalone(f.KeyFile)
}

// standalone adds the defaults of the tool: a key file and the known_hosts
// file of the user.
func (s Settings) standalone(keyFile string) (Settings, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	s.StateDir = filepath.Join(home, ".cli-tools", "host-cli")
	if s.HostKey == "" {
		s.KnownHosts = filepath.Join(home, ".ssh", "known_hosts")
	}
	if s.Key != nil {
		return s, nil
	}
	keyFile = firstOf(keyFile, os.Getenv(EnvKeyFile))
	if keyFile != "" {
		if s.Key, err = os.ReadFile(keyFile); err != nil {
			return s, fmt.Errorf("key file: %w", err)
		}
		return s, nil
	}
	if s.Agent != "" {
		return s, nil
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			s.Key = b
			return s, nil
		}
	}
	return s, nil
}

// Missing returns the first missing required input, or nil.
func (s Settings) Missing() *sdk.MissingInput {
	switch {
	case s.Addr == "":
		return &sdk.MissingInput{Name: EnvHost, Label: HostLabel}
	case s.User == "":
		return &sdk.MissingInput{Name: EnvUser, Label: UserLabel}
	case s.Key == nil && s.Agent == "":
		return &sdk.MissingInput{Name: EnvKey, Label: KeyLabel}
	}
	return nil
}

func duration(env string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration such as 60s or 5m", env, v)
	}
	return max(d, MinEvery), nil
}

func percent(env string) (float64, error) {
	v := strings.TrimSuffix(strings.TrimSpace(os.Getenv(env)), "%")
	if v == "" {
		return DefaultAbove, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 || n > 100 {
		return 0, fmt.Errorf("%s: %q is not a percent from 1 to 100", env, v)
	}
	return n, nil
}

func withPort(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), "22")
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
