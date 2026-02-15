package domain

import (
	"errors"
	"fmt"
)

var (
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
