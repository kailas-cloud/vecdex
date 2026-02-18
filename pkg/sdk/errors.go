package vecdex

import "github.com/kailas-cloud/vecdex/internal/domain"

// Sentinel errors re-exported from the domain layer.
// Use errors.Is() to check.
var (
	ErrNotFound               = domain.ErrNotFound
	ErrAlreadyExists          = domain.ErrAlreadyExists
	ErrInvalidSchema          = domain.ErrInvalidSchema
	ErrDocumentNotFound       = domain.ErrDocumentNotFound
	ErrVectorDimMismatch      = domain.ErrVectorDimMismatch
	ErrRevisionConflict       = domain.ErrRevisionConflict
	ErrRateLimited            = domain.ErrRateLimited
	ErrEmbeddingQuotaExceeded = domain.ErrEmbeddingQuotaExceeded
	ErrEmbeddingProviderError = domain.ErrEmbeddingProviderError
	ErrGeoQueryInvalid        = domain.ErrGeoQueryInvalid
	ErrCollectionTypeMismatch = domain.ErrCollectionTypeMismatch
)
