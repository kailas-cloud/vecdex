package chi

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestDeadlinePatchDistinguishesOmissionClearAndReplacement(t *testing.T) {
	if value, err := expiryPatchFromJSON(nil); err != nil || value != nil {
		t.Fatalf("omitted deadline became an update: %v %v", value, err)
	}
	for _, raw := range []string{"null", " null ", "1234567890"} {
		value, err := expiryPatchFromJSON(json.RawMessage(raw))
		if err != nil || value == nil {
			t.Fatalf("valid deadline patch rejected: %s %v", raw, err)
		}
		if raw != "1234567890" && *value != 0 {
			t.Fatal("null did not clear the deadline")
		}
	}
	for _, raw := range []string{"0", "-1", "1.5", `"1234"`, "253402300800000", "{}"} {
		if _, err := expiryPatchFromJSON(json.RawMessage(raw)); !errors.Is(err, domain.ErrInvalidSchema) {
			t.Fatalf("invalid deadline patch accepted: %s %v", raw, err)
		}
	}
}
