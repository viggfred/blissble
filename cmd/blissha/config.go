package main

import (
	"crypto/tls"
	"crypto/x509"
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
	// to refresh position/battery. Set it to 0 to disable periodic refresh
	// entirely (command-only). A nil pointer means "unset" and gets a default.
	Poll *Duration `yaml:"poll_interval"`
	// IdleDisconnect, when > 0, enables on-demand mode: the BLE link is kept
	// disconnected and only opened to run a command or a refresh, then dropped
	// after this idle window. 0 (default) keeps a persistent connection.
	IdleDisconnect Duration      `yaml:"idle_disconnect"`
	Blinds         []BlindConfig `yaml:"blinds"`
}

// MQTTConfig holds broker connection and Home Assistant discovery settings.
type MQTTConfig struct {
	Broker          string `yaml:"broker"` // e.g. tcp://192.168.1.10:1883 or tls://host:8883
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	ClientID        string `yaml:"client_id"`        // default "blissble"
	DiscoveryPrefix string `yaml:"discovery_prefix"` // default "homeassistant"
	BaseTopic       string `yaml:"base_topic"`       // default "blissble"
	// TLS configures a tls:// (or ssl://) broker connection. Omit for a plain
	// tcp:// broker, or for a tls:// broker whose certificate is already trusted
	// by the system CA pool (then no options are needed).
	TLS *TLSConfig `yaml:"tls"`
}

// TLSConfig configures the MQTT TLS connection.
type TLSConfig struct {
	CACert   string `yaml:"ca_cert"`  // PEM CA to trust (e.g. a self-signed broker CA)
	Cert     string `yaml:"cert"`     // client certificate (for mutual TLS)
	Key      string `yaml:"key"`      // client private key
	Insecure bool   `yaml:"insecure"` // skip server certificate verification (not recommended)
}

// build turns the TLS config into a *tls.Config for the MQTT client.
func (t *TLSConfig) build() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.Insecure} //nolint:gosec // opt-in via config
	if t.CACert != "" {
		pem, err := os.ReadFile(t.CACert)
		if err != nil {
			return nil, fmt.Errorf("read tls.ca_cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls.ca_cert %q: no valid certificates", t.CACert)
		}
		cfg.RootCAs = pool
	}
	if t.Cert != "" || t.Key != "" {
		if t.Cert == "" || t.Key == "" {
			return nil, fmt.Errorf("tls: both cert and key are required for mutual TLS")
		}
		crt, err := tls.LoadX509KeyPair(t.Cert, t.Key)
		if err != nil {
			return nil, fmt.Errorf("load tls client cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{crt}
	}
	return cfg, nil
}

// BlindConfig describes a single motor to expose to Home Assistant.
type BlindConfig struct {
	Name        string `yaml:"name"`
	MAC         string `yaml:"mac"`
	Password    string `yaml:"password"`     // optional; defaults to the built-in key
	Invert      bool   `yaml:"invert"`       // flip position if open/closed are swapped in HA
	DeviceClass string `yaml:"device_class"` // optional HA cover device_class (shade, blind, curtain, ...)
	// Adapter selects which Bluetooth adapter drives this blind. Empty uses the
	// default (hci0); otherwise "hciN" or the adapter's own Bluetooth MAC (the
	// MAC is stable across reboots/replug, unlike hciN numbering).
	Adapter string `yaml:"adapter"`
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
	if c.Poll == nil { // unset -> pick a default cadence for the mode
		d := Duration(30 * time.Second)
		if c.IdleDisconnect > 0 {
			d = Duration(time.Hour) // on-demand: refresh infrequently
		}
		c.Poll = &d
	} else if *c.Poll < 0 {
		*c.Poll = 0 // negative is meaningless; treat as disabled
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
