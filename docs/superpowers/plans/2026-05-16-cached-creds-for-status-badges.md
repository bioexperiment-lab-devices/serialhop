# Cached Credentials for Status Badges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the panel's Server and Tunnel status lamps probe against the credentials the running Windows service is actually using, by caching the lab-bridge identity (`host`, `user`, `pass`) in `server-info.cache.json` and reading it from the cache instead of the YAML on every probe tick.

**Architecture:** Extend the existing `bootstrap.Cache` with `Host`/`Pass` fields and a new `ReadCacheRaw` that ignores the user anchor. Add `bootstrap.SeedCache` which writes the running identity into the cache at the very top of service start (foreground and SCM worker) — before `bootstrap.Resolve` runs — so the cache always reflects the credentials currently in use. The panel adds a `probeCreds` selector that reads from the cache (or falls back to YAML when the service is `StateNotInstalled` or the cache is from a pre-fix v1) and the probe loops use it instead of `LoadPartial`.

**Tech Stack:** Go 1.x, Wails v2 (Windows-only), standard `testing` package. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-16-cached-creds-for-status-badges-design.md`

---

## File map

**New:**
- `internal/bootstrap/seed.go` — `SeedCache` function.
- `internal/bootstrap/seed_test.go` — tests for `SeedCache`.
- `internal/panel/probe_creds.go` — `(*lampState).probeCreds(cachePath, configPath)` selector.
- `internal/panel/probe_creds_test.go` — tests for the selector.

**Modified:**
- `internal/bootstrap/cache.go` — add `Host`/`Pass` fields, add `ReadCacheRaw`.
- `internal/bootstrap/cache_test.go` — extend with new field round-trip + `ReadCacheRaw` cases.
- `internal/winsvc/worker.go` — call `SeedCache` before `bootstrap.Resolve`.
- `main.go` — call `SeedCache` in `runForeground` before `bootstrap.Resolve`.
- `internal/panel/servicecli.go` — drop `user` field, `NewServiceCli(cachePath string)`, use `ReadCacheRaw`.
- `internal/panel/servicecli_test.go` — update call sites.
- `internal/panel/wails_app.go` — replace `LoadPartial` blocks in probe loops with `a.lamps.probeCreds(...)`; drop second arg on `NewServiceCli`.

**Untouched (verified):**
- `internal/panel/bindings.go::Diagnostics` already reads the cache file raw via `os.ReadFile` + `json.Unmarshal` (lines 425-440), so it surfaces whatever is on disk without changes.
- `internal/app/app.go` and its tests — `writeActualRestPort` still uses the anchored `ReadCache`, which is fine because `SeedCache` runs first and aligns the cache user to the current YAML user.

---

## Task 1: Add `Host` and `Pass` fields to `bootstrap.Cache` (back-compat preserved)

**Files:**
- Modify: `internal/bootstrap/cache.go:30-37`
- Test: `internal/bootstrap/cache_test.go`

- [ ] **Step 1: Write the failing test for round-trip of `Host` and `Pass`**

Append to `internal/bootstrap/cache_test.go`:

```go
func TestWriteCache_AndReadCache_RoundTripIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.Host = "lab-bridge.example.com"
	in.Pass = "s3cret"
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.Host != "lab-bridge.example.com" {
		t.Errorf("Host: got %q, want %q", got.Host, "lab-bridge.example.com")
	}
	if got.Pass != "s3cret" {
		t.Errorf("Pass: got %q, want %q", got.Pass, "s3cret")
	}
}

func TestWriteCache_HostAndPassJSONKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.Host = "lab-bridge.example.com"
	in.Pass = "s3cret"
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	data, err := os.ReadFile(p) //nolint:gosec // p is t.TempDir() + literal filename
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(data)
	for _, want := range []string{`"host": "lab-bridge.example.com"`, `"pass": "s3cret"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing key/value %s; body:\n%s", want, body)
		}
	}
}

func TestReadCache_LegacyV1FileHasEmptyHostAndPass(t *testing.T) {
	// Simulates a v1 cache written before this change: no host/pass keys.
	p := filepath.Join(t.TempDir(), "cache.json")
	legacy := `{
		"version": 1,
		"fetched_at": "2026-05-13T00:00:00Z",
		"user": "alice",
		"server_info": {"chisel_listen_port": 7000, "loki_push_url": "", "forward_tunnels": null},
		"remote_port": 8089,
		"actual_rest_port": 49283
	}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.Host != "" {
		t.Errorf("Host: got %q, want empty", got.Host)
	}
	if got.Pass != "" {
		t.Errorf("Pass: got %q, want empty", got.Pass)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort: got %d, want 49283", got.ActualRestPort)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bootstrap/ -run 'TestWriteCache_AndReadCache_RoundTripIdentity|TestWriteCache_HostAndPassJSONKeys|TestReadCache_LegacyV1FileHasEmptyHostAndPass' -v`
Expected: FAIL — compile errors `in.Host undefined` and `in.Pass undefined` (the struct does not yet have those fields).

- [ ] **Step 3: Add the fields to the `Cache` struct**

In `internal/bootstrap/cache.go`, replace the existing `Cache` struct (lines 27-37) with:

```go
// Cache is the on-disk schema for server-info.cache.json. The User
// field anchors the cache to a specific identity so that changing
// lab_bridge.user in the YAML invalidates stale data automatically.
// Host/User/Pass record the lab-bridge identity the running service is
// using; they are written by SeedCache at service start (before
// bootstrap.Resolve) so the panel's status-badge probes always probe
// the credentials the service is actually using, not whatever the YAML
// currently says.
type Cache struct {
	Version        int                  `json:"version"`
	FetchedAt      string               `json:"fetched_at"`
	Host           string               `json:"host"`
	User           string               `json:"user"`
	Pass           string               `json:"pass"`
	ServerInfo     labbridge.ServerInfo `json:"server_info"`
	RemotePort     int                  `json:"remote_port"`
	ActualRestPort int                  `json:"actual_rest_port"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bootstrap/ -run 'TestWriteCache_AndReadCache_RoundTripIdentity|TestWriteCache_HostAndPassJSONKeys|TestReadCache_LegacyV1FileHasEmptyHostAndPass' -v`
Expected: PASS for all three.

- [ ] **Step 5: Verify the full bootstrap test suite still passes**

Run: `go test ./internal/bootstrap/ -v`
Expected: PASS, including pre-existing tests (round-trip, atomic, version-mismatch, etc.).

- [ ] **Step 6: Commit**

```bash
git add internal/bootstrap/cache.go internal/bootstrap/cache_test.go
git commit -m "$(cat <<'EOF'
fix(bootstrap): add Host and Pass to server-info cache schema

Lays the groundwork for status-badge probes to read identity from the
cache instead of the YAML. Cache version stays at 1; a pre-fix file
reads with empty Host/Pass and no error.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `ReadCacheRaw` (unanchored read)

**Files:**
- Modify: `internal/bootstrap/cache.go`
- Test: `internal/bootstrap/cache_test.go`

- [ ] **Step 1: Write failing tests for `ReadCacheRaw`**

Append to `internal/bootstrap/cache_test.go`:

```go
func TestReadCacheRaw_ReturnsCacheRegardlessOfUser(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache() // User: "alice"
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.User != "alice" {
		t.Errorf("User: got %q, want %q", got.User, "alice")
	}
	if got.RemotePort != in.RemotePort {
		t.Errorf("RemotePort: got %d, want %d", got.RemotePort, in.RemotePort)
	}
}

func TestReadCacheRaw_MissingFileReturnsErrCacheMissing(t *testing.T) {
	_, err := ReadCacheRaw(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing, got %v", err)
	}
}

func TestReadCacheRaw_VersionMismatchDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte(`{"version":99,"user":"alice"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCacheRaw(p)
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on version mismatch, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected version-mismatch cache file to be deleted; stat err = %v", statErr)
	}
}

func TestReadCacheRaw_CorruptJSONDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCacheRaw(p)
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on corrupt JSON, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected corrupt cache file to be deleted; stat err = %v", statErr)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bootstrap/ -run 'TestReadCacheRaw_' -v`
Expected: FAIL with `undefined: ReadCacheRaw`.

- [ ] **Step 3: Add `ReadCacheRaw` to `cache.go`**

Append to `internal/bootstrap/cache.go` (after the existing `ReadCache` function, before the closing of the file):

```go
// ReadCacheRaw reads the cache file at path without checking the user
// anchor. Same error contract and side effects as ReadCache (missing /
// corrupt / version-mismatched files return ErrCacheMissing; corrupt
// and version-mismatched files are also deleted). Used by panel code
// that wants whatever the running service wrote, regardless of whether
// the YAML's lab_bridge.user currently matches.
func ReadCacheRaw(path string) (Cache, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ServerInfoCachePath() under DataDir
	if err != nil {
		if os.IsNotExist(err) {
			return Cache{}, ErrCacheMissing
		}
		slog.Warn("bootstrap: read cache failed", "path", path, "err", err)
		return Cache{}, ErrCacheMissing
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Warn("bootstrap: cache corrupt; deleting", "path", path, "err", err)
		_ = os.Remove(path)
		return Cache{}, ErrCacheMissing
	}
	if c.Version != cacheCurrentVersion {
		slog.Warn("bootstrap: cache version mismatch; deleting", "path", path, "version", c.Version)
		_ = os.Remove(path)
		return Cache{}, ErrCacheMissing
	}
	return c, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bootstrap/ -run 'TestReadCacheRaw_' -v`
Expected: PASS for all four.

- [ ] **Step 5: Verify the full bootstrap test suite still passes**

Run: `go test ./internal/bootstrap/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/bootstrap/cache.go internal/bootstrap/cache_test.go
git commit -m "$(cat <<'EOF'
fix(bootstrap): add ReadCacheRaw for unanchored cache reads

Mirrors ReadCache but skips the user-mismatch check. Lets the panel
read whatever the running service wrote, even if the operator changed
lab_bridge.user in the YAML since the service started.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `SeedCache`

**Files:**
- Create: `internal/bootstrap/seed.go`
- Test: `internal/bootstrap/seed_test.go`

- [ ] **Step 1: Write failing tests for `SeedCache`**

Create `internal/bootstrap/seed_test.go`:

```go
package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestSeedCache_MissingFileCreatesFreshCache(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := SeedCache(p, "host.example", "alice", "pw"); err != nil {
		t.Fatalf("SeedCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Version != cacheCurrentVersion {
		t.Errorf("Version: got %d, want %d", got.Version, cacheCurrentVersion)
	}
	if got.Host != "host.example" || got.User != "alice" || got.Pass != "pw" {
		t.Errorf("identity triple: got (%q,%q,%q), want (host.example,alice,pw)", got.Host, got.User, got.Pass)
	}
	if got.FetchedAt == "" {
		t.Errorf("FetchedAt should be set")
	}
}

func TestSeedCache_PreservesExistingNonIdentityFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	prior := Cache{
		Version:        cacheCurrentVersion,
		FetchedAt:      "2026-05-01T00:00:00Z",
		Host:           "old.example",
		User:           "old-user",
		Pass:           "old-pw",
		ServerInfo:     labbridge.ServerInfo{ChiselListenPort: 7000, LokiPushURL: "http://loki:3100"},
		RemotePort:     8089,
		ActualRestPort: 49283,
	}
	if err := WriteCache(p, prior); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	if err := SeedCache(p, "new.example", "new-user", "new-pw"); err != nil {
		t.Fatalf("SeedCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Host != "new.example" || got.User != "new-user" || got.Pass != "new-pw" {
		t.Errorf("identity not overwritten: got (%q,%q,%q)", got.Host, got.User, got.Pass)
	}
	if got.ServerInfo.ChiselListenPort != 7000 {
		t.Errorf("ServerInfo.ChiselListenPort clobbered: got %d", got.ServerInfo.ChiselListenPort)
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort clobbered: got %d", got.RemotePort)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort clobbered: got %d", got.ActualRestPort)
	}
}

func TestSeedCache_CorruptCacheGetsReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SeedCache(p, "h", "u", "p"); err != nil {
		t.Fatalf("SeedCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Host != "h" || got.User != "u" || got.Pass != "p" {
		t.Errorf("identity: got (%q,%q,%q)", got.Host, got.User, got.Pass)
	}
}

func TestSeedCache_IsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := SeedCache(p, "h", "u", "pw"); err != nil {
		t.Fatalf("SeedCache #1: %v", err)
	}
	if err := SeedCache(p, "h", "u", "pw"); err != nil {
		t.Fatalf("SeedCache #2: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Host != "h" || got.User != "u" || got.Pass != "pw" {
		t.Errorf("identity: got (%q,%q,%q)", got.Host, got.User, got.Pass)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bootstrap/ -run 'TestSeedCache_' -v`
Expected: FAIL with `undefined: SeedCache`.

- [ ] **Step 3: Create `seed.go`**

Create `internal/bootstrap/seed.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bootstrap/ -run 'TestSeedCache_' -v`
Expected: PASS for all four.

- [ ] **Step 5: Run the whole bootstrap suite to verify no regression**

Run: `go test ./internal/bootstrap/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/bootstrap/seed.go internal/bootstrap/seed_test.go
git commit -m "$(cat <<'EOF'
fix(bootstrap): add SeedCache to record running lab-bridge identity

Writes host/user/pass into the cache while preserving other fields.
Will be called at service startup before bootstrap.Resolve so the cache
always reflects the credentials the running service is actually using.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Wire `SeedCache` into the SCM worker

**Files:**
- Modify: `internal/winsvc/worker.go:71-96`

No test for this site directly — the worker's `Execute` method is Windows-only and runs inside an SCM handle. `SeedCache` itself is covered by Task 3; this task is purely the wiring. We verify the file builds.

- [ ] **Step 1: Insert `SeedCache` call before `bootstrap.Resolve`**

In `internal/winsvc/worker.go`, replace the goroutine body (lines 71-96) with:

```go
	appDone := make(chan error, 1)
	go func() {
		hc := &http.Client{Timeout: 30 * time.Second}
		userAgent := "SerialHop/" + version.Base() + " (bootstrap)"
		// Seed the bootstrap cache with the running lab-bridge identity
		// BEFORE we start trying to resolve. This way the panel's
		// status-lamp probes (which read the cache) reflect the
		// credentials the service is actually using, even if Resolve is
		// stuck in a retry loop because those credentials are wrong.
		if err := bootstrap.SeedCache(
			paths.ServerInfoCachePath(),
			cfg.LabBridge.Host, cfg.LabBridge.User, cfg.LabBridge.Pass,
		); err != nil {
			slog.Warn("seed cache failed", "err", err)
		}
		resolved, err := bootstrap.Resolve(ctx, bootstrap.Options{
			HTTPClient: hc,
			Base:       "https://" + cfg.LabBridge.Host,
			User:       cfg.LabBridge.User,
			Pass:       cfg.LabBridge.Pass,
			CachePath:  paths.ServerInfoCachePath(),
			UserAgent:  userAgent,
		})
		if err != nil {
			// ctx.Err() means we're shutting down — exit cleanly without
			// surfacing this as a service failure.
			if ctx.Err() != nil {
				appDone <- nil
				return
			}
			appDone <- fmt.Errorf("bootstrap: %w", err)
			return
		}
		h.manager.SetPushURL(resolved.ServerInfo.LokiPushURL)
		h.manager.StartShipper(cfg.LabBridge.User)
		appDone <- app.Run(ctx, cfg, resolved)
	}()
```

- [ ] **Step 2: Verify the package still compiles for Windows**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/winsvc/...`
Expected: no output (success).

- [ ] **Step 3: Run any non-Windows tests for the package**

Run: `go test ./internal/winsvc/ -v`
Expected: PASS (the worker file itself is `//go:build windows` so tests of it don't run on macOS; the platform-neutral SCM tests still execute).

- [ ] **Step 4: Commit**

```bash
git add internal/winsvc/worker.go
git commit -m "$(cat <<'EOF'
fix(winsvc): seed cache with running identity before bootstrap

Service start now writes the lab-bridge host/user/pass into the
bootstrap cache before kicking off Resolve. Panel probes will read from
this snapshot so they reflect the credentials the service is actually
using, regardless of subsequent YAML edits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Wire `SeedCache` into the foreground entry point

**Files:**
- Modify: `main.go:144-158`

- [ ] **Step 1: Insert `SeedCache` call in `runForeground`**

In `main.go`, replace the block from line 144 to line 158 with:

```go
	hc := &http.Client{Timeout: 30 * time.Second}
	// Seed the bootstrap cache with the running lab-bridge identity
	// BEFORE we start trying to resolve, so panel status-lamp probes
	// (which read the cache) reflect the credentials the foreground run
	// is actually using.
	if err := bootstrap.SeedCache(
		paths.ServerInfoCachePath(),
		cfg.LabBridge.Host, cfg.LabBridge.User, cfg.LabBridge.Pass,
	); err != nil {
		slog.Warn("seed cache failed", "err", err)
	}
	resolved, err := bootstrap.Resolve(ctx, bootstrap.Options{
		HTTPClient: hc,
		Base:       "https://" + cfg.LabBridge.Host,
		User:       cfg.LabBridge.User,
		Pass:       cfg.LabBridge.Pass,
		CachePath:  paths.ServerInfoCachePath(),
		UserAgent:  "SerialHop/" + internalversion.Base() + " (foreground)",
	})
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	return app.Run(ctx, cfg, resolved)
```

- [ ] **Step 2: Verify the binary builds for Windows**

Run: `GOOS=windows GOARCH=amd64 go build .`
Expected: no output (success). `main.go` has `//go:build windows` so this only builds for Windows.

- [ ] **Step 3: Run cross-platform tests for the root package**

Run: `go test . -v`
Expected: PASS — `main_test.go` is the existing minimal cross-platform test, unaffected by this change.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "$(cat <<'EOF'
fix(main): seed cache with running identity in runForeground

Foreground (developer-mode) start now writes the lab-bridge identity
into the bootstrap cache before Resolve, matching the SCM worker path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Drop `user` from `ServiceCli`; switch to `ReadCacheRaw`

**Files:**
- Modify: `internal/panel/servicecli.go`
- Modify: `internal/panel/servicecli_test.go`
- Modify: `internal/panel/wails_app.go:65` (call-site of `NewServiceCli`)

The existing tests already create cache files with `User: "alice"` and call `NewServiceCli(path, "alice")`. After this change they will call `NewServiceCli(path)` and rely on the unanchored read, which works regardless of what user the cache claims.

- [ ] **Step 1: Add a failing test that demonstrates user-anchor independence**

Append to `internal/panel/servicecli_test.go`:

```go
func TestServiceCli_GetDevices_IgnoresCacheUserMismatch(t *testing.T) {
	// Cache anchored to "alice"; client constructed without any user arg.
	// The local REST port belongs to whichever service is running, so the
	// client must talk to it regardless of YAML lab_bridge.user changes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{Devices: []api.DeviceDTO{{ID: "x"}}})
	}))
	defer srv.Close()
	port := mustPortFromURL(t, srv.URL)
	// seedCache writes a cache anchored to "alice"; we now build the client
	// with no user argument and expect it to talk to the server anyway.
	cli := NewServiceCli(seedCache(t, port))
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/panel/ -run TestServiceCli_GetDevices_IgnoresCacheUserMismatch -v`
Expected: FAIL — `NewServiceCli` still requires the `user` argument.

- [ ] **Step 3: Update `servicecli.go` to drop `user`, use `ReadCacheRaw`**

Replace the relevant parts of `internal/panel/servicecli.go`:

Replace the struct and constructor (lines 35-53) with:

```go
// ServiceCli is a thin typed HTTP client that talks to the local
// SerialHop service over 127.0.0.1:<ActualRestPort>. It reads the
// bootstrap cache per call so a service restart while the panel is
// open doesn't strand it on a stale port. The cache is read unanchored
// (via ReadCacheRaw): the local REST listener belongs to whichever
// service is running, regardless of whether the YAML's lab_bridge.user
// has since been edited.
type ServiceCli struct {
	cachePath string
	hc        *http.Client
}

// NewServiceCli returns a client anchored only to the given bootstrap-
// cache path. The HTTP client has a 5 s per-call timeout.
func NewServiceCli(cachePath string) *ServiceCli {
	return &ServiceCli{
		cachePath: cachePath,
		hc:        &http.Client{Timeout: 5 * time.Second},
	}
}
```

Replace `baseURL` (lines 56-66) with:

```go
// baseURL reads the cache and returns "http://127.0.0.1:<port>".
// Returns StatusUnreachable on any cache-read failure or zero port.
func (c *ServiceCli) baseURL() (string, ServiceCliStatus) {
	cache, err := bootstrap.ReadCacheRaw(c.cachePath)
	if err != nil {
		return "", StatusUnreachable
	}
	if cache.ActualRestPort == 0 {
		return "", StatusUnreachable
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cache.ActualRestPort)), StatusOK
}
```

- [ ] **Step 4: Update existing `servicecli_test.go` call sites to match the new signature**

In `internal/panel/servicecli_test.go`, change every `NewServiceCli(seedCache(t, port), "alice")` and `NewServiceCli(filepath.Join(...), "alice")` to drop the second argument:

```go
// At line 48 (TestServiceCli_GetDevices_OK):
cli := NewServiceCli(seedCache(t, port))

// At line 62 (TestServiceCli_GetDevices_CacheMissingReturnsUnreachable):
cli := NewServiceCli(filepath.Join(t.TempDir(), "missing.json"))

// At line 73 (TestServiceCli_GetDevices_ActualPortZeroReturnsUnreachable):
cli := NewServiceCli(seedCache(t, 0))

// At line 85 (TestServiceCli_GetDevices_ConnectionRefusedReturnsServiceDown):
cli := NewServiceCli(seedCache(t, 1))

// At line 104 (TestServiceCli_Discover_PostsToDiscover):
cli := NewServiceCli(seedCache(t, port))
```

- [ ] **Step 5: Update the `wails_app.go` call site**

In `internal/panel/wails_app.go` at line 65, change:

```go
a.svc = NewServiceCli(paths.ServerInfoCachePath(), cfg.LabBridge.User)
```

to:

```go
a.svc = NewServiceCli(paths.ServerInfoCachePath())
```

The `cfg, _ := config.LoadPartial(...)` on line 64 is now used only for the `AutoUpdate` check on line 66, so leave it in place.

- [ ] **Step 6: Run the panel tests**

Run: `go test ./internal/panel/ -v`
Expected: PASS, including the new `TestServiceCli_GetDevices_IgnoresCacheUserMismatch` and all existing panel tests.

- [ ] **Step 7: Verify Windows build**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: no output (success).

- [ ] **Step 8: Commit**

```bash
git add internal/panel/servicecli.go internal/panel/servicecli_test.go internal/panel/wails_app.go
git commit -m "$(cat <<'EOF'
fix(panel): drop user anchor from ServiceCli, read cache raw

The local REST listener belongs to whichever service is running, so
talking to it shouldn't depend on the YAML's lab_bridge.user matching
what the cache was anchored to. NewServiceCli now takes only the cache
path and uses ReadCacheRaw.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Add `(*lampState).probeCreds(cachePath, configPath)`

**Files:**
- Create: `internal/panel/probe_creds.go`
- Create: `internal/panel/probe_creds_test.go`

This selector is the heart of the fix. It is a method on `*lampState` (cross-platform, defined in `lampstate.go`) and so is testable on macOS/Linux without touching the Windows-only `App` struct.

- [ ] **Step 1: Write failing tests for `probeCreds`**

Create `internal/panel/probe_creds_test.go`:

```go
package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func writeYAML(t *testing.T, path, host, user, pass string) {
	t.Helper()
	body := "lab_bridge:\n" +
		"  host: " + host + "\n" +
		"  user: " + user + "\n" +
		"  pass: " + pass + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
}

func TestProbeCreds_NotInstalled_UsesYAML(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		Host: "cache.example", User: "cache-user", Pass: "cache-pw",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateNotInstalled})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "yaml.example" || user != "yaml-user" || pass != "yaml-pw" {
		t.Errorf("got (%q,%q,%q), want YAML triple", host, user, pass)
	}
}

func TestProbeCreds_Running_UsesCacheWhenHostPresent(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		Host: "cache.example", User: "cache-user", Pass: "cache-pw",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateRunning})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "cache.example" || user != "cache-user" || pass != "cache-pw" {
		t.Errorf("got (%q,%q,%q), want cache triple", host, user, pass)
	}
}

func TestProbeCreds_Running_LegacyCacheFallsBackToYAML(t *testing.T) {
	// Simulates a v1 cache file written before this fix: Host/Pass empty.
	// The service hasn't yet been restarted post-upgrade, so probeCreds
	// falls back to YAML one time to avoid lamps appearing broken.
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		User: "cache-user", // Host/Pass intentionally empty
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateRunning})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "yaml.example" || user != "yaml-user" || pass != "yaml-pw" {
		t.Errorf("got (%q,%q,%q), want YAML triple (legacy fallback)", host, user, pass)
	}
}

func TestProbeCreds_Running_CacheMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "missing.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateRunning})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "" || user != "" || pass != "" {
		t.Errorf("got (%q,%q,%q), want empty triple (cache missing while installed)", host, user, pass)
	}
}

func TestProbeCreds_Stopped_StillUsesCache(t *testing.T) {
	// Stopped is "installed but not running" — we don't fall back to YAML
	// in that state; we want lamps to reflect what the (now-stopped)
	// service was using.
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		Host: "cache.example", User: "cache-user", Pass: "cache-pw",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateStopped})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "cache.example" || user != "cache-user" || pass != "cache-pw" {
		t.Errorf("got (%q,%q,%q), want cache triple (stopped state)", host, user, pass)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/panel/ -run TestProbeCreds_ -v`
Expected: FAIL — `ls.probeCreds undefined`.

- [ ] **Step 3: Create `probe_creds.go`**

Create `internal/panel/probe_creds.go`:

```go
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
		y, _ := config.LoadPartial(configPath)
		return y.LabBridge.Host, y.LabBridge.User, y.LabBridge.Pass
	}
	return c.Host, c.User, c.Pass
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/panel/ -run TestProbeCreds_ -v`
Expected: PASS for all five.

- [ ] **Step 5: Run the whole panel test suite**

Run: `go test ./internal/panel/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/probe_creds.go internal/panel/probe_creds_test.go
git commit -m "$(cat <<'EOF'
fix(panel): add probeCreds selector for status-lamp identity

Reads identity from the bootstrap cache when the service is installed
and the cache has Host populated; falls back to YAML when the service
is not installed, or when a pre-fix v1 cache hasn't yet been rewritten
by a restart. Pure method on *lampState — testable cross-platform.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Switch the panel's probe loops to use `probeCreds`

**Files:**
- Modify: `internal/panel/wails_app.go:77-94`

This is the wiring task — the existing `config.LoadPartial(...)` blocks inside the two probe-loop closures get replaced with `a.lamps.probeCreds(...)`. No new tests: the selector itself is covered in Task 7; the probe machinery is covered by existing tests in `probe_test.go`.

- [ ] **Step 1: Replace the two probe-loop closures**

In `internal/panel/wails_app.go`, replace the two `go probeLoop(...)` blocks (lines 77-94) with:

```go
	go probeLoop(ctx, 30*time.Second, a.serverTrigger, func(ctx context.Context) {
		host, _, _ := a.lamps.probeCreds(paths.ServerInfoCachePath(), paths.ConfigPath())
		base := ""
		if host != "" {
			base = "https://" + host
		}
		runServerProbe(ctx, probeHC, base, userAgent, a.lamps)
		a.emitServerLamp()
	})
	go probeLoop(ctx, 30*time.Second, a.tunnelTrigger, func(ctx context.Context) {
		host, user, pass := a.lamps.probeCreds(paths.ServerInfoCachePath(), paths.ConfigPath())
		base := ""
		if host != "" {
			base = "https://" + host
		}
		runTunnelProbe(ctx, probeHC, base, user, pass, userAgent, a.lamps)
		a.emitTunnelLamp()
	})
```

- [ ] **Step 2: Verify the Windows build**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: no output (success).

- [ ] **Step 3: Run the panel test suite**

Run: `go test ./internal/panel/ -v`
Expected: PASS (including pre-existing `probe_test.go` cases — the probe goroutine plumbing is unchanged).

- [ ] **Step 4: Commit**

```bash
git add internal/panel/wails_app.go
git commit -m "$(cat <<'EOF'
fix(panel): probe loops read identity from cache, not YAML

Server and Tunnel status lamps now reflect the credentials the running
service is actually using. Saving a new lab_bridge.host (or .user /
.pass) in the YAML no longer makes the lamps flip to Unreachable until
the operator restarts the service — at which point SeedCache updates
the cache and the lamps re-probe the new endpoint.

Closes: status-badge / config-edit ghosting issue.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Pre-flight verification before opening the PR

**Files:** none (verification only).

These commands match the `pr.yml` `verify` job per `CLAUDE.md`. Run them locally so the CI round-trip is clean.

- [ ] **Step 1: Format check**

Run: `gofmt -l .`
Expected: prints nothing.

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: prints nothing, exit 0.

- [ ] **Step 3: Lint**

Run: `golangci-lint run`
Expected: exit 0, no findings.

- [ ] **Step 4: Test with race + count=1 on darwin/amd64 (host)**

Run: `go test -race -count=1 ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Windows build**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: no output.

- [ ] **Step 6: Vulnerability scan**

Run: `govulncheck ./...`
Expected: exit 0, no actionable findings.

- [ ] **Step 7: Confirm git status is clean**

Run: `git status`
Expected: nothing to commit (working tree clean).

- [ ] **Step 8: Confirm branch is ready for PR**

The branch should contain (in order):
1. `docs: spec for caching running lab-bridge identity for status-badge probes` (already committed)
2. `docs: correct bindings.go note in cached-creds spec` (already committed)
3. `fix(bootstrap): add Host and Pass to server-info cache schema`
4. `fix(bootstrap): add ReadCacheRaw for unanchored cache reads`
5. `fix(bootstrap): add SeedCache to record running lab-bridge identity`
6. `fix(winsvc): seed cache with running identity before bootstrap`
7. `fix(main): seed cache with running identity in runForeground`
8. `fix(panel): drop user anchor from ServiceCli, read cache raw`
9. `fix(panel): add probeCreds selector for status-lamp identity`
10. `fix(panel): probe loops read identity from cache, not YAML`

Run: `git log --oneline main..HEAD`
Expected: ten commits matching the above.

- [ ] **Step 9: Open PR**

The PR title will be squashed onto `main`, and `release-please` parses it. Use:

```
fix(panel): cache running lab-bridge identity for status-badge probes
```

This is a `fix:` Conventional Commit → patch bump on the next release.
