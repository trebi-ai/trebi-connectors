// Package config chooses between the standalone mode and Trebi mode. It is
// the only code that reads the home folder and the settings env names.
// Trebi mode is on when TREBI_STATE_DIR is set.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Standalone env names. Trebi mode does not read them.
const (
	EnvStoreDir       = "WHATSAPP_CLI_STORE_DIR"
	EnvDeviceLabel    = "WHATSAPP_CLI_DEVICE_LABEL"
	EnvDevicePlatform = "WHATSAPP_CLI_DEVICE_PLATFORM"
)

// EnvDeviceName is the device name input of the catalog manifest. Only
// Trebi mode reads it.
const EnvDeviceName = "WA_DEVICE_NAME"

// ErrTrebiSets is a flag that selects a store in Trebi mode.
var ErrTrebiSets = errors.New("Trebi sets this value") //nolint:staticcheck // Trebi is a name

// Trebi reports whether the program runs in Trebi mode.
func Trebi() bool {
	_, ok := sdk.FromEnv()
	return ok
}

// StoreDir returns the absolute store folder. In Trebi mode it is
// TREBI_STATE_DIR, and a --store flag is an error. Standalone, the order
// is --store, WHATSAPP_CLI_STORE_DIR, then ~/.whatsapp-cli.
func StoreDir(flagStore string) (string, error) {
	dir := flagStore
	if t, ok := sdk.FromEnv(); ok {
		if flagStore != "" {
			return "", errors.New("--store: " + ErrTrebiSets.Error())
		}
		dir = t.StateDir
	}
	if dir == "" {
		dir = os.Getenv(EnvStoreDir)
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			dir = ".whatsapp-cli"
		} else {
			dir = filepath.Join(home, ".whatsapp-cli")
		}
	}
	return filepath.Abs(dir)
}

// Device is the name and the platform that the phone shows for this
// linked device. Empty values keep the whatsmeow defaults.
type Device struct {
	Label    string
	Platform string
}

// DeviceSettings returns the device of this mode: WA_DEVICE_NAME in Trebi
// mode, the WHATSAPP_CLI_DEVICE_* names standalone.
func DeviceSettings() Device {
	if Trebi() {
		return Device{Label: strings.TrimSpace(os.Getenv(EnvDeviceName))}
	}
	return Device{
		Label:    strings.TrimSpace(os.Getenv(EnvDeviceLabel)),
		Platform: strings.TrimSpace(os.Getenv(EnvDevicePlatform)),
	}
}
