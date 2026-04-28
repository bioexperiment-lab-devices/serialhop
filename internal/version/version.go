package version

// Version is overridden at build time via -ldflags -X. The "dev" default keeps
// `go run` and tests producing a sensible string.
var Version = "dev"
