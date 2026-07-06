package blissha

import (
	"context"
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
	bridgeAvail string
}

// New validates cfg, applies defaults for any unset fields, and builds the MQTT
// client and per-blind managers. It does not open any connection; call Run for
// that. A nil logger falls back to slog.Default.
func New(cfg Config, logger *slog.Logger) (*Bridge, error) {
	if logger == nil {
		logger = slog.Default()
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	b := &Bridge{
		cfg:         cfg,
		logger:      logger,
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

	// Cache one Adapter and one scan mutex per physical dongle (keyed by hci id),
	// so blinds on the same adapter serialize scans while different adapters scan
	// in parallel.
	adapters := map[string]*bluetooth.Adapter{}
	scanMus := map[string]*sync.Mutex{}
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
			scanMus[id] = &sync.Mutex{}
		}
		b.managers = append(b.managers, newManager(cfg.MQTT, bc, b.client, adapter, scanMus[id], cfg.Poll, cfg.IdleDisconnect, logger))
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
