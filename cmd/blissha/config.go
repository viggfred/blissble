package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/viggfred/blissble/pkg/blissha"
)

// fileConfig mirrors the on-disk YAML. LoadConfig converts it to the plain-struct
// blissha.Config the library consumes, keeping all YAML/CLI concerns here.
type fileConfig struct {
	MQTT           mqttFile    `yaml:"mqtt"`
	Poll           *Duration   `yaml:"poll_interval"` // nil = unset (mode default); 0 = command-only
	IdleDisconnect Duration    `yaml:"idle_disconnect"`
	Blinds         []blindFile `yaml:"blinds"`
}

type mqttFile struct {
	Broker          string   `yaml:"broker"` // e.g. tcp://192.168.1.10:1883 or tls://host:8883
	Username        string   `yaml:"username"`
	Password        string   `yaml:"password"`
	ClientID        string   `yaml:"client_id"`        // default "blissble"
	DiscoveryPrefix string   `yaml:"discovery_prefix"` // default "homeassistant"
	BaseTopic       string   `yaml:"base_topic"`       // default "blissble"
	TLS             *tlsFile `yaml:"tls"`
}

type tlsFile struct {
	CACert   string `yaml:"ca_cert"`  // PEM CA to trust (e.g. a self-signed broker CA)
	Cert     string `yaml:"cert"`     // client certificate (for mutual TLS)
	Key      string `yaml:"key"`      // client private key
	Insecure bool   `yaml:"insecure"` // skip server certificate verification (not recommended)
}

type blindFile struct {
	Name        string `yaml:"name"`
	MAC         string `yaml:"mac"`
	Password    string `yaml:"password"`     // optional; defaults to the built-in key
	Invert      bool   `yaml:"invert"`       // flip position if open/closed are swapped in HA
	DeviceClass string `yaml:"device_class"` // optional HA cover device_class
	Adapter     string `yaml:"adapter"`      // "", "hciN", or the adapter's Bluetooth MAC
}

// LoadConfig reads the YAML file at path and returns a ready blissha.Config
// (defaults for unset fields are applied; validation happens in blissha.New).
func LoadConfig(path string) (blissha.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return blissha.Config{}, err
	}
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return blissha.Config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg := blissha.Config{
		MQTT: blissha.MQTTConfig{
			Broker:          fc.MQTT.Broker,
			Username:        fc.MQTT.Username,
			Password:        fc.MQTT.Password,
			ClientID:        fc.MQTT.ClientID,
			DiscoveryPrefix: fc.MQTT.DiscoveryPrefix,
			BaseTopic:       fc.MQTT.BaseTopic,
		},
		IdleDisconnect: time.Duration(fc.IdleDisconnect),
	}

	// Mode-aware poll default: an unset (or nonsensical negative) poll_interval
	// picks a sensible cadence (30s persistent / 1h on-demand); an explicit 0
	// means command-only and is preserved. This "unset vs 0" distinction is why
	// the YAML field is a pointer.
	switch {
	case fc.Poll == nil || *fc.Poll < 0:
		cfg.Poll = 30 * time.Second
		if cfg.IdleDisconnect > 0 {
			cfg.Poll = time.Hour
		}
	default:
		cfg.Poll = time.Duration(*fc.Poll)
	}

	if fc.MQTT.TLS != nil {
		tlsCfg, err := fc.MQTT.TLS.build()
		if err != nil {
			return blissha.Config{}, fmt.Errorf("mqtt.tls: %w", err)
		}
		cfg.MQTT.TLS = tlsCfg
	}

	for _, b := range fc.Blinds {
		cfg.Blinds = append(cfg.Blinds, blissha.BlindConfig{
			Name:        b.Name,
			MAC:         b.MAC,
			Password:    b.Password,
			Invert:      b.Invert,
			DeviceClass: b.DeviceClass,
			Adapter:     b.Adapter,
		})
	}
	return cfg, nil
}

// build turns the YAML tls block into a *tls.Config for the MQTT client.
func (t *tlsFile) build() (*tls.Config, error) {
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
