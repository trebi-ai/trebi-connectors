package config

import (
	"os"
	"path/filepath"
)

// EnvStoreDir is the environment variable that overrides the default store
// directory. This is useful for Docker, CI, and multi-tenant deployments
// where the store path needs to be configured without passing --store on
// every invocation.
const EnvStoreDir = "WHATSAPP_CLI_STORE_DIR"

// EnvStateDir is the state directory that the Trebi daemon gives to a
// connector. It wins over EnvStoreDir.
const EnvStateDir = "TREBI_STATE_DIR"

// DefaultStoreDir returns the store directory to use when --store is not
// supplied. It checks TREBI_STATE_DIR, then WHATSAPP_CLI_STORE_DIR, then
// falls back to ~/.whatsapp-cli.
func DefaultStoreDir() string {
	if dir := os.Getenv(EnvStateDir); dir != "" {
		return dir
	}
	if dir := os.Getenv(EnvStoreDir); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".whatsapp-cli"
	}
	return filepath.Join(home, ".whatsapp-cli")
}
