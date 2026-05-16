package flasher

import (
	"crypto/sha256"
	"encoding/hex"
)

// shortID returns the first 8 hex chars of sha256(id). Used in slog
// attributes to avoid logging raw device identifiers verbatim.
func shortID(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:4])
}
