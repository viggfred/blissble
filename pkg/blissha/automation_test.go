package blissha

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseAutomationMode(t *testing.T) {
	for s, want := range map[string]AutomationMode{
		"": ModeOff, "off": ModeOff, "sun_glare": ModeSunGlare,
		"sun_shade": ModeSunShade, "schedule": ModeSchedule, "thermal": ModeThermal,
		"SUN_GLARE": ModeSunGlare,
	} {
		got, err := ParseAutomationMode(s)
		require.NoError(t, err, s)
		require.Equal(t, want, got, s)
		if want != ModeOff {
			require.Equal(t, want, mustMode(t, want.String()), "round-trip %s", s)
		}
	}
	_, err := ParseAutomationMode("nope")
	require.Error(t, err)
}

func mustMode(t *testing.T, s string) AutomationMode {
	t.Helper()
	m, err := ParseAutomationMode(s)
	require.NoError(t, err)
	return m
}

func TestParseDaySet(t *testing.T) {
	got, err := ParseDaySet([]string{"mon", "wed", "fri"})
	require.NoError(t, err)
	require.Equal(t, Mon|Wed|Fri, got)

	got, err = ParseDaySet([]string{"weekdays"})
	require.NoError(t, err)
	require.Equal(t, Weekdays, got)

	_, err = ParseDaySet([]string{"funday"})
	require.Error(t, err)
}

func TestDaySetContains(t *testing.T) {
	require.True(t, DaySet(0).Contains(time.Monday), "empty set = every day")
	require.True(t, Weekdays.Contains(time.Monday))
	require.False(t, Weekdays.Contains(time.Sunday))
	require.True(t, Weekend.Contains(time.Sunday))
}

func TestAutomationDefaults(t *testing.T) {
	// Off carries no surprises.
	off := Automation{}
	off.applyDefaults()
	require.Zero(t, off.Recompute)

	a := Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180}}
	a.applyDefaults()
	require.Equal(t, 5*time.Minute, a.Recompute)
	require.Equal(t, 10*time.Minute, a.MinMoveInterval)
	require.Equal(t, 2*time.Hour, a.OverrideTimeout)
	require.Equal(t, 5, a.Deadband)
	require.Equal(t, 5, a.Step)
	require.Equal(t, 8, a.MaxMovesPerHour)
	require.InDelta(t, 5, a.MinAltitudeDeg, 1e-9)
	require.Equal(t, TriYes, a.OccupancyUnknown, "unknown occupancy assumed occupied")
	require.InDelta(t, 0.5, a.Window.Sensitivity, 1e-9, "sensitivity knob defaulted when no geometry")

	// Precise geometry suppresses the sensitivity default.
	g := Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180, HeightM: 1.4}}
	g.applyDefaults()
	require.Zero(t, g.Window.Sensitivity)
}

func TestAutomationValidate(t *testing.T) {
	valid := Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180}}
	valid.applyDefaults()
	require.NoError(t, valid.validate())

	// Missing/oob azimuth.
	bad := Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 400}}
	bad.applyDefaults()
	require.Error(t, bad.validate())

	// when_unoccupied must be off/thermal/sun_shade.
	wu := Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180}, WhenUnoccupied: ModeSunGlare}
	wu.applyDefaults()
	require.Error(t, wu.validate())

	// Schedule mode needs at least one entry.
	sch := Automation{Mode: ModeSchedule}
	sch.applyDefaults()
	require.Error(t, sch.validate())

	// A valid schedule entry passes.
	sch = Automation{Mode: ModeSchedule, Schedule: []ScheduleEntry{{Days: Weekdays, CloseAt: 22 * 60, OpenAt: 7 * 60, Ramp: 20 * time.Minute}}}
	sch.applyDefaults()
	require.NoError(t, sch.validate())

	// Out-of-range position.
	sch.Schedule[0].SleepHA = 150
	require.Error(t, sch.validate())
}

func TestLocationValidate(t *testing.T) {
	require.NoError(t, (&Location{Lat: 59.9, Lon: 10.7}).validate())
	require.Error(t, (&Location{Lat: 91, Lon: 0}).validate())
	require.Error(t, (&Location{Lat: 0, Lon: 181}).validate())
}

func TestConfigAutomationLocationRequirement(t *testing.T) {
	base := func(a Automation) *Config {
		return &Config{
			MQTT:   MQTTConfig{Broker: "tcp://b:1883"},
			Blinds: []BlindConfig{{Name: "LR", MAC: "AA:BB:CC:DD:EE:01", Automation: a}},
		}
	}
	oslo, err := time.LoadLocation("Europe/Oslo")
	require.NoError(t, err)

	// Sun mode without a location is rejected.
	c := base(Automation{Mode: ModeSunGlare, Window: Window{AzimuthDeg: 180}})
	c.applyDefaults()
	require.Error(t, c.validate(), "sun mode needs location")

	// With lat/lon it validates.
	c.Location = &Location{Lat: 59.9, Lon: 10.7}
	require.NoError(t, c.validate())

	// Schedule mode needs a timezone, not just lat/lon.
	c = base(Automation{Mode: ModeSchedule, Schedule: []ScheduleEntry{{OpenAt: 7 * 60, CloseAt: 22 * 60}}})
	c.applyDefaults()
	c.Location = &Location{Lat: 59.9, Lon: 10.7}
	require.Error(t, c.validate(), "schedule needs timezone")
	c.Location.Zone = oslo
	require.NoError(t, c.validate())
}
