package bliss

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestLoginCommand(t *testing.T) {
	// Validated live against a real HD1300.
	if got := LoginCommand("xxxxxx"); !bytes.Equal(got, mustHex(t, "ff03030303787878787878")) {
		t.Fatalf("login(xxxxxx) = %x", got)
	}
	// Passwords shorter than 6 bytes are zero-padded to 6.
	if got := LoginCommand("12"); !bytes.Equal(got, mustHex(t, "ff03030303313200000000")) {
		t.Fatalf("login(12) = %x", got)
	}
	// Passwords of 6+ bytes are sent verbatim (no truncation/padding).
	if got := LoginCommand("longpass"); !bytes.Equal(got, mustHex(t, "ff030303036c6f6e6770617373")) {
		t.Fatalf("login(longpass) = %x", got)
	}
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
		if !bytes.Equal(c.got, mustHex(t, c.want)) {
			t.Errorf("%s = %x, want %s", c.name, c.got, c.want)
		}
	}
}

func TestGotoCommand(t *testing.T) {
	// range 1000: 50% -> 500 = 0x01F4 -> LE "f4 01"
	if got := GotoCommand(50, 1000); !bytes.Equal(got, mustHex(t, "ff58ea41bf03f401")) {
		t.Errorf("goto(50,1000) = %x", got)
	}
	// 100% -> 1000 = 0x03E8 -> LE "e8 03"
	if got := GotoCommand(100, 1000); !bytes.Equal(got, mustHex(t, "ff58ea41bf03e803")) {
		t.Errorf("goto(100,1000) = %x", got)
	}
	// single-byte range: 50% of 100 -> 0x32
	if got := GotoCommand(50, 100); !bytes.Equal(got, mustHex(t, "ff58ea41bf0332")) {
		t.Errorf("goto(50,100) = %x", got)
	}
	// clamp
	if got := GotoCommand(200, 1000); !bytes.Equal(got, mustHex(t, "ff58ea41bf03e803")) {
		t.Errorf("goto(200,1000) clamp = %x", got)
	}
}

func TestParseLoginResponse(t *testing.T) {
	// Real captured frame: login succeeded.
	ev, ok := ParseResponse(mustHex(t, "ff010203d40001"))
	if !ok || ev.Type != EventLoginResult || !ev.Success {
		t.Fatalf("login parse: ok=%v ev=%+v", ok, ev)
	}
}

func TestParseStatusResponse(t *testing.T) {
	// FF 01 02 03 D2 <flags=0x11> <pos=0x40>: dir bit set, low battery.
	ev, ok := ParseResponse(mustHex(t, "ff010203d21140"))
	if !ok || ev.Type != EventStatus {
		t.Fatalf("status parse: ok=%v ev=%+v", ok, ev)
	}
	if ev.Position != 0x40 {
		t.Errorf("position = %d, want 64", ev.Position)
	}
	if ev.Battery != BatteryLow {
		t.Errorf("battery = %v, want low", ev.Battery)
	}
	if !ev.Direction {
		t.Errorf("direction should be set")
	}
}

func TestSpeedCommand(t *testing.T) {
	cases := map[int]string{100: "ff58ea41f00301", 75: "ff58ea41f10301", 50: "ff58ea41f20301", 25: "ff58ea41f30301"}
	for pct, want := range cases {
		if got := SpeedCommand(pct); !bytes.Equal(got, mustHex(t, want)) {
			t.Errorf("speed(%d) = %x, want %s", pct, got, want)
		}
	}
	// snapping to nearest preset
	if got := SpeedCommand(60); !bytes.Equal(got, mustHex(t, "ff58ea41f20301")) {
		t.Errorf("speed(60) should snap to 50: %x", got)
	}
}

func TestFavoriteCommands(t *testing.T) {
	if got := GoToFavoriteCommand(); !bytes.Equal(got, mustHex(t, "ff58ea4193")) {
		t.Errorf("gotoFavorite = %x", got)
	}
	if got := SetFavoriteCommand(); !bytes.Equal(got, mustHex(t, "ff58ea4191")) {
		t.Errorf("setFavorite = %x", got)
	}
	if got := DeleteFavoriteCommand(); !bytes.Equal(got, mustHex(t, "ff58ea4192")) {
		t.Errorf("deleteFavorite = %x", got)
	}
}

func TestSetClockCommand(t *testing.T) {
	// 2026-07-04 22:15:30 -> 1a 07 04 16 0f 1e
	tm := time.Date(2026, time.July, 4, 22, 15, 30, 0, time.UTC)
	if got := SetClockCommand(tm); !bytes.Equal(got, mustHex(t, "ff58ea410200"+"1a0704160f1e")) {
		t.Errorf("setClock = %x", got)
	}
}

func TestAddDeleteTimerCommand(t *testing.T) {
	// slot 1, weekdays (0x3E), 07:30:00, 100% (=1000=e803), not silent
	got := AddTimerCommand(1, Weekdays, 7, 30, 0, 100, false, 1000)
	if !bytes.Equal(got, mustHex(t, "ff58ea41030001b23f3e071e00e803")) {
		t.Errorf("addTimer = %x", got)
	}
	if Weekdays != 0x3E {
		t.Errorf("Weekdays mask = %#x, want 0x3e", byte(Weekdays))
	}
	if got := DeleteTimerCommand(5); !bytes.Equal(got, mustHex(t, "ff58ea41030105")) {
		t.Errorf("deleteTimer = %x", got)
	}
}

func TestParseTimerResponses(t *testing.T) {
	if ev, ok := ParseResponse(mustHex(t, "ff010203d70001")); !ok || ev.Type != EventTimerSet || !ev.Success {
		t.Errorf("D7: ok=%v ev=%+v", ok, ev)
	}
	if ev, ok := ParseResponse(mustHex(t, "ff010203d80001")); !ok || ev.Type != EventTimerDelete || !ev.Success {
		t.Errorf("D8: ok=%v ev=%+v", ok, ev)
	}
	if ev, ok := ParseResponse(mustHex(t, "ff010203d6000a")); !ok || ev.Type != EventTimerIndex || ev.Index != 10 {
		t.Errorf("D6: ok=%v ev=%+v", ok, ev)
	}
}

func TestParseD1StatusReply(t *testing.T) {
	// Real captured readStatus reply at 75%: D1 02 4B EE02 CE1F 00.
	ev, ok := ParseResponse(mustHex(t, "ff010203d1024bee02ce1f00"))
	if !ok || ev.Type != EventStatus {
		t.Fatalf("D1 parse: ok=%v ev=%+v", ok, ev)
	}
	if ev.Position != 75 {
		t.Errorf("position = %d, want 75", ev.Position)
	}
	if ev.PositionRaw != 750 {
		t.Errorf("positionRaw = %d, want 750", ev.PositionRaw)
	}
	if ev.HasBattery {
		t.Errorf("D1 reply should not claim battery info")
	}
}

func TestParseD2BatteryAndRaw(t *testing.T) {
	// D2 flags 0x18 => battery none; position at [2], raw at [3..4] (LE).
	ev, ok := ParseResponse(mustHex(t, "ff010203d2186414e8"))
	if !ok || ev.Type != EventStatus || !ev.HasBattery {
		t.Fatalf("D2 parse: ok=%v ev=%+v", ok, ev)
	}
	if ev.Battery != BatteryNone {
		t.Errorf("battery = %v, want none", ev.Battery)
	}
	if ev.Position != 0x64 {
		t.Errorf("position = %d, want 100", ev.Position)
	}
	if ev.PositionRaw != 0xE814 {
		t.Errorf("positionRaw = %#x, want 0xe814", ev.PositionRaw)
	}
}

func TestParseUnknownOpcode(t *testing.T) {
	ev, ok := ParseResponse(mustHex(t, "ff010203ab0102"))
	if !ok || ev.Type != EventUnknown || ev.Opcode != 0xAB {
		t.Fatalf("unknown opcode: ok=%v ev=%+v", ok, ev)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, ok := ParseResponse([]byte{0x01, 0x02}); ok {
		t.Error("short frame should not parse")
	}
	if _, ok := ParseResponse([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE}); ok {
		t.Error("wrong header should not parse")
	}
}
