// Package solar computes the sun's position (altitude and azimuth) and the
// sunrise/sunset/solar-noon events for a location and time, using the NOAA solar
// position algorithm. It is pure and depends only on the standard library, so it
// is deterministic and easily unit-tested — mirroring the pure wire-format ethos
// of the bliss protocol package.
//
// All angles are in degrees. Azimuth is measured clockwise from true north
// (0=N, 90=E, 180=S, 270=W). Altitude is the geometric elevation above the
// horizon (no atmospheric-refraction correction); sunrise/sunset use the
// conventional 90.833° zenith (refraction + solar radius).
package solar

import (
	"math"
	"time"
)

// DayKind classifies a day at a given latitude: a normal day with a sunrise and
// sunset, or a polar day/night where the sun never sets or never rises.
type DayKind int

// Day classifications returned by DayEvents.
const (
	KindNormal DayKind = iota
	KindPolarDay
	KindPolarNight
)

// String returns a short label for the day kind.
func (k DayKind) String() string {
	switch k {
	case KindPolarDay:
		return "polar-day"
	case KindPolarNight:
		return "polar-night"
	default:
		return "normal"
	}
}

// Position returns the sun's altitude (degrees above the horizon; negative when
// below) and azimuth (degrees clockwise from true north) for the given latitude,
// longitude (positive east), and instant. It uses t.UTC() internally, so the
// result does not depend on t's location.
func Position(latDeg, lonDeg float64, t time.Time) (altitudeDeg, azimuthDeg float64) {
	tu := t.UTC()
	jd := julianDate(tu)
	decl, eqTime := sunParams(jd)

	h, m, s := tu.Clock()
	minutesUTC := float64(h)*60 + float64(m) + float64(s)/60 + float64(tu.Nanosecond())/1e9/60
	trueSolarTime := math.Mod(minutesUTC+eqTime+4*lonDeg, 1440)
	if trueSolarTime < 0 {
		trueSolarTime += 1440
	}
	hourAngle := trueSolarTime/4 - 180 // degrees, in [-180, 180)

	latR := rad(latDeg)
	declR := rad(decl)
	cosZenith := clamp(math.Sin(latR)*math.Sin(declR)+math.Cos(latR)*math.Cos(declR)*math.Cos(rad(hourAngle)), -1, 1)
	zenith := deg(math.Acos(cosZenith))
	altitudeDeg = 90 - zenith

	azDenom := math.Cos(latR) * math.Sin(rad(zenith))
	var az float64
	if math.Abs(azDenom) > 0.001 {
		azRad := clamp((math.Sin(latR)*math.Cos(rad(zenith))-math.Sin(declR))/azDenom, -1, 1)
		az = 180 - deg(math.Acos(azRad))
		if hourAngle > 0 {
			az = -az
		}
	} else { // sun overhead: azimuth is ill-defined, pick the meridian
		if latDeg > 0 {
			az = 180
		}
	}
	azimuthDeg = mod360(az)
	return altitudeDeg, azimuthDeg
}

// ProfileAngle projects the sun's altitude onto the vertical plane perpendicular
// to a window (the "vertical shadow angle"): atan(tan(altitude)/cos(relAz)),
// where relAz is the sun's azimuth relative to the window normal. The caller must
// gate on illumination first (altitude above the horizon and |relAz| well under
// 90°); as |relAz| approaches 90° the projection diverges.
func ProfileAngle(altitudeDeg, relAzimuthDeg float64) float64 {
	return deg(math.Atan(math.Tan(rad(altitudeDeg)) / math.Cos(rad(relAzimuthDeg))))
}

// DayEvents returns the sunrise, sunset and solar-noon instants (in UTC) for the
// calendar date of `date` (interpreted in UTC) at the given location, plus a
// DayKind. For KindPolarDay/KindPolarNight there is no sunrise/sunset, so those
// are the zero Time; solar noon is always returned.
func DayEvents(latDeg, lonDeg float64, date time.Time) (sunrise, sunset, noon time.Time, kind DayKind) {
	du := date.UTC()
	y, mo, d := du.Date()
	// Evaluate declination / equation-of-time at local solar noon (approximated by
	// 12:00 UTC on the date); accurate to well under a minute for our purposes.
	jd := julianDate(time.Date(y, mo, d, 12, 0, 0, 0, time.UTC))
	decl, eqTime := sunParams(jd)

	solarNoonMin := 720 - 4*lonDeg - eqTime // minutes past UTC midnight
	noon = minutesUTCToTime(y, mo, d, solarNoonMin)

	latR := rad(latDeg)
	declR := rad(decl)
	cosH := math.Cos(rad(90.833))/(math.Cos(latR)*math.Cos(declR)) - math.Tan(latR)*math.Tan(declR)
	switch {
	case cosH > 1:
		return time.Time{}, time.Time{}, noon, KindPolarNight
	case cosH < -1:
		return time.Time{}, time.Time{}, noon, KindPolarDay
	}
	haSunrise := deg(math.Acos(cosH)) // degrees of hour angle from noon to sunrise
	sunrise = minutesUTCToTime(y, mo, d, solarNoonMin-haSunrise*4)
	sunset = minutesUTCToTime(y, mo, d, solarNoonMin+haSunrise*4)
	return sunrise, sunset, noon, KindNormal
}

// sunParams returns the sun's declination (degrees) and the equation of time
// (minutes) for a Julian Date, per the NOAA algorithm.
func sunParams(jd float64) (declDeg, eqTimeMin float64) {
	t := (jd - 2451545.0) / 36525.0 // Julian centuries since J2000.0
	l0 := mod360(280.46646 + t*(36000.76983+t*0.0003032))
	m := 357.52911 + t*(35999.05029-0.0001537*t)
	e := 0.016708634 - t*(0.000042037+0.0000001267*t)

	mr := rad(m)
	center := math.Sin(mr)*(1.914602-t*(0.004817+0.000014*t)) +
		math.Sin(2*mr)*(0.019993-0.000101*t) +
		math.Sin(3*mr)*0.000289
	trueLong := l0 + center
	appLong := trueLong - 0.00569 - 0.00478*math.Sin(rad(125.04-1934.136*t))

	obliq0 := 23 + (26+(21.448-t*(46.815+t*(0.00059-t*0.001813)))/60)/60
	obliq := obliq0 + 0.00256*math.Cos(rad(125.04-1934.136*t))

	declDeg = deg(math.Asin(math.Sin(rad(obliq)) * math.Sin(rad(appLong))))

	y := math.Tan(rad(obliq / 2))
	y *= y
	l0r := rad(l0)
	eqTimeMin = 4 * deg(y*math.Sin(2*l0r)-2*e*math.Sin(mr)+
		4*e*y*math.Sin(mr)*math.Cos(2*l0r)-
		0.5*y*y*math.Sin(4*l0r)-
		1.25*e*e*math.Sin(2*mr))
	return declDeg, eqTimeMin
}

// julianDate returns the Julian Date for a UTC instant.
func julianDate(t time.Time) float64 {
	return float64(t.UnixNano())/1e9/86400.0 + 2440587.5
}

// minutesUTCToTime builds a UTC time from a calendar date and a count of minutes
// past that date's UTC midnight (may be negative or exceed a day, which correctly
// shifts into the neighbouring date).
func minutesUTCToTime(y int, mo time.Month, d int, minutes float64) time.Time {
	midnight := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
	return midnight.Add(time.Duration(minutes * float64(time.Minute)))
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }
func deg(rad float64) float64 { return rad * 180 / math.Pi }

func mod360(x float64) float64 {
	x = math.Mod(x, 360)
	if x < 0 {
		x += 360
	}
	return x
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
