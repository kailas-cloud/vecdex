package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrCollectionGenerationMismatch rejects handles for a different incarnation.
	ErrCollectionGenerationMismatch = errors.New("collection generation mismatch")
	// ErrIdempotencyConflict prevents reusing a source request key with another operation.
	ErrIdempotencyConflict = errors.New("idempotency key was already used with different input")
	// ErrIdempotencyInProgress asks a caller to retry the same request after its active owner finishes.
	ErrIdempotencyInProgress = errors.New("idempotent request is still in progress")
	// ErrEmbeddingSpaceMismatch requires recreating a collection after changing its vectorizer.
	ErrEmbeddingSpaceMismatch = errors.New(
		"embedding configuration changed; create a new collection and reindex its source documents",
	)
	// ErrNotFound signals a missing resource.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists signals a duplicate resource.
	ErrAlreadyExists = errors.New("already exists")
	// ErrInvalidSchema signals an invalid schema definition.
	ErrInvalidSchema = errors.New("invalid schema")
	// ErrDocumentNotFound signals a missing document.
	ErrDocumentNotFound = errors.New("document not found")
	// ErrVectorDimMismatch signals a vector dimension mismatch.
	ErrVectorDimMismatch = errors.New("vector dimension mismatch")

	// ErrRevisionConflict signals an optimistic locking conflict.
	ErrRevisionConflict = errors.New("revision conflict")
	// ErrRateLimited signals a rate limit hit.
	ErrRateLimited = errors.New("rate limited")
	// ErrEmbeddingQuotaExceeded signals an exhausted embedding budget.
	ErrEmbeddingQuotaExceeded = errors.New("embedding quota exceeded")
	// ErrEmbeddingInputTooLong identifies an explicit model context-limit rejection.
	ErrEmbeddingInputTooLong = errors.New("embedding input exceeds model context")
	// ErrEmbeddingProviderError signals an embedding provider failure.
	ErrEmbeddingProviderError = errors.New("embedding provider error")
	// ErrNotImplemented signals an unimplemented feature.
	ErrNotImplemented = errors.New("not implemented")
	// ErrKeywordSearchNotSupported signals that the backend lacks keyword search.
	ErrKeywordSearchNotSupported = errors.New("keyword search not supported by backend")
)

// RevisionConflictError wraps ErrRevisionConflict with the current resource revision.
type RevisionConflictError struct {
	CurrentRevision int
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%s: current revision is %d", ErrRevisionConflict.Error(), e.CurrentRevision)
}

func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

// NewRevisionConflict creates a revision conflict error.
func NewRevisionConflict(currentRevision int) error {
	return &RevisionConflictError{CurrentRevision: currentRevision}
}

// NewVersionConflict reports a conditional-write failure against an opaque version.
func NewVersionConflict(current string) error { return &VersionConflictError{CurrentVersion: current} }

// VersionConflictError identifies a stale or absent source incarnation.
type VersionConflictError struct{ CurrentVersion string }

func (e *VersionConflictError) Error() string {
	if e.CurrentVersion == "" {
		return "revision conflict: source version is absent"
	}
	return fmt.Sprintf("%s: current version is %s", ErrRevisionConflict, e.CurrentVersion)
}

func (e *VersionConflictError) Unwrap() error { return ErrRevisionConflict }
