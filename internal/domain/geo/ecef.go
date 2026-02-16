package geo

import "math"

// EarthRadiusMeters is the mean radius of Earth used for Haversine distance.
const EarthRadiusMeters = 6_371_000.0

// GeoVectorDim is the fixed vector dimension for geo collections (ECEF 3D).
const GeoVectorDim = 3

// ToECEF converts latitude/longitude (degrees) to a unit-sphere ECEF vector.
// The result is a 3D unit vector suitable for L2-based KNN search.
func ToECEF(latDeg, lonDeg float64) [3]float32 {
	lat := latDeg * math.Pi / 180
	lon := lonDeg * math.Pi / 180
	x := math.Cos(lat) * math.Cos(lon)
	y := math.Cos(lat) * math.Sin(lon)
	z := math.Sin(lat)
	return [3]float32{float32(x), float32(y), float32(z)}
}

// ToVector converts latitude/longitude (degrees) to a float32 slice for KNN storage.
func ToVector(latDeg, lonDeg float64) []float32 {
	v := ToECEF(latDeg, lonDeg)
	return v[:]
}

// Haversine returns the great-circle distance in meters between two points
// specified by latitude and longitude in degrees.
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return EarthRadiusMeters * c
}

// L2ToHaversineMeters converts L2 distance between two unit-sphere ECEF vectors
// to approximate great-circle distance in meters. Uses the identity:
// L2^2 = 2*(1 - cos(angle)), so angle = 2*arcsin(L2/2).
func L2ToHaversineMeters(l2dist float64) float64 {
	// Clamp to valid range for arcsin (numerical noise can push slightly above 1)
	half := l2dist / 2
	if half > 1 {
		half = 1
	}
	angle := 2 * math.Asin(half)
	return EarthRadiusMeters * angle
}

// ValidateCoordinates checks that latitude is in [-90,90] and longitude in [-180,180].
func ValidateCoordinates(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}
