package blissha

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Reference instants (12:00 UTC ≈ solar noon at longitude 0, sun ~due south).
var (
	summerNoon = time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
	winterNoon = time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC)
	midnight   = time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC)
)

func defaulted(a Automation) Automation { a.applyDefaults(); return a }

// A south-facing glare setup at lat 40, lon 0.
func glareInput(now time.Time, cur int, sig Signals) DecisionInput {
	return DecisionInput{
		Cfg:     defaulted(Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180}}),
		Loc:     Location{Lat: 40, Lon: 0},
		Now:     now,
		Current: Position{HA: cur, Known: true},
		Signals: sig,
	}
}

func TestDecideOff(t *testing.T) {
	in := DecisionInput{Cfg: defaulted(Automation{Mode: ModeOff}), Now: summerNoon, Current: Position{HA: 50, Known: true}}
	d := Decide(in)
	require.False(t, d.Move)
	require.Equal(t, "off", d.Reason)
}

func TestDecideOverride(t *testing.T) {
	in := glareInput(winterNoon, 100, Signals{})
	in.State.OverrideUntil = winterNoon.Add(time.Hour)
	d := Decide(in)
	require.False(t, d.Move, "override suspends automation")
	require.Equal(t, "override", d.Reason)
	require.InDelta(t, time.Hour.Seconds(), d.NextEvalIn.Seconds(), 2, "re-eval when override expires")

	// Once expired, automation resumes and can move.
	in.State.OverrideUntil = winterNoon.Add(-time.Minute)
	require.True(t, Decide(in).Move)
}

func TestDecideGlare(t *testing.T) {
	// Winter noon: low sun on a south window → shade closes substantially.
	d := Decide(glareInput(winterNoon, 100, Signals{}))
	require.True(t, d.Move)
	require.Less(t, d.TargetHA, 40, "low sun should close the shade a lot")
	require.Equal(t, 0, d.TargetHA%5, "target quantized to step")

	// Summer noon: high sun barely penetrates → stay open.
	d = Decide(glareInput(summerNoon, 100, Signals{}))
	require.Equal(t, 100, d.TargetHA, "high sun → stay open")
	require.False(t, d.Move, "already open")

	// Night: sun below horizon → not on window → open.
	d = Decide(glareInput(midnight, 30, Signals{}))
	require.True(t, d.Move)
	require.Equal(t, 100, d.TargetHA)
}

func TestDecideGlareOccupancyGate(t *testing.T) {
	// require_occupancy + empty room + when_unoccupied off → no action.
	in := glareInput(winterNoon, 100, Signals{RoomOccupied: TriNo})
	in.Cfg.RequireOccupancy = true
	in.Cfg.WhenUnoccupied = ModeOff
	d := Decide(in)
	require.False(t, d.Move)
	require.Equal(t, "gated_unoccupied", d.Reason)

	// Occupied → glare runs.
	in.Signals.RoomOccupied = TriYes
	require.True(t, Decide(in).Move)

	// Unknown occupancy defaults to "occupied" so it still runs.
	in.Signals.RoomOccupied = TriUnknown
	require.True(t, Decide(in).Move)
}

func TestDecideSunShadeHysteresis(t *testing.T) {
	shade := func(now time.Time, cur int, st ControllerState) DecisionInput {
		return DecisionInput{
			Cfg: defaulted(Automation{Mode: ModeSunShade, Window: Window{AzimuthDeg: 180}, ShadeClosedHA: 25}),
			Loc: Location{Lat: 40, Lon: 0},
			Now: now, Current: Position{HA: cur, Known: true}, State: st,
		}
	}
	d := Decide(shade(winterNoon, 100, ControllerState{}))
	require.Equal(t, 25, d.TargetHA, "sun on window → closed position")
	require.True(t, d.State.Latched)

	d = Decide(shade(midnight, 25, ControllerState{}))
	require.Equal(t, 100, d.TargetHA, "no sun → open")
	require.False(t, d.State.Latched)
}

func scheduleInput(now time.Time, cur int, entries []ScheduleEntry, loc Location) DecisionInput {
	return DecisionInput{
		Cfg:     defaulted(Automation{Mode: ModeSchedule, Schedule: entries}),
		Loc:     loc,
		Now:     now,
		Current: Position{HA: cur, Known: true},
	}
}

func TestDecideSchedule(t *testing.T) {
	loc := Location{Lat: 40, Lon: 0, Zone: time.UTC}
	entry := ScheduleEntry{OpenAt: 7 * 60, CloseAt: 22 * 60, SleepHA: 0, OpenHA: 100}

	// Middle of the night → asleep.
	d := Decide(scheduleInput(time.Date(2024, 6, 21, 3, 0, 0, 0, time.UTC), 100, []ScheduleEntry{entry}, loc))
	require.Equal(t, 0, d.TargetHA)
	require.Equal(t, "schedule_asleep", d.Reason)

	// Midday → awake/open.
	d = Decide(scheduleInput(time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC), 0, []ScheduleEntry{entry}, loc))
	require.Equal(t, 100, d.TargetHA)
	require.Equal(t, "schedule_awake", d.Reason)
}

func TestDecideWakeRamp(t *testing.T) {
	loc := Location{Lat: 40, Lon: 0, Zone: time.UTC}
	entry := ScheduleEntry{OpenAt: 7 * 60, CloseAt: 22 * 60, SleepHA: 0, OpenHA: 100, Ramp: 20 * time.Minute}
	// 10 minutes into a 20-minute ramp → halfway open.
	d := Decide(scheduleInput(time.Date(2024, 6, 21, 7, 10, 0, 0, time.UTC), 0, []ScheduleEntry{entry}, loc))
	require.True(t, d.Move)
	require.Equal(t, 50, d.TargetHA)
	require.Equal(t, "wake_ramp", d.Reason)
	require.Less(t, d.NextEvalIn, 5*time.Minute, "step through the ramp quickly")
	require.GreaterOrEqual(t, d.NextEvalIn, 30*time.Second)
}

func TestDecideScheduleSunriseClamp(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	require.NoError(t, err)
	loc := Location{Lat: 59.91, Lon: 10.75, Zone: oslo}
	// 08:00 local on the December solstice — before Oslo's ~09:18 sunrise.
	now := time.Date(2024, 12, 21, 8, 0, 0, 0, oslo)

	clamped := ScheduleEntry{OpenAt: 7 * 60, CloseAt: 22 * 60, SleepHA: 0, OpenHA: 100, NotBeforeSunrise: true}
	d := Decide(scheduleInput(now, 0, []ScheduleEntry{clamped}, loc))
	require.Equal(t, 0, d.TargetHA, "clamped open time keeps it asleep until sunrise")

	unclamped := ScheduleEntry{OpenAt: 7 * 60, CloseAt: 22 * 60, SleepHA: 0, OpenHA: 100}
	d = Decide(scheduleInput(now, 0, []ScheduleEntry{unclamped}, loc))
	require.Equal(t, 100, d.TargetHA, "without the clamp, 08:00 is past the 07:00 open")
}

func TestDecideThermal(t *testing.T) {
	thermal := func(now time.Time, sig Signals) DecisionInput {
		// Thermal thresholds have no library default (0 is a valid value), so a
		// struct-based caller sets them explicitly (the YAML loader resolves them).
		return DecisionInput{
			Cfg: defaulted(Automation{Mode: ModeThermal, Window: Window{AzimuthDeg: 180},
				Thermal: Thermal{SummerCloseHA: 20, WinterOpenHA: 100, HotTempC: 24, ColdTempC: 10}}),
			Loc: Location{Lat: 40, Lon: 0}, Now: now, Current: Position{HA: 100, Known: true}, Signals: sig,
		}
	}
	// Summer + sun on window → close to block heat.
	require.Equal(t, 20, Decide(thermal(summerNoon, Signals{})).TargetHA)
	// Winter → open for passive gain.
	require.Equal(t, 100, Decide(thermal(winterNoon, Signals{})).TargetHA)
	// Known hot temperature overrides the month (winter date, warm day).
	require.Equal(t, 20, Decide(thermal(winterNoon, Signals{TempKnown: true, OutdoorTempC: 30})).TargetHA)
}

func TestDecidePresenceSim(t *testing.T) {
	in := DecisionInput{
		Cfg:     defaulted(Automation{Mode: ModeSunShade, Window: Window{AzimuthDeg: 180}, PresenceSim: PresenceSim{Enabled: true, MorningOpen: 7 * 60, EveningClose: 21 * 60, OpenHA: 100, CloseHA: 0}}),
		Loc:     Location{Lat: 40, Lon: 0, Zone: time.UTC},
		Current: Position{HA: 0, Known: true},
		Signals: Signals{HomeAway: TriYes},
	}
	in.Now = time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
	d := Decide(in)
	require.Equal(t, "presence_sim", d.Reason)
	require.Equal(t, 100, d.TargetHA)

	in.Now = time.Date(2024, 6, 21, 23, 0, 0, 0, time.UTC)
	require.Equal(t, 0, Decide(in).TargetHA)
}

func TestDecidePrivacyAfterDark(t *testing.T) {
	in := glareInput(midnight, 100, Signals{})
	in.Cfg.Privacy = Privacy{AfterDark: true, CloseHA: 0}
	d := Decide(in)
	require.Equal(t, "privacy_dark", d.Reason)
	require.Equal(t, 0, d.TargetHA)
	require.True(t, d.Move)
}

func TestDecideGuards(t *testing.T) {
	// Deadband: a would-be target within 5% of current does not move.
	in := glareInput(winterNoon, 8, Signals{}) // glare target ~5-10 at winter noon
	d := Decide(in)
	if absInt(d.TargetHA-8) < 5 {
		require.False(t, d.Move, "within deadband")
		require.Equal(t, "deadband", d.Reason)
	}

	// Min-move-interval blocks a move made too soon after the last.
	in = glareInput(winterNoon, 100, Signals{})
	in.State.LastMoveAt = winterNoon.Add(-time.Minute) // < 10m default
	d = Decide(in)
	require.False(t, d.Move)
	require.Equal(t, "min_interval", d.Reason)

	// Hourly rate cap.
	in = glareInput(winterNoon, 100, Signals{})
	in.State.HourStart = winterNoon
	in.State.MovesThisHour = 8 // == default MaxMovesPerHour
	d = Decide(in)
	require.False(t, d.Move)
	require.Equal(t, "rate_capped", d.Reason)
}

func TestDecideUnknownPosition(t *testing.T) {
	// Proportional glare refuses to move until the position is known.
	in := glareInput(winterNoon, 0, Signals{})
	in.Current = Position{Known: false}
	d := Decide(in)
	require.False(t, d.Move)
	require.Equal(t, "await_position", d.Reason)

	// A definite schedule target may move even with unknown position.
	loc := Location{Lat: 40, Lon: 0, Zone: time.UTC}
	entry := ScheduleEntry{OpenAt: 7 * 60, CloseAt: 22 * 60, SleepHA: 0, OpenHA: 100}
	si := scheduleInput(time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC), 0, []ScheduleEntry{entry}, loc)
	si.Current = Position{Known: false}
	require.True(t, Decide(si).Move)
}

func TestDecideRateCapNextEval(t *testing.T) {
	in := glareInput(winterNoon, 100, Signals{})
	in.State.HourStart = winterNoon.Add(-20 * time.Minute) // ~40 min left in the window
	in.State.MovesThisHour = 8                             // == default MaxMovesPerHour
	d := Decide(in)
	require.False(t, d.Move)
	require.Equal(t, "rate_capped", d.Reason)
	require.Less(t, d.NextEvalIn, time.Hour, "re-eval when the window resets, not OverrideTimeout later")
	require.Greater(t, d.NextEvalIn, 30*time.Minute)
}

func TestDecideWakeRampWrapsMidnight(t *testing.T) {
	loc := Location{Lat: 40, Lon: 0, Zone: time.UTC}
	entry := ScheduleEntry{OpenAt: 23*60 + 50, CloseAt: 8 * 60, SleepHA: 0, OpenHA: 100, Ramp: 30 * time.Minute}
	// 00:10 is 20 minutes into a ramp that began at 23:50 → ~2/3 open.
	d := Decide(scheduleInput(time.Date(2024, 6, 22, 0, 10, 0, 0, time.UTC), 0, []ScheduleEntry{entry}, loc))
	require.Equal(t, "wake_ramp", d.Reason, "ramp must continue across midnight")
	require.InDelta(t, 67, d.TargetHA, 5)
}

func TestDecideSunsetClampWrapWindow(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	require.NoError(t, err)
	loc := Location{Lat: 59.91, Lon: 10.75, Zone: oslo}
	// Waking window wraps past midnight (07:00 → 01:00) with a sunset clamp.
	entry := ScheduleEntry{OpenAt: 7 * 60, CloseAt: 1 * 60, SleepHA: 0, OpenHA: 100, NotAfterSunset: true}
	// 23:00 in June is past Oslo's ~22:40 sunset, so the clamp should force asleep.
	now := time.Date(2024, 6, 21, 23, 0, 0, 0, oslo)
	d := Decide(scheduleInput(now, 100, []ScheduleEntry{entry}, loc))
	require.Equal(t, 0, d.TargetHA, "sunset clamp applies even when the window wraps midnight")
}
