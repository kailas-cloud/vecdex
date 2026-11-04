package db

import "context"

// HashMutation describes one atomic document change.
// Replace discards old fields; otherwise Set/Remove are a partial update.
type HashMutation struct {
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
}

// HashMutationResult is the committed revision and prior existence of the record.
type HashMutationResult struct {
	Revision int
	Created  bool
}

// HashMutator applies a hash replacement, patch or delete with optimistic concurrency.
type HashMutator interface {
	MutateHash(ctx context.Context, mutation *HashMutation) (HashMutationResult, error)
}
