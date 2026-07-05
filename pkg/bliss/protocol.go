// Package bliss implements the Bluetooth Low Energy control protocol for
// Hunter Douglas "Bliss Smart Blinds" motors (BLE local name HDxxxx, official
// Android app package nl.hunterdouglas.bliss).
//
// The protocol was reverse-engineered from the official app and is entirely
// unencrypted: plaintext command frames are written to the Command GATT
// characteristic and status frames arrive as notifications on the Response
// characteristic. A single login command carrying a universal, hard-coded
// password authenticates the session — there is no per-device or cloud key.
//
// This file (protocol.go) holds only the pure wire-format logic — command
// builders and the response parser — with no Bluetooth dependencies, so it can
// be unit-tested and reused independently of the BLE transport in client.go.
package bliss

import (
	"bytes"
	"encoding/binary"
	"time"
)

// GATT UUIDs for the Bliss control service. The motor is an nRF52 chip that
// happens to use Telink-style UUIDs; this is NOT the Telink or MotionBlinds
// protocol.
const (
	ServiceUUID  = "00010203-0405-0607-0809-0a0b0c0d1910" // control service
	CommandUUID  = "00010405-0405-0607-0809-0a0b0c0d1910" // write commands here
	ResponseUUID = "00010304-0405-0607-0809-0a0b0c0d1910" // status notifications
)

// DefaultPassword is the universal login password hard-coded in the official
// app (every call site uses this literal). The blind rejects commands until a
// successful login.
const DefaultPassword = "xxxxxx"

// DefaultRange is the position range for HD1300-class motors: target positions
// are scaled to 0..DefaultRange and sent as a 16-bit little-endian value.
const DefaultRange = 1000

// Fixed command frames. Roller motion commands share the header FF 58 EA 41.
var (
	cmdRollerUp   = []byte{0xFF, 0x58, 0xEA, 0x41, 0xCF, 0x03, 0x01}
	cmdRollerDown = []byte{0xFF, 0x58, 0xEA, 0x41, 0x1F, 0x03, 0x01}
	cmdRollerStop = []byte{0xFF, 0x58, 0xEA, 0x41, 0x5F, 0x03, 0x01}

	// "Fine adjust" — the app's slow/precision step used by the control UI's
	// up/down nudge buttons (fineAdjust()).
	cmdRollerFineUp   = []byte{0xFF, 0x58, 0xEA, 0x41, 0x22, 0x03, 0x01}
	cmdRollerFineDown = []byte{0xFF, 0x58, 0xEA, 0x41, 0x23, 0x03, 0x01}
	cmdReadStatus     = []byte{0xFF, 0x58, 0xEA, 0x41, 0xD1, 0x03, 0x01}
	cmdHeartbeat      = []byte{0xFF, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}
	gotoPrefix        = []byte{0xFF, 0x58, 0xEA, 0x41, 0xBF, 0x03}
	loginPrefix       = []byte{0xFF, 0x03, 0x03, 0x03, 0x03}

	// Speed presets.
	cmdSpeed100 = []byte{0xFF, 0x58, 0xEA, 0x41, 0xF0, 0x03, 0x01}
	cmdSpeed75  = []byte{0xFF, 0x58, 0xEA, 0x41, 0xF1, 0x03, 0x01}
	cmdSpeed50  = []byte{0xFF, 0x58, 0xEA, 0x41, 0xF2, 0x03, 0x01}
	cmdSpeed25  = []byte{0xFF, 0x58, 0xEA, 0x41, 0xF3, 0x03, 0x01}

	// Favorite position (single-opcode frames).
	cmdFavoriteGoto   = []byte{0xFF, 0x58, 0xEA, 0x41, 0x93}
	cmdFavoriteSet    = []byte{0xFF, 0x58, 0xEA, 0x41, 0x91}
	cmdFavoriteDelete = []byte{0xFF, 0x58, 0xEA, 0x41, 0x92}

	// Schedule / timers.
	setTimePrefix   = []byte{0xFF, 0x58, 0xEA, 0x41, 0x02, 0x00}
	addTimerHeader  = []byte{0xFF, 0x58, 0xEA, 0x41, 0x03}
	deleteTimerCmd  = []byte{0xFF, 0x58, 0xEA, 0x41, 0x03, 0x01}
	timerSlotsQuery = []byte{0xFF, 0x58, 0xEA, 0x41, 0x04}
)

// TimerSlots is the number of schedule slots on the motor (indices 1..TimerSlots).
const TimerSlots = 16

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// UpCommand returns the "roller up / open" frame.
func UpCommand() []byte { return clone(cmdRollerUp) }

// DownCommand returns the "roller down / close" frame.
func DownCommand() []byte { return clone(cmdRollerDown) }

// StopCommand returns the "stop" frame.
func StopCommand() []byte { return clone(cmdRollerStop) }

// FineUpCommand returns the "fine adjust up" (slow/precision step) frame.
func FineUpCommand() []byte { return clone(cmdRollerFineUp) }

// FineDownCommand returns the "fine adjust down" (slow/precision step) frame.
func FineDownCommand() []byte { return clone(cmdRollerFineDown) }

// ReadStatusCommand returns the frame that requests a status report.
func ReadStatusCommand() []byte { return clone(cmdReadStatus) }

// HeartbeatCommand returns the keep-alive frame.
func HeartbeatCommand() []byte { return clone(cmdHeartbeat) }

// LoginCommand builds the login frame: the login prefix followed by the
// password bytes, zero-padded to a minimum of 6 bytes (matching the app).
func LoginCommand(password string) []byte {
	pw := []byte(password)
	if len(pw) < 6 {
		p := make([]byte, 6)
		copy(p, pw)
		pw = p
	}
	return append(clone(loginPrefix), pw...)
}

// positionBytes encodes a percentage (0..100) scaled to motorRange: a 16-bit
// little-endian value when range is 1000, otherwise a single byte.
func positionBytes(percent uint8, motorRange int) []byte {
	if percent > 100 {
		percent = 100
	}
	if motorRange <= 0 {
		motorRange = DefaultRange
	}
	scaled := int(percent) * motorRange / 100
	if motorRange == 1000 {
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(scaled))
		return b
	}
	return []byte{byte(scaled)}
}

// GotoCommand builds a move-to-position frame for a percentage 0..100.
func GotoCommand(percent uint8, motorRange int) []byte {
	return append(clone(gotoPrefix), positionBytes(percent, motorRange)...)
}

// SpeedCommand returns the frame for a motor speed preset. percent is snapped to
// the nearest supported preset (25, 50, 75 or 100) at the midpoints 37/62/87.
func SpeedCommand(percent int) []byte {
	switch {
	case percent <= 37:
		return clone(cmdSpeed25)
	case percent <= 62:
		return clone(cmdSpeed50)
	case percent <= 87:
		return clone(cmdSpeed75)
	default:
		return clone(cmdSpeed100)
	}
}

// GoToFavoriteCommand moves the blind to its saved favorite position.
func GoToFavoriteCommand() []byte { return clone(cmdFavoriteGoto) }

// SetFavoriteCommand saves the current position as the favorite.
func SetFavoriteCommand() []byte { return clone(cmdFavoriteSet) }

// DeleteFavoriteCommand clears the saved favorite position.
func DeleteFavoriteCommand() []byte { return clone(cmdFavoriteDelete) }

// Days is a weekday bitmask for schedule entries; OR the day constants together.
type Days uint8

const (
	Sunday    Days = 0x01
	Monday    Days = 0x02
	Tuesday   Days = 0x04
	Wednesday Days = 0x08
	Thursday  Days = 0x10
	Friday    Days = 0x20
	Saturday  Days = 0x40

	EveryDay Days = 0x7F                                             // all seven days
	Weekdays Days = Monday | Tuesday | Wednesday | Thursday | Friday // Mon–Fri
	Weekend  Days = Saturday | Sunday                                // Sat + Sun
)

// SetClockCommand syncs the motor's internal clock to t (used by schedules).
func SetClockCommand(t time.Time) []byte {
	return append(clone(setTimePrefix),
		byte(t.Year()-2000), byte(int(t.Month())), byte(t.Day()),
		byte(t.Hour()), byte(t.Minute()), byte(t.Second()))
}

// AddTimerCommand builds a schedule entry that moves the blind to positionPct on
// the given days at hour:minute:second. index is the timer slot (1..TimerSlots);
// silent suppresses the motor's audible confirmation. This encoding targets
// single-bar motors (e.g. HD1300); dual-bar/tilt blinds append extra bytes.
func AddTimerCommand(index uint8, days Days, hour, minute, second, positionPct uint8, silent bool, motorRange int) []byte {
	silentByte := byte(0x00)
	if silent {
		silentByte = 0x80
	}
	frame := append(clone(addTimerHeader), silentByte, index, 0xB2, 0x3F, byte(days), hour, minute, second)
	return append(frame, positionBytes(positionPct, motorRange)...)
}

// DeleteTimerCommand clears the schedule slot at index.
func DeleteTimerCommand(index uint8) []byte { return append(clone(deleteTimerCmd), index) }

// TimerSlotsQueryCommand requests the next available schedule slot.
func TimerSlotsQueryCommand() []byte { return clone(timerSlotsQuery) }

// BatteryLevel is the coarse battery state reported in status frames.
type BatteryLevel uint8

const (
	BatteryNormal BatteryLevel = iota
	BatteryLow
	BatteryNone
	BatteryUnknown
)

func (b BatteryLevel) String() string {
	switch b {
	case BatteryNormal:
		return "normal"
	case BatteryLow:
		return "low"
	case BatteryNone:
		return "none"
	default:
		return "unknown"
	}
}

// EventType classifies a parsed response frame.
type EventType uint8

const (
	EventUnknown     EventType = iota
	EventStatus                // opcode 0xD2 / 0xD1: position + flags + battery
	EventLoginResult           // opcode 0xD4
	EventPasswordSet           // opcode 0xD3
	EventTimerIndex            // opcode 0xD6: next available schedule slot
	EventTimerSet              // opcode 0xD7: add/edit-timer result
	EventTimerDelete           // opcode 0xD8: delete-timer result
)

// Event is a parsed response frame. Every frame carries the fixed 4-byte header
// FF 01 02 03 followed by an opcode byte and opcode-specific payload.
type Event struct {
	Type   EventType
	Opcode byte
	Raw    []byte

	// EventStatus fields:
	Position     uint8  // device-reported position percentage (0..100)
	PositionRaw  uint16 // raw position in motor-range units (0..Range), when present
	Reversed     bool   // motor direction-reverse config flag (NOT live movement direction)
	LimitSetting bool
	RemoteLink   bool
	Battery      BatteryLevel
	HasBattery   bool // whether Battery is meaningful for this event

	// EventLoginResult / EventPasswordSet / EventTimerSet / EventTimerDelete:
	Success bool

	// EventTimerIndex:
	Index uint8
}

var responseHeader = []byte{0xFF, 0x01, 0x02, 0x03}

// ParseResponse decodes a raw notification frame from the Response
// characteristic. ok is false if the frame is too short or lacks the expected
// FF 01 02 03 header.
func ParseResponse(frame []byte) (Event, bool) {
	if len(frame) < 5 || !bytes.HasPrefix(frame, responseHeader) {
		return Event{}, false
	}
	payload := frame[4:] // opcode + data (the app drops the 4 header bytes)
	op := payload[0]
	e := Event{Opcode: op, Raw: clone(frame)}
	switch op {
	case 0xD1, 0xD2: // status: the readStatus reply (D1) and pushed report (D2) share a layout
		if len(payload) < 3 {
			return Event{}, false
		}
		flags := payload[1]
		e.Type = EventStatus
		e.Position = payload[2] // percentage; a 16-bit raw value follows at [3:5]
		e.Reversed = flags&0x01 != 0
		e.LimitSetting = flags&0x02 != 0
		e.RemoteLink = flags&0x04 != 0
		e.HasBattery = true
		// Battery is encoded in flags bits 3-4 (matches the app's gen2 parser).
		switch flags & 0x18 {
		case 0x00:
			e.Battery = BatteryNormal
		case 0x08:
			e.Battery = BatteryLow
		case 0x10:
			e.Battery = BatteryNone
		default:
			e.Battery = BatteryUnknown
		}
		if len(payload) >= 5 {
			e.PositionRaw = uint16(payload[3]) | uint16(payload[4])<<8
		}
	case 0xD4: // login result
		if len(payload) < 3 {
			return Event{}, false
		}
		e.Type = EventLoginResult
		e.Success = payload[2] > 0
	case 0xD3: // password-set result
		if len(payload) < 3 {
			return Event{}, false
		}
		e.Type = EventPasswordSet
		e.Success = payload[2] > 0
	case 0xD6: // next available timer slot
		if len(payload) < 3 {
			return Event{}, false
		}
		e.Type = EventTimerIndex
		e.Index = payload[2]
	case 0xD7: // add/edit-timer result
		if len(payload) < 3 {
			return Event{}, false
		}
		e.Type = EventTimerSet
		e.Success = payload[2] > 0
	case 0xD8: // delete-timer result
		if len(payload) < 3 {
			return Event{}, false
		}
		e.Type = EventTimerDelete
		e.Success = payload[2] > 0
	default:
		e.Type = EventUnknown
	}
	return e, true
}
