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
	cmdReadStatus = []byte{0xFF, 0x58, 0xEA, 0x41, 0xD1, 0x03, 0x01}
	cmdHeartbeat  = []byte{0xFF, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}
	gotoPrefix    = []byte{0xFF, 0x58, 0xEA, 0x41, 0xBF, 0x03}
	loginPrefix   = []byte{0xFF, 0x03, 0x03, 0x03, 0x03}
)

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

// GotoCommand builds a move-to-position frame. percent is clamped to 0..100 and
// scaled to motorRange; for a range of 1000 the value is encoded as a 16-bit
// little-endian integer, otherwise as a single byte.
func GotoCommand(percent uint8, motorRange int) []byte {
	if percent > 100 {
		percent = 100
	}
	if motorRange <= 0 {
		motorRange = DefaultRange
	}
	scaled := int(percent) * motorRange / 100
	out := clone(gotoPrefix)
	if motorRange == 1000 {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(scaled))
		return append(out, b[:]...)
	}
	return append(out, byte(scaled))
}

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
	EventStatus                // opcode 0xD2: position + flags + battery
	EventLoginResult           // opcode 0xD4
	EventPasswordSet           // opcode 0xD3
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
	Direction    bool
	LimitSetting bool
	RemoteLink   bool
	Battery      BatteryLevel
	HasBattery   bool // whether Battery is meaningful for this event

	// EventLoginResult / EventPasswordSet:
	Success bool
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
	case 0xD2: // pushed status report: flags at [1], position % at [2]
		if len(payload) < 3 {
			return Event{}, false
		}
		flags := payload[1]
		e.Type = EventStatus
		e.Position = payload[2]
		e.Direction = flags&0x01 != 0
		e.LimitSetting = flags&0x02 != 0
		e.RemoteLink = flags&0x04 != 0
		e.HasBattery = true
		switch flags & 0x18 {
		case 0x00:
			e.Battery = BatteryNormal
		case 0x10:
			e.Battery = BatteryLow
		case 0x18:
			e.Battery = BatteryNone
		default:
			e.Battery = BatteryUnknown
		}
		if len(payload) >= 5 {
			e.PositionRaw = uint16(payload[3]) | uint16(payload[4])<<8
		}
	case 0xD1: // readStatus reply: position % at [2], raw 16-bit at [3:5]
		if len(payload) < 3 {
			return Event{}, false
		}
		e.Type = EventStatus
		e.Position = payload[2]
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
	default:
		e.Type = EventUnknown
	}
	return e, true
}
