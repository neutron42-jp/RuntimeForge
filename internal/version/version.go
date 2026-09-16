// Package version exposes build metadata for the binary.
package version

// These values are overridable at build time via -ldflags.
var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a human readable version line.
func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
