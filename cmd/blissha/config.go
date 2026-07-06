package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/viggfred/blissble/pkg/blissha"
)

// fileConfig mirrors the on-disk YAML. LoadConfig converts it to the plain-struct
// blissha.Config the library consumes, keeping all YAML/CLI concerns here.
type fileConfig struct {
	MQTT           mqttFile      `yaml:"mqtt"`
	Poll           *Duration     `yaml:"poll_interval"` // nil = unset (mode default); 0 = command-only
	IdleDisconnect Duration      `yaml:"idle_disconnect"`
	Location       *locationFile `yaml:"location"`
	Blinds         []blindFile   `yaml:"blinds"`
}

type locationFile struct {
	Latitude  float64 `yaml:"latitude"`
	Longitude float64 `yaml:"longitude"`
	Timezone  string  `yaml:"timezone"` // IANA name, e.g. Europe/Oslo (no system-default fallback)
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
	Name        string          `yaml:"name"`
	MAC         string          `yaml:"mac"`
	Password    string          `yaml:"password"`     // optional; defaults to the built-in key
	Invert      bool            `yaml:"invert"`       // flip position if open/closed are swapped in HA
	DeviceClass string          `yaml:"device_class"` // optional HA cover device_class
	Adapter     string          `yaml:"adapter"`      // "", "hciN", or the adapter's Bluetooth MAC
	Automation  *automationFile `yaml:"automation"`
}

// automationFile mirrors the per-blind automation: block.
type automationFile struct {
	Mode             string              `yaml:"mode"` // off|sun_glare|sun_shade|schedule|thermal
	Window           *windowFile         `yaml:"window"`
	ShadeClosed      *int                `yaml:"shade_closed"` // pointer: 0 is a valid (full-close) value
	Schedule         []scheduleEntryFile `yaml:"schedule"`
	Thermal          *thermalFile        `yaml:"thermal"`
	PresenceSim      *presenceSimFile    `yaml:"presence_sim"`
	Privacy          *privacyFile        `yaml:"privacy"`
	Lux              *luxFile            `yaml:"lux"`
	RequireOccupancy bool                `yaml:"require_occupancy"`
	WhenUnoccupied   string              `yaml:"when_unoccupied"`
	Recompute        Duration            `yaml:"recompute"`
	MinMoveInterval  Duration            `yaml:"min_move_interval"`
	OverrideTimeout  Duration            `yaml:"override_timeout"`
	Deadband         int                 `yaml:"deadband"`
	Step             int                 `yaml:"step"`
	MaxMovesPerHour  int                 `yaml:"max_moves_per_hour"`
	MinAltitude      float64             `yaml:"min_altitude"`
}

type windowFile struct {
	Azimuth          float64 `yaml:"azimuth"`
	BottomUp         bool    `yaml:"bottom_up"`
	HeightM          float64 `yaml:"height_m"`
	SillM            float64 `yaml:"sill_m"`
	ProtectedDepthM  float64 `yaml:"protected_depth_m"`
	ProtectedHeightM float64 `yaml:"protected_height_m"`
	Sensitivity      float64 `yaml:"sensitivity"`
}

type scheduleEntryFile struct {
	Days             daysYAML  `yaml:"days"`
	CloseAt          clockYAML `yaml:"close_at"`
	OpenAt           clockYAML `yaml:"open_at"`
	Sleep            int       `yaml:"sleep"`
	Open             int       `yaml:"open"`
	Ramp             Duration  `yaml:"ramp"`
	NotBeforeSunrise bool      `yaml:"not_before_sunrise"`
	NotAfterSunset   bool      `yaml:"not_after_sunset"`
}

type thermalFile struct {
	SummerClose *int     `yaml:"summer_close"` // pointer: 0 (full close) is valid
	WinterOpen  int      `yaml:"winter_open"`
	HotTempC    *float64 `yaml:"hot_temp_c"`  // pointer: 0 °C is a valid threshold
	ColdTempC   *float64 `yaml:"cold_temp_c"` // pointer: 0 °C is a valid threshold
}

type presenceSimFile struct {
	Enabled      bool      `yaml:"enabled"`
	MorningOpen  clockYAML `yaml:"morning_open"`
	EveningClose clockYAML `yaml:"evening_close"`
	Open         int       `yaml:"open"`
	Close        int       `yaml:"close"`
}

type privacyFile struct {
	AfterDark    bool `yaml:"after_dark"`
	Close        int  `yaml:"close"`
	OccupiedOnly bool `yaml:"occupied_only"`
}

type luxFile struct {
	EngageAbove    float64 `yaml:"engage_above"`
	DisengageBelow float64 `yaml:"disengage_below"`
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

	if fc.Location != nil {
		loc := &blissha.Location{Lat: fc.Location.Latitude, Lon: fc.Location.Longitude}
		if tz := strings.TrimSpace(fc.Location.Timezone); tz != "" {
			// Explicit IANA zone only; never fall back to the system default (a
			// container is usually UTC, which would fire schedules hours off). The
			// zone database is embedded via the time/tzdata import in main.go.
			z, err := time.LoadLocation(tz)
			if err != nil {
				return blissha.Config{}, fmt.Errorf("location.timezone %q: %w", tz, err)
			}
			loc.Zone = z
		}
		cfg.Location = loc
	}

	for _, b := range fc.Blinds {
		bc := blissha.BlindConfig{
			Name:        b.Name,
			MAC:         b.MAC,
			Password:    b.Password,
			Invert:      b.Invert,
			DeviceClass: b.DeviceClass,
			Adapter:     b.Adapter,
		}
		if b.Automation != nil {
			auto, err := b.Automation.build()
			if err != nil {
				return blissha.Config{}, fmt.Errorf("blinds[%s].automation: %w", b.Name, err)
			}
			bc.Automation = auto
		}
		cfg.Blinds = append(cfg.Blinds, bc)
	}
	return cfg, nil
}

// intOr / floatOr return the pointed-to value, or def when the pointer is nil
// (the YAML key was absent). This distinguishes an explicit 0 from an unset key.
func intOr(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

func floatOr(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}

// build converts the YAML automation block into a blissha.Automation.
func (a *automationFile) build() (blissha.Automation, error) {
	mode, err := blissha.ParseAutomationMode(a.Mode)
	if err != nil {
		return blissha.Automation{}, err
	}
	whenUnoccupied, err := blissha.ParseAutomationMode(a.WhenUnoccupied)
	if err != nil {
		return blissha.Automation{}, fmt.Errorf("when_unoccupied: %w", err)
	}
	out := blissha.Automation{
		Mode:             mode,
		ShadeClosedHA:    intOr(a.ShadeClosed, 25), // nil = default; explicit 0 = full close
		RequireOccupancy: a.RequireOccupancy,
		WhenUnoccupied:   whenUnoccupied,
		Recompute:        time.Duration(a.Recompute),
		MinMoveInterval:  time.Duration(a.MinMoveInterval),
		OverrideTimeout:  time.Duration(a.OverrideTimeout),
		Deadband:         a.Deadband,
		Step:             a.Step,
		MaxMovesPerHour:  a.MaxMovesPerHour,
		MinAltitudeDeg:   a.MinAltitude,
	}
	if w := a.Window; w != nil {
		out.Window = blissha.Window{
			AzimuthDeg: w.Azimuth, BottomUp: w.BottomUp, HeightM: w.HeightM, SillM: w.SillM,
			ProtectedDepthM: w.ProtectedDepthM, ProtectedHeightM: w.ProtectedHeightM, Sensitivity: w.Sensitivity,
		}
	}
	for _, e := range a.Schedule {
		out.Schedule = append(out.Schedule, blissha.ScheduleEntry{
			Days: blissha.DaySet(e.Days), CloseAt: blissha.Clock(e.CloseAt), OpenAt: blissha.Clock(e.OpenAt),
			SleepHA: e.Sleep, OpenHA: e.Open, Ramp: time.Duration(e.Ramp),
			NotBeforeSunrise: e.NotBeforeSunrise, NotAfterSunset: e.NotAfterSunset,
		})
	}
	if th := a.Thermal; th != nil {
		out.Thermal = blissha.Thermal{
			SummerCloseHA: intOr(th.SummerClose, 20), // nil = default; explicit 0 = full close
			WinterOpenHA:  th.WinterOpen,             // 0 (nonsensical "open") defaulted to 100 by the library
			HotTempC:      floatOr(th.HotTempC, 24),
			ColdTempC:     floatOr(th.ColdTempC, 10),
		}
	}
	if p := a.PresenceSim; p != nil {
		out.PresenceSim = blissha.PresenceSim{Enabled: p.Enabled, MorningOpen: blissha.Clock(p.MorningOpen), EveningClose: blissha.Clock(p.EveningClose), OpenHA: p.Open, CloseHA: p.Close}
	}
	if p := a.Privacy; p != nil {
		out.Privacy = blissha.Privacy{AfterDark: p.AfterDark, CloseHA: p.Close, OccupiedOnly: p.OccupiedOnly}
	}
	if l := a.Lux; l != nil {
		out.Lux = blissha.LuxGate{EngageAbove: l.EngageAbove, DisengageBelow: l.DisengageBelow}
	}
	return out, nil
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

// clockYAML is a blissha.Clock that unmarshals from "HH:MM".
type clockYAML blissha.Clock

// UnmarshalYAML parses a 24-hour "HH:MM" wall-clock time.
func (c *clockYAML) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return fmt.Errorf("invalid time %q, want HH:MM", s)
	}
	*c = clockYAML(hh*60 + mm)
	return nil
}

// daysYAML is a blissha.DaySet that unmarshals from a list of day tokens like
// [mon, tue] or [weekdays].
type daysYAML blissha.DaySet

// UnmarshalYAML parses a list of weekday tokens into a day bitmask.
func (d *daysYAML) UnmarshalYAML(value *yaml.Node) error {
	var tokens []string
	if err := value.Decode(&tokens); err != nil {
		return err
	}
	set, err := blissha.ParseDaySet(tokens)
	if err != nil {
		return err
	}
	*d = daysYAML(set)
	return nil
}
