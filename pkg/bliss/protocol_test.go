package bliss

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err, "bad hex %q", s)
	return b
}

func TestLoginCommand(t *testing.T) {
	// Validated live against a real HD1300.
	require.Equal(t, mustHex(t, "ff03030303787878787878"), LoginCommand("xxxxxx"))
	// Passwords shorter than 6 bytes are zero-padded to 6.
	require.Equal(t, mustHex(t, "ff03030303313200000000"), LoginCommand("12"))
	// Passwords of 6+ bytes are sent verbatim (no truncation/padding).
	require.Equal(t, mustHex(t, "ff030303036c6f6e6770617373"), LoginCommand("longpass"))
}

func TestFixedCommands(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{"up", UpCommand(), "ff58ea41cf0301"},
		{"down", DownCommand(), "ff58ea411f0301"},
		{"stop", StopCommand(), "ff58ea415f0301"},
		{"fineup", FineUpCommand(), "ff58ea41220301"},
		{"finedown", FineDownCommand(), "ff58ea41230301"},
		{"status", ReadStatusCommand(), "ff58ea41d10301"},
		{"heartbeat", HeartbeatCommand(), "ff010101010101"},
	}
	for _, c := range cases {
		require.Equal(t, mustHex(t, c.want), c.got, c.name)
	}
}

func TestGotoCommand(t *testing.T) {
	// range 1000: 50% -> 500 = 0x01F4 -> LE "f4 01"
	require.Equal(t, mustHex(t, "ff58ea41bf03f401"), GotoCommand(50, 1000))
	// 100% -> 1000 = 0x03E8 -> LE "e8 03"
	require.Equal(t, mustHex(t, "ff58ea41bf03e803"), GotoCommand(100, 1000))
	// single-byte range: 50% of 100 -> 0x32
	require.Equal(t, mustHex(t, "ff58ea41bf0332"), GotoCommand(50, 100))
	// clamp
	require.Equal(t, mustHex(t, "ff58ea41bf03e803"), GotoCommand(200, 1000))
}

func TestParseLoginResponse(t *testing.T) {
	// Real captured frame: login succeeded.
	ev, ok := ParseResponse(mustHex(t, "ff010203d40001"))
	require.True(t, ok)
	require.Equal(t, EventLoginResult, ev.Type)
	require.True(t, ev.Success)
}

func TestParseStatusResponse(t *testing.T) {
	// FF 01 02 03 D2 <flags=0x09> <pos=0x40>: reverse bit set, low battery (0x08).
	ev, ok := ParseResponse(mustHex(t, "ff010203d20940"))
	require.True(t, ok)
	require.Equal(t, EventStatus, ev.Type)
	require.EqualValues(t, 0x40, ev.Position)
	require.True(t, ev.HasBattery)
	require.Equal(t, BatteryLow, ev.Battery)
	require.True(t, ev.Reversed, "reverse bit should be set")
}

func TestSpeedCommand(t *testing.T) {
	cases := map[int]string{100: "ff58ea41f00301", 75: "ff58ea41f10301", 50: "ff58ea41f20301", 25: "ff58ea41f30301"}
	for pct, want := range cases {
		require.Equal(t, mustHex(t, want), SpeedCommand(pct), "speed(%d)", pct)
	}
	// snapping to nearest preset
	require.Equal(t, mustHex(t, "ff58ea41f20301"), SpeedCommand(60), "speed(60) should snap to 50")
}

func TestFavoriteCommands(t *testing.T) {
	require.Equal(t, mustHex(t, "ff58ea4193"), GoToFavoriteCommand())
	require.Equal(t, mustHex(t, "ff58ea4191"), SetFavoriteCommand())
	require.Equal(t, mustHex(t, "ff58ea4192"), DeleteFavoriteCommand())
}

func TestSetClockCommand(t *testing.T) {
	// 2026-07-04 22:15:30 -> 1a 07 04 16 0f 1e
	tm := time.Date(2026, time.July, 4, 22, 15, 30, 0, time.UTC)
	require.Equal(t, mustHex(t, "ff58ea410200"+"1a0704160f1e"), SetClockCommand(tm))
}

func TestAddDeleteTimerCommand(t *testing.T) {
	// slot 1, weekdays (0x3E), 07:30:00, 100% (=1000=e803), not silent
	require.Equal(t, mustHex(t, "ff58ea41030001b23f3e071e00e803"), AddTimerCommand(1, Weekdays, 7, 30, 0, 100, false, 1000))
	require.EqualValues(t, 0x3E, Weekdays, "Weekdays mask")
	require.Equal(t, mustHex(t, "ff58ea41030105"), DeleteTimerCommand(5))
}

func TestParseTimerResponses(t *testing.T) {
	ev, ok := ParseResponse(mustHex(t, "ff010203d70001"))
	require.True(t, ok)
	require.Equal(t, EventTimerSet, ev.Type)
	require.True(t, ev.Success)

	ev, ok = ParseResponse(mustHex(t, "ff010203d80001"))
	require.True(t, ok)
	require.Equal(t, EventTimerDelete, ev.Type)
	require.True(t, ev.Success)

	ev, ok = ParseResponse(mustHex(t, "ff010203d6000a"))
	require.True(t, ok)
	require.Equal(t, EventTimerIndex, ev.Type)
	require.EqualValues(t, 10, ev.Index)
}

func TestParseD1StatusReply(t *testing.T) {
	// Real captured readStatus reply at 75%: D1 02 4B EE02 CE1F 00.
	ev, ok := ParseResponse(mustHex(t, "ff010203d1024bee02ce1f00"))
	require.True(t, ok)
	require.Equal(t, EventStatus, ev.Type)
	require.EqualValues(t, 75, ev.Position)
	require.EqualValues(t, 750, ev.PositionRaw)
	// D1 carries battery in the flags byte too (0x02 & 0x18 == 0 -> normal).
	require.True(t, ev.HasBattery)
	require.Equal(t, BatteryNormal, ev.Battery)
}

func TestParseD2BatteryAndRaw(t *testing.T) {
	// D2 flags 0x10 => battery none; position % at [2], raw 16-bit LE at [3..4].
	ev, ok := ParseResponse(mustHex(t, "ff010203d21064e803"))
	require.True(t, ok)
	require.Equal(t, EventStatus, ev.Type)
	require.True(t, ev.HasBattery)
	require.Equal(t, BatteryNone, ev.Battery)
	require.EqualValues(t, 100, ev.Position)
	require.EqualValues(t, 1000, ev.PositionRaw)
}

func TestParseCapturedD1(t *testing.T) {
	// Real readStatus reply captured from an HD1300: position 99% (raw 999),
	// flags 0x02 => reverse off, battery normal.
	ev, ok := ParseResponse(mustHex(t, "ff010203d10263e703ce1fb3"))
	require.True(t, ok)
	require.Equal(t, EventStatus, ev.Type)
	require.EqualValues(t, 99, ev.Position)
	require.EqualValues(t, 999, ev.PositionRaw)
	require.True(t, ev.HasBattery)
	require.Equal(t, BatteryNormal, ev.Battery)
	require.False(t, ev.Reversed)
}

func TestParseUnknownOpcode(t *testing.T) {
	ev, ok := ParseResponse(mustHex(t, "ff010203ab0102"))
	require.True(t, ok)
	require.Equal(t, EventUnknown, ev.Type)
	require.EqualValues(t, 0xAB, ev.Opcode)
}

func TestParseRejectsGarbage(t *testing.T) {
	_, ok := ParseResponse([]byte{0x01, 0x02})
	require.False(t, ok, "short frame should not parse")
	_, ok = ParseResponse([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE})
	require.False(t, ok, "wrong header should not parse")
}
