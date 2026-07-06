package blissha

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults(t *testing.T) {
	c := Config{MQTT: MQTTConfig{Broker: "tcp://b:1883"}}
	c.applyDefaults()
	require.Equal(t, "blissble", c.MQTT.ClientID)
	require.Equal(t, "homeassistant", c.MQTT.DiscoveryPrefix)
	require.Equal(t, "blissble", c.MQTT.BaseTopic)
	// Persistent mode (no idle disconnect) must poll for liveness, so Poll == 0
	// falls back to a default cadence rather than becoming command-only.
	require.Equal(t, defaultPersistentPoll, c.Poll)

	// In on-demand mode, Poll == 0 is honored as command-only.
	c = Config{MQTT: MQTTConfig{Broker: "b"}, IdleDisconnect: time.Second}
	c.applyDefaults()
	require.Zero(t, c.Poll, "command-only preserved in on-demand mode")

	// Negative durations are clamped (then, being persistent, defaulted).
	c = Config{Poll: -1, IdleDisconnect: -1}
	c.applyDefaults()
	require.Equal(t, defaultPersistentPoll, c.Poll, "negative Poll clamped then defaulted")
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
