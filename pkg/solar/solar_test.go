package solar

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Reference instants (noon UTC) near the 2024 solstices/equinoxes.
var (
	june21 = time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
	dec21  = time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC)
	mar20  = time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC)
)

// Declination hits ±23.44° at the solstices and ~0 at the equinoxes.
func TestDeclination(t *testing.T) {
	d, _ := sunParams(julianDate(june21))
	require.InDelta(t, 23.44, d, 0.2, "June solstice declination")
	d, _ = sunParams(julianDate(dec21))
	require.InDelta(t, -23.44, d, 0.2, "December solstice declination")
	d, _ = sunParams(julianDate(mar20))
	require.InDelta(t, 0, d, 0.4, "March equinox declination")
}

// The equation of time has well-known extrema (~-14.2 min ≈ Feb 11,
// ~+16.4 min ≈ Nov 3).
func TestEquationOfTime(t *testing.T) {
	_, eq := sunParams(julianDate(time.Date(2024, 2, 11, 12, 0, 0, 0, time.UTC)))
	require.InDelta(t, -14.2, eq, 1.0, "February equation-of-time minimum")
	_, eq = sunParams(julianDate(time.Date(2024, 11, 3, 12, 0, 0, 0, time.UTC)))
	require.InDelta(t, 16.4, eq, 1.0, "November equation-of-time maximum")
}

// At the subsolar point (latitude = declination, longitude where it is solar
// noon) the sun is exactly overhead: altitude = 90°. This is an exact invariant
// of the formulas and catches gross declination/hour-angle/azimuth bugs.
func TestSubsolarPointOverhead(t *testing.T) {
	for _, tm := range []time.Time{
		june21, dec21, mar20,
		time.Date(2024, 1, 15, 3, 30, 0, 0, time.UTC),
		time.Date(2024, 8, 2, 18, 45, 0, 0, time.UTC),
	} {
		decl, eqTime := sunParams(julianDate(tm))
		h, m, s := tm.Clock()
		minUTC := float64(h)*60 + float64(m) + float64(s)/60
		lon := (720 - eqTime - minUTC) / 4 // longitude where trueSolarTime == noon
		for lon > 180 {
			lon -= 360
		}
		for lon < -180 {
			lon += 360
		}
		alt, _ := Position(decl, lon, tm)
		require.InDelta(t, 90, alt, 0.02, "subsolar altitude at %s", tm)
	}
}

// At solar noon the sun is on the meridian: due south for a northern mid-latitude
// (azimuth 180) and at the expected altitude 90-|lat-decl|.
func TestSolarNoonGeometry(t *testing.T) {
	lat, lon := 40.0, -105.0
	_, _, noon, kind := DayEvents(lat, lon, mar20)
	require.Equal(t, KindNormal, kind)

	alt, az := Position(lat, lon, noon)
	decl, _ := sunParams(julianDate(noon))
	require.InDelta(t, 90-math.Abs(lat-decl), alt, 0.5, "noon altitude")
	require.InDelta(t, 180, az, 2.0, "noon azimuth (due south)")
}

// In the southern hemisphere the midday sun is to the north: azimuth near 0/360.
func TestSouthernHemisphereNoon(t *testing.T) {
	lat, lon := -33.87, 151.21 // Sydney
	_, _, noon, _ := DayEvents(lat, lon, june21)
	_, az := Position(lat, lon, noon)
	require.True(t, az < 5 || az > 355, "S-hemi noon azimuth should be ~north, got %v", az)
}

// Through the day (N hemisphere) azimuth increases monotonically west, passing
// 180 at noon; morning sun is east of south, afternoon west of south.
func TestAzimuthProgression(t *testing.T) {
	lat, lon := 40.0, 0.0
	sunrise, sunset, noon, kind := DayEvents(lat, lon, june21)
	require.Equal(t, KindNormal, kind)

	prev := -1.0
	for f := 0.1; f <= 0.9; f += 0.1 {
		tm := sunrise.Add(time.Duration(float64(sunset.Sub(sunrise)) * f))
		_, az := Position(lat, lon, tm)
		require.Greater(t, az, prev, "azimuth should increase through the day")
		prev = az
	}

	_, azAM := Position(lat, lon, noon.Add(-3*time.Hour))
	_, azPM := Position(lat, lon, noon.Add(3*time.Hour))
	require.Less(t, azAM, 180.0, "morning sun east of south")
	require.Greater(t, azPM, 180.0, "afternoon sun west of south")

	// Altitude is symmetric about solar noon.
	altAM, _ := Position(lat, lon, noon.Add(-2*time.Hour))
	altPM, _ := Position(lat, lon, noon.Add(2*time.Hour))
	require.InDelta(t, altAM, altPM, 0.5, "altitude symmetric about noon")
}

// Sunrise bearing swings with the season: north of east in summer (az<90),
// south of east in winter (az>90).
func TestSunriseBearingBySeason(t *testing.T) {
	lat, lon := 40.0, 0.0
	sr, _, _, _ := DayEvents(lat, lon, june21)
	_, azSummer := Position(lat, lon, sr)
	require.Less(t, azSummer, 90.0, "summer sunrise north of east")

	srW, _, _, _ := DayEvents(lat, lon, dec21)
	_, azWinter := Position(lat, lon, srW)
	require.Greater(t, azWinter, 90.0, "winter sunrise south of east")
}

// High latitudes have polar day/night at the solstices; mid-latitudes are normal.
func TestPolarDayNight(t *testing.T) {
	_, _, _, k := DayEvents(78, 15, june21) // Svalbard, midnight sun
	require.Equal(t, KindPolarDay, k)
	_, _, _, k = DayEvents(78, 15, dec21) // polar night
	require.Equal(t, KindPolarNight, k)
	_, _, _, k = DayEvents(40, 0, june21)
	require.Equal(t, KindNormal, k)

	require.Equal(t, "polar-day", KindPolarDay.String())
	require.Equal(t, "polar-night", KindPolarNight.String())
	require.Equal(t, "normal", KindNormal.String())
}

// Sunrise precedes solar noon precedes sunset, and noon is when altitude peaks.
func TestDayEventsOrdering(t *testing.T) {
	lat, lon := 59.91, 10.75 // Oslo
	sunrise, sunset, noon, kind := DayEvents(lat, lon, june21)
	require.Equal(t, KindNormal, kind)
	require.True(t, sunrise.Before(noon) && noon.Before(sunset), "sunrise<noon<sunset")

	altNoon, _ := Position(lat, lon, noon)
	altBefore, _ := Position(lat, lon, noon.Add(-90*time.Minute))
	altAfter, _ := Position(lat, lon, noon.Add(90*time.Minute))
	require.Greater(t, altNoon, altBefore)
	require.Greater(t, altNoon, altAfter)

	// Altitude at sunrise/sunset is ~0 (using the 90.833° convention, ~-0.83°).
	altRise, _ := Position(lat, lon, sunrise)
	require.InDelta(t, -0.833, altRise, 0.5, "altitude at sunrise ~0")
}

func TestProfileAngle(t *testing.T) {
	// On-axis (relAz 0): the profile angle equals the altitude.
	require.InDelta(t, 30, ProfileAngle(30, 0), 1e-9)
	// Off-axis raises the projected angle (steeper apparent sun in profile).
	require.Greater(t, ProfileAngle(30, 60), ProfileAngle(30, 0))
	require.Greater(t, ProfileAngle(30, 80), ProfileAngle(30, 60))
	// Worked value: atan(tan30/cos60) = atan(0.5774/0.5) ≈ 49.1°.
	require.InDelta(t, 49.1, ProfileAngle(30, 60), 0.2)
}
