package main

import (
	"os"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/config"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

func main() {
	applyDeviceLabel()
	if err := execute(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}

// applyDeviceLabel sets the name that the phone shows for this linked
// device. config.DeviceSettings picks the env names of the mode.
func applyDeviceLabel() {
	d := config.DeviceSettings()
	label := d.Label
	if d.Platform != "" {
		platform := parsePlatformType(d.Platform)
		store.DeviceProps.PlatformType = platform.Enum()
	}
	if label == "" {
		return
	}
	store.SetOSInfo(label, [3]uint32{0, 1, 0})
	store.BaseClientPayload.UserAgent.Device = proto.String(label)
	store.BaseClientPayload.UserAgent.Manufacturer = proto.String(label)
}

func parsePlatformType(raw string) waCompanionReg.DeviceProps_PlatformType {
	value := strings.TrimSpace(raw)
	if value == "" {
		return waCompanionReg.DeviceProps_CHROME
	}
	value = strings.ToUpper(value)
	if enumValue, ok := waCompanionReg.DeviceProps_PlatformType_value[value]; ok {
		return waCompanionReg.DeviceProps_PlatformType(enumValue)
	}
	return waCompanionReg.DeviceProps_CHROME
}
