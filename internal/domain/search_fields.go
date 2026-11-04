package domain

import "context"

type searchMetadataFieldsKey struct{}

// WithSearchMetadataFields carries the collection's physical metadata field names
// to storage, allowing search to request metadata without downloading vectors.
func WithSearchMetadataFields(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, searchMetadataFieldsKey{}, append([]string(nil), names...))
}

// SearchMetadataFields returns the immutable metadata projection for this query.
func SearchMetadataFields(ctx context.Context) []string {
	fields, _ := ctx.Value(searchMetadataFieldsKey{}).([]string)
	return fields
}
