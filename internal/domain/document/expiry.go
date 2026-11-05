package document

import (
	"fmt"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

// MaxExpiresAt is the last millisecond in year 9999, exactly representable by
// both a JSON number and a Valkey sorted-set score. Zero means no expiration.
const MaxExpiresAt int64 = 253402300799999

// ExpiresAt is the absolute UTC Unix millisecond deadline, or zero for persistent sources.
func (d *Document) ExpiresAt() int64 { return d.expiresAt }

// SetExpiresAt replaces the source deadline. Expiration does not extend on reads.
func (d *Document) SetExpiresAt(value int64) error {
	if value < 0 || value > MaxExpiresAt {
		return fmt.Errorf("expires_at must be between 0 and %d: %w", MaxExpiresAt, domain.ErrInvalidSchema)
	}
	d.expiresAt = value
	return nil
}

// ParseExpiresAt validates a persisted deadline. An absent legacy field is persistent.
func ParseExpiresAt(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || value > MaxExpiresAt {
		return 0, fmt.Errorf("invalid stored source expiration %q", raw)
	}
	return value, nil
}

// Expired reports logical source visibility independently of cleanup progress.
func Expired(expiresAt, now int64) bool { return expiresAt > 0 && expiresAt <= now }
