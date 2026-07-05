package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sysBluetoothDir = "/sys/class/bluetooth"

// resolveHCI maps a config adapter spec to a BlueZ adapter id (hciN):
//
//	""     -> "hci0" (the default adapter)
//	"hciN" -> "hciN"
//	MAC    -> the hciN whose Bluetooth address matches (stable across replug)
//
// Matching by MAC is preferred with multiple identical dongles, since hciN
// numbering is not stable across reboots.
func resolveHCI(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return "hci0", nil
	case strings.HasPrefix(spec, "hci"):
		return spec, nil
	default:
		return hciForAddress(spec)
	}
}

// hciForAddress finds the adapter whose Bluetooth address equals mac by reading
// /sys/class/bluetooth/hci*/address.
func hciForAddress(mac string) (string, error) {
	entries, err := os.ReadDir(sysBluetoothDir)
	if err != nil {
		return "", fmt.Errorf("list bluetooth adapters: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "hci") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sysBluetoothDir, name, "address"))
		if err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(string(data)), strings.TrimSpace(mac)) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no bluetooth adapter found with address %s", mac)
}
