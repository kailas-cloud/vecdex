package batch

// ItemStatus is the processing outcome of a single batch item.
type ItemStatus string

// Batch item status values.
const (
	StatusOK    ItemStatus = "ok"
	StatusError ItemStatus = "error"
)

// Result is the outcome of processing one item in a batch operation.
type Result struct {
	id      string
	version string
	status  ItemStatus
	err     error
}

// NewOK creates a successful batch result.
func NewOK(id string) Result { return Result{id: id, status: StatusOK} }

// NewWritten returns the opaque version committed by a successful upsert.
func NewWritten(id, version string) Result { return Result{id: id, version: version, status: StatusOK} }

// NewError creates a failed batch result.
func NewError(id string, err error) Result { return Result{id: id, status: StatusError, err: err} }

// ID returns the item identifier.
func (r Result) ID() string { return r.id }

// Version returns the new source version after a successful upsert.
func (r Result) Version() string { return r.version }

// Status returns the processing outcome.
func (r Result) Status() ItemStatus { return r.status }

// Err returns the error, if any.
func (r Result) Err() error { return r.err }
