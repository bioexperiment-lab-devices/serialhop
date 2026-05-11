// Package updater implements the in-app auto-update flow: latest-release
// discovery, download, SHA-256 verification, and orchestration of the
// elevated install. See docs/superpowers/specs/2026-05-11-auto-update-design.md.
package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// IsNewer reports whether `remote` is strictly newer than `local`.
//
// Both arguments may have a leading 'v' (e.g., "v0.7.0") and `local` may
// carry the "+buildmeta" suffix produced by the dev-build `-ldflags -X`
// (e.g., "0.6.1+v0.6.1-7-gabc1234-dirty"). Build metadata is stripped
// before comparison — dev builds are treated as equivalent to their base.
//
// Comparison is integer-wise on (major, minor, patch). Pre-release suffixes
// after a '-' on the SemVer side are not currently produced by this project
// and are not handled; if they appear, parse fails.
func IsNewer(remote, local string) (bool, error) {
	r, err := parse(remote)
	if err != nil {
		return false, fmt.Errorf("parse remote: %w", err)
	}
	l, err := parse(local)
	if err != nil {
		return false, fmt.Errorf("parse local: %w", err)
	}
	switch {
	case r.major != l.major:
		return r.major > l.major, nil
	case r.minor != l.minor:
		return r.minor > l.minor, nil
	default:
		return r.patch > l.patch, nil
	}
}

type semver struct{ major, minor, patch int }

func parse(v string) (semver, error) {
	s := strings.TrimPrefix(v, "v")
	// Drop "+buildmeta" if present.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("not X.Y.Z: %q", v)
	}
	out := semver{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, fmt.Errorf("bad component %q in %q", p, v)
		}
		switch i {
		case 0:
			out.major = n
		case 1:
			out.minor = n
		case 2:
			out.patch = n
		}
	}
	return out, nil
}
