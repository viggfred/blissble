package main

import (
	"encoding/json"
	"testing"
)

func TestBlindID(t *testing.T) {
	if got := blindID("AA:BB:CC:DD:EE:01"); got != "aabbccddee01" {
		t.Errorf("blindID = %q", got)
	}
	if got := blindID(" aa-bb-cc-dd-ee-ff "); got != "aabbccddeeff" {
		t.Errorf("blindID(dashed) = %q", got)
	}
}

func TestPositionMapping(t *testing.T) {
	// Default (invert=false) is a passthrough: device and HA share 100=open.
	// invert=true reverses it.
	cases := []struct {
		dev    uint8
		invert bool
		ha     int
	}{
		{0, false, 0}, {100, false, 100}, {30, false, 30},
		{0, true, 100}, {100, true, 0}, {30, true, 70},
	}
	for _, c := range cases {
		if got := toHA(c.dev, c.invert); got != c.ha {
			t.Errorf("toHA(%d,%v) = %d, want %d", c.dev, c.invert, got, c.ha)
		}
		if got := toDevice(c.ha, c.invert); got != c.dev {
			t.Errorf("toDevice(%d,%v) = %d, want %d", c.ha, c.invert, got, c.dev)
		}
	}
	// Clamping.
	if got := toDevice(150, false); got != 100 {
		t.Errorf("toDevice(150) = %d, want 100", got)
	}
	if got := toDevice(-5, false); got != 0 {
		t.Errorf("toDevice(-5) = %d, want 0", got)
	}
}

func TestCoverDiscoveryPayload(t *testing.T) {
	mqtt := MQTTConfig{DiscoveryPrefix: "homeassistant", BaseTopic: "blissble"}
	b := BlindConfig{Name: "Living Room", MAC: "AA:BB:CC:DD:EE:01", DeviceClass: "shade"}
	id := blindID(b.MAC)

	var m map[string]any
	if err := json.Unmarshal(coverDiscoveryPayload(mqtt, b, id), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	checks := map[string]any{
		"command_topic":      "blissble/aabbccddee01/set",
		"position_topic":     "blissble/aabbccddee01/position",
		"set_position_topic": "blissble/aabbccddee01/set_position",
		"state_topic":        "blissble/aabbccddee01/state",
		"state_opening":      "opening",
		"position_open":      float64(100),
		"position_closed":    float64(0),
		"device_class":       "shade",
		"unique_id":          "blissble_aabbccddee01_cover",
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("%s = %v (%T), want %v", k, m[k], m[k], want)
		}
	}
	if avail, ok := m["availability"].([]any); !ok || len(avail) != 2 {
		t.Errorf("availability = %v, want 2 topics", m["availability"])
	}
	dev, _ := m["device"].(map[string]any)
	ids, _ := dev["identifiers"].([]any)
	if len(ids) != 1 || ids[0] != "blissble_aabbccddee01" {
		t.Errorf("device.identifiers = %v", dev["identifiers"])
	}
}

func TestButtonDiscoveryAndKey(t *testing.T) {
	mqtt := MQTTConfig{DiscoveryPrefix: "homeassistant", BaseTopic: "blissble"}
	b := BlindConfig{Name: "Living Room", MAC: "AA:BB:CC:DD:EE:01"}
	id := blindID(b.MAC)

	var m map[string]any
	if err := json.Unmarshal(buttonDiscoveryPayload(mqtt, b, id, "slow_up", "Slow up"), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["command_topic"] != "blissble/aabbccddee01/button/slow_up" {
		t.Errorf("button command_topic = %v", m["command_topic"])
	}
	if m["payload_press"] != "PRESS" {
		t.Errorf("payload_press = %v", m["payload_press"])
	}
	if got := buttonKeyFromTopic("blissble/aabbccddee01/button/slow_up"); got != "slow_up" {
		t.Errorf("buttonKeyFromTopic = %q", got)
	}
	if len(coverButtons) != 4 {
		t.Errorf("expected 4 buttons, got %d", len(coverButtons))
	}
}
