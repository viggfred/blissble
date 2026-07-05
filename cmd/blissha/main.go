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
	desc  string
	fn    func() error
	state string // HA cover state to announce before running (e.g. "opening"); "" = none
	track bool   // poll position until the blind settles, so HA sees it stop
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
		_ = m.refreshState(ctx)
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
			m.runOp(ctx, op)
		case <-ticker.C:
			if err := m.refreshState(ctx); err != nil {
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
	_ = m.refreshState(ctx)
	idle.Reset(m.idleDisconnect)

	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			m.runOp(ctx, op)
			idle.Reset(m.idleDisconnect)
		case <-refresh:
			_ = m.refreshState(ctx)
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

// refreshState reads status and publishes the resting cover state, so HA always
// has a defined state (open/closed) rather than "unknown".
func (m *manager) refreshState(ctx context.Context) error {
	m.ensureConnected(ctx)
	if err := m.blind.RequestStatus(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond): // let the reply arrive
	}
	m.publishState(m.settledState())
	return nil
}

// submit queues a BLE action for the manager goroutine, dropping it (with a
// warning) if the queue is somehow full.
func (m *manager) submit(op blindOp) {
	select {
	case m.actions <- op:
	default:
		m.log.Warn("action queue full; dropping command", "action", op.desc)
	}
}

// runOp executes a queued action: announce its cover state, run it, then either
// track the blind until it settles (movement) or just refresh status.
func (m *manager) runOp(ctx context.Context, op blindOp) {
	m.ensureConnected(ctx) // no-op if already connected (persistent mode)
	m.publishState(op.state)
	if err := op.fn(); err != nil {
		m.log.Warn("command failed", "action", op.desc, "error", err)
	}
	if op.track {
		m.trackUntilSettled(ctx)
	} else {
		_ = m.refreshState(ctx)
	}
}

// trackUntilSettled follows the blind after a movement command (open/close are
// continuous "travel to the limit" commands that can take ~40s), publishing
// position as it travels, until it stops moving — the position is unchanged for
// two consecutive polls — or a safety timeout. It then publishes the resting
// state so HA clears the opening/closing indication.
//
// It deliberately does NOT treat the *starting* end position as "settled" (that
// bug made an open-from-closed bounce straight back to closed), and it stays
// responsive: a new command (stop, or reverse) interrupts and is applied.
func (m *manager) trackUntilSettled(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(90 * time.Second) // safety cap; movement normally ends sooner
	last, stable := -1, 0
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			m.ensureConnected(ctx)
			m.publishState(op.state)
			if err := op.fn(); err != nil {
				m.log.Warn("command failed", "action", op.desc, "error", err)
			}
			if !op.track { // e.g. stop: settle immediately
				m.publishState(m.settledState())
				return
			}
			last, stable = -1, 0 // restart settle detection for the new movement
		case <-ticker.C:
			m.ensureConnected(ctx)
			m.requestStatus() // -> onBLEEvent publishes the fresh position
			cur := int(m.blind.State().Position)
			if cur == last {
				if stable++; stable >= 2 {
					m.publishState(m.settledState())
					return
				}
			} else {
				last, stable = cur, 0
			}
		case <-timeout:
			m.publishState(m.settledState())
			return
		}
	}
}

// settledState maps the last known position to a resting cover state.
func (m *manager) settledState() string {
	switch ha := toHA(m.blind.State().Position, m.cfg.Invert); {
	case ha >= 100:
		return "open"
	case ha <= 0:
		return "closed"
	default:
		return "stopped"
	}
}

func (m *manager) publishState(s string) {
	if s != "" {
		m.client.Publish(m.t.state, 1, true, s)
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
		m.submit(blindOp{desc: "open", fn: m.blind.Open, state: "opening", track: true})
	case "CLOSE":
		m.submit(blindOp{desc: "close", fn: m.blind.Close, state: "closing", track: true})
	case "STOP":
		m.submit(blindOp{desc: "stop", fn: m.blind.Stop, state: "stopped"})
	default:
		m.log.Warn("unknown cover command", "payload", cmd)
	}
}

func (m *manager) handleButton(_ mqtt.Client, msg mqtt.Message) {
	key := buttonKeyFromTopic(msg.Topic())
	switch key {
	case "fast_up":
		m.submit(blindOp{desc: "fast_up", fn: m.blind.Open, state: "opening", track: true})
	case "fast_down":
		m.submit(blindOp{desc: "fast_down", fn: m.blind.Close, state: "closing", track: true})
	case "slow_up":
		m.submit(blindOp{desc: "slow_up", fn: m.blind.FineUp, state: "opening", track: true})
	case "slow_down":
		m.submit(blindOp{desc: "slow_down", fn: m.blind.FineDown, state: "closing", track: true})
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
	state := "opening"
	if ha < toHA(m.blind.State().Position, m.cfg.Invert) {
		state = "closing"
	}
	m.submit(blindOp{
		desc:  "set_position",
		fn:    func() error { return m.blind.SetPosition(toDevice(ha, m.cfg.Invert)) },
		state: state,
		track: true,
	})
}

func (m *manager) publishAvailability(state string) {
	m.client.Publish(m.t.availability, 1, true, state)
}
