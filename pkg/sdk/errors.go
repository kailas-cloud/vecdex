package vecdex

import "github.com/kailas-cloud/vecdex/internal/domain"

// Sentinel errors re-exported from the domain layer.
// Use errors.Is() to check.
var (
	ErrIdempotencyConflict       = domain.ErrIdempotencyConflict
	ErrIdempotencyInProgress     = domain.ErrIdempotencyInProgress
	ErrNotFound                  = domain.ErrNotFound
	ErrAlreadyExists             = domain.ErrAlreadyExists
	ErrInvalidSchema             = domain.ErrInvalidSchema
	ErrDocumentNotFound          = domain.ErrDocumentNotFound
	ErrVectorDimMismatch         = domain.ErrVectorDimMismatch
	ErrRevisionConflict          = domain.ErrRevisionConflict
	ErrRateLimited               = domain.ErrRateLimited
	ErrEmbeddingQuotaExceeded    = domain.ErrEmbeddingQuotaExceeded
	ErrEmbeddingProviderError    = domain.ErrEmbeddingProviderError
	ErrEmbeddingInputTooLong     = domain.ErrEmbeddingInputTooLong
	ErrEmbeddingSpaceMismatch    = domain.ErrEmbeddingSpaceMismatch
	ErrKeywordSearchNotSupported = domain.ErrKeywordSearchNotSupported
	ErrNotImplemented            = domain.ErrNotImplemented
)

// VersionConflictError includes the current opaque version when a source exists.
type VersionConflictError = domain.VersionConflictError
