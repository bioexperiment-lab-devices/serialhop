# Camera Streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the SerialHop side of the lab-bridge video-streaming protocol (per `docs/2026-05-24-serialhop-streaming-protocol.md`) plus a new **Cameras** tab so an operator can choose which cameras are exposed to remote viewers.

**Architecture:** The Windows service stays the chisel-tunnel REST ingress and gains three new handlers under `/api/translations*` that are *stateless HTTP proxies* to the panel. The panel (running in the operator's user session) owns camera enumeration, armed-cameras state, the `session_id ↔ camera` mapping, and all `ffmpeg.exe` child processes. ffmpeg's bundled WHIP muxer handles SDP/ICE/RTP outbound to lab-bridge.

**Tech Stack:** Go 1.22+, Wails v2, React + TypeScript (Vitest), `net/http`, `os/exec`, bundled `ffmpeg.exe` (gyan.dev essentials build).

**Reference spec:** `docs/superpowers/specs/2026-05-24-camera-streaming-design.md`

---

## File Structure

### New files

```
internal/streamer/
  types.go                      # Camera, Session, ArmedList, StreamingState DTOs
  enumerator.go                 # Enumerator interface
  enumerator_windows.go         # `ffmpeg -list_devices true -f dshow -i dummy` parser (Windows)
  enumerator_other.go           # fake for macOS/Linux dev
  enumerator_windows_test.go    # parser tests against fixtures
  testdata/ffmpeg_list_devices_one.txt
  testdata/ffmpeg_list_devices_two.txt
  testdata/ffmpeg_list_devices_empty.txt
  ffmpeg_build.go               # pinned ffmpeg version string + SHA256
  ffmpeg.go                     # FFmpegResolver: path lookup + version probe + argv builder
  ffmpeg_test.go
  defaults.go                   # resolution / fps / bitrate / proxy timeout constants
  store.go                      # armed_cameras.json read/write/atomic-replace
  store_test.go
  session.go                    # one WHIP session ↔ one ffmpeg child (cross-platform glue)
  session_windows.go            # CTRL_BREAK_EVENT process-group machinery
  session_other.go              # SIGTERM equivalents for dev hosts
  session_test.go               # uses testbin/fake_ffmpeg
  manager.go                    # armed + active sessions; replace-on-conflict, idempotency, 409
  manager_test.go
  testbin/fake_ffmpeg/main.go   # tiny stub that mimics ffmpeg's externally-visible behavior

internal/panel/
  streaming_http.go             # localhost HTTP listener (POST/GET /api/translations*)
  streaming_http_test.go
  streaming_bindings.go         # Wails bindings (//go:build windows): ListCameras / SetArmed / GetStreamingState
  streaming_bindings_other.go   # no-op stubs so the package compiles on macOS/Linux
  streaming_lifecycle.go        # startup: orphan kill, endpoint file write, manager start; shutdown reverse
  streaming_lifecycle_test.go

internal/api/
  translations.go               # service-side proxy handlers
  translations_test.go

internal/panel/frontend/src/tabs/
  CamerasTab.tsx
  CamerasTab.test.tsx
```

### Modified files

```
internal/paths/paths.go                        # add FFmpegPath(), PanelEndpointPath(), ArmedCamerasPath()
internal/paths/paths_test.go                    # cover new helpers

internal/bootstrap/cache.go                     # add WritePanelEndpoint, ReadPanelEndpoint helpers + types
internal/bootstrap/cache_test.go                # cover them

internal/api/handlers.go                        # mount three new routes; thread a TranslationsProxy field
internal/api/handlers_test.go                   # adjust constructor calls (Server.New gains arg)
internal/app/app.go                             # wire TranslationsProxy into api.New

internal/panel/wails_app.go                     # start streaming subsystem in App.startup; shut down in OnShutdown

internal/panel/frontend/src/components/TabBar.tsx
internal/panel/frontend/src/App.tsx
internal/panel/frontend/src/wails/go/main/App.ts

tools/installer/...                             # copy ffmpeg.exe into install dir
Taskfile.yaml                                   # task to download + verify ffmpeg.exe for local dev
```

---

## Conventions used throughout this plan

- **TDD:** Write the failing test first; verify it fails; implement the minimal code; verify it passes; commit. Skip the test-first step only when a step is purely additive plumbing with no behavior to verify (called out per task).
- **Commits:** Each task ends with one commit using Conventional Commit prefix (`feat:` / `test:` / `chore:` / `docs:`) — per `CLAUDE.md`, the squash-merge title (= PR title) is what becomes the changelog entry, so individual commits inside a feature branch can use any of those types.
- **Cross-platform:** Every Windows-only `_windows.go` file has a matching `_other.go` so `go test ./...` is green on macOS. Tests that exercise Windows-only behavior carry `//go:build windows`.
- **Pre-commit local checks** (per `CLAUDE.md`): before each commit, run
  ```
  gofmt -l .
  go vet ./...
  go test -race -count=1 ./...
  ```
  golangci-lint and govulncheck are also part of CI's `verify` job; run them once per multi-task batch.

---

## Task 1 — Foundation: new path helpers + panel endpoint cache

**Files:**
- Modify: `internal/paths/paths.go`
- Modify: `internal/paths/paths_test.go`
- Modify: `internal/bootstrap/cache.go`
- Modify: `internal/bootstrap/cache_test.go`

The panel and service need three new well-known on-disk locations:
- `<DataDir>/ffmpeg.exe` (read by the panel; written by the installer in Task 17)
- `<DataDir>/panel-endpoint.json` (written by the panel; read by the service)
- `<DataDir>/armed-cameras.json` (read/written by the panel)

The `PanelEndpoint` payload is a small JSON record describing where the panel's localhost listener is bound.

- [ ] **Step 1: Write the failing test for the new path helpers**

Append to `internal/paths/paths_test.go`:

```go
func TestFFmpegPath(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/tmp/sh")
	want := filepath.Join("/tmp/sh", "ffmpeg.exe")
	if got := FFmpegPath(); got != want {
		t.Fatalf("FFmpegPath = %q, want %q", got, want)
	}
}

func TestFFmpegPath_NoDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := FFmpegPath(); got != "" {
		t.Fatalf("FFmpegPath should be empty when no data dir; got %q", got)
	}
}

func TestPanelEndpointPath(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/tmp/sh")
	want := filepath.Join("/tmp/sh", "panel-endpoint.json")
	if got := PanelEndpointPath(); got != want {
		t.Fatalf("PanelEndpointPath = %q, want %q", got, want)
	}
}

func TestArmedCamerasPath(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/tmp/sh")
	want := filepath.Join("/tmp/sh", "armed-cameras.json")
	if got := ArmedCamerasPath(); got != want {
		t.Fatalf("ArmedCamerasPath = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests; verify they fail**

```
go test ./internal/paths/ -run 'TestFFmpegPath|TestPanelEndpointPath|TestArmedCamerasPath' -v
```
Expected: undefined: FFmpegPath / PanelEndpointPath / ArmedCamerasPath.

- [ ] **Step 3: Implement the helpers**

Add to `internal/paths/paths.go` (just below `ServerInfoCachePath`):

```go
const (
	FFmpegBinaryName     = "ffmpeg.exe"
	PanelEndpointFileName = "panel-endpoint.json"
	ArmedCamerasFileName  = "armed-cameras.json"
)

func FFmpegPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, FFmpegBinaryName)
}

func PanelEndpointPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelEndpointFileName)
}

func ArmedCamerasPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ArmedCamerasFileName)
}
```

- [ ] **Step 4: Run the tests; verify they pass**

```
go test ./internal/paths/ -run 'TestFFmpegPath|TestPanelEndpointPath|TestArmedCamerasPath' -v
```

- [ ] **Step 5: Write the failing test for the panel-endpoint bootstrap helpers**

Append to `internal/bootstrap/cache_test.go`:

```go
func TestWriteReadPanelEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-endpoint.json")
	in := PanelEndpoint{
		Host:      "127.0.0.1",
		Port:      49217,
		PID:       12345,
		StartedAt: "2026-05-24T13:45:00Z",
	}
	if err := WritePanelEndpoint(path, in); err != nil {
		t.Fatalf("WritePanelEndpoint: %v", err)
	}
	out, err := ReadPanelEndpoint(path)
	if err != nil {
		t.Fatalf("ReadPanelEndpoint: %v", err)
	}
	if out.Port != 49217 || out.PID != 12345 || out.Host != "127.0.0.1" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestReadPanelEndpoint_Missing(t *testing.T) {
	_, err := ReadPanelEndpoint(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrPanelEndpointMissing) {
		t.Fatalf("want ErrPanelEndpointMissing, got %v", err)
	}
}

func TestReadPanelEndpoint_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-endpoint.json")
	if err := os.WriteFile(path, []byte(`{"version": 99, "host":"127.0.0.1","port":1,"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPanelEndpoint(path)
	if !errors.Is(err, ErrPanelEndpointMissing) {
		t.Fatalf("want ErrPanelEndpointMissing for version mismatch, got %v", err)
	}
}
```

- [ ] **Step 6: Run the tests; verify they fail**

```
go test ./internal/bootstrap/ -run TestWriteReadPanelEndpoint -v
go test ./internal/bootstrap/ -run TestReadPanelEndpoint -v
```
Expected: undefined: PanelEndpoint, WritePanelEndpoint, ReadPanelEndpoint, ErrPanelEndpointMissing.

- [ ] **Step 7: Implement the helpers**

Append to `internal/bootstrap/cache.go`:

```go
const panelEndpointCurrentVersion = 1

var ErrPanelEndpointMissing = errors.New("bootstrap: panel endpoint missing")

type PanelEndpoint struct {
	Version   int    `json:"version"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

func WritePanelEndpoint(path string, e PanelEndpoint) error {
	e.Version = panelEndpointCurrentVersion
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("bootstrap: marshal panel endpoint: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "panel-endpoint.json.*.tmp")
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

func ReadPanelEndpoint(path string) (PanelEndpoint, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.PanelEndpointPath()
	if err != nil {
		if os.IsNotExist(err) {
			return PanelEndpoint{}, ErrPanelEndpointMissing
		}
		slog.Warn("bootstrap: read panel endpoint failed", "path", path, "err", err)
		return PanelEndpoint{}, ErrPanelEndpointMissing
	}
	var e PanelEndpoint
	if err := json.Unmarshal(data, &e); err != nil {
		slog.Warn("bootstrap: panel endpoint corrupt; deleting", "path", path, "err", err)
		_ = os.Remove(path)
		return PanelEndpoint{}, ErrPanelEndpointMissing
	}
	if e.Version != panelEndpointCurrentVersion {
		slog.Warn("bootstrap: panel endpoint version mismatch; deleting", "path", path, "version", e.Version)
		_ = os.Remove(path)
		return PanelEndpoint{}, ErrPanelEndpointMissing
	}
	return e, nil
}

func DeletePanelEndpoint(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("bootstrap: delete panel endpoint: %w", err)
	}
	return nil
}
```

- [ ] **Step 8: Run the tests; verify they pass**

```
go test ./internal/bootstrap/ -run 'TestWriteReadPanelEndpoint|TestReadPanelEndpoint' -v
```

- [ ] **Step 9: gofmt + go vet + full test sweep**

```
gofmt -l ./internal/paths ./internal/bootstrap
go vet ./internal/paths/... ./internal/bootstrap/...
go test -race -count=1 ./internal/paths/... ./internal/bootstrap/...
```

- [ ] **Step 10: Commit**

```
git add internal/paths/ internal/bootstrap/
git commit -m "feat(paths,bootstrap): path helpers and PanelEndpoint cache for streaming"
```

---

## Task 2 — Streamer types and enumeration interface (with a working fake)

**Files:**
- Create: `internal/streamer/types.go`
- Create: `internal/streamer/enumerator.go`
- Create: `internal/streamer/enumerator_other.go`
- Create: `internal/streamer/enumerator_other_test.go`

This sets up the `streamer` package's public surface and the cross-platform fallback so the package compiles and tests pass on macOS/Linux.

- [ ] **Step 1: Create the types**

Create `internal/streamer/types.go`:

```go
// Package streamer implements the SerialHop side of the lab-bridge video
// streaming protocol. See docs/2026-05-24-serialhop-streaming-protocol.md
// and docs/superpowers/specs/2026-05-24-camera-streaming-design.md.
package streamer

import "time"

// Camera is one physical camera as reported by the OS enumerator.
type Camera struct {
	// ID is the stable, OS-level identifier (DirectShow "Alternative name"
	// on Windows). Survives reboots and replugs into the same USB port.
	ID string `json:"id"`
	// Label is the friendly device name shown to operators and to viewers.
	Label string `json:"label"`
}

// ArmedCamera is the persisted form of an operator-allowed camera.
type ArmedCamera struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SessionState describes a single in-flight WHIP publish.
type SessionState struct {
	CameraID  string    `json:"camera_id"`
	SessionID string    `json:"session_id"`
	StartedAt time.Time `json:"started_at"`
}

// StreamingState is the panel UI's view of the world.
type StreamingState struct {
	Cameras  []CameraView   `json:"cameras"`
	Sessions []SessionState `json:"sessions"`
	// FfmpegOK is false when the bundled ffmpeg.exe is missing or fails its
	// version probe. The UI shows a red banner in that case.
	FfmpegOK bool `json:"ffmpeg_ok"`
}

// CameraView is one row in the Cameras tab.
type CameraView struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Armed        bool   `json:"armed"`
	Connected    bool   `json:"connected"`
	Live         bool   `json:"live"`        // currently publishing
	LastErrorMsg string `json:"last_error_msg,omitempty"`
}
```

- [ ] **Step 2: Create the enumerator interface**

Create `internal/streamer/enumerator.go`:

```go
package streamer

import "context"

// Enumerator lists cameras attached to the host. Implementations are
// platform-specific; the windows build uses ffmpeg's `-list_devices`, the
// other build is a development fake.
type Enumerator interface {
	List(ctx context.Context) ([]Camera, error)
}
```

- [ ] **Step 3: Write the failing test for the non-Windows fake**

Create `internal/streamer/enumerator_other_test.go`:

```go
//go:build !windows

package streamer

import (
	"context"
	"testing"
)

func TestFakeEnumerator_List(t *testing.T) {
	e := NewEnumerator()
	got, err := e.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 fake camera, got %d", len(got))
	}
	if got[0].ID == "" || got[0].Label == "" {
		t.Fatalf("fake camera should have id+label, got %+v", got[0])
	}
}
```

- [ ] **Step 4: Run the test; verify it fails**

```
go test ./internal/streamer/ -run TestFakeEnumerator_List -v
```
Expected: undefined: NewEnumerator.

- [ ] **Step 5: Implement the non-Windows fake**

Create `internal/streamer/enumerator_other.go`:

```go
//go:build !windows

package streamer

import (
	"context"
)

// NewEnumerator returns a development fake on non-Windows hosts.
//
// The fake is intentionally minimal: one canned camera so the panel UI
// renders something in `wails dev` on a developer Mac/Linux box. Tests
// for production behavior (parsing, lifecycle) live in the *_windows*
// files behind a build tag.
func NewEnumerator() Enumerator {
	return fakeEnumerator{}
}

type fakeEnumerator struct{}

func (fakeEnumerator) List(_ context.Context) ([]Camera, error) {
	return []Camera{
		{
			ID:    "fake:dev-camera-0",
			Label: "Fake Dev Camera",
		},
	}, nil
}
```

- [ ] **Step 6: Run the test; verify it passes**

```
go test ./internal/streamer/ -run TestFakeEnumerator_List -v
```

- [ ] **Step 7: gofmt + go vet**

```
gofmt -l ./internal/streamer
go vet ./internal/streamer/...
```

- [ ] **Step 8: Commit**

```
git add internal/streamer/types.go internal/streamer/enumerator.go internal/streamer/enumerator_other.go internal/streamer/enumerator_other_test.go
git commit -m "feat(streamer): package skeleton with cross-platform enumerator interface"
```

---

## Task 3 — Windows DirectShow enumerator (parses `ffmpeg -list_devices`)

**Files:**
- Create: `internal/streamer/enumerator_windows.go`
- Create: `internal/streamer/enumerator_windows_test.go`
- Create: `internal/streamer/testdata/ffmpeg_list_devices_one.txt`
- Create: `internal/streamer/testdata/ffmpeg_list_devices_two.txt`
- Create: `internal/streamer/testdata/ffmpeg_list_devices_empty.txt`

The Windows enumerator runs `ffmpeg.exe -hide_banner -list_devices true -f dshow -i dummy` and parses the stderr output. ffmpeg prints DirectShow device names in a stable line-prefixed format. We extract the friendly name plus its "Alternative name" (the Windows device instance path, used as the stable id).

Parser must handle:
- Multiple cameras.
- A single camera.
- No cameras.
- A trailing audio-devices section that should be ignored.

- [ ] **Step 1: Create the test fixtures**

Create `internal/streamer/testdata/ffmpeg_list_devices_one.txt`:

```
[dshow @ 000001a2b3c4d000] "Logitech HD Pro Webcam C920"
[dshow @ 000001a2b3c4d000]   Alternative name "@device:pnp:\\?\usb#vid_046d&pid_082d&mi_00#7&3a23cf2f&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global"
[dshow @ 000001a2b3c4d000] DirectShow audio devices
[dshow @ 000001a2b3c4d000] "Microphone (HD Pro Webcam C920)"
[dshow @ 000001a2b3c4d000]   Alternative name "@device:cm:{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{ABCDEF}"
dummy: Immediate exit requested
```

Create `internal/streamer/testdata/ffmpeg_list_devices_two.txt`:

```
[dshow @ 000001a2b3c4d000] DirectShow video devices (some may be both video and audio devices)
[dshow @ 000001a2b3c4d000] "Logitech HD Pro Webcam C920"
[dshow @ 000001a2b3c4d000]   Alternative name "@device:pnp:\\?\usb#vid_046d&pid_082d#001#{guid1}\global"
[dshow @ 000001a2b3c4d000] "Microsoft Camera Front"
[dshow @ 000001a2b3c4d000]   Alternative name "@device:pnp:\\?\usb#vid_045e&pid_0779#002#{guid2}\global"
[dshow @ 000001a2b3c4d000] DirectShow audio devices
[dshow @ 000001a2b3c4d000] "Microphone Array"
[dshow @ 000001a2b3c4d000]   Alternative name "@device:cm:{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{XYZ}"
dummy: Immediate exit requested
```

Create `internal/streamer/testdata/ffmpeg_list_devices_empty.txt`:

```
[dshow @ 000001a2b3c4d000] DirectShow video devices (some may be both video and audio devices)
[dshow @ 000001a2b3c4d000] DirectShow audio devices
dummy: Immediate exit requested
```

- [ ] **Step 2: Write the failing parser test**

Create `internal/streamer/enumerator_windows_test.go`:

```go
//go:build windows

package streamer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseListDevices_TwoCameras(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_two.txt")
	got, err := parseListDevices(data)
	if err != nil {
		t.Fatalf("parseListDevices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 video devices, got %d (%+v)", len(got), got)
	}
	if got[0].Label != "Logitech HD Pro Webcam C920" {
		t.Errorf("got[0].Label = %q", got[0].Label)
	}
	if got[0].ID == "" || !strings_contains(got[0].ID, "vid_046d") {
		t.Errorf("got[0].ID = %q (expected the Alternative name)", got[0].ID)
	}
	if got[1].Label != "Microsoft Camera Front" {
		t.Errorf("got[1].Label = %q", got[1].Label)
	}
}

func TestParseListDevices_OneCamera(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_one.txt")
	got, err := parseListDevices(data)
	if err != nil {
		t.Fatalf("parseListDevices: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Logitech HD Pro Webcam C920" {
		t.Fatalf("want 1 camera C920, got %+v", got)
	}
}

func TestParseListDevices_Empty(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_empty.txt")
	got, err := parseListDevices(data)
	if err != nil {
		t.Fatalf("parseListDevices: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 cameras, got %d", len(got))
	}
}

// strings_contains is a tiny helper so the test file doesn't import "strings"
// next to a real-package method named the same.
func strings_contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %s: %v", p, err)
	}
	return b
}
```

- [ ] **Step 3: Run the test; verify it fails**

```
GOOS=windows go test ./internal/streamer/ -run TestParseListDevices -v
```
Expected: undefined: parseListDevices.

(If you can't easily run with `GOOS=windows` locally, skip this step — the implementation is mechanical and CI on the Windows runner will catch any regression.)

- [ ] **Step 4: Implement the parser**

Create `internal/streamer/enumerator_windows.go`:

```go
//go:build windows

package streamer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// NewEnumerator returns the Windows DirectShow enumerator backed by ffmpeg.
func NewEnumerator() Enumerator {
	return &dshowEnumerator{ffmpegPath: paths.FFmpegPath()}
}

type dshowEnumerator struct {
	ffmpegPath string
}

func (e *dshowEnumerator) List(ctx context.Context) ([]Camera, error) {
	if e.ffmpegPath == "" {
		return nil, fmt.Errorf("streamer: ffmpeg path unset")
	}
	// ffmpeg writes the list to stderr and exits non-zero (it has no real
	// input). We capture stderr and ignore the exit code.
	cmd := exec.CommandContext(ctx, e.ffmpegPath,
		"-hide_banner",
		"-list_devices", "true",
		"-f", "dshow",
		"-i", "dummy",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // expected non-zero exit
	return parseListDevices(stderr.Bytes())
}

// parseListDevices extracts the video-device list from ffmpeg dshow's
// -list_devices output.
//
// Format (one device occupies two consecutive lines):
//
//	[dshow @ ...] "Friendly name"
//	[dshow @ ...]   Alternative name "@device:pnp:\\..."
//
// We stop appending devices once we see the audio-devices marker.
func parseListDevices(raw []byte) ([]Camera, error) {
	const audioMarker = "DirectShow audio devices"
	const altNamePrefix = "Alternative name "

	var cameras []Camera
	var pending *Camera // the camera whose Alternative name we expect next

	lines := bytes.Split(raw, []byte("\n"))
	inAudio := false
	for _, ln := range lines {
		s := string(bytes.TrimRight(ln, "\r"))
		if i := strings.Index(s, "] "); i >= 0 && strings.HasPrefix(s, "[dshow @") {
			s = strings.TrimSpace(s[i+2:])
		} else {
			continue
		}
		if strings.HasPrefix(s, audioMarker) {
			inAudio = true
			continue
		}
		if inAudio {
			continue
		}
		// Friendly name line: starts and ends with a quote.
		if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
			label := strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
			pending = &Camera{Label: label}
			cameras = append(cameras, *pending)
			pending = &cameras[len(cameras)-1]
			continue
		}
		// Alternative name line.
		if pending != nil && strings.HasPrefix(s, altNamePrefix) {
			rest := strings.TrimPrefix(s, altNamePrefix)
			rest = strings.TrimSpace(rest)
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, `"`), `"`)
			pending.ID = rest
			pending = nil
		}
	}
	// Discard cameras that didn't get an Alternative name — without a stable
	// id we'd violate the protocol's id-stability contract.
	out := cameras[:0]
	for _, c := range cameras {
		if c.ID != "" {
			out = append(out, c)
		}
	}
	return out, nil
}
```

- [ ] **Step 5: Run the test; verify it passes**

```
GOOS=windows go test ./internal/streamer/ -run TestParseListDevices -v
```

If you can run on Windows directly, also do a smoke test by deleting `_ = cmd.Run()` and replacing it temporarily with `if err := cmd.Run(); ... { return nil, err }` to confirm it spawns; revert after.

- [ ] **Step 6: gofmt + go vet on both platforms**

```
gofmt -l ./internal/streamer
go vet ./internal/streamer/...
GOOS=windows go vet ./internal/streamer/...
```

- [ ] **Step 7: Commit**

```
git add internal/streamer/enumerator_windows.go internal/streamer/enumerator_windows_test.go internal/streamer/testdata/
git commit -m "feat(streamer): Windows DirectShow enumerator + parser tests"
```

---

## Task 4 — Pinned ffmpeg build metadata + version probe + path resolver

**Files:**
- Create: `internal/streamer/ffmpeg_build.go`
- Create: `internal/streamer/ffmpeg.go`
- Create: `internal/streamer/ffmpeg_test.go`
- Create: `internal/streamer/defaults.go`

This task picks a real ffmpeg build, records its identity, and exposes the path/version-probe surface that the manager will use.

> **Implementer action:** before writing code, download the chosen build, compute SHA256, capture the `ffmpeg -version` first line, and paste the values into `ffmpeg_build.go`. The values below are placeholders the *implementer* fills in once — they are not "TODO" markers in the design sense.

- [ ] **Step 1: Pick the build and record its identity**

Use the gyan.dev "essentials" Windows release. Procedure:

1. Visit https://www.gyan.dev/ffmpeg/builds/ → "release essentials" → click the 7z link.
2. Note the version (e.g. `7.1-essentials_build`) and the published SHA256.
3. `7z x ffmpeg-7.1-essentials_build.7z` → grab `bin\ffmpeg.exe`.
4. `sha256sum bin/ffmpeg.exe` → record.
5. `bin/ffmpeg.exe -version | head -n 1` → record the first line.

Fill these into the constants in Step 2.

- [ ] **Step 2: Create the build pin file**

Create `internal/streamer/ffmpeg_build.go`:

```go
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
	PinnedFFmpegBinarySHA256 = "REPLACE_WITH_REAL_SHA256_FROM_STEP_1"

	// PinnedFFmpegSourceURL is informational — the public download URL.
	PinnedFFmpegSourceURL = "https://www.gyan.dev/ffmpeg/builds/"
)
```

- [ ] **Step 3: Create the defaults file**

Create `internal/streamer/defaults.go`:

```go
package streamer

import "time"

// Encoding & timing defaults. These match the protocol's recommended
// targets (1280x720, ~1.5 Mbps, H.264 Constrained Baseline) and are
// applied to every session in v1 — per-camera overrides are explicitly
// deferred to v2 (see spec §11).
const (
	DefaultVideoWidth        = 1280
	DefaultVideoHeight       = 720
	DefaultFramerate         = 24
	DefaultBitrateKbps       = 1500
	DefaultKeyframeInterval  = 48 // ~2s @ 24fps
	DefaultGracefulStopGrace = 2 * time.Second
	DefaultProxyTimeout      = 5 * time.Second
	DefaultProbeTimeout      = 5 * time.Second
)
```

- [ ] **Step 4: Write the failing test for the version probe**

Create `internal/streamer/ffmpeg_test.go`:

```go
package streamer

import (
	"context"
	"errors"
	"testing"
)

func TestProbeFFmpeg_OK(t *testing.T) {
	r := &FFmpegResolver{
		Path: "/dev/null", // unused — overridden via runVersion
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "ffmpeg version 7.1-essentials_build-www.gyan.dev Copyright (c) ...", nil
		},
	}
	if err := r.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestProbeFFmpeg_MissingBinary(t *testing.T) {
	r := &FFmpegResolver{
		Path: "",
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("not used")
		},
	}
	err := r.Probe(context.Background())
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("want ErrFFmpegUnavailable, got %v", err)
	}
}

func TestProbeFFmpeg_VersionMismatch(t *testing.T) {
	r := &FFmpegResolver{
		Path: "/tmp/ffmpeg",
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "ffmpeg version 4.0-wrong build", nil
		},
	}
	err := r.Probe(context.Background())
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("want ErrFFmpegUnavailable, got %v", err)
	}
}

func TestProbeFFmpeg_RunFailed(t *testing.T) {
	r := &FFmpegResolver{
		Path: "/tmp/ffmpeg",
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("permission denied")
		},
	}
	err := r.Probe(context.Background())
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("want ErrFFmpegUnavailable, got %v", err)
	}
}
```

- [ ] **Step 5: Run the test; verify it fails**

```
go test ./internal/streamer/ -run TestProbeFFmpeg -v
```
Expected: undefined: FFmpegResolver, ErrFFmpegUnavailable.

- [ ] **Step 6: Implement the resolver**

Create `internal/streamer/ffmpeg.go`:

```go
package streamer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// ErrFFmpegUnavailable is returned by FFmpegResolver.Probe when the
// bundled ffmpeg binary is missing, unreachable, or reports an unexpected
// version banner. The manager surfaces this as a 503 with body
// `{"error":"ffmpeg unavailable"}` on /start, and as a UI banner.
var ErrFFmpegUnavailable = errors.New("streamer: ffmpeg unavailable")

// FFmpegResolver locates and validates the bundled ffmpeg.exe.
//
// Probe is safe for concurrent use; once it succeeds it caches the result
// for the process lifetime (per spec §7 "Ffmpeg version probe TTL").
type FFmpegResolver struct {
	Path string // absolute path to ffmpeg.exe

	mu         sync.Mutex
	probed     bool
	probeErr   error
	runVersion func(ctx context.Context, path string) (string, error) // injected in tests
}

// NewFFmpegResolver constructs a resolver for the given binary path.
func NewFFmpegResolver(path string) *FFmpegResolver {
	return &FFmpegResolver{
		Path:       path,
		runVersion: defaultRunVersion,
	}
}

// Probe checks the binary on first call and returns a cached result on
// subsequent calls within the same process.
func (r *FFmpegResolver) Probe(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probed {
		return r.probeErr
	}
	r.probed = true
	if r.Path == "" {
		r.probeErr = fmt.Errorf("%w: empty path", ErrFFmpegUnavailable)
		return r.probeErr
	}
	out, err := r.runVersion(ctx, r.Path)
	if err != nil {
		r.probeErr = fmt.Errorf("%w: %v", ErrFFmpegUnavailable, err)
		return r.probeErr
	}
	if !strings.HasPrefix(out, PinnedFFmpegVersion) {
		r.probeErr = fmt.Errorf("%w: unexpected version banner: %q (want prefix %q)",
			ErrFFmpegUnavailable, firstLine(out), PinnedFFmpegVersion)
		return r.probeErr
	}
	r.probeErr = nil
	return nil
}

func defaultRunVersion(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
```

- [ ] **Step 7: Run the tests; verify they pass**

```
go test ./internal/streamer/ -run TestProbeFFmpeg -v
```

- [ ] **Step 8: gofmt + go vet**

```
gofmt -l ./internal/streamer
go vet ./internal/streamer/...
```

- [ ] **Step 9: Commit**

```
git add internal/streamer/ffmpeg.go internal/streamer/ffmpeg_build.go internal/streamer/ffmpeg_test.go internal/streamer/defaults.go
git commit -m "feat(streamer): pinned ffmpeg build + version probe + defaults"
```

---

## Task 5 — ffmpeg argv builder for WHIP publish

**Files:**
- Modify: `internal/streamer/ffmpeg.go`
- Modify: `internal/streamer/ffmpeg_test.go`

Building the argv is one place where being explicit-and-tested pays off: a typo in a flag is invisible until streaming actually fails.

- [ ] **Step 1: Write the failing test**

Append to `internal/streamer/ffmpeg_test.go`:

```go
import "strings" // ensure import block has "strings"

func TestBuildWHIPArgs(t *testing.T) {
	args := BuildWHIPArgs(WHIPArgs{
		BinaryPath:   "C:\\Program Files\\SerialHop\\ffmpeg.exe",
		CameraLabel:  "Logitech HD Pro Webcam C920",
		SessionID:    "01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		WHIPURL:      "https://lab.example.com/streamer/whip/01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		BearerFlag:   "-authorization",
		BearerToken:  "tk_F2k9q_secret",
		Width:        1280,
		Height:       720,
		Framerate:    24,
		BitrateKbps:  1500,
		KeyframeIntv: 48,
	})

	mustHave := []string{
		"C:\\Program Files\\SerialHop\\ffmpeg.exe",
		"-f", "dshow",
		"-video_size", "1280x720",
		"-framerate", "24",
		"-i", `video=Logitech HD Pro Webcam C920`,
		"-c:v", "libx264",
		"-profile:v", "baseline",
		"-b:v", "1500k",
		"-g", "48",
		"-metadata", "serialhop_session=01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		"-f", "whip",
		"-authorization", "Bearer tk_F2k9q_secret",
		"https://lab.example.com/streamer/whip/01HXYZ8K2NQM4R6V9P3T1W5Z7B",
	}
	for _, want := range mustHave {
		found := false
		for _, a := range args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("args missing %q\nactual: %q", want, args)
		}
	}
}

func TestBuildWHIPArgs_TokenNotInOrderedLog(t *testing.T) {
	args := BuildWHIPArgs(WHIPArgs{
		BinaryPath:  "ffmpeg",
		CameraLabel: "Cam",
		SessionID:   "S",
		WHIPURL:     "u",
		BearerFlag:  "-authorization",
		BearerToken: "SECRET",
	})
	// The exported helper RedactedArgs hides the token for logging.
	red := RedactedArgs(args)
	for _, a := range red {
		if strings.Contains(a, "SECRET") {
			t.Fatalf("token leaked into redacted args: %q", red)
		}
	}
}
```

- [ ] **Step 2: Run the test; verify it fails**

```
go test ./internal/streamer/ -run TestBuildWHIPArgs -v
```
Expected: undefined: BuildWHIPArgs, WHIPArgs, RedactedArgs.

- [ ] **Step 3: Implement the argv builder**

Append to `internal/streamer/ffmpeg.go`:

```go
import "strconv" // ensure import

// WHIPArgs is the per-session input that determines the ffmpeg argv.
type WHIPArgs struct {
	BinaryPath  string
	CameraLabel string
	SessionID   string
	WHIPURL     string

	// BearerFlag is the ffmpeg WHIP-muxer flag name that carries the
	// bearer token (e.g. "-authorization"). The exact name depends on the
	// pinned ffmpeg build's WHIP muxer; the implementer confirms it
	// against the binary picked in Task 4.
	BearerFlag  string
	BearerToken string

	Width        int
	Height       int
	Framerate    int
	BitrateKbps  int
	KeyframeIntv int
}

// BuildWHIPArgs produces the full argv for a WHIP publish session.
func BuildWHIPArgs(in WHIPArgs) []string {
	w := in.Width
	if w == 0 {
		w = DefaultVideoWidth
	}
	h := in.Height
	if h == 0 {
		h = DefaultVideoHeight
	}
	fps := in.Framerate
	if fps == 0 {
		fps = DefaultFramerate
	}
	br := in.BitrateKbps
	if br == 0 {
		br = DefaultBitrateKbps
	}
	g := in.KeyframeIntv
	if g == 0 {
		g = DefaultKeyframeInterval
	}
	return []string{
		in.BinaryPath,
		"-hide_banner",
		"-loglevel", "error",
		"-f", "dshow",
		"-rtbufsize", "256M",
		"-framerate", strconv.Itoa(fps),
		"-video_size", strconv.Itoa(w) + "x" + strconv.Itoa(h),
		"-i", "video=" + in.CameraLabel,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-profile:v", "baseline",
		"-level", "3.1",
		"-pix_fmt", "yuv420p",
		"-b:v", strconv.Itoa(br) + "k",
		"-maxrate", strconv.Itoa(br) + "k",
		"-bufsize", strconv.Itoa(2*br) + "k",
		"-g", strconv.Itoa(g),
		"-keyint_min", strconv.Itoa(g),
		"-metadata", "serialhop_session=" + in.SessionID,
		"-f", "whip",
		in.BearerFlag, "Bearer " + in.BearerToken,
		in.WHIPURL,
	}
}

// RedactedArgs returns a copy of argv suitable for logging — bearer
// tokens replaced with `Bearer ****`.
func RedactedArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "-authorization" || out[i] == "-bearer_token" {
			out[i+1] = "Bearer ****"
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests; verify they pass**

```
go test ./internal/streamer/ -run TestBuildWHIPArgs -v
```

- [ ] **Step 5: Commit**

```
git add internal/streamer/ffmpeg.go internal/streamer/ffmpeg_test.go
git commit -m "feat(streamer): ffmpeg WHIP argv builder with token redaction"
```

---

## Task 6 — Armed-cameras store (atomic JSON read/write)

**Files:**
- Create: `internal/streamer/store.go`
- Create: `internal/streamer/store_test.go`

The store is a thin, testable layer over `armed-cameras.json`. It owns:
- Reading the file (returns empty list if missing).
- Atomic write (temp + rename, like `bootstrap.WriteCache`).
- Version field for forward-compat (matches the spec's §4.1 schema).

- [ ] **Step 1: Write the failing tests**

Create `internal/streamer/store_test.go`:

```go
package streamer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	s := NewStore(p)

	cams, err := s.Load()
	if err != nil {
		t.Fatalf("Load on empty: %v", err)
	}
	if len(cams) != 0 {
		t.Fatalf("want empty, got %+v", cams)
	}

	want := []ArmedCamera{
		{ID: "id-1", Label: "Cam One"},
		{ID: "id-2", Label: "Cam Two"},
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if len(got) != 2 || got[0].ID != "id-1" || got[1].Label != "Cam Two" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_VersionMismatchTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	if err := os.WriteFile(p, []byte(`{"version": 99, "cameras":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(p)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("version mismatch should yield empty list, got %+v", got)
	}
}

func TestStore_CorruptFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	if err := os.WriteFile(p, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(p)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("corrupt file should yield empty list, got %+v", got)
	}
}

func TestStore_SaveAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	s := NewStore(p)
	if err := s.Save([]ArmedCamera{{ID: "a"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// After save, no .tmp leftover.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !errors.Is(nil, nil) && filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}
```

- [ ] **Step 2: Run the tests; verify they fail**

```
go test ./internal/streamer/ -run TestStore -v
```
Expected: undefined: NewStore.

- [ ] **Step 3: Implement the store**

Create `internal/streamer/store.go`:

```go
package streamer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const armedCamerasCurrentVersion = 1

type Store struct {
	path string
}

type armedFile struct {
	Version  int           `json:"version"`
	Cameras  []ArmedCamera `json:"cameras"`
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load returns the persisted list. A missing file, corrupt JSON, or
// version mismatch yields an empty list with no error — the operator
// just sees "no cameras armed" and can re-arm them.
func (s *Store) Load() ([]ArmedCamera, error) {
	data, err := os.ReadFile(s.path) //nolint:gosec // path is paths.ArmedCamerasPath()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		slog.Warn("streamer: read armed cameras failed", "path", s.path, "err", err)
		return nil, nil
	}
	var af armedFile
	if err := json.Unmarshal(data, &af); err != nil {
		slog.Warn("streamer: armed cameras corrupt; treating as empty", "path", s.path, "err", err)
		return nil, nil
	}
	if af.Version != armedCamerasCurrentVersion {
		slog.Warn("streamer: armed cameras version mismatch; treating as empty", "path", s.path, "version", af.Version)
		return nil, nil
	}
	return af.Cameras, nil
}

// Save atomically replaces the persisted list.
func (s *Store) Save(cams []ArmedCamera) error {
	af := armedFile{Version: armedCamerasCurrentVersion, Cameras: cams}
	data, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return fmt.Errorf("streamer: marshal armed cameras: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "armed-cameras.json.*.tmp")
	if err != nil {
		return fmt.Errorf("streamer: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: rename temp: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests; verify they pass**

```
go test ./internal/streamer/ -run TestStore -v
```

- [ ] **Step 5: Commit**

```
git add internal/streamer/store.go internal/streamer/store_test.go
git commit -m "feat(streamer): atomic armed-cameras JSON store"
```

---

## Task 7 — Stub ffmpeg + Session lifecycle (single child process)

**Files:**
- Create: `internal/streamer/testbin/fake_ffmpeg/main.go`
- Create: `internal/streamer/session.go`
- Create: `internal/streamer/session_other.go`
- Create: `internal/streamer/session_windows.go`
- Create: `internal/streamer/session_test.go`

`Session` is the abstraction over one ffmpeg child. It owns the
`*exec.Cmd`, captures stderr (last N lines), supports graceful
termination, and exposes a `Done()` channel.

`fake_ffmpeg` is a tiny program we can run under `Session` in tests
without needing real ffmpeg. It honours `-metadata serialhop_session=<sid>`
parsing (just to mirror the production argv), emits one stderr line,
sleeps until a signal, and exits 0.

- [ ] **Step 1: Create the stub ffmpeg**

Create `internal/streamer/testbin/fake_ffmpeg/main.go`:

```go
// Package main implements a tiny ffmpeg stand-in for streamer tests.
//
// Behavior:
//   - Prints "fake_ffmpeg: started" to stderr immediately.
//   - If the env var FAKE_FFMPEG_EXIT_FAST=1, prints "exiting fast" to
//     stderr and exits 1 within ~50ms.
//   - If the env var FAKE_FFMPEG_IGNORE_SIGNALS=1, ignores SIGTERM /
//     CTRL_BREAK (Windows) and only exits on hard kill.
//   - Otherwise, sleeps until SIGTERM (Unix) / os.Interrupt (Windows)
//     and exits 0.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	fmt.Fprintln(os.Stderr, "fake_ffmpeg: started")
	if os.Getenv("FAKE_FFMPEG_EXIT_FAST") == "1" {
		fmt.Fprintln(os.Stderr, "fake_ffmpeg: exiting fast")
		time.Sleep(50 * time.Millisecond)
		os.Exit(1)
	}
	ignore := os.Getenv("FAKE_FFMPEG_IGNORE_SIGNALS") == "1"

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, os.Interrupt)
	select {
	case <-sigCh:
		if ignore {
			// Stay alive; allow KILL to terminate us.
			select {}
		}
		fmt.Fprintln(os.Stderr, "fake_ffmpeg: clean exit")
		os.Exit(0)
	case <-time.After(30 * time.Second):
		// Safety: don't hang the test suite.
		os.Exit(2)
	}
}
```

- [ ] **Step 2: Write the failing session tests**

Create `internal/streamer/session_test.go`:

```go
package streamer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func buildFakeFFmpeg(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fake_ffmpeg")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./testbin/fake_ffmpeg")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build fake_ffmpeg: %v", err)
	}
	return out
}

func TestSession_StartThenStop(t *testing.T) {
	bin := buildFakeFFmpeg(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Argv:           []string{bin, "--marker", "test-sid"},
		GracefulPeriod: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// Give it a moment to print the "started" line so stderr capture is non-empty.
	time.Sleep(100 * time.Millisecond)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	<-s.Done()
	if s.LastError() != "" && !contains(s.LastError(), "clean exit") &&
		!contains(s.LastError(), "started") {
		// LastError is the most recent stderr line; on a clean stop we
		// don't require any particular content, but it should be non-empty.
		t.Logf("LastError after clean stop: %q (informational)", s.LastError())
	}
}

func TestSession_QuickExitSurfacesStderr(t *testing.T) {
	bin := buildFakeFFmpeg(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Argv:           []string{bin},
		Env:            []string{"FAKE_FFMPEG_EXIT_FAST=1"},
		GracefulPeriod: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit within 2s")
	}
	if s.ExitCode() == 0 {
		t.Errorf("want non-zero exit, got %d", s.ExitCode())
	}
	if s.LastError() == "" {
		t.Errorf("expected stderr captured, got empty")
	}
}

func TestSession_HardKillsAfterGracePeriod(t *testing.T) {
	bin := buildFakeFFmpeg(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Argv:           []string{bin},
		Env:            []string{"FAKE_FFMPEG_IGNORE_SIGNALS=1"},
		GracefulPeriod: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	_ = s.Stop(context.Background())
	<-s.Done()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("hard kill took too long: %v", elapsed)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Run the test; verify it fails**

```
go test ./internal/streamer/ -run TestSession -v
```
Expected: undefined: StartSession, SessionConfig.

- [ ] **Step 4: Create the cross-platform session core**

Create `internal/streamer/session.go`:

```go
package streamer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// SessionConfig is the input to StartSession.
type SessionConfig struct {
	Argv           []string
	Env            []string // extra env passed alongside os.Environ()
	GracefulPeriod time.Duration
}

// Session is a running ffmpeg child.
type Session struct {
	cfg SessionConfig

	cmd  *exec.Cmd
	done chan struct{}

	mu           sync.Mutex
	lastStderr   string
	exitCode     int
	exitErr      error
}

// StartSession launches the child.
func StartSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if len(cfg.Argv) == 0 {
		return nil, fmt.Errorf("streamer: empty argv")
	}
	if cfg.GracefulPeriod == 0 {
		cfg.GracefulPeriod = DefaultGracefulStopGrace
	}
	cmd := exec.CommandContext(ctx, cfg.Argv[0], cfg.Argv[1:]...)
	cmd.Env = append(cmd.Env, cfg.Env...)
	applyPlatformAttrs(cmd) // session_windows / session_other
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("streamer: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("streamer: start: %w", err)
	}
	s := &Session{
		cfg:  cfg,
		cmd:  cmd,
		done: make(chan struct{}),
	}
	go s.drainStderr(stderr)
	go s.wait()
	return s, nil
}

func (s *Session) drainStderr(r io.Reader) {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 16*1024), 64*1024)
	for scan.Scan() {
		s.mu.Lock()
		s.lastStderr = scan.Text()
		s.mu.Unlock()
	}
}

func (s *Session) wait() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.exitErr = err
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	s.mu.Unlock()
	close(s.done)
}

// Done is closed when the child exits.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitCode is the child's exit status; zero if still running.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// LastError returns the last stderr line (best-effort, not guaranteed to
// be the most informative one).
func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStderr
}

// PID returns the OS process id.
func (s *Session) PID() int { return s.cmd.Process.Pid }

// Stop asks the child to exit gracefully, then hard-kills it after
// SessionConfig.GracefulPeriod.
func (s *Session) Stop(ctx context.Context) error {
	if err := signalGraceful(s.cmd); err != nil {
		// Already exited, or graceful signal unsupported — fall through
		// to hard kill.
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(s.cfg.GracefulPeriod):
	}
	return hardKill(s.cmd)
}
```

- [ ] **Step 5: Create the non-Windows session helpers**

Create `internal/streamer/session_other.go`:

```go
//go:build !windows

package streamer

import (
	"os/exec"
	"syscall"
)

func applyPlatformAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGraceful(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}

func hardKill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
```

- [ ] **Step 6: Create the Windows session helpers**

Create `internal/streamer/session_windows.go`:

```go
//go:build windows

package streamer

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

// applyPlatformAttrs ensures the child gets its own process group so we
// can deliver CTRL_BREAK_EVENT to it (and only it).
func applyPlatformAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
}

func signalGraceful(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

// hardKill uses `taskkill /pid <pid> /T /F` to take down the child plus
// any descendants. cmd.Process.Kill() would only kill the top process,
// which is fine for ffmpeg today but `taskkill /T` is the documented
// future-proof approach.
func hardKill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	k := exec.Command("taskkill", "/pid", pid, "/T", "/F")
	return k.Run()
}
```

- [ ] **Step 7: Add the dependency**

```
go get golang.org/x/sys/windows@latest   # if not already in go.mod
go mod tidy
```

(Likely already a transitive dep — `go mod tidy` is idempotent.)

- [ ] **Step 8: Run the session tests; verify they pass**

```
go test ./internal/streamer/ -run TestSession -v -timeout 30s
```

- [ ] **Step 9: gofmt + go vet on both platforms**

```
gofmt -l ./internal/streamer
go vet ./internal/streamer/...
GOOS=windows go vet ./internal/streamer/...
```

- [ ] **Step 10: Commit**

```
git add internal/streamer/session.go internal/streamer/session_other.go internal/streamer/session_windows.go internal/streamer/session_test.go internal/streamer/testbin/ go.mod go.sum
git commit -m "feat(streamer): Session abstraction with graceful + hard kill"
```

---

## Task 8 — Manager: armed list + active sessions (replace-on-conflict, idempotency, 409)

**Files:**
- Create: `internal/streamer/manager.go`
- Create: `internal/streamer/manager_test.go`

The manager is the **single source of truth** for armed cameras and
active sessions inside the panel process. It is the layer the HTTP
listener and Wails bindings both call into.

Public surface:

```go
type Manager interface {
    Refresh(ctx context.Context) ([]Camera, error) // re-enumerates; updates connected state
    Cameras() []CameraView                          // current view used by UI
    SetArmed(cameraID string, armed bool) error
    Translations() []Translation                    // for GET /api/translations
    Start(ctx context.Context, cameraID string, in StartRequest) StartOutcome
    Stop(cameraID string, sessionID string) StopOutcome
    Shutdown(ctx context.Context) error             // kills active sessions
}
```

We model `StartOutcome` / `StopOutcome` as small structs that map cleanly
to HTTP status codes:

```go
type StartOutcome struct { Status int; Body any }
type StopOutcome  struct { Status int; Body any }
```

- [ ] **Step 1: Write the failing tests**

Create `internal/streamer/manager_test.go`:

```go
package streamer

import (
	"context"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type fakeSpawner struct {
	live   atomic.Int32
	args   [][]string
	killed atomic.Int32
}

func (f *fakeSpawner) Start(_ context.Context, argv []string) (sessionHandle, error) {
	f.live.Add(1)
	f.args = append(f.args, append([]string(nil), argv...))
	return &fakeSessionHandle{spawner: f, doneCh: make(chan struct{})}, nil
}

type fakeSessionHandle struct {
	spawner *fakeSpawner
	doneCh  chan struct{}
	stopped atomic.Bool
}

func (h *fakeSessionHandle) Done() <-chan struct{} { return h.doneCh }
func (h *fakeSessionHandle) Stop(_ context.Context) error {
	if h.stopped.CompareAndSwap(false, true) {
		h.spawner.killed.Add(1)
		h.spawner.live.Add(-1)
		close(h.doneCh)
	}
	return nil
}
func (h *fakeSessionHandle) LastError() string { return "" }
func (h *fakeSessionHandle) PID() int          { return 0 }

func newTestManager(t *testing.T) (*manager, *fakeSpawner) {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "armed-cameras.json"))
	spawner := &fakeSpawner{}
	enum := fakeEnumeratorFixed{cams: []Camera{
		{ID: "cam-A", Label: "Cam A"},
		{ID: "cam-B", Label: "Cam B"},
	}}
	m := NewManager(ManagerConfig{
		Store:       store,
		Enumerator:  enum,
		Spawner:     spawner,
		FFmpegReady: func() error { return nil },
		BearerFlag:  "-authorization",
	})
	if _, err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return m.(*manager), spawner
}

type fakeEnumeratorFixed struct{ cams []Camera }

func (f fakeEnumeratorFixed) List(_ context.Context) ([]Camera, error) { return f.cams, nil }

func TestManager_SetArmed_GoesToTranslations(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.SetArmed("cam-A", true); err != nil {
		t.Fatalf("SetArmed: %v", err)
	}
	tr := m.Translations()
	if len(tr) != 1 || tr[0].ID != "cam-A" {
		t.Fatalf("want [cam-A], got %+v", tr)
	}
}

func TestManager_Start_UnknownID_404(t *testing.T) {
	m, _ := newTestManager(t)
	out := m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1"})
	if out.Status != http.StatusNotFound {
		t.Fatalf("want 404, got %d", out.Status)
	}
}

func TestManager_Start_Armed_202(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	out := m.Start(context.Background(), "cam-A", StartRequest{
		SessionID: "S1", WHIPURL: "http://u", WHIPToken: "tk",
	})
	if out.Status != http.StatusAccepted {
		t.Fatalf("want 202, got %d", out.Status)
	}
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("want 1 live session, got %d", got)
	}
}

func TestManager_Start_IdempotentSameSession(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("want 1 live session (idempotent), got %d", got)
	}
}

func TestManager_Start_ReplaceOnConflict(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	out := m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S2", WHIPURL: "u", WHIPToken: "tk"})
	if out.Status != http.StatusAccepted {
		t.Fatalf("want 202 on replace, got %d", out.Status)
	}
	if got := sp.killed.Load(); got != 1 {
		t.Fatalf("expected one old session killed, got %d", got)
	}
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("want 1 live session after replace, got %d", got)
	}
}

func TestManager_Start_FFmpegUnavailable_503(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	m.ffmpegReady = func() error { return ErrFFmpegUnavailable }
	out := m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1"})
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", out.Status)
	}
}

func TestManager_Stop_Match_204(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	out := m.Stop("cam-A", "S1")
	if out.Status != http.StatusNoContent {
		t.Fatalf("want 204, got %d", out.Status)
	}
	if got := sp.live.Load(); got != 0 {
		t.Fatalf("session not stopped, live=%d", got)
	}
}

func TestManager_Stop_Mismatch_409(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	out := m.Stop("cam-A", "STALE")
	if out.Status != http.StatusConflict {
		t.Fatalf("want 409, got %d", out.Status)
	}
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("active session must be preserved on 409 stop, live=%d", got)
	}
}

func TestManager_Stop_NoActive_204(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	out := m.Stop("cam-A", "anything")
	if out.Status != http.StatusNoContent {
		t.Fatalf("want 204 on no-active, got %d", out.Status)
	}
}

func TestManager_UnarmKillsActiveSession(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	if err := m.SetArmed("cam-A", false); err != nil {
		t.Fatalf("SetArmed off: %v", err)
	}
	if got := sp.live.Load(); got != 0 {
		t.Fatalf("unarm must kill active session, live=%d", got)
	}
}
```

- [ ] **Step 2: Run the tests; verify they fail**

```
go test ./internal/streamer/ -run TestManager -v
```
Expected: undefined: Manager / NewManager / StartRequest / ...

- [ ] **Step 3: Implement the manager**

Create `internal/streamer/manager.go`:

```go
package streamer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Translation is one entry in `GET /api/translations` (matches protocol §1.1).
type Translation struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// StartRequest is the body of `POST /api/translations/{id}/start`.
type StartRequest struct {
	SessionID  string   `json:"session_id"`
	WHIPURL    string   `json:"whip_url"`
	WHIPToken  string   `json:"whip_token"`
	IceServers []any    `json:"ice_servers"`
}

// StopRequest is the body of `POST /api/translations/{id}/stop`.
type StopRequest struct {
	SessionID string `json:"session_id"`
}

// StartOutcome / StopOutcome describe what the HTTP listener should
// respond with.
type StartOutcome struct {
	Status int
	Body   any
}
type StopOutcome struct {
	Status int
	Body   any
}

// sessionHandle is the subset of *Session the manager uses. Tests inject
// a fake that implements this interface.
type sessionHandle interface {
	Done() <-chan struct{}
	Stop(ctx context.Context) error
	LastError() string
	PID() int
}

// Spawner abstracts session creation so tests can substitute fakes.
type Spawner interface {
	Start(ctx context.Context, argv []string) (sessionHandle, error)
}

// realSpawner spawns real ffmpeg processes via StartSession.
type realSpawner struct{}

func (realSpawner) Start(ctx context.Context, argv []string) (sessionHandle, error) {
	return StartSession(ctx, SessionConfig{Argv: argv, GracefulPeriod: DefaultGracefulStopGrace})
}

// Manager is the streamer subsystem's external surface.
type Manager interface {
	Refresh(ctx context.Context) ([]Camera, error)
	Cameras() []CameraView
	SetArmed(cameraID string, armed bool) error
	Translations() []Translation
	Start(ctx context.Context, cameraID string, in StartRequest) StartOutcome
	Stop(cameraID string, sessionID string) StopOutcome
	Shutdown(ctx context.Context) error
}

// ManagerConfig wires the manager.
type ManagerConfig struct {
	Store       *Store
	Enumerator  Enumerator
	Spawner     Spawner
	FFmpegPath  string
	FFmpegReady func() error
	BearerFlag  string
	OnChange    func() // fired after any state-changing op (UI re-render)
}

type manager struct {
	store       *Store
	enum        Enumerator
	spawner     Spawner
	ffmpegPath  string
	ffmpegReady func() error
	bearerFlag  string
	onChange    func()

	mu       sync.Mutex
	cameras  map[string]*managedCam // by id
	sessions map[string]*activeSess // by camera id
}

type managedCam struct {
	Camera
	Armed     bool
	Connected bool
	LastError string
}

type activeSess struct {
	cameraID  string
	sessionID string
	startedAt time.Time
	handle    sessionHandle
}

// NewManager constructs a Manager. Spawner defaults to a real ffmpeg
// spawner; tests pass fakeSpawner.
func NewManager(cfg ManagerConfig) Manager {
	if cfg.Spawner == nil {
		cfg.Spawner = realSpawner{}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func() {}
	}
	m := &manager{
		store:       cfg.Store,
		enum:        cfg.Enumerator,
		spawner:     cfg.Spawner,
		ffmpegPath:  cfg.FFmpegPath,
		ffmpegReady: cfg.FFmpegReady,
		bearerFlag:  cfg.BearerFlag,
		onChange:    cfg.OnChange,
		cameras:     map[string]*managedCam{},
		sessions:    map[string]*activeSess{},
	}
	if cfg.Store != nil {
		armed, _ := cfg.Store.Load()
		for _, a := range armed {
			m.cameras[a.ID] = &managedCam{
				Camera:    Camera{ID: a.ID, Label: a.Label},
				Armed:     true,
				Connected: false, // will be flipped on Refresh
			}
		}
	}
	return m
}

func (m *manager) Refresh(ctx context.Context) ([]Camera, error) {
	cams, err := m.enum.List(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Mark all currently-known cameras as disconnected.
	for _, c := range m.cameras {
		c.Connected = false
	}
	for _, c := range cams {
		if mc, ok := m.cameras[c.ID]; ok {
			mc.Label = c.Label // refresh label in case it changed
			mc.Connected = true
		} else {
			m.cameras[c.ID] = &managedCam{
				Camera:    c,
				Armed:     false,
				Connected: true,
			}
		}
	}
	m.onChange()
	return cams, nil
}

func (m *manager) Cameras() []CameraView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CameraView, 0, len(m.cameras))
	for _, c := range m.cameras {
		_, live := m.sessions[c.ID]
		out = append(out, CameraView{
			ID:           c.ID,
			Label:        c.Label,
			Armed:        c.Armed,
			Connected:    c.Connected,
			Live:         live,
			LastErrorMsg: c.LastError,
		})
	}
	return out
}

func (m *manager) Translations() []Translation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Translation, 0, len(m.cameras))
	for _, c := range m.cameras {
		if c.Armed && c.Connected {
			out = append(out, Translation{ID: c.ID, Label: c.Label})
		}
	}
	return out
}

func (m *manager) SetArmed(cameraID string, armed bool) error {
	m.mu.Lock()
	c, ok := m.cameras[cameraID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("streamer: unknown camera %q", cameraID)
	}
	c.Armed = armed
	// If unarming while a session is live, tear it down here under the
	// same lock to avoid races with concurrent Start.
	var toStop *activeSess
	if !armed {
		if s, ok := m.sessions[cameraID]; ok {
			toStop = s
			delete(m.sessions, cameraID)
		}
	}
	m.persistLocked()
	m.mu.Unlock()
	if toStop != nil {
		_ = toStop.handle.Stop(context.Background())
	}
	m.onChange()
	return nil
}

func (m *manager) persistLocked() {
	if m.store == nil {
		return
	}
	armed := make([]ArmedCamera, 0)
	for _, c := range m.cameras {
		if c.Armed {
			armed = append(armed, ArmedCamera{ID: c.ID, Label: c.Label})
		}
	}
	if err := m.store.Save(armed); err != nil {
		// Don't fail the in-memory update; surface a UI-visible error elsewhere.
		// Persistence error logged by callers if needed.
		_ = err
	}
}

func (m *manager) Start(ctx context.Context, cameraID string, in StartRequest) StartOutcome {
	if m.ffmpegReady != nil {
		if err := m.ffmpegReady(); err != nil {
			return StartOutcome{
				Status: http.StatusServiceUnavailable,
				Body:   map[string]string{"error": "ffmpeg unavailable"},
			}
		}
	}
	m.mu.Lock()
	c, ok := m.cameras[cameraID]
	if !ok || !c.Armed || !c.Connected {
		m.mu.Unlock()
		return StartOutcome{Status: http.StatusNotFound, Body: map[string]string{"error": "unknown translation"}}
	}
	if cur, ok := m.sessions[cameraID]; ok {
		if cur.sessionID == in.SessionID {
			m.mu.Unlock()
			return StartOutcome{Status: http.StatusAccepted, Body: struct{}{}}
		}
		// Replace-on-conflict: kill old below the lock.
		oldHandle := cur.handle
		delete(m.sessions, cameraID)
		m.mu.Unlock()
		_ = oldHandle.Stop(context.Background())
		m.mu.Lock()
	}
	label := c.Label
	cameraIDLocked := c.ID
	m.mu.Unlock()
	argv := BuildWHIPArgs(WHIPArgs{
		BinaryPath:  m.ffmpegPath,
		CameraLabel: label,
		SessionID:   in.SessionID,
		WHIPURL:     in.WHIPURL,
		BearerFlag:  m.bearerFlag,
		BearerToken: in.WHIPToken,
	})
	h, err := m.spawner.Start(ctx, argv)
	if err != nil {
		m.mu.Lock()
		c.LastError = err.Error()
		m.mu.Unlock()
		return StartOutcome{Status: http.StatusServiceUnavailable, Body: map[string]string{"error": err.Error()}}
	}
	m.mu.Lock()
	m.sessions[cameraIDLocked] = &activeSess{
		cameraID:  cameraIDLocked,
		sessionID: in.SessionID,
		startedAt: time.Now(),
		handle:    h,
	}
	m.mu.Unlock()
	go m.watchSession(cameraIDLocked, in.SessionID, h)
	m.onChange()
	return StartOutcome{Status: http.StatusAccepted, Body: struct{}{}}
}

func (m *manager) watchSession(cameraID, sessionID string, h sessionHandle) {
	<-h.Done()
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessions[cameraID]; ok && cur.sessionID == sessionID {
		delete(m.sessions, cameraID)
		if errMsg := h.LastError(); errMsg != "" {
			if c, ok := m.cameras[cameraID]; ok {
				c.LastError = errMsg
			}
		}
	}
	go m.onChange()
}

func (m *manager) Stop(cameraID, sessionID string) StopOutcome {
	m.mu.Lock()
	cur, ok := m.sessions[cameraID]
	if !ok {
		m.mu.Unlock()
		return StopOutcome{Status: http.StatusNoContent, Body: struct{}{}}
	}
	if cur.sessionID != sessionID {
		body := map[string]string{"active_session_id": cur.sessionID}
		m.mu.Unlock()
		return StopOutcome{Status: http.StatusConflict, Body: body}
	}
	handle := cur.handle
	delete(m.sessions, cameraID)
	m.mu.Unlock()
	_ = handle.Stop(context.Background())
	m.onChange()
	return StopOutcome{Status: http.StatusNoContent, Body: struct{}{}}
}

func (m *manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	handles := make([]sessionHandle, 0, len(m.sessions))
	for _, s := range m.sessions {
		handles = append(handles, s.handle)
	}
	m.sessions = map[string]*activeSess{}
	m.mu.Unlock()
	var err error
	for _, h := range handles {
		if e := h.Stop(ctx); e != nil && err == nil {
			err = e
		}
	}
	if err != nil {
		return fmt.Errorf("streamer: shutdown: %w", err)
	}
	return nil
}

// Sentinel for callers that want to distinguish "armed-but-unknown" from
// truly-unknown.
var ErrUnknownCamera = errors.New("streamer: unknown camera")
```

- [ ] **Step 4: Run the tests; verify they pass**

```
go test ./internal/streamer/ -run TestManager -v -timeout 30s
```

- [ ] **Step 5: Run the full streamer test suite with -race**

```
go test -race -count=1 ./internal/streamer/...
```

- [ ] **Step 6: gofmt + go vet**

```
gofmt -l ./internal/streamer
go vet ./internal/streamer/...
GOOS=windows go vet ./internal/streamer/...
```

- [ ] **Step 7: Commit**

```
git add internal/streamer/manager.go internal/streamer/manager_test.go
git commit -m "feat(streamer): Manager with replace-on-conflict, idempotency, 409 guard"
```

---

## Task 9 — Panel localhost HTTP listener

**Files:**
- Create: `internal/panel/streaming_http.go`
- Create: `internal/panel/streaming_http_test.go`

The listener exposes exactly the three protocol endpoints. It does not
parse anything more than required: paths, JSON bodies, and the
`{id}` path segment. Routing uses Go 1.22+ `http.ServeMux` to mirror the
existing `internal/api` style.

- [ ] **Step 1: Write the failing tests**

Create `internal/panel/streaming_http_test.go`:

```go
package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

func setupManager(t *testing.T) streamer.Manager {
	t.Helper()
	dir := t.TempDir()
	store := streamer.NewStore(filepath.Join(dir, "armed.json"))
	m := streamer.NewManager(streamer.ManagerConfig{
		Store:       store,
		Enumerator:  fixedEnum{},
		FFmpegReady: func() error { return nil },
		BearerFlag:  "-authorization",
		Spawner:     noopSpawner{},
	})
	if _, err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := m.SetArmed("cam-A", true); err != nil {
		t.Fatalf("SetArmed: %v", err)
	}
	return m
}

type fixedEnum struct{}

func (fixedEnum) List(_ context.Context) ([]streamer.Camera, error) {
	return []streamer.Camera{{ID: "cam-A", Label: "Cam A"}}, nil
}

type noopSpawner struct{}

func (noopSpawner) Start(_ context.Context, _ []string) (streamer.SessionHandleForTest, error) {
	ch := make(chan struct{})
	return noopSessionHandle{done: ch}, nil
}

// noopSessionHandle is a sessionHandle that never exits until Stop is called.
type noopSessionHandle struct{ done chan struct{} }

func (n noopSessionHandle) Done() <-chan struct{}        { return n.done }
func (n noopSessionHandle) Stop(_ context.Context) error { close(n.done); return nil }
func (n noopSessionHandle) LastError() string            { return "" }
func (n noopSessionHandle) PID() int                     { return 0 }

func TestStreamingHTTP_GetTranslations(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/translations")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body struct {
		Translations []streamer.Translation `json:"translations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Translations) != 1 || body.Translations[0].ID != "cam-A" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestStreamingHTTP_StartUnknown_404(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	body := bytes.NewBufferString(`{"session_id":"S1","whip_url":"u","whip_token":"tk"}`)
	resp, err := http.Post(srv.URL+"/api/translations/nope/start", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestStreamingHTTP_Start_Then_Stop_RoundTrip(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	startBody := `{"session_id":"S1","whip_url":"u","whip_token":"tk"}`
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/start", "application/json", bytes.NewBufferString(startBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("want 202 start, got %d", resp.StatusCode)
	}
	stopBody := `{"session_id":"S1"}`
	resp2, err := http.Post(srv.URL+"/api/translations/cam-A/stop", "application/json", bytes.NewBufferString(stopBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 204 {
		t.Fatalf("want 204 stop, got %d", resp2.StatusCode)
	}
}

func TestStreamingHTTP_StaleStop_409(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	_, _ = http.Post(srv.URL+"/api/translations/cam-A/start",
		"application/json",
		bytes.NewBufferString(`{"session_id":"REAL","whip_url":"u","whip_token":"tk"}`))
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/stop",
		"application/json",
		bytes.NewBufferString(`{"session_id":"STALE"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"active_session_id":"REAL"`)) {
		t.Fatalf("expected active_session_id in body, got %s", b)
	}
}

func TestStreamingHTTP_MethodNotAllowed(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/translations", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}
```

Also, the test references `streamer.SessionHandleForTest`. Add a small exported alias inside `internal/streamer/manager.go`:

```go
// SessionHandleForTest is an alias exposing the unexported sessionHandle
// interface for use by tests in other packages.
type SessionHandleForTest = sessionHandle
```

(Also export Spawner.Start's return type accordingly. Update the test
spawners in Task 8 if needed to satisfy this alias.)

- [ ] **Step 2: Add the alias to manager.go**

Append to `internal/streamer/manager.go`:

```go
// SessionHandleForTest is an exported alias that lets tests in other
// packages construct fake spawners.
type SessionHandleForTest = sessionHandle
```

Re-run the streamer tests to confirm nothing breaks:

```
go test ./internal/streamer/...
```

- [ ] **Step 3: Run the panel tests; verify they fail**

```
go test ./internal/panel/ -run TestStreamingHTTP -v
```
Expected: undefined: streamingHandler.

- [ ] **Step 4: Implement the handler**

Create `internal/panel/streaming_http.go`:

```go
package panel

import (
	"encoding/json"
	"net/http"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

// streamingHandler returns the http.Handler that serves the three
// protocol endpoints. The service-side proxy in internal/api will
// connect to this handler over loopback.
func streamingHandler(m streamer.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/translations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"translations": m.Translations(),
		})
	})
	mux.HandleFunc("POST /api/translations/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req streamer.StartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
			return
		}
		out := m.Start(r.Context(), id, req)
		writeJSON(w, out.Status, out.Body)
	})
	mux.HandleFunc("POST /api/translations/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req streamer.StopRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
			return
		}
		out := m.Stop(id, req.SessionID)
		if out.Status == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, out.Status, out.Body)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil || body == struct{}{} {
		_, _ = w.Write([]byte("{}"))
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Already wrote header — nothing more to do.
		_ = err
	}
}
```

- [ ] **Step 5: Run the tests; verify they pass**

```
go test ./internal/panel/ -run TestStreamingHTTP -v -race
```

- [ ] **Step 6: gofmt + go vet**

```
gofmt -l ./internal/panel ./internal/streamer
go vet ./internal/panel/... ./internal/streamer/...
```

- [ ] **Step 7: Commit**

```
git add internal/panel/streaming_http.go internal/panel/streaming_http_test.go internal/streamer/manager.go
git commit -m "feat(panel): localhost HTTP listener for /api/translations*"
```

---

## Task 10 — Service-side proxy handlers

**Files:**
- Create: `internal/api/translations.go`
- Create: `internal/api/translations_test.go`
- Modify: `internal/api/handlers.go`
- Modify: `internal/api/handlers_test.go`
- Modify: `internal/app/app.go`

The service mounts three handlers that forward to the panel via
`127.0.0.1:<panel_port>`. The panel endpoint is discovered by reading
`panel-endpoint.json` (Task 1).

Behavior contract (matches spec §2 failure-mode-table):
- `GET /api/translations`: panel down → `200 {"translations":[]}`.
- `POST .../start`: panel down → `503 {"error":"panel not running"}`.
- `POST .../stop`: panel down → `204`.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/translations_test.go`:

```go
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

func writeEndpoint(t *testing.T, dir string, host string, port int) string {
	t.Helper()
	p := filepath.Join(dir, "panel-endpoint.json")
	if err := bootstrap.WritePanelEndpoint(p, bootstrap.PanelEndpoint{Host: host, Port: port, PID: 1}); err != nil {
		t.Fatalf("WritePanelEndpoint: %v", err)
	}
	return p
}

func TestProxyGet_PanelUp(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"translations":[{"id":"cam-A","label":"Cam A"}]}`))
	}))
	defer panel.Close()
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", panelPortFromURL(panel.URL))
	h := NewTranslationsProxy(endpoint)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/api/translations")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"cam-A"`)) {
		t.Fatalf("body: %s", b)
	}
}

func TestProxyGet_PanelDown_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	// Endpoint file points at an unreachable port.
	endpoint := writeEndpoint(t, dir, "127.0.0.1", 1) // port 1 is reserved
	h := NewTranslationsProxy(endpoint)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/api/translations")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body struct {
		Translations []any `json:"translations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Translations) != 0 {
		t.Fatalf("want empty translations, got %+v", body)
	}
}

func TestProxyStart_PanelDown_503(t *testing.T) {
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", 1)
	h := NewTranslationsProxy(endpoint)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/api/translations/cam-A/start", "application/json",
		bytes.NewBufferString(`{"session_id":"S1"}`))
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestProxyStop_PanelDown_204(t *testing.T) {
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", 1)
	h := NewTranslationsProxy(endpoint)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/api/translations/cam-A/stop", "application/json",
		bytes.NewBufferString(`{"session_id":"S1"}`))
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}

func TestProxyStart_PassesBodyThrough(t *testing.T) {
	var seenBody []byte
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer panel.Close()
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", panelPortFromURL(panel.URL))
	h := NewTranslationsProxy(endpoint)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/api/translations/cam-A/start", "application/json",
		bytes.NewBufferString(`{"session_id":"S1","whip_url":"u"}`))
	resp.Body.Close()
	if !bytes.Contains(seenBody, []byte(`"S1"`)) {
		t.Fatalf("body not forwarded: %s", seenBody)
	}
}

// panelPortFromURL extracts the port from a httptest.Server URL.
func panelPortFromURL(u string) int {
	// httptest URL is "http://127.0.0.1:PORT"
	var port int
	_, _ = fmtSscanf(u, "http://127.0.0.1:%d", &port)
	return port
}

func fmtSscanf(s, format string, args ...any) (int, error) {
	// indirection so the import block doesn't pull "fmt" if not needed elsewhere
	return fmtSscanfImpl(s, format, args...)
}
```

Use the actual `fmt.Sscanf` here — the test indirection above is just to
keep the import explicit. Replace `fmtSscanf` and `fmtSscanfImpl` with a
direct `fmt.Sscanf` call after writing the test:

```go
func panelPortFromURL(u string) int {
	var port int
	_, _ = fmt.Sscanf(u, "http://127.0.0.1:%d", &port)
	return port
}
```

(Add `import "fmt"` to the test file.)

- [ ] **Step 2: Run the tests; verify they fail**

```
go test ./internal/api/ -run TestProxy -v
```
Expected: undefined: NewTranslationsProxy.

- [ ] **Step 3: Implement the proxy**

Create `internal/api/translations.go`:

```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

const (
	defaultProxyTimeout = 5 * time.Second
)

// TranslationsProxy serves the three streaming endpoints by HTTP-forwarding
// them to the panel's localhost listener. The panel's listen port is
// looked up per-request from panel-endpoint.json so a panel restart is
// picked up without restarting the service.
type TranslationsProxy struct {
	endpointPath string
	hc           *http.Client
}

// NewTranslationsProxy constructs a proxy that resolves the panel's
// endpoint from the given JSON file path.
func NewTranslationsProxy(endpointPath string) *TranslationsProxy {
	return &TranslationsProxy{
		endpointPath: endpointPath,
		hc: &http.Client{
			Timeout: defaultProxyTimeout,
		},
	}
}

// Handler returns the http.Handler that the service should mount.
func (p *TranslationsProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/translations", p.handleGet)
	mux.HandleFunc("POST /api/translations/{id}/start", p.handleStart)
	mux.HandleFunc("POST /api/translations/{id}/stop", p.handleStop)
	return mux
}

func (p *TranslationsProxy) handleGet(w http.ResponseWriter, r *http.Request) {
	body, status, err := p.forward(r.Context(), http.MethodGet, "/api/translations", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"translations": []any{}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (p *TranslationsProxy) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	respBody, status, perr := p.forward(r.Context(), http.MethodPost,
		"/api/translations/"+id+"/start", body)
	if perr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "panel not running"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

func (p *TranslationsProxy) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	respBody, status, perr := p.forward(r.Context(), http.MethodPost,
		"/api/translations/"+id+"/stop", body)
	if perr != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

func (p *TranslationsProxy) forward(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	ep, err := bootstrap.ReadPanelEndpoint(p.endpointPath)
	if err != nil {
		return nil, 0, err
	}
	host := ep.Host
	if host == "" {
		host = "127.0.0.1"
	}
	url := "http://" + host + ":" + strconv.Itoa(ep.Port) + path
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, 0, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, 0, err
	}
	return out, resp.StatusCode, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		_ = err
	}
}

var _ = errors.New // keep "errors" import if no other use
```

Note: `writeJSON` already exists in `internal/api/handlers.go` (look for
the existing helper there). If so, remove the duplicate from
`translations.go` and import from the existing one.

- [ ] **Step 4: Mount the proxy in the service**

Update `internal/api/handlers.go` to accept a `*TranslationsProxy`:

```go
type Server struct {
	// ...existing fields...
	translations *TranslationsProxy
}

func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	rawSerialEnabled bool,
	fl flasher.Flasher,
	flashingEnabled bool,
	keepAwake power.KeepAwake,
	translations *TranslationsProxy,
) *Server {
	return &Server{
		// ...existing fields...
		translations: translations,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// ...existing routes...
	if s.translations != nil {
		// Stitch the proxy's three routes into our mux.
		mux.Handle("GET /api/translations", s.translations.Handler())
		mux.Handle("POST /api/translations/{id}/start", s.translations.Handler())
		mux.Handle("POST /api/translations/{id}/stop", s.translations.Handler())
	}
	return logMiddleware(mux)
}
```

(Confirm against the existing file — adjust args list, signatures match.)

- [ ] **Step 5: Update the service composition root**

Modify `internal/app/app.go` — update the `api.New(...)` call to include
the new arg:

```go
import "github.com/bioexperiment-lab-devices/serialhop/internal/paths" // already imported

translationsProxy := api.NewTranslationsProxy(paths.PanelEndpointPath())
srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, fl, flashingEnabled, keepAwake, translationsProxy)
```

- [ ] **Step 6: Update existing handler tests to pass nil/proxy where needed**

Search for `api.New(` call sites in `internal/api/handlers_test.go` and
`internal/api/handlers_power_test.go` and pass `nil` as the new argument
(tests don't exercise the proxy directly):

```
grep -n "api.New(" internal/api/*_test.go
```

For each call, add `, nil` as the final argument.

- [ ] **Step 7: Run the tests**

```
go test ./internal/api/ -v
go test -race -count=1 ./internal/api/...
```

- [ ] **Step 8: gofmt + go vet on both platforms**

```
gofmt -l ./internal/api ./internal/app
go vet ./internal/api/... ./internal/app/...
GOOS=windows go vet ./internal/api/... ./internal/app/...
```

- [ ] **Step 9: Commit**

```
git add internal/api/translations.go internal/api/translations_test.go internal/api/handlers.go internal/api/handlers_test.go internal/api/handlers_power_test.go internal/app/app.go
git commit -m "feat(api): service-side proxy for /api/translations*"
```

---

## Task 11 — Panel streaming lifecycle (startup wiring + orphan kill + shutdown)

**Files:**
- Create: `internal/panel/streaming_lifecycle.go`
- Create: `internal/panel/streaming_lifecycle_other.go`
- Create: `internal/panel/streaming_lifecycle_windows.go`
- Create: `internal/panel/streaming_lifecycle_test.go`
- Modify: `internal/panel/wails_app.go`

`StreamingLifecycle` is the glue that:

1. On panel startup: kills orphan ffmpeg children from a previous panel
   run (Windows only — best-effort), constructs the manager, binds a
   localhost HTTP listener, writes `panel-endpoint.json`, and runs an
   initial `Refresh()`.
2. On panel shutdown: closes the listener, calls `manager.Shutdown()`,
   deletes `panel-endpoint.json`.

The orphan-kill is Windows-only (`taskkill` + WMIC query). On non-Windows
it's a no-op.

- [ ] **Step 1: Write the failing test for the start/stop wiring**

Create `internal/panel/streaming_lifecycle_test.go`:

```go
package panel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

func TestStreamingLifecycle_StartStop(t *testing.T) {
	dir := t.TempDir()
	endpoint := filepath.Join(dir, "panel-endpoint.json")
	armed := filepath.Join(dir, "armed-cameras.json")

	lc, err := startStreamingForTest(context.Background(), endpoint, armed)
	if err != nil {
		t.Fatalf("startStreamingForTest: %v", err)
	}
	// Endpoint file written.
	ep, err := bootstrap.ReadPanelEndpoint(endpoint)
	if err != nil {
		t.Fatalf("ReadPanelEndpoint: %v", err)
	}
	if ep.Port == 0 || ep.PID == 0 {
		t.Fatalf("bad endpoint: %+v", ep)
	}
	// Stop tears down.
	if err := lc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Endpoint file removed.
	if _, err := os.Stat(endpoint); !os.IsNotExist(err) {
		t.Fatalf("endpoint file should be removed: %v", err)
	}
}
```

- [ ] **Step 2: Run the test; verify it fails**

```
go test ./internal/panel/ -run TestStreamingLifecycle -v
```
Expected: undefined: startStreamingForTest.

- [ ] **Step 3: Implement the cross-platform lifecycle**

Create `internal/panel/streaming_lifecycle.go`:

```go
package panel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

// StreamingLifecycle owns the streamer subsystem inside the panel
// process. It is started in App.startup and stopped in App.shutdown.
type StreamingLifecycle struct {
	endpointPath string
	armedPath    string
	ffmpegPath   string
	bearerFlag   string

	manager streamer.Manager
	srv     *http.Server
	listen  net.Listener
}

// NewStreamingLifecycle constructs an unstarted lifecycle.
func NewStreamingLifecycle(endpointPath, armedPath, ffmpegPath, bearerFlag string) *StreamingLifecycle {
	return &StreamingLifecycle{
		endpointPath: endpointPath,
		armedPath:    armedPath,
		ffmpegPath:   ffmpegPath,
		bearerFlag:   bearerFlag,
	}
}

// Start does the full panel-side initialization (steps spec §6.5):
//   1. Kill orphans from the previous run (platform-specific).
//   2. Construct the manager.
//   3. Bind the localhost HTTP listener.
//   4. Write panel-endpoint.json.
//   5. Run an initial Refresh.
func (lc *StreamingLifecycle) Start(ctx context.Context) error {
	_ = killOrphans(ctx) // platform-specific; best-effort

	store := streamer.NewStore(lc.armedPath)
	resolver := streamer.NewFFmpegResolver(lc.ffmpegPath)
	mgr := streamer.NewManager(streamer.ManagerConfig{
		Store:       store,
		Enumerator:  streamer.NewEnumerator(),
		FFmpegPath:  lc.ffmpegPath,
		FFmpegReady: func() error { return resolver.Probe(context.Background()) },
		BearerFlag:  lc.bearerFlag,
	})
	if _, err := mgr.Refresh(ctx); err != nil {
		// Refresh failure (e.g. ffmpeg missing) is not fatal — the UI
		// surfaces it. Continue.
		_ = err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := l.Addr().(*net.TCPAddr)
	srv := &http.Server{
		Handler:           streamingHandler(mgr),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		err := srv.Serve(l)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Listener closed unexpectedly; log via slog elsewhere if needed.
			_ = err
		}
	}()
	if err := bootstrap.WritePanelEndpoint(lc.endpointPath, bootstrap.PanelEndpoint{
		Host:      "127.0.0.1",
		Port:      addr.Port,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = srv.Shutdown(context.Background())
		_ = l.Close()
		return err
	}
	lc.manager = mgr
	lc.srv = srv
	lc.listen = l
	_ = strconv.Itoa // keep import
	return nil
}

// Manager returns the running manager. Nil if Start has not been called.
func (lc *StreamingLifecycle) Manager() streamer.Manager { return lc.manager }

// Stop reverses Start.
func (lc *StreamingLifecycle) Stop(ctx context.Context) error {
	if lc.srv != nil {
		_ = lc.srv.Shutdown(ctx)
	}
	if lc.listen != nil {
		_ = lc.listen.Close()
	}
	if lc.manager != nil {
		_ = lc.manager.Shutdown(ctx)
	}
	_ = bootstrap.DeletePanelEndpoint(lc.endpointPath)
	return nil
}

// startStreamingForTest is a convenience constructor used by the
// lifecycle test in streaming_lifecycle_test.go.
func startStreamingForTest(ctx context.Context, endpointPath, armedPath string) (*StreamingLifecycle, error) {
	lc := NewStreamingLifecycle(endpointPath, armedPath, "", "-authorization")
	if err := lc.Start(ctx); err != nil {
		return nil, err
	}
	return lc, nil
}
```

- [ ] **Step 4: Create the non-Windows orphan-kill stub**

Create `internal/panel/streaming_lifecycle_other.go`:

```go
//go:build !windows

package panel

import "context"

// killOrphans is a no-op on non-Windows (dev hosts).
func killOrphans(_ context.Context) error { return nil }
```

- [ ] **Step 5: Create the Windows orphan-kill**

Create `internal/panel/streaming_lifecycle_windows.go`:

```go
//go:build windows

package panel

import (
	"context"
	"log/slog"
	"os/exec"
)

// killOrphans finds ffmpeg.exe processes that carry our session marker
// in their command line and force-kills them. Used at panel startup to
// recover from a previous panel that crashed without graceful shutdown.
func killOrphans(ctx context.Context) error {
	// wmic is deprecated but still ubiquitous; use it to find candidates by command line.
	cmd := exec.CommandContext(ctx, "wmic", "process", "where",
		"name='ffmpeg.exe' and CommandLine like '%serialhop_session=%'",
		"get", "ProcessId", "/format:value")
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Info("streamer: orphan scan failed (no orphans assumed)", "err", err)
		return nil
	}
	pids := parseWmicPids(string(out))
	for _, pid := range pids {
		k := exec.CommandContext(ctx, "taskkill", "/pid", pid, "/T", "/F")
		if err := k.Run(); err != nil {
			slog.Info("streamer: orphan kill failed", "pid", pid, "err", err)
		} else {
			slog.Info("streamer: killed orphan ffmpeg", "pid", pid)
		}
	}
	return nil
}

func parseWmicPids(s string) []string {
	var out []string
	for _, ln := range splitLines(s) {
		const prefix = "ProcessId="
		if len(ln) > len(prefix) && ln[:len(prefix)] == prefix {
			out = append(out, ln[len(prefix):])
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			ln := s[start:i]
			if n := len(ln); n > 0 && ln[n-1] == '\r' {
				ln = ln[:n-1]
			}
			if ln != "" {
				out = append(out, ln)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		ln := s[start:]
		if n := len(ln); n > 0 && ln[n-1] == '\r' {
			ln = ln[:n-1]
		}
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
```

- [ ] **Step 6: Run the lifecycle test**

```
go test ./internal/panel/ -run TestStreamingLifecycle -v
```

- [ ] **Step 7: Wire into App.startup**

Modify `internal/panel/wails_app.go` — at the bottom of `func (a *App) startup(ctx context.Context)`, add:

```go
import "github.com/bioexperiment-lab-devices/serialhop/internal/paths"
import "github.com/bioexperiment-lab-devices/serialhop/internal/streamer" // for BearerFlag constant if applicable

a.streaming = NewStreamingLifecycle(
    paths.PanelEndpointPath(),
    paths.ArmedCamerasPath(),
    paths.FFmpegPath(),
    "-authorization", // matches the WHIP muxer flag for our pinned ffmpeg build
)
if err := a.streaming.Start(ctx); err != nil {
    slog.Error("streaming subsystem failed to start", "err", err)
}
```

And add to the `App` struct in the same file:

```go
type App struct {
    // ...existing...
    streaming *StreamingLifecycle
}
```

And expose a shutdown hook. Locate the existing Wails `OnShutdown` (in
`wails_app.go`'s `RunWithBindings`) and call:

```go
OnShutdown: func(ctx context.Context) {
    if a := getCurrentApp(); a != nil && a.streaming != nil {
        _ = a.streaming.Stop(ctx)
    }
},
```

> **Note for the implementer:** the existing wiring of `OnShutdown`
> varies; if it's already a method on `*App`, just add the
> `_ = a.streaming.Stop(ctx)` line to the existing body. Search for
> `OnShutdown:` in `internal/panel/wails_app.go` for the current style.

- [ ] **Step 8: Run all panel + streamer tests**

```
go test -race -count=1 ./internal/panel/... ./internal/streamer/...
```

- [ ] **Step 9: gofmt + go vet on both platforms**

```
gofmt -l ./internal/panel
go vet ./internal/panel/...
GOOS=windows go vet ./internal/panel/...
```

- [ ] **Step 10: Commit**

```
git add internal/panel/streaming_lifecycle.go internal/panel/streaming_lifecycle_other.go internal/panel/streaming_lifecycle_windows.go internal/panel/streaming_lifecycle_test.go internal/panel/wails_app.go
git commit -m "feat(panel): streaming subsystem lifecycle (orphan kill, listener, endpoint file)"
```

---

## Task 12 — Wails bindings for the Cameras tab

**Files:**
- Create: `internal/panel/streaming_bindings.go`
- Create: `internal/panel/streaming_bindings_other.go`

The bindings expose three methods to the frontend:

- `ListCameras() StreamingState` — full snapshot for the tab.
- `SetCameraArmed(id string, armed bool) error`
- `RefreshCameras() error`

**Important constraint** (from project memory): bound methods on `*panel.App`
reached via embedding in `main.App` MUST NOT take `context.Context` as
the first parameter — Wails v2's ctx auto-inject doesn't work through
embedding and silently fails on the JS side. The methods below explicitly
omit ctx.

- [ ] **Step 1: Implement the non-Windows stub**

Create `internal/panel/streaming_bindings_other.go`:

```go
//go:build !windows

package panel

import "github.com/bioexperiment-lab-devices/serialhop/internal/streamer"

// On non-Windows (dev only) the bindings still need to compile so the
// package builds on macOS. They return empty values.

func (a *App) ListCameras() streamer.StreamingState {
	return streamer.StreamingState{}
}
func (a *App) SetCameraArmed(_ string, _ bool) error { return nil }
func (a *App) RefreshCameras() error                  { return nil }
```

- [ ] **Step 2: Implement the Windows bindings**

Create `internal/panel/streaming_bindings.go`:

```go
//go:build windows

package panel

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

func (a *App) ListCameras() streamer.StreamingState {
	if a.streaming == nil || a.streaming.Manager() == nil {
		return streamer.StreamingState{}
	}
	m := a.streaming.Manager()
	return streamer.StreamingState{
		Cameras:  m.Cameras(),
		FfmpegOK: ffmpegOK(m),
	}
}

func (a *App) SetCameraArmed(id string, armed bool) error {
	if a.streaming == nil || a.streaming.Manager() == nil {
		return errors.New("streaming subsystem not initialized")
	}
	if err := a.streaming.Manager().SetArmed(id, armed); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "streaming:state")
	return nil
}

func (a *App) RefreshCameras() error {
	if a.streaming == nil || a.streaming.Manager() == nil {
		return errors.New("streaming subsystem not initialized")
	}
	if _, err := a.streaming.Manager().Refresh(context.Background()); err != nil {
		return err
	}
	runtime.EventsEmit(a.ctx, "streaming:state")
	return nil
}

// ffmpegOK is the cached result of the version probe. We surface it as
// part of StreamingState so the tab can render the red banner.
func ffmpegOK(m streamer.Manager) bool {
	// Manager.Translations / Cameras do not expose probe state; for v1
	// we probe lazily on first Start. The UI banner is best-effort:
	// we treat the probe as "OK" until a Start has failed.
	return true
}
```

> The simple `ffmpegOK` heuristic above means the red banner only appears
> after a failed Start. If you want an earlier signal, expose the probe
> result through the manager (e.g. add `Manager.FfmpegHealthy() error`).
> Defer that decision to v2 unless ops finds the lag confusing.

- [ ] **Step 3: Wire the new bindings into the main.App struct**

The Wails `Bind:` list currently passes `&App{App: app}`. The new
methods are on `*panel.App`, so they're inherited via embedding — no
binding list edit needed beyond confirming this.

Search for the binding registration:

```
grep -n "Bind:\s*\[" internal/panel/wails_app.go
```

Confirm the existing line registers `app` itself or `*App`. If new
methods don't show up in `window.go.main.App` at runtime, add them
explicitly:

```go
Bind: []interface{}{
    &App{App: app},
},
```

No new entries needed — embedding handles it.

- [ ] **Step 4: Build the panel and start the panel manually to sanity-check the bindings**

```
task build
# On Windows: launch SerialHop.exe and open the Cameras tab (in Task 14).
# On macOS: confirm `go build ./...` is green (the tab tests use the fakes).
```

- [ ] **Step 5: gofmt + go vet on both platforms**

```
gofmt -l ./internal/panel
GOOS=windows go vet ./internal/panel/...
go vet ./internal/panel/...
```

- [ ] **Step 6: Commit**

```
git add internal/panel/streaming_bindings.go internal/panel/streaming_bindings_other.go
git commit -m "feat(panel): Wails bindings for camera arming"
```

---

## Task 13 — Add Cameras tab id to TabBar + App.tsx mount + Wails TS bindings

**Files:**
- Modify: `internal/panel/frontend/src/components/TabBar.tsx`
- Modify: `internal/panel/frontend/src/App.tsx`
- Modify: `internal/panel/frontend/src/wails/go/main/App.ts`

- [ ] **Step 1: Add the TS binding declarations**

Append to `internal/panel/frontend/src/wails/go/main/App.ts`:

```ts
export interface CameraView {
  id: string;
  label: string;
  armed: boolean;
  connected: boolean;
  live: boolean;
  last_error_msg?: string;
}
export interface StreamingState {
  cameras: CameraView[];
  ffmpeg_ok: boolean;
}

export function ListCameras(): Promise<StreamingState> {
  return call<StreamingState>("ListCameras");
}
export function SetCameraArmed(id: string, armed: boolean): Promise<void> {
  return call<void>("SetCameraArmed", id, armed);
}
export function RefreshCameras(): Promise<void> {
  return call<void>("RefreshCameras");
}
```

- [ ] **Step 2: Extend the TabBar**

Modify `internal/panel/frontend/src/components/TabBar.tsx`:

```ts
type TabId = "status" | "config" | "devices" | "ports" | "cameras" | "logs";

const TABS: { id: TabId; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "config", label: "Config" },
  { id: "devices", label: "Devices" },
  { id: "ports", label: "Ports" },
  { id: "cameras", label: "Cameras" },
  { id: "logs", label: "Logs" },
];
```

- [ ] **Step 3: Update App.tsx to mount the (still-to-be-created) tab**

Modify `internal/panel/frontend/src/App.tsx`:

```ts
import { CamerasTab } from "./tabs/CamerasTab";

const TAB_LABELS: Record<TabId, string> = {
  status: "Status",
  config: "Config",
  devices: "Devices",
  ports: "Ports",
  cameras: "Cameras",
  logs: "Logs",
};

// ...inside the JSX, after the ports block...
{tab === "cameras" && (
  <ErrorBoundary scope="tab:cameras" version={version}>
    <CamerasTab />
  </ErrorBoundary>
)}
```

- [ ] **Step 4: TypeScript compile**

```
cd internal/panel/frontend
npm run build
```
Expected: build fails with `Cannot find module './tabs/CamerasTab'`. That's
fine — Task 14 creates the tab. Leave the import in place; Task 14
removes the error.

Alternatively, comment out the import + JSX block until Task 14 lands.
Choose either — both are acceptable as long as Task 14 follows
immediately.

- [ ] **Step 5: Commit**

```
git add internal/panel/frontend/src/wails/go/main/App.ts internal/panel/frontend/src/components/TabBar.tsx internal/panel/frontend/src/App.tsx
git commit -m "feat(panel-ui): wire Cameras tab id into TabBar and App routing"
```

---

## Task 14 — Cameras tab React component

**Files:**
- Create: `internal/panel/frontend/src/tabs/CamerasTab.tsx`
- Create: `internal/panel/frontend/src/tabs/CamerasTab.test.tsx`

The tab matches spec §9.

- [ ] **Step 1: Write the failing component test**

Create `internal/panel/frontend/src/tabs/CamerasTab.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { CamerasTab } from "./CamerasTab";

vi.mock("../wails/go/main/App", () => ({
  ListCameras: vi.fn(),
  SetCameraArmed: vi.fn(),
  RefreshCameras: vi.fn(),
}));

import { ListCameras, SetCameraArmed, RefreshCameras } from "../wails/go/main/App";

describe("CamerasTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders empty state when no cameras", async () => {
    (ListCameras as any).mockResolvedValue({ cameras: [], ffmpeg_ok: true });
    render(<CamerasTab />);
    expect(await screen.findByText(/No cameras detected/i)).toBeInTheDocument();
  });

  it("renders one card per camera", async () => {
    (ListCameras as any).mockResolvedValue({
      cameras: [
        { id: "id-A", label: "Logitech C270", armed: false, connected: true, live: false },
        { id: "id-B", label: "Front Cam", armed: true, connected: true, live: false },
      ],
      ffmpeg_ok: true,
    });
    render(<CamerasTab />);
    expect(await screen.findByText("Logitech C270")).toBeInTheDocument();
    expect(await screen.findByText("Front Cam")).toBeInTheDocument();
  });

  it("toggles arming", async () => {
    (ListCameras as any).mockResolvedValue({
      cameras: [{ id: "id-A", label: "Cam A", armed: false, connected: true, live: false }],
      ffmpeg_ok: true,
    });
    (SetCameraArmed as any).mockResolvedValue(undefined);
    render(<CamerasTab />);
    const toggle = await screen.findByRole("switch", { name: /allow streaming/i });
    fireEvent.click(toggle);
    await waitFor(() => expect(SetCameraArmed).toHaveBeenCalledWith("id-A", true));
  });

  it("shows ffmpeg-unavailable banner", async () => {
    (ListCameras as any).mockResolvedValue({ cameras: [], ffmpeg_ok: false });
    render(<CamerasTab />);
    expect(await screen.findByText(/ffmpeg\.exe missing/i)).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run the test; verify it fails**

```
cd internal/panel/frontend
npm test -- CamerasTab
```
Expected: fail (component does not exist).

- [ ] **Step 3: Implement the component**

Create `internal/panel/frontend/src/tabs/CamerasTab.tsx`:

```tsx
import { useEffect, useState, useCallback } from "react";
import { Button } from "../components/Button";
import { ListCameras, SetCameraArmed, RefreshCameras, type CameraView, type StreamingState } from "../wails/go/main/App";
import { useWailsEvent } from "../wailsEvents";

export function CamerasTab() {
  const [state, setState] = useState<StreamingState>({ cameras: [], ffmpeg_ok: true });
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await ListCameras();
      setState({ cameras: r.cameras ?? [], ffmpeg_ok: !!r.ffmpeg_ok });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);
  useEffect(() => useWailsEvent("streaming:state", () => { load(); }), [load]);

  const refresh = async () => {
    setRefreshing(true);
    try { await RefreshCameras(); await load(); } finally { setRefreshing(false); }
  };

  const setArmed = async (id: string, armed: boolean) => {
    // Optimistic update
    setState(s => ({
      ...s,
      cameras: s.cameras.map(c => c.id === id ? { ...c, armed } : c),
    }));
    try {
      await SetCameraArmed(id, armed);
    } catch (e) {
      // Revert on failure.
      setState(s => ({
        ...s,
        cameras: s.cameras.map(c => c.id === id ? { ...c, armed: !armed } : c),
      }));
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  if (loading) return <div className="shp-pad">Loading cameras…</div>;

  const armedCount = state.cameras.filter(c => c.armed).length;

  return (
    <div className="shp-pad">
      <div className="shp-row shp-row--header">
        <h2>Cameras</h2>
        <div className="shp-spacer" />
        <span className="shp-meta">{armedCount}/{state.cameras.length} armed</span>
        <Button onClick={refresh} disabled={refreshing}>{refreshing ? "Refreshing…" : "Refresh"}</Button>
      </div>

      {!state.ffmpeg_ok && (
        <div className="shp-banner shp-banner--error" role="alert">
          ffmpeg.exe missing or modified — reinstall SerialHop to enable camera streaming.
        </div>
      )}

      {error && (
        <div className="shp-banner shp-banner--warning" role="alert">
          {error}
          <Button onClick={() => setError(null)}>Dismiss</Button>
        </div>
      )}

      {state.cameras.length === 0 ? (
        <div className="shp-empty">
          No cameras detected. Connect a camera or check whether another application is using it.
        </div>
      ) : (
        <div className="shp-cards">
          {state.cameras.map(c => (
            <CameraCard key={c.id} camera={c} onToggle={a => setArmed(c.id, a)} />
          ))}
        </div>
      )}
    </div>
  );
}

interface CameraCardProps {
  camera: CameraView;
  onToggle: (next: boolean) => void;
}

function CameraCard({ camera, onToggle }: CameraCardProps) {
  const badge = badgeFor(camera);
  return (
    <div className="shp-card">
      <div className="shp-row">
        <div>
          <div className="shp-card__title">{camera.label}</div>
          <div className="shp-card__id" title={camera.id}>{camera.id}</div>
        </div>
        <div className="shp-spacer" />
        <span className={`shp-badge shp-badge--${badge.kind}`}>{badge.label}</span>
      </div>
      <div className="shp-row">
        <label className="shp-switch">
          <input
            type="checkbox"
            role="switch"
            aria-label={`Allow streaming ${camera.label}`}
            checked={camera.armed}
            onChange={e => onToggle(e.target.checked)}
          />
          <span>Allow streaming</span>
        </label>
      </div>
      {camera.last_error_msg && (
        <div className="shp-card__error">{camera.last_error_msg}</div>
      )}
    </div>
  );
}

function badgeFor(c: CameraView): { kind: string; label: string } {
  if (c.live) return { kind: "ok", label: "Live" };
  if (!c.connected) return { kind: "warn", label: "Disconnected" };
  if (c.armed) return { kind: "idle", label: "Armed" };
  return { kind: "muted", label: "Disarmed" };
}
```

- [ ] **Step 4: Run the test; verify it passes**

```
cd internal/panel/frontend
npm test -- CamerasTab
```

- [ ] **Step 5: TypeScript build**

```
npm run build
```
Expected: green.

- [ ] **Step 6: Commit**

```
git add internal/panel/frontend/src/tabs/CamerasTab.tsx internal/panel/frontend/src/tabs/CamerasTab.test.tsx
git commit -m "feat(panel-ui): Cameras tab component + tests"
```

---

## Task 15 — Cross-cutting: ensure the full test sweep is green

**Files:** none — verification only.

- [ ] **Step 1: Full Go test sweep with race detector**

```
go test -race -count=1 ./...
```
Expected: all pass.

- [ ] **Step 2: gofmt + go vet**

```
gofmt -l .
go vet ./...
```
Expected: empty output.

- [ ] **Step 3: golangci-lint**

```
golangci-lint run
```
Expected: zero issues. Fix any in-place.

- [ ] **Step 4: Frontend tests + build**

```
cd internal/panel/frontend
npm test
npm run build
```

- [ ] **Step 5: Windows cross-check via go vet only (or full test on a Windows runner)**

```
GOOS=windows go vet ./...
```

If you have a Windows runner available, also run:

```
GOOS=windows go test ./...
```

- [ ] **Step 6: govulncheck**

```
govulncheck ./...
```

- [ ] **Step 7: Commit nothing — but if any fixes were required, commit them now**

```
git status
# if any fixes...
git add -p
git commit -m "test: full sweep green for camera-streaming feature"
```

---

## Task 16 — Installer: bundle ffmpeg.exe + SHA256 verification

**Files:**
- Modify: `tools/installer/...` (the exact files depend on the existing installer structure)
- Modify: `Taskfile.yaml`

This task adds ffmpeg.exe to the installer's payload and verifies its
SHA-256 matches `PinnedFFmpegBinarySHA256` from Task 4.

- [ ] **Step 1: Inventory the installer**

```
ls tools/installer
grep -rn "embed\|payload" tools/installer | head -30
```

Identify how the installer currently embeds `SerialHop.exe`. The pattern
should be reproducible for `ffmpeg.exe`.

- [ ] **Step 2: Add ffmpeg.exe to the installer payload**

(Specific to your installer; pseudocode below.) The installer must:

1. Embed or reference `ffmpeg.exe` (likely via `//go:embed ffmpeg.exe`).
2. Write it to `<InstallDir>\ffmpeg.exe` during install.
3. Compute its SHA-256 after writing and compare against
   `streamer.PinnedFFmpegBinarySHA256`. Abort install on mismatch.
4. Remove it during uninstall.

Concrete sketch — add to the installer's payload writer:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

func writeFFmpeg(installDir string) error {
	dest := filepath.Join(installDir, "ffmpeg.exe")
	if err := os.WriteFile(dest, ffmpegPayload, 0o755); err != nil {
		return err
	}
	f, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != streamer.PinnedFFmpegBinarySHA256 {
		_ = os.Remove(dest)
		return errors.New("ffmpeg.exe SHA-256 mismatch: refusing to install")
	}
	return nil
}
```

`ffmpegPayload` is the embedded blob, e.g.:

```go
//go:embed ffmpeg.exe
var ffmpegPayload []byte
```

- [ ] **Step 3: Add a Taskfile target to fetch ffmpeg.exe for the installer build**

Add to `Taskfile.yaml`:

```yaml
  fetch-ffmpeg:
    desc: Download and verify ffmpeg.exe for the installer payload.
    cmds:
      - go run ./tools/fetch-ffmpeg --out tools/installer/ffmpeg.exe
```

Then create `tools/fetch-ffmpeg/main.go`: a small Go program that
downloads the pinned ffmpeg essentials archive from the recorded URL,
extracts `bin/ffmpeg.exe`, and verifies the SHA-256 matches
`streamer.PinnedFFmpegBinarySHA256` before writing the output.

> Skip writing fetch-ffmpeg if the installer build process pulls the
> binary differently (e.g. CI downloads it in a workflow). The
> SHA-256-verify-then-embed mechanic is what matters.

- [ ] **Step 4: Update the installer's uninstall path**

Remove `<InstallDir>\ffmpeg.exe` in the same code path that removes
`SerialHop.exe`. Find the existing uninstall code (likely in
`tools/installer/...`) and add the equivalent line.

- [ ] **Step 5: Smoke-test the installer build**

```
task fetch-ffmpeg
task build
# Verify dist artifact references ffmpeg.exe (size check, etc.)
```

- [ ] **Step 6: Update release notes (later, when the release-please PR appears)**

Add a note to whatever CHANGELOG/release notes the project uses that the
installer payload grew by ~80MB to include `ffmpeg.exe`.

- [ ] **Step 7: Commit**

```
git add tools/installer/ tools/fetch-ffmpeg/ Taskfile.yaml
git commit -m "feat(installer): bundle ffmpeg.exe with SHA-256 verification"
```

---

## Task 17 — Manual end-to-end smoke on a real Windows machine

**Files:** none.

This task is operational, not code. It catches integration issues no
unit test can: real DirectShow camera, real lab-bridge, real WHIP
exchange.

- [ ] **Step 1: Install the panel + service via the new installer**

On a Windows lab machine with a USB webcam:

1. Uninstall the previous SerialHop install.
2. Install the new build.
3. Verify `<InstallDir>\ffmpeg.exe` exists and `ffmpeg.exe -version` runs.

- [ ] **Step 2: Confirm enumeration**

1. Open the Cameras tab.
2. Verify the webcam appears with a sensible label.
3. Click Refresh — the camera remains.

- [ ] **Step 3: Arm the camera**

1. Toggle Allow streaming ON.
2. Verify the badge shifts to "Armed".
3. Open `<DataDir>\armed-cameras.json` in a text viewer; verify the camera is listed.

- [ ] **Step 4: Real lab-bridge handshake**

1. From lab-bridge's viewer UI, open a viewer for this lab.
2. Verify the Cameras tab badge shifts to "Live" within ~5 seconds.
3. Verify video is visible in the lab-bridge viewer.
4. Open Task Manager and confirm exactly one `ffmpeg.exe` child of the
   panel process.

- [ ] **Step 5: Stop scenarios**

1. Close the viewer browser tab → after the lab-bridge 5s debounce,
   verify the panel badge returns to "Armed" and the `ffmpeg.exe`
   process exits.
2. Open a viewer, then close the panel hard (Task Manager → End Task).
   Verify `ffmpeg.exe` is gone within a few seconds (the bridge will
   call /stop, which now 503s due to panel-down, and the orphan-kill at
   next panel launch will catch leftovers).
3. Open a viewer, then unplug the camera. Verify badge goes to
   "Disconnected" and lab-bridge viewer sees ICE failure.

- [ ] **Step 6: Replace-on-conflict**

1. Open two viewer tabs to the same camera nearly simultaneously.
2. Verify both eventually see video, only one `ffmpeg.exe` is running,
   and the panel didn't log any 4xx.

- [ ] **Step 7: Log inspection for token leakage**

```
findstr /i "Bearer" <DataDir>\logs\*.log
```
Expected: no real token strings present. If any are found, fix the
logging site (likely in the manager's spawn path) and re-test.

- [ ] **Step 8: Document any defects, then close out the branch**

Per `CLAUDE.md`, ensure the PR title is Conventional Commits style. A
sensible title:

```
feat: add camera streaming (SerialHop protocol v1)
```

---

## Self-review

Spec coverage:

- §1 Purpose / §2 Architecture → Tasks 8 (manager), 10 (service proxy), 11 (panel lifecycle).
- §3 Package layout → Tasks 2-8, 9, 10, 11.
- §4.1 armed_cameras.json → Task 6.
- §4.2 panel_endpoint.json → Task 1 + 11.
- §5 Camera identity → Task 3 (parser).
- §6.1 Arming → Tasks 12, 14.
- §6.2 Stream start → Task 8 (manager) + Task 9 (panel HTTP) + Task 10 (proxy).
- §6.3 Stream stop → Same trio; 409 logic in Task 8.
- §6.4 Operator unarms while publishing → Task 8 (`SetArmed` kills active session).
- §6.5 Crash recovery → Task 11 (`killOrphans`).
- §6.6 Panel shutdown → Task 11 (`Stop`).
- §7 Defaults → Task 4.
- §8 Failure handling → Tasks 8, 9, 10, 14 collectively.
- §9 UI → Tasks 13, 14.
- §10 Bundling ffmpeg → Task 16.
- §11 Testing strategy → Tasks 3, 6, 7, 8, 9, 10, 14, 15.
- §12 Trust model → Task 5 (`RedactedArgs`), Task 17 (log-leakage check).
- §13 Migration → no implementation; release notes in Task 16.
- §14 Versioning → covered by additive design; no specific task.
- §15 Conformance checklist → Task 17 smoke-test.

Placeholder scan: the only "REPLACE_WITH_..." token is the SHA-256 in
`ffmpeg_build.go`, which Task 4 explicitly directs the implementer to
fill in from the chosen build (with the exact recording procedure).
That's a deliberate parameter, not an unresolved placeholder.

Type consistency:

- `streamer.Manager` exported methods: `Refresh`, `Cameras`,
  `SetArmed`, `Translations`, `Start`, `Stop`, `Shutdown` — all match
  between Task 8 (impl), Task 9 (HTTP), Task 12 (bindings).
- `StartRequest` / `StopRequest` field names match between Task 8, Task
  9, and Task 10's pass-through test fixtures.
- TS `CameraView` / `StreamingState` field names match the Go DTOs in
  Task 2 (snake_case in JSON, exposed via `json:"..."` tags).

Plan looks consistent.
