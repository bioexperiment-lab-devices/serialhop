package version

import "strings"

// Version is overridden at build time via -ldflags -X. The "dev" default keeps
// `go run` and tests producing a sensible string.
var Version = "dev"

// Base returns Version with any SemVer build-metadata suffix (`+...`) stripped,
// e.g. "0.5.2+v0.5.2" → "0.5.2". Use this for human-facing UI surfaces where
// the git-describe suffix is noise; keep full Version in logs and CLI output
// where the provenance is useful.
func Base() string {
	if i := strings.IndexByte(Version, '+'); i >= 0 {
		return Version[:i]
	}
	return Version
}
