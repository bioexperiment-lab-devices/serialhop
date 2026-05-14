# Panel UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `lxn/walk` panel with a Wails v2 + React + TypeScript app implementing the five-tab UI from `docs/superpowers/specs/2026-05-13-panel-ui-redesign-design.md`.

**Architecture:** Single Windows binary, four runtime modes (service / admin helper / foreground-dev / panel). Only panel mode changes — it becomes a Wails app that embeds Edge WebView2. The Go side runs probe loops + SCM polling + log tailing + service-control UAC subprocesses; the TS/React side renders five tabs and owns the in-flight config form state. Cross-cutting interaction model is hybrid: push events for state (lamps, update-state, log lines), pull bindings for list data (devices, ports). The existing `internal/api` REST API, `internal/winsvc`, `internal/config`, and the entire release/update pipeline are unchanged.

**Tech Stack:** Go 1.25 + Wails v2 (pinned at task time), React 18 + TypeScript 5 + Vite 5, plain CSS adapted from `docs/serialhop-ui/project/styles.css`. Vitest + React Testing Library for frontend tests. Standard `testing` package for Go.

---

## Phase 1 — Service-side foundation

### Task 1: Add `ActualRestPort` to `bootstrap.Cache`

**Files:**
- Modify: `internal/bootstrap/cache.go:31-36` (`Cache` struct definition)
- Modify: `internal/bootstrap/cache_test.go` (`sampleCache` + new test)

- [ ] **Step 1: Write the failing test**

Append to `internal/bootstrap/cache_test.go`:

```go
func TestWriteCache_AndReadCache_RoundTripActualRestPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.ActualRestPort = 49283
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort: got %d, want 49283", got.ActualRestPort)
	}
}

func TestWriteCache_ActualRestPortJSONKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.ActualRestPort = 49283
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"actual_rest_port": 49283`) {
		t.Errorf("missing actual_rest_port key; body:\n%s", data)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/bootstrap/ -run TestWriteCache_AndReadCache_RoundTripActualRestPort -v`
Expected: FAIL — `Cache` has no `ActualRestPort` field.

- [ ] **Step 3: Add the field**

Edit `internal/bootstrap/cache.go`:

```go
type Cache struct {
	Version        int                  `json:"version"`
	FetchedAt      string               `json:"fetched_at"`
	User           string               `json:"user"`
	ServerInfo     labbridge.ServerInfo `json:"server_info"`
	RemotePort     int                  `json:"remote_port"`
	ActualRestPort int                  `json:"actual_rest_port"`
}
```

- [ ] **Step 4: Run tests to confirm green**

Run: `go test -race -count=1 ./internal/bootstrap/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bootstrap/cache.go internal/bootstrap/cache_test.go
git commit -m "feat(bootstrap): add ActualRestPort to cache for panel→service routing"
```

---

### Task 2: Service writes its bound REST port into the cache

The service learns the actual port from `api.Listen` and must write it into the cache so the panel can find it.

**Files:**
- Modify: `internal/app/app.go:37-42` (after `api.Listen` returns)
- Test: `internal/app/app_test.go` (new file)

- [ ] **Step 1: Write a failing test**

Create `internal/app/app_test.go`:

```go
package app

import (
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestWriteActualRestPort_UpdatesCacheAtomically(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	seed := bootstrap.Cache{
		Version:    1,
		FetchedAt:  "2026-05-13T00:00:00Z",
		User:       "alice",
		ServerInfo: labbridge.ServerInfo{ChiselListenPort: 7000},
		RemotePort: 8089,
	}
	if err := bootstrap.WriteCache(cachePath, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeActualRestPort(cachePath, "alice", 49283); err != nil {
		t.Fatalf("writeActualRestPort: %v", err)
	}
	got, err := bootstrap.ReadCache(cachePath, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort: got %d, want 49283", got.ActualRestPort)
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort clobbered: got %d, want 8089", got.RemotePort)
	}
}

func TestWriteActualRestPort_NoCacheIsNotAnError(t *testing.T) {
	// If the cache doesn't exist yet (first launch racing chisel bootstrap)
	// we silently no-op. The next bootstrap.Resolve will rewrite the cache.
	if err := writeActualRestPort(filepath.Join(t.TempDir(), "nope.json"), "alice", 49283); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}
```

- [ ] **Step 2: Confirm fail**

Run: `go test ./internal/app/ -run TestWriteActualRestPort -v`
Expected: FAIL — `writeActualRestPort` undefined.

- [ ] **Step 3: Add the helper to `internal/app/app.go`**

Add at the bottom of `internal/app/app.go`:

```go
// writeActualRestPort updates the bootstrap cache with the port the local
// REST listener actually bound to. Called once after api.Listen returns.
// Silently no-ops if the cache is missing or anchored to a different user;
// the panel falls back to its "service unreachable" empty state in that case.
func writeActualRestPort(cachePath, user string, port int) error {
	c, err := bootstrap.ReadCache(cachePath, user)
	if err != nil {
		// ErrCacheMissing or anchored-to-other-user: silently skip.
		return nil
	}
	c.ActualRestPort = port
	return bootstrap.WriteCache(cachePath, c)
}
```

- [ ] **Step 4: Wire it into `Run`**

Modify `internal/app/app.go`, immediately after `slog.Info("rest listening", …)`:

```go
	if err := writeActualRestPort(paths.ServerInfoCachePath(), cfg.LabBridge.User, localPort); err != nil {
		slog.Warn("failed to write actual rest port to cache", "err", err)
	}
```

- [ ] **Step 5: Confirm tests pass**

Run: `go test -race -count=1 ./internal/app/... ./internal/bootstrap/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): write actual REST port to bootstrap cache for panel→service routing"
```

---

## Phase 2 — New cross-platform Go helpers (panel-side)

### Task 3: `internal/panel/servicecli.go` — typed HTTP client to the local service

The panel uses this client to drive the Devices and Ports tabs. It reads `ActualRestPort` from the cache per call and returns a three-way status (ok / unreachable / service-down) so bindings can map to the empty-state banners in the spec §6.4 / §7.3.

**Files:**
- Create: `internal/panel/servicecli.go`
- Create: `internal/panel/servicecli_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/panel/servicecli_test.go`:

```go
package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func seedCache(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	c := bootstrap.Cache{
		Version:        1,
		FetchedAt:      "2026-05-13T00:00:00Z",
		User:           "alice",
		ServerInfo:     labbridge.ServerInfo{ChiselListenPort: 7000},
		RemotePort:     8089,
		ActualRestPort: port,
	}
	if err := bootstrap.WriteCache(cachePath, c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return cachePath
}

func TestServiceCli_GetDevices_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devices" {
			t.Errorf("path: got %s, want /devices", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{
			Devices: []api.DeviceDTO{{ID: "pump_1", Type: "pump", Port: "COM5"}},
		})
	}))
	defer srv.Close()

	port := mustPortFromURL(t, srv.URL)
	cli := NewServiceCli(seedCache(t, port), "alice")
	resp, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "pump_1" {
		t.Errorf("unexpected devices: %+v", resp.Devices)
	}
}

func TestServiceCli_GetDevices_CacheMissingReturnsUnreachable(t *testing.T) {
	cli := NewServiceCli(filepath.Join(t.TempDir(), "missing.json"), "alice")
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusUnreachable {
		t.Errorf("status: got %v, want StatusUnreachable", status)
	}
}

func TestServiceCli_GetDevices_ActualPortZeroReturnsUnreachable(t *testing.T) {
	cli := NewServiceCli(seedCache(t, 0), "alice")
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusUnreachable {
		t.Errorf("status: got %v, want StatusUnreachable", status)
	}
}

func TestServiceCli_GetDevices_ConnectionRefusedReturnsServiceDown(t *testing.T) {
	// Use a port we know nothing is listening on.
	cli := NewServiceCli(seedCache(t, 1), "alice") // port 1 reserved → conn refused
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusServiceDown {
		t.Errorf("status: got %v, want StatusServiceDown", status)
	}
}

func TestServiceCli_Discover_PostsToDiscover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/discover" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{Devices: []api.DeviceDTO{}})
	}))
	defer srv.Close()
	port := mustPortFromURL(t, srv.URL)
	cli := NewServiceCli(seedCache(t, port), "alice")
	_, status, err := cli.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
}

func mustPortFromURL(t *testing.T, raw string) int {
	t.Helper()
	// httptest URL is "http://127.0.0.1:PORT"
	idx := strings.LastIndex(raw, ":")
	if idx < 0 {
		t.Fatalf("can't parse port from %q", raw)
	}
	var p int
	if _, err := fmtSscanf(raw[idx+1:], &p); err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return p
}

func fmtSscanf(s string, out *int) (int, error) {
	// thin wrapper so we don't import "fmt" twice in the test file
	return jsonNumberToInt(s, out)
}

func jsonNumberToInt(s string, out *int) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int(c-'0')
	}
	*out = v
	return v, nil
}
```

- [ ] **Step 2: Confirm fail**

Run: `go test ./internal/panel/ -run TestServiceCli -v`
Expected: FAIL — `NewServiceCli` / `StatusOK` undefined.

- [ ] **Step 3: Implement the client**

Create `internal/panel/servicecli.go`:

```go
package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

// ServiceCliStatus is the three-way reachability outcome the panel exposes
// to operators as the empty-state banner on the Devices and Ports tabs.
// See spec §6.4 / §7.3.
type ServiceCliStatus int

const (
	StatusOK ServiceCliStatus = iota
	// StatusUnreachable — the bootstrap cache is missing, anchored to a
	// different user, or ActualRestPort == 0. The panel doesn't know where
	// the service is even if it is running. Show: "Can't reach the local
	// service. It may have just started — wait a few seconds and click
	// Refresh."
	StatusUnreachable
	// StatusServiceDown — we know the port but the HTTP call failed
	// (connection refused, timeout, etc.). The service is not running.
	// Show: "Service is not running. Start it from the Status tab."
	StatusServiceDown
)

// ServiceCli is a thin typed HTTP client that talks to the local SerialHop
// service over 127.0.0.1:<ActualRestPort>. It reads the bootstrap cache
// per call so a service restart while the panel is open doesn't strand
// it on a stale port.
type ServiceCli struct {
	cachePath string
	user      string
	hc        *http.Client
}

// NewServiceCli returns a client anchored to the given bootstrap-cache
// path + lab-bridge user. The HTTP client has a 5s per-call timeout.
func NewServiceCli(cachePath, user string) *ServiceCli {
	return &ServiceCli{
		cachePath: cachePath,
		user:      user,
		hc:        &http.Client{Timeout: 5 * time.Second},
	}
}

// baseURL reads the cache and returns "http://127.0.0.1:<port>".
// Returns StatusUnreachable on any cache-read failure or zero port.
func (c *ServiceCli) baseURL() (string, ServiceCliStatus) {
	cache, err := bootstrap.ReadCache(c.cachePath, c.user)
	if err != nil {
		return "", StatusUnreachable
	}
	if cache.ActualRestPort == 0 {
		return "", StatusUnreachable
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cache.ActualRestPort)), StatusOK
}

func (c *ServiceCli) do(ctx context.Context, method, path string, out any) (ServiceCliStatus, error) {
	base, status := c.baseURL()
	if status != StatusOK {
		return status, nil
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return StatusOK, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// Treat any transport-level error as service-down; the
		// caller has no actionable distinction between "refused"
		// and "timeout" — both mean the operator should start the
		// service.
		return StatusServiceDown, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		return StatusServiceDown, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return StatusServiceDown, fmt.Errorf("decode: %w", err)
		}
	}
	return StatusOK, nil
}

// GetDevices proxies GET /devices.
func (c *ServiceCli) GetDevices(ctx context.Context) (api.DevicesResponse, ServiceCliStatus, error) {
	var out api.DevicesResponse
	status, err := c.do(ctx, "GET", "/devices", &out)
	return out, status, err
}

// Discover proxies POST /discover.
func (c *ServiceCli) Discover(ctx context.Context) (api.DevicesResponse, ServiceCliStatus, error) {
	var out api.DevicesResponse
	status, err := c.do(ctx, "POST", "/discover", &out)
	return out, status, err
}

// DisconnectAll proxies POST /devices/disconnect.
func (c *ServiceCli) DisconnectAll(ctx context.Context) (api.DisconnectResponse, ServiceCliStatus, error) {
	var out api.DisconnectResponse
	status, err := c.do(ctx, "POST", "/devices/disconnect", &out)
	return out, status, err
}

// GetPorts proxies GET /serial/ports/detailed.
func (c *ServiceCli) GetPorts(ctx context.Context) (api.DetailedPortsResponse, ServiceCliStatus, error) {
	var out api.DetailedPortsResponse
	status, err := c.do(ctx, "GET", "/serial/ports/detailed", &out)
	return out, status, err
}

// Unused but kept to make the linter happy if errors.Is paths get added later.
var _ = errors.New
```

Remove the unused `errors` import + sentinel if `golangci-lint` complains.

- [ ] **Step 4: Confirm tests pass**

Run: `go test -race -count=1 ./internal/panel/ -run TestServiceCli`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/servicecli.go internal/panel/servicecli_test.go
git commit -m "feat(panel): add ServiceCli — typed local-service HTTP client with reachability status"
```

---

### Task 4: `internal/panel/filetail.go` — bounded-ring file tail with rotation detection

Tails an on-disk log file from end-of-file, calls a callback with each new line, and survives lumberjack rotation (the file at the same path gets replaced).

**Files:**
- Create: `internal/panel/filetail.go`
- Create: `internal/panel/filetail_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/panel/filetail_test.go`:

```go
package panel

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type sink struct {
	mu       sync.Mutex
	lines    []string
	rotated  int
}

func (s *sink) line(line string) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
}

func (s *sink) rotate() {
	s.mu.Lock()
	s.rotated++
	s.mu.Unlock()
}

func (s *sink) snapshot() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.lines...)
	return out, s.rotated
}

func TestFileTail_StartsFromEndOfFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("preexisting line\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailer := NewFileTail(p, 10*time.Millisecond, s.line, s.rotate)
	go tailer.Run(ctx)

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("new line\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.snapshot()
		if len(got) > 0 {
			if got[0] != "new line" {
				t.Errorf("got %q, want %q", got[0], "new line")
			}
			if len(got) > 1 {
				t.Errorf("expected exactly 1 line, got %v", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timeout — no lines emitted")
}

func TestFileTail_DetectsRotationOnInodeReset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("v1 line\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailer := NewFileTail(p, 10*time.Millisecond, s.line, s.rotate)
	go tailer.Run(ctx)

	// Wait for the tailer to attach.
	time.Sleep(50 * time.Millisecond)

	// Simulate lumberjack rotation: remove file, create new shorter one.
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(p, []byte("post-rotation\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lines, rot := s.snapshot()
		if rot > 0 && len(lines) > 0 && lines[len(lines)-1] == "post-rotation" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, rot := s.snapshot()
	t.Errorf("rotation not detected: rotated=%d lines=%v", rot, got)
}

func TestFileTail_MissingFileEmitsNothing(t *testing.T) {
	s := &sink{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	tailer := NewFileTail(filepath.Join(t.TempDir(), "nope"), 10*time.Millisecond, s.line, s.rotate)
	tailer.Run(ctx) // blocks until ctx expires
	got, _ := s.snapshot()
	if len(got) != 0 {
		t.Errorf("expected no lines, got %v", got)
	}
}
```

- [ ] **Step 2: Confirm fail**

Run: `go test ./internal/panel/ -run TestFileTail -v`
Expected: FAIL — `NewFileTail` undefined.

- [ ] **Step 3: Implement the tailer**

Create `internal/panel/filetail.go`:

```go
package panel

import (
	"bufio"
	"context"
	"io"
	"os"
	"time"
)

// FileTail follows a file appended-to by an external process, calling
// onLine with each newly-appended line and onRotate when it detects the
// file has been replaced underneath it (lumberjack rotation: same path,
// different inode / smaller size).
//
// The tailer always seeks to end-of-file when it first opens the target
// so it never emits backlog. If the file does not exist yet, the tailer
// silently retries each poll interval; when the file appears it attaches
// at offset 0 (logs created since the tailer started belong to the
// stream — but logs that existed before the tailer started do not).
type FileTail struct {
	path       string
	pollEvery  time.Duration
	onLine     func(string)
	onRotate   func()
}

// NewFileTail constructs a FileTail. onLine receives one line at a time
// (without the trailing newline). onRotate is called once per detected
// rotation. Both callbacks are invoked from FileTail's own goroutine —
// they must not block; route work onto a different channel if needed.
func NewFileTail(path string, pollEvery time.Duration, onLine func(string), onRotate func()) *FileTail {
	return &FileTail{path: path, pollEvery: pollEvery, onLine: onLine, onRotate: onRotate}
}

// Run blocks until ctx is cancelled, polling the file at pollEvery and
// emitting any new lines via onLine. Rotation is detected by comparing
// the current file's size + identity to what the tailer has open.
func (t *FileTail) Run(ctx context.Context) {
	var (
		f      *os.File
		reader *bufio.Reader
		stat   os.FileInfo
	)
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	ticker := time.NewTicker(t.pollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// (Re)open if we don't have a handle.
		if f == nil {
			nf, err := os.Open(t.path) //nolint:gosec // path comes from paths.*LogPath()
			if err != nil {
				continue
			}
			// Initial attach: seek to end so we don't replay backlog.
			if _, err := nf.Seek(0, io.SeekEnd); err != nil {
				_ = nf.Close()
				continue
			}
			ns, err := nf.Stat()
			if err != nil {
				_ = nf.Close()
				continue
			}
			f, reader, stat = nf, bufio.NewReader(nf), ns
			continue
		}

		// Detect rotation: compare the path's current file to ours.
		// Two heuristics combined catch lumberjack's behavior on Windows:
		//   (a) os.SameFile — different inode means a new file replaced
		//       ours at the same path.
		//   (b) size shrinkage — same handle but the underlying file got
		//       truncated.
		// On Windows os.SameFile uses VolumeSerialNumber + FileIndex; it
		// works for the rotation case.
		curStat, statErr := os.Stat(t.path)
		switch {
		case os.IsNotExist(statErr):
			// File deleted; close handle and wait for it to reappear.
			_ = f.Close()
			f, reader, stat = nil, nil, nil
			t.onRotate()
			continue
		case statErr != nil:
			// Other stat error: skip this tick.
			continue
		case !os.SameFile(stat, curStat):
			// New file at same path.
			_ = f.Close()
			f, reader, stat = nil, nil, nil
			t.onRotate()
			continue
		case curStat.Size() < stat.Size():
			// Truncation; treat as rotation.
			_ = f.Close()
			f, reader, stat = nil, nil, nil
			t.onRotate()
			continue
		}

		// Read any new bytes.
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				// Trim trailing \n (and \r if Windows wrote one).
				n := len(line)
				if n > 0 && line[n-1] == '\n' {
					line = line[:n-1]
					n--
				}
				if n > 0 && line[n-1] == '\r' {
					line = line[:n-1]
				}
				t.onLine(line)
			}
			if err == io.EOF {
				// Wait for next tick.
				stat = curStat
				break
			}
			if err != nil {
				// Treat as transient; will retry on next tick.
				break
			}
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/panel/ -run TestFileTail`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/filetail.go internal/panel/filetail_test.go
git commit -m "feat(panel): add FileTail — bounded log tailer with rotation detection"
```

---

### Task 5: `internal/panel/credverify.go` — verify-then-save state machine

Wraps the existing `verifyCredentials()` (in `internal/panel/firstrun.go`) with the change-detection from spec §5.9.

**Files:**
- Create: `internal/panel/credverify.go`
- Create: `internal/panel/credverify_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/panel/credverify_test.go`:

```go
package panel

import (
	"context"
	"errors"
	"testing"
)

type fakeVerifier struct {
	calls   int
	outcome CredsCheckResult
	err     error
}

func (f *fakeVerifier) Verify(_ context.Context, _, _, _ string) (CredsCheckResult, error) {
	f.calls++
	return f.outcome, f.err
}

func TestCredVerify_UnchangedCredentialsSkipNetwork(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsOK}
	cv := NewCredVerifier(fv)
	res, err := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "pw",
		NewHost: "h", NewUser: "alice", NewPass: "pw",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Outcome != "skipped" {
		t.Errorf("Outcome: got %q, want skipped", res.Outcome)
	}
	if fv.calls != 0 {
		t.Errorf("Verify called %d times — expected 0 (no change)", fv.calls)
	}
}

func TestCredVerify_PasswordChangedTriggersVerify_OK(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsOK}
	cv := NewCredVerifier(fv)
	res, _ := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "old",
		NewHost: "h", NewUser: "alice", NewPass: "new",
	})
	if res.Outcome != "ok" {
		t.Errorf("Outcome: got %q, want ok", res.Outcome)
	}
	if fv.calls != 1 {
		t.Errorf("Verify calls: got %d, want 1", fv.calls)
	}
}

func TestCredVerify_UserChangedAndUnauthorized(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsUnauthorized}
	cv := NewCredVerifier(fv)
	res, _ := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "pw",
		NewHost: "h", NewUser: "bob", NewPass: "pw",
	})
	if res.Outcome != "unauthorized" {
		t.Errorf("Outcome: got %q, want unauthorized", res.Outcome)
	}
}

func TestCredVerify_NetworkErrorSurfacesNeedsConfirm(t *testing.T) {
	fv := &fakeVerifier{outcome: CredsNeedsConfirm, err: errors.New("dial tcp: connection refused")}
	cv := NewCredVerifier(fv)
	res, _ := cv.Decide(context.Background(), CredChange{
		OldUser: "alice", OldPass: "pw",
		NewHost: "h", NewUser: "alice", NewPass: "pw2",
	})
	if res.Outcome != "needs_confirm" {
		t.Errorf("Outcome: got %q, want needs_confirm", res.Outcome)
	}
	if res.Detail == "" {
		t.Errorf("Detail empty — operator needs network-error text")
	}
}
```

- [ ] **Step 2: Confirm fail**

Run: `go test ./internal/panel/ -run TestCredVerify -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Create `internal/panel/credverify.go`:

```go
package panel

import "context"

// CredsCheckResult is the categorical outcome from a credentials probe
// against the lab-bridge. Mirrors the firstrun-flow values so the panel
// reuses a single verify implementation.
type CredsCheckResult int

const (
	CredsOK CredsCheckResult = iota
	CredsUnauthorized
	CredsNeedsConfirm
)

// CredVerifier abstracts the network probe so credverify can be unit-tested
// without going over the wire. The real implementation is firstrun.go's
// verifyCredentials; bindings.go constructs the verifier that wraps it.
type CredVerifier interface {
	Verify(ctx context.Context, host, user, pass string) (CredsCheckResult, error)
}

// CredChange captures the inputs the save flow has at decision time:
// the on-disk-loaded user/pass (Old*), and the form's current values
// (New*). The save flow only triggers a network probe if either user
// or pass changed.
type CredChange struct {
	OldUser, OldPass         string
	NewHost, NewUser, NewPass string
}

// CredDecision is the panel-facing result returned to the SaveConfig
// binding. Outcome ∈ {"skipped", "ok", "unauthorized", "needs_confirm"}.
// Detail is populated for "needs_confirm" (the network error to surface
// to the operator).
type CredDecision struct {
	Outcome string
	Detail  string
}

// CredVerify decides what to do when SaveConfig sees an in-flight form.
// Pure: only the Verify call hits the network.
type CredVerify struct {
	verifier CredVerifier
}

func NewCredVerifier(v CredVerifier) *CredVerify {
	return &CredVerify{verifier: v}
}

// Decide returns the action SaveConfig should take. When the operator
// hasn't touched user/pass, no network call is made (Outcome="skipped").
// Otherwise the verifier is consulted and its categorical result is
// translated into the panel's Outcome enum.
func (c *CredVerify) Decide(ctx context.Context, ch CredChange) (CredDecision, error) {
	if ch.NewUser == ch.OldUser && ch.NewPass == ch.OldPass {
		return CredDecision{Outcome: "skipped"}, nil
	}
	res, err := c.verifier.Verify(ctx, ch.NewHost, ch.NewUser, ch.NewPass)
	switch res {
	case CredsOK:
		return CredDecision{Outcome: "ok"}, nil
	case CredsUnauthorized:
		return CredDecision{Outcome: "unauthorized"}, nil
	case CredsNeedsConfirm:
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		return CredDecision{Outcome: "needs_confirm", Detail: detail}, nil
	}
	return CredDecision{Outcome: "unauthorized"}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/panel/ -run TestCredVerify`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/credverify.go internal/panel/credverify_test.go
git commit -m "feat(panel): add CredVerify — verify-then-save state machine for credential changes"
```

---

## Phase 3 — Wails infrastructure (scaffolding + CI changes)

> **Versions pinned at this task time:** Wails v2.10.x (or latest v2 minor at execution), Node 22 LTS, React 18.3+, TypeScript 5.5+, Vite 5.x. These are the floor versions; the engineer should pick the latest stable patch within each major at execution time and check it into `package.json` / `go.mod`. Record exact versions in the commit message.

### Task 6: Add Wails v2 dependency and scaffold the frontend project

This is a one-task scaffold — multiple files but they all only exist to make `wails dev` / `wails build` work. No business logic yet.

**Files:**
- Modify: `go.mod`, `go.sum` (Wails v2)
- Create: `wails.json` (Wails project config, repo root)
- Create: `internal/panel/frontend/package.json`
- Create: `internal/panel/frontend/vite.config.ts`
- Create: `internal/panel/frontend/tsconfig.json`
- Create: `internal/panel/frontend/index.html`
- Create: `internal/panel/frontend/src/main.tsx`
- Create: `internal/panel/frontend/src/App.tsx`
- Create: `internal/panel/frontend/src/styles/global.css`
- Create: `internal/panel/frontend/.gitignore`
- Create: `internal/panel/frontend/.eslintrc.cjs`

- [ ] **Step 1: Add Wails to `go.mod`**

```bash
go get github.com/wailsapp/wails/v2@latest
go mod tidy
```

Verify Wails appears in `go.mod`. Pin to a v2.x.y in `go.mod` directly if `@latest` resolves to a non-v2 line.

- [ ] **Step 2: Create `wails.json`**

```json
{
  "$schema": "https://wails.io/schemas/config.v2.json",
  "name": "SerialHop",
  "outputfilename": "SerialHop",
  "frontend:install": "npm install",
  "frontend:build": "npm run build",
  "frontend:dev:watcher": "npm run dev",
  "frontend:dev:serverUrl": "auto",
  "wailsjsdir": "./internal/panel/frontend/src/wails",
  "frontend:dir": "./internal/panel/frontend",
  "author": {
    "name": "Bioexperiment Lab Devices"
  }
}
```

- [ ] **Step 3: Create the frontend project files**

`internal/panel/frontend/package.json`:

```json
{
  "name": "serialhop-panel-frontend",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "test": "vitest run",
    "test:watch": "vitest",
    "lint": "eslint src --ext .ts,.tsx"
  },
  "dependencies": {
    "react": "^18.3.0",
    "react-dom": "^18.3.0"
  },
  "devDependencies": {
    "@testing-library/jest-dom": "^6.4.0",
    "@testing-library/react": "^16.0.0",
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0",
    "@typescript-eslint/eslint-plugin": "^7.0.0",
    "@typescript-eslint/parser": "^7.0.0",
    "@vitejs/plugin-react": "^4.3.0",
    "eslint": "^8.57.0",
    "eslint-plugin-react": "^7.34.0",
    "eslint-plugin-react-hooks": "^4.6.0",
    "jsdom": "^24.0.0",
    "typescript": "^5.5.0",
    "vite": "^5.2.0",
    "vitest": "^1.5.0"
  }
}
```

`internal/panel/frontend/vite.config.ts`:

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
});
```

`internal/panel/frontend/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "skipLibCheck": true,
    "resolveJsonModule": true,
    "esModuleInterop": true,
    "isolatedModules": true,
    "types": ["vitest/globals", "@testing-library/jest-dom"]
  },
  "include": ["src"]
}
```

`internal/panel/frontend/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>SerialHop</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`internal/panel/frontend/src/main.tsx`:

```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import "./styles/global.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
```

`internal/panel/frontend/src/App.tsx`:

```tsx
export function App() {
  return <div>SerialHop (scaffold)</div>;
}
```

`internal/panel/frontend/src/styles/global.css`:

```css
:root {
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
}
body { margin: 0; }
```

`internal/panel/frontend/src/test/setup.ts`:

```ts
import "@testing-library/jest-dom";
```

`internal/panel/frontend/.gitignore`:

```
node_modules/
dist/
src/wails/
.vite/
*.log
```

`internal/panel/frontend/.eslintrc.cjs`:

```js
module.exports = {
  root: true,
  env: { browser: true, es2022: true, node: true },
  extends: [
    "eslint:recommended",
    "plugin:@typescript-eslint/recommended",
    "plugin:react/recommended",
    "plugin:react-hooks/recommended",
  ],
  parser: "@typescript-eslint/parser",
  plugins: ["@typescript-eslint", "react", "react-hooks"],
  settings: { react: { version: "detect" } },
  rules: {
    "react/react-in-jsx-scope": "off",
  },
};
```

- [ ] **Step 4: Verify scaffold builds**

```bash
cd internal/panel/frontend
npm install
npm run build
cd -
```

Expected: Vite produces `internal/panel/frontend/dist/` with `index.html` + asset bundles.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum wails.json internal/panel/frontend/
git commit -m "build(panel): scaffold Wails v2 + React/Vite/TS frontend project"
```

---

### Task 7: Wails App skeleton — `wails_app.go` + `panel.Run` reimplementation

Create the Wails entry point that replaces the walk-based `panel.go`. At this task it's an empty shell — it opens a window, embeds the frontend, exposes no bindings yet. The existing walk-based `panel.go` is **not** deleted yet (we delete it in the final phase once parity is reached, so intermediate commits keep working).

**Files:**
- Create: `internal/panel/wails_app.go` (`//go:build windows`)
- Create: `internal/panel/wails_app_other.go` (`//go:build !windows`)
- Modify: `internal/panel/panel.go` — temporarily rename the function `walkRun` so the new `Run()` shadows it. (We delete walkRun in Task 28.)

> Why the rename: we want `panel.Run()` to be a single entry point that `cmd/serialhop/main.go` calls. During the transition, the Wails-based `Run()` is the live one; `walkRun()` keeps the old code reachable in git but unreferenced.

- [ ] **Step 1: Rename old entry point**

Edit `internal/panel/panel.go` line ~52: change

```go
func Run() error {
```

to

```go
func walkRun() error { //nolint:unused // kept temporarily during Wails migration; deleted in cleanup task
```

- [ ] **Step 2: Create the Wails app skeleton**

`internal/panel/wails_app.go`:

```go
//go:build windows

package panel

import (
	"context"
	"embed"
	"fmt"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

// App is the Wails application. Bindings methods are defined in
// bindings.go; events emission lives in events.go. The struct itself
// holds the long-lived collaborators (probe goroutines, log tailer,
// service-cli) initialized in startup.
type App struct {
	ctx context.Context
	// Long-lived collaborators wired in startup() — added by later tasks.
}

func newApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Wiring added by later tasks.
}

func (a *App) shutdown(_ context.Context) {
	// Wiring added by later tasks.
}

// Run is the panel-mode entry point invoked from cmd/serialhop/main.go.
// Replaces the walk-based panel from panel.go.
func Run() error {
	app := newApp()
	err := wails.Run(&options.App{
		Title:     "SerialHop v" + version.Base(),
		Width:     980,
		Height:    700,
		MinWidth:  860,
		MinHeight: 580,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		return fmt.Errorf("wails run: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Create the non-Windows stub**

`internal/panel/wails_app_other.go`:

```go
//go:build !windows

package panel

import "errors"

// Run is a non-Windows stub so the package builds on macOS/Linux CI.
// The panel only ships on Windows; on other platforms invoking it is
// a programming error.
func Run() error {
	return errors.New("panel.Run is only available on Windows")
}
```

- [ ] **Step 4: Delete `internal/panel/panel_other.go`**

The old non-Windows stub is replaced by `wails_app_other.go`.

```bash
git rm internal/panel/panel_other.go
```

- [ ] **Step 5: Verify the package builds on macOS/Linux + on Windows cross-compile**

```bash
go build ./internal/panel/
GOOS=windows GOARCH=amd64 go build ./internal/panel/
```

Expected: both succeed. The macOS build uses `wails_app_other.go`; the Windows build uses `wails_app.go`.

- [ ] **Step 6: Confirm existing tests still pass**

```bash
go test -race -count=1 ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/wails_app.go internal/panel/wails_app_other.go internal/panel/panel.go
git rm internal/panel/panel_other.go
git commit -m "feat(panel): add Wails app skeleton (empty bindings; old walk code kept as walkRun for now)"
```

---

### Task 8: Update Taskfile to use `wails build` and update CI to install Node + Wails CLI

**Files:**
- Modify: `Taskfile.yaml`
- Modify: `.github/workflows/pr.yml`
- Modify: `.github/workflows/release-please.yml`
- Modify: `tools/buildcmd/main.go` — may need to be retired if `wails build` covers all of its cases (the engineer checks at task time).

- [ ] **Step 1: Modify `Taskfile.yaml`**

Replace the `build` task body with a Wails-aware version, keeping resource generation as a dep:

```yaml
  build:
    desc: Build the binary (override target via `task build GOOS=... GOARCH=...`)
    deps: [resource]
    cmds:
      - wails build -platform {{.GOOS}}/{{.GOARCH}} -o {{.BINARY_NAME}} -ldflags="-X 'github.com/bioexperiment-lab-devices/serialhop/internal/version.Version={{.VERSION | default "dev"}}'"
      - cmd: mkdir -p {{.OUTPUT_DIR}}
        platforms: [linux, darwin]
      - cmd: powershell -Command "New-Item -ItemType Directory -Force -Path {{.OUTPUT_DIR}} | Out-Null"
        platforms: [windows]
      - cmd: mv build/bin/{{.BINARY_NAME}} {{.OUTPUT_DIR}}/{{.BINARY_NAME}}
        platforms: [linux, darwin]
      - cmd: powershell -Command "Move-Item -Force build/bin/{{.BINARY_NAME}} {{.OUTPUT_DIR}}/{{.BINARY_NAME}}"
        platforms: [windows]
```

If `wails build` requires the manifest/icon embedded via Wails-side config rather than the existing `.syso` flow, the engineer adjusts at task time. The minimum requirement is: `task build` produces `dist/SerialHop.exe` and that binary contains the new Wails-embedded frontend.

- [ ] **Step 2: Modify `.github/workflows/pr.yml` — add Node setup + frontend build**

Insert after the `actions/setup-go@v6` step:

```yaml
      - uses: actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: internal/panel/frontend/package-lock.json

      - name: install Wails CLI
        run: go install github.com/wailsapp/wails/v2/cmd/wails@latest

      - name: install frontend deps
        run: npm ci
        working-directory: internal/panel/frontend

      - name: frontend build
        run: npm run build
        working-directory: internal/panel/frontend

      - name: frontend tests
        run: npm test
        working-directory: internal/panel/frontend
```

Modify the `cross-compile windows binary` step to use `wails build` instead of `go build`:

```yaml
      - name: cross-compile windows binary
        run: |
          wails build -platform windows/amd64 \
            -ldflags="-X 'github.com/bioexperiment-lab-devices/serialhop/internal/version.Version=ci-pr-${{ github.event.pull_request.number }}'"
          cp build/bin/SerialHop.exe /tmp/SerialHop.exe
```

- [ ] **Step 3: Modify `.github/workflows/release-please.yml`**

In the `release-build` job, add the same Node setup + Wails CLI install steps (after the existing `setup-go`). The `build` step (`task build`) keeps working because Taskfile now invokes `wails build` internally — but the `frontend:install` step in `wails.json` runs `npm install` so add an explicit `npm ci` step before `task build` to make the cache deterministic:

```yaml
      - name: install frontend deps
        run: npm ci
        working-directory: internal/panel/frontend

      - name: build
        run: task build VERSION=${{ needs.release-please.outputs.tag_name }}
        shell: bash
```

(`VERSION=` substitution requires adjusting Taskfile's `build` task to honor a `VERSION` variable for `-ldflags`. The example above already does that.)

- [ ] **Step 4: Run `pr.yml` locally (or just push a draft PR)**

If `act` is available locally: `act -W .github/workflows/pr.yml`. Otherwise validate by opening a draft PR. Verify the workflow passes.

- [ ] **Step 5: Commit**

```bash
git add Taskfile.yaml .github/workflows/pr.yml .github/workflows/release-please.yml
git commit -m "build(ci): integrate Wails CLI + Node 22 into PR and release pipelines"
```

---

## Phase 4 — Go-side bindings and events

### Task 9: Add binding method signatures + DTOs (still returning zero values)

This task wires up every binding in the spec §11.1 as a method on the `App` struct, but with placeholder implementations. The goal is: Wails auto-generates TS shims with the right shapes; later tasks fill in the bodies.

**Files:**
- Create: `internal/panel/bindings.go` (`//go:build windows`)
- Create: `internal/panel/bindings_other.go` (`//go:build !windows`) — empty stubs so the package compiles on macOS/Linux for tests that import it.

- [ ] **Step 1: Create `internal/panel/bindings.go`**

```go
//go:build windows

package panel

import (
	"context"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// --- DTOs declared just for the binding surface. ---

// FieldError pairs a config field path (dot-separated for nested structs,
// e.g. "lab_bridge.host") with a human-readable detail string.
type FieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

type SaveResult struct {
	OK          bool         `json:"ok"`
	FieldErrors []FieldError `json:"field_errors,omitempty"`
}

type CredsResult struct {
	Outcome string `json:"outcome"` // "ok" | "unauthorized" | "needs_confirm" | "skipped"
	Detail  string `json:"detail,omitempty"`
}

type AdminResult struct {
	OK           bool   `json:"ok"`
	ErrorMessage string `json:"error_message,omitempty"`
	Cancelled    bool   `json:"cancelled,omitempty"`
}

type ServiceTabStatusDTO struct {
	Reachable bool   `json:"reachable"`
	Reason    string `json:"reason,omitempty"` // "service_down" | "unreachable" | ""
}

// --- Bindings ---

func (a *App) GetVersion() string { return version.Base() }

func (a *App) LoadConfigFromDisk() config.Config {
	// Implemented in Task 10.
	return config.Default()
}

func (a *App) ValidateConfig(_ config.Config) []FieldError {
	// Implemented in Task 10.
	return nil
}

func (a *App) SaveConfig(_ config.Config) SaveResult {
	// Implemented in Task 10.
	return SaveResult{OK: false}
}

func (a *App) VerifyCredentials(_, _, _ string) CredsResult {
	// Implemented in Task 11.
	return CredsResult{Outcome: "ok"}
}

func (a *App) OpenConfigInEditor() error    { return nil } // Implemented in Task 10.
func (a *App) OpenLogsFolder() error        { return nil } // Implemented in Task 14.
func (a *App) OpenReleaseNotes() error      { return nil } // Implemented in Task 13.
func (a *App) PickBackupDir() string        { return "" }  // Implemented in Task 10.

func (a *App) InstallService() AdminResult   { return AdminResult{} } // Implemented in Task 12.
func (a *App) UninstallService() AdminResult { return AdminResult{} } // Implemented in Task 12.
func (a *App) RestartService() AdminResult   { return AdminResult{} } // Implemented in Task 12.

func (a *App) TriggerProbe(_ string) {}           // Implemented in Task 15.
func (a *App) CheckForUpdate()       {}           // Implemented in Task 13.
func (a *App) DownloadUpdate()       {}           // Implemented in Task 13.
func (a *App) CancelDownload()       {}           // Implemented in Task 13.
func (a *App) InstallUpdate() AdminResult {       // Implemented in Task 13.
	return AdminResult{}
}

func (a *App) GetDevices(_ context.Context) (api.DevicesResponse, ServiceTabStatusDTO) {
	return api.DevicesResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}
func (a *App) Discover(_ context.Context) (api.DevicesResponse, ServiceTabStatusDTO) {
	return api.DevicesResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}
func (a *App) DisconnectAll(_ context.Context) (api.DisconnectResponse, ServiceTabStatusDTO) {
	return api.DisconnectResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}
func (a *App) GetPorts(_ context.Context) (api.DetailedPortsResponse, ServiceTabStatusDTO) {
	return api.DetailedPortsResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}

func (a *App) StartLogStream(_ string) {} // Implemented in Task 14.
func (a *App) StopLogStream()          {} // Implemented in Task 14.
```

- [ ] **Step 2: Verify Wails generates the TS shims**

```bash
cd internal/panel/frontend
npm run dev  # or: wails generate module
```

Expected: `internal/panel/frontend/src/wails/` populated with `.d.ts` and `.js` files mirroring the binding signatures. Cancel the dev server once shims appear.

- [ ] **Step 3: Smoke-build the binary**

```bash
GOOS=windows GOARCH=amd64 go build ./cmd/serialhop
```

Expected: PASS (no link errors).

- [ ] **Step 4: Run all tests**

```bash
go test -race -count=1 ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go
git commit -m "feat(panel): declare Wails binding signatures (placeholder bodies; impl in follow-ups)"
```

---

### Task 10: Implement config bindings — `LoadConfigFromDisk`, `ValidateConfig`, `SaveConfig`, `OpenConfigInEditor`, `PickBackupDir`

**Files:**
- Modify: `internal/panel/bindings.go`
- Test: `internal/panel/bindings_config_test.go` (new)

- [ ] **Step 1: Write failing tests**

`internal/panel/bindings_config_test.go`:

```go
//go:build windows

package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func TestSaveConfig_WritesYAMLAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	app := newApp()
	cfg := config.Default()
	cfg.LabBridge.User = "alice"
	cfg.LabBridge.Pass = "pw"
	cfg.LabBridge.Host = "h.example"

	res := app.SaveConfig(cfg)
	if !res.OK {
		t.Fatalf("SaveConfig: %+v", res)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LabBridge.User != "alice" || got.LabBridge.Pass != "pw" || got.LabBridge.Host != "h.example" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestSaveConfig_ValidationFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	app := newApp()
	cfg := config.Default() // user/pass empty → validation fails
	res := app.SaveConfig(cfg)
	if res.OK {
		t.Errorf("expected save to fail validation")
	}
	if len(res.FieldErrors) == 0 {
		t.Errorf("expected field errors, got none")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("yaml should not have been written on validation failure")
	}
}

func TestValidateConfig_HappyPath(t *testing.T) {
	app := newApp()
	cfg := config.Default()
	cfg.LabBridge.User = "alice"
	cfg.LabBridge.Pass = "pw"
	if errs := app.ValidateConfig(cfg); len(errs) != 0 {
		t.Errorf("expected no errors, got %+v", errs)
	}
}

func TestLoadConfigFromDisk_ReturnsDefaultWhenMissing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nope.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	got := newApp().LoadConfigFromDisk()
	if got.LabBridge.Host != config.Default().LabBridge.Host {
		t.Errorf("expected default; got %+v", got)
	}
}
```

- [ ] **Step 2: Confirm fail**

Run: `GOOS=windows go test ./internal/panel/ -tags=windows -run TestSaveConfig -v` — or, easier: run on a Windows dev box. macOS users skip and rely on CI.

Actually, since the bindings are Windows-tagged, the tests live in the same tag. On non-Windows hosts these tests are not even compiled — that's OK; CI on Windows would catch them. For pre-CI iteration, build with `GOOS=windows`.

- [ ] **Step 3: Add the config-path test hook + implementations**

Edit `internal/panel/bindings.go` (replace the four config-related placeholder methods + import `gopkg.in/yaml.v3`):

```go
import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// resolveConfigPath returns paths.ConfigPath() unless the test hook is
// set (SERIALHOP_TEST_CONFIG_PATH). Used to make config bindings
// unit-testable without touching ProgramData.
func resolveConfigPath() string {
	if p := os.Getenv("SERIALHOP_TEST_CONFIG_PATH"); p != "" {
		return p
	}
	return paths.ConfigPath()
}

func (a *App) LoadConfigFromDisk() config.Config {
	p := resolveConfigPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return config.Default()
	}
	c, err := config.Load(p)
	if err != nil {
		// Surface as a warn header via event; return default so the form
		// is still editable.
		a.emitWarn("Config file unreadable: " + err.Error())
		return config.Default()
	}
	return c
}

func (a *App) ValidateConfig(cfg config.Config) []FieldError {
	if err := config.Validate(cfg); err != nil {
		return []FieldError{{Field: extractField(err), Detail: err.Error()}}
	}
	return nil
}

// extractField pulls a dot-path out of a config.Validate error when one is
// present. config.Validate returns wrapped errors like `"lab_bridge.host:
// required"`; the prefix up to the first ":" is the field path. Falls
// back to empty string (which the UI maps to a global banner).
func extractField(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if idx := strings.Index(msg, ":"); idx > 0 {
		candidate := msg[:idx]
		// Reject anything that doesn't look like a dot-path (e.g. "open file: …").
		if !strings.ContainsAny(candidate, " ") {
			return candidate
		}
	}
	return ""
}

func (a *App) SaveConfig(cfg config.Config) SaveResult {
	if errs := a.ValidateConfig(cfg); len(errs) > 0 {
		return SaveResult{OK: false, FieldErrors: errs}
	}
	p := resolveConfigPath()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return SaveResult{OK: false, FieldErrors: []FieldError{{Detail: err.Error()}}}
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return SaveResult{OK: false, FieldErrors: []FieldError{{Detail: err.Error()}}}
	}
	a.emitEvent("config:saved", nil)
	return SaveResult{OK: true}
}

func (a *App) OpenConfigInEditor() error {
	return OpenWithDefaultApp(resolveConfigPath())
}

func (a *App) PickBackupDir() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose firmware backup directory",
	})
	if err != nil {
		return ""
	}
	return dir
}
```

Add `OpenWithDefaultApp` if it's not already exported from `internal/panel`. The current code has it as an unexported function in `panel.go`; promote it to exported in a new file:

`internal/panel/open.go`:

```go
//go:build windows

package panel

import (
	"os/exec"
	"syscall"
)

// OpenWithDefaultApp launches the OS's default handler for the given
// path or URL. Exported for use from bindings.go. The "rundll32" form
// avoids cmd.exe's quoting hazards.
func OpenWithDefaultApp(target string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
```

(If `OpenWithDefaultApp` is already exported in `panel.go`, skip the new file; this is a refactor-during-migration step the engineer judges.)

Add `emitWarn` and `emitEvent` helpers to `internal/panel/events.go`:

`internal/panel/events.go`:

```go
//go:build windows

package panel

import "github.com/wailsapp/wails/v2/pkg/runtime"

// emitEvent is a thin wrapper around runtime.EventsEmit that no-ops
// when ctx is nil (i.e. before startup completes — used in early-life
// log lines we don't want to surface).
func (a *App) emitEvent(name string, data interface{}) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, name, data)
}

func (a *App) emitWarn(msg string) {
	a.emitEvent("warn:set", map[string]string{"message": msg})
}

func (a *App) clearWarn() {
	a.emitEvent("warn:clear", nil)
}
```

- [ ] **Step 4: Run tests**

```bash
GOOS=windows go test -tags=windows -count=1 ./internal/panel/ -run "TestSaveConfig|TestValidateConfig|TestLoadConfigFromDisk"
```

If the workflow runs CI on Windows, defer the failing assertion check to the CI run.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go internal/panel/events.go internal/panel/open.go internal/panel/bindings_config_test.go
git commit -m "feat(panel): implement config bindings — Load/Validate/Save + OpenInEditor + PickBackupDir"
```

---

### Task 11: Implement `VerifyCredentials` binding via `CredVerify`

**Files:**
- Modify: `internal/panel/bindings.go`
- Test: existing `credverify_test.go` already covers the state machine; here we wire it into the binding.

- [ ] **Step 1: Add the real verifier glue**

Add to `internal/panel/bindings.go`:

```go
// liveCredVerifier wraps the existing verifyCredentials helper in firstrun.go
// so CredVerify can call it through the CredVerifier interface.
type liveCredVerifier struct {
	hc *http.Client
}

func (l *liveCredVerifier) Verify(ctx context.Context, host, user, pass string) (CredsCheckResult, error) {
	// verifyCredentials lives in firstrun.go and returns its own result type;
	// the call below translates that to CredsCheckResult.
	res, err := verifyCredentialsWithCtx(ctx, l.hc, host, user, pass)
	return res, err
}

// VerifyCredentials runs the verify-then-save state machine for the
// CURRENT form vs the on-disk YAML. Returns a categorical outcome the
// TS side maps to inline errors / confirm modals (spec §5.9).
func (a *App) VerifyCredentials(newHost, newUser, newPass string) CredsResult {
	old := a.LoadConfigFromDisk()
	cv := NewCredVerifier(&liveCredVerifier{hc: &http.Client{Timeout: 10 * time.Second}})
	dec, err := cv.Decide(a.ctx, CredChange{
		OldUser: old.LabBridge.User, OldPass: old.LabBridge.Pass,
		NewHost: newHost, NewUser: newUser, NewPass: newPass,
	})
	if err != nil && dec.Outcome == "" {
		return CredsResult{Outcome: "needs_confirm", Detail: err.Error()}
	}
	return CredsResult{Outcome: dec.Outcome, Detail: dec.Detail}
}
```

Add imports `net/http`, `time` to `bindings.go` if not already present.

- [ ] **Step 2: Extract `verifyCredentialsWithCtx` from `firstrun.go`**

Open `internal/panel/firstrun.go`. The existing `verifyCredentials` likely has a signature without a context arg; refactor it to accept ctx so the binding can pass it down. Add a non-breaking wrapper for any existing callers if needed. The exact diff depends on what `verifyCredentials` looks like at task time; the engineer reads the file and adjusts.

The return type should be `(CredsCheckResult, error)` to match `CredVerifier.Verify`.

- [ ] **Step 3: Update or add tests**

The existing `firstrun_test.go` covers the network path. Add to `bindings_config_test.go`:

```go
//go:build windows

package panel

import "testing"

func TestVerifyCredentials_UnchangedSkipsVerify(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/cfg.yaml"
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	// Seed an on-disk config to compare against.
	app := newApp()
	if !app.SaveConfig(seedValidConfig()).OK {
		t.Fatalf("seed save failed")
	}

	res := app.VerifyCredentials("h.example", "alice", "pw")
	// Since user/pass match the saved values, network is skipped.
	if res.Outcome != "skipped" {
		t.Errorf("Outcome: got %q, want skipped", res.Outcome)
	}
}

func seedValidConfig() config.Config {
	c := config.Default()
	c.LabBridge.Host = "h.example"
	c.LabBridge.User = "alice"
	c.LabBridge.Pass = "pw"
	return c
}
```

- [ ] **Step 4: Run tests**

```bash
GOOS=windows go test -tags=windows -count=1 ./internal/panel/ -run "TestVerifyCredentials"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go internal/panel/bindings_config_test.go internal/panel/firstrun.go
git commit -m "feat(panel): wire VerifyCredentials binding to CredVerify"
```

---

### Task 12: Implement service-control bindings (`InstallService` / `UninstallService` / `RestartService`)

These wrap the existing `RunElevatedAdminAction` UAC helper.

**Files:**
- Modify: `internal/panel/bindings.go`

- [ ] **Step 1: Replace the three placeholder implementations**

Add to `internal/panel/bindings.go`:

```go
func (a *App) InstallService() AdminResult   { return a.runAdmin("install", "Service installed") }
func (a *App) UninstallService() AdminResult { return a.runAdmin("uninstall", "Service uninstalled") }
func (a *App) RestartService() AdminResult   { return a.runAdmin("restart", "Service restarted") }

func (a *App) runAdmin(action, successMsg string) AdminResult {
	a.emitEvent("footer:set", map[string]string{"kind": "work", "text": "Working…"})
	errMsg, err := RunElevatedAdminAction(action)
	switch {
	case errors.Is(err, ErrUserCancelled):
		a.emitEvent("footer:set", map[string]string{"kind": "info", "text": "Cancelled."})
		return AdminResult{Cancelled: true}
	case err != nil:
		a.emitEvent("footer:set", map[string]interface{}{"kind": "err", "text": "Failed: " + err.Error()})
		return AdminResult{ErrorMessage: err.Error()}
	case errMsg != "":
		a.emitEvent("footer:set", map[string]interface{}{"kind": "err", "text": "Failed: " + errMsg})
		return AdminResult{ErrorMessage: errMsg}
	}
	a.emitEvent("footer:set", map[string]string{
		"kind": "ok",
		"text": successMsg + " at " + time.Now().Format("15:04:05"),
	})
	return AdminResult{OK: true}
}
```

Add `"errors"` to imports if not present.

- [ ] **Step 2: Run all tests**

```bash
go test -race -count=1 ./...
```

Expected: PASS (the new code is Windows-tagged; macOS CI skips it).

- [ ] **Step 3: Commit**

```bash
git add internal/panel/bindings.go
git commit -m "feat(panel): implement service-control bindings (Install/Uninstall/Restart via UAC)"
```

---

### Task 13: Implement update-flow bindings + event emission

Wires the existing `runUpdateCheck`, `ctlDownload`, `ctlInstall` flows into Wails bindings + `update:state` events.

**Files:**
- Modify: `internal/panel/bindings.go`
- Modify: `internal/panel/wails_app.go` — add `updateCtl` to App struct, kick off the launch + 6h-recheck goroutines in `startup`.

- [ ] **Step 1: Augment App struct**

In `wails_app.go`, expand the App struct + startup wiring:

```go
type App struct {
	ctx      context.Context
	updateCh *updateCtl
	hc       *http.Client
	logTail  *logTailController // initialized in Task 14
}

func newApp() *App {
	return &App{
		updateCh: &updateCtl{},
		hc:       &http.Client{},
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cfg, _ := config.LoadPartial(paths.ConfigPath())
	if cfg.AutoUpdate.Enabled {
		go func() {
			time.Sleep(500 * time.Millisecond)
			runUpdateCheckEvent(a)
		}()
		go a.updateRecheckLoop(ctx)
	}
}

func (a *App) updateRecheckLoop(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runUpdateCheckEvent(a)
		}
	}
}
```

(`runUpdateCheckEvent` is implemented next; it adapts the existing `runUpdateCheck` to emit events instead of calling `apply`.)

- [ ] **Step 2: Adapt the update flow to emit events**

Add at the bottom of `internal/panel/bindings.go` (or a new `update_bindings.go` if `bindings.go` grows past ~400 lines):

```go
func (a *App) CheckForUpdate() { go runUpdateCheckEvent(a) }

func (a *App) DownloadUpdate() {
	go ctlDownloadEvent(a)
}

func (a *App) CancelDownload() {
	a.updateCh.mu.Lock()
	cancel := a.updateCh.dlCancel
	a.updateCh.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) InstallUpdate() AdminResult { return ctlInstallEvent(a) }

func (a *App) OpenReleaseNotes() error {
	a.updateCh.mu.Lock()
	url := a.updateCh.release.HTMLURL
	a.updateCh.mu.Unlock()
	if url == "" {
		return nil
	}
	return OpenWithDefaultApp(url)
}

// runUpdateCheckEvent is the event-emitting counterpart of runUpdateCheck.
// Reuses the same checks; replaces apply(EvUpdateAvailable) etc. with
// emitEvent("update:state", …).
func runUpdateCheckEvent(a *App) {
	// Body lifted from the existing runUpdateCheck — only the apply()
	// callbacks change.  See panel.go for the original. Replace each
	// apply(EvX) site with a.updateCh.applyEvent(a, EvX) below.
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	installDir := filepath.Dir(exePath)
	updateUA := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := updater.LatestRelease(ctx, a.hc, updater.DefaultReleasesURL, updateUA)
	if err != nil {
		writePanelDebugLog("update_check_failed", err)
		return
	}
	newer, err := updater.IsNewer(rel.TagName, version.Version)
	if err != nil || !newer {
		return
	}
	var exeAsset *updater.Asset
	for i := range rel.Assets {
		name := rel.Assets[i].Name
		if strings.HasPrefix(name, "SerialHop-v") && strings.HasSuffix(name, ".exe") {
			exeAsset = &rel.Assets[i]
			break
		}
	}
	if exeAsset == nil {
		return
	}
	stagedPath := filepath.Join(installDir, exeAsset.Name)
	if _, err := os.Stat(stagedPath); err == nil {
		sumsAsset := rel.AssetByName("SHA256SUMS.txt")
		if sumsAsset != nil {
			body, ferr := fetchSums(a.hc, updateUA, sumsAsset.BrowserDownloadURL)
			if ferr == nil && updater.VerifyFile(stagedPath, body, exeAsset.Name) == nil {
				a.updateCh.mu.Lock()
				a.updateCh.release = rel
				a.updateCh.exeAsset = exeAsset
				a.updateCh.exeFile = stagedPath
				a.updateCh.mu.Unlock()
				a.applyUpdateEvent(EvUpdateAvailable)
				a.applyUpdateEvent(EvDownloadStart)
				a.applyUpdateEvent(EvDownloadOK)
				cleanupStaleStagedFiles(installDir, exeAsset.Name)
				return
			}
		}
		_ = os.Remove(stagedPath)
	}
	cleanupStaleStagedFiles(installDir, exeAsset.Name)
	a.updateCh.mu.Lock()
	a.updateCh.release = rel
	a.updateCh.exeAsset = exeAsset
	a.updateCh.mu.Unlock()
	a.applyUpdateEvent(EvUpdateAvailable)
}

// applyUpdateEvent advances the update state machine and emits update:state.
func (a *App) applyUpdateEvent(ev UpdateEvent) {
	a.updateCh.mu.Lock()
	a.updateCh.state = nextUpdateState(a.updateCh.state, ev)
	st := a.updateCh.state
	tag := a.updateCh.release.TagName
	a.updateCh.mu.Unlock()
	a.emitEvent("update:state", map[string]interface{}{
		"state":       int(st),
		"release_tag": tag,
	})
}

// ctlDownloadEvent: lifted from ctlDownload, emitting events + footer
// progress instead of mw.Synchronize calls.
func ctlDownloadEvent(a *App) {
	a.updateCh.mu.Lock()
	rel := a.updateCh.release
	asset := a.updateCh.exeAsset
	a.updateCh.mu.Unlock()
	if asset == nil {
		return
	}
	exePath, _ := os.Executable()
	installDir := filepath.Dir(exePath)
	updateUA := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	a.updateCh.mu.Lock()
	a.updateCh.dlCancel = cancel
	a.updateCh.mu.Unlock()
	defer func() {
		a.updateCh.mu.Lock()
		a.updateCh.dlCancel = nil
		a.updateCh.mu.Unlock()
		cancel()
	}()
	a.applyUpdateEvent(EvDownloadStart)
	dest := filepath.Join(installDir, asset.Name)
	var lastReport time.Time
	progress := func(received, total int64) {
		if time.Since(lastReport) < 200*time.Millisecond && (total <= 0 || received < total) {
			return
		}
		lastReport = time.Now()
		pct := 0
		if total > 0 {
			pct = int(float64(received) / float64(total) * 100)
		}
		a.emitEvent("footer:set", map[string]interface{}{
			"kind":     "work",
			"text":     fmt.Sprintf("Downloading %d%% (%.1f MB)", pct, float64(received)/1e6),
			"progress": pct,
		})
	}
	if err := updater.Download(ctx, a.hc, asset.BrowserDownloadURL, dest, updateUA, progress); err != nil {
		if errors.Is(err, context.Canceled) {
			a.emitEvent("footer:set", map[string]string{"kind": "info", "text": "Download cancelled."})
			a.applyUpdateEvent(EvCancel)
			return
		}
		writePanelDebugLog("update_download_failed", err)
		a.applyUpdateEvent(EvDownloadFail)
		return
	}
	sumsAsset := rel.AssetByName("SHA256SUMS.txt")
	if sumsAsset == nil {
		_ = os.Remove(dest)
		a.applyUpdateEvent(EvDownloadFail)
		return
	}
	body, err := fetchSums(a.hc, updateUA, sumsAsset.BrowserDownloadURL)
	if err != nil || updater.VerifyFile(dest, body, asset.Name) != nil {
		_ = os.Remove(dest)
		a.applyUpdateEvent(EvDownloadFail)
		return
	}
	a.updateCh.mu.Lock()
	a.updateCh.exeFile = dest
	a.updateCh.mu.Unlock()
	a.emitEvent("footer:set", map[string]string{"kind": "ok", "text": "Download complete."})
	a.applyUpdateEvent(EvDownloadOK)
}

// ctlInstallEvent: lifted from ctlInstall, returns the AdminResult to
// the binding caller AND emits footer events.
func ctlInstallEvent(a *App) AdminResult {
	a.updateCh.mu.Lock()
	src := a.updateCh.exeFile
	a.updateCh.mu.Unlock()
	if src == "" {
		return AdminResult{ErrorMessage: "no staged file"}
	}
	a.applyUpdateEvent(EvInstallStart)
	a.emitEvent("footer:set", map[string]string{"kind": "work", "text": "Installing update…"})
	errMsg, err := RunElevatedAdminAction("update", "--update-src="+src)
	switch {
	case errors.Is(err, ErrUserCancelled):
		a.applyUpdateEvent(EvCancel)
		return AdminResult{Cancelled: true}
	case err != nil:
		a.applyUpdateEvent(EvInstallFail)
		return AdminResult{ErrorMessage: err.Error()}
	case errMsg != "":
		a.applyUpdateEvent(EvInstallFail)
		return AdminResult{ErrorMessage: errMsg}
	}
	a.applyUpdateEvent(EvInstallOK)
	return AdminResult{OK: true}
}
```

Imports to add: `os`, `path/filepath`, `strings`, `errors`, `fmt`,
`github.com/bioexperiment-lab-devices/serialhop/internal/updater`,
`github.com/bioexperiment-lab-devices/serialhop/internal/paths`.

- [ ] **Step 2: Run tests**

```bash
go test -race -count=1 ./...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/panel/bindings.go internal/panel/wails_app.go
git commit -m "feat(panel): implement auto-update bindings + update:state event emission"
```

---

### Task 14: Implement log-streaming bindings + tail goroutine controller

**Files:**
- Modify: `internal/panel/bindings.go`
- Create: `internal/panel/log_tail_controller.go` (`//go:build windows`)

- [ ] **Step 1: Create the controller**

`internal/panel/log_tail_controller.go`:

```go
//go:build windows

package panel

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// logTailController owns at most one FileTail goroutine. Switching
// streams stops the existing tailer and starts a new one.
type logTailController struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	stream string
}

func (c *logTailController) start(streamID string, emit func(name string, data interface{})) {
	c.stop()
	path, ok := streamPath(streamID)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.stream = streamID
	c.mu.Unlock()
	parse := streamID == "service" // service log is slog JSON; others are raw
	onLine := func(line string) {
		payload := map[string]interface{}{"stream": streamID}
		if parse {
			var rec map[string]interface{}
			if json.Unmarshal([]byte(line), &rec) == nil {
				payload["record"] = rec
			} else {
				payload["raw"] = line
			}
		} else {
			payload["raw"] = line
		}
		emit("log:line", payload)
	}
	onRotate := func() {
		emit("log:rotated", map[string]string{"stream": streamID})
	}
	tailer := NewFileTail(path, 500*time.Millisecond, onLine, onRotate)
	go tailer.Run(ctx)
}

func (c *logTailController) stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
		c.stream = ""
	}
	c.mu.Unlock()
}

// streamPath maps a binding-level stream id to the on-disk path.
// Returns ok=false for unknown ids — the binding silently no-ops.
func streamPath(id string) (string, bool) {
	switch id {
	case "service":
		return paths.ServiceLogPath(), true
	case "stderr":
		return paths.StderrLogPath(), true
	case "panel":
		return paths.PanelErrorLogPath(), true
	}
	return "", false
}
```

- [ ] **Step 2: Wire into bindings**

In `bindings.go`, replace the two placeholders:

```go
func (a *App) StartLogStream(id string) {
	if a.logTail == nil {
		a.logTail = &logTailController{}
	}
	a.logTail.start(id, a.emitEvent)
}

func (a *App) StopLogStream() {
	if a.logTail == nil {
		return
	}
	a.logTail.stop()
}

func (a *App) OpenLogsFolder() error {
	return OpenWithDefaultApp(paths.LogsDir())
}
```

- [ ] **Step 3: Run tests**

```bash
go test -race -count=1 ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/log_tail_controller.go internal/panel/bindings.go
git commit -m "feat(panel): implement Start/StopLogStream + parse slog-JSON records"
```

---

### Task 15: Implement Devices/Ports bindings via `ServiceCli`

**Files:**
- Modify: `internal/panel/bindings.go`
- Modify: `internal/panel/wails_app.go` — initialize `*ServiceCli` in `startup`.

- [ ] **Step 1: Wire ServiceCli into App**

Edit `wails_app.go`:

```go
type App struct {
	ctx      context.Context
	updateCh *updateCtl
	hc       *http.Client
	logTail  *logTailController
	svc      *ServiceCli
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, _ := config.LoadPartial(paths.ConfigPath())
	a.svc = NewServiceCli(paths.ServerInfoCachePath(), cfg.LabBridge.User)
	// ... existing auto-update wiring unchanged
}
```

- [ ] **Step 2: Implement the four bindings**

Replace the placeholder methods in `bindings.go`:

```go
func toTabStatus(s ServiceCliStatus) ServiceTabStatusDTO {
	switch s {
	case StatusOK:
		return ServiceTabStatusDTO{Reachable: true}
	case StatusServiceDown:
		return ServiceTabStatusDTO{Reachable: false, Reason: "service_down"}
	}
	return ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}

func (a *App) GetDevices(ctx context.Context) (api.DevicesResponse, ServiceTabStatusDTO) {
	resp, st, _ := a.svc.GetDevices(ctx)
	return resp, toTabStatus(st)
}

func (a *App) Discover(ctx context.Context) (api.DevicesResponse, ServiceTabStatusDTO) {
	resp, st, _ := a.svc.Discover(ctx)
	return resp, toTabStatus(st)
}

func (a *App) DisconnectAll(ctx context.Context) (api.DisconnectResponse, ServiceTabStatusDTO) {
	resp, st, _ := a.svc.DisconnectAll(ctx)
	if st == StatusOK {
		a.emitEvent("footer:set", map[string]interface{}{
			"kind": "ok",
			"text": fmt.Sprintf("Disconnected %d device(s).", resp.Released),
		})
	}
	return resp, toTabStatus(st)
}

func (a *App) GetPorts(ctx context.Context) (api.DetailedPortsResponse, ServiceTabStatusDTO) {
	resp, st, _ := a.svc.GetPorts(ctx)
	return resp, toTabStatus(st)
}
```

- [ ] **Step 3: Run tests**

```bash
go test -race -count=1 ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/bindings.go internal/panel/wails_app.go
git commit -m "feat(panel): implement Devices/Ports bindings via ServiceCli"
```

---

### Task 16: Wire probe loops + SCM polling to emit `status:lamp` events

Reuse the existing `probe.go` + `state.go` + `lampstate.go`. Move the goroutines from the old `panel.go` flow into `wails_app.go.startup`.

**Files:**
- Modify: `internal/panel/wails_app.go`

- [ ] **Step 1: Add lamp-state + goroutine fields to App**

```go
type App struct {
	ctx       context.Context
	updateCh  *updateCtl
	hc        *http.Client
	logTail   *logTailController
	svc       *ServiceCli
	lamps     *lampState
	probeCtx  context.Context
	probeStop context.CancelFunc
	serverTrigger chan struct{}
	tunnelTrigger chan struct{}
	lastService winsvc.ServiceState // last-known SCM state for stickiness
}

func newApp() *App {
	return &App{
		updateCh: &updateCtl{},
		hc:       &http.Client{},
		lamps: &lampState{
			server: netLamp{kind: lampChecking},
			tunnel: netLamp{kind: lampChecking},
		},
		serverTrigger: make(chan struct{}, 1),
		tunnelTrigger: make(chan struct{}, 1),
		lastService:   winsvc.StateNotInstalled,
	}
}
```

- [ ] **Step 2: Implement the loop launch in startup**

Append to `startup(ctx)`:

```go
	a.probeCtx, a.probeStop = context.WithCancel(ctx)
	probeHC := &http.Client{Timeout: 30 * time.Second}
	userAgent := "SerialHop/" + version.Base() + " (status-probe)"

	go probeLoop(a.probeCtx, 30*time.Second, a.serverTrigger, func(ctx context.Context) {
		c, _ := config.LoadPartial(paths.ConfigPath())
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runServerProbe(ctx, probeHC, base, userAgent, a.lamps)
		a.emitServerLamp()
	})
	go probeLoop(a.probeCtx, 30*time.Second, a.tunnelTrigger, func(ctx context.Context) {
		c, _ := config.LoadPartial(paths.ConfigPath())
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runTunnelProbe(ctx, probeHC, base, c.LabBridge.User, c.LabBridge.Pass, userAgent, a.lamps)
		a.emitTunnelLamp()
	})
	go a.scmPollLoop(a.probeCtx)
```

Also add the per-lamp emit helpers + the SCM poll:

```go
func (a *App) emitServerLamp() {
	_, srv, _ := a.lamps.snapshot()
	color, text := serverLampPresentation(srv)
	a.emitEvent("status:lamp", map[string]string{
		"which": "server",
		"tone":  toneString(color),
		"label": text,
	})
}

func (a *App) emitTunnelLamp() {
	_, _, tun := a.lamps.snapshot()
	color, text := tunnelLampPresentation(tun)
	a.emitEvent("status:lamp", map[string]string{
		"which": "tunnel",
		"tone":  toneString(color),
		"label": text,
	})
}

func (a *App) emitServiceLamp() {
	svc, _, _ := a.lamps.snapshot()
	color, text := serviceLampPresentation(svc)
	a.emitEvent("status:lamp", map[string]string{
		"which": "service",
		"tone":  toneString(color),
		"label": text,
	})
}

func toneString(c StatusColor) string {
	switch c {
	case ColorGreen:
		return "green"
	case ColorYellow:
		return "yellow"
	case ColorRed:
		return "red"
	}
	return "grey"
}

func (a *App) scmPollLoop(ctx context.Context) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		scmState, ok := queryServiceState()
		if !ok {
			scmState = a.lastService
		} else {
			a.lastService = scmState
		}
		cfg, cfgErr := config.LoadPartial(paths.ConfigPath())
		_ = cfg
		newSvc := serviceLamp{state: scmState, cfgValid: cfgErr == nil}
		old, _, _ := a.lamps.snapshot()
		a.lamps.setService(newSvc)
		if old.state != newSvc.state || old.cfgValid != newSvc.cfgValid {
			a.emitServiceLamp()
		}
		// Warn header tracking.
		if cfgErr != nil {
			a.emitWarn("⚠ " + cfgErr.Error())
		} else {
			a.clearWarn()
		}
	}
}
```

The `queryServiceState` function exists in the current `panel.go` — promote it from local to a package-level (still Windows-tagged) function. The engineer can either move it as part of this task or leave it in `panel.go` and reference it from `wails_app.go` (both are in package `panel` so it's visible).

- [ ] **Step 3: Implement TriggerProbe binding**

In `bindings.go`:

```go
func (a *App) TriggerProbe(which string) {
	switch which {
	case "server":
		a.lamps.setServer(netLamp{kind: lampChecking})
		a.emitServerLamp()
		trySend(a.serverTrigger)
	case "tunnel":
		a.lamps.setTunnel(netLamp{kind: lampChecking})
		a.emitTunnelLamp()
		trySend(a.tunnelTrigger)
	}
}
```

- [ ] **Step 4: Run tests + cross-compile to Windows**

```bash
go test -race -count=1 ./...
GOOS=windows GOARCH=amd64 go build ./cmd/serialhop
```

Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/wails_app.go internal/panel/bindings.go
git commit -m "feat(panel): wire probe loops + SCM polling to status:lamp events"
```

---

## Phase 5 — Frontend implementation

> **Working dir for this phase:** `internal/panel/frontend/`. Commands assume you're cd'd in; commits should be at the repo root.

### Task 17: TypeScript shared types + Wails runtime helpers

**Files:**
- Create: `internal/panel/frontend/src/types.ts`
- Create: `internal/panel/frontend/src/wailsEvents.ts`

- [ ] **Step 1: Add type definitions**

`internal/panel/frontend/src/types.ts`:

```ts
export type Tone = "green" | "yellow" | "red" | "grey";
export type LampWhich = "service" | "server" | "tunnel";
export type FooterKind = "ok" | "work" | "err" | "info";

export interface LampPayload {
  which: LampWhich;
  tone: Tone;
  label: string;
  sub?: string;
}

export interface FooterPayload {
  kind: FooterKind;
  text: string;
  time?: string;
  progress?: number;
}

export interface LogLinePayload {
  stream: "service" | "stderr" | "panel";
  raw?: string;
  record?: Record<string, unknown>;
}

// UpdateState mirrors internal/panel/update_state.go.
export enum UpdateState {
  Idle = 0,
  Available = 1,
  Downloading = 2,
  DownloadFailed = 3,
  Ready = 4,
  Installing = 5,
  Installed = 6,
  InstallFailed = 7,
}

export interface UpdateStatePayload {
  state: UpdateState;
  release_tag: string;
}

export interface FieldErrorDTO {
  field: string;
  detail: string;
}
```

- [ ] **Step 2: Wails event subscription helper**

`internal/panel/frontend/src/wailsEvents.ts`:

```ts
import { EventsOn, EventsOff } from "./wails/runtime/runtime";

export function useWailsEvent<T>(name: string, handler: (data: T) => void): void {
  // The generated Wails runtime exposes EventsOn(name, callback).
  // We wrap it so React components can subscribe in useEffect.
  const cb = (data: unknown) => handler(data as T);
  EventsOn(name, cb);
  return () => EventsOff(name);
}
```

(`useWailsEvent` is intentionally a plain function returning cleanup, not a hook — components call it from inside `useEffect`. This avoids React-hook lint complaints when the name argument is dynamic.)

- [ ] **Step 3: Commit**

```bash
git add internal/panel/frontend/src/types.ts internal/panel/frontend/src/wailsEvents.ts
git commit -m "feat(frontend): add shared TypeScript types + Wails event subscription helper"
```

---

### Task 18: Shared UI components (Lamp, Button, Help, Field, Section, Footer, TitleBar, TabBar, Warning, Modal, Checkbox)

Adapt the mockup primitives from `docs/serialhop-ui/project/panel-shell.jsx` into TSX. Style classes are reused 1:1 from `docs/serialhop-ui/project/styles.css` — copy that file's relevant rules into `src/styles/global.css` (and supplement to cover new states the mockup didn't draw).

**Files:**
- Create: `internal/panel/frontend/src/components/Lamp.tsx`
- Create: `internal/panel/frontend/src/components/Help.tsx`
- Create: `internal/panel/frontend/src/components/Button.tsx`
- Create: `internal/panel/frontend/src/components/Field.tsx`
- Create: `internal/panel/frontend/src/components/Section.tsx`
- Create: `internal/panel/frontend/src/components/Footer.tsx`
- Create: `internal/panel/frontend/src/components/TitleBar.tsx`
- Create: `internal/panel/frontend/src/components/TabBar.tsx`
- Create: `internal/panel/frontend/src/components/Warning.tsx`
- Create: `internal/panel/frontend/src/components/Modal.tsx`
- Create: `internal/panel/frontend/src/components/Checkbox.tsx`
- Modify: `internal/panel/frontend/src/styles/global.css` — copy mockup CSS in.

- [ ] **Step 1: Copy mockup CSS rules**

Open `docs/serialhop-ui/project/styles.css`. Copy every rule starting with `.shp-` (the mockup's component-scoped classes) into `internal/panel/frontend/src/styles/global.css`. Leave global base styles (`body`, `:root`) at the top.

- [ ] **Step 2: Create each component**

The mockup file `docs/serialhop-ui/project/panel-shell.jsx` is the structural reference for these. Each TSX file follows the same JSX structure with TS types added. Example for `Lamp.tsx`:

```tsx
import type { Tone } from "../types";

interface LampProps {
  name: string;
  tone: Tone;
  label: string;
  sub?: string;
  pulse?: boolean;
  children?: React.ReactNode; // for the Help icon
}

export function Lamp({ name, tone, label, sub, pulse, children }: LampProps) {
  return (
    <div className="shp-lamp">
      <div className="shp-lamp__row">
        <span className="shp-lamp__name">{name}</span>
        {children}
      </div>
      <div className="shp-lamp__state">
        <span className="shp-lamp__dot" data-tone={tone} data-pulse={pulse ? true : undefined} />
        <div style={{ display: "flex", flexDirection: "column" }}>
          <span className="shp-lamp__label">{label}</span>
          {sub && <span className="shp-lamp__sub">{sub}</span>}
        </div>
      </div>
    </div>
  );
}
```

`Help.tsx`:

```tsx
import { useState } from "react";

interface HelpProps {
  title: string;
  what: string;
  defaultVal?: string;
  when?: string;
}

export function Help({ title, what, defaultVal, when }: HelpProps) {
  const [open, setOpen] = useState(false);
  return (
    <span style={{ position: "relative", display: "inline-flex" }}>
      <span
        className="shp-help"
        data-open={open}
        role="button"
        tabIndex={0}
        onClick={() => setOpen(o => !o)}
        onKeyDown={e => (e.key === "Enter" || e.key === " ") && setOpen(o => !o)}
      >
        ?
      </span>
      {open && (
        <div className="shp-popover" onClick={() => setOpen(false)}>
          <h5>{title}</h5>
          <p>{what}</p>
          {defaultVal && (
            <dl>
              <dt>Default</dt>
              <dd>{defaultVal}</dd>
            </dl>
          )}
          {when && <p style={{ marginTop: 6 }}>{when}</p>}
        </div>
      )}
    </span>
  );
}
```

`Button.tsx`:

```tsx
import type { ButtonHTMLAttributes } from "react";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "primary" | "danger" | "ghost";
  elevated?: boolean;
  small?: boolean;
}

export function Button({ variant = "default", elevated, small, children, className, ...rest }: ButtonProps) {
  const cls = [
    "shp-btn",
    variant === "primary" && "shp-btn--primary",
    variant === "danger" && "shp-btn--danger",
    variant === "ghost" && "shp-btn--ghost",
    small && "shp-btn--sm",
    className,
  ].filter(Boolean).join(" ");
  return (
    <button className={cls} {...rest}>
      {elevated && <span className="shp-btn__shield">UAC</span>}
      {children}
    </button>
  );
}
```

`Footer.tsx`:

```tsx
import type { FooterKind } from "../types";

interface FooterProps {
  kind?: FooterKind;
  text: string;
  time?: string;
  progress?: number;
}

export function Footer({ kind = "info", text, time, progress }: FooterProps) {
  const kindLabel: Record<FooterKind, string> = {
    ok: "OK",
    work: "···",
    err: "ERR",
    info: "·",
  };
  return (
    <div className="shp-footer">
      <span className="shp-footer__icon" data-kind={kind}>{kindLabel[kind]}</span>
      <span className="shp-footer__text" dangerouslySetInnerHTML={{ __html: text }} />
      {typeof progress === "number" && (
        <span className="shp-footer__progress">
          <i style={{ width: `${progress}%` }} />
        </span>
      )}
      {time && <span className="shp-footer__time">{time}</span>}
    </div>
  );
}
```

`TitleBar.tsx`:

```tsx
interface TitleBarProps {
  version: string;
}

export function TitleBar({ version }: TitleBarProps) {
  return (
    <div className="shp-titlebar">
      <div className="shp-titlebar__title">
        <b>SerialHop</b> <span className="shp-titlebar__chip">v{version}</span>
      </div>
    </div>
  );
}
```

(Window control buttons removed — Wails uses native OS chrome; the
mockup's faux controls are illustrative only.)

`TabBar.tsx`:

```tsx
type TabId = "status" | "config" | "devices" | "ports" | "logs";

interface TabBarProps {
  active: TabId;
  dirty?: boolean;
  onChange: (id: TabId) => void;
}

const TABS: { id: TabId; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "config", label: "Config" },
  { id: "devices", label: "Devices" },
  { id: "ports", label: "Ports" },
  { id: "logs", label: "Logs" },
];

export function TabBar({ active, dirty, onChange }: TabBarProps) {
  return (
    <div className="shp-tabs">
      {TABS.map(t => (
        <button
          key={t.id}
          className="shp-tab"
          data-active={active === t.id}
          onClick={() => onChange(t.id)}
        >
          {t.label}
          {t.id === "config" && dirty && <span className="shp-tab__dirty" />}
        </button>
      ))}
    </div>
  );
}

export type { TabId };
```

`Warning.tsx`:

```tsx
interface WarningProps {
  message?: string;
  tone?: "warn" | "info";
}

export function Warning({ message, tone = "warn" }: WarningProps) {
  if (!message) return null;
  return (
    <div className="shp-warning" data-tone={tone}>
      <span className="shp-warning__icon">⚠</span>
      <span>{message}</span>
    </div>
  );
}
```

`Field.tsx`, `Section.tsx`, `Modal.tsx`, `Checkbox.tsx` — adapt directly from `panel-shell.jsx` with the same approach: add prop interfaces, lose the openHelpId-via-prop-drilling pattern (we use the `Help` component's internal state instead).

- [ ] **Step 3: Smoke-render**

Update `App.tsx` temporarily to render a smoke screen:

```tsx
import { TitleBar } from "./components/TitleBar";
import { TabBar } from "./components/TabBar";
import { Footer } from "./components/Footer";
import { Lamp } from "./components/Lamp";

export function App() {
  return (
    <div>
      <TitleBar version="0.13.0" />
      <TabBar active="status" onChange={() => {}} />
      <Lamp name="Service" tone="green" label="Running" />
      <Footer kind="ok" text="Ready" time="15:04:23" />
    </div>
  );
}
```

```bash
npm run build
```

Expected: build succeeds; no TS errors.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/frontend/src/components internal/panel/frontend/src/styles/global.css internal/panel/frontend/src/App.tsx
git commit -m "feat(frontend): port mockup primitives — TitleBar/TabBar/Lamp/Help/Button/Field/Section/Footer/Warning/Modal/Checkbox"
```

---

### Task 19: `App.tsx` — tab router + global event subscriptions

**Files:**
- Modify: `internal/panel/frontend/src/App.tsx`
- Create: `internal/panel/frontend/src/state/globalStore.ts`

- [ ] **Step 1: Create the global store**

`internal/panel/frontend/src/state/globalStore.ts`:

```ts
import { useEffect, useState } from "react";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";
import type { FooterPayload, LampPayload, LampWhich, Tone } from "../types";

interface LampState {
  tone: Tone;
  label: string;
  sub?: string;
}

const DEFAULT_LAMP: LampState = { tone: "grey", label: "Checking…" };

export function useGlobalUiState() {
  const [warn, setWarn] = useState<string | undefined>();
  const [footer, setFooter] = useState<FooterPayload>({ kind: "info", text: "" });
  const [lamps, setLamps] = useState<Record<LampWhich, LampState>>({
    service: DEFAULT_LAMP,
    server: DEFAULT_LAMP,
    tunnel: DEFAULT_LAMP,
  });

  useEffect(() => {
    const onWarn = (data: { message: string }) => setWarn(data.message);
    const onClear = () => setWarn(undefined);
    const onLamp = (p: LampPayload) =>
      setLamps(prev => ({ ...prev, [p.which]: { tone: p.tone, label: p.label, sub: p.sub } }));
    const onFooter = (p: FooterPayload) => setFooter(p);
    EventsOn("warn:set", onWarn);
    EventsOn("warn:clear", onClear);
    EventsOn("status:lamp", onLamp);
    EventsOn("footer:set", onFooter);
    return () => {
      EventsOff("warn:set");
      EventsOff("warn:clear");
      EventsOff("status:lamp");
      EventsOff("footer:set");
    };
  }, []);

  return { warn, footer, lamps };
}
```

- [ ] **Step 2: Rewrite App.tsx**

```tsx
import { useEffect, useState } from "react";
import { TitleBar } from "./components/TitleBar";
import { TabBar, type TabId } from "./components/TabBar";
import { Warning } from "./components/Warning";
import { Footer } from "./components/Footer";
import { StatusTab } from "./tabs/StatusTab";
import { ConfigTab } from "./tabs/ConfigTab";
import { DevicesTab } from "./tabs/DevicesTab";
import { PortsTab } from "./tabs/PortsTab";
import { LogsTab } from "./tabs/LogsTab";
import { GetVersion, LoadConfigFromDisk } from "./wails/go/main/App";
import { useGlobalUiState } from "./state/globalStore";

export function App() {
  const [version, setVersion] = useState("…");
  const [tab, setTab] = useState<TabId>("status");
  const [configDirty, setConfigDirty] = useState(false);
  const { warn, footer, lamps } = useGlobalUiState();

  useEffect(() => {
    GetVersion().then(setVersion);
    // First-launch: open on Config tab if creds are missing.
    LoadConfigFromDisk().then(cfg => {
      if (!cfg.lab_bridge.user || !cfg.lab_bridge.pass) setTab("config");
    });
  }, []);

  return (
    <div className="shp-window">
      <TitleBar version={version} />
      <TabBar active={tab} dirty={configDirty} onChange={setTab} />
      <Warning message={warn} />
      <div className="shp-content">
        <div className="shp-content__pad">
          {tab === "status" && <StatusTab lamps={lamps} />}
          {tab === "config" && <ConfigTab onDirtyChange={setConfigDirty} />}
          {tab === "devices" && <DevicesTab />}
          {tab === "ports" && <PortsTab />}
          {tab === "logs" && <LogsTab />}
        </div>
      </div>
      <Footer {...footer} />
    </div>
  );
}
```

- [ ] **Step 3: Stub the five tabs**

Create `internal/panel/frontend/src/tabs/StatusTab.tsx` (and Config/Devices/Ports/Logs siblings) as one-liner stubs that the next tasks fill in:

```tsx
// StatusTab.tsx
import type { LampWhich, Tone } from "../types";
type Lamps = Record<LampWhich, { tone: Tone; label: string; sub?: string }>;
export function StatusTab({ lamps: _lamps }: { lamps: Lamps }) {
  return <div>Status (todo)</div>;
}
```

```tsx
// ConfigTab.tsx
export function ConfigTab({ onDirtyChange: _ }: { onDirtyChange: (b: boolean) => void }) {
  return <div>Config (todo)</div>;
}
```

```tsx
// DevicesTab.tsx
export function DevicesTab() { return <div>Devices (todo)</div>; }
```

```tsx
// PortsTab.tsx
export function PortsTab() { return <div>Ports (todo)</div>; }
```

```tsx
// LogsTab.tsx
export function LogsTab() { return <div>Logs (todo)</div>; }
```

- [ ] **Step 4: Smoke-build**

```bash
cd internal/panel/frontend && npm run build && cd -
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/frontend/src/App.tsx internal/panel/frontend/src/state internal/panel/frontend/src/tabs
git commit -m "feat(frontend): App.tsx tab router + global event subscriptions (tabs stubbed)"
```

---

### Task 20: Status tab — lamps, service-action buttons, update row

**Files:**
- Modify: `internal/panel/frontend/src/tabs/StatusTab.tsx`

- [ ] **Step 1: Implement the tab**

```tsx
import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Lamp } from "../components/Lamp";
import { Help } from "../components/Help";
import { UpdateState, type LampWhich, type Tone, type UpdateStatePayload } from "../types";
import {
  InstallService, UninstallService, RestartService,
  DownloadUpdate, CancelDownload, InstallUpdate, OpenReleaseNotes,
} from "../wails/go/main/App";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";

type Lamps = Record<LampWhich, { tone: Tone; label: string; sub?: string }>;

export function StatusTab({ lamps }: { lamps: Lamps }) {
  const [update, setUpdate] = useState<UpdateStatePayload>({ state: UpdateState.Idle, release_tag: "" });
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const h = (p: UpdateStatePayload) => setUpdate(p);
    EventsOn("update:state", h);
    return () => EventsOff("update:state");
  }, []);

  const adminAction = async (fn: () => Promise<{ ok: boolean; error_message?: string }>) => {
    setBusy(true);
    try { await fn(); } finally { setBusy(false); }
  };

  // Service-action enablement — derived from the service lamp tone for now.
  // The Go-side state.ComputeButtons is the source of truth; future iteration
  // can expose that result directly. For v1 the tones map cleanly:
  //   grey/red → "not installed" → only Install enabled
  //   green/yellow → installed → Uninstall + Restart enabled
  const svc = lamps.service.tone;
  const installEnabled = svc === "grey" || svc === "red";
  const installedEnabled = svc === "green" || svc === "yellow";

  return (
    <div className="status-tab">
      <section className="lamps">
        <Lamp name="Service" tone={lamps.service.tone} label={lamps.service.label} sub={lamps.service.sub}>
          <Help title="Service" what="Local SerialHop Windows service state." />
        </Lamp>
        <Lamp name="Server" tone={lamps.server.tone} label={lamps.server.label} sub={lamps.server.sub}>
          <Help title="Server" what="Reachability + health of the configured lab-bridge server." />
        </Lamp>
        <Lamp name="Tunnel" tone={lamps.tunnel.tone} label={lamps.tunnel.label} sub={lamps.tunnel.sub}>
          <Help title="Tunnel" what="State of this machine's Chisel reverse tunnel into the lab-bridge." />
        </Lamp>
      </section>

      <section className="actions">
        <Button elevated disabled={busy || !installEnabled} onClick={() => adminAction(InstallService)}>Install</Button>
        <Button elevated disabled={busy || !installedEnabled} onClick={() => adminAction(UninstallService)}>Uninstall</Button>
        <Button elevated disabled={busy || !installedEnabled} onClick={() => adminAction(RestartService)}>Restart</Button>
      </section>

      {update.state !== UpdateState.Idle && (
        <section className="update-row">
          <UpdateLabel update={update} />
          <UpdateButtons update={update}
            onDownload={() => DownloadUpdate()}
            onCancel={() => CancelDownload()}
            onInstall={() => InstallUpdate()}
            onReleaseNotes={() => OpenReleaseNotes()}
          />
        </section>
      )}
    </div>
  );
}

function UpdateLabel({ update }: { update: UpdateStatePayload }) {
  const text: Record<UpdateState, string> = {
    [UpdateState.Idle]: "",
    [UpdateState.Available]: `Update: ${update.release_tag} available`,
    [UpdateState.Downloading]: `Update: ${update.release_tag} — downloading…`,
    [UpdateState.DownloadFailed]: `Update: ${update.release_tag} — download failed`,
    [UpdateState.Ready]: `Update: ${update.release_tag} — ready to install`,
    [UpdateState.Installing]: "Update: installing…",
    [UpdateState.Installed]: `Updated to ${update.release_tag}. Close and reopen this window to load the new panel.`,
    [UpdateState.InstallFailed]: "Update failed — service restored to previous version.",
  };
  const color =
    update.state === UpdateState.DownloadFailed || update.state === UpdateState.InstallFailed ? "red"
    : update.state === UpdateState.Installed ? "green"
    : "default";
  return <span data-color={color}>{text[update.state]}</span>;
}

function UpdateButtons(props: {
  update: UpdateStatePayload;
  onDownload: () => void;
  onCancel: () => void;
  onInstall: () => void;
  onReleaseNotes: () => void;
}) {
  const s = props.update.state;
  return (
    <div className="update-buttons">
      {s === UpdateState.Available && <>
        <Button variant="primary" onClick={props.onDownload}>Download</Button>
        <Button variant="ghost" onClick={props.onReleaseNotes}>Release notes</Button>
      </>}
      {s === UpdateState.Downloading && <Button variant="ghost" onClick={props.onCancel}>Cancel</Button>}
      {s === UpdateState.DownloadFailed && <Button variant="primary" onClick={props.onDownload}>Retry</Button>}
      {s === UpdateState.Ready && <>
        <Button variant="primary" elevated onClick={props.onInstall}>Install update</Button>
        <Button variant="ghost" onClick={props.onReleaseNotes}>Release notes</Button>
      </>}
      {s === UpdateState.InstallFailed && <Button variant="primary" elevated onClick={props.onInstall}>Retry</Button>}
    </div>
  );
}
```

- [ ] **Step 2: Smoke-build**

```bash
cd internal/panel/frontend && npm run build && cd -
```

- [ ] **Step 3: Commit**

```bash
git add internal/panel/frontend/src/tabs/StatusTab.tsx
git commit -m "feat(frontend): Status tab — lamps, service-action buttons, update row"
```

---

### Task 21: Config tab — sections, fields, verify-then-save, unsaved-changes guard

**Files:**
- Modify: `internal/panel/frontend/src/tabs/ConfigTab.tsx`
- Create: `internal/panel/frontend/src/tabs/ConfigTab.test.tsx`

This is the largest tab. The TS form state mirrors `config.Config` from Go. On Save: call `ValidateConfig`, then (if changed) `VerifyCredentials`, then `SaveConfig`.

- [ ] **Step 1: Implement the tab**

`internal/panel/frontend/src/tabs/ConfigTab.tsx`:

```tsx
import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Field } from "../components/Field";
import { Section } from "../components/Section";
import { Help } from "../components/Help";
import { Checkbox } from "../components/Checkbox";
import { Modal } from "../components/Modal";
import {
  LoadConfigFromDisk, SaveConfig, ValidateConfig, VerifyCredentials,
  OpenConfigInEditor, PickBackupDir, RestartService,
} from "../wails/go/main/App";
import type { config } from "../wails/go/models";
import type { FieldErrorDTO } from "../types";

interface Props { onDirtyChange: (b: boolean) => void; }

// Local helper — clone preventing accidental aliasing.
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v));

export function ConfigTab({ onDirtyChange }: Props) {
  const [loaded, setLoaded] = useState<config.Config | null>(null);
  const [form, setForm] = useState<config.Config | null>(null);
  const [errors, setErrors] = useState<FieldErrorDTO[]>([]);
  const [pendingConfirm, setPendingConfirm] = useState<string | null>(null);

  useEffect(() => {
    LoadConfigFromDisk().then(cfg => { setLoaded(clone(cfg)); setForm(clone(cfg)); });
  }, []);

  const dirty = !!(loaded && form && JSON.stringify(loaded) !== JSON.stringify(form));
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);

  if (!form || !loaded) return <div>Loading…</div>;

  const set = <K extends keyof config.Config>(k: K, v: config.Config[K]) =>
    setForm(prev => prev && { ...prev, [k]: v });
  const setNested = <K extends keyof config.Config, KK extends keyof config.Config[K]>(
    sec: K, key: KK, v: config.Config[K][KK],
  ) => setForm(prev => prev && { ...prev, [sec]: { ...(prev[sec] as object), [key]: v } as config.Config[K] });

  const credsMissing = !form.lab_bridge.user || !form.lab_bridge.pass;

  const save = async (alsoRestart: boolean) => {
    const vErrs = await ValidateConfig(form);
    if (vErrs && vErrs.length) { setErrors(vErrs); return; }
    setErrors([]);
    // Verify creds if they changed.
    const verify = await VerifyCredentials(form.lab_bridge.host, form.lab_bridge.user, form.lab_bridge.pass);
    if (verify.outcome === "unauthorized") {
      setErrors([{ field: "lab_bridge.user", detail: "Server rejected these credentials. Check the username and password." }]);
      return;
    }
    if (verify.outcome === "needs_confirm") {
      setPendingConfirm(verify.detail);
      return;
    }
    await doSave(alsoRestart);
  };

  const doSave = async (alsoRestart: boolean) => {
    const res = await SaveConfig(form);
    if (!res.ok) { setErrors(res.field_errors || []); return; }
    setLoaded(clone(form));
    if (alsoRestart) await RestartService();
  };

  const discard = () => { setForm(clone(loaded)); setErrors([]); };

  return (
    <div className="config-tab">
      {credsMissing && (
        <div className="shp-banner" data-tone="info">
          Enter your lab-bridge credentials to enable the service.
        </div>
      )}

      <Section title="Lab-bridge">
        <Field label="Host" helpComponent={<Help title="Host" what="lab-bridge VPS host." defaultVal="111.88.145.138" />}>
          <input value={form.lab_bridge.host} onChange={e => setNested("lab_bridge", "host", e.target.value)} />
        </Field>
        <Field label="Username" hint={errFor(errors, "lab_bridge.user")}>
          <input value={form.lab_bridge.user} onChange={e => setNested("lab_bridge", "user", e.target.value)} />
        </Field>
        <Field label="Password" hint={errFor(errors, "lab_bridge.pass")}>
          <input value={form.lab_bridge.pass} onChange={e => setNested("lab_bridge", "pass", e.target.value)} />
        </Field>
      </Section>

      <Section title="REST">
        <Field label="Port" helpComponent={<Help title="REST port" what="Local TCP port the SerialHop service binds." defaultVal="0 (OS-assigned)" />}>
          <input type="number" min={0} max={65535} value={form.rest.port}
            onChange={e => setNested("rest", "port", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <Section title="Discovery">
        <ListField label="Include"
          values={form.discovery.include}
          onChange={v => setNested("discovery", "include", v)}
          disabled={form.discovery.exclude.length > 0}
          note={form.discovery.exclude.length > 0 ? "Include and Exclude can't be used together" : undefined}
        />
        <ListField label="Exclude"
          values={form.discovery.exclude}
          onChange={v => setNested("discovery", "exclude", v)}
          disabled={form.discovery.include.length > 0}
          note={form.discovery.include.length > 0 ? "Include and Exclude can't be used together" : undefined}
        />
        <Field label="Post-open settle (ms)">
          <input type="number" min={0} value={form.discovery.post_open_settle_ms}
            onChange={e => setNested("discovery", "post_open_settle_ms", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <Section title="Log">
        <Field label="Level">
          <select value={form.log.level} onChange={e => setNested("log", "level", e.target.value)}>
            <option>debug</option><option>info</option><option>warn</option><option>error</option>
          </select>
        </Field>
      </Section>

      <Section title="Raw serial">
        <Field label="">
          <Checkbox label="Enabled" checked={form.raw_serial.enabled}
            onChange={v => setNested("raw_serial", "enabled", v)} />
        </Field>
      </Section>

      <Section title="Auto-update">
        <Field label="">
          <Checkbox label="Enabled" checked={form.auto_update.enabled}
            onChange={v => setNested("auto_update", "enabled", v)} />
        </Field>
      </Section>

      <Section title="Firmware flashing">
        <p className="shp-section__info">
          Firmware flashing is higher risk than raw serial — a bad .hex bricks
          the board (ISP recovery required). Leave disabled unless you're
          actively flashing devices.
        </p>
        <Field label="">
          <Checkbox label="Enabled" checked={form.flashing.enabled}
            onChange={v => setNested("flashing", "enabled", v)} />
        </Field>
        <Field label="Backup directory" disabled={!form.flashing.enabled}>
          <input value={form.flashing.backup_dir}
            disabled={!form.flashing.enabled}
            onChange={e => setNested("flashing", "backup_dir", e.target.value)} />
          <Button small disabled={!form.flashing.enabled}
            onClick={async () => { const d = await PickBackupDir(); if (d) setNested("flashing", "backup_dir", d); }}>
            Pick…
          </Button>
        </Field>
        <Field label="Keep N backups" disabled={!form.flashing.enabled}>
          <input type="number" min={0} value={form.flashing.keep_n}
            disabled={!form.flashing.enabled}
            onChange={e => setNested("flashing", "keep_n", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <div className="config-actions">
        <Button variant="primary" disabled={!dirty} onClick={() => save(false)}>Save</Button>
        <Button variant="primary" elevated disabled={!dirty} onClick={() => save(true)}>Save &amp; restart</Button>
        <Button variant="ghost" disabled={!dirty} onClick={discard}>Discard changes</Button>
        <Button variant="ghost" onClick={() => OpenConfigInEditor()}>Open in editor</Button>
      </div>

      {pendingConfirm && (
        <Modal
          title="Couldn't verify credentials"
          actions={
            <>
              <Button variant="ghost" onClick={() => setPendingConfirm(null)}>Cancel</Button>
              <Button variant="primary" onClick={async () => { setPendingConfirm(null); await doSave(false); }}>
                Save anyway
              </Button>
            </>
          }
        >
          <p>Couldn't reach the server to verify the credentials ({pendingConfirm}). Save anyway?</p>
        </Modal>
      )}
    </div>
  );
}

function errFor(errs: FieldErrorDTO[], field: string): string | undefined {
  const e = errs.find(e => e.field === field);
  return e?.detail;
}

interface ListFieldProps {
  label: string;
  values: string[];
  onChange: (v: string[]) => void;
  disabled?: boolean;
  note?: string;
}

function ListField({ label, values, onChange, disabled, note }: ListFieldProps) {
  return (
    <Field label={label} hint={note} disabled={disabled}>
      <div className="list-field">
        {values.map((v, i) => (
          <div key={i} className="list-field__row">
            <input value={v} disabled={disabled}
              onChange={e => { const copy = [...values]; copy[i] = e.target.value; onChange(copy); }} />
            <Button small disabled={disabled}
              onClick={() => onChange(values.filter((_, j) => j !== i))}>Remove</Button>
          </div>
        ))}
        <Button small disabled={disabled} onClick={() => onChange([...values, ""])}>Add row</Button>
      </div>
    </Field>
  );
}
```

- [ ] **Step 2: Add a Vitest unit test for the dirty/verify flow**

`internal/panel/frontend/src/tabs/ConfigTab.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConfigTab } from "./ConfigTab";

vi.mock("../wails/go/main/App", () => ({
  LoadConfigFromDisk: vi.fn(),
  ValidateConfig: vi.fn(),
  SaveConfig: vi.fn(),
  VerifyCredentials: vi.fn(),
  OpenConfigInEditor: vi.fn(),
  PickBackupDir: vi.fn(),
  RestartService: vi.fn(),
}));

const App = await import("../wails/go/main/App");
const seedCfg = () => ({
  lab_bridge: { host: "h", user: "alice", pass: "pw" },
  rest: { port: 0 },
  discovery: { include: [], exclude: [], post_open_settle_ms: 2000 },
  log: { level: "info" },
  raw_serial: { enabled: false },
  auto_update: { enabled: true },
  flashing: { enabled: false, backup_dir: "", keep_n: 10 },
});

beforeEach(() => {
  (App.LoadConfigFromDisk as any).mockResolvedValue(seedCfg());
  (App.ValidateConfig as any).mockResolvedValue([]);
  (App.SaveConfig as any).mockResolvedValue({ ok: true });
  (App.VerifyCredentials as any).mockResolvedValue({ outcome: "skipped" });
});

describe("ConfigTab", () => {
  it("marks form dirty when a field changes and clears on Discard", async () => {
    const onDirty = vi.fn();
    render(<ConfigTab onDirtyChange={onDirty} />);
    await waitFor(() => screen.getByDisplayValue("h"));
    fireEvent.change(screen.getByDisplayValue("h"), { target: { value: "h2" } });
    await waitFor(() => expect(onDirty).toHaveBeenCalledWith(true));
    fireEvent.click(screen.getByText("Discard changes"));
    await waitFor(() => expect(onDirty).toHaveBeenCalledWith(false));
  });

  it("shows inline error when verifyCredentials returns unauthorized", async () => {
    (App.VerifyCredentials as any).mockResolvedValueOnce({ outcome: "unauthorized" });
    render(<ConfigTab onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("alice"));
    fireEvent.change(screen.getByDisplayValue("alice"), { target: { value: "bob" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => screen.getByText(/rejected these credentials/));
    expect(App.SaveConfig).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 3: Run tests**

```bash
cd internal/panel/frontend && npm test && cd -
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/frontend/src/tabs/ConfigTab.tsx internal/panel/frontend/src/tabs/ConfigTab.test.tsx
git commit -m "feat(frontend): Config tab — form + validation + verify-then-save + unsaved-changes"
```

---

### Task 22: Devices tab + Ports tab

These two tabs share the same banner/state pattern; implement together.

**Files:**
- Modify: `internal/panel/frontend/src/tabs/DevicesTab.tsx`
- Modify: `internal/panel/frontend/src/tabs/PortsTab.tsx`

- [ ] **Step 1: Implement DevicesTab**

```tsx
import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { GetDevices, Discover, DisconnectAll } from "../wails/go/main/App";
import type { api } from "../wails/go/models";

type Status = { reachable: boolean; reason?: string };

export function DevicesTab() {
  const [resp, setResp] = useState<api.DevicesResponse>({ devices: [], discovered_at: null });
  const [status, setStatus] = useState<Status>({ reachable: false });
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    setBusy(true);
    try {
      const [r, s] = await GetDevices();
      setResp(r); setStatus(s);
    } finally { setBusy(false); }
  };

  const rediscover = async () => {
    setBusy(true);
    try {
      const [r, s] = await Discover();
      setResp(r); setStatus(s);
    } finally { setBusy(false); }
  };

  const disconnect = async () => {
    setBusy(true);
    try { await DisconnectAll(); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const empty = resp.devices.length === 0;
  const banner = pickBanner(status, empty, "devices");

  return (
    <div className="devices-tab">
      <div className="banner-row">
        <span>{resp.discovered_at ? `Discovered at ${fmtTime(resp.discovered_at)}` : "Never run"}</span>
      </div>
      <div className="actions">
        <Button onClick={rediscover} disabled={busy || !status.reachable}>Rediscover</Button>
        <Button onClick={disconnect} disabled={busy || !status.reachable || empty}>Disconnect all</Button>
        <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
      </div>
      {banner && <div className="empty-banner">{banner}</div>}
      <table className="devices-table">
        <thead><tr><th>ID</th><th>Type</th><th>Port</th></tr></thead>
        <tbody>
          {[...resp.devices].sort((a, b) => a.id.localeCompare(b.id)).map(d => (
            <tr key={d.id}><td>{d.id}</td><td>{d.type}</td><td>{d.port}</td></tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString();
}

function pickBanner(status: Status, empty: boolean, tab: "devices" | "ports"): string | null {
  if (!status.reachable && status.reason === "service_down") {
    return "Service is not running. Start it from the Status tab.";
  }
  if (!status.reachable) {
    return "Can't reach the local service. It may have just started — wait a few seconds and click Refresh.";
  }
  if (empty && tab === "devices") return "No devices yet. Click Rediscover to probe serial ports.";
  if (empty && tab === "ports") return "No serial ports detected on this machine.";
  return null;
}
```

- [ ] **Step 2: Implement PortsTab**

```tsx
import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Help } from "../components/Help";
import { GetPorts, Discover } from "../wails/go/main/App";
import type { api } from "../wails/go/models";

type Status = { reachable: boolean; reason?: string };

export function PortsTab() {
  const [resp, setResp] = useState<api.DetailedPortsResponse>({ ports: [] });
  const [status, setStatus] = useState<Status>({ reachable: false });
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    setBusy(true);
    try { const [r, s] = await GetPorts(); setResp(r); setStatus(s); } finally { setBusy(false); }
  };
  const rediscover = async () => {
    setBusy(true);
    try { await Discover(); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const banner = !status.reachable
    ? (status.reason === "service_down"
        ? "Service is not running. Start it from the Status tab."
        : "Can't reach the local service. It may have just started — wait a few seconds and click Refresh.")
    : resp.ports.length === 0 ? "No serial ports detected on this machine." : null;

  return (
    <div className="ports-tab">
      <div className="actions">
        <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
        <Button onClick={rediscover} disabled={busy || !status.reachable}>Rediscover</Button>
      </div>
      {banner && <div className="empty-banner">{banner}</div>}
      <table className="ports-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>USB</th>
            <th>VID <Help title="VID" what="USB vendor ID in hexadecimal." /></th>
            <th>PID <Help title="PID" what="USB product ID in hexadecimal." /></th>
            <th>Serial <Help title="Serial number" what="USB serial string if the device reports one." /></th>
            <th>Product <Help title="Product" what="USB product descriptor string." /></th>
            <th>Discovered <Help title="Discovered" what="True if discovery matched a SerialHop device on this port." /></th>
            <th>Device ID <Help title="Device ID" what="The logical device ID this port was bound to during the last discovery." /></th>
          </tr>
        </thead>
        <tbody>
          {[...resp.ports].sort((a, b) => a.name.localeCompare(b.name)).map(p => (
            <tr key={p.name}>
              <td>{p.name}</td>
              <td>{p.is_usb ? "✓" : ""}</td>
              <td>{p.vid}</td>
              <td>{p.pid}</td>
              <td>{p.serial_number}</td>
              <td>{p.product}</td>
              <td>{p.discovered ? "✓" : ""}</td>
              <td>{p.device_id || ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
```

- [ ] **Step 3: Smoke-build + commit**

```bash
cd internal/panel/frontend && npm run build && cd -
git add internal/panel/frontend/src/tabs/DevicesTab.tsx internal/panel/frontend/src/tabs/PortsTab.tsx
git commit -m "feat(frontend): Devices + Ports tabs with three-way empty-state banners"
```

---

### Task 23: Logs tab — stream selector, level filter, follow, search, parsed slog view

**Files:**
- Modify: `internal/panel/frontend/src/tabs/LogsTab.tsx`

- [ ] **Step 1: Implement**

```tsx
import { useEffect, useRef, useState } from "react";
import { Button } from "../components/Button";
import { Help } from "../components/Help";
import { StartLogStream, StopLogStream, OpenLogsFolder } from "../wails/go/main/App";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";
import type { LogLinePayload } from "../types";

type StreamID = "service" | "stderr" | "panel";
type LevelFilter = "all" | "debug" | "info" | "warn" | "error";
const RING_CAPACITY = 5_000;
const LEVEL_RANK: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 };

export function LogsTab() {
  const [stream, setStream] = useState<StreamID>("service");
  const [level, setLevel] = useState<LevelFilter>("all");
  const [follow, setFollow] = useState(true);
  const [search, setSearch] = useState("");
  const [lines, setLines] = useState<LogLinePayload[]>([]);
  const [selected, setSelected] = useState<LogLinePayload | null>(null);
  const endRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setLines([]); setSelected(null);
    StartLogStream(stream);
    const onLine = (p: LogLinePayload) => {
      if (p.stream !== stream) return;
      setLines(prev => {
        const next = [...prev, p];
        if (next.length > RING_CAPACITY) next.splice(0, next.length - RING_CAPACITY);
        return next;
      });
    };
    const onRot = () => setLines(prev => [...prev, { stream, raw: "— rotated —" }]);
    EventsOn("log:line", onLine);
    EventsOn("log:rotated", onRot);
    return () => { EventsOff("log:line"); EventsOff("log:rotated"); StopLogStream(); };
  }, [stream]);

  useEffect(() => { if (follow) endRef.current?.scrollIntoView({ behavior: "auto" }); }, [lines, follow]);

  const filtered = lines.filter(l => {
    if (stream === "service" && level !== "all" && l.record) {
      const recLevel = String(l.record.level || "").toLowerCase();
      if (LEVEL_RANK[recLevel] !== undefined && LEVEL_RANK[recLevel] < LEVEL_RANK[level]) return false;
    }
    if (search) {
      const hay = l.raw || JSON.stringify(l.record || {});
      if (!hay.toLowerCase().includes(search.toLowerCase())) return false;
    }
    return true;
  });

  return (
    <div className="logs-tab">
      <div className="logs-controls">
        <label>
          Stream:
          <select value={stream} onChange={e => setStream(e.target.value as StreamID)}>
            <option value="service">Service log</option>
            <option value="stderr">Stderr</option>
            <option value="panel">Panel errors</option>
          </select>
          <Help title={`${stream} stream`} what="Source file for the displayed log entries." />
        </label>
        <label>
          Level:
          <select value={level} onChange={e => setLevel(e.target.value as LevelFilter)} disabled={stream !== "service"}>
            <option>all</option><option>debug</option><option>info</option><option>warn</option><option>error</option>
          </select>
        </label>
        <label>
          <input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} /> Follow
        </label>
        <input className="logs-search" placeholder="Search…" value={search} onChange={e => setSearch(e.target.value)} />
      </div>
      <div className="logs-view">
        {stream === "service" ? (
          <table className="logs-table">
            <thead><tr><th>Time</th><th>Level</th><th>Message</th></tr></thead>
            <tbody>
              {filtered.map((l, i) => l.record && (
                <tr key={i} onClick={() => setSelected(l)} data-selected={selected === l}>
                  <td>{String(l.record.time || "")}</td>
                  <td>{String(l.record.level || "")}</td>
                  <td>{String(l.record.msg || "")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <pre className="logs-raw">
            {filtered.map((l, i) => <div key={i}>{l.raw}</div>)}
          </pre>
        )}
        <div ref={endRef} />
      </div>
      {selected?.record && (
        <pre className="logs-details">{JSON.stringify(selected.record, null, 2)}</pre>
      )}
      <div className="logs-actions">
        <Button variant="ghost" onClick={() => OpenLogsFolder()}>Open logs folder</Button>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Build + commit**

```bash
cd internal/panel/frontend && npm run build && cd -
git add internal/panel/frontend/src/tabs/LogsTab.tsx
git commit -m "feat(frontend): Logs tab — stream selector, level filter, follow, search, slog record detail"
```

---

## Phase 6 — Cleanup + manual smoke

### Task 24: Remove `lxn/walk` dependency, delete old panel files, smoke-test end-to-end

**Files:**
- Delete: `internal/panel/panel.go` (the old walk file)
- Delete: `internal/panel/credsdialog_windows.go`
- Delete: `internal/panel/credsdialog_other.go`
- Delete: `internal/panel/timer_windows.go`
- Modify: `go.mod` / `go.sum` — remove `github.com/lxn/walk` + transitive deps via `go mod tidy`.

- [ ] **Step 1: Delete the walk-based files**

```bash
git rm internal/panel/panel.go internal/panel/credsdialog_windows.go internal/panel/credsdialog_other.go internal/panel/timer_windows.go
```

If any of these contain helpers that were referenced from `wails_app.go` (e.g., `queryServiceState`, `readCacheDisplay`, `fetchSums`, `cleanupStaleStagedFiles`, `applyUpdateRow`'s helpers, `trySend`, `OpenWithDefaultApp`), copy them out into focused, build-tagged files before deleting `panel.go`. Suggested split:

- `internal/panel/scm_query.go` (`//go:build windows`) — `queryServiceState`.
- `internal/panel/sums.go` — `fetchSums`, `cleanupStaleStagedFiles`.
- `internal/panel/trysend.go` — `trySend`.

Move the function declarations verbatim; no logic changes.

- [ ] **Step 2: Tidy go.mod**

```bash
go mod tidy
```

Expected: `lxn/walk` and `gopkg.in/Knetic/govaluate.v3` (and any other transitive walk deps) disappear from `go.sum`.

- [ ] **Step 3: Cross-platform build + test**

```bash
go test -race -count=1 ./...
GOOS=windows GOARCH=amd64 go build ./cmd/serialhop
```

Expected: PASS.

- [ ] **Step 4: Manual smoke test (Windows)**

On a Windows dev box (or via the CI artifact + a Windows VM):

1. Run `SerialHop.exe` (panel mode) — verify the window opens with the new tabbed UI.
2. Empty config — verify it opens on the Config tab with the "Enter your lab-bridge credentials" banner.
3. Enter creds, click Save — verify YAML appears at `%ProgramData%\SerialHop\SerialHop_config.yaml`.
4. Click Install on Status tab — UAC prompt → service installed → service lamp turns green.
5. Switch to Devices tab — verify devices list refreshes.
6. Switch to Logs tab → Service log — verify lines tail in real time.
7. Trigger an auto-update flow (mock the release endpoint if necessary, or wait for the next release) — verify the update row appears.

- [ ] **Step 5: Commit**

```bash
git add -u
git add internal/panel/scm_query.go internal/panel/sums.go internal/panel/trysend.go 2>/dev/null
git commit -m "feat(panel)!: remove lxn/walk; Wails-based panel is the only implementation"
```

(Note: this commit doesn't introduce a breaking API change — operator-visible behavior is preserved or improved. `!` is for the size of the swap, not for breakage. If the project's release-please configuration treats `!` as major-bump, drop the `!` and rely on the `feat:` minor bump.)

- [ ] **Step 6: Final pre-flight against CI**

Locally before pushing:

```bash
gofmt -l .                  # must print nothing
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
(cd internal/panel/frontend && npm ci && npm run lint && npm test && npm run build)
```

Expected: all green. Then open the PR.

---

## Self-review

Spec sections vs tasks:

- **§1 Purpose & scope** — n/a (motivational).
- **§2 Runtime architecture** — Task 7 (Wails app skeleton).
- **§3 Tab structure & global elements** — Task 19 (App.tsx routing + global event subscriptions) + Task 18 (TitleBar, TabBar, Warning, Footer).
- **§4 Status tab** — Task 20 + Task 16 (probe loop event wiring) + Task 12 (service-control bindings) + Task 13 (update flow).
- **§5 Config tab** — Task 21 + Task 10 (config bindings) + Task 11 (verify-then-save) + Task 5 (CredVerify helper).
- **§6 Devices tab** — Task 22 (Devices half) + Task 15 (bindings) + Task 3 (ServiceCli).
- **§7 Ports tab** — Task 22 (Ports half) + Task 15 + Task 3.
- **§8 Logs tab** — Task 23 + Task 14 (StartLogStream/StopLogStream) + Task 4 (FileTail).
- **§9 Help icon convention** — Task 18 (`Help` component) + applied within each tab task.
- **§10 ActualRestPort cache change** — Tasks 1 + 2.
- **§11 Go ↔ TS contract** — Tasks 9 (signatures) + 10–16 (impls) + Task 17 (TS types).
- **§12 Code reuse table** — Task 7 (rename walkRun) + Task 24 (deletes).
- **§13 Frontend layout** — Task 6 (scaffold) + Task 18 (components).
- **§14 Build pipeline** — Task 8.
- **§15 Removed** — Task 24.
- **§16 Unchanged** — n/a (no code).
- **§17 Testing** — covered task-by-task; bootstrap (Task 1), app (Task 2), servicecli (Task 3), filetail (Task 4), credverify (Task 5), config bindings (Task 10), VerifyCredentials (Task 11), ConfigTab (Task 21).
- **§18 Deferred to plan** — folder picker (Task 10 via `runtime.OpenDirectoryDialog`); file-tail polling cadence pinned to 500 ms (Task 14); TS ring buffer 5,000 lines (Task 23); sort orders + column widths (Tasks 22, 23); window default size 980×700 / min 860×580 (Task 7).

**Placeholders:** none — every step has either code or a concrete command. The few engineer-judgement notes ("if `OpenWithDefaultApp` is already exported, skip", "if `wails build` covers buildcmd's cases") are flagged as such, not as TODOs to fill in.

**Type consistency:** `ServiceCliStatus` / `ServiceTabStatusDTO` / pairs across Tasks 3, 9, 15, 22. Lamp tone is `Tone` / `string` across Go (`toneString`) and TS (`Tone` enum). `UpdateState` integer values agree between Go (`update_state.go` existing constants) and TS (Task 17 enum) — engineer must confirm by inspecting `update_state.go` numbering before pasting Task 17.

**Open risks called out:**

1. **`buildcmd` retirement vs adoption** (Task 8): the existing `tools/buildcmd` injects ldflags-encoded version strings. If `wails build`'s `-ldflags` accepts the same flags cleanly, `buildcmd` is retired; otherwise we keep `buildcmd` wrapping `wails build`.
2. **WebView2 runtime presence** on the few Windows-10-pre-21H1 boxes (spec §14). v1 documents the manual install path; no auto-install logic.
3. **`extractField` regex in Task 10** is heuristic-based. If `config.Validate` returns errors that don't match `field:detail`, the UI shows them as global banners rather than inline. That's correct behavior (better fallback than silent loss), but the validator may need a follow-up tweak to enrich field paths.
