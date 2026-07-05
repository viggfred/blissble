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
	"tinygo.org/x/bluetooth"

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
		managers = append(managers, newManager(cfg.MQTT, bc, client, adapter, scanMus[id], time.Duration(cfg.Poll), time.Duration(cfg.IdleDisconnect), logger))
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

// blindOp is a BLE action queued by an MQTT handler and executed on the
// manager's goroutine (so connect/idle handling stays in one place).
type blindOp struct {
	desc string
	fn   func() error
}

// manager owns one blind: its BLE connection, MQTT topics and HA discovery.
type manager struct {
	cfg            BlindConfig
	mqtt           MQTTConfig
	id             string
	t              topics
	blind          *bliss.Blind
	client         mqtt.Client
	poll           time.Duration
	idleDisconnect time.Duration // 0 = persistent connection; >0 = on-demand
	actions        chan blindOp
	log            *slog.Logger
}

func newManager(mqttCfg MQTTConfig, bc BlindConfig, client mqtt.Client, adapter *bluetooth.Adapter, scanMu *sync.Mutex, poll, idleDisconnect time.Duration, logger *slog.Logger) *manager {
	id := blindID(bc.MAC)
	log := logger.With("blind", bc.Name, "mac", bc.MAC)
	blind := bliss.New(bliss.Config{
		MACAddress: bc.MAC,
		Password:   bc.Password, // "" → library default
		Adapter:    adapter,
		Logger:     log,
		ScanMutex:  scanMu,
	})
	m := &manager{
		cfg:            bc,
		mqtt:           mqttCfg,
		id:             id,
		t:              blindTopics(mqttCfg.BaseTopic, id),
		blind:          blind,
		client:         client,
		poll:           poll,
		idleDisconnect: idleDisconnect,
		actions:        make(chan blindOp, 8),
		log:            log,
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

// run drives the blind, either holding the connection open (persistent) or
// connecting only on demand and dropping the link after an idle window.
func (m *manager) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	if m.idleDisconnect > 0 {
		m.runOnDemand(ctx)
		return
	}
	m.runPersistent(ctx)
}

// runPersistent keeps a connection open, reconnecting with backoff on drops,
// and polls status on the interval.
func (m *manager) runPersistent(ctx context.Context) {
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
		m.requestStatus()
		m.pollLoop(ctx)
	}
}

// pollLoop serves queued commands and polls status until the context is
// cancelled or a poll fails (link lost, prompting a reconnect in runPersistent).
func (m *manager) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(m.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			if err := op.fn(); err != nil {
				m.log.Warn("command failed", "action", op.desc, "error", err)
			}
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

// runOnDemand keeps the BLE link disconnected while idle. It connects to run a
// queued command or a periodic refresh, then drops the link after idleDisconnect
// of inactivity — trading a few seconds of latency for much less battery use.
func (m *manager) runOnDemand(ctx context.Context) {
	m.publishAvailability("online") // optimistic; refined on each connect attempt
	var refresh <-chan time.Time
	if m.poll > 0 {
		t := time.NewTicker(m.poll)
		defer t.Stop()
		refresh = t.C
	}
	idle := time.NewTimer(m.idleDisconnect)
	idle.Stop()
	defer m.blind.Disconnect()

	// Connect once at startup so Home Assistant gets current position/battery
	// immediately, rather than waiting for the first refresh tick.
	m.ensureConnected(ctx)
	m.requestStatus()
	idle.Reset(m.idleDisconnect)

	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			m.ensureConnected(ctx)
			if err := op.fn(); err != nil {
				m.log.Warn("command failed", "action", op.desc, "error", err)
			}
			m.requestStatus()
			idle.Reset(m.idleDisconnect)
		case <-refresh:
			m.ensureConnected(ctx)
			m.requestStatus()
			idle.Reset(m.idleDisconnect)
		case <-idle.C:
			m.log.Debug("idle; disconnecting to save battery")
			m.blind.Disconnect()
		}
	}
}

// ensureConnected connects if the link is down, updating availability.
func (m *manager) ensureConnected(ctx context.Context) {
	if m.blind.State().Connected {
		return
	}
	if err := m.blind.Connect(ctx); err != nil {
		m.log.Warn("connect failed", "error", err)
		m.publishAvailability("offline")
		return
	}
	m.publishAvailability("online")
}

// requestStatus asks the blind to report status (best effort).
func (m *manager) requestStatus() {
	if err := m.blind.RequestStatus(); err != nil {
		m.log.Debug("status request failed", "error", err)
	}
}

// submit queues a BLE action for the manager goroutine, dropping it (with a
// warning) if the queue is somehow full.
func (m *manager) submit(desc string, fn func() error) {
	select {
	case m.actions <- blindOp{desc, fn}:
	default:
		m.log.Warn("action queue full; dropping command", "action", desc)
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
	switch cmd := strings.ToUpper(strings.TrimSpace(string(msg.Payload()))); cmd {
	case "OPEN":
		m.submit("open", m.blind.Open)
	case "CLOSE":
		m.submit("close", m.blind.Close)
	case "STOP":
		m.submit("stop", m.blind.Stop)
	default:
		m.log.Warn("unknown cover command", "payload", cmd)
	}
}

func (m *manager) handleButton(_ mqtt.Client, msg mqtt.Message) {
	key := buttonKeyFromTopic(msg.Topic())
	switch key {
	case "fast_up":
		m.submit("fast_up", m.blind.Open)
	case "fast_down":
		m.submit("fast_down", m.blind.Close)
	case "slow_up":
		m.submit("slow_up", m.blind.FineUp)
	case "slow_down":
		m.submit("slow_down", m.blind.FineDown)
	default:
		m.log.Warn("unknown button", "key", key)
	}
}

func (m *manager) handleSetPosition(_ mqtt.Client, msg mqtt.Message) {
	ha, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil {
		m.log.Warn("invalid set_position payload", "payload", string(msg.Payload()))
		return
	}
	m.submit("set_position", func() error {
		return m.blind.SetPosition(toDevice(ha, m.cfg.Invert))
	})
}

func (m *manager) publishAvailability(state string) {
	m.client.Publish(m.t.availability, 1, true, state)
}
