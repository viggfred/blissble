package blissha

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
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

// hciForAddress finds the adapter whose Bluetooth address equals mac. It asks
// BlueZ over D-Bus (the authoritative source, and the same channel the bridge
// uses to drive the adapters) and falls back to reading the sysfs address only
// if D-Bus can't be queried. The D-Bus path matters because some kernels no
// longer expose /sys/class/bluetooth/hciN/address (e.g. Ubuntu 26.04), and it
// also works inside a container that mounts the D-Bus socket but not host sysfs.
func hciForAddress(mac string) (string, error) {
	id, err := hciByAddressDBus(mac)
	if err == nil {
		return id, nil
	}
	if id, ferr := hciForAddressIn(sysBluetoothDir, mac); ferr == nil {
		return id, nil
	}
	return "", err // surface the primary (D-Bus) error
}

// hciByAddressDBus enumerates BlueZ adapters via the ObjectManager and returns
// the one whose org.bluez.Adapter1.Address matches mac (case-insensitively).
func hciByAddressDBus(mac string) (string, error) {
	conn, err := dbus.SystemBus() // shared connection; do not close
	if err != nil {
		return "", fmt.Errorf("connect system bus: %w", err)
	}
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object("org.bluez", "/").
		Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&managed); err != nil {
		return "", fmt.Errorf("query bluez adapters over d-bus: %w", err)
	}
	want := strings.TrimSpace(mac)
	for path, ifaces := range managed {
		props, ok := ifaces["org.bluez.Adapter1"]
		if !ok {
			continue
		}
		addr, _ := props["Address"].Value().(string)
		if strings.EqualFold(strings.TrimSpace(addr), want) {
			return adapterIDFromPath(path), nil
		}
	}
	return "", fmt.Errorf("no bluetooth adapter found with address %s", mac)
}

// adapterIDFromPath turns a BlueZ object path (/org/bluez/hci0) into its adapter
// id (hci0).
func adapterIDFromPath(p dbus.ObjectPath) string {
	s := string(p)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// hciForAddressIn matches mac against the sysfs address files under baseDir. This
// is the legacy fallback; matching is case-insensitive and whitespace-tolerant
// (sysfs address files end in a newline, and BlueZ may report upper or lower
// case). Injecting baseDir keeps the logic unit-testable.
func hciForAddressIn(baseDir, mac string) (string, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return "", fmt.Errorf("list bluetooth adapters in %s: %w", baseDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "hci") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(baseDir, name, "address"))
		if err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(string(data)), strings.TrimSpace(mac)) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no bluetooth adapter found with address %s (looked in %s)", mac, baseDir)
}
