package document

// SourceProjection selects parent fields without loading any fragment vectors.
// Identity, version and expiration are always returned.
type SourceProjection struct {
	Content  bool
	Metadata bool
}
