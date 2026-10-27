// Package version holds build metadata injected at link time.
package version

import "runtime"

// These are overridden by -ldflags "-X ...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human readable build identifier.
func String() string {
	return Version + " (" + Commit + ", " + Date + ", " + runtime.Version() + ")"
}
