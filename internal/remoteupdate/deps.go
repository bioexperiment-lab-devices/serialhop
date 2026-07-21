package remoteupdate

import "github.com/bioexperiment-lab-devices/serialhop/internal/updater"

// Indirection so the default release URLs live in one place and tests can
// override via Config without importing updater.
var (
	defaultReleasesURL = updater.DefaultReleasesURL
	defaultTagURL      = updater.ReleasesByTagURL
)
