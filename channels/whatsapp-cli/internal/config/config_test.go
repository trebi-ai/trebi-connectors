package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultStoreDir(t *testing.T) {
	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "/custom/store/path")
		got := DefaultStoreDir()
		if got != "/custom/store/path" {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, "/custom/store/path")
		}
	})

	t.Run("falls back to ~/.whatsapp-cli when env unset", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "")
		got := DefaultStoreDir()
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".whatsapp-cli")
		if got != want {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, want)
		}
	})

	t.Run("env var constant is WHATSAPP_CLI_STORE_DIR", func(t *testing.T) {
		if EnvStoreDir != "WHATSAPP_CLI_STORE_DIR" {
			t.Errorf("EnvStoreDir = %q, want %q", EnvStoreDir, "WHATSAPP_CLI_STORE_DIR")
		}
	})
}
