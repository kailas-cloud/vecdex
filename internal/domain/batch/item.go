package batch

import "github.com/kailas-cloud/vecdex/internal/domain/document"

// UpsertItem binds one source document to its optional write precondition.
// Conditions are not stored as document metadata.
type UpsertItem struct {
	document.Document
	IfVersion      *string
	IdempotencyKey *string
}

// DeleteItem deletes one source only if its optional version still matches.
type DeleteItem struct {
	ID             string
	IfVersion      *string
	IdempotencyKey *string
}
