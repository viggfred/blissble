package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadConfigParsing(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, `
mqtt:
  broker: tcp://broker:1883
poll_interval: 45s
blinds:
  - name: Living Room
    mac: AA:BB:CC:DD:EE:01
    device_class: shade
  - name: Bedroom
    mac: AA:BB:CC:DD:EE:FF
    invert: true
`))
	require.NoError(t, err)
	require.Equal(t, "tcp://broker:1883", c.MQTT.Broker)
	require.Equal(t, 45*time.Second, c.Poll)
	require.Len(t, c.Blinds, 2)
	require.Equal(t, "Living Room", c.Blinds[0].Name)
	require.Equal(t, "shade", c.Blinds[0].DeviceClass)
	require.True(t, c.Blinds[1].Invert)
}

func TestLoadConfigPollDefaults(t *testing.T) {
	// Unset -> persistent default (30s).
	c, err := LoadConfig(writeConfig(t, "mqtt:\n  broker: b\n"))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, c.Poll)

	// Unset in on-demand -> hourly default.
	c, err = LoadConfig(writeConfig(t, "mqtt:\n  broker: b\nidle_disconnect: 30s\n"))
	require.NoError(t, err)
	require.Equal(t, time.Hour, c.Poll)

	// Explicit 0 -> command-only, NOT re-defaulted.
	c, err = LoadConfig(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: 0s\n"))
	require.NoError(t, err)
	require.Zero(t, c.Poll, "explicit 0 means command-only")

	// Negative -> treated as unset (mode default), not silently command-only.
	c, err = LoadConfig(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: -30s\n"))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, c.Poll, "negative poll falls back to the default cadence")
}

func TestLoadConfigErrors(t *testing.T) {
	// Missing file.
	_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err, "missing file")

	// Bad duration.
	_, err = LoadConfig(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: notaduration\n"))
	require.Error(t, err, "bad duration")

	// Malformed YAML.
	_, err = LoadConfig(writeConfig(t, "mqtt: [this is not a map\n"))
	require.Error(t, err, "malformed yaml")
}

func TestLoadConfigTLS(t *testing.T) {
	// insecure: true builds a config that skips verification.
	c, err := LoadConfig(writeConfig(t, "mqtt:\n  broker: tls://b:8883\n  tls:\n    insecure: true\n"))
	require.NoError(t, err)
	require.NotNil(t, c.MQTT.TLS)
	require.True(t, c.MQTT.TLS.InsecureSkipVerify)
	require.Equal(t, uint16(tls.VersionTLS12), c.MQTT.TLS.MinVersion)

	// A missing CA file is a load error.
	_, err = LoadConfig(writeConfig(t, "mqtt:\n  broker: b\n  tls:\n    ca_cert: /no/such/ca.pem\n"))
	require.Error(t, err, "missing ca_cert")

	// A client cert without a key is a load error.
	_, err = LoadConfig(writeConfig(t, "mqtt:\n  broker: b\n  tls:\n    cert: /x.pem\n"))
	require.Error(t, err, "cert without key")
}
