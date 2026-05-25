//go:build !production

package main

// payload + ffmpegPayload are empty in dev/test builds. The release
// build pipeline invokes `go build -tags production` to switch to
// payload_production.go, which embeds the real SerialHop.exe and
// ffmpeg.exe via //go:embed. This split lets `go test ./...` and dev
// `go build` run without staging the payload files.
var (
	payload       []byte
	ffmpegPayload []byte
)
