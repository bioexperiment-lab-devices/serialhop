# Config cleanup + server-info-driven bootstrap + first-run creds dialog — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drop hardcoded chisel/Loki values from the YAML config and from Go constants; fetch them from `GET /api/public/server-info` at startup with a user-anchored disk cache; gate the panel's first launch on a modal dialog that collects `username`/`password` and verifies them against `GET /api/public/clients/{user}` before saving.

**Spec:** `docs/superpowers/specs/2026-05-11-config-server-info-design.md`

**Architecture:** A new `internal/bootstrap` package resolves a `Resolved{ServerInfo, RemotePort}` at service-worker startup using parallel calls to `labbridge.FetchServerInfo` and `labbridge.FetchClient`, falling back to a user-anchored on-disk JSON cache and retrying with exponential backoff when both are unavailable. The chisel client and log shipper become data-driven (no hardcoded forward tunnels or Loki URL). The panel gates its first launch on a Walk dialog that collects credentials and verifies them live before patching the YAML config.

**Tech Stack:** Go, `gopkg.in/yaml.v3` (Node API for comment-preserving patches), `lxn/walk` for the Windows dialog, `httptest` for endpoint tests, no new external dependencies.

---

## File Structure

**New files:**
- `internal/labbridge/serverinfo.go` — `FetchServerInfo`, `ServerInfo`, `ForwardTunnel`.
- `internal/labbridge/serverinfo_test.go` — endpoint tests.
- `internal/bootstrap/cache.go` — `Cache` struct, `ReadCache`, `WriteCache`, atomic write.
- `internal/bootstrap/cache_test.go` — cache I/O tests.
- `internal/bootstrap/bootstrap.go` — `Resolved`, `Resolve`, retry loop.
- `internal/bootstrap/bootstrap_test.go` — resolver tests.
- `internal/panel/firstrun.go` — cross-platform first-run helpers (`decideFirstRun`, `verifyCredentials`, `patchCredentials`, `writeOrPatchCreds`).
- `internal/panel/firstrun_test.go` — helper tests.
- `internal/panel/credsdialog_windows.go` — Walk dialog (build tag `windows`).
- `internal/panel/credsdialog_other.go` — stub for non-windows (build tag `!windows`).
- `internal/config/testdata/scaffold.golden.yaml` — golden scaffold for regression test.

**Modified files:**
- `internal/paths/paths.go` — add `ServerInfoCachePath` + filename const.
- `internal/paths/paths_test.go` — test for the new path.
- `internal/config/config.go` — drop `ChiselConfig`; clear `LabBridge.User` default; new scaffold template.
- `internal/config/load.go` — require `lab_bridge.user`/`pass` non-empty; drop chisel validation.
- `internal/config/config_test.go` — golden snapshot; update default-config expectations.
- `internal/config/load_test.go` — update fixture bodies; new user/pass cases.
- `internal/labbridge/client.go` — no signature changes (existing `FetchClient` already does what we need); `maxBodyBytes` reused.
- `internal/chisel/client.go` — `ForwardTunnels []labbridge.ForwardTunnel` field; data-driven `buildRemotes`.
- `internal/chisel/client_test.go` — update existing cases.
- `internal/logship/logship.go` — drop `defaultPushURL`; add exported `SetPushURL`; guard `StartShipper`; remove `setPushURLForTest`.
- `internal/logship/logship_test.go` — switch callers to `SetPushURL`; add empty-pushURL noop test.
- `internal/app/app.go` — `Run` signature gains `bootstrap.Resolved`.
- `internal/winsvc/worker.go` — call `bootstrap.Resolve`, set push URL, pass `Resolved` to `app.Run`.
- `cmd/serialhop/main.go` — foreground path also calls `bootstrap.Resolve`.
- `internal/panel/panel.go` — first-run gate replaces `ensureScaffold`; config display reads from cache.
- `README.md` — note the first-run dialog under the install steps.

---

## Implementation Order Rationale

Implementation proceeds in five phases, ordered so each commit is buildable and tests stay green:

- **Phase A** (Tasks 1–6): purely additive — new packages, new types, new methods. Existing call sites untouched.
- **Phase B** (Tasks 7–10): switchover. `app.Run` gains its `Resolved` parameter; chisel and logship lose their hardcoded fallbacks; `winsvc/worker.go` and `cmd/serialhop/main.go` wire bootstrap in. Each task in Phase B leaves the tree green.
- **Phase C** (Task 11): drop `ChiselConfig` from the schema. Safe because Phase B already removed all `cfg.Chisel.*` references.
- **Phase D** (Tasks 12–17): panel first-run UX.
- **Phase E** (Task 18): README.

---

## Task 1: `paths.ServerInfoCachePath`

**Files:**
- Modify: `internal/paths/paths.go`
- Test: `internal/paths/paths_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/paths/paths_test.go`:

```go
func TestServerInfoCachePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	want := filepath.Join(dir, "server-info.cache.json")
	if got := ServerInfoCachePath(); got != want {
		t.Errorf("ServerInfoCachePath: got %q, want %q", got, want)
	}
}

func TestServerInfoCachePath_EmptyWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := ServerInfoCachePath(); got != "" {
		t.Errorf("ServerInfoCachePath: got %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -run TestServerInfoCachePath -v`
Expected: FAIL with "ServerInfoCachePath undefined".

- [ ] **Step 3: Implement**

In `internal/paths/paths.go`, add the constant alongside the existing `*FileName` block:

```go
const (
	ConfigFileName          = "SerialHop_config.yaml"
	ServiceLogFileName      = "SerialHop.log"
	StderrLogFileName       = "SerialHop_stderr.log"
	PanelErrorLogFileName   = "SerialHop_panel_error.log"
	ServerInfoCacheFileName = "server-info.cache.json"
)
```

Add the getter beneath `PanelErrorLogPath`:

```go
// ServerInfoCachePath returns <DataDir>/server-info.cache.json, or ""
// if DataDir is empty.
func ServerInfoCachePath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ServerInfoCacheFileName)
}
```

- [ ] **Step 4: Verify passes**

Run: `go test ./internal/paths/ -run TestServerInfoCachePath -v`
Expected: PASS for both cases.

- [ ] **Step 5: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat(paths): add ServerInfoCachePath for the bootstrap cache file"
```

---

## Task 2: `labbridge.FetchServerInfo` — types and parse contract

**Files:**
- Create: `internal/labbridge/serverinfo.go`
- Create: `internal/labbridge/serverinfo_test.go`

- [ ] **Step 1: Write failing tests for happy path + forward-compat**

Create `internal/labbridge/serverinfo_test.go`:

```go
package labbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchServerInfo_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/server-info" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("server-info must not send Authorization header")
		}
		_, _ = w.Write([]byte(`{
			"chisel": {"listen_port": 7000},
			"loki":   {"push_url": "http://127.0.0.1:3100/loki/api/v1/push"},
			"forward_tunnels": [
				{"name": "loki", "local": "127.0.0.1:3100", "remote": "loki:3100"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchServerInfo: %v", err)
	}
	if got.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d, want 7000", got.ChiselListenPort)
	}
	if got.LokiPushURL != "http://127.0.0.1:3100/loki/api/v1/push" {
		t.Errorf("LokiPushURL: got %q", got.LokiPushURL)
	}
	if len(got.ForwardTunnels) != 1 {
		t.Fatalf("ForwardTunnels: got %d, want 1", len(got.ForwardTunnels))
	}
	ft := got.ForwardTunnels[0]
	if ft.Name != "loki" || ft.Local != "127.0.0.1:3100" || ft.Remote != "loki:3100" {
		t.Errorf("forward tunnel: got %+v", ft)
	}
}

func TestFetchServerInfo_IgnoresUnknownKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"chisel": {"listen_port": 7000, "fingerprint": "abc123"},
			"loki":   {"push_url": "http://x/loki"},
			"forward_tunnels": [],
			"agent":  {"version": "1.2.3", "sha256": "deadbeef"}
		}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchServerInfo: %v", err)
	}
	if got.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d, want 7000", got.ChiselListenPort)
	}
	if len(got.ForwardTunnels) != 0 {
		t.Errorf("ForwardTunnels: got %d, want 0", len(got.ForwardTunnels))
	}
}

func TestFetchServerInfo_NullForwardTunnels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"chisel": {"listen_port": 7000},
			"loki":   {"push_url": "http://x"},
			"forward_tunnels": null
		}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchServerInfo: %v", err)
	}
	if got.ForwardTunnels != nil && len(got.ForwardTunnels) != 0 {
		t.Errorf("ForwardTunnels: got %v, want empty/nil", got.ForwardTunnels)
	}
}

func TestFetchServerInfo_RejectsListenPortOutOfRange(t *testing.T) {
	for _, body := range []string{
		`{"chisel":{"listen_port":0},"loki":{"push_url":"http://x"}}`,
		`{"chisel":{"listen_port":70000},"loki":{"push_url":"http://x"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "chisel.listen_port") {
			t.Errorf("body %q: want chisel.listen_port error, got %v", body, err)
		}
	}
}

func TestFetchServerInfo_RejectsEmptyLokiPushURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chisel":{"listen_port":7000},"loki":{"push_url":""}}`))
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "loki.push_url") {
		t.Errorf("want loki.push_url error, got %v", err)
	}
}

func TestFetchServerInfo_RejectsEmptyForwardTunnelEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"chisel":{"listen_port":7000},
			"loki":{"push_url":"http://x"},
			"forward_tunnels":[{"name":"loki","local":"","remote":"loki:3100"}]
		}`))
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "forward_tunnels") {
		t.Errorf("want forward_tunnels error, got %v", err)
	}
}

func TestFetchServerInfo_5xxWrapsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("want ErrServerError, got %v", err)
	}
}

func TestFetchServerInfo_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "parse server-info body") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestFetchServerInfo_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := FetchServerInfo(ctx, srv.Client(), srv.URL, testUserAgent)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/labbridge/ -run TestFetchServerInfo -v`
Expected: FAIL — `FetchServerInfo undefined`, `ServerInfo undefined`, `ForwardTunnel undefined`.

- [ ] **Step 3: Implement**

Create `internal/labbridge/serverinfo.go`:

```go
package labbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const serverInfoPath = "/api/public/server-info"

// ForwardTunnel describes one chisel -L forward the agent should open.
type ForwardTunnel struct {
	Name   string
	Local  string
	Remote string
}

// ServerInfo is the parsed result of GET /api/public/server-info.
// Unknown fields in the response are silently ignored to allow the
// server to add new keys (e.g. agent metadata, chisel fingerprint)
// without breaking older agents.
type ServerInfo struct {
	ChiselListenPort int
	LokiPushURL      string
	ForwardTunnels   []ForwardTunnel
}

type serverInfoBody struct {
	Chisel struct {
		ListenPort int `json:"listen_port"`
	} `json:"chisel"`
	Loki struct {
		PushURL string `json:"push_url"`
	} `json:"loki"`
	ForwardTunnels []struct {
		Name   string `json:"name"`
		Local  string `json:"local"`
		Remote string `json:"remote"`
	} `json:"forward_tunnels"`
}

// FetchServerInfo retrieves the agent-bootstrap parameters from the
// lab-bridge VPS. No Authorization header is sent.
//
// Returns wrapped ErrServerError on HTTP 5xx; plain error for transport,
// parse, validation, or unexpected status. Unknown response fields are
// silently ignored (forward-compat).
func FetchServerInfo(ctx context.Context, hc *http.Client, base, userAgent string) (ServerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+serverInfoPath, nil)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: build server-info request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: do server-info: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 500 {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: %w (status %d)", ErrServerError, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: read server-info body: %w", err)
	}
	var b serverInfoBody
	if err := json.Unmarshal(body, &b); err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: parse server-info body: %w", err)
	}

	if b.Chisel.ListenPort < 1 || b.Chisel.ListenPort > 65535 {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: chisel.listen_port out of range (got %d)", b.Chisel.ListenPort)
	}
	if b.Loki.PushURL == "" {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: loki.push_url is empty")
	}

	tunnels := make([]ForwardTunnel, 0, len(b.ForwardTunnels))
	for i, t := range b.ForwardTunnels {
		if t.Local == "" || t.Remote == "" {
			return ServerInfo{}, fmt.Errorf("labbridge: server-info: forward_tunnels[%d] has empty local or remote", i)
		}
		tunnels = append(tunnels, ForwardTunnel{Name: t.Name, Local: t.Local, Remote: t.Remote})
	}

	return ServerInfo{
		ChiselListenPort: b.Chisel.ListenPort,
		LokiPushURL:      b.Loki.PushURL,
		ForwardTunnels:   tunnels,
	}, nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/labbridge/ -v`
Expected: PASS for all new tests; existing health/client tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/labbridge/serverinfo.go internal/labbridge/serverinfo_test.go
git commit -m "feat(labbridge): add FetchServerInfo for the agent-bootstrap endpoint"
```

---

## Task 3: `bootstrap` package — cache file (read/write)

**Files:**
- Create: `internal/bootstrap/cache.go`
- Create: `internal/bootstrap/cache_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/bootstrap/cache_test.go`:

```go
package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func sampleCache() Cache {
	return Cache{
		Version:   cacheCurrentVersion,
		FetchedAt: "2026-05-11T14:32:01Z",
		User:      "alice",
		ServerInfo: labbridge.ServerInfo{
			ChiselListenPort: 7000,
			LokiPushURL:      "http://127.0.0.1:3100/loki/api/v1/push",
			ForwardTunnels: []labbridge.ForwardTunnel{
				{Name: "loki", Local: "127.0.0.1:3100", Remote: "loki:3100"},
			},
		},
		RemotePort: 8089,
	}
}

func TestWriteCache_AndReadCache_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.RemotePort != in.RemotePort || got.ServerInfo.ChiselListenPort != in.ServerInfo.ChiselListenPort {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, in)
	}
}

func TestReadCache_MissingFileReturnsErrCacheMissing(t *testing.T) {
	_, err := ReadCache(filepath.Join(t.TempDir(), "nope.json"), "alice")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing, got %v", err)
	}
}

func TestReadCache_UserMismatchTreatedAsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(p, sampleCache()); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	_, err := ReadCache(p, "bob")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on user mismatch, got %v", err)
	}
}

func TestReadCache_CorruptJSONDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCache(p, "alice")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on corrupt JSON, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected corrupt cache file to be deleted; stat err = %v", statErr)
	}
}

func TestReadCache_VersionMismatchDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte(`{"version":99,"user":"alice"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCache(p, "alice")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on version mismatch, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected version-mismatch cache file to be deleted; stat err = %v", statErr)
	}
}

func TestWriteCache_IsAtomic(t *testing.T) {
	// WriteCache must not leave a tmp file behind on success.
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	if err := WriteCache(p, sampleCache()); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "cache.json" {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/bootstrap/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Create `internal/bootstrap/cache.go`:

```go
// Package bootstrap resolves the agent's per-startup parameters
// (chisel listen port, Loki push URL, forward tunnels, reverse-tunnel
// port) by calling the lab-bridge HTTPS API at startup. Results are
// cached on disk in a user-anchored JSON file so the service can come
// back up after a restart even if the server is briefly unreachable.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

const cacheCurrentVersion = 1

// ErrCacheMissing is returned by ReadCache when the cache file is
// absent, unparseable, version-mismatched, or anchored to a different
// user. Callers should treat all of these the same: fall back to a
// live fetch.
var ErrCacheMissing = errors.New("bootstrap: cache missing")

// Cache is the on-disk schema for server-info.cache.json. The User
// field anchors the cache to a specific identity so that changing
// lab_bridge.user in the YAML invalidates stale data automatically.
type Cache struct {
	Version    int                  `json:"version"`
	FetchedAt  string               `json:"fetched_at"`
	User       string               `json:"user"`
	ServerInfo labbridge.ServerInfo `json:"server_info"`
	RemotePort int                  `json:"remote_port"`
}

// WriteCache atomically writes c to path. Any existing file at path is
// replaced. Permissions are 0600.
func WriteCache(path string, c Cache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("bootstrap: marshal cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "server-info.cache.json.*.tmp")
	if err != nil {
		return fmt.Errorf("bootstrap: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: rename temp: %w", err)
	}
	return nil
}

// ReadCache reads the cache file at path and returns it if it is valid
// and anchored to user. Any failure (missing file, parse error, version
// mismatch, user mismatch) returns ErrCacheMissing; corrupt or
// version-mismatched files are deleted as a side effect so the next
// successful WriteCache starts from a clean slate.
func ReadCache(path, user string) (Cache, error) {
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
	if c.User != user {
		slog.Info("bootstrap: cache user mismatch; ignoring", "cache_user", c.User, "cfg_user", user)
		return Cache{}, ErrCacheMissing
	}
	return c, nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/bootstrap/ -v`
Expected: PASS for all cache tests.

- [ ] **Step 5: Commit**

```bash
git add internal/bootstrap/cache.go internal/bootstrap/cache_test.go
git commit -m "feat(bootstrap): add user-anchored cache file with atomic writes"
```

---

## Task 4: `bootstrap.Resolve` — orchestration + retry loop

**Files:**
- Create: `internal/bootstrap/bootstrap.go`
- Modify: `internal/bootstrap/bootstrap_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/bootstrap/bootstrap_test.go`:

```go
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func newServer(t *testing.T, serverInfoHandler, clientHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/public/server-info", serverInfoHandler)
	mux.HandleFunc("/api/public/clients/", clientHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func okServerInfo(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{
		"chisel":{"listen_port":7000},
		"loki":{"push_url":"http://127.0.0.1:3100/loki/api/v1/push"},
		"forward_tunnels":[{"name":"loki","local":"127.0.0.1:3100","remote":"loki:3100"}]
	}`))
}

func okClient(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
}

func TestResolve_LiveSuccess_WritesCacheAndReturnsLive(t *testing.T) {
	srv := newServer(t, okServerInfo, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	opts := Options{
		HTTPClient: srv.Client(),
		Base:       srv.URL,
		User:       "alice",
		Pass:       "s3cret",
		CachePath:  cachePath,
		UserAgent:  "test/1",
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ServerInfo.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d", got.ServerInfo.ChiselListenPort)
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort: got %d", got.RemotePort)
	}
	c, err := ReadCache(cachePath, "alice")
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if c.User != "alice" {
		t.Errorf("cache user: got %q, want alice", c.User)
	}
}

func TestResolve_5xxThenCache_ReturnsCache(t *testing.T) {
	srv := newServer(t,
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
		okClient,
	)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(cachePath, sampleCache()); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "alice", Pass: "p",
		CachePath: cachePath, UserAgent: "test/1",
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.RemotePort != 8089 {
		t.Errorf("expected cached RemotePort 8089, got %d", got.RemotePort)
	}
}

func TestResolve_401_BypassesCacheAndRetries(t *testing.T) {
	// Even with a valid cache present, a live 401 must force the retry
	// loop — never serve cached values when creds are demonstrably wrong.
	var clientCalls atomic.Int32
	srv := newServer(t, okServerInfo, func(w http.ResponseWriter, _ *http.Request) {
		n := clientCalls.Add(1)
		if n < 3 {
			http.Error(w, "unauthorized", 401)
			return
		}
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	})
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(cachePath, sampleCache()); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	opts := Options{
		HTTPClient:     srv.Client(),
		Base:           srv.URL,
		User:           "alice",
		Pass:           "p",
		CachePath:      cachePath,
		UserAgent:      "test/1",
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if clientCalls.Load() < 3 {
		t.Errorf("expected at least 3 client calls (retry past 401), got %d", clientCalls.Load())
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort: got %d", got.RemotePort)
	}
}

func TestResolve_NoCache_RetriesUntilSuccess(t *testing.T) {
	var serverInfoCalls atomic.Int32
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := serverInfoCalls.Add(1)
		if n < 2 {
			http.Error(w, "boom", 500)
			return
		}
		okServerInfo(w, nil)
	}, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	opts := Options{
		HTTPClient:     srv.Client(),
		Base:           srv.URL,
		User:           "alice",
		Pass:           "p",
		CachePath:      cachePath,
		UserAgent:      "test/1",
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ServerInfo.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d", got.ServerInfo.ChiselListenPort)
	}
	if serverInfoCalls.Load() < 2 {
		t.Errorf("expected at least 2 server-info calls, got %d", serverInfoCalls.Load())
	}
}

func TestResolve_CtxCancelledNoCache_ReturnsCtxErr(t *testing.T) {
	srv := newServer(t,
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
	)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	opts := Options{
		HTTPClient:     srv.Client(),
		Base:           srv.URL,
		User:           "alice",
		Pass:           "p",
		CachePath:      cachePath,
		UserAgent:      "test/1",
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}
	_, err := Resolve(ctx, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestResolve_LiveSuccessCarriesCorrectUserInCache(t *testing.T) {
	srv := newServer(t, okServerInfo, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "bob", Pass: "p",
		CachePath: cachePath, UserAgent: "test/1",
	}
	if _, err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Reading with the wrong user must yield ErrCacheMissing.
	if _, err := ReadCache(cachePath, "alice"); !errors.Is(err, ErrCacheMissing) {
		t.Errorf("user-mismatch read: expected ErrCacheMissing, got %v", err)
	}
	if _, err := ReadCache(cachePath, "bob"); err != nil {
		t.Errorf("matching user read: got %v", err)
	}
}

// Compile-time check that Resolved exposes the expected fields.
var _ = func() Resolved {
	return Resolved{
		ServerInfo: labbridge.ServerInfo{},
		RemotePort: 0,
	}
}()

func TestResolve_VerifiesBearerHeaderOnClientCall(t *testing.T) {
	var gotAuth string
	srv := newServer(t, okServerInfo, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	})
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "u", Pass: "s3cret",
		CachePath: cachePath, UserAgent: "test/1",
	}
	if _, err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer s3cret")
	}
}

func TestResolve_FetchTimeoutHonored(t *testing.T) {
	// If FetchTimeout is short and the server is slow, Resolve must fall
	// through to the retry path; we then race the test against retries.
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		okServerInfo(w, nil)
	}, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "u", Pass: "p",
		CachePath: cachePath, UserAgent: "test/1",
		FetchTimeout:   10 * time.Millisecond,
		InitialBackoff: 1 * time.Millisecond, MaxBackoff: 2 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := Resolve(ctx, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

// Helper used only here.
var _ = fmt.Sprintf
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/bootstrap/ -run TestResolve -v`
Expected: FAIL — `Resolve undefined`, `Options undefined`, `Resolved undefined`.

- [ ] **Step 3: Implement**

Create `internal/bootstrap/bootstrap.go`:

```go
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// Resolved is the fully-resolved set of parameters the agent needs at
// startup that cannot be derived from local config alone.
type Resolved struct {
	ServerInfo labbridge.ServerInfo
	RemotePort int
}

// Options carries everything Resolve needs. The retry parameters have
// sensible defaults; tests pass tiny values to keep them fast.
type Options struct {
	HTTPClient *http.Client
	Base       string // "https://<host>" (or http://, for tests)
	User       string
	Pass       string
	CachePath  string
	UserAgent  string

	// Timeouts. Zero values trigger production defaults:
	//   FetchTimeout   = 5s   (per parallel fetch attempt)
	//   InitialBackoff = 1s
	//   MaxBackoff     = 1m
	FetchTimeout   time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func (o Options) fetchTimeout() time.Duration {
	if o.FetchTimeout > 0 {
		return o.FetchTimeout
	}
	return 5 * time.Second
}

func (o Options) initialBackoff() time.Duration {
	if o.InitialBackoff > 0 {
		return o.InitialBackoff
	}
	return 1 * time.Second
}

func (o Options) maxBackoff() time.Duration {
	if o.MaxBackoff > 0 {
		return o.MaxBackoff
	}
	return 1 * time.Minute
}

// Resolve performs one or more parallel attempts to fetch /server-info
// and /clients/{user}, falling back to a user-anchored disk cache and
// then to an exponential-backoff retry loop. See the design doc for
// the full algorithm.
func Resolve(ctx context.Context, opts Options) (Resolved, error) {
	backoff := opts.initialBackoff()
	maxBackoff := opts.maxBackoff()
	for {
		res, sawUnauthorized, err := tryLive(ctx, opts)
		if err == nil {
			if writeErr := WriteCache(opts.CachePath, cacheFromResolved(res, opts.User)); writeErr != nil {
				slog.Warn("bootstrap: write cache failed", "err", writeErr)
			}
			return res, nil
		}
		if ctx.Err() != nil {
			return Resolved{}, ctx.Err()
		}
		slog.Warn("bootstrap: live fetch failed", "err", err, "unauthorized", sawUnauthorized)

		if !sawUnauthorized {
			if c, cacheErr := ReadCache(opts.CachePath, opts.User); cacheErr == nil {
				slog.Warn("bootstrap: serving cached server-info while live is unavailable",
					"cache_user", c.User, "fetched_at", c.FetchedAt)
				return Resolved{ServerInfo: c.ServerInfo, RemotePort: c.RemotePort}, nil
			}
		}

		select {
		case <-ctx.Done():
			return Resolved{}, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// tryLive does one parallel pair of fetches. Returns (resolved, sawUnauthorized, error).
// sawUnauthorized is true iff FetchClient returned ErrUnauthorized;
// callers use it to decide whether to consult the cache.
func tryLive(ctx context.Context, opts Options) (Resolved, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, opts.fetchTimeout())
	defer cancel()

	var (
		wg               sync.WaitGroup
		info             labbridge.ServerInfo
		infoErr          error
		client           labbridge.ClientInfo
		clientErr        error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		info, infoErr = labbridge.FetchServerInfo(cctx, opts.HTTPClient, opts.Base, opts.UserAgent)
	}()
	go func() {
		defer wg.Done()
		client, clientErr = labbridge.FetchClient(cctx, opts.HTTPClient, opts.Base, opts.User, opts.Pass, opts.UserAgent)
	}()
	wg.Wait()

	sawUnauthorized := errors.Is(clientErr, labbridge.ErrUnauthorized)

	if infoErr != nil {
		return Resolved{}, sawUnauthorized, fmt.Errorf("bootstrap: server-info: %w", infoErr)
	}
	if clientErr != nil {
		return Resolved{}, sawUnauthorized, fmt.Errorf("bootstrap: clients: %w", clientErr)
	}
	return Resolved{ServerInfo: info, RemotePort: client.Port}, false, nil
}

func cacheFromResolved(r Resolved, user string) Cache {
	return Cache{
		Version:    cacheCurrentVersion,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		User:       user,
		ServerInfo: r.ServerInfo,
		RemotePort: r.RemotePort,
	}
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/bootstrap/ -v`
Expected: PASS for all bootstrap tests.

- [ ] **Step 5: Commit**

```bash
git add internal/bootstrap/bootstrap.go internal/bootstrap/bootstrap_test.go
git commit -m "feat(bootstrap): add Resolve with cache fallback and 401-bypass retry"
```

---

## Task 5: `logship.SetPushURL` exported method

The existing `setPushURLForTest` is private. We export it as `SetPushURL` (used by the worker before `StartShipper`), keep `defaultPushURL` for now so `Init` doesn't change behavior, and add a guard so calling `StartShipper` with an empty push URL is a logged noop. We'll delete `defaultPushURL` in Task 8.

**Files:**
- Modify: `internal/logship/logship.go`
- Modify: `internal/logship/logship_test.go`

- [ ] **Step 1: Add failing test for empty-URL noop**

Append to `internal/logship/logship_test.go`:

```go
func TestManagerStartShipperWithEmptyPushURLIsNoOp(t *testing.T) {
	setupTestEnv(t)
	m, err := Init("1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.SetPushURL("") // explicit empty
	m.StartShipper("lab-1")
	if got := m.shipperCountForTest(); got != 0 {
		t.Fatalf("shipper started with empty push URL; count = %d", got)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/logship/ -run TestManagerStartShipperWithEmptyPushURL -v`
Expected: FAIL — `SetPushURL undefined`.

- [ ] **Step 3: Implement**

In `internal/logship/logship.go`, add the exported method and the guard. Find:

```go
// StartShipper starts the shipper goroutine if clientLabel is non-empty
// and no shipper is already running. Idempotent.
func (m *Manager) StartShipper(clientLabel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shipCtx != nil {
		return // already started
	}
	if clientLabel == "" {
		slog.Warn("log streaming disabled (no chisel user)")
		return
	}
```

Replace the `if clientLabel == ""` block with the user-and-URL guard:

```go
	if clientLabel == "" {
		slog.Warn("log streaming disabled (no chisel user)")
		return
	}
	if m.pushURL == "" {
		slog.Warn("log streaming disabled (no push URL — SetPushURL not called?)")
		return
	}
```

Replace the private `setPushURLForTest` block:

```go
// --- test-only helpers (lower-cased; only callable from logship_test.go) ---

func (m *Manager) setPushURLForTest(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushURL = url
}
```

with the exported method (test-only `shipperCountForTest` stays):

```go
// SetPushURL sets the Loki push URL. Must be called before StartShipper.
// Safe to call again before StartShipper to change the URL; calling
// after StartShipper has no effect on the running shipper.
func (m *Manager) SetPushURL(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushURL = url
}

// --- test-only helpers (lower-cased; only callable from logship_test.go) ---

func (m *Manager) shipperCountForTest() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shipperC
}
```

In `internal/logship/logship_test.go`, replace the two existing callers of `m.setPushURLForTest(srv.URL)` with `m.SetPushURL(srv.URL)`.

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/logship/ -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/logship.go internal/logship/logship_test.go
git commit -m "feat(logship): export SetPushURL and guard StartShipper when URL is empty"
```

---

## Task 6: `chisel.Config.ForwardTunnels` (additive — old behavior preserved)

We add the field and a code path that uses it, while keeping the current `if cfg.User != ""` hardcoded loki line as a fallback. The fallback goes away in Task 8.

**Files:**
- Modify: `internal/chisel/client.go`
- Modify: `internal/chisel/client_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/chisel/client_test.go`:

```go
func TestRemotesAppendsForwardTunnels(t *testing.T) {
	got := buildRemotes(Config{
		User: "", RemotePort: 8081, LocalPort: 5000,
		ForwardTunnels: []labbridge.ForwardTunnel{
			{Name: "loki", Local: "127.0.0.1:3100", Remote: "loki:3100"},
			{Name: "graf", Local: "127.0.0.1:3000", Remote: "grafana:3000"},
		},
	})
	want := []string{
		"R:8081:127.0.0.1:5000",
		"127.0.0.1:3100:loki:3100",
		"127.0.0.1:3000:grafana:3000",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}
```

You'll also need to add the import at the top of `client_test.go`:

```go
import (
	"slices"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/chisel/ -run TestRemotesAppendsForwardTunnels -v`
Expected: FAIL — `ForwardTunnels` field undefined.

- [ ] **Step 3: Implement**

In `internal/chisel/client.go`, update `Config` and `buildRemotes`:

```go
import (
	// existing imports unchanged
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// Config is the subset of chisel client options this app exposes.
type Config struct {
	Server         string
	User           string
	Pass           string
	RemotePort     int
	LocalPort      int
	ForwardTunnels []labbridge.ForwardTunnel
}

// buildRemotes returns the list of chisel route strings for cfg. The
// reverse route exposes the local REST server; each ForwardTunnel is
// rendered as <local>:<remote>. The legacy "127.0.0.1:3100:loki:3100"
// fallback (gated on cfg.User != "") is kept so call sites that have
// not yet been migrated to a populated ForwardTunnels list still work;
// it will be removed once all callers pass ForwardTunnels explicitly.
func buildRemotes(cfg Config) []string {
	out := []string{fmt.Sprintf("R:%d:127.0.0.1:%d", cfg.RemotePort, cfg.LocalPort)}
	for _, t := range cfg.ForwardTunnels {
		out = append(out, fmt.Sprintf("%s:%s", t.Local, t.Remote))
	}
	if len(cfg.ForwardTunnels) == 0 && cfg.User != "" {
		out = append(out, "127.0.0.1:3100:loki:3100")
	}
	return out
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/chisel/ -v`
Expected: PASS for the new test; existing `TestRemotesIncludesForwardWhenAuthSet` and `TestRemotesOmitsForwardWhenNoAuth` still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/chisel/client.go internal/chisel/client_test.go
git commit -m "feat(chisel): add ForwardTunnels field (legacy hardcoded fallback retained)"
```

---

## Task 7: `app.Run` takes `bootstrap.Resolved`

This task is the central switchover. `Run` keeps reading `cfg.LabBridge.*` and `cfg.Rest.Port` from config, but the chisel server and remote-port values come from `resolved`. Both `winsvc/worker.go` and `cmd/serialhop/main.go` (foreground) must be updated in the same task to keep the tree green.

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/winsvc/worker.go`
- Modify: `cmd/serialhop/main.go`

- [ ] **Step 1: Update `app.Run` signature**

Edit `internal/app/app.go`. Change the import block to include `bootstrap` and update the function signature:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/chisel"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func Run(ctx context.Context, cfg config.Config, resolved bootstrap.Resolved) error {
```

Replace the chisel.Run call site:

```go
	chiselDone := make(chan error, 1)
	go func() {
		chiselDone <- chisel.Run(ctx, chisel.Config{
			Server:         net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(resolved.ServerInfo.ChiselListenPort)),
			User:           cfg.LabBridge.User,
			Pass:           cfg.LabBridge.Pass,
			RemotePort:     resolved.RemotePort,
			LocalPort:      localPort,
			ForwardTunnels: resolved.ServerInfo.ForwardTunnels,
		})
	}()
```

Update the startup log block to use the resolved values:

```go
	slog.Info("serialhop starting",
		"chisel_host", cfg.LabBridge.Host,
		"chisel_port", resolved.ServerInfo.ChiselListenPort,
		"remote_port", resolved.RemotePort,
		"rest_port", cfg.Rest.Port,
		"discovery_include", cfg.Discovery.Include,
		"discovery_exclude", cfg.Discovery.Exclude,
		"discovery_post_open_settle_ms", cfg.Discovery.PostOpenSettleMs,
		"forward_tunnels", len(resolved.ServerInfo.ForwardTunnels),
	)
```

- [ ] **Step 2: Update `winsvc/worker.go` to call bootstrap (without blocking SCM startup)**

In `internal/winsvc/worker.go`, the worker must transition to `svc.Running` immediately so Windows SCM does not time out (default ~30 s) when the lab-bridge server is unreachable on first launch. The bootstrap call runs inside the same goroutine that owns app.Run, sharing `ctx` so Stop requests cancel both.

Update the imports:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/app"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/logship"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"

	"golang.org/x/sys/windows/svc"
)
```

Replace the `Execute` body's middle section. The original:

```go
	h.manager.SetLevel(logship.ParseLogLevel(cfg.Log.Level))
	h.manager.StartShipper(cfg.LabBridge.User)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appDone := make(chan error, 1)
	go func() {
		appDone <- app.Run(ctx, cfg)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}
```

becomes:

```go
	h.manager.SetLevel(logship.ParseLogLevel(cfg.Log.Level))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Report Running before bootstrap so the SCM keeps the service alive
	// even when the lab-bridge server is unreachable on first launch.
	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	appDone := make(chan error, 1)
	go func() {
		hc := &http.Client{Timeout: 30 * time.Second}
		userAgent := "SerialHop/" + version.Base() + " (bootstrap)"
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

The rest of the function (the `for { select { ... } }` loop reading `r` and `appDone`) stays unchanged: Stop / Shutdown still cancels `ctx`, which interrupts both `bootstrap.Resolve` and `app.Run`.

- [ ] **Step 3: Update `cmd/serialhop/main.go` foreground path**

In `cmd/serialhop/main.go`, update `runForeground`. After `cfg, err := config.Load(cfgPath)` succeeds, do the bootstrap step. The imports need `bootstrap`, `net/http`, and `time` (already imported).

```go
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	configureStdoutLogger(cfg.Log.Level)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	hc := &http.Client{Timeout: 30 * time.Second}
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

Add the imports near the top:

```go
import (
	// ...existing imports...
	"net/http"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)
```

- [ ] **Step 4: Verify the tree compiles and tests still pass**

Run: `go build ./... && go test ./internal/app/... ./internal/winsvc/... ./internal/bootstrap/... ./internal/chisel/... ./internal/logship/...`
Expected: builds clean, tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/winsvc/worker.go cmd/serialhop/main.go
git commit -m "feat(app): wire bootstrap.Resolve into service and foreground entrypoints"
```

---

## Task 8: Remove logship `defaultPushURL` and chisel legacy fallback

Now that all live entrypoints set the push URL and pass `ForwardTunnels`, the legacy code paths are dead. Remove them.

**Files:**
- Modify: `internal/logship/logship.go`
- Modify: `internal/chisel/client.go`
- Modify: `internal/chisel/client_test.go`

- [ ] **Step 1: Remove `defaultPushURL`**

In `internal/logship/logship.go`, delete the `defaultPushURL` constant block:

```go
// defaultPushURL is the local end of the chisel forward tunnel that
// reaches the in-VPS Loki.
const defaultPushURL = "http://127.0.0.1:3100/loki/api/v1/push"
```

And in `Init`, remove `pushURL: defaultPushURL,` from the struct literal so `pushURL` starts empty.

- [ ] **Step 2: Remove chisel legacy fallback**

In `internal/chisel/client.go`, update `buildRemotes` to drop the `len(cfg.ForwardTunnels) == 0 && cfg.User != ""` branch:

```go
// buildRemotes returns the list of chisel route strings for cfg. The
// reverse route exposes the local REST server; each ForwardTunnel is
// rendered as <local>:<remote>.
func buildRemotes(cfg Config) []string {
	out := []string{fmt.Sprintf("R:%d:127.0.0.1:%d", cfg.RemotePort, cfg.LocalPort)}
	for _, t := range cfg.ForwardTunnels {
		out = append(out, fmt.Sprintf("%s:%s", t.Local, t.Remote))
	}
	return out
}
```

- [ ] **Step 3: Update chisel tests to match**

In `internal/chisel/client_test.go`, the two existing legacy-fallback tests need to be updated:

Replace `TestRemotesIncludesForwardWhenAuthSet` with:

```go
func TestRemotesIncludesAllForwardTunnels(t *testing.T) {
	got := buildRemotes(Config{
		User: "lab-1", RemotePort: 8081, LocalPort: 5000,
		ForwardTunnels: []labbridge.ForwardTunnel{
			{Name: "loki", Local: "127.0.0.1:3100", Remote: "loki:3100"},
		},
	})
	want := []string{
		"R:8081:127.0.0.1:5000",
		"127.0.0.1:3100:loki:3100",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}
```

Replace `TestRemotesOmitsForwardWhenNoAuth` with:

```go
func TestRemotesEmptyForwardTunnelsOnlyReverseRoute(t *testing.T) {
	got := buildRemotes(Config{User: "", RemotePort: 8081, LocalPort: 5000})
	want := []string{"R:8081:127.0.0.1:5000"}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}
```

Update the doc comment in the file to remove the `if cfg.User != ""` branch description if applicable. The earlier `TestRemotesAppendsForwardTunnels` still exercises multi-tunnel ordering and stays as-is.

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/chisel/... ./internal/logship/...`
Expected: PASS.

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/logship.go internal/chisel/client.go internal/chisel/client_test.go
git commit -m "refactor: remove hardcoded Loki URL and legacy chisel forward fallback"
```

---

## Task 9: Update `chisel.Run` startup log to include forward-tunnel count

Tiny follow-up: the existing `slog.Info("chisel: starting", ...)` call still has `forward_loki` derived from `cfg.User != ""`. Replace with the actual list length.

**Files:**
- Modify: `internal/chisel/client.go`

- [ ] **Step 1: Update the log fields**

In `internal/chisel/client.go`, find:

```go
	slog.Info("chisel: starting",
		"server", cfg.Server,
		"remote_port", cfg.RemotePort,
		"local_port", cfg.LocalPort,
		"auth", cfg.User != "",
		"forward_loki", cfg.User != "")
```

Replace with:

```go
	slog.Info("chisel: starting",
		"server", cfg.Server,
		"remote_port", cfg.RemotePort,
		"local_port", cfg.LocalPort,
		"auth", cfg.User != "",
		"forward_tunnels", len(cfg.ForwardTunnels))
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/chisel/client.go
git commit -m "refactor(chisel): log forward-tunnel count instead of legacy forward_loki bool"
```

---

## Task 10: Drop `ChiselConfig` from the schema and require user/pass

With `cfg.Chisel.*` no longer referenced anywhere, we can delete the struct, drop the user default, and tighten validation. The scaffold template + tests change at the same time.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/load_test.go`
- Create: `internal/config/testdata/scaffold.golden.yaml`

- [ ] **Step 1: Write failing validation tests**

Replace the `TestValidate_Cases` body in `internal/config/config_test.go`... wait, that test lives in `load_test.go`. Open `internal/config/load_test.go` and replace `TestValidate_Cases` with:

```go
func TestValidate_Cases(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Config)
		wantErr string
	}{
		{"host empty", func(c *Config) { c.LabBridge.Host = "" }, "lab_bridge.host"},
		{"user empty", func(c *Config) { c.LabBridge.User = "" }, "lab_bridge.user"},
		{"pass empty", func(c *Config) { c.LabBridge.Pass = "" }, "lab_bridge.pass"},
		{"include+exclude both set", func(c *Config) {
			c.Discovery.Include = []string{"COM1"}
			c.Discovery.Exclude = []string{"COM2"}
		}, "mutually exclusive"},
		{"log.level invalid", func(c *Config) { c.Log.Level = "verbose" }, "log.level"},
		{"post_open_settle_ms negative", func(c *Config) {
			c.Discovery.PostOpenSettleMs = -1
		}, "post_open_settle_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			// Default() now has empty user/pass; populate before mutating so
			// only the field under test trips validation.
			c.LabBridge.User = "u"
			c.LabBridge.Pass = "p"
			tc.mut(&c)
			err := Validate(&c)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}
```

Replace `TestValidate_DefaultIsValid` (since the default is now invalid — empty user/pass) with:

```go
func TestValidate_DefaultIsInvalidUntilCredsSet(t *testing.T) {
	c := Default()
	if err := Validate(&c); err == nil {
		t.Fatalf("Default() should fail validation (empty user/pass)")
	}
}

func TestValidate_DefaultWithCredsIsValid(t *testing.T) {
	c := Default()
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	if err := Validate(&c); err != nil {
		t.Fatalf("Default()+creds should validate, got %v", err)
	}
}
```

Update every test body that currently has `chisel: {port: 7000, remote_port: 9000}` blocks to remove the `chisel:` section and ensure `user`/`pass` are set:

- `TestLoad_Success`: remove `chisel:` block; assert that `LabBridge.User` is `"u"`. Drop the `c.Chisel.Port` and `c.Chisel.RemotePort` assertions.
- `TestLoadPartial_Valid`: same — remove `chisel:`, drop the chisel assertion, keep `host`/`level`.
- `TestLoadPartial_InvalidValidationReturnsParsedFields`: change the body to `lab_bridge: { host: "", user: "u", pass: "p" }` so the host-empty check fires (drop chisel block and the `c.Chisel.RemotePort` assertion). Keep the `log.level` assertion.
- `TestLoad_PostOpenSettleCustom`, `TestLoad_RawSerialEnabled`, `TestLoad_AutoUpdateDisabled`, `TestLoad_AutoUpdateDefaultsToTrue`: add `user: "u"` + `pass: "p"` under `lab_bridge`; remove `chisel:` block.
- `TestLoad_OldSchemaIsRejected`: keep the test (old `chisel.server` etc. should still be ignored), but the assertion changes from "lab_bridge.host" to "lab_bridge.host" + verify the test still works with `host: ""`. No change to body otherwise.

Replace `TestDefaultConfig` in `internal/config/config_test.go` with:

```go
func TestDefaultConfig(t *testing.T) {
	c := Default()
	if c.LabBridge.Host != "111.88.145.138" {
		t.Errorf("lab_bridge.host: got %q, want %q", c.LabBridge.Host, "111.88.145.138")
	}
	if c.LabBridge.User != "" {
		t.Errorf("lab_bridge.user: got %q, want empty (no default identity)", c.LabBridge.User)
	}
	if c.LabBridge.Pass != "" {
		t.Errorf("lab_bridge.pass: got %q, want empty", c.LabBridge.Pass)
	}
	if c.Rest.Port != 0 {
		t.Errorf("rest.port: got %d, want 0", c.Rest.Port)
	}
	if c.Log.Level != "info" {
		t.Errorf("log.level: got %q, want info", c.Log.Level)
	}
}
```

Replace `TestWriteScaffold_RoundTrip` with a golden-file-based test:

```go
func TestWriteScaffold_GoldenSnapshot(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteScaffold(&buf); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	got := buf.String()

	wantPath := filepath.Join("testdata", "scaffold.golden.yaml")
	if want, err := os.ReadFile(wantPath); err == nil && string(want) == got {
		// match
	} else if err != nil {
		t.Fatalf("read golden: %v", err)
	} else {
		t.Errorf("scaffold output drifted from golden file %s.\nGOT:\n%s\nWANT:\n%s", wantPath, got, string(want))
	}

	// Sanity: scaffold must parse as YAML and round-trip into a usable
	// Config (after filling creds, since they default to "").
	var parsed Config
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("scaffold did not parse as YAML: %v\n%s", err, got)
	}
	parsed.LabBridge.User = "u"
	parsed.LabBridge.Pass = "p"
	if err := Validate(&parsed); err != nil {
		t.Errorf("scaffold + creds should validate, got %v", err)
	}
}
```

Add the imports `os`, `path/filepath` if not present in `config_test.go`.

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/config/... -v`
Expected: many FAILs — `Chisel` no longer exists on `Config`, missing fields, golden file missing.

- [ ] **Step 3: Update `Config`, `Default`, scaffold, validation**

In `internal/config/config.go`, delete the `ChiselConfig` struct, remove the `Chisel` field from `Config`, and update `Default` + the scaffold template:

```go
package config

import (
	"fmt"
	"io"
)

type Config struct {
	LabBridge  LabBridgeConfig  `yaml:"lab_bridge"`
	Rest       RestConfig       `yaml:"rest"`
	Discovery  DiscoveryConfig  `yaml:"discovery"`
	Log        LogConfig        `yaml:"log"`
	RawSerial  RawSerialConfig  `yaml:"raw_serial"`
	AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
}

type LabBridgeConfig struct {
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

type RestConfig struct {
	Port int `yaml:"port"`
}

type DiscoveryConfig struct {
	Include          []string `yaml:"include"`
	Exclude          []string `yaml:"exclude"`
	PostOpenSettleMs int      `yaml:"post_open_settle_ms"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type RawSerialConfig struct {
	Enabled bool `yaml:"enabled"`
}

type AutoUpdateConfig struct {
	Enabled bool `yaml:"enabled"`
}

func Default() Config {
	return Config{
		LabBridge: LabBridgeConfig{
			Host: "111.88.145.138",
			User: "",
			Pass: "",
		},
		Rest: RestConfig{Port: 0},
		Discovery: DiscoveryConfig{
			Include:          []string{},
			Exclude:          []string{},
			PostOpenSettleMs: 2000,
		},
		Log:        LogConfig{Level: "info"},
		RawSerial:  RawSerialConfig{Enabled: false},
		AutoUpdate: AutoUpdateConfig{Enabled: true},
	}
}

const scaffoldTemplate = `# SerialHop_config.yaml
# Auto-generated scaffold. Site values are filled in via the panel's
# first-run dialog (username + password). Other fields are optional —
# edit only if you need to change defaults.

lab_bridge:
  host: "111.88.145.138"   # lab-bridge VPS host (chisel + public HTTPS API).
                           # change only when pointing at a different deployment.
  user: ""                 # REQUIRED — chisel auth user; also Bearer-token
                           # identity for the public API. No default.
  pass: ""                 # REQUIRED — chisel password; also Bearer token
                           # for /api/public/clients/{user}. No default.

rest:
  port: 0                  # local REST port; 0 = OS picks a free one.

discovery:
  include: []              # optional: only probe these COM ports, e.g. ["COM3", "COM4"]
  exclude: []              # optional: skip these COM ports, e.g. ["COM1"]
  post_open_settle_ms: 2000  # wait after opening a port before probing. covers the
                             # Arduino auto-reset bootloader window (~1-2 s). lower
                             # if your boards don't reset on DTR; 0 to disable.

log:
  level: "info"            # debug | info | warn | error

raw_serial:
  enabled: false           # set true to allow GET /serial/ports and
                           # POST /serial/ports/{port}/command. bypasses
                           # device classification — leave off unless diagnosing.

auto_update:
  enabled: true            # check GitHub Releases for newer versions
                           # and offer to install them from the panel.
                           # set to false on air-gapped lab boxes.
`

func WriteScaffold(w io.Writer) error {
	if _, err := fmt.Fprint(w, scaffoldTemplate); err != nil {
		return fmt.Errorf("write scaffold: %w", err)
	}
	return nil
}
```

In `internal/config/load.go`, update `Validate`:

```go
func Validate(c *Config) error {
	if c.LabBridge.Host == "" {
		return fmt.Errorf("lab_bridge.host must be non-empty")
	}
	if c.LabBridge.User == "" {
		return fmt.Errorf("lab_bridge.user must be non-empty")
	}
	if c.LabBridge.Pass == "" {
		return fmt.Errorf("lab_bridge.pass must be non-empty")
	}
	if c.Rest.Port < 0 || c.Rest.Port > 65535 {
		return fmt.Errorf("rest.port must be in 0..65535 (got %d)", c.Rest.Port)
	}
	if len(c.Discovery.Include) > 0 && len(c.Discovery.Exclude) > 0 {
		return fmt.Errorf("discovery.include and discovery.exclude are mutually exclusive")
	}
	if c.Discovery.PostOpenSettleMs < 0 {
		return fmt.Errorf("discovery.post_open_settle_ms must be >= 0 (got %d)", c.Discovery.PostOpenSettleMs)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug|info|warn|error (got %q)", c.Log.Level)
	}
	return nil
}
```

- [ ] **Step 4: Create the golden scaffold file**

Create `internal/config/testdata/scaffold.golden.yaml` containing exactly the same text as `scaffoldTemplate` above (including the trailing newline). The contents must match byte-for-byte. The easiest way: run the test once, observe the diff, copy the `GOT` block into the file.

- [ ] **Step 5: Verify all package tests pass**

Run: `go test ./internal/config/... -v`
Expected: PASS for every test in the package, including the golden snapshot.

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/load.go internal/config/config_test.go internal/config/load_test.go internal/config/testdata/scaffold.golden.yaml
git commit -m "feat(config): drop ChiselConfig schema, require non-empty user/pass"
```

---

## Task 11: `decideFirstRun` + `readFirstRunState`

Cross-platform helpers for the panel's first-run gate.

**Files:**
- Create: `internal/panel/firstrun.go`
- Create: `internal/panel/firstrun_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/panel/firstrun_test.go`:

```go
package panel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func TestDecideFirstRun_MissingFile(t *testing.T) {
	s := FirstRunState{Exists: false, ParseErr: nil, Cfg: config.Default()}
	if got := decideFirstRun(s); got != FirstRunShowDialog {
		t.Errorf("missing file: got %v, want ShowDialog", got)
	}
}

func TestDecideFirstRun_BothSet(t *testing.T) {
	c := config.Default()
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	s := FirstRunState{Exists: true, ParseErr: nil, Cfg: c}
	if got := decideFirstRun(s); got != FirstRunOpenPanel {
		t.Errorf("both set: got %v, want OpenPanel", got)
	}
}

func TestDecideFirstRun_UserBlank(t *testing.T) {
	c := config.Default()
	c.LabBridge.User = ""
	c.LabBridge.Pass = "p"
	s := FirstRunState{Exists: true, ParseErr: nil, Cfg: c}
	if got := decideFirstRun(s); got != FirstRunShowDialog {
		t.Errorf("user blank: got %v, want ShowDialog", got)
	}
}

func TestDecideFirstRun_PassBlank(t *testing.T) {
	c := config.Default()
	c.LabBridge.User = "u"
	c.LabBridge.Pass = ""
	s := FirstRunState{Exists: true, ParseErr: nil, Cfg: c}
	if got := decideFirstRun(s); got != FirstRunShowDialog {
		t.Errorf("pass blank: got %v, want ShowDialog", got)
	}
}

func TestDecideFirstRun_YAMLParseErrorOpensPanel(t *testing.T) {
	s := FirstRunState{Exists: true, ParseErr: errors.New("yaml: invalid"), Cfg: config.Default()}
	if got := decideFirstRun(s); got != FirstRunOpenPanel {
		t.Errorf("parse error: got %v, want OpenPanel (existing warning surfaces it)", got)
	}
}

func TestReadFirstRunState_MissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	s := readFirstRunState(p)
	if s.Exists {
		t.Errorf("Exists: got true, want false")
	}
	if s.ParseErr != nil {
		t.Errorf("ParseErr: got %v, want nil", s.ParseErr)
	}
	if s.Cfg.LabBridge.Host == "" {
		t.Errorf("Cfg: expected Default() values, got %+v", s.Cfg)
	}
}

func TestReadFirstRunState_PresentParses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`lab_bridge: {host: "10.0.0.1", user: "u", pass: "p"}
rest: {port: 0}
log: {level: "info"}
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := readFirstRunState(p)
	if !s.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if s.ParseErr != nil {
		t.Errorf("ParseErr: got %v, want nil (validation errors are not ParseErr)", s.ParseErr)
	}
	if s.Cfg.LabBridge.User != "u" {
		t.Errorf("user: got %q, want u", s.Cfg.LabBridge.User)
	}
}

func TestReadFirstRunState_MalformedYAMLSetsParseErr(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("::: not yaml :::"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := readFirstRunState(p)
	if !s.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if s.ParseErr == nil {
		t.Errorf("ParseErr: expected non-nil on malformed YAML")
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/panel/ -run TestDecideFirstRun -v`
Expected: FAIL — package symbols missing.

- [ ] **Step 3: Implement**

Create `internal/panel/firstrun.go`:

```go
package panel

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

// FirstRunAction is the decision returned by decideFirstRun.
type FirstRunAction int

const (
	FirstRunOpenPanel FirstRunAction = iota
	FirstRunShowDialog
)

// FirstRunState describes everything decideFirstRun needs about the
// on-disk config to choose an action.
type FirstRunState struct {
	Exists   bool          // config file exists
	ParseErr error         // non-nil iff YAML parse failed
	Cfg      config.Config // populated from Default() and overlaid with whatever parsed cleanly
}

// readFirstRunState inspects path and returns a FirstRunState describing
// the file's existence and parsed contents.
func readFirstRunState(path string) FirstRunState {
	s := FirstRunState{Cfg: config.Default()}
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s
		}
		s.Exists = true
		s.ParseErr = err
		return s
	}
	s.Exists = true
	if uerr := yaml.Unmarshal(data, &s.Cfg); uerr != nil {
		s.ParseErr = uerr
		s.Cfg = config.Default()
	}
	return s
}

// decideFirstRun returns ShowDialog when the file is missing or both
// credentials are absent; otherwise OpenPanel. Malformed YAML opens the
// panel (the existing validation-warning label surfaces the parse error
// — we don't silently overwrite a file we cannot understand).
func decideFirstRun(s FirstRunState) FirstRunAction {
	if !s.Exists {
		return FirstRunShowDialog
	}
	if s.ParseErr != nil {
		return FirstRunOpenPanel
	}
	if s.Cfg.LabBridge.User == "" || s.Cfg.LabBridge.Pass == "" {
		return FirstRunShowDialog
	}
	return FirstRunOpenPanel
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/panel/ -run "TestDecideFirstRun|TestReadFirstRunState" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/firstrun.go internal/panel/firstrun_test.go
git commit -m "feat(panel): add decideFirstRun and readFirstRunState helpers"
```

---

## Task 12: `verifyCredentials` — classify the labbridge response

This is the dialog's submit-side logic, factored to a pure helper that we can table-test against a fake labbridge endpoint.

**Files:**
- Modify: `internal/panel/firstrun.go`
- Modify: `internal/panel/firstrun_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/panel/firstrun_test.go`:

```go
import (
	// add to existing imports:
	"context"
	"net/http"
	"net/http/httptest"
	"time"
)

func TestVerifyCredentials_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	}))
	t.Cleanup(srv.Close)
	got := verifyCredentials(context.Background(), srv.Client(), srv.URL, "u", "p", "test/1")
	if got.Kind != CredsOK {
		t.Errorf("Kind: got %v, want CredsOK (detail=%q)", got.Kind, got.Detail)
	}
}

func TestVerifyCredentials_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", 401)
	}))
	t.Cleanup(srv.Close)
	got := verifyCredentials(context.Background(), srv.Client(), srv.URL, "u", "wrong", "test/1")
	if got.Kind != CredsUnauthorized {
		t.Errorf("Kind: got %v, want CredsUnauthorized", got.Kind)
	}
}

func TestVerifyCredentials_500NeedsConfirm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "roster broken", 500)
	}))
	t.Cleanup(srv.Close)
	got := verifyCredentials(context.Background(), srv.Client(), srv.URL, "u", "p", "test/1")
	if got.Kind != CredsNeedsConfirm {
		t.Errorf("Kind: got %v, want CredsNeedsConfirm", got.Kind)
	}
	if got.Detail == "" {
		t.Errorf("Detail should describe the error")
	}
}

func TestVerifyCredentials_NetworkNeedsConfirm(t *testing.T) {
	// Point at a closed port.
	got := verifyCredentials(context.Background(), &http.Client{Timeout: 100 * time.Millisecond},
		"http://127.0.0.1:1", "u", "p", "test/1")
	if got.Kind != CredsNeedsConfirm {
		t.Errorf("Kind: got %v, want CredsNeedsConfirm", got.Kind)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/panel/ -run TestVerifyCredentials -v`
Expected: FAIL — `verifyCredentials`, `CredsOK`, etc. undefined.

- [ ] **Step 3: Implement**

Append to `internal/panel/firstrun.go`:

```go
import (
	// add to existing imports:
	"context"
	"errors"
	"net/http"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// CredsCheckKind enumerates how the dialog should react to verifyCredentials.
type CredsCheckKind int

const (
	CredsOK             CredsCheckKind = iota // 200 — save.
	CredsUnauthorized                         // 401 — inline error, stay in dialog.
	CredsNeedsConfirm                         // 5xx or network — prompt the user to "save anyway?".
)

// CredsCheckResult is the verdict of verifyCredentials.
type CredsCheckResult struct {
	Kind   CredsCheckKind
	Detail string // human-readable reason for Confirm/Unauthorized; empty on OK.
}

// verifyCredentials makes one /api/public/clients/{user} call and
// classifies the outcome. base must be the scheme+host (e.g. "https://x").
func verifyCredentials(ctx context.Context, hc *http.Client, base, user, pass, userAgent string) CredsCheckResult {
	_, err := labbridge.FetchClient(ctx, hc, base, user, pass, userAgent)
	switch {
	case err == nil:
		return CredsCheckResult{Kind: CredsOK}
	case errors.Is(err, labbridge.ErrUnauthorized):
		return CredsCheckResult{Kind: CredsUnauthorized, Detail: "server rejected credentials"}
	default:
		return CredsCheckResult{Kind: CredsNeedsConfirm, Detail: err.Error()}
	}
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/panel/ -run TestVerifyCredentials -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/firstrun.go internal/panel/firstrun_test.go
git commit -m "feat(panel): add verifyCredentials classifier for the first-run dialog"
```

---

## Task 13: `patchCredentials` — comment-preserving YAML update

**Files:**
- Modify: `internal/panel/firstrun.go`
- Modify: `internal/panel/firstrun_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/panel/firstrun_test.go`:

```go
import (
	// add to existing imports:
	"strings"

	"gopkg.in/yaml.v3"
)

func TestPatchCredentials_ReplacesUserAndPass(t *testing.T) {
	in := []byte(`# top comment
lab_bridge:
  host: "10.0.0.1"   # host comment
  user: ""           # user comment
  pass: ""           # pass comment

rest:
  port: 0
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, `user: "alice"`) {
		t.Errorf("user not replaced:\n%s", s)
	}
	if !strings.Contains(s, `pass: "s3cret"`) {
		t.Errorf("pass not replaced:\n%s", s)
	}
	if !strings.Contains(s, `host: "10.0.0.1"`) {
		t.Errorf("host should be preserved:\n%s", s)
	}
	if !strings.Contains(s, "# top comment") {
		t.Errorf("top comment dropped:\n%s", s)
	}
	if !strings.Contains(s, "# host comment") {
		t.Errorf("inline host comment dropped:\n%s", s)
	}
}

func TestPatchCredentials_PreservesUnrelatedFields(t *testing.T) {
	in := []byte(`lab_bridge:
  host: "h"
  user: ""
  pass: ""
discovery:
  include: ["COM3", "COM4"]
log:
  level: "debug"
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	// Round-trip into a config struct and check fields.
	var c config.Config
	if err := yaml.Unmarshal(got, &c); err != nil {
		t.Fatalf("unmarshal patched: %v", err)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got user=%q pass=%q", c.LabBridge.User, c.LabBridge.Pass)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level: got %q, want debug", c.Log.Level)
	}
	if len(c.Discovery.Include) != 2 || c.Discovery.Include[0] != "COM3" {
		t.Errorf("discovery.include not preserved: %+v", c.Discovery.Include)
	}
}

func TestPatchCredentials_AppendsLabBridgeWhenAbsent(t *testing.T) {
	in := []byte(`rest:
  port: 0
log:
  level: "info"
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	var c config.Config
	if err := yaml.Unmarshal(got, &c); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got user=%q pass=%q", c.LabBridge.User, c.LabBridge.Pass)
	}
}

func TestPatchCredentials_AddsKeysWhenLabBridgePresentButCredsMissing(t *testing.T) {
	in := []byte(`lab_bridge:
  host: "h"
rest:
  port: 0
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	var c config.Config
	if err := yaml.Unmarshal(got, &c); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if c.LabBridge.Host != "h" {
		t.Errorf("host: got %q", c.LabBridge.Host)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got user=%q pass=%q", c.LabBridge.User, c.LabBridge.Pass)
	}
}

func TestPatchCredentials_RejectsMalformedYAML(t *testing.T) {
	_, err := patchCredentials([]byte("::: not yaml :::"), "u", "p")
	if err == nil {
		t.Errorf("expected error on malformed YAML, got nil")
	}
}
```

Also add a write-helper test:

```go
func TestWriteOrPatchCreds_CreatesScaffoldWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := writeOrPatchCreds(p, "alice", "s3cret"); err != nil {
		t.Fatalf("writeOrPatchCreds: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `user: "alice"`) {
		t.Errorf("scaffold missing user:\n%s", data)
	}
	if !strings.Contains(string(data), `pass: "s3cret"`) {
		t.Errorf("scaffold missing pass:\n%s", data)
	}
	// File must validate end-to-end (post-creds).
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabBridge.User != "alice" {
		t.Errorf("loaded user: got %q", c.LabBridge.User)
	}
}

func TestWriteOrPatchCreds_PatchesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`lab_bridge:
  host: "10.0.0.1"
  user: ""
  pass: ""
log:
  level: "debug"
`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeOrPatchCreds(p, "alice", "s3cret"); err != nil {
		t.Fatalf("writeOrPatchCreds: %v", err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got %+v", c.LabBridge)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level not preserved: got %q", c.Log.Level)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/panel/ -run "TestPatchCredentials|TestWriteOrPatchCreds" -v`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement**

Append to `internal/panel/firstrun.go`:

```go
import (
	// add to existing imports:
	"fmt"
	"path/filepath"
	"strings"
)

// patchCredentials replaces (or appends) lab_bridge.user and
// lab_bridge.pass inside yamlBytes, preserving comments and unrelated
// fields. The two values are written as double-quoted scalars to match
// the scaffold style.
func patchCredentials(yamlBytes []byte, user, pass string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &root); err != nil {
		return nil, fmt.Errorf("patchCredentials: parse: %w", err)
	}
	if root.Kind == 0 {
		// Empty input: build a fresh mapping from scratch.
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("patchCredentials: unexpected YAML shape (kind=%d)", root.Kind)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("patchCredentials: top-level YAML must be a mapping (kind=%d)", doc.Kind)
	}

	labBridge := findMappingChild(doc, "lab_bridge")
	if labBridge == nil {
		// Append a new lab_bridge block.
		doc.Content = append(doc.Content,
			scalarKey("lab_bridge"),
			newLabBridgeMapping(user, pass),
		)
	} else {
		setOrAppendScalar(labBridge, "user", user)
		setOrAppendScalar(labBridge, "pass", pass)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("patchCredentials: marshal: %w", err)
	}
	return out, nil
}

// findMappingChild returns the value Node for key inside a mapping
// Node, or nil if not present. Caller must ensure parent.Kind == MappingNode.
func findMappingChild(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		k, v := parent.Content[i], parent.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return v
		}
	}
	return nil
}

// setOrAppendScalar sets parent[key] to a double-quoted scalar value.
// Appends a new key+value pair if key is absent.
func setOrAppendScalar(parent *yaml.Node, key, value string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		k := parent.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			parent.Content[i+1] = scalarString(value)
			return
		}
	}
	parent.Content = append(parent.Content, scalarKey(key), scalarString(value))
}

func scalarKey(name string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: name}
}

func scalarString(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Value: v}
}

func newLabBridgeMapping(user, pass string) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			scalarKey("user"), scalarString(user),
			scalarKey("pass"), scalarString(pass),
		},
	}
}

// writeOrPatchCreds writes the credentials to path. If path does not
// exist, the full scaffold is rendered with user and pass substituted.
// If path exists, only lab_bridge.user/pass are updated; everything
// else (including comments) is preserved. The file is written
// atomically (tmp + rename) at 0600.
func writeOrPatchCreds(path, user, pass string) error {
	existing, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("writeOrPatchCreds: read: %w", err)
	}
	var out []byte
	if errors.Is(err, os.ErrNotExist) {
		out = renderScaffoldWithCreds(user, pass)
	} else {
		out, err = patchCredentials(existing, user, pass)
		if err != nil {
			return err
		}
	}
	return atomicWriteFile(path, out, 0o600)
}

// renderScaffoldWithCreds returns the bytes of the scaffold template
// with user and pass substituted.
func renderScaffoldWithCreds(user, pass string) []byte {
	var buf strings.Builder
	if err := config.WriteScaffold(&buf); err != nil {
		// WriteScaffold can only fail if its io.Writer fails; strings.Builder doesn't.
		panic(fmt.Sprintf("WriteScaffold to strings.Builder failed: %v", err))
	}
	s := buf.String()
	s = strings.Replace(s, `user: ""`, fmt.Sprintf(`user: %q`, user), 1)
	s = strings.Replace(s, `pass: ""`, fmt.Sprintf(`pass: %q`, pass), 1)
	return []byte(s)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-cfg-*")
	if err != nil {
		return fmt.Errorf("atomicWriteFile: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: close: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/panel/ -run "TestPatchCredentials|TestWriteOrPatchCreds" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/firstrun.go internal/panel/firstrun_test.go
git commit -m "feat(panel): add comment-preserving credential patcher + atomic writer"
```

---

## Task 14: First-run dialog (Walk, Windows-only)

The dialog is glue. All testable logic already lives in `firstrun.go`.

**Files:**
- Create: `internal/panel/credsdialog_windows.go`
- Create: `internal/panel/credsdialog_other.go`

- [ ] **Step 1: Create the non-Windows stub**

Create `internal/panel/credsdialog_other.go`:

```go
//go:build !windows

package panel

import "github.com/bioexperiment-lab-devices/serialhop/internal/config"

// runCredsDialog is implemented only on Windows. On other platforms the
// panel itself doesn't compile (panel.go is windows-only) but firstrun
// helpers are cross-platform; this stub keeps the package's exported
// API consistent and the tests buildable.
func runCredsDialog(_ string, _ config.Config) bool { return false }
```

- [ ] **Step 2: Create the Windows implementation**

Create `internal/panel/credsdialog_windows.go`:

```go
//go:build windows

package panel

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// runCredsDialog shows the first-run credentials dialog modal to no
// parent. Returns true if the user submitted credentials and the
// config was written; false on cancel or fatal error.
func runCredsDialog(cfgPath string, cfg config.Config) bool {
	var (
		dlg       *walk.Dialog
		userEdit  *walk.LineEdit
		passEdit  *walk.LineEdit
		statusLbl *walk.Label
		saveBtn   *walk.PushButton
		cancelBtn *walk.PushButton
	)
	saved := false
	hc := &http.Client{Timeout: 10 * time.Second}
	userAgent := "SerialHop/" + version.Base() + " (firstrun)"
	base := "https://" + cfg.LabBridge.Host

	showStatus := func(msg string) {
		_ = statusLbl.SetText(msg)
		statusLbl.SetVisible(msg != "")
	}

	doSave := func(user, pass string) {
		if err := writeOrPatchCreds(cfgPath, user, pass); err != nil {
			walk.MsgBox(dlg, "Error", "Couldn't save config: "+err.Error(), walk.MsgBoxIconError)
			return
		}
		saved = true
		dlg.Accept()
	}

	onSubmit := func() {
		user := strings.TrimSpace(userEdit.Text())
		pass := strings.TrimSpace(passEdit.Text())
		if user == "" || pass == "" {
			showStatus("Username and password are required.")
			return
		}
		saveBtn.SetEnabled(false)
		cancelBtn.SetEnabled(false)
		showStatus("Verifying…")

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := verifyCredentials(ctx, hc, base, user, pass, userAgent)

			dlg.Synchronize(func() {
				saveBtn.SetEnabled(true)
				cancelBtn.SetEnabled(true)
				switch result.Kind {
				case CredsOK:
					showStatus("")
					doSave(user, pass)
				case CredsUnauthorized:
					showStatus("Server rejected these credentials. Check the username and password.")
				case CredsNeedsConfirm:
					msg := fmt.Sprintf("Couldn't reach %s to verify the credentials (%s). Save anyway?",
						cfg.LabBridge.Host, result.Detail)
					answer := walk.MsgBox(dlg, "Can't reach server", msg,
						walk.MsgBoxYesNo|walk.MsgBoxIconWarning|walk.MsgBoxDefButton2)
					if answer == walk.DlgCmdYes {
						doSave(user, pass)
					} else {
						showStatus("")
					}
				}
			})
		}()
	}

	_, err := Dialog{
		AssignTo:      &dlg,
		Title:         "SerialHop — Set credentials",
		MinSize:       Size{Width: 380, Height: 220},
		Layout:        VBox{},
		DefaultButton: &saveBtn,
		CancelButton:  &cancelBtn,
		Children: []Widget{
			Label{Text: "Lab-bridge server is configured to reach " + cfg.LabBridge.Host + ".\nEnter your credentials:"},
			Composite{
				Layout: Grid{Columns: 2},
				Children: []Widget{
					Label{Text: "Username:"},
					LineEdit{AssignTo: &userEdit},
					Label{Text: "Password:"},
					LineEdit{AssignTo: &passEdit}, // plain text per user requirement
				},
			},
			Label{
				AssignTo:  &statusLbl,
				TextColor: walk.RGB(192, 0, 0),
				Visible:   false,
			},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &cancelBtn, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
					PushButton{AssignTo: &saveBtn, Text: "Save", OnClicked: onSubmit},
				},
			},
		},
	}.Run(nil)

	if err != nil {
		writePanelDebugLog("creds_dialog_error", err)
		return false
	}
	return saved
}
```

- [ ] **Step 3: Verify build on both platforms**

Run: `go build ./...`
Expected: success.

Run: `GOOS=linux go build ./...` (cross-compile sanity check; matches CI's macOS+Windows requirement insofar as it exercises both build tags)
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/credsdialog_windows.go internal/panel/credsdialog_other.go
git commit -m "feat(panel): add first-run credentials dialog (Windows-only)"
```

---

## Task 15: Wire first-run gate into `panel.Run`; replace `ensureScaffold`

**Files:**
- Modify: `internal/panel/panel.go`

- [ ] **Step 1: Replace the scaffold-on-launch block**

In `internal/panel/panel.go`, find the existing block near the top of `Run`:

```go
	cfgPath := paths.ConfigPath()
	if pathsErr == nil {
		if err := ensureScaffold(cfgPath); err != nil {
			// Non-fatal: the panel can still run; it'll show "config missing".
			_ = err
		}
	}
```

Replace with the first-run gate:

```go
	cfgPath := paths.ConfigPath()
	if pathsErr == nil {
		state := readFirstRunState(cfgPath)
		if decideFirstRun(state) == FirstRunShowDialog {
			_ = runCredsDialog(cfgPath, state.Cfg)
			// Whether the user saved or cancelled, fall through. On cancel,
			// the panel opens with empty creds and the existing
			// validation-warning label surfaces the missing-fields error.
		}
	}
```

Delete the now-unused `ensureScaffold` function (at the bottom of the file).

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: success. (Tests in this file are minimal; smoke-tested below.)

- [ ] **Step 3: Run the cross-platform package tests**

Run: `go test ./internal/panel/...`
Expected: PASS for all firstrun_test.go cases.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/panel.go
git commit -m "feat(panel): gate first launch on credentials dialog instead of bare scaffold"
```

---

## Task 16: Panel config display reads from bootstrap cache

The panel still shows "Chisel server: host:port" and "Remote port: N", but the source moves from `cfg.Chisel.*` (gone) to the cache file written by the service worker.

**Files:**
- Modify: `internal/panel/panel.go`

- [ ] **Step 1: Update the refresh logic**

In `internal/panel/panel.go`, find inside `refresh()`:

```go
		serverLbl.SetText("Chisel server:    " + net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(cfg.Chisel.Port)))
		remotePort.SetText(fmt.Sprintf("Remote port:      %d", cfg.Chisel.RemotePort))
```

Replace with a cache-driven block:

```go
		serverDisplay, remoteDisplay := readCacheDisplay(cfg.LabBridge.Host, cfg.LabBridge.User)
		serverLbl.SetText("Chisel server:    " + serverDisplay)
		remotePort.SetText("Remote port:      " + remoteDisplay)
```

Add at the bottom of `panel.go`:

```go
// readCacheDisplay returns the strings used in the panel's
// "Chisel server" and "Remote port" labels. When the bootstrap cache
// exists and is anchored to user, the cached values are shown; otherwise
// host:<…> and "…". Errors are swallowed silently (lamps already surface
// connectivity problems).
func readCacheDisplay(host, user string) (server, remote string) {
	if host == "" {
		return "—", "—"
	}
	c, err := bootstrap.ReadCache(paths.ServerInfoCachePath(), user)
	if err != nil {
		return net.JoinHostPort(host, "…"), "…"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.ServerInfo.ChiselListenPort)), strconv.Itoa(c.RemotePort)
}
```

Add the import:

```go
"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
```

Verify that `strconv` and `net` imports are still present (they are; the existing code uses them).

- [ ] **Step 2: Verify build and tests**

Run: `go build ./...`
Expected: success.

Run: `go test ./internal/panel/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/panel/panel.go
git commit -m "feat(panel): drive Chisel-server/remote-port labels from bootstrap cache"
```

---

## Task 17: Update README install steps

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Edit step 3**

In `README.md`, replace the current step 3 of "Install on a Windows lab machine":

```
3. Click **Open config file**, set `chisel.remote_port`, `lab_bridge.user`, and `lab_bridge.pass` (and any other site-specific values), save.
```

with:

```
3. On first launch the panel pops up a **Set credentials** dialog. Enter your `lab_bridge.user` and `lab_bridge.pass`; the panel verifies them against the lab-bridge server and writes them to the config file. The chisel listen port, Loki push URL, and forward tunnels are fetched from the server automatically — no further config editing is required for a standard install.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: replace remote_port instruction with first-run dialog walkthrough"
```

---

## Task 18: Full-suite verification

**Files:** (none — verification only)

- [ ] **Step 1: Format check**

Run: `gofmt -l .`
Expected: prints nothing.

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 3: Lint**

Run: `golangci-lint run`
Expected: clean.

- [ ] **Step 4: Race-tested full test suite**

Run: `go test -race -count=1 ./...`
Expected: PASS for all packages on macOS and Windows (CI runs both via `pr.yml`).

- [ ] **Step 5: Vulncheck**

Run: `govulncheck ./...`
Expected: no high-severity vulns introduced.

- [ ] **Step 6: Optional — render scaffold to a temp file and eyeball it**

Run:

```bash
go run ./tools/render-manifest --help 2>/dev/null || true   # sanity: still compiles
go test ./internal/config/ -run TestWriteScaffold_GoldenSnapshot -v
```

Expected: golden snapshot PASSES. If it fails, the scaffold template drifted; review and update `testdata/scaffold.golden.yaml` deliberately.

---

## Self-Review Notes

**Spec coverage:** Every requirement in `docs/superpowers/specs/2026-05-11-config-server-info-design.md` maps to a task:
- §"Configuration shape after cleanup" → Task 10.
- §"Server-info client" → Task 2.
- §"Disk cache" → Task 3.
- §"Bootstrap resolver" → Task 4 (including 401-bypass and user-anchoring behavior).
- §"Wiring changes: worker.go, app.go, chisel, logship" → Tasks 5, 6, 7, 8, 9.
- §"First-run credentials dialog" → Tasks 11, 12, 13, 14, 15.
- §"Panel display from cache" → Task 16.
- §"Testing" — every test case enumerated in the spec has a corresponding step in tasks 2, 3, 4, 5, 8, 11, 12, 13.

**Type consistency:** `bootstrap.Resolved`, `labbridge.ServerInfo`, `labbridge.ForwardTunnel`, `chisel.Config.ForwardTunnels`, `bootstrap.Cache.User` — all match across tasks. The cache JSON field names (`server_info`, `forward_tunnels`, `remote_port`, `user`, `version`, `fetched_at`) match the spec's example.

**Placeholders:** None — every code block is complete, every command is concrete, every test has a body.

**Risk callouts:**
- Task 4's `tryLive` runs two goroutines; the test for backoff (`TestResolve_NoCache_RetriesUntilSuccess`) uses 1 ms initial backoff to keep wall-clock short. If flakiness emerges in CI, raise `MaxBackoff` to 50 ms and adjust the test ctx timeout.
- Task 13's `patchCredentials` uses `yaml.v3` Node API — note that v3's default marshal indent is 4 spaces, which won't match the scaffold's 2-space indent if the file is later re-parsed and re-marshaled. The patch path preserves the original Node positions, so this only matters for the append-lab-bridge case. The tests round-trip via `yaml.Unmarshal` rather than string comparison, so they tolerate the indent.
- Task 14's dialog uses `lxn/walk` Synchronize for the async verification step; `Dialog.Run` blocks the calling goroutine and only the main UI goroutine is allowed to Synchronize.
