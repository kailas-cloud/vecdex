// Package memorycontext stores explicit, durable agent contexts independently of MCP sessions.
package memorycontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxPayloadBytes bounds each persisted context and receipt.
	MaxPayloadBytes = 64 * 1024
	// MaxPins is the maximum number of document references in a context.
	MaxPins = 100
	// DefaultLifetime is an absolute lifetime; reads never extend it.
	DefaultLifetime = 7 * 24 * time.Hour
	// ReceiptLifetime is the idempotency window, independent of context lifetime.
	ReceiptLifetime = 24 * time.Hour
)

// Request, concurrency, and storage errors exposed to the MCP adapter.
var (
	ErrInvalid             = errors.New("invalid memory context request")
	ErrNotFound            = errors.New("memory context not found")
	ErrConflict            = errors.New("memory context version conflict")
	ErrIdempotencyConflict = errors.New("memory context idempotency key conflict")
	ErrBusy                = errors.New("memory context transaction contention")
	ErrCorrupt             = errors.New("invalid stored memory context")
)

// ConflictError reports the current version without overwriting concurrent work.
type ConflictError struct{ CurrentVersion string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: current version %s", ErrConflict, e.CurrentVersion)
}
func (e *ConflictError) Unwrap() error { return ErrConflict }

// Pin is a reference, never a snapshot of document text.
type Pin struct {
	DocumentID string `json:"document_id"`
	Version    string `json:"version"`
}

// Context binds one collection incarnation to search defaults and advisory pins.
type Context struct {
	ID         string          `json:"id"`
	Version    string          `json:"version"`
	Owner      string          `json:"owner"`
	Name       string          `json:"name"`
	Collection string          `json:"collection"`
	Generation string          `json:"generation"`
	Search     json.RawMessage `json:"search"`
	Pins       []Pin           `json:"pins"`
	ExpiresAt  *time.Time      `json:"expires_at"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// Expiry distinguishes omission from an explicit persistent (nil At) setting.
// On create omission selects DefaultLifetime; on update it preserves expiry.
type Expiry struct {
	Set bool       `json:"set"`
	At  *time.Time `json:"at"`
}

// CreateRequest opens a new context; ID and version are assigned by the service.
type CreateRequest struct {
	Owner          string          `json:"owner"`
	Name           string          `json:"name"`
	Collection     string          `json:"collection"`
	Generation     string          `json:"generation"`
	Search         json.RawMessage `json:"search"`
	Pins           []Pin           `json:"pins"`
	Expiry         Expiry          `json:"expiry"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// UpdateRequest changes context preferences, with mandatory version and idempotency guards.
// Collection and Generation cannot be changed; open a new context for a different binding.
type UpdateRequest struct {
	Owner           string           `json:"owner"`
	ID              string           `json:"id"`
	ExpectedVersion string           `json:"expected_version"`
	IdempotencyKey  string           `json:"idempotency_key"`
	Name            *string          `json:"name,omitempty"`
	Search          *json.RawMessage `json:"search,omitempty"`
	Pins            *[]Pin           `json:"pins,omitempty"`
	Expiry          Expiry           `json:"expiry"`
}

// DeleteRequest closes a context while retaining its idempotency receipts.
type DeleteRequest struct {
	Owner           string `json:"owner"`
	ID              string `json:"id"`
	ExpectedVersion string `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

// MutationResult always contains the original committed value, including on replay.
// CurrentState is "present" or "absent" at the latest read; CurrentVersion may differ
// from Context.Version. For deletion Context is the removed value, not a live object.
type MutationResult struct {
	Context        Context `json:"context"`
	Operation      string  `json:"operation"`
	Replayed       bool    `json:"replayed"`
	CurrentState   string  `json:"current_state"`
	CurrentVersion string  `json:"current_version,omitempty"`
}

// ListRequest lists only contexts belonging to Owner. Cursor is owner-bound.
type ListRequest struct {
	Owner  string `json:"owner"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Page is bounded, ordered by opaque ID, and does not promise a snapshot under writes.
// Expired records can make a page shorter than Limit while NextCursor remains nonempty.
type Page struct {
	Contexts   []Context `json:"contexts"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
