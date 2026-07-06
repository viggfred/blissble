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
}

// applyDefaults fills unset string fields and clamps negative durations. Note
// that Poll == 0 is meaningful (command-only) and is left untouched; the
// "unset -> sensible cadence" default is the config source's job (see the
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
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.MQTT.Broker) == "" {
		return fmt.Errorf("mqtt.broker is required")
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
	}
	return nil
}

// blindID derives a stable topic/entity id from a MAC address.
func blindID(mac string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(mac)))
}
