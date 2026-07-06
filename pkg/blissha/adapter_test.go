package blissha

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

func TestResolveHCI(t *testing.T) {
	got, err := resolveHCI("")
	require.NoError(t, err)
	require.Equal(t, "hci0", got)

	got, err = resolveHCI("hci1")
	require.NoError(t, err)
	require.Equal(t, "hci1", got)

	got, err = resolveHCI("  hci2  ")
	require.NoError(t, err)
	require.Equal(t, "hci2", got, "should trim whitespace")

	// A MAC that matches no adapter must error (rather than silently defaulting).
	_, err = resolveHCI("00:00:00:00:00:99")
	require.Error(t, err, "unknown adapter MAC should error")
}

// writeAdapter creates a fake sysfs adapter entry: <dir>/<hci>/address holding
// the MAC plus a trailing newline (as the real /sys/class/bluetooth files do).
func writeAdapter(t *testing.T, dir, hci, addr string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, hci), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, hci, "address"), []byte(addr+"\n"), 0o644))
}

func TestHCIForAddressCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	// The user's setup: one controller, 84:5C:F3:EC:8B:4F, as hci0. sysfs commonly
	// stores the address lower-cased, so use lower case in the fixture.
	writeAdapter(t, dir, "hci0", "84:5c:f3:ec:8b:4f")
	writeAdapter(t, dir, "hci1", "00:1a:7d:11:22:33")

	// An uppercase config value (what the user tried) resolves fine.
	got, err := hciForAddressIn(dir, "84:5C:F3:EC:8B:4F")
	require.NoError(t, err)
	require.Equal(t, "hci0", got)

	// Lowercase and whitespace-padded also work.
	got, err = hciForAddressIn(dir, "  84:5c:f3:ec:8b:4f  ")
	require.NoError(t, err)
	require.Equal(t, "hci0", got)

	got, err = hciForAddressIn(dir, "00:1A:7D:11:22:33")
	require.NoError(t, err)
	require.Equal(t, "hci1", got)

	// An address no adapter has → error.
	_, err = hciForAddressIn(dir, "AA:BB:CC:DD:EE:FF")
	require.Error(t, err)

	// A missing/empty sysfs dir (e.g. inside a container without host sysfs)
	// errors clearly rather than matching — this is the likely real-world cause.
	_, err = hciForAddressIn(filepath.Join(dir, "does-not-exist"), "84:5C:F3:EC:8B:4F")
	require.Error(t, err)
}

func TestAdapterIDFromPath(t *testing.T) {
	require.Equal(t, "hci0", adapterIDFromPath(dbus.ObjectPath("/org/bluez/hci0")))
	require.Equal(t, "hci1", adapterIDFromPath(dbus.ObjectPath("/org/bluez/hci1")))
	require.Equal(t, "hci0", adapterIDFromPath(dbus.ObjectPath("hci0")))
}
