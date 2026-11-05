package chi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

func TestExpectedVersionAcceptsReturnedETag(t *testing.T) {
	for _, value := range []string{`"opaque-version_123"`, `  "source-v2"  `} {
		version, err := expectedVersion(&value)
		if err != nil || version == nil || *version == "" {
			t.Fatalf("valid ETag rejected: %q %v", value, err)
		}
	}
	for _, value := range []string{"", "1", `""`, `W/"source"`, `"one", "two"`, `"one`, "*", "\"a\nb\""} {
		if _, err := expectedVersion(&value); !errors.Is(err, domain.ErrInvalidSchema) {
			t.Fatalf("invalid document ETag accepted: %q %v", value, err)
		}
	}
	if version, err := expectedVersion(nil); err != nil || version != nil {
		t.Fatalf("omitted condition became conditional: %v %v", version, err)
	}
}

func TestVersionConflictResponseIncludesCurrentValidator(t *testing.T) {
	for _, version := range []string{"current-source-version", ""} {
		t.Run(version, func(t *testing.T) {
			response := httptest.NewRecorder()
			err := fmt.Errorf("conditional write: %w", domain.NewVersionConflict(version))
			if !versionConflictHandler(response, err, "document version changed") {
				t.Fatal("version conflict was not handled")
			}
			if response.Code != http.StatusConflict {
				t.Fatalf("status=%d", response.Code)
			}
			var body gen.ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != gen.ErrorResponseCodeVersionConflict {
				t.Fatalf("code=%s", body.Code)
			}
			if version == "" {
				if response.Header().Get("ETag") != "" || body.CurrentVersion != nil {
					t.Fatal("missing document received a current validator")
				}
			} else if response.Header().Get("ETag") != strconv.Quote(version) ||
				body.CurrentVersion == nil || *body.CurrentVersion != version {
				t.Fatal("current validator was not returned")
			}
		})
	}
}
