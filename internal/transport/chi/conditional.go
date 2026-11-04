package chi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

// expectedRevision accepts the returned strong ETag and the original numeric
// revision form. Weak validators and lists are outside this revision API.
func expectedRevision(header *string) (*int, error) {
	if header == nil {
		return nil, nil //nolint:nilnil // No header intentionally means an unconditional write.
	}
	value := strings.TrimSpace(*header)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	revision, err := strconv.Atoi(value)
	if err != nil || revision <= 0 || strconv.Itoa(revision) != value {
		return nil, fmt.Errorf("invalid If-Match revision: %w", domain.ErrInvalidSchema)
	}
	return &revision, nil
}
