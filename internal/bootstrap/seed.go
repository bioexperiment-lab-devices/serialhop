package bootstrap

import (
	"time"
)

// SeedCache writes the running lab-bridge identity (host/user/pass) into
// the cache at path, preserving any server_info / remote_port /
// actual_rest_port from a previous run. Called at service startup
// (worker.Execute and main.runForeground) BEFORE bootstrap.Resolve, so
// that the cache always reflects the credentials the service is actually
// using — even if bootstrap is stuck in a retry loop because the
// credentials are wrong.
//
// If the cache file is missing or corrupt, SeedCache writes a fresh one
// with Version=cacheCurrentVersion and only the identity triple
// populated. Idempotent.
func SeedCache(path, host, user, pass string) error {
	c, err := ReadCacheRaw(path)
	if err != nil {
		// ErrCacheMissing (file absent / corrupt / version-mismatch — the
		// last two cases also delete the file). Start fresh.
		c = Cache{Version: cacheCurrentVersion}
	}
	c.Version = cacheCurrentVersion
	c.Host = host
	c.User = user
	c.Pass = pass
	c.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return WriteCache(path, c)
}
