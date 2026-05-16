package panel

import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// probeCreds returns the lab-bridge identity triple (host, user, pass)
// the status-lamp probes should use this tick.
//
// Selection rules (see spec 2026-05-16-cached-creds-for-status-badges):
//
//   - Service is StateNotInstalled        → YAML.
//     Reason: on a fresh install before the service exists, the cache
//     hasn't been written yet, and the operator wants lamp feedback as
//     they fill in the Config tab.
//
//   - Service is installed AND cache is missing/corrupt → empty triple.
//     The probes short-circuit to Unreachable. This is an anomalous
//     state worth surfacing.
//
//   - Service is installed AND cache.Host == "" (legacy v1 from before
//     this fix, service not yet restarted post-upgrade) → YAML.
//     One-time fallback so the upgrade window doesn't appear broken.
//     Once the service is next restarted, SeedCache populates Host and
//     the cache path takes over.
//
//   - Service is installed AND cache.Host != "" → cache triple.
//     This is the steady-state happy path that fixes the bug: YAML
//     edits do not affect lamps until the service is restarted.
func (s *lampState) probeCreds(cachePath, configPath string) (host, user, pass string) {
	svc, _, _ := s.snapshot()
	if svc.state == winsvc.StateNotInstalled {
		c, _ := config.LoadPartial(configPath)
		return c.LabBridge.Host, c.LabBridge.User, c.LabBridge.Pass
	}
	c, err := bootstrap.ReadCacheRaw(cachePath)
	if err != nil {
		return "", "", ""
	}
	if c.Host == "" {
		// Pre-fix v1 cache: SeedCache hasn't run yet since upgrade.
		y, _ := config.LoadPartial(configPath)
		return y.LabBridge.Host, y.LabBridge.User, y.LabBridge.Pass
	}
	return c.Host, c.User, c.Pass
}
