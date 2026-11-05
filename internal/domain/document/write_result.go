package document

// WriteResult identifies one committed mutation without retaining source data.
// Read the document separately when its current full representation is needed.
type WriteResult struct {
	ID      string
	Version string
	Created bool
}
