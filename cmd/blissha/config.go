package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level blissha YAML configuration.
type Config struct {
	MQTT MQTTConfig `yaml:"mqtt"`
	// Poll is the status-refresh cadence. In persistent mode it polls over the
	// held connection; in on-demand mode it is how often to briefly connect just
	// to refresh position/battery.
	Poll Duration `yaml:"poll_interval"`
	// IdleDisconnect, when > 0, enables on-demand mode: the BLE link is kept
	// disconnected and only opened to run a command or a refresh, then dropped
	// after this idle window. 0 (default) keeps a persistent connection.
	IdleDisconnect Duration      `yaml:"idle_disconnect"`
	Blinds         []BlindConfig `yaml:"blinds"`
}

// MQTTConfig holds broker connection and Home Assistant discovery settings.
type MQTTConfig struct {
	Broker          string `yaml:"broker"` // e.g. tcp://192.168.1.10:1883
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	ClientID        string `yaml:"client_id"`        // default "blissble"
	DiscoveryPrefix string `yaml:"discovery_prefix"` // default "homeassistant"
	BaseTopic       string `yaml:"base_topic"`       // default "blissble"
}

// BlindConfig describes a single motor to expose to Home Assistant.
type BlindConfig struct {
	Name        string `yaml:"name"`
	MAC         string `yaml:"mac"`
	Password    string `yaml:"password"`     // optional; defaults to the built-in key
	Invert      bool   `yaml:"invert"`       // flip position if open/closed are swapped in HA
	DeviceClass string `yaml:"device_class"` // optional HA cover device_class (shade, blind, curtain, ...)
}

// Duration is a time.Duration that unmarshals from a string like "30s".
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// LoadConfig reads, defaults and validates the config file at path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

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
	if c.IdleDisconnect < 0 {
		c.IdleDisconnect = 0
	}
	if c.Poll <= 0 {
		if c.IdleDisconnect > 0 {
			c.Poll = Duration(time.Hour) // on-demand: refresh infrequently
		} else {
			c.Poll = Duration(30 * time.Second)
		}
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
