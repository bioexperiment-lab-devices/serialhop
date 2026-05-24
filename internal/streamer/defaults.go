package streamer

import "time"

// Encoding & timing defaults. These match the protocol's recommended
// targets (1280x720, ~1.5 Mbps, H.264 Constrained Baseline) and are
// applied to every session in v1 — per-camera overrides are explicitly
// deferred to v2 (see spec §11).
const (
	DefaultVideoWidth        = 1280
	DefaultVideoHeight       = 720
	DefaultFramerate         = 24
	DefaultBitrateKbps       = 1500
	DefaultKeyframeInterval  = 48 // ~2s @ 24fps
	DefaultGracefulStopGrace = 2 * time.Second
	DefaultProxyTimeout      = 5 * time.Second
	DefaultProbeTimeout      = 5 * time.Second
)
