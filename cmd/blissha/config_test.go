package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaultsAndParsing(t *testing.T) {
	path := writeConfig(t, `
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
`)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.MQTT.ClientID != "blissble" || c.MQTT.DiscoveryPrefix != "homeassistant" || c.MQTT.BaseTopic != "blissble" {
		t.Errorf("defaults not applied: %+v", c.MQTT)
	}
	if time.Duration(c.Poll) != 45*time.Second {
		t.Errorf("poll = %v, want 45s", time.Duration(c.Poll))
	}
	if len(c.Blinds) != 2 || c.Blinds[0].Name != "Living Room" || !c.Blinds[1].Invert {
		t.Errorf("blinds parsed wrong: %+v", c.Blinds)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	// Missing broker.
	if _, err := LoadConfig(writeConfig(t, "blinds: []\n")); err == nil {
		t.Error("expected error for missing broker")
	}
	// Duplicate MAC.
	dup := writeConfig(t, `
mqtt:
  broker: tcp://b:1883
blinds:
  - name: A
    mac: AA:BB:CC:DD:EE:01
  - name: B
    mac: AA-BB-CC-DD-EE-01
`)
	if _, err := LoadConfig(dup); err == nil {
		t.Error("expected error for duplicate mac")
	}
	// Zero blinds is allowed.
	if _, err := LoadConfig(writeConfig(t, "mqtt:\n  broker: tcp://b:1883\n")); err != nil {
		t.Errorf("zero blinds should be valid: %v", err)
	}
}

func TestDurationUnmarshalError(t *testing.T) {
	if _, err := LoadConfig(writeConfig(t, "mqtt:\n  broker: b\npoll_interval: notaduration\n")); err == nil {
		t.Error("expected error for bad duration")
	}
}
