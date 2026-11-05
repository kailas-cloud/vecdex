package domain

import "fmt"

// ValidateExpectedVersion accepts one opaque version usable as a strong ETag.
// Nil means an unconditional write; explicit empty conditions are invalid.
func ValidateExpectedVersion(value *string) error {
	if value == nil {
		return nil
	}
	if *value == "" {
		return fmt.Errorf("version condition must not be empty: %w", ErrInvalidSchema)
	}
	for _, char := range *value {
		if char <= ' ' || char == 127 || char == '"' || char == ',' {
			return fmt.Errorf("invalid source version condition: %w", ErrInvalidSchema)
		}
	}
	return nil
}
