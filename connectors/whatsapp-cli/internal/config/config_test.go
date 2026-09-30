package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDir(t *testing.T) {
	t.Run("trebi state dir wins", func(t *testing.T) {
		t.Setenv("TREBI_STATE_DIR", "/trebi/state")
		t.Setenv(EnvStoreDir, "/custom/store/path")
		if got, err := StoreDir(""); err != nil || got != "/trebi/state" {
			t.Errorf("StoreDir() = %q, %v, want /trebi/state", got, err)
		}
		if _, err := StoreDir("/flag"); err == nil || err.Error() != "--store: Trebi sets this value" {
			t.Errorf("StoreDir(flag) in Trebi mode: %v", err)
		}
	})

	t.Run("flag, then env, then home", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("TREBI_STATE_DIR", "")
		t.Setenv(EnvStoreDir, "/custom/store/path")
		if got, _ := StoreDir("/flag"); got != "/flag" {
			t.Errorf("StoreDir(flag) = %q", got)
		}
		if got, _ := StoreDir(""); got != "/custom/store/path" {
			t.Errorf("StoreDir() = %q, want the env value", got)
		}
		t.Setenv(EnvStoreDir, "")
		if got, _ := StoreDir(""); got != filepath.Join(home, ".whatsapp-cli") {
			t.Errorf("StoreDir() = %q, want the home default", got)
		}
	})
}

func TestDeviceSettings(t *testing.T) {
	t.Setenv(EnvDeviceLabel, "Laptop")
	t.Setenv(EnvDevicePlatform, "SAFARI")
	t.Setenv(EnvDeviceName, "Office")

	t.Setenv("TREBI_STATE_DIR", "")
	if d := DeviceSettings(); d.Label != "Laptop" || d.Platform != "SAFARI" {
		t.Errorf("standalone: %+v", d)
	}
	t.Setenv("TREBI_STATE_DIR", t.TempDir())
	if d := DeviceSettings(); d.Label != "Office" || d.Platform != "" {
		t.Errorf("Trebi mode: %+v", d)
	}
	os.Unsetenv(EnvDeviceName) //nolint:errcheck // t.Setenv restores it
	if d := DeviceSettings(); d.Label != "" {
		t.Errorf("Trebi mode reads a standalone name: %+v", d)
	}
}
