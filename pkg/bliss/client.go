package bliss

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// Config configures a Blind client.
type Config struct {
	// MACAddress of the motor, e.g. "AA:BB:CC:DD:EE:01". Required.
	MACAddress string
	// Password sent in the login command. Defaults to DefaultPassword.
	Password string
	// Range is the motor's position range for GotoCommand. Defaults to DefaultRange.
	Range int
	// Adapter to use. Defaults to bluetooth.DefaultAdapter.
	Adapter *bluetooth.Adapter
	// Logger for structured logs. Defaults to slog.Default().
	Logger *slog.Logger
	// ScanTimeout bounds discovery per Connect. Defaults to 20s.
	ScanTimeout time.Duration
	// ScanMutex, if set, is held for the duration of each BLE scan. Share one
	// mutex across all Blind instances so only one scan runs at a time — BlueZ
	// permits one scan per adapter, and the tinygo/x/bluetooth Linux scanner
	// additionally cross-talks between adapters (a scan stopping on one adapter
	// aborts a concurrent scan on another), so a single shared mutex is safest.
	ScanMutex *sync.Mutex
}

// State is a snapshot of the last known blind state.
type State struct {
	Connected bool
	LoggedIn  bool
	Position  uint8
	Battery   BatteryLevel
	Reversed  bool // motor direction-reverse config flag (not live movement)
}

// Blind is a client for a single Bliss Smart Blinds motor over BLE. It is safe
// for concurrent use.
type Blind struct {
	cfg     Config
	adapter *bluetooth.Adapter
	logger  *slog.Logger

	sendMu sync.Mutex // serializes command delivery (and thus reconnects)

	mu        sync.RWMutex
	device    bluetooth.Device
	cmdChar   bluetooth.DeviceCharacteristic
	connected bool
	state     State
	onEvent   func(Event)
	loginCh   chan bool
	rootCtx   context.Context // context from the initial Connect, for reconnects
}

// New creates a Blind client. Config.MACAddress must be set; all other fields
// have sensible defaults.
func New(cfg Config) *Blind {
	if cfg.Password == "" {
		cfg.Password = DefaultPassword
	}
	if cfg.Range == 0 {
		cfg.Range = DefaultRange
	}
	if cfg.Adapter == nil {
		cfg.Adapter = bluetooth.DefaultAdapter
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ScanTimeout == 0 {
		cfg.ScanTimeout = 20 * time.Second
	}
	return &Blind{
		cfg:     cfg,
		adapter: cfg.Adapter,
		logger:  cfg.Logger.With(slog.String("component", "blissble"), slog.String("mac", cfg.MACAddress)),
	}
}

// OnEvent registers a callback invoked for every parsed response frame (status
// reports, login results, ...). Set it before Connect. The callback runs on the
// BLE notification goroutine, so keep it quick and non-blocking.
func (b *Blind) OnEvent(fn func(Event)) {
	b.mu.Lock()
	b.onEvent = fn
	b.mu.Unlock()
}

// Connect enables the adapter, scans for the motor (BlueZ will not connect to a
// device it has not discovered), connects, discovers the GATT characteristics,
// subscribes to status notifications and logs in. It returns once login is
// confirmed. The context is retained so that later commands can transparently
// reconnect if the motor drops the link while idle.
func (b *Blind) Connect(ctx context.Context) error {
	b.mu.Lock()
	b.rootCtx = ctx
	b.mu.Unlock()
	return b.connectOnce(ctx)
}

func (b *Blind) connectOnce(ctx context.Context) error {
	if err := b.adapter.Enable(); err != nil {
		return fmt.Errorf("enable bluetooth adapter: %w", err)
	}
	mac, err := bluetooth.ParseMAC(b.cfg.MACAddress)
	if err != nil {
		return fmt.Errorf("invalid MAC %q: %w", b.cfg.MACAddress, err)
	}

	b.logger.Info("scanning for motor")
	if err := b.scan(ctx, mac); err != nil {
		return err
	}

	b.logger.Info("connecting")
	device, err := b.adapter.Connect(bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: mac}}, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	cmdChar, respChar, err := b.discover(device)
	if err != nil {
		device.Disconnect()
		return err
	}

	if err := respChar.EnableNotifications(b.onNotify); err != nil {
		device.Disconnect()
		return fmt.Errorf("subscribe to responses: %w", err)
	}

	b.mu.Lock()
	b.device = device
	b.cmdChar = cmdChar
	b.connected = true
	b.state.Connected = true
	b.mu.Unlock()

	if err := b.doLogin(ctx); err != nil {
		b.Disconnect()
		return err
	}
	b.logger.Info("connected and logged in")
	return nil
}

// scan runs a BLE discovery until the target MAC appears (registering it with
// BlueZ) or the scan window / context elapses.
func (b *Blind) scan(ctx context.Context, target bluetooth.MAC) error {
	if b.cfg.ScanMutex != nil {
		b.cfg.ScanMutex.Lock()
		defer b.cfg.ScanMutex.Unlock()
	}
	found := make(chan struct{})
	var once sync.Once
	scanErr := make(chan error, 1)
	go func() {
		scanErr <- b.adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
			if r.Address.MAC == target {
				once.Do(func() {
					close(found)
					_ = a.StopScan()
				})
			}
		})
	}()
	select {
	case err := <-scanErr:
		// Scan returned on its own — either because our callback matched and
		// stopped it, or because it failed/ended early (so we don't wait out
		// the whole timeout on, say, a busy adapter).
		select {
		case <-found:
			return nil
		default:
		}
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}
		return fmt.Errorf("device %s not found (scan ended)", target.String())
	case <-ctx.Done():
		_ = b.adapter.StopScan()
		<-scanErr
		return ctx.Err()
	case <-time.After(b.cfg.ScanTimeout):
		_ = b.adapter.StopScan()
		<-scanErr
		return fmt.Errorf("device %s not found within %s (is it awake and in range?)", target.String(), b.cfg.ScanTimeout)
	}
}

// discover finds the Command (write) and Response (notify) characteristics.
func (b *Blind) discover(device bluetooth.Device) (cmd, resp bluetooth.DeviceCharacteristic, err error) {
	svcUUID, err := bluetooth.ParseUUID(ServiceUUID)
	if err != nil {
		return cmd, resp, fmt.Errorf("parse service UUID: %w", err)
	}
	services, err := device.DiscoverServices([]bluetooth.UUID{svcUUID})
	if err != nil {
		return cmd, resp, fmt.Errorf("discover service: %w", err)
	}
	var haveCmd, haveResp bool
	for _, s := range services {
		chars, e := s.DiscoverCharacteristics(nil)
		if e != nil {
			continue
		}
		for i := range chars {
			switch chars[i].UUID().String() {
			case CommandUUID:
				cmd, haveCmd = chars[i], true
			case ResponseUUID:
				resp, haveResp = chars[i], true
			}
		}
	}
	if !haveCmd || !haveResp {
		return cmd, resp, errors.New("required Command/Response characteristics not found on device")
	}
	return cmd, resp, nil
}

// applyEvent folds a parsed response event into a state snapshot. It is pure so
// the state-transition logic can be unit-tested without a BLE connection.
func applyEvent(s State, ev Event) State {
	switch ev.Type {
	case EventStatus:
		s.Position = ev.Position
		s.Reversed = ev.Reversed
		if ev.HasBattery {
			s.Battery = ev.Battery
		}
	case EventLoginResult:
		s.LoggedIn = ev.Success
	}
	return s
}

// hexValue lazily hex-encodes bytes for slog, so the formatting only runs when
// the log record is actually emitted (i.e. at debug level), not on every frame.
type hexValue []byte

func (h hexValue) LogValue() slog.Value { return slog.StringValue(fmt.Sprintf("%X", []byte(h))) }

// onNotify parses each notification, updates state, and fans out to OnEvent.
func (b *Blind) onNotify(buf []byte) {
	ev, ok := ParseResponse(buf)
	if !ok {
		b.logger.Debug("unparsed notification", slog.Any("hex", hexValue(buf)))
		return
	}
	b.mu.Lock()
	b.state = applyEvent(b.state, ev)
	if ev.Type == EventLoginResult && b.loginCh != nil {
		select {
		case b.loginCh <- ev.Success:
		default:
		}
	}
	cb := b.onEvent
	b.mu.Unlock()

	b.logger.Debug("response", slog.Any("hex", hexValue(buf)), slog.String("op", fmt.Sprintf("0x%02X", ev.Opcode)))
	if cb != nil {
		cb(ev)
	}
}

// doLogin sends the login command and waits for the device's login result.
func (b *Blind) doLogin(ctx context.Context) error {
	ch := make(chan bool, 1)
	b.mu.Lock()
	b.loginCh = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.loginCh = nil
		b.mu.Unlock()
	}()

	if err := b.writeRaw(LoginCommand(b.cfg.Password)); err != nil {
		return fmt.Errorf("send login: %w", err)
	}
	select {
	case ok := <-ch:
		if !ok {
			return errors.New("login rejected (wrong password)")
		}
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("login timed out (no response from device)")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writeRaw writes a frame to the Command characteristic (write-with-response,
// which this device requires) without any reconnect handling.
func (b *Blind) writeRaw(frame []byte) error {
	b.mu.RLock()
	c := b.cmdChar
	connected := b.connected
	b.mu.RUnlock()
	if !connected {
		return errors.New("not connected")
	}
	if _, err := c.Write(frame); err != nil {
		return err
	}
	b.logger.Debug("sent", slog.Any("hex", hexValue(frame)))
	return nil
}

// Send writes a command frame to the Command characteristic. Battery motors drop
// the BLE link after a short idle period; if the write fails Send transparently
// reconnects, re-logs-in, and retries once.
func (b *Blind) Send(frame []byte) error {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	err := b.writeRaw(frame)
	if err == nil {
		return nil
	}
	b.logger.Warn("command write failed; reconnecting", slog.Any("error", err))
	if err := b.reconnect(); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}
	if err := b.writeRaw(frame); err != nil {
		return fmt.Errorf("write after reconnect: %w", err)
	}
	return nil
}

// reconnect tears down the (dropped) link and re-establishes it, reusing the
// context supplied to the initial Connect so cancellation still applies.
func (b *Blind) reconnect() error {
	b.mu.RLock()
	root := b.rootCtx
	b.mu.RUnlock()
	if root == nil {
		root = context.Background()
	}
	if err := root.Err(); err != nil {
		return err
	}
	_ = b.Disconnect()
	ctx, cancel := context.WithTimeout(root, b.cfg.ScanTimeout+15*time.Second)
	defer cancel()
	return b.connectOnce(ctx)
}

// Open raises the blind (roller up).
func (b *Blind) Open() error { return b.Send(UpCommand()) }

// Close lowers the blind (roller down).
func (b *Blind) Close() error { return b.Send(DownCommand()) }

// Stop halts movement.
func (b *Blind) Stop() error { return b.Send(StopCommand()) }

// FineUp nudges the blind up by a small step (the app's slow/fine adjust).
func (b *Blind) FineUp() error { return b.Send(FineUpCommand()) }

// FineDown nudges the blind down by a small step (the app's slow/fine adjust).
func (b *Blind) FineDown() error { return b.Send(FineDownCommand()) }

// SetPosition moves the blind to a target percentage (0..100).
func (b *Blind) SetPosition(percent uint8) error {
	return b.Send(GotoCommand(percent, b.cfg.Range))
}

// SetSpeed selects a motor speed preset (25/50/75/100, snapped to nearest).
func (b *Blind) SetSpeed(percent int) error { return b.Send(SpeedCommand(percent)) }

// GoToFavorite moves the blind to its saved favorite position.
func (b *Blind) GoToFavorite() error { return b.Send(GoToFavoriteCommand()) }

// SetFavorite saves the current position as the favorite.
func (b *Blind) SetFavorite() error { return b.Send(SetFavoriteCommand()) }

// DeleteFavorite clears the saved favorite position.
func (b *Blind) DeleteFavorite() error { return b.Send(DeleteFavoriteCommand()) }

// SetClock syncs the motor's internal clock (required for schedules to fire at
// the right wall-clock time).
func (b *Blind) SetClock(t time.Time) error { return b.Send(SetClockCommand(t)) }

// SyncClock sets the motor's clock to the current local time.
func (b *Blind) SyncClock() error { return b.SetClock(time.Now()) }

// AddTimer programs schedule slot index (1..TimerSlots) to move the blind to
// positionPct on the given days at hour:minute:second. Results arrive via
// OnEvent as EventTimerSet.
func (b *Blind) AddTimer(index uint8, days Days, hour, minute, second, positionPct uint8, silent bool) error {
	return b.Send(AddTimerCommand(index, days, hour, minute, second, positionPct, silent, b.cfg.Range))
}

// DeleteTimer clears schedule slot index. Results arrive via OnEvent.
func (b *Blind) DeleteTimer(index uint8) error { return b.Send(DeleteTimerCommand(index)) }

// ClearTimers deletes every schedule slot.
func (b *Blind) ClearTimers() error {
	for i := uint8(1); i <= TimerSlots; i++ {
		if err := b.DeleteTimer(i); err != nil {
			return err
		}
	}
	return nil
}

// QueryTimerSlots requests the next free schedule slot (arrives via OnEvent as
// EventTimerIndex).
func (b *Blind) QueryTimerSlots() error { return b.Send(TimerSlotsQueryCommand()) }

// RequestStatus asks the motor to report its status (arrives via OnEvent).
func (b *Blind) RequestStatus() error { return b.Send(ReadStatusCommand()) }

// Heartbeat sends the keep-alive frame.
func (b *Blind) Heartbeat() error { return b.Send(HeartbeatCommand()) }

// State returns a snapshot of the last known blind state.
func (b *Blind) State() State {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state
}

// Disconnect tears down the BLE connection.
func (b *Blind) Disconnect() error {
	b.mu.Lock()
	device := b.device
	wasConnected := b.connected
	b.connected = false
	b.state = State{}
	b.mu.Unlock()
	if !wasConnected {
		return nil
	}
	return device.Disconnect()
}
