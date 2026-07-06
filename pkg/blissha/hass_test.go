package blissha

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlindID(t *testing.T) {
	require.Equal(t, "aabbccddee01", blindID("AA:BB:CC:DD:EE:01"))
	require.Equal(t, "aabbccddeeff", blindID(" aa-bb-cc-dd-ee-ff "))
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
		require.Equal(t, c.ha, toHA(c.dev, c.invert), "toHA(%d,%v)", c.dev, c.invert)
		require.Equal(t, c.dev, toDevice(c.ha, c.invert), "toDevice(%d,%v)", c.ha, c.invert)
	}
	// Clamping.
	require.Equal(t, uint8(100), toDevice(150, false), "clamp high")
	require.Equal(t, uint8(0), toDevice(-5, false), "clamp low")
}

func TestCoverDiscoveryPayload(t *testing.T) {
	mqtt := MQTTConfig{DiscoveryPrefix: "homeassistant", BaseTopic: "blissble"}
	b := BlindConfig{Name: "Living Room", MAC: "AA:BB:CC:DD:EE:01", DeviceClass: "shade"}
	id := blindID(b.MAC)

	var m map[string]any
	require.NoError(t, json.Unmarshal(coverDiscoveryPayload(mqtt, b, id), &m))

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
		require.Equal(t, want, m[k], "field %s", k)
	}

	avail, ok := m["availability"].([]any)
	require.True(t, ok, "availability should be a list")
	require.Len(t, avail, 2, "cover and bridge availability")

	dev, _ := m["device"].(map[string]any)
	require.Equal(t, []any{"blissble_aabbccddee01"}, dev["identifiers"])
}

func TestButtonDiscoveryAndKey(t *testing.T) {
	mqtt := MQTTConfig{DiscoveryPrefix: "homeassistant", BaseTopic: "blissble"}
	b := BlindConfig{Name: "Living Room", MAC: "AA:BB:CC:DD:EE:01"}
	id := blindID(b.MAC)

	var m map[string]any
	require.NoError(t, json.Unmarshal(buttonDiscoveryPayload(mqtt, b, id, "slow_up", "Slow up"), &m))
	require.Equal(t, "blissble/aabbccddee01/button/slow_up", m["command_topic"])
	require.Equal(t, "PRESS", m["payload_press"])
	require.Equal(t, "slow_up", buttonKeyFromTopic("blissble/aabbccddee01/button/slow_up"))
	require.Len(t, coverButtons, 4)
}
