package blissha

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// AutomationMode selects a blind's automation policy. The zero value (ModeOff)
// disables automation entirely.
type AutomationMode int

// Automation policies.
const (
	ModeOff AutomationMode = iota
	ModeSunGlare
	ModeSunShade
	ModeSchedule
	ModeThermal
)

// String returns the config/vocabulary name of the mode.
func (m AutomationMode) String() string {
	switch m {
	case ModeSunGlare:
		return "sun_glare"
	case ModeSunShade:
		return "sun_shade"
	case ModeSchedule:
		return "schedule"
	case ModeThermal:
		return "thermal"
	default:
		return "off"
	}
}

// ParseAutomationMode maps a config string to a mode (single source of truth for
// the vocabulary, shared by the YAML loader).
func ParseAutomationMode(s string) (AutomationMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off":
		return ModeOff, nil
	case "sun_glare":
		return ModeSunGlare, nil
	case "sun_shade":
		return ModeSunShade, nil
	case "schedule":
		return ModeSchedule, nil
	case "thermal":
		return ModeThermal, nil
	default:
		return ModeOff, fmt.Errorf("unknown automation mode %q", s)
	}
}

func (m AutomationMode) usesSun() bool {
	return m == ModeSunGlare || m == ModeSunShade || m == ModeThermal
}

// Tristate models an optional boolean signal (occupancy, home-away): unknown, no,
// or yes. The zero value is TriUnknown, so a never-set signal degrades gracefully.
type Tristate int8

// Tristate values.
const (
	TriUnknown Tristate = iota
	TriNo
	TriYes
)

// DaySet is a bitmask of weekdays. Bit i corresponds to time.Weekday i
// (Sunday=0 .. Saturday=6).
type DaySet uint8

// Day bits and common combinations.
const (
	Sun DaySet = 1 << iota
	Mon
	Tue
	Wed
	Thu
	Fri
	Sat

	AllDays  = Sun | Mon | Tue | Wed | Thu | Fri | Sat
	Weekdays = Mon | Tue | Wed | Thu | Fri
	Weekend  = Sat | Sun
)

// Contains reports whether the set includes the given weekday. An empty set is
// treated as "every day".
func (d DaySet) Contains(wd time.Weekday) bool {
	if d == 0 {
		return true
	}
	return d&(1<<uint(wd)) != 0
}

var dayNames = map[string]DaySet{
	"sun": Sun, "mon": Mon, "tue": Tue, "wed": Wed, "thu": Thu, "fri": Fri, "sat": Sat,
	"daily": AllDays, "everyday": AllDays, "all": AllDays,
	"weekdays": Weekdays, "weekday": Weekdays, "weekend": Weekend,
}

// ParseDaySet turns a list of day tokens ("mon","tue",...,"weekdays","weekend",
// "daily") into a DaySet (shared by the YAML loader).
func ParseDaySet(tokens []string) (DaySet, error) {
	var set DaySet
	for _, tok := range tokens {
		d, ok := dayNames[strings.ToLower(strings.TrimSpace(tok))]
		if !ok {
			return 0, fmt.Errorf("unknown day %q", tok)
		}
		set |= d
	}
	return set, nil
}

// Clock is a wall-clock time-of-day as minutes since local midnight (0..1439).
type Clock int

// HourMinute splits the clock into hours and minutes.
func (c Clock) HourMinute() (hour, minute int) { return int(c) / 60, int(c) % 60 }

// Location is the home's geographic position. Sun-based modes need Lat/Lon;
// schedule and presence simulation need Zone (an explicit IANA timezone — the
// config loader resolves it and never falls back to the system default).
type Location struct {
	Lat, Lon float64
	Zone     *time.Location
}

func (l *Location) validate() error {
	if math.IsNaN(l.Lat) || l.Lat < -90 || l.Lat > 90 {
		return fmt.Errorf("latitude %v out of range [-90,90]", l.Lat)
	}
	if math.IsNaN(l.Lon) || l.Lon < -180 || l.Lon > 180 {
		return fmt.Errorf("longitude %v out of range [-180,180]", l.Lon)
	}
	return nil
}

// Window describes the aperture for sun-based modes.
type Window struct {
	// AzimuthDeg is the compass direction the window faces (0=N, 90=E, 180=S,
	// 270=W); required for sun modes.
	AzimuthDeg float64
	// BottomUp flips the shade geometry for bottom-up rollers (aperture at the
	// top rather than the bottom). This is distinct from BlindConfig.Invert,
	// which only flips the reported number.
	BottomUp bool
	// Precise geometry (metres). When HeightM == 0, Sensitivity is used instead.
	HeightM          float64
	SillM            float64
	ProtectedDepthM  float64
	ProtectedHeightM float64
	// Sensitivity (0..1) is the simple no-measurement knob: how aggressively to
	// shade (higher = closes more). Used when HeightM == 0.
	Sensitivity float64
}

// ScheduleEntry is one clock-based rule for ModeSchedule.
type ScheduleEntry struct {
	Days             DaySet // empty = every day
	CloseAt          Clock
	OpenAt           Clock
	SleepHA          int           // position while "asleep" (default 0)
	OpenHA           int           // position when awake/open (default 100)
	Ramp             time.Duration // gradual wake duration from OpenAt (0 = instant)
	NotBeforeSunrise bool          // clamp OpenAt to no earlier than sunrise
	NotAfterSunset   bool          // clamp CloseAt to no later than sunset
}

// Thermal configures ModeThermal (season/temperature-aware heat management).
type Thermal struct {
	SummerCloseHA int
	WinterOpenHA  int
	HotTempC      float64 // block sun above this outdoor temp (when temp known)
	ColdTempC     float64 // allow gain below this (when temp known)
}

// PresenceSim opens/closes on a natural pattern when the home is away, to look
// lived-in.
type PresenceSim struct {
	Enabled      bool
	MorningOpen  Clock
	EveningClose Clock
	OpenHA       int
	CloseHA      int
}

// Privacy closes the blind after dark (sun below the horizon).
type Privacy struct {
	AfterDark    bool
	CloseHA      int
	OccupiedOnly bool // only close after dark when the room is occupied
}

// LuxGate gates shading on actual brightness (from an external lux signal), so
// the blind doesn't shade on overcast days. Zero values disable it.
type LuxGate struct {
	EngageAbove    float64
	DisengageBelow float64
}

func (g LuxGate) enabled() bool { return g.EngageAbove > 0 }

// Automation is the optional per-blind automation policy.
type Automation struct {
	Mode AutomationMode

	Window        Window
	ShadeClosedHA int // ModeSunShade target while the sun is on the window

	Schedule []ScheduleEntry
	Thermal  Thermal

	PresenceSim PresenceSim
	Privacy     Privacy
	Lux         LuxGate

	RequireOccupancy bool
	WhenUnoccupied   AutomationMode // fallback when unoccupied (Off/Thermal/SunShade)
	OccupancyUnknown Tristate       // how to treat unknown occupancy (default TriYes)

	Recompute       time.Duration
	MinMoveInterval time.Duration
	OverrideTimeout time.Duration
	Deadband        int     // HA %
	Step            int     // quantize target to this many %
	MaxMovesPerHour int     // hard safety cap
	MinAltitudeDeg  float64 // ignore sun below this elevation
}

// applyDefaults fills unset fields for an active automation. It is a no-op for
// ModeOff so a disabled blind carries no surprises.
func (a *Automation) applyDefaults() {
	if a.Mode == ModeOff {
		return
	}
	if a.Recompute <= 0 {
		a.Recompute = 5 * time.Minute
	}
	if a.MinMoveInterval <= 0 {
		a.MinMoveInterval = 10 * time.Minute
	}
	if a.OverrideTimeout <= 0 {
		a.OverrideTimeout = 2 * time.Hour
	}
	if a.Deadband <= 0 {
		a.Deadband = 5
	}
	if a.Step <= 0 {
		a.Step = 5
	}
	if a.MaxMovesPerHour <= 0 {
		a.MaxMovesPerHour = 8
	}
	if a.MinAltitudeDeg <= 0 {
		a.MinAltitudeDeg = 5
	}
	if a.OccupancyUnknown == TriUnknown {
		a.OccupancyUnknown = TriYes // assume occupied when no signal is supplied
	}
	if a.Window.HeightM == 0 && a.Window.Sensitivity == 0 {
		a.Window.Sensitivity = 0.5
	}
	// ShadeClosedHA and the Thermal thresholds are intentionally NOT defaulted
	// from 0 here: 0 is a legitimate value (full close / 0 °C), so treating it as
	// "unset" would be a footgun. The YAML loader resolves their defaults from an
	// absent key (a nil pointer); struct-based callers set them explicitly.
	if a.Thermal.WinterOpenHA == 0 {
		a.Thermal.WinterOpenHA = 100 // an "open" position of 0 is nonsensical, so 0 = unset
	}
	if a.PresenceSim.MorningOpen == 0 {
		a.PresenceSim.MorningOpen = 7 * 60
	}
	if a.PresenceSim.EveningClose == 0 {
		a.PresenceSim.EveningClose = 21 * 60
	}
	if a.PresenceSim.OpenHA == 0 {
		a.PresenceSim.OpenHA = 100
	}
	for i := range a.Schedule {
		if a.Schedule[i].OpenHA == 0 {
			a.Schedule[i].OpenHA = 100
		}
	}
}

// needsGeo reports whether the policy needs latitude/longitude (sun geometry).
func (a Automation) needsGeo() bool {
	if a.Mode.usesSun() || a.WhenUnoccupied.usesSun() || a.Privacy.AfterDark {
		return true
	}
	if a.Mode == ModeSchedule {
		for _, e := range a.Schedule {
			if e.NotBeforeSunrise || e.NotAfterSunset {
				return true
			}
		}
	}
	return false
}

// needsZone reports whether the policy needs a wall-clock timezone.
func (a Automation) needsZone() bool {
	return a.Mode == ModeSchedule || a.PresenceSim.Enabled
}

func inHA(p int) bool { return p >= 0 && p <= 100 }

func (a *Automation) validate() error {
	if a.Mode == ModeOff {
		return nil
	}
	if a.Deadband < 0 || a.Deadband > 100 {
		return fmt.Errorf("deadband %d out of range [0,100]", a.Deadband)
	}
	if a.Step < 1 || a.Step > 100 {
		return fmt.Errorf("step %d out of range [1,100]", a.Step)
	}
	if !inHA(a.ShadeClosedHA) {
		return fmt.Errorf("shade_closed %d out of range [0,100]", a.ShadeClosedHA)
	}
	if w := a.WhenUnoccupied; w != ModeOff && w != ModeThermal && w != ModeSunShade {
		return fmt.Errorf("when_unoccupied must be off, thermal or sun_shade (got %v)", w)
	}
	if a.Mode.usesSun() || a.WhenUnoccupied.usesSun() {
		if math.IsNaN(a.Window.AzimuthDeg) || a.Window.AzimuthDeg < 0 || a.Window.AzimuthDeg >= 360 {
			return fmt.Errorf("window.azimuth %v out of range [0,360)", a.Window.AzimuthDeg)
		}
		if a.Window.Sensitivity < 0 || a.Window.Sensitivity > 1 {
			return fmt.Errorf("window.sensitivity %v out of range [0,1]", a.Window.Sensitivity)
		}
		if a.Window.HeightM < 0 || a.Window.SillM < 0 || a.Window.ProtectedDepthM < 0 || a.Window.ProtectedHeightM < 0 {
			return fmt.Errorf("window geometry must be non-negative")
		}
	}
	if a.Mode == ModeSchedule {
		if len(a.Schedule) == 0 {
			return fmt.Errorf("schedule mode requires at least one schedule entry")
		}
		for i, e := range a.Schedule {
			if e.CloseAt < 0 || e.CloseAt > 1439 || e.OpenAt < 0 || e.OpenAt > 1439 {
				return fmt.Errorf("schedule[%d]: times must be within a day", i)
			}
			if !inHA(e.SleepHA) || !inHA(e.OpenHA) {
				return fmt.Errorf("schedule[%d]: positions out of range [0,100]", i)
			}
			if e.Ramp < 0 {
				return fmt.Errorf("schedule[%d]: ramp must be non-negative", i)
			}
		}
	}
	if !inHA(a.Thermal.SummerCloseHA) || !inHA(a.Thermal.WinterOpenHA) {
		return fmt.Errorf("thermal positions out of range [0,100]")
	}
	if a.PresenceSim.Enabled && (!inHA(a.PresenceSim.OpenHA) || !inHA(a.PresenceSim.CloseHA)) {
		return fmt.Errorf("presence_sim positions out of range [0,100]")
	}
	if a.Privacy.AfterDark && !inHA(a.Privacy.CloseHA) {
		return fmt.Errorf("privacy close position out of range [0,100]")
	}
	return nil
}
