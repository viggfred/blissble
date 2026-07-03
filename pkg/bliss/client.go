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
}

// State is a snapshot of the last known blind state.
type State struct {
	Connected bool
	LoggedIn  bool
	Position  uint8
	Battery   BatteryLevel
	Direction bool
}

// Blind is a client for a single Bliss Smart Blinds motor over BLE. It is safe
// for concurrent use.
type Blind struct {
	cfg     Config
	adapter *bluetooth.Adapter
	logger  *slog.Logger

	mu        sync.RWMutex
	device    bluetooth.Device
	cmdChar   bluetooth.DeviceCharacteristic
	connected bool
	state     State
	onEvent   func(Event)
	loginCh   chan bool
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
// confirmed.
func (b *Blind) Connect(ctx context.Context) error {
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
	found := make(chan struct{})
	var once sync.Once
	scanErr := make(chan error, 1)
	go func() {
		scanErr <- b.adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
			if r.Address.MAC == target {
				once.Do(func() {
					_ = a.StopScan()
					close(found)
				})
			}
		})
	}()
	select {
	case <-found:
		return <-scanErr
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

// onNotify parses each notification, updates state, and fans out to OnEvent.
func (b *Blind) onNotify(buf []byte) {
	ev, ok := ParseResponse(buf)
	if !ok {
		b.logger.Debug("unparsed notification", slog.String("hex", fmt.Sprintf("%X", buf)))
		return
	}
	b.mu.Lock()
	switch ev.Type {
	case EventStatus:
		b.state.Position = ev.Position
		b.state.Battery = ev.Battery
		b.state.Direction = ev.Direction
	case EventLoginResult:
		b.state.LoggedIn = ev.Success
		if b.loginCh != nil {
			select {
			case b.loginCh <- ev.Success:
			default:
			}
		}
	}
	cb := b.onEvent
	b.mu.Unlock()

	b.logger.Debug("response", slog.String("hex", fmt.Sprintf("%X", buf)), slog.String("op", fmt.Sprintf("0x%02X", ev.Opcode)))
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

	if err := b.Send(LoginCommand(b.cfg.Password)); err != nil {
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

// Send writes a raw command frame to the Command characteristic (write with
// response, which this device requires).
func (b *Blind) Send(frame []byte) error {
	b.mu.RLock()
	c := b.cmdChar
	connected := b.connected
	b.mu.RUnlock()
	if !connected {
		return errors.New("not connected")
	}
	if _, err := c.Write(frame); err != nil {
		return fmt.Errorf("write command: %w", err)
	}
	b.logger.Debug("sent", slog.String("hex", fmt.Sprintf("%X", frame)))
	return nil
}

// Open raises the blind (roller up).
func (b *Blind) Open() error { return b.Send(UpCommand()) }

// Close lowers the blind (roller down).
func (b *Blind) Close() error { return b.Send(DownCommand()) }

// Stop halts movement.
func (b *Blind) Stop() error { return b.Send(StopCommand()) }

// SetPosition moves the blind to a target percentage (0..100).
func (b *Blind) SetPosition(percent uint8) error {
	return b.Send(GotoCommand(percent, b.cfg.Range))
}

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
