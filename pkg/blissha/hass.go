package blissha

import (
	"encoding/json"
	"fmt"
	"strings"
)

// topics holds the per-blind MQTT topic layout.
type topics struct {
	availability string
	command      string
	position     string
	setPosition  string
	state        string
	battery      string
}

func blindTopics(base, id string) topics {
	p := fmt.Sprintf("%s/%s", base, id)
	return topics{
		availability: p + "/availability",
		command:      p + "/set",
		position:     p + "/position",
		setPosition:  p + "/set_position",
		state:        p + "/state",
		battery:      p + "/battery",
	}
}

// bridgeAvailabilityTopic is the daemon-wide availability topic (also the MQTT
// last-will), so every entity goes unavailable if blissha dies.
func bridgeAvailabilityTopic(base string) string { return base + "/bridge/availability" }

func coverDiscoveryTopic(prefix, id string) string {
	return fmt.Sprintf("%s/cover/blissble_%s/config", prefix, id)
}

func batteryDiscoveryTopic(prefix, id string) string {
	return fmt.Sprintf("%s/sensor/blissble_%s_battery/config", prefix, id)
}

func deviceBlock(b BlindConfig, id string) map[string]any {
	return map[string]any{
		"identifiers":  []string{"blissble_" + id},
		"name":         b.Name,
		"manufacturer": "Hunter Douglas",
		"model":        "Bliss Smart Blinds",
	}
}

var originBlock = map[string]any{
	"name":        "blissble",
	"support_url": "https://github.com/viggfred/blissble",
}

// availability returns the two-topic availability list (bridge + per-blind) used
// by every entity, with availability_mode "all".
func availability(mqtt MQTTConfig, t topics) []map[string]string {
	return []map[string]string{
		{"topic": bridgeAvailabilityTopic(mqtt.BaseTopic)},
		{"topic": t.availability},
	}
}

// coverButtons are the discrete press-buttons exposed alongside the cover:
// full-speed (fast) open/close and fine-step (slow) nudges.
var coverButtons = []struct{ Key, Name string }{
	{"fast_up", "Fast up"},
	{"fast_down", "Fast down"},
	{"slow_up", "Slow up"},
	{"slow_down", "Slow down"},
}

func buttonDiscoveryTopic(prefix, id, key string) string {
	return fmt.Sprintf("%s/button/blissble_%s_%s/config", prefix, id, key)
}

func buttonCommandTopic(base, id, key string) string {
	return fmt.Sprintf("%s/%s/button/%s", base, id, key)
}

// buttonCommandWildcard subscribes to all of a blind's button command topics.
func buttonCommandWildcard(base, id string) string {
	return fmt.Sprintf("%s/%s/button/+", base, id)
}

// buttonKeyFromTopic extracts the button key (last path segment) from a topic.
func buttonKeyFromTopic(topic string) string {
	i := strings.LastIndex(topic, "/")
	if i < 0 {
		return topic
	}
	return topic[i+1:]
}

// buttonDiscoveryPayload builds an HA MQTT button entity for a discrete action.
func buttonDiscoveryPayload(mqtt MQTTConfig, b BlindConfig, id, key, name string) []byte {
	t := blindTopics(mqtt.BaseTopic, id)
	cfg := map[string]any{
		"name":                  name,
		"unique_id":             "blissble_" + id + "_" + key,
		"command_topic":         buttonCommandTopic(mqtt.BaseTopic, id, key),
		"payload_press":         "PRESS",
		"availability_mode":     "all",
		"payload_available":     "online",
		"payload_not_available": "offline",
		"availability":          availability(mqtt, t),
		"qos":                   1,
		"device":                deviceBlock(b, id),
		"origin":                originBlock,
	}
	out, _ := json.Marshal(cfg)
	return out
}

// coverDiscoveryPayload builds the Home Assistant MQTT cover discovery config.
// The cover is position-based (0=closed, 100=open); HA derives open/closed from
// the reported position, and open/close/stop go to the command topic.
func coverDiscoveryPayload(mqtt MQTTConfig, b BlindConfig, id string) []byte {
	t := blindTopics(mqtt.BaseTopic, id)
	cfg := map[string]any{
		"name":               nil, // use the device name
		"unique_id":          "blissble_" + id + "_cover",
		"command_topic":      t.command,
		"payload_open":       "OPEN",
		"payload_close":      "CLOSE",
		"payload_stop":       "STOP",
		"position_topic":     t.position,
		"set_position_topic": t.setPosition,
		"position_open":      100,
		"position_closed":    0,
		// Explicit state so HA shows opening/closing and settles the arrows
		// instead of getting stuck after a press.
		"state_topic":           t.state,
		"state_open":            "open",
		"state_opening":         "opening",
		"state_closed":          "closed",
		"state_closing":         "closing",
		"state_stopped":         "stopped",
		"availability_mode":     "all",
		"payload_available":     "online",
		"payload_not_available": "offline",
		"availability":          availability(mqtt, t),
		"qos":                   1,
		"device":                deviceBlock(b, id),
		"origin":                originBlock,
	}
	if b.DeviceClass != "" {
		cfg["device_class"] = b.DeviceClass
	}
	out, _ := json.Marshal(cfg)
	return out
}

// batteryDiscoveryPayload builds a diagnostic battery-status sensor grouped
// under the same HA device.
func batteryDiscoveryPayload(mqtt MQTTConfig, b BlindConfig, id string) []byte {
	t := blindTopics(mqtt.BaseTopic, id)
	cfg := map[string]any{
		"name":                  "Battery",
		"unique_id":             "blissble_" + id + "_battery",
		"state_topic":           t.battery,
		"entity_category":       "diagnostic",
		"availability_mode":     "all",
		"payload_available":     "online",
		"payload_not_available": "offline",
		"availability":          availability(mqtt, t),
		"qos":                   1,
		"device":                deviceBlock(b, id),
		"origin":                originBlock,
	}
	out, _ := json.Marshal(cfg)
	return out
}

// toHA converts a device position (0..100) to Home Assistant's convention
// (0=closed, 100=open). The device uses the same orientation (100 = fully open),
// so the default is a passthrough; invert reverses it for oppositely-mounted
// blinds.
func toHA(devicePos uint8, invert bool) int {
	p := min(int(devicePos), 100)
	if invert {
		return 100 - p
	}
	return p
}

// toDevice is the inverse of toHA: it converts a Home Assistant position to the
// device's 0..100 percentage.
func toDevice(haPos int, invert bool) uint8 {
	haPos = max(0, min(haPos, 100))
	if invert {
		return uint8(100 - haPos)
	}
	return uint8(haPos)
}
