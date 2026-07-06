// Package blissha is an embeddable bridge between Hunter Douglas Bliss Smart
// Blinds (over Bluetooth LE) and Home Assistant (over MQTT). It exposes each
// configured blind as an MQTT Cover via HA MQTT discovery, so standard Home
// Assistant controls work with no custom UI.
//
// Configure it with plain structs and run it:
//
//	bridge, err := blissha.New(blissha.Config{
//		MQTT:   blissha.MQTTConfig{Broker: "tcp://localhost:1883"},
//		Blinds: []blissha.BlindConfig{{Name: "Living Room", MAC: "AA:BB:CC:DD:EE:FF"}},
//	}, logger)
//	// ...
//	err = bridge.Run(ctx) // blocks until ctx is cancelled
//
// It manages 0..many blinds across one or more Bluetooth adapters over a single
// MQTT connection. The blissha command wraps this package with YAML/CLI config.
package blissha

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"tinygo.org/x/bluetooth"

	"github.com/viggfred/blissble/pkg/bliss"
)

// opOrigin distinguishes a human/HA-initiated move from an automation move, so a
// manual move can pause automation (override) while automation's own moves don't.
type opOrigin int

const (
	opManual opOrigin = iota // default: human/HA command
	opAuto                   // issued by the sun/schedule controller
)

// blindOp is a BLE action queued by an MQTT handler or the controller and
// executed on the manager's goroutine (so connect/idle handling stays in one
// place).
type blindOp struct {
	desc   string
	fn     func() error
	state  string // HA cover state to announce before running (e.g. "opening"); "" = none
	track  bool   // poll position until the blind settles, so HA sees it stop
	origin opOrigin
}

// ctrlKind identifies a controller signal update.
type ctrlKind int

const (
	ctrlRoomOcc ctrlKind = iota
	ctrlHomeAway
	ctrlLux
	ctrlTemp
)

// controlMsg carries an external signal (occupancy, lux, temperature) from
// another goroutine onto the manager goroutine. Unlike a blindOp it never
// triggers a BLE connect — it only updates state and re-evaluates automation.
type controlMsg struct {
	kind ctrlKind
	b    bool
	f    float64
}

// evalFloor is the minimum interval between automation evaluations. Evaluation
// is cheap (local math + cached position, no BLE), so a short floor is fine.
const evalFloor = 30 * time.Second

// externalMoveTolerance is how far (HA %) an at-rest position may drift from the
// last automation target before we treat it as a human/RF-remote move.
const externalMoveTolerance = 5

// retainedMsg is a retained MQTT publish (topic + payload).
type retainedMsg struct {
	topic   string
	payload []byte
}

// manager owns one blind: its BLE connection, MQTT topics and HA discovery.
type manager struct {
	cfg            BlindConfig
	mqtt           MQTTConfig
	id             string
	t              topics
	discovery      []retainedMsg // HA discovery configs, built once (they never change)
	blind          *bliss.Blind
	client         mqtt.Client
	poll           time.Duration
	idleDisconnect time.Duration // 0 = persistent connection; >0 = on-demand
	actions        chan blindOp
	log            *slog.Logger

	// Automation (zero value ModeOff = disabled). All of these fields are owned
	// exclusively by the manager goroutine; external signals arrive via control.
	auto     Automation
	loc      Location
	control  chan controlMsg
	sig      Signals
	ctrl     ControllerState
	nextEval time.Duration // last Decision.NextEvalIn hint
	// lastPosHA is the manager's own cache of the last observed HA position. It
	// is used by the controller (rather than bliss.State(), which Disconnect
	// zeroes) so a stale/idle link doesn't look like a closed blind, and it
	// survives idle disconnects in on-demand mode.
	lastPosHA int
	posKnown  bool // true once a status has been observed
}

func newManager(mqttCfg MQTTConfig, bc BlindConfig, loc *Location, client mqtt.Client, adapter *bluetooth.Adapter, scanMu *sync.Mutex, poll, idleDisconnect time.Duration, logger *slog.Logger) *manager {
	id := blindID(bc.MAC)
	log := logger.With("blind", bc.Name, "mac", bc.MAC)
	blind := bliss.New(bliss.Config{
		MACAddress: bc.MAC,
		Password:   bc.Password, // "" → library default
		Adapter:    adapter,
		Logger:     log,
		ScanMutex:  scanMu,
	})
	var location Location
	if loc != nil {
		location = *loc
	}
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
		auto:           bc.Automation,
		loc:            location,
		control:        make(chan controlMsg, 8),
	}
	m.discovery = m.buildDiscovery()
	blind.OnEvent(m.onBLEEvent)
	return m
}

// automating reports whether this blind has automation enabled.
func (m *manager) automating() bool { return m.auto.Mode != ModeOff }

// sendControl hands an external signal to the manager goroutine (non-blocking).
func (m *manager) sendControl(msg controlMsg) {
	select {
	case m.control <- msg:
	default:
		m.log.Warn("control queue full; dropping signal")
	}
}

// buildDiscovery marshals the retained HA discovery configs once. They depend
// only on immutable per-blind config, so they are cached and simply republished
// on every (re)connect rather than rebuilt each time.
func (m *manager) buildDiscovery() []retainedMsg {
	msgs := []retainedMsg{
		{coverDiscoveryTopic(m.mqtt.DiscoveryPrefix, m.id), coverDiscoveryPayload(m.mqtt, m.cfg, m.t, m.id)},
		{batteryDiscoveryTopic(m.mqtt.DiscoveryPrefix, m.id), batteryDiscoveryPayload(m.mqtt, m.cfg, m.t, m.id)},
	}
	for _, btn := range coverButtons {
		msgs = append(msgs, retainedMsg{
			buttonDiscoveryTopic(m.mqtt.DiscoveryPrefix, m.id, btn.Key),
			buttonDiscoveryPayload(m.mqtt, m.cfg, m.t, m.id, btn.Key, btn.Name),
		})
	}
	return msgs
}

// onMQTTConnect (re)publishes discovery and (re)subscribes to command topics.
// It runs on every MQTT (re)connect so entities survive broker restarts.
func (m *manager) onMQTTConnect(c mqtt.Client) {
	for _, d := range m.discovery {
		c.Publish(d.topic, 1, true, d.payload)
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
		_ = m.refreshState(ctx, true)
		m.pollLoop(ctx)
	}
}

// pollLoop serves queued commands and polls status until the context is
// cancelled or a poll fails (link lost, prompting a reconnect in runPersistent).
func (m *manager) pollLoop(ctx context.Context) {
	var tick <-chan time.Time // nil (never fires) when polling is disabled
	if m.poll > 0 {
		ticker := time.NewTicker(m.poll)
		defer ticker.Stop()
		tick = ticker.C
	}
	evalTimer := m.newEvalTimer()
	var evalC <-chan time.Time
	if evalTimer != nil {
		defer evalTimer.Stop()
		evalC = evalTimer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			m.runOp(ctx, op)
		case msg := <-m.control:
			m.applyControl(msg)
		case <-evalC:
			m.evaluate()
			evalTimer.Reset(m.evalDelay())
		case <-tick:
			if err := m.refreshState(ctx, true); err != nil {
				m.log.Warn("status poll failed; will reconnect", "error", err)
				m.publishAvailability("offline")
				m.blind.Disconnect()
				return
			}
		}
	}
}

// newEvalTimer starts the automation evaluation timer, or returns nil when
// automation is disabled.
func (m *manager) newEvalTimer() *time.Timer {
	if !m.automating() {
		return nil
	}
	return time.NewTimer(m.evalDelay())
}

// evalDelay is how long until the next automation evaluation, honoring the last
// Decision hint but never faster than evalFloor.
func (m *manager) evalDelay() time.Duration {
	d := m.nextEval
	if d <= 0 {
		d = m.auto.Recompute
	}
	if d <= 0 {
		d = 5 * time.Minute
	}
	return max(d, evalFloor)
}

// onDemandRetryStart/Cap bound the reconnect backoff used while a blind is
// unreachable in on-demand mode (so it recovers in seconds-to-minutes rather
// than waiting for the — possibly hourly — poll interval).
const (
	onDemandRetryStart = 5 * time.Second
	onDemandRetryCap   = 2 * time.Minute
)

// runOnDemand keeps the BLE link disconnected while idle. It connects to run a
// queued command or a periodic refresh, then drops the link after idleDisconnect
// of inactivity — trading a few seconds of latency for much less battery use.
// While the blind is unreachable it retries on a bounded backoff.
func (m *manager) runOnDemand(ctx context.Context) {
	m.publishAvailability("online") // optimistic; refined on each connect attempt

	idle := time.NewTimer(m.idleDisconnect)
	idle.Stop()
	refresh := time.NewTimer(0) // fires ~immediately for the startup refresh
	defer refresh.Stop()
	defer m.blind.Disconnect()

	backoff := onDemandRetryStart
	// scheduleRefresh arms the refresh timer: soon (bounded backoff) while the
	// blind is unreachable, otherwise at the normal poll cadence, and re-arms the
	// idle-disconnect timer whenever the link is up.
	scheduleRefresh := func() {
		if !m.blind.State().Connected {
			// Command-only (poll == 0): don't chase an unreachable blind on a
			// timer — that would defeat the whole point of the mode. Wait for the
			// next command to trigger a connect instead.
			if m.poll == 0 {
				return
			}
			refresh.Reset(backoff)
			backoff = min(backoff*2, onDemandRetryCap)
			return
		}
		backoff = onDemandRetryStart
		idle.Reset(m.idleDisconnect)
		if m.poll > 0 {
			refresh.Reset(m.poll)
		}
	}

	// The automation timer is independent of the connection/refresh logic:
	// evaluation is BLE-free, so it fires even while disconnected (command-only),
	// and only submits a move — which wakes the link via the actions path.
	evalTimer := m.newEvalTimer()
	var evalC <-chan time.Time
	if evalTimer != nil {
		defer evalTimer.Stop()
		evalC = evalTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			m.runOp(ctx, op)
			scheduleRefresh()
		case msg := <-m.control:
			m.applyControl(msg)
		case <-evalC:
			m.evaluate()
			evalTimer.Reset(m.evalDelay())
		case <-refresh.C:
			_ = m.refreshState(ctx, true)
			scheduleRefresh()
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
// has a defined state (open/closed) rather than "unknown". poll marks a routine
// status poll (as opposed to the settle after one of our own moves), which is
// the only context in which an unexpected position change means an external move.
func (m *manager) refreshState(ctx context.Context, poll bool) error {
	m.ensureConnected(ctx)
	if err := m.blind.RequestStatus(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond): // let the reply arrive
	}
	m.publishSettled(poll)
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
	m.noteOrigin(op)
	m.ensureConnected(ctx) // no-op if already connected (persistent mode)
	m.publishState(op.state)
	if err := op.fn(); err != nil {
		m.log.Warn("command failed", "action", op.desc, "error", err)
	}
	if op.track {
		m.trackUntilSettled(ctx)
	} else {
		_ = m.refreshState(ctx, false) // our own move; not a poll
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
	tracker := newSettleTracker()
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-m.actions:
			m.noteOrigin(op)
			m.ensureConnected(ctx)
			m.publishState(op.state)
			if err := op.fn(); err != nil {
				m.log.Warn("command failed", "action", op.desc, "error", err)
			}
			if !op.track { // e.g. stop: settle immediately
				m.publishSettled(false)
				return
			}
			tracker = newSettleTracker() // restart settle detection for the new movement
		case <-ticker.C:
			m.ensureConnected(ctx)
			m.requestStatus() // -> onBLEEvent publishes the fresh position
			if tracker.observe(int(m.blind.State().Position)) {
				m.publishSettled(false)
				return
			}
		case <-timeout:
			m.publishSettled(false)
			return
		}
	}
}

// publishSettled publishes the resting cover state from the current position and
// records that observation for the controller. poll=true means it came from a
// routine status poll (see observe).
func (m *manager) publishSettled(poll bool) {
	haPos := toHA(m.blind.State().Position, m.cfg.Invert)
	m.publishState(restingState(haPos))
	m.observe(haPos, poll)
}

// observe records an at-rest position for the controller. On a routine poll, if
// the position has changed since we last saw it by more than the tolerance and
// automation didn't command that change (our own moves update lastPosHA at their
// settle, so they never look like a change here), a human or the RF remote moved
// it — so automation backs off for OverrideTimeout rather than fighting them.
// Comparing successive observations (not the commanded target) means a failed or
// slightly-off automation move is not misread as an external move, and a genuine
// external move arms the override exactly once rather than re-arming every poll.
func (m *manager) observe(haPos int, poll bool) {
	prev, had := m.lastPosHA, m.posKnown
	m.lastPosHA = haPos
	m.posKnown = true
	if !poll || !had || !m.automating() {
		return
	}
	if time.Now().Before(m.ctrl.OverrideUntil) {
		return // already paused; nothing new to react to
	}
	if absInt(haPos-prev) > externalMoveTolerance {
		m.ctrl.OverrideUntil = time.Now().Add(m.auto.OverrideTimeout)
		m.log.Debug("external move detected; pausing automation", "observed", haPos, "previous", prev)
	}
}

// noteOrigin pauses automation (override) when a human/HA command runs, so
// automation resumes only after the override window. Automation's own moves
// (opAuto) never trigger this.
func (m *manager) noteOrigin(op blindOp) {
	if op.origin == opManual && m.automating() {
		m.ctrl.OverrideUntil = time.Now().Add(m.auto.OverrideTimeout)
	}
}

// applyControl folds an external signal into the manager's state and
// re-evaluates automation.
func (m *manager) applyControl(msg controlMsg) {
	switch msg.kind {
	case ctrlRoomOcc:
		m.sig.RoomOccupied = triFromBool(msg.b)
	case ctrlHomeAway:
		m.sig.HomeAway = triFromBool(msg.b)
	case ctrlLux:
		m.sig.Lux, m.sig.LuxKnown = msg.f, true
	case ctrlTemp:
		m.sig.OutdoorTempC, m.sig.TempKnown = msg.f, true
	}
	m.evaluate()
}

// evaluate runs the pure decision engine against cached state (no BLE) and, if a
// move is warranted, queues it as an automation-tagged op.
func (m *manager) evaluate() {
	if !m.automating() {
		return
	}
	// Use the manager's cached position, not bliss.State(): the cache survives an
	// idle disconnect (which zeroes the client's state), so a disconnected blind
	// doesn't read as fully closed and trigger a redundant reconnect+move.
	dec := Decide(DecisionInput{
		Cfg:     m.auto,
		Loc:     m.loc,
		Now:     time.Now(),
		Current: Position{HA: m.lastPosHA, Known: m.posKnown},
		Signals: m.sig,
		State:   m.ctrl,
	})
	m.ctrl = dec.State
	m.nextEval = dec.NextEvalIn
	if !dec.Move {
		m.log.Debug("automation hold", "reason", dec.Reason, "next", m.nextEval)
		return
	}
	m.log.Debug("automation move", "target", dec.TargetHA, "reason", dec.Reason)
	target := dec.TargetHA
	state := "opening"
	if target < m.lastPosHA {
		state = "closing"
	}
	m.submit(blindOp{
		desc:   "auto:" + dec.Reason,
		fn:     func() error { return m.blind.SetPosition(toDevice(target, m.cfg.Invert)) },
		state:  state,
		track:  true,
		origin: opAuto,
	})
}

func triFromBool(b bool) Tristate {
	if b {
		return TriYes
	}
	return TriNo
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
