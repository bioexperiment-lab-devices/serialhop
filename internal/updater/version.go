// Package updater implements the in-app auto-update flow: latest-release
// discovery, download, SHA-256 verification, and orchestration of the
// elevated install. See docs/superpowers/specs/2026-05-11-auto-update-design.md.
package updater

import (
	"fmt"
	"log/slog"
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
		slog.Warn("updater version parse failed", "input", remote, "err", err.Error())
		return false, fmt.Errorf("parse remote: %w", err)
	}
	l, err := parse(local)
	if err != nil {
		slog.Warn("updater version parse failed", "input", local, "err", err.Error())
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

// Compare returns -1, 0, or +1 if a is older than, equal to, or newer than b,
// respectively, by SemVer Major.Minor.Patch ordering. Both inputs may carry a
// leading "v" or a trailing "+buildmeta" segment (the dev-build shape produced
// by tools/buildcmd); they are stripped before comparison. Returns an error
// only if either input fails to parse as X.Y.Z.
func Compare(a, b string) (int, error) {
	ap, err := parse(a)
	if err != nil {
		slog.Warn("updater version parse failed", "input", a, "err", err.Error())
		return 0, fmt.Errorf("parse a: %w", err)
	}
	bp, err := parse(b)
	if err != nil {
		slog.Warn("updater version parse failed", "input", b, "err", err.Error())
		return 0, fmt.Errorf("parse b: %w", err)
	}
	switch {
	case ap.major != bp.major:
		if ap.major < bp.major {
			return -1, nil
		}
		return 1, nil
	case ap.minor != bp.minor:
		if ap.minor < bp.minor {
			return -1, nil
		}
		return 1, nil
	case ap.patch != bp.patch:
		if ap.patch < bp.patch {
			return -1, nil
		}
		return 1, nil
	default:
		return 0, nil
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
