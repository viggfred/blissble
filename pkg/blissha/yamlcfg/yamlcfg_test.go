package yamlcfg

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"

	"github.com/viggfred/blissble/pkg/blissha"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadConfigParsing(t *testing.T) {
	c, err := Load(writeConfig(t, `
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
	c, err := Load(writeConfig(t, "mqtt:\n  broker: b\n"))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, c.Poll)

	// Unset in on-demand -> hourly default.
	c, err = Load(writeConfig(t, "mqtt:\n  broker: b\nidle_disconnect: 30s\n"))
	require.NoError(t, err)
	require.Equal(t, time.Hour, c.Poll)

	// Explicit 0 -> command-only, NOT re-defaulted.
	c, err = Load(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: 0s\n"))
	require.NoError(t, err)
	require.Zero(t, c.Poll, "explicit 0 means command-only")

	// Negative -> treated as unset (mode default), not silently command-only.
	c, err = Load(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: -30s\n"))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, c.Poll, "negative poll falls back to the default cadence")
}

func TestLoadConfigErrors(t *testing.T) {
	// Missing file.
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err, "missing file")

	// Bad duration.
	_, err = Load(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: notaduration\n"))
	require.Error(t, err, "bad duration")

	// Malformed YAML.
	_, err = Load(writeConfig(t, "mqtt: [this is not a map\n"))
	require.Error(t, err, "malformed yaml")
}

func TestLoadConfigTLS(t *testing.T) {
	// insecure: true builds a config that skips verification.
	c, err := Load(writeConfig(t, "mqtt:\n  broker: tls://b:8883\n  tls:\n    insecure: true\n"))
	require.NoError(t, err)
	require.NotNil(t, c.MQTT.TLS)
	require.True(t, c.MQTT.TLS.InsecureSkipVerify)
	require.Equal(t, uint16(tls.VersionTLS12), c.MQTT.TLS.MinVersion)

	// A missing CA file is a load error.
	_, err = Load(writeConfig(t, "mqtt:\n  broker: b\n  tls:\n    ca_cert: /no/such/ca.pem\n"))
	require.Error(t, err, "missing ca_cert")

	// A client cert without a key is a load error.
	_, err = Load(writeConfig(t, "mqtt:\n  broker: b\n  tls:\n    cert: /x.pem\n"))
	require.Error(t, err, "cert without key")
}

func TestLoadConfigAutomation(t *testing.T) {
	c, err := Load(writeConfig(t, `
mqtt: { broker: tcp://b:1883 }
location: { latitude: 59.91, longitude: 10.75, timezone: Europe/Oslo }
blinds:
  - name: LR
    mac: AA:BB:CC:DD:EE:01
    automation:
      mode: sun_glare
      window: { azimuth: 270, sensitivity: 0.6 }
      require_occupancy: true
      when_unoccupied: thermal
  - name: BR
    mac: AA:BB:CC:DD:EE:02
    automation:
      mode: schedule
      schedule:
        - days: [mon, tue]
          close_at: "22:30"
          open_at: "07:00"
          ramp: 20m
`))
	require.NoError(t, err)
	require.NotNil(t, c.Location)
	require.InDelta(t, 59.91, c.Location.Lat, 1e-9)
	require.NotNil(t, c.Location.Zone)
	require.Equal(t, "Europe/Oslo", c.Location.Zone.String())

	lr := c.Blinds[0].Automation
	require.Equal(t, blissha.ModeSunGlare, lr.Mode)
	require.InDelta(t, 270, lr.Window.AzimuthDeg, 1e-9)
	require.True(t, lr.RequireOccupancy)
	require.Equal(t, blissha.ModeThermal, lr.WhenUnoccupied)

	br := c.Blinds[1].Automation
	require.Equal(t, blissha.ModeSchedule, br.Mode)
	require.Len(t, br.Schedule, 1)
	require.Equal(t, blissha.Mon|blissha.Tue, br.Schedule[0].Days)
	require.Equal(t, blissha.Clock(22*60+30), br.Schedule[0].CloseAt)
	require.Equal(t, blissha.Clock(7*60), br.Schedule[0].OpenAt)
	require.Equal(t, 20*time.Minute, br.Schedule[0].Ramp)

	require.NoError(t, c.Normalize(), "defaults + validation should pass")
}

func TestLoadConfigAutomationErrors(t *testing.T) {
	// Unknown IANA timezone.
	_, err := Load(writeConfig(t, "mqtt: {broker: b}\nlocation: {latitude: 1, longitude: 2, timezone: Nowhere/Nope}\n"))
	require.Error(t, err, "bad timezone")

	// Unknown mode.
	_, err = Load(writeConfig(t, "mqtt: {broker: b}\nblinds:\n  - {name: X, mac: AA:BB:CC:DD:EE:01, automation: {mode: nonsense}}\n"))
	require.Error(t, err, "bad mode")

	// Out-of-range clock.
	_, err = Load(writeConfig(t, "mqtt: {broker: b}\nblinds:\n  - name: X\n    mac: AA:BB:CC:DD:EE:01\n    automation: {mode: schedule, schedule: [{open_at: \"25:00\", close_at: \"22:00\"}]}\n"))
	require.Error(t, err, "bad clock")

	// Unknown day token.
	_, err = Load(writeConfig(t, "mqtt: {broker: b}\nblinds:\n  - name: X\n    mac: AA:BB:CC:DD:EE:01\n    automation: {mode: schedule, schedule: [{days: [funday], open_at: \"07:00\", close_at: \"22:00\"}]}\n"))
	require.Error(t, err, "bad day")
}

func TestLoadConfigSunModeNeedsLocation(t *testing.T) {
	// Loads fine, but Normalize (validation) rejects a sun mode with no location.
	c, err := Load(writeConfig(t, "mqtt: {broker: b}\nblinds:\n  - {name: X, mac: AA:BB:CC:DD:EE:01, automation: {mode: sun_glare, window: {azimuth: 180}}}\n"))
	require.NoError(t, err)
	require.Error(t, c.Normalize(), "sun mode requires a location")
}

func TestLoadConfigZeroValuesHonored(t *testing.T) {
	base := "mqtt: {broker: b}\nlocation: {latitude: 1, longitude: 2}\n"

	// shade_closed: 0 must stay 0 (full close), not silently become 25.
	c, err := Load(writeConfig(t, base+"blinds:\n  - name: X\n    mac: AA:BB:CC:DD:EE:01\n    automation: {mode: sun_shade, window: {azimuth: 180}, shade_closed: 0}\n"))
	require.NoError(t, err)
	require.Equal(t, 0, c.Blinds[0].Automation.ShadeClosedHA)
	require.NoError(t, c.Normalize())
	require.Equal(t, 0, c.Blinds[0].Automation.ShadeClosedHA, "Normalize must not re-default an explicit 0")

	// Absent shade_closed → default 25.
	c, err = Load(writeConfig(t, base+"blinds:\n  - name: X\n    mac: AA:BB:CC:DD:EE:01\n    automation: {mode: sun_shade, window: {azimuth: 180}}\n"))
	require.NoError(t, err)
	require.Equal(t, 25, c.Blinds[0].Automation.ShadeClosedHA)

	// thermal: summer_close:0 (full close) and cold_temp_c:0 (freezing) are honored.
	c, err = Load(writeConfig(t, base+"blinds:\n  - name: X\n    mac: AA:BB:CC:DD:EE:01\n    automation: {mode: thermal, window: {azimuth: 180}, thermal: {summer_close: 0, cold_temp_c: 0}}\n"))
	require.NoError(t, err)
	require.Equal(t, 0, c.Blinds[0].Automation.Thermal.SummerCloseHA)
	require.Zero(t, c.Blinds[0].Automation.Thermal.ColdTempC)
	require.EqualValues(t, 24, c.Blinds[0].Automation.Thermal.HotTempC, "absent hot_temp_c → default 24")
}

// TestEmbeddedSection covers the second consumer: the schema types used
// as a section inside a larger application config (e.g. gridtariff's
// blinds: section), converted with Config.Build.
func TestEmbeddedSection(t *testing.T) {
	type appConfig struct {
		OtherStuff string `yaml:"otherStuff"`
		Blinds     Config `yaml:"blinds"`
	}
	var app appConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
otherStuff: hello
blinds:
  mqtt:
    broker: tcp://b:1883
  idle_disconnect: 30s
  blinds:
    - name: LR
      mac: AA:BB:CC:DD:EE:01
`), &app))

	cfg, err := app.Blinds.Build()
	require.NoError(t, err)
	require.Equal(t, "tcp://b:1883", cfg.MQTT.Broker)
	require.Equal(t, 30*time.Second, cfg.IdleDisconnect)
	require.Equal(t, time.Hour, cfg.Poll, "on-demand default applies through Build")
	require.Len(t, cfg.Blinds, 1)
	require.NoError(t, cfg.Normalize())
}
