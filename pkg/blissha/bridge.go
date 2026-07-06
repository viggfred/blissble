package blissha

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"tinygo.org/x/bluetooth"
)

// Bridge connects a set of Bliss blinds to Home Assistant over one MQTT
// connection. Build it with New and drive it with Run.
type Bridge struct {
	cfg         Config
	logger      *slog.Logger
	client      mqtt.Client
	managers    []*manager
	byID        map[string]*manager // blindID -> manager, for signal routing
	bridgeAvail string
}

// New validates cfg, applies defaults for any unset fields, and builds the MQTT
// client and per-blind managers. It does not open any connection; call Run for
// that. A nil logger falls back to slog.Default.
func New(cfg Config, logger *slog.Logger) (*Bridge, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}

	b := &Bridge{
		cfg:         cfg,
		logger:      logger,
		byID:        map[string]*manager{},
		bridgeAvail: bridgeAvailabilityTopic(cfg.MQTT.BaseTopic),
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTT.Broker).
		SetClientID(cfg.MQTT.ClientID).
		SetWill(b.bridgeAvail, "offline", 1, true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(b.onMQTTConnect).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("mqtt connection lost", "error", err)
		})
	if cfg.MQTT.Username != "" {
		opts.SetUsername(cfg.MQTT.Username).SetPassword(cfg.MQTT.Password)
	}
	if cfg.MQTT.TLS != nil {
		opts.SetTLSConfig(cfg.MQTT.TLS)
	}
	b.client = mqtt.NewClient(opts)

	// Cache one Adapter per physical dongle (keyed by hci id). All blinds share a
	// SINGLE scan mutex so only one BLE scan runs at a time across every adapter.
	// This is required, not just an optimization: tinygo/x/bluetooth's Linux Scan
	// watches the org.bluez.Adapter1 "Discovering" property without filtering by
	// adapter path, so when a scan on one adapter finishes and stops discovery, a
	// scan running concurrently on a *different* adapter aborts with "scan was
	// stopped unexpectedly" and leaks its discovery ("Operation already in
	// progress"). Serializing scans avoids the overlap entirely. Connects still
	// run in parallel; scans are brief.
	adapters := map[string]*bluetooth.Adapter{}
	scanMu := &sync.Mutex{}
	for _, bc := range cfg.Blinds {
		id, err := resolveHCI(bc.Adapter)
		if err != nil {
			logger.Error("skipping blind: bluetooth adapter not found", "blind", bc.Name, "adapter", bc.Adapter, "error", err)
			continue
		}
		adapter, ok := adapters[id]
		if !ok {
			adapter = bluetooth.NewAdapter(id)
			adapters[id] = adapter
		}
		mgr := newManager(cfg.MQTT, bc, cfg.Location, b.client, adapter, scanMu, cfg.Poll, cfg.IdleDisconnect, logger)
		b.managers = append(b.managers, mgr)
		b.byID[mgr.id] = mgr
	}
	return b, nil
}

// onMQTTConnect (re)publishes discovery and availability whenever the MQTT link
// comes up. It runs on every (re)connect, so it must be idempotent.
func (b *Bridge) onMQTTConnect(c mqtt.Client) {
	b.logger.Info("mqtt connected", "broker", b.cfg.MQTT.Broker)
	c.Publish(b.bridgeAvail, 1, true, "online")
	for _, m := range b.managers {
		m.onMQTTConnect(c)
	}
}

// Run connects to MQTT, starts every blind manager, and blocks until ctx is
// cancelled. On shutdown it marks each blind and the bridge offline, drops the
// BLE links, and disconnects from MQTT cleanly.
func (b *Bridge) Run(ctx context.Context) error {
	b.logger.Info("connecting to mqtt", "broker", b.cfg.MQTT.Broker, "blinds", len(b.managers))
	token := b.client.Connect()
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		b.logger.Warn("mqtt not connected yet; retrying in the background", "error", token.Error())
	}

	var wg sync.WaitGroup
	for _, m := range b.managers {
		wg.Add(1)
		go m.run(ctx, &wg)
	}
	if len(b.managers) == 0 {
		b.logger.Warn("no blinds configured; the bridge is idle")
	}

	<-ctx.Done()
	b.logger.Info("shutting down")
	for _, m := range b.managers {
		m.publishAvailability("offline")
		m.blind.Disconnect()
	}
	b.client.Publish(b.bridgeAvail, 1, true, "offline").WaitTimeout(2 * time.Second)
	wg.Wait()
	b.client.Disconnect(500)
	return nil
}

// SetRoomOccupancy reports whether the room containing a blind is occupied. The
// blind is named by MAC or blind id (separators/case are normalized). Occupancy
// gates occupancy-dependent automation (e.g. sun_glare with require_occupancy).
// Returns an error for an unknown blind. Safe to call from any goroutine.
func (b *Bridge) SetRoomOccupancy(blind string, occupied bool) error {
	return b.signal(blind, controlMsg{kind: ctrlRoomOcc, b: occupied})
}

// SetLux reports the ambient brightness (lux) at a blind, so lux-gated shading
// only engages when it is actually bright out.
func (b *Bridge) SetLux(blind string, lux float64) error {
	return b.signal(blind, controlMsg{kind: ctrlLux, f: lux})
}

// SetHomeAway sets the home-wide away state on every blind. away=true (nobody
// home) drives presence simulation where enabled.
func (b *Bridge) SetHomeAway(away bool) {
	b.broadcast(controlMsg{kind: ctrlHomeAway, b: away})
}

// SetOutdoorTemp sets the outdoor temperature (°C) on every blind, used by
// thermal mode when the season is temperature-driven.
func (b *Bridge) SetOutdoorTemp(tempC float64) {
	b.broadcast(controlMsg{kind: ctrlTemp, f: tempC})
}

func (b *Bridge) signal(blind string, msg controlMsg) error {
	m, ok := b.byID[blindID(blind)]
	if !ok {
		return fmt.Errorf("unknown blind %q", blind)
	}
	m.sendControl(msg)
	return nil
}

func (b *Bridge) broadcast(msg controlMsg) {
	for _, m := range b.managers {
		m.sendControl(msg)
	}
}
