package geo

import "math"

// Converter transforms GPS coordinates to a local East-North-Up (ENU) frame.
type Converter struct {
	originLat float64
	originLon float64
	originAlt float64
	set       bool
}

// NewConverter creates a Converter with the given origin.
func NewConverter(lat, lon, alt float64) *Converter {
	return &Converter{originLat: lat, originLon: lon, originAlt: alt, set: true}
}

// NewAutoConverter creates a Converter whose origin will be set by the first
// call to SetOrigin.
func NewAutoConverter() *Converter {
	return &Converter{}
}

// SetOrigin establishes the ENU origin. Safe to call multiple times; only the
// first call takes effect when using "auto" origin mode.
func (c *Converter) SetOrigin(lat, lon, alt float64) bool {
	if c.set {
		return false
	}
	c.originLat = lat
	c.originLon = lon
	c.originAlt = alt
	c.set = true
	return true
}

// IsSet reports whether an origin has been established.
func (c *Converter) IsSet() bool { return c.set }

// ResetOrigin clears the established origin so that the next SetOrigin call
// will take effect again. Intended for demo/testing use.
func (c *Converter) ResetOrigin() { c.set = false }

// Origin returns the ENU origin in GPS coordinates.
func (c *Converter) Origin() (lat, lon, alt float64) {
	return c.originLat, c.originLon, c.originAlt
}

// GPSToENU converts a GPS position (degrees, meters) to local ENU meters.
// Uses a simple flat-earth approximation valid for distances < ~10 km.
func (c *Converter) GPSToENU(lat, lon, alt float64) (east, north, up float64) {
	dLat := lat - c.originLat
	dLon := lon - c.originLon
	dAlt := alt - c.originAlt

	// Meters per degree
	metersPerDegLat := 111320.0
	metersPerDegLon := 111320.0 * math.Cos(c.originLat*math.Pi/180.0)

	north = dLat * metersPerDegLat
	east = dLon * metersPerDegLon
	up = dAlt
	return
}

// ENUToGPS is the inverse of GPSToENU.
func (c *Converter) ENUToGPS(east, north, up float64) (lat, lon, alt float64) {
	metersPerDegLat := 111320.0
	metersPerDegLon := 111320.0 * math.Cos(c.originLat*math.Pi/180.0)

	lat = c.originLat + north/metersPerDegLat
	lon = c.originLon + east/metersPerDegLon
	alt = c.originAlt + up
	return
}
