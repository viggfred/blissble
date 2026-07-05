// Command blissha bridges Hunter Douglas Bliss Smart Blinds to Home Assistant
// over MQTT. It exposes each configured blind as an MQTT Cover (via HA MQTT
// discovery), so standard Home Assistant controls work with no custom UI.
//
//	blissha -config /config/config.yaml
//
// It manages 0..many blinds over a single Bluetooth adapter and one MQTT
// connection, and is designed to run as a container (see the Containerfile).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/viggfred/blissble/pkg/bliss"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bridgeAvail := bridgeAvailabilityTopic(cfg.MQTT.BaseTopic)

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTT.Broker).
		SetClientID(cfg.MQTT.ClientID).
		SetWill(bridgeAvail, "offline", 1, true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second)
	if cfg.MQTT.Username != "" {
		opts.SetUsername(cfg.MQTT.Username).SetPassword(cfg.MQTT.Password)
	}

	// managers is captured by the OnConnect handler below; it is fully populated
	// before Connect() is called, so the handler always sees every blind.
	var managers []*manager
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		logger.Info("mqtt connected", "broker", cfg.MQTT.Broker)
		c.Publish(bridgeAvail, 1, true, "online")
		for _, m := range managers {
			m.onMQTTConnect(c)
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		logger.Warn("mqtt connection lost", "error", err)
	})

	client := mqtt.NewClient(opts)
	scanMu := &sync.Mutex{} // one BLE scan at a time across all blinds
	for _, bc := range cfg.Blinds {
		managers = append(managers, newManager(cfg.MQTT, bc, client, scanMu, time.Duration(cfg.Poll), logger))
	}

	logger.Info("connecting to mqtt", "broker", cfg.MQTT.Broker, "blinds", len(managers))
	token := client.Connect()
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		logger.Warn("mqtt not connected yet; retrying in the background", "error", token.Error())
	}

	var wg sync.WaitGroup
	for _, m := range managers {
		wg.Add(1)
		go m.run(ctx, &wg)
	}
	if len(managers) == 0 {
		logger.Warn("no blinds configured; the bridge is idle")
	}

	<-ctx.Done()
	logger.Info("shutting down")
	for _, m := range managers {
		m.publishAvailability("offline")
		m.blind.Disconnect()
	}
	client.Publish(bridgeAvail, 1, true, "offline").WaitTimeout(2 * time.Second)
	wg.Wait()
	client.Disconnect(500)
}

// manager owns one blind: its BLE connection, MQTT topics and HA discovery.
type manager struct {
	cfg    BlindConfig
	mqtt   MQTTConfig
	id     string
	t      topics
	blind  *bliss.Blind
	client mqtt.Client
	poll   time.Duration
	log    *slog.Logger
}

func newManager(mqttCfg MQTTConfig, bc BlindConfig, client mqtt.Client, scanMu *sync.Mutex, poll time.Duration, logger *slog.Logger) *manager {
	id := blindID(bc.MAC)
	log := logger.With("blind", bc.Name, "mac", bc.MAC)
	blind := bliss.New(bliss.Config{
		MACAddress: bc.MAC,
		Password:   bc.Password, // "" → library default
		Logger:     log,
		ScanMutex:  scanMu,
	})
	m := &manager{
		cfg:    bc,
		mqtt:   mqttCfg,
		id:     id,
		t:      blindTopics(mqttCfg.BaseTopic, id),
		blind:  blind,
		client: client,
		poll:   poll,
		log:    log,
	}
	blind.OnEvent(m.onBLEEvent)
	return m
}

// onMQTTConnect (re)publishes discovery and (re)subscribes to command topics.
// It runs on every MQTT (re)connect so entities survive broker restarts.
func (m *manager) onMQTTConnect(c mqtt.Client) {
	c.Publish(coverDiscoveryTopic(m.mqtt.DiscoveryPrefix, m.id), 1, true, coverDiscoveryPayload(m.mqtt, m.cfg, m.id))
	c.Publish(batteryDiscoveryTopic(m.mqtt.DiscoveryPrefix, m.id), 1, true, batteryDiscoveryPayload(m.mqtt, m.cfg, m.id))
	for _, btn := range coverButtons {
		c.Publish(buttonDiscoveryTopic(m.mqtt.DiscoveryPrefix, m.id, btn.Key), 1, true,
			buttonDiscoveryPayload(m.mqtt, m.cfg, m.id, btn.Key, btn.Name))
	}
	c.Subscribe(m.t.command, 1, m.handleCommand)
	c.Subscribe(m.t.setPosition, 1, m.handleSetPosition)
	c.Subscribe(buttonCommandWildcard(m.mqtt.BaseTopic, m.id), 1, m.handleButton)
}

// run maintains the BLE connection: connect (with backoff), then poll status
// until the link drops or the context is cancelled, then reconnect.
func (m *manager) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	backoff := time.Second
	for ctx.Err() == nil {
		if err := m.blind.Connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("connect failed; retrying", "error", err, "in", backoff)
			m.publishAvailability("offline")
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		m.log.Info("blind connected")
		m.publishAvailability("online")
		if err := m.blind.RequestStatus(); err != nil {
			m.log.Debug("initial status request failed", "error", err)
		}
		m.pollLoop(ctx)
	}
}

// pollLoop periodically requests status (which also keeps the link warm) and
// returns when the context is cancelled or a poll fails (link lost).
func (m *manager) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(m.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.blind.RequestStatus(); err != nil {
				m.log.Warn("status poll failed; will reconnect", "error", err)
				m.publishAvailability("offline")
				m.blind.Disconnect()
				return
			}
		}
	}
}

func (m *manager) onBLEEvent(ev bliss.Event) {
	if ev.Type != bliss.EventStatus {
		return
	}
	m.client.Publish(m.t.position, 1, true, strconv.Itoa(toHA(ev.Position, m.cfg.Invert)))
	if ev.HasBattery {
		m.client.Publish(m.t.battery, 1, true, ev.Battery.String())
	}
}

func (m *manager) handleCommand(_ mqtt.Client, msg mqtt.Message) {
	cmd := strings.ToUpper(strings.TrimSpace(string(msg.Payload())))
	var err error
	switch cmd {
	case "OPEN":
		err = m.blind.Open()
	case "CLOSE":
		err = m.blind.Close()
	case "STOP":
		err = m.blind.Stop()
	default:
		m.log.Warn("unknown cover command", "payload", cmd)
		return
	}
	if err != nil {
		m.log.Warn("command failed", "cmd", cmd, "error", err)
	}
}

func (m *manager) handleButton(_ mqtt.Client, msg mqtt.Message) {
	key := buttonKeyFromTopic(msg.Topic())
	var err error
	switch key {
	case "fast_up":
		err = m.blind.Open()
	case "fast_down":
		err = m.blind.Close()
	case "slow_up":
		err = m.blind.FineUp()
	case "slow_down":
		err = m.blind.FineDown()
	default:
		m.log.Warn("unknown button", "key", key)
		return
	}
	if err != nil {
		m.log.Warn("button action failed", "key", key, "error", err)
	}
}

func (m *manager) handleSetPosition(_ mqtt.Client, msg mqtt.Message) {
	ha, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil {
		m.log.Warn("invalid set_position payload", "payload", string(msg.Payload()))
		return
	}
	if err := m.blind.SetPosition(toDevice(ha, m.cfg.Invert)); err != nil {
		m.log.Warn("set position failed", "error", err)
	}
}

func (m *manager) publishAvailability(state string) {
	m.client.Publish(m.t.availability, 1, true, state)
}
