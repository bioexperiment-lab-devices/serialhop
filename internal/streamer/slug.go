package streamer

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// CameraIDPattern is the lab-bridge streaming protocol's URL-safe id
// charset (per docs/2026-05-24-serialhop-streaming-protocol.md as
// amended after lab-bridge PR #163). SerialHop must announce only ids
// that satisfy this pattern; the OS-level DirectShow "Alternative
// name" is NOT URL-safe (contains backslashes, hashes, slashes,
// braces) and won't round-trip through Go's net/http.ServeMux
// "{id}" pattern.
var CameraIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// SlugifyDeviceID produces a stable URL-safe id from a raw OS-level
// device path. The path itself (e.g. DirectShow's alternative name
// `@device_pnp_\?\usb#vid_...`) contains characters net/http
// URL-decodes before route matching, so an encoded `%2F` becomes a
// literal `/` and breaks the single-segment `{id}` pattern. The slug
// avoids that.
//
// Format: `cam-<sha256_first_16_hex>` (20 chars total). Properties:
//   - Stable: same input → same slug across reboots and panel runs.
//   - URL-safe: matches CameraIDPattern.
//   - Collision-resistant for the small N of cameras on one machine
//     (truncating SHA-256 to 64 bits has a birthday-bound of ~4
//     billion before a 50% collision, far beyond any realistic lab
//     setup). SHA-256 over SHA-1 only because gosec G505 blocks
//     SHA-1; the hash here is an identifier, not a crypto primitive.
//
// The original device path is opaque to operators and to the lab-
// bridge protocol; only the slug is exchanged on the wire. The
// friendly name (DirectShow's quoted label) still flows to ffmpeg via
// `-i video=<label>` because that's what ffmpeg's dshow input
// expects — see BuildWHIPArgs.
func SlugifyDeviceID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "cam-" + hex.EncodeToString(sum[:8])
}
