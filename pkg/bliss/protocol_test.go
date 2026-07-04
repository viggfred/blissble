package bliss

import (
	"bytes"
	"encoding/hex"
	"testing"
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

func TestParseRejectsGarbage(t *testing.T) {
	if _, ok := ParseResponse([]byte{0x01, 0x02}); ok {
		t.Error("short frame should not parse")
	}
	if _, ok := ParseResponse([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE}); ok {
		t.Error("wrong header should not parse")
	}
}
