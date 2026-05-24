// Package streamer implements the SerialHop side of the lab-bridge video
// streaming protocol. See docs/2026-05-24-serialhop-streaming-protocol.md
// and docs/superpowers/specs/2026-05-24-camera-streaming-design.md.
package streamer

import "time"

// Camera is one physical camera as reported by the OS enumerator.
type Camera struct {
	// ID is the stable, OS-level identifier (DirectShow "Alternative name"
	// on Windows). Survives reboots and replugs into the same USB port.
	ID string `json:"id"`
	// Label is the friendly device name shown to operators and to viewers.
	Label string `json:"label"`
}

// ArmedCamera is the persisted form of an operator-allowed camera.
type ArmedCamera struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SessionState describes a single in-flight WHIP publish.
type SessionState struct {
	CameraID  string    `json:"camera_id"`
	SessionID string    `json:"session_id"`
	StartedAt time.Time `json:"started_at"`
}

// StreamingState is the panel UI's view of the world.
type StreamingState struct {
	Cameras  []CameraView   `json:"cameras"`
	Sessions []SessionState `json:"sessions"`
	// FfmpegOK is false when the bundled ffmpeg.exe is missing or fails its
	// version probe. The UI shows a red banner in that case.
	FfmpegOK bool `json:"ffmpeg_ok"`
}

// CameraView is one row in the Cameras tab.
type CameraView struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Armed        bool   `json:"armed"`
	Connected    bool   `json:"connected"`
	Live         bool   `json:"live"` // currently publishing
	LastErrorMsg string `json:"last_error_msg,omitempty"`
}
