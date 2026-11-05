package document

import "testing"

func TestExpirationBoundariesAndStoredValues(t *testing.T) {
	for _, value := range []int64{0, 1, MaxExpiresAt} {
		var doc Document
		if err := doc.SetExpiresAt(value); err != nil || doc.ExpiresAt() != value {
			t.Fatalf("valid deadline rejected: %d %v", value, err)
		}
	}
	for _, raw := range []string{"-1", "1.5", "NaN", "253402300800000", "broken"} {
		if _, err := ParseExpiresAt(raw); err == nil {
			t.Fatalf("corrupt stored deadline accepted: %q", raw)
		}
	}
	if Expired(0, 100) || Expired(101, 100) || !Expired(100, 100) || !Expired(99, 100) {
		t.Fatal("expiry boundary is not inclusive or persistent sources expire")
	}
}
