package geo

import (
	"math"
	"testing"
)

func almost(a, b, eps float64) bool {
	if a > b {
		return a-b < eps
	}
	return b-a < eps
}

func TestToECEF_Equator_PrimeMeridian(t *testing.T) {
	v := ToECEF(0, 0)
	if !almost(float64(v[0]), 1, 1e-6) || !almost(float64(v[1]), 0, 1e-6) || !almost(float64(v[2]), 0, 1e-6) {
		t.Fatalf("want (1,0,0) got (%f,%f,%f)", v[0], v[1], v[2])
	}
}

func TestToECEF_Equator_90E(t *testing.T) {
	v := ToECEF(0, 90)
	if !almost(float64(v[0]), 0, 1e-6) || !almost(float64(v[1]), 1, 1e-6) || !almost(float64(v[2]), 0, 1e-6) {
		t.Fatalf("want (0,1,0) got (%f,%f,%f)", v[0], v[1], v[2])
	}
}

func TestToECEF_NorthPole(t *testing.T) {
	v := ToECEF(90, 0)
	if !almost(float64(v[0]), 0, 1e-6) || !almost(float64(v[1]), 0, 1e-6) || !almost(float64(v[2]), 1, 1e-6) {
		t.Fatalf("want (0,0,1) got (%f,%f,%f)", v[0], v[1], v[2])
	}
}

func TestToECEF_SouthPole(t *testing.T) {
	v := ToECEF(-90, 0)
	if !almost(float64(v[0]), 0, 1e-6) || !almost(float64(v[1]), 0, 1e-6) || !almost(float64(v[2]), -1, 1e-6) {
		t.Fatalf("want (0,0,-1) got (%f,%f,%f)", v[0], v[1], v[2])
	}
}

func TestToVector(t *testing.T) {
	v := ToVector(0, 0)
	if len(v) != 3 {
		t.Fatalf("want len 3, got %d", len(v))
	}
	if !almost(float64(v[0]), 1, 1e-6) {
		t.Fatalf("want x=1, got %f", v[0])
	}
}

func TestHaversine_SamePoint(t *testing.T) {
	d := Haversine(40.7128, -74.0060, 40.7128, -74.0060)
	if d != 0 {
		t.Fatalf("want 0, got %f", d)
	}
}

func TestHaversine_NewYork_London(t *testing.T) {
	// NYC to London: ~5,570 km
	d := Haversine(40.7128, -74.0060, 51.5074, -0.1278)
	expected := 5_570_000.0
	if !almost(d, expected, 30_000) { // 30km tolerance (spherical approx)
		t.Fatalf("want ~%.0fm, got %.0fm", expected, d)
	}
}

func TestHaversine_Antipodal(t *testing.T) {
	// Opposite sides of Earth: ~20,015 km (half circumference)
	d := Haversine(0, 0, 0, 180)
	expected := math.Pi * EarthRadiusMeters
	if !almost(d, expected, 1) {
		t.Fatalf("want ~%.0fm, got %.0fm", expected, d)
	}
}

func TestL2ToHaversineMeters_Zero(t *testing.T) {
	d := L2ToHaversineMeters(0)
	if d != 0 {
		t.Fatalf("want 0, got %f", d)
	}
}

func TestL2ToHaversineMeters_Consistency(t *testing.T) {
	// Compute L2 distance between NYC and London ECEF vectors,
	// then convert to meters and compare to Haversine.
	v1 := ToECEF(40.7128, -74.0060)
	v2 := ToECEF(51.5074, -0.1278)

	dx := float64(v1[0] - v2[0])
	dy := float64(v1[1] - v2[1])
	dz := float64(v1[2] - v2[2])
	l2 := math.Sqrt(dx*dx + dy*dy + dz*dz)

	fromL2 := L2ToHaversineMeters(l2)
	direct := Haversine(40.7128, -74.0060, 51.5074, -0.1278)

	// Should match within 1km (float32 rounding in ECEF)
	if !almost(fromL2, direct, 1_000) {
		t.Fatalf("L2-derived %.0fm vs Haversine %.0fm", fromL2, direct)
	}
}

func TestValidateCoordinates(t *testing.T) {
	tests := []struct {
		lat, lon float64
		valid    bool
	}{
		{0, 0, true},
		{90, 180, true},
		{-90, -180, true},
		{91, 0, false},
		{0, 181, false},
		{-91, 0, false},
		{0, -181, false},
	}
	for _, tt := range tests {
		if got := ValidateCoordinates(tt.lat, tt.lon); got != tt.valid {
			t.Errorf("ValidateCoordinates(%f, %f) = %v, want %v", tt.lat, tt.lon, got, tt.valid)
		}
	}
}
