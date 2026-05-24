package streamer

// FFmpeg build pin. When upgrading, update all four constants together.
// Procedure: see plan task 4 step 1. The SHA256 is over ffmpeg.exe itself
// (the binary the installer copies into <DataDir>/ffmpeg.exe), not the
// archive.
const (
	// PinnedFFmpegVersion is the first-line prefix we expect from
	// `ffmpeg -version`. Substring match (not exact) so minor banner
	// differences across rebuilds don't break us.
	PinnedFFmpegVersion = "ffmpeg version 7.1"

	// PinnedFFmpegBuildLabel identifies the build to humans; logged on
	// startup and included in error messages.
	PinnedFFmpegBuildLabel = "gyan.dev essentials 7.1"

	// PinnedFFmpegBinarySHA256 is the SHA-256 of ffmpeg.exe. The installer
	// verifies the binary against this value before copying it into place.
	//
	// PLACEHOLDER: replace with the real value when picking the bundled
	// binary (see plan task 4 step 1, deferred to task 16). Until then,
	// the installer's verify step will deliberately fail, which is the
	// safe default.
	PinnedFFmpegBinarySHA256 = "REPLACE_WITH_REAL_SHA256_FROM_STEP_1"

	// PinnedFFmpegSourceURL is informational — the public download URL.
	PinnedFFmpegSourceURL = "https://www.gyan.dev/ffmpeg/builds/"
)
