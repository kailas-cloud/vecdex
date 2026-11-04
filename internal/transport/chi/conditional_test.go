package chi

import (
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestExpectedRevisionAcceptsReturnedETag(t *testing.T) {
	for _, value := range []string{"1", `"1"`, `  "42"  `} {
		revision, err := expectedRevision(&value)
		if err != nil || revision == nil || *revision <= 0 {
			t.Fatalf("valid ETag rejected: %q %v", value, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "01", `W/"1"`, `"1", "2"`, `"1`, "922337203685477580899"} {
		if _, err := expectedRevision(&value); !errors.Is(err, domain.ErrInvalidSchema) {
			t.Fatalf("invalid revision accepted: %q %v", value, err)
		}
	}
}
