// Package version holds build metadata injected at build time via -ldflags.
package version

import "fmt"

var (
	// Commit is the short git hash, with "+dirty" appended when the tree was modified.
	Commit = "dev"
	// Date is the build date (YYYY-MM-DD).
	Date = "unknown"
	// Tag is the release tag (e.g. "v0.3.1") for CI release builds, empty otherwise.
	Tag = ""
)

// String renders the canonical version string: "githash(+dirty) - date".
func String() string {
	return fmt.Sprintf("%s - %s", Commit, Date)
}

// Full includes the release tag when present.
func Full() string {
	if Tag != "" {
		return fmt.Sprintf("%s (%s)", Tag, String())
	}
	return String()
}
