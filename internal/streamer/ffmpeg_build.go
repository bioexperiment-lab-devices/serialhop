package streamer

// FFmpeg build pin. When upgrading, update all six constants together.
// The .exe SHA is what `runInstallOrUpgrade` verifies before staging the
// binary into <InstallDir>\ffmpeg.exe; the archive SHA + URL are used by
// tools/fetch-ffmpeg at build time.
const (
	// PinnedFFmpegVersion is the first-line prefix we expect from
	// `ffmpeg -version`. Substring match (not exact) so 7.1.1 / 7.1.2
	// rebuilds within the same minor line still pass the probe.
	PinnedFFmpegVersion = "ffmpeg version 7.1"

	// PinnedFFmpegBuildLabel identifies the build to humans; logged on
	// startup and included in error messages.
	PinnedFFmpegBuildLabel = "gyan.dev essentials 7.1.1"

	// PinnedFFmpegBinarySHA256 is the SHA-256 of the unpacked ffmpeg.exe.
	// Computed from the gyan.dev essentials 7.1.1 build's bin/ffmpeg.exe.
	// The installer verifies the bundled payload against this value
	// before copying it into the install dir.
	PinnedFFmpegBinarySHA256 = "b90225987bdd042cca09a1efb5e34e9848f2d1dbf5fbcd388753a44145522997"

	// PinnedFFmpegArchiveSHA256 is the SHA-256 of the .zip archive that
	// tools/fetch-ffmpeg downloads. The fetcher verifies it before
	// extracting; mismatch means refuse-to-build.
	PinnedFFmpegArchiveSHA256 = "04861d3339c5ebe38b56c19a15cf2c0cc97f5de4fa8910e4d47e5e6404e4a2d4"

	// PinnedFFmpegArchiveURL is the canonical download URL. Pinned to a
	// specific GitHub release of gyan.dev/codexffmpeg so the artifact is
	// immutable (release assets, not rolling links).
	PinnedFFmpegArchiveURL = "https://github.com/GyanD/codexffmpeg/releases/download/7.1.1/ffmpeg-7.1.1-essentials_build.zip"

	// PinnedFFmpegArchiveMember is the path within the archive that
	// holds the ffmpeg.exe we want. tools/fetch-ffmpeg extracts this
	// single file.
	PinnedFFmpegArchiveMember = "ffmpeg-7.1.1-essentials_build/bin/ffmpeg.exe"
)
