package blissha

import (
	"math"
	"time"

	"github.com/viggfred/blissble/pkg/solar"
)

// Signals is the external world seen by the decision engine. Every field is
// optional; the zero value means "unknown", and Decide degrades gracefully.
// Signals are supplied programmatically via the Bridge embed API.
type Signals struct {
	RoomOccupied Tristate
	HomeAway     Tristate // TriYes = nobody home → presence simulation
	Lux          float64
	LuxKnown     bool // false → assume clear-sky (worst case for glare)
	OutdoorTempC float64
	TempKnown    bool // false → season from month + hemisphere
}

// Position is the cover's last-known state in Home Assistant convention
// (0=closed, 100=open). Known is false until the first status is seen, which in
// command-only mode may be a while.
type Position struct {
	HA    int
	Known bool
}

// ControllerState is the per-blind automation state. It is owned solely by the
// manager goroutine and threaded through Decide (in and out) so Decide stays a
// pure function.
type ControllerState struct {
	OverrideUntil time.Time // automation suspended until here (manual/external move)
	LastTarget    int       // last HA target automation actuated; 0 with Known=false ⇒ none yet
	HasLastTarget bool
	LastMoveAt    time.Time
	MovesThisHour int
	HourStart     time.Time
	Latched       bool // hysteresis latch for the sun-on-window gate
}

// DecisionInput is the complete, self-contained input to Decide. Nothing here
// requires Bluetooth: the sun is local math and Current is the cached position.
type DecisionInput struct {
	Cfg     Automation
	Loc     Location
	Now     time.Time
	Current Position
	Signals Signals
	State   ControllerState
}

// Decision is the result of one evaluation. When Move is true the caller should
// actuate TargetHA; otherwise TargetHA is the "would-be" target (for
// diagnostics). NextEvalIn hints when to re-evaluate (0 = default cadence).
type Decision struct {
	Move       bool
	TargetHA   int
	Reason     string
	NextEvalIn time.Duration
	State      ControllerState
}

// Decide computes the desired position for a blind. It is pure: same input →
// same output, no I/O, no globals, no clock reads (Now is passed in).
func Decide(in DecisionInput) Decision {
	st := in.State
	def := in.Cfg.Recompute
	if def <= 0 {
		def = 5 * time.Minute
	}

	if in.Cfg.Mode == ModeOff {
		return hold(st, in.Current.HA, "off", def)
	}
	// Manual/external override suspends automation until it expires.
	if !st.OverrideUntil.IsZero() && in.Now.Before(st.OverrideUntil) {
		return hold(st, in.Current.HA, "override", st.OverrideUntil.Sub(in.Now)+time.Second)
	}

	res := policy(in, &st)
	if !res.opinion {
		return hold(st, in.Current.HA, res.reason, orDefault(res.next, def))
	}
	return applyGuards(in, st, res, def)
}

// policyResult is one policy's opinion before the battery guards run.
type policyResult struct {
	target  int
	reason  string
	next    time.Duration
	opinion bool // false = no move wanted (hold)
}

// policy resolves exactly one active policy per the precedence ladder and returns
// its raw target. It may update st.Latched (the sun-gate hysteresis).
func policy(in DecisionInput, st *ControllerState) policyResult {
	// Presence simulation when the home is empty.
	if in.Cfg.PresenceSim.Enabled && in.Signals.HomeAway == TriYes {
		return policyResult{target: presenceTarget(in), reason: "presence_sim", opinion: true}
	}
	// The sun is shared by after-dark privacy and every sun-based mode; compute it
	// once here rather than recomputing it in each helper.
	alt, az := solar.Position(in.Loc.Lat, in.Loc.Lon, in.Now)
	beta := normalizeRelAz(az, in.Cfg.Window.AzimuthDeg)

	// After-dark privacy can win over the day modes at night.
	if in.Cfg.Privacy.AfterDark && alt <= 0 &&
		(!in.Cfg.Privacy.OccupiedOnly || resolveOccupied(in) == TriYes) {
		return policyResult{target: in.Cfg.Privacy.CloseHA, reason: "privacy_dark", opinion: true}
	}
	// Primary mode, gated by occupancy.
	mode := in.Cfg.Mode
	reason := mode.String()
	if in.Cfg.RequireOccupancy && resolveOccupied(in) == TriNo {
		mode = in.Cfg.WhenUnoccupied
		reason = "unoccupied_" + mode.String()
	}
	switch mode {
	case ModeSunGlare:
		if !sunOnWindow(in, st, alt, beta) {
			return policyResult{target: 100, reason: reason, opinion: true}
		}
		return policyResult{target: glareTargetHA(in, alt, beta), reason: reason, opinion: true}
	case ModeSunShade:
		if !sunOnWindow(in, st, alt, beta) {
			return policyResult{target: 100, reason: reason, opinion: true}
		}
		return policyResult{target: in.Cfg.ShadeClosedHA, reason: reason, opinion: true}
	case ModeThermal:
		return policyResult{target: thermalTarget(in, st, alt, beta), reason: reason, opinion: true}
	case ModeSchedule:
		target, r, next := scheduleTarget(in)
		return policyResult{target: target, reason: r, next: next, opinion: true}
	default: // ModeOff (e.g. when_unoccupied: off)
		return policyResult{reason: "gated_unoccupied", opinion: false}
	}
}

func resolveOccupied(in DecisionInput) Tristate {
	o := in.Signals.RoomOccupied
	if o == TriUnknown {
		o = in.Cfg.OccupancyUnknown
	}
	if o == TriUnknown {
		o = TriYes
	}
	return o
}

// sunOnWindow reports whether direct sun currently strikes the window, with
// hysteresis (via st.Latched) on both the altitude and bearing gates and on an
// optional lux gate, so dawn/dusk and grazing-sun jitter can't flap the output.
func sunOnWindow(in DecisionInput, st *ControllerState, alt, beta float64) bool {
	minAlt := in.Cfg.MinAltitudeDeg
	geomEngage := alt >= minAlt && math.Abs(beta) <= 85
	geomHold := alt >= minAlt-2 && math.Abs(beta) <= 90

	luxEngage, luxHold := true, true
	if in.Cfg.Lux.enabled() && in.Signals.LuxKnown {
		luxEngage = in.Signals.Lux >= in.Cfg.Lux.EngageAbove
		luxHold = in.Signals.Lux >= in.Cfg.Lux.DisengageBelow
	}

	var on bool
	if st.Latched {
		on = geomHold && luxHold
	} else {
		on = geomEngage && luxEngage
	}
	st.Latched = on
	return on
}

// glareTargetHA computes the proportional open position that keeps direct sun off
// the protected zone (profile-angle cut-off). Caller has confirmed illumination.
func glareTargetHA(in DecisionInput, alt, beta float64) int {
	rho := solar.ProfileAngle(alt, beta)
	return int(math.Round(targetOpenFraction(in.Cfg.Window, rho) * 100))
}

// targetOpenFraction returns the fraction the shade should be open (0..1) to keep
// the protected point shaded at the given profile angle. See the plan for the
// derivation: shade bottom at sill+f·height; protecting (depth d, height t)
// requires sill+f·height ≤ t + d·tan ρ.
func targetOpenFraction(w Window, rhoDeg float64) float64 {
	height, sill, depth, protHeight := w.HeightM, w.SillM, w.ProtectedDepthM, w.ProtectedHeightM
	if height <= 0 { // simple knob: assume a typical window, sensitivity scales depth
		height, sill, protHeight = 1.4, 0.9, 0
		depth = 0.5 + w.Sensitivity*3.0 // 0..1 → 0.5..3.5 m protected depth
	}
	if depth <= 0 {
		depth = 1.5
	}
	f := (depth*math.Tan(radians(rhoDeg)) + protHeight - sill) / height
	if w.BottomUp { // aperture at the top: open fraction is complementary
		f = 1 - f
	}
	return clampF(f, 0, 1)
}

func thermalTarget(in DecisionInput, st *ControllerState, alt, beta float64) int {
	hot, cold := season(in)
	switch {
	case hot && sunOnWindow(in, st, alt, beta):
		return in.Cfg.Thermal.SummerCloseHA // block heat gain
	case cold:
		return in.Cfg.Thermal.WinterOpenHA // allow passive gain
	default:
		return 100 // no heat to manage → open for light/view
	}
}

// season classifies hot/cold from an outdoor temperature if known, else from the
// month and hemisphere.
func season(in DecisionInput) (hot, cold bool) {
	if in.Signals.TempKnown {
		return in.Signals.OutdoorTempC >= in.Cfg.Thermal.HotTempC,
			in.Signals.OutdoorTempC <= in.Cfg.Thermal.ColdTempC
	}
	m := in.Now.Month()
	northSummer := m >= time.April && m <= time.September
	if in.Loc.Lat < 0 { // southern hemisphere: seasons flipped
		northSummer = !northSummer
	}
	return northSummer, !northSummer
}

func presenceTarget(in DecisionInput) int {
	mins := localMinutes(in.Now, in.Loc.Zone)
	if mins >= int(in.Cfg.PresenceSim.MorningOpen) && mins < int(in.Cfg.PresenceSim.EveningClose) {
		return in.Cfg.PresenceSim.OpenHA
	}
	return in.Cfg.PresenceSim.CloseHA
}

// scheduleTarget evaluates the bedroom-style clock schedule (with an optional
// gradual wake ramp and solar clamps) for the current local time.
func scheduleTarget(in DecisionInput) (target int, reason string, next time.Duration) {
	zone := in.Loc.Zone
	if zone == nil {
		zone = time.UTC
	}
	now := in.Now.In(zone)
	wd := now.Weekday()
	mins := now.Hour()*60 + now.Minute()

	var e *ScheduleEntry
	for i := range in.Cfg.Schedule {
		if in.Cfg.Schedule[i].Days.Contains(wd) {
			e = &in.Cfg.Schedule[i]
			break
		}
	}
	if e == nil {
		return 100, "schedule_idle", 0 // no rule today → open
	}

	openMin, closeMin := int(e.OpenAt), int(e.CloseAt)
	if e.NotBeforeSunrise || e.NotAfterSunset {
		sunrise, sunset, _, kind := solar.DayEvents(in.Loc.Lat, in.Loc.Lon, now)
		if kind == solar.KindNormal {
			if e.NotBeforeSunrise {
				openMin = max(openMin, localMinutes(sunrise, zone))
			}
			if e.NotAfterSunset {
				// Clamp close to sunset, measured forward from open so it stays
				// correct when the waking window wraps past midnight.
				sunsetMin := localMinutes(sunset, zone)
				if minutesAfter(sunsetMin, openMin) < minutesAfter(closeMin, openMin) {
					closeMin = sunsetMin
				}
			}
		}
	}

	// sinceOpen is minutes since the open time, wrapping past midnight, so a
	// late-night wake ramp isn't truncated at 00:00.
	rampMin := int(e.Ramp / time.Minute)
	sinceOpen := minutesAfter(mins, openMin)
	if rampMin > 0 && sinceOpen < rampMin {
		frac := float64(sinceOpen) / float64(rampMin)
		target = e.SleepHA + int(math.Round(frac*float64(e.OpenHA-e.SleepHA)))
		// Step through the ramp in a handful of moves rather than continuously.
		return target, "wake_ramp", clampDur(e.Ramp/8, 30*time.Second, in.Cfg.Recompute)
	}
	if awake(mins, openMin, closeMin) {
		return e.OpenHA, "schedule_awake", 0
	}
	return e.SleepHA, "schedule_asleep", 0
}

// minutesAfter returns how many minutes t is after ref within a 24h day,
// wrapping past midnight (result in [0,1439]).
func minutesAfter(t, ref int) int { return ((t-ref)%1440 + 1440) % 1440 }

// awake reports whether now is within the [open, close) waking window, handling
// windows that wrap past midnight.
func awake(mins, openMin, closeMin int) bool {
	if openMin <= closeMin {
		return mins >= openMin && mins < closeMin
	}
	return mins >= openMin || mins < closeMin
}

// applyGuards turns a raw target into a Move/hold decision: deadband, quantize to
// Step, min-move-interval, and the hourly rate cap. It also enforces the
// unknown-position rule for proportional glare.
func applyGuards(in DecisionInput, st ControllerState, res policyResult, def time.Duration) Decision {
	next := orDefault(res.next, def)
	target := clampInt(quantize(res.target, in.Cfg.Step), 0, 100)

	if !in.Current.Known {
		// Proportional glare must not make a blind leap before we know where it is.
		if in.Cfg.Mode == ModeSunGlare {
			return hold(st, target, "await_position", def)
		}
		// Discrete/schedule targets are definite; the move also establishes state.
	} else if absInt(target-in.Current.HA) < in.Cfg.Deadband || target == in.Current.HA {
		return hold(st, target, "deadband", next)
	}

	if !st.LastMoveAt.IsZero() && in.Now.Sub(st.LastMoveAt) < in.Cfg.MinMoveInterval {
		remaining := in.Cfg.MinMoveInterval - in.Now.Sub(st.LastMoveAt)
		return hold(st, target, "min_interval", min(remaining+time.Second, next))
	}

	// Hourly rate cap (belt-and-suspenders against churn).
	if st.HourStart.IsZero() || in.Now.Sub(st.HourStart) >= time.Hour {
		st.HourStart = in.Now
		st.MovesThisHour = 0
	}
	if in.Cfg.MaxMovesPerHour > 0 && st.MovesThisHour >= in.Cfg.MaxMovesPerHour {
		// Re-evaluate when the hourly window resets (not OverrideTimeout later),
		// so the blind isn't left wrong for hours after the cap frees up.
		untilReset := time.Hour - in.Now.Sub(st.HourStart) + time.Second
		return hold(st, target, "rate_capped", untilReset)
	}

	st.LastTarget = target
	st.HasLastTarget = true
	st.LastMoveAt = in.Now
	st.MovesThisHour++
	return Decision{Move: true, TargetHA: target, Reason: res.reason, NextEvalIn: next, State: st}
}

func hold(st ControllerState, target int, reason string, next time.Duration) Decision {
	if next <= 0 {
		next = 5 * time.Minute
	}
	return Decision{Move: false, TargetHA: target, Reason: reason, NextEvalIn: next, State: st}
}

// --- small pure helpers ---

func normalizeRelAz(az, windowAz float64) float64 {
	return math.Mod(az-windowAz+540, 360) - 180
}

func quantize(v, step int) int {
	if step <= 1 {
		return v
	}
	return int(math.Round(float64(v)/float64(step))) * step
}

func localMinutes(t time.Time, zone *time.Location) int {
	if zone != nil {
		t = t.In(zone)
	}
	return t.Hour()*60 + t.Minute()
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if hi > 0 && d > hi {
		return hi
	}
	return d
}

func radians(deg float64) float64 { return deg * math.Pi / 180 }

func clampF(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
