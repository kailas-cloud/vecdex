package db

import "context"

// HashMutation describes one atomic document change.
// Replace discards old fields; otherwise Set/Remove are a partial update.
type HashMutation struct {
	Request *WriteRequest
	// DeadlineKey maintains the partition-local live/expiration catalog.
	DeadlineKey     string
	ExpiryMarkerKey string
	ExpiresAt       int64
	// ExpiredBefore permits maintenance to delete a physically retained expired
	// source, provided its deadline and ExpectedVersion still match.
	ExpiredBefore    int64
	LifecycleKey     string
	LifecycleMember  string
	LifecyclePending bool
	// WorkQueueKey enables durable preparation and cleanup in the same hash slot.
	WorkQueueKey     string
	CatalogKey       string
	CatalogMember    string
	Key              string
	StagingKey       string
	Set              map[string]string
	Children         []HashSetItem
	Remove           []string
	Replace          bool
	Delete           bool
	RequireExists    bool
	RequireAbsent    bool
	GuardKey         string
	GuardGeneration  string
	GuardState       string
	ExpectedRevision *int
	ExpectedVersion  *string
}

// HashMutationResult is the committed revision and prior existence of the record.
type HashMutationResult struct {
	OperationID string `json:"operation_id"`
	Version     string `json:"version,omitempty"`
	Revision    int    `json:"revision"`
	Created     bool   `json:"created"`
}

// HashMutator applies a hash replacement, patch or delete with optimistic concurrency.
type HashMutator interface {
	MutateHash(ctx context.Context, mutation *HashMutation) (HashMutationResult, error)
}

// MutationWorkStore recovers registered work without scanning the keyspace.
type MutationWorkStore interface {
	RecoverMutations(ctx context.Context, queue string, limit int) (int, error)
	// CancelMutations requires the caller to close all relevant generation guards first.
	CancelMutations(ctx context.Context, queue string, limit int) (int, error)
}
