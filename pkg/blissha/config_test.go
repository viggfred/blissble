package blissha

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults(t *testing.T) {
	c := Config{MQTT: MQTTConfig{Broker: "tcp://b:1883"}}
	c.applyDefaults()
	require.Equal(t, "blissble", c.MQTT.ClientID)
	require.Equal(t, "homeassistant", c.MQTT.DiscoveryPrefix)
	require.Equal(t, "blissble", c.MQTT.BaseTopic)
	// Poll == 0 is meaningful (command-only) and must be preserved, not defaulted.
	require.Zero(t, c.Poll)

	// Negative durations are clamped to 0.
	c = Config{Poll: -1, IdleDisconnect: -1}
	c.applyDefaults()
	require.Zero(t, c.Poll, "negative Poll clamped")
	require.Zero(t, c.IdleDisconnect, "negative IdleDisconnect clamped")
}

func TestValidate(t *testing.T) {
	require.Error(t, (&Config{}).validate(), "missing broker")
	require.Error(t, (&Config{MQTT: MQTTConfig{Broker: "b"}, Blinds: []BlindConfig{{MAC: "AA:BB:CC:DD:EE:01"}}}).validate(), "missing name")
	require.Error(t, (&Config{MQTT: MQTTConfig{Broker: "b"}, Blinds: []BlindConfig{{Name: "A"}}}).validate(), "missing mac")

	// Duplicate MAC (differing separators/case must still collide).
	dup := &Config{MQTT: MQTTConfig{Broker: "b"}, Blinds: []BlindConfig{
		{Name: "A", MAC: "AA:BB:CC:DD:EE:01"},
		{Name: "B", MAC: "aa-bb-cc-dd-ee-01"},
	}}
	require.Error(t, dup.validate(), "duplicate mac")

	// Zero blinds and a single valid blind are both fine.
	require.NoError(t, (&Config{MQTT: MQTTConfig{Broker: "b"}}).validate(), "zero blinds should be valid")
	require.NoError(t, (&Config{MQTT: MQTTConfig{Broker: "b"}, Blinds: []BlindConfig{{Name: "A", MAC: "AA:BB:CC:DD:EE:01"}}}).validate())
}
