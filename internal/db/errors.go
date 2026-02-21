package db

import "errors"

// Sentinel errors for database operations.
var (
	ErrKeyNotFound   = errors.New("db: key not found")
	ErrKeyExists     = errors.New("db: key already exists")
	ErrIndexNotFound = errors.New("db: index not found")
	ErrIndexExists   = errors.New("db: index already exists")
)

// Op constants map to Valkey/Redis command names for error context.
const (
	OpCreateIndex = "FT.CREATE"
	OpDropIndex   = "FT.DROPINDEX"
	OpIndexInfo   = "FT.INFO"
	OpSearch      = "FT.SEARCH"
	OpDel         = "DEL"
	OpHDel        = "HDEL"
	OpHGetAll     = "HGETALL"
	OpHSet        = "HSET"
	OpExists      = "EXISTS"
	OpScan        = "SCAN"
	OpGet         = "GET"
	OpSet         = "SET"
	OpIncrBy      = "INCRBY"
	OpExpire      = "EXPIRE"
)

// Error wraps an underlying error with the operation name for diagnostics.
type Error struct {
	Op  string
	Err error
}

func (e *Error) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }
