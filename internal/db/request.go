package db

import "context"

// WriteRequest is a partition-local claim for an externally retried mutation.
// Its small outcome is committed in the same transaction as the source.
type WriteRequest struct {
	Key             string
	Fingerprint     string
	Owner           string
	GuardKey        string
	GuardGeneration string
}

// WriteRequestResult either grants the claim or replays a committed outcome.
type WriteRequestResult struct {
	Replay bool
	Result HashMutationResult
}

// WriteRequestStore prevents concurrent duplicate preparation and retains
// successful mutation outcomes for 24 hours without retaining source copies.
type WriteRequestStore interface {
	ClaimWriteRequest(ctx context.Context, request *WriteRequest) (WriteRequestResult, error)
	ReleaseWriteRequest(ctx context.Context, request *WriteRequest) error
}
