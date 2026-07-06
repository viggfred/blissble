package blissha

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/viggfred/blissble/pkg/bliss"
)

// testManager builds a manager wired for pure-logic tests: a real (unconnected)
// bliss.Blind whose State() is a safe cached zero value, and buffered channels.
// No MQTT client and no BLE connection are involved.
func testManager(auto Automation, loc Location) *manager {
	auto.applyDefaults()
	return &manager{
		cfg:     BlindConfig{Name: "Test", MAC: "AA:BB:CC:DD:EE:01"},
		blind:   bliss.New(bliss.Config{MACAddress: "AA:BB:CC:DD:EE:01"}),
		actions: make(chan blindOp, 8),
		control: make(chan controlMsg, 8),
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		auto:    auto,
		loc:     loc,
	}
}

func alwaysAwake() Automation {
	return Automation{Mode: ModeSchedule, Schedule: []ScheduleEntry{{OpenAt: 0, CloseAt: 1439, OpenHA: 100, SleepHA: 0}}}
}

func TestEvaluateQueuesAutoMoveWithoutBLE(t *testing.T) {
	m := testManager(alwaysAwake(), Location{Zone: time.UTC})
	m.posKnown = true // cached position is 0 (closed); schedule wants 100 (open)

	m.evaluate()

	require.False(t, m.blind.State().Connected, "evaluate must not open a BLE connection")
	select {
	case op := <-m.actions:
		require.Equal(t, opAuto, op.origin, "automation move must be tagged opAuto")
		require.True(t, op.track)
	default:
		t.Fatal("expected an automation move to be queued")
	}
	require.True(t, m.ctrl.HasLastTarget)
	require.Equal(t, 100, m.ctrl.LastTarget)
}

func TestEvaluateOffDoesNothing(t *testing.T) {
	m := testManager(Automation{Mode: ModeOff}, Location{})
	m.evaluate()
	select {
	case <-m.actions:
		t.Fatal("disabled automation must not queue moves")
	default:
	}
}

func TestNoteOriginOverride(t *testing.T) {
	m := testManager(alwaysAwake(), Location{Zone: time.UTC})

	m.noteOrigin(blindOp{origin: opManual})
	require.False(t, m.ctrl.OverrideUntil.IsZero(), "manual op pauses automation")

	m.ctrl.OverrideUntil = time.Time{}
	m.noteOrigin(blindOp{origin: opAuto})
	require.True(t, m.ctrl.OverrideUntil.IsZero(), "automation's own move must not self-suspend")
}

func TestObserveExternalMove(t *testing.T) {
	m := testManager(alwaysAwake(), Location{Zone: time.UTC})
	m.observe(100, false) // learn position; a non-poll observation never overrides
	require.True(t, m.posKnown)
	require.True(t, m.ctrl.OverrideUntil.IsZero())

	m.observe(30, true) // big change on a routine poll → a human/RF moved it
	require.False(t, m.ctrl.OverrideUntil.IsZero(), "external move should pause automation")

	// A small drift on a poll is jitter, not an external move.
	m2 := testManager(alwaysAwake(), Location{Zone: time.UTC})
	m2.observe(100, false)
	m2.observe(98, true)
	require.True(t, m2.ctrl.OverrideUntil.IsZero())

	// A large change from our OWN move (settle, poll=false) must not self-suspend,
	// even if the motor settles far from where it started.
	m3 := testManager(alwaysAwake(), Location{Zone: time.UTC})
	m3.observe(100, false)
	m3.observe(30, false)
	require.True(t, m3.ctrl.OverrideUntil.IsZero(), "automation's own moves never override")
}

func TestEvaluateUsesCachedPositionNotWipedState(t *testing.T) {
	// Regression: after an idle disconnect bliss.State().Position is zeroed. The
	// controller must use the manager's cached position instead, so an already-open
	// blind isn't seen as closed and needlessly re-moved.
	m := testManager(alwaysAwake(), Location{Zone: time.UTC})
	m.lastPosHA = 100 // we last knew it was open; awake target is also 100
	m.posKnown = true

	m.evaluate()
	select {
	case <-m.actions:
		t.Fatal("must not move: cached position already matches the target")
	default:
	}
}

func TestApplyControlFoldsSignals(t *testing.T) {
	m := testManager(Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180}}, Location{Lat: 40, Lon: 0})
	m.posKnown = true

	m.applyControl(controlMsg{kind: ctrlRoomOcc, b: true})
	require.Equal(t, TriYes, m.sig.RoomOccupied)

	m.applyControl(controlMsg{kind: ctrlLux, f: 42000})
	require.True(t, m.sig.LuxKnown)
	require.InDelta(t, 42000, m.sig.Lux, 1e-9)

	m.applyControl(controlMsg{kind: ctrlHomeAway, b: false})
	require.Equal(t, TriNo, m.sig.HomeAway)
}

func TestSendControlNonBlocking(t *testing.T) {
	m := testManager(alwaysAwake(), Location{Zone: time.UTC})
	// Fill the buffer and then some; sendControl must never block or panic.
	for range 20 {
		m.sendControl(controlMsg{kind: ctrlRoomOcc, b: true})
	}
	require.Len(t, m.control, cap(m.control), "excess signals are dropped, not blocked")
}
