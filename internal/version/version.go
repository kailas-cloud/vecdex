// Package version holds build metadata injected via ldflags.
package version

//nolint:revive // Set via ldflags at build time.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
