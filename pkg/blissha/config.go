package blissha

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"
)

// Config is the programmatic configuration for a Bridge. All durations are
// plain time.Duration; the blissha command builds this from YAML/CLI. Zero
// values are filled by applyDefaults, so a minimal Config only needs a broker
// and at least one blind.
type Config struct {
	MQTT MQTTConfig
	// Poll is the status-refresh cadence. In persistent mode it polls over the
	// held connection; in on-demand mode it is how often to briefly connect just
	// to refresh position/battery. 0 disables periodic refresh entirely
	// (command-only): the bridge then connects only to run a command.
	Poll time.Duration
	// IdleDisconnect, when > 0, enables on-demand mode: the BLE link is kept
	// disconnected and only opened to run a command or a refresh, then dropped
	// after this idle window. 0 keeps a persistent connection.
	IdleDisconnect time.Duration
	Blinds         []BlindConfig
	// Location is the home's geographic position, shared by all sun-based and
	// schedule automation. Required (non-nil) only if a blind uses such a mode.
	Location *Location
}

// MQTTConfig holds broker connection and Home Assistant discovery settings.
type MQTTConfig struct {
	Broker          string // e.g. tcp://192.168.1.10:1883 or tls://host:8883
	Username        string
	Password        string
	ClientID        string // default "blissble"
	DiscoveryPrefix string // default "homeassistant"
	BaseTopic       string // default "blissble"
	// TLS configures a tls:// (or ssl://) broker connection. Leave nil for a
	// plain tcp:// broker, or for a tls:// broker whose certificate is already
	// trusted by the system CA pool. The caller builds this *tls.Config however
	// it likes (the blissha command builds it from the YAML tls: block).
	TLS *tls.Config
}

// BlindConfig describes a single motor to expose to Home Assistant.
type BlindConfig struct {
	Name        string
	MAC         string
	Password    string // optional; defaults to the built-in key (bliss.DefaultPassword)
	Invert      bool   // flip position if open/closed are swapped in HA
	DeviceClass string // optional HA cover device_class (shade, blind, curtain, ...)
	// Adapter selects which Bluetooth adapter drives this blind. Empty uses the
	// default (hci0); otherwise "hciN" or the adapter's own Bluetooth MAC (the
	// MAC is stable across reboots/replug, unlike hciN numbering).
	Adapter string
	// Automation is the optional sun/schedule automation for this blind. The zero
	// value (ModeOff) disables it.
	Automation Automation
}

// defaultPersistentPoll is the status cadence used in persistent mode when no
// interval is given. Persistent mode must poll to keep the held link alive and
// notice silent drops, so command-only (Poll == 0) is not honored there.
const defaultPersistentPoll = 30 * time.Second

// applyDefaults fills unset string fields and resolves the poll cadence. Poll ==
// 0 means command-only (connect only for commands), which only makes sense in
// on-demand mode where the link is dropped while idle; the "unset -> sensible
// cadence" default for a set-but-empty poll is the config source's job (see the
// blissha command's YAML loader), since a struct has no "unset" for 0.
func (c *Config) applyDefaults() {
	if c.MQTT.ClientID == "" {
		c.MQTT.ClientID = "blissble"
	}
	if c.MQTT.DiscoveryPrefix == "" {
		c.MQTT.DiscoveryPrefix = "homeassistant"
	}
	if c.MQTT.BaseTopic == "" {
		c.MQTT.BaseTopic = "blissble"
	}
	if c.Poll < 0 {
		c.Poll = 0
	}
	if c.IdleDisconnect < 0 {
		c.IdleDisconnect = 0
	}
	// In persistent mode a held connection must be polled to detect silent
	// drops (the BLE client exposes no passive disconnect signal), so a 0 here
	// would leave a dropped link undetected — fall back to a cadence instead.
	if c.Poll == 0 && c.IdleDisconnect == 0 {
		c.Poll = defaultPersistentPoll
	}
	for i := range c.Blinds {
		c.Blinds[i].Automation.applyDefaults()
	}
}

// Normalize applies defaults to unset fields and validates the configuration.
// New calls it; embedders and tools (e.g. a dry run) can call it to obtain a
// fully-defaulted, validated Config.
func (c *Config) Normalize() error {
	c.applyDefaults()
	return c.validate()
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.MQTT.Broker) == "" {
		return fmt.Errorf("mqtt.broker is required")
	}
	if c.Location != nil {
		if err := c.Location.validate(); err != nil {
			return fmt.Errorf("location: %w", err)
		}
	}
	seen := make(map[string]bool)
	for i, b := range c.Blinds {
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("blinds[%d]: name is required", i)
		}
		if strings.TrimSpace(b.MAC) == "" {
			return fmt.Errorf("blinds[%d] (%s): mac is required", i, b.Name)
		}
		id := blindID(b.MAC)
		if seen[id] {
			return fmt.Errorf("blinds[%d]: duplicate mac %s", i, b.MAC)
		}
		seen[id] = true

		if err := b.Automation.validate(); err != nil {
			return fmt.Errorf("blinds[%d] (%s): automation: %w", i, b.Name, err)
		}
		if b.Automation.needsGeo() && c.Location == nil {
			return fmt.Errorf("blinds[%d] (%s): automation mode %v needs a top-level location (latitude/longitude)", i, b.Name, b.Automation.Mode)
		}
		if b.Automation.needsZone() && (c.Location == nil || c.Location.Zone == nil) {
			return fmt.Errorf("blinds[%d] (%s): automation mode %v needs location.timezone", i, b.Name, b.Automation.Mode)
		}
	}
	return nil
}

// blindID derives a stable topic/entity id from a MAC address.
func blindID(mac string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(mac)))
}
