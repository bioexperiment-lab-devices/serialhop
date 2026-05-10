# Raw Serial Port Endpoints Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /serial/ports` and `POST /serial/ports/{port}/command` to the REST API, gated behind a new `raw_serial.enabled` config flag (default `false`), so an operator can enumerate serial ports and send raw bytes to ports without a classified device — for diagnosing unknown hardware.

**Architecture:** Pure addition. New routes registered on the existing `http.ServeMux`. Per-handler check on `raw_serial.enabled` returns 403 when off. Listing reuses `serial.Opener.List()` and annotates each port with discovery state from the registry. Sending opens the port fresh per request, drains, writes, optionally reads via the existing `serial.ReadFrame`, closes. No new dependencies; no changes to discovery, chisel, or service plumbing.

**Tech Stack:** Go 1.22+ stdlib `net/http` (path-pattern routing), `gopkg.in/yaml.v3` (config), existing `internal/serial`/`internal/registry`/`internal/api` packages, `lxn/walk` for the panel label.

**Spec:** `docs/superpowers/specs/2026-05-10-raw-serial-endpoints-design.md`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/config/config.go` | Modify | Add `RawSerialConfig` struct, field on `Config`, `Default()` value, scaffold section. |
| `internal/config/config_test.go` | Modify | Default-value coverage; scaffold round-trip preserves new section. |
| `internal/config/load_test.go` | Modify | Parse `raw_serial.enabled: true`. |
| `internal/registry/registry.go` | Modify | Add `IsDiscovering()` and `HasPort(name)` helpers. |
| `internal/registry/registry_test.go` | Modify | Tests for the two new helpers. |
| `internal/api/types.go` | Modify | Add `PortDTO`, `PortsResponse`. |
| `internal/api/server.go` | Modify | `New()` gains `opener`, `rawSerialEnabled`; register two new routes. |
| `internal/api/handlers.go` | Modify | `Server` struct gains `opener` and `rawSerialEnabled` fields (move from receiver into struct literal in `New`). |
| `internal/api/handlers_test.go` | Modify | Update `newTestServer` and the one inline `New(...)` call site to pass new params. |
| `internal/api/raw_serial.go` | Create | `handleGetSerialPorts`, `handlePostSerialCommand`, executeRaw helper. |
| `internal/api/raw_serial_test.go` | Create | Full coverage per spec §7. |
| `internal/app/app.go` | Modify | Pass `opener` and `cfg.RawSerial.Enabled` into `api.New(...)`. |
| `internal/panel/panel.go` | Modify | Add `Raw serial:       enabled`/`disabled` label. |
| `README.md` | Modify | Add the two endpoints to the REST API table. |

---

## Pre-flight

Run from repo root before starting:

```bash
go test -count=1 ./...
gofmt -l .
go vet ./...
golangci-lint run
```

Expected: tests pass, no formatter or lint output. This is the clean baseline.

---

## Task 1: Add `raw_serial.enabled` config field

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/load_test.go`

- [ ] **Step 1.1: Write failing test for `Default().RawSerial.Enabled == false`**

Append this test to `internal/config/config_test.go` after `TestDefaultConfig`:

```go
func TestDefaultConfig_RawSerialDisabled(t *testing.T) {
	c := Default()
	if c.RawSerial.Enabled {
		t.Errorf("raw_serial.enabled: got true, want false (must default off)")
	}
}
```

- [ ] **Step 1.2: Run — should fail**

```bash
go test ./internal/config/ -run TestDefaultConfig_RawSerialDisabled -count=1 -v
```

Expected: compile error — `c.RawSerial undefined (type Config has no field or method RawSerial)`.

- [ ] **Step 1.3: Add the struct + field + default**

Edit `internal/config/config.go`:

- After the `LogConfig` type declaration block, add:

```go
type RawSerialConfig struct {
	Enabled bool `yaml:"enabled"`
}
```

- In `type Config struct`, after the `Log` field, add:

```go
	RawSerial RawSerialConfig `yaml:"raw_serial"`
```

- In `Default()`, after the `Log` field, add:

```go
		RawSerial: RawSerialConfig{Enabled: false},
```

- [ ] **Step 1.4: Run — should pass**

```bash
go test ./internal/config/ -run TestDefaultConfig_RawSerialDisabled -count=1 -v
```

Expected: PASS.

- [ ] **Step 1.5: Write failing test for parsing `raw_serial.enabled: true`**

Append to `internal/config/load_test.go`:

```go
func TestLoad_RawSerialEnabled(t *testing.T) {
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9000
rest:
  port: 0
log:
  level: "info"
raw_serial:
  enabled: true
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.RawSerial.Enabled {
		t.Errorf("raw_serial.enabled: got false, want true")
	}
}
```

- [ ] **Step 1.6: Run — should pass**

```bash
go test ./internal/config/ -run TestLoad_RawSerialEnabled -count=1 -v
```

Expected: PASS (struct already supports it from Step 1.3).

- [ ] **Step 1.7: Update scaffold to include the new section**

Edit the `scaffoldTemplate` constant in `internal/config/config.go`. After the `log:` block and before the closing backtick, append:

```
raw_serial:
  enabled: false                  # set true to allow GET /serial/ports and
                                  # POST /serial/ports/{port}/command. bypasses
                                  # device classification — leave off unless diagnosing.
```

The complete updated template tail looks like:

```go
log:
  level: "info"                   # debug | info | warn | error

raw_serial:
  enabled: false                  # set true to allow GET /serial/ports and
                                  # POST /serial/ports/{port}/command. bypasses
                                  # device classification — leave off unless diagnosing.
`
```

- [ ] **Step 1.8: Add scaffold round-trip assertion for the new section**

Edit `TestWriteScaffold_RoundTrip` in `internal/config/config_test.go`. After the existing `parsed.Chisel.RemotePort` assertion, append:

```go
	if parsed.RawSerial.Enabled {
		t.Errorf("round-trip raw_serial.enabled: got true, want false (default)")
	}
```

- [ ] **Step 1.9: Run all config tests**

```bash
go test ./internal/config/ -count=1
```

Expected: all PASS.

- [ ] **Step 1.10: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/load_test.go
git commit -m "feat(config): add raw_serial.enabled field, default off"
```

---

## Task 2: Registry — `IsDiscovering` and `HasPort` helpers

**Files:**
- Modify: `internal/registry/registry.go`
- Modify: `internal/registry/registry_test.go`

- [ ] **Step 2.1: Write failing test for `IsDiscovering`**

Append to `internal/registry/registry_test.go`:

```go
func TestRegistry_IsDiscovering(t *testing.T) {
	r := New()
	if r.IsDiscovering() {
		t.Errorf("IsDiscovering(): got true on fresh registry, want false")
	}
	if !r.LockDiscovery() {
		t.Fatal("LockDiscovery: setup failed")
	}
	if !r.IsDiscovering() {
		t.Errorf("IsDiscovering(): got false while locked, want true")
	}
	r.UnlockDiscovery()
	if r.IsDiscovering() {
		t.Errorf("IsDiscovering(): got true after Unlock, want false")
	}
}
```

- [ ] **Step 2.2: Run — should fail**

```bash
go test ./internal/registry/ -run TestRegistry_IsDiscovering -count=1 -v
```

Expected: compile error — `r.IsDiscovering undefined`.

- [ ] **Step 2.3: Implement `IsDiscovering`**

Append to `internal/registry/registry.go`, near the existing `LockDiscovery` / `UnlockDiscovery` methods:

```go
// IsDiscovering reports whether a discovery is currently in progress.
// Non-acquiring read; callers must NOT use it as a lock.
func (r *Registry) IsDiscovering() bool {
	return r.discoverGate.Load()
}
```

- [ ] **Step 2.4: Run — should pass**

```bash
go test ./internal/registry/ -run TestRegistry_IsDiscovering -count=1 -v
```

Expected: PASS.

- [ ] **Step 2.5: Write failing test for `HasPort`**

Append to `internal/registry/registry_test.go`:

```go
func TestRegistry_HasPort(t *testing.T) {
	r := New()

	if id, ok := r.HasPort("COM3"); ok {
		t.Errorf("HasPort(COM3): got (%q, true) on empty registry, want (\"\", false)", id)
	}

	r.Replace([]*Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3"},
		{ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM4"},
	})

	id, ok := r.HasPort("COM3")
	if !ok || id != "pump_1" {
		t.Errorf("HasPort(COM3): got (%q, %v), want (\"pump_1\", true)", id, ok)
	}
	id, ok = r.HasPort("COM99")
	if ok || id != "" {
		t.Errorf("HasPort(COM99): got (%q, %v), want (\"\", false)", id, ok)
	}
}
```

- [ ] **Step 2.6: Run — should fail**

```bash
go test ./internal/registry/ -run TestRegistry_HasPort -count=1 -v
```

Expected: compile error — `r.HasPort undefined`.

- [ ] **Step 2.7: Implement `HasPort`**

Append to `internal/registry/registry.go`:

```go
// HasPort reports whether any device in the registry currently uses the named
// serial port. If a match exists, returns its device ID and true; otherwise
// "", false. Linear scan — registry size is bounded by the number of attached
// devices (typically <10).
func (r *Registry) HasPort(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, d := range r.devices {
		if d.Port == name {
			return d.ID, true
		}
	}
	return "", false
}
```

- [ ] **Step 2.8: Run all registry tests**

```bash
go test ./internal/registry/ -count=1
```

Expected: all PASS.

- [ ] **Step 2.9: Commit**

```bash
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat(registry): add IsDiscovering and HasPort helpers"
```

---

## Task 3: Wire `Opener` and `rawSerialEnabled` through `api.Server`

This task does not add behavior — it only extends `api.New`'s signature so subsequent tasks can use it. Existing tests continue to pass.

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/handlers_test.go`
- Modify: `internal/app/app.go`

- [ ] **Step 3.1: Extend the `Server` struct + `New()` signature**

Edit `internal/api/handlers.go`. Replace:

```go
type Server struct {
	reg      *registry.Registry
	discover DiscoverFn
}

func New(reg *registry.Registry, discover DiscoverFn) *Server {
	return &Server{reg: reg, discover: discover}
}
```

with:

```go
type Server struct {
	reg              *registry.Registry
	discover         DiscoverFn
	opener           labserial.Opener
	rawSerialEnabled bool
}

func New(reg *registry.Registry, discover DiscoverFn, opener labserial.Opener, rawSerialEnabled bool) *Server {
	return &Server{
		reg:              reg,
		discover:         discover,
		opener:           opener,
		rawSerialEnabled: rawSerialEnabled,
	}
}
```

(`labserial` is already aliased as the import for `internal/serial` in `handlers.go`.)

- [ ] **Step 3.2: Update existing test helpers and call sites in `handlers_test.go`**

In `internal/api/handlers_test.go`:

- Replace `newTestServer` with:

```go
func newTestServer(t *testing.T, reg *registry.Registry, disc DiscoverFn) http.Handler {
	t.Helper()
	if disc == nil {
		disc = fakeDiscoverFn(nil, nil)
	}
	return New(reg, disc, serial.NewFakeOpener(), false).Handler()
}
```

- In `TestPostDiscover_ClosesOldPortsBeforeProbing`, replace:

```go
	srv := New(reg, discoverFn).Handler()
```

with:

```go
	srv := New(reg, discoverFn, serial.NewFakeOpener(), false).Handler()
```

- [ ] **Step 3.3: Update `app.go` call site**

In `internal/app/app.go`, replace:

```go
	srv := api.New(reg, discoverFn)
```

with:

```go
	srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled)
```

- [ ] **Step 3.4: Run — full suite must still pass**

```bash
go test -race -count=1 ./...
```

Expected: all PASS. No new behavior, but the wiring change is exercised by the existing handler tests.

- [ ] **Step 3.5: Commit**

```bash
git add internal/api/handlers.go internal/api/handlers_test.go internal/app/app.go
git commit -m "refactor(api): thread opener and raw_serial_enabled into Server"
```

---

## Task 4: `GET /serial/ports` endpoint

**Files:**
- Modify: `internal/api/types.go`
- Modify: `internal/api/server.go`
- Create: `internal/api/raw_serial.go`
- Create: `internal/api/raw_serial_test.go`

- [ ] **Step 4.1: Add response types**

Append to `internal/api/types.go`:

```go
type PortDTO struct {
	Name       string `json:"name"`
	Discovered bool   `json:"discovered"`
	DeviceID   string `json:"device_id,omitempty"`
}

type PortsResponse struct {
	Ports []PortDTO `json:"ports"`
}
```

- [ ] **Step 4.2: Write the disabled-flag test**

Create `internal/api/raw_serial_test.go`. Start with a minimal import set; later steps add `encoding/json`, `sort`, and `io` as their tests need them.

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// rawSrv builds an api.Server.Handler() with the given registry, opener, and
// raw_serial.enabled flag. Used by every test in this file.
func rawSrv(t *testing.T, reg *registry.Registry, opener serial.Opener, enabled bool) http.Handler {
	t.Helper()
	return New(reg, fakeDiscoverFn(nil, nil), opener, enabled).Handler()
}

func TestGetSerialPorts_DisabledReturns403(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	srv := rawSrv(t, reg, opener, false)

	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "raw serial disabled") {
		t.Errorf("body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 4.3: Run — should fail at compile (no route yet)**

```bash
go test ./internal/api/ -run TestGetSerialPorts_DisabledReturns403 -count=1 -v
```

Expected: test compiles but FAILS — the unregistered route returns 405 (method not allowed) or a similar non-403 status. (If you instead see a `404 page not found`, that's also a fail — we want 403.)

- [ ] **Step 4.4: Implement the listing handler skeleton**

Create `internal/api/raw_serial.go`:

```go
package api

import (
	"log/slog"
	"net/http"
	"sort"
)

func (s *Server) handleGetSerialPorts(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		slog.Debug("raw_serial_disabled", "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	names, err := s.opener.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	sort.Strings(names)
	out := make([]PortDTO, 0, len(names))
	for _, n := range names {
		dto := PortDTO{Name: n}
		if id, ok := s.reg.HasPort(n); ok {
			dto.Discovered = true
			dto.DeviceID = id
		}
		out = append(out, dto)
	}
	slog.Info("raw_serial_list", "count", len(out))
	writeJSON(w, http.StatusOK, PortsResponse{Ports: out})
}
```

Then register the route. Routes are registered in `Handler()` in `internal/api/handlers.go`. Add the new route line:

```go
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", s.handleGetDevices)
	mux.HandleFunc("POST /discover", s.handlePostDiscover)
	mux.HandleFunc("POST /devices/{id}/command", s.handlePostCommand)
	mux.HandleFunc("GET /serial/ports", s.handleGetSerialPorts)
	return mux
}
```

- [ ] **Step 4.5: Run — should pass**

```bash
go test ./internal/api/ -run TestGetSerialPorts_DisabledReturns403 -count=1 -v
```

Expected: PASS.

- [ ] **Step 4.6: Add empty-registry test**

This test uses `encoding/json` and `sort`. Add them to the import block in `internal/api/raw_serial_test.go`:

```go
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)
```

Then append:

```go
func TestGetSerialPorts_EmptyRegistry(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	opener.Add(serial.NewFakePort("COM5"))
	srv := rawSrv(t, reg, opener, true)

	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp PortsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Ports) != 2 {
		t.Fatalf("ports: got %d, want 2 (%v)", len(resp.Ports), resp.Ports)
	}
	if !sort.SliceIsSorted(resp.Ports, func(i, j int) bool { return resp.Ports[i].Name < resp.Ports[j].Name }) {
		t.Errorf("ports not sorted by name: %v", resp.Ports)
	}
	for _, p := range resp.Ports {
		if p.Discovered || p.DeviceID != "" {
			t.Errorf("port %q: got discovered=%v device_id=%q, want discovered=false device_id=\"\"", p.Name, p.Discovered, p.DeviceID)
		}
	}
}
```

- [ ] **Step 4.7: Run — should pass**

```bash
go test ./internal/api/ -run TestGetSerialPorts_EmptyRegistry -count=1 -v
```

Expected: PASS.

- [ ] **Step 4.8: Add discovered-annotation test**

Append to `internal/api/raw_serial_test.go`:

```go
func TestGetSerialPorts_AnnotatesDiscoveredDevices(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	opener.Add(serial.NewFakePort("COM5"))
	opener.Add(serial.NewFakePort("COM7"))

	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3"), Opener: opener},
		{ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM7", Conn: serial.NewFakePort("COM7"), Opener: opener},
	})

	srv := rawSrv(t, reg, opener, true)
	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp PortsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]struct {
		discovered bool
		id         string
	}{
		"COM3": {true, "pump_1"},
		"COM5": {false, ""},
		"COM7": {true, "valve_1"},
	}
	for _, p := range resp.Ports {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected port %q in response", p.Name)
			continue
		}
		if p.Discovered != w.discovered || p.DeviceID != w.id {
			t.Errorf("port %q: got discovered=%v id=%q, want discovered=%v id=%q",
				p.Name, p.Discovered, p.DeviceID, w.discovered, w.id)
		}
	}
}
```

- [ ] **Step 4.9: Run — should pass**

```bash
go test ./internal/api/ -run TestGetSerialPorts -count=1 -v
```

Expected: 3 PASS.

- [ ] **Step 4.10: Run full suite to confirm nothing regressed**

```bash
go test -race -count=1 ./...
```

Expected: all PASS.

- [ ] **Step 4.11: Commit**

```bash
git add internal/api/types.go internal/api/handlers.go internal/api/raw_serial.go internal/api/raw_serial_test.go
git commit -m "feat(api): add GET /serial/ports endpoint"
```

---

## Task 5: `POST /serial/ports/{port}/command` — gating and validation

This task implements the handler's pre-I/O gating: the four early-return branches (403, 404, 409 port-discovered, 409 discovery-in-progress) and the param/body validation paths (400). I/O paths (open/write/read) come in Task 6.

**Files:**
- Modify: `internal/api/handlers.go` (route registration only)
- Modify: `internal/api/raw_serial.go`
- Modify: `internal/api/raw_serial_test.go`

- [ ] **Step 5.1: Register the new route**

In `internal/api/handlers.go`, edit `Handler()`:

```go
	mux.HandleFunc("GET /serial/ports", s.handleGetSerialPorts)
	mux.HandleFunc("POST /serial/ports/{port}/command", s.handlePostSerialCommand)
```

- [ ] **Step 5.2: Write the disabled-flag test**

Append to `internal/api/raw_serial_test.go`:

```go
func postRaw(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestPostSerialCommand_DisabledReturns403(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, false)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
}
```

- [ ] **Step 5.3: Run — should fail (handler not yet defined)**

```bash
go test ./internal/api/ -run TestPostSerialCommand_DisabledReturns403 -count=1 -v
```

Expected: compile error — `s.handlePostSerialCommand undefined`.

- [ ] **Step 5.4: Add handler skeleton**

Append to `internal/api/raw_serial.go`:

```go
func (s *Server) handlePostSerialCommand(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		slog.Debug("raw_serial_disabled", "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	// remaining branches added in subsequent steps.
	writeError(w, http.StatusNotImplemented, "not implemented", "")
}
```

- [ ] **Step 5.5: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_DisabledReturns403 -count=1 -v
```

Expected: PASS.

- [ ] **Step 5.6: Test — port not in `Opener.List()` → 404**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_PortNotFound(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM99/command", `{"command":[1]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port not found") {
		t.Errorf("body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 5.7: Run — should fail (still 501)**

```bash
go test ./internal/api/ -run TestPostSerialCommand_PortNotFound -count=1 -v
```

Expected: FAIL with "got 501, want 404".

- [ ] **Step 5.8: Implement port-existence check**

Replace the placeholder in `handlePostSerialCommand` so the handler now reads:

```go
func (s *Server) handlePostSerialCommand(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		slog.Debug("raw_serial_disabled", "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	port := r.PathValue("port")

	names, err := s.opener.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	found := false
	for _, n := range names {
		if n == port {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "port not found", port)
		return
	}

	if id, ok := s.reg.HasPort(port); ok {
		writeError(w, http.StatusConflict, "port has discovered device",
			"use /devices/"+id+"/command instead")
		return
	}

	if s.reg.IsDiscovering() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}

	// validation + I/O follow in later steps.
	writeError(w, http.StatusNotImplemented, "not implemented", "")
}
```

- [ ] **Step 5.9: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_PortNotFound -count=1 -v
```

Expected: PASS.

- [ ] **Step 5.10: Test — port has discovered device → 409**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_PortHasDiscoveredDevice(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3"), Opener: opener},
	})
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port has discovered device") {
		t.Errorf("body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/devices/pump_1/command") {
		t.Errorf("body should suggest /devices/pump_1/command, got: %s", rec.Body.String())
	}
}
```

- [ ] **Step 5.11: Run — should pass (already implemented in 5.8)**

```bash
go test ./internal/api/ -run TestPostSerialCommand_PortHasDiscoveredDevice -count=1 -v
```

Expected: PASS.

- [ ] **Step 5.12: Test — discovery in progress → 409**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_DiscoveryInProgress(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	if !reg.LockDiscovery() {
		t.Fatal("setup: LockDiscovery should succeed")
	}
	defer reg.UnlockDiscovery()
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "discovery in progress") {
		t.Errorf("body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 5.13: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_DiscoveryInProgress -count=1 -v
```

Expected: PASS.

- [ ] **Step 5.14: Test — bad query param / oversize body / bad byte / unknown field → 400**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_BadQueryParam(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?timeout_ms=99999999", `{"command":[1]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostSerialCommand_BadByte(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[300,1,2]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostSerialCommand_UnknownField(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1,2,3],"hidden":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostSerialCommand_BodyTooLarge(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	body := strings.Builder{}
	body.WriteString(`{"command":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString("1")
	}
	body.WriteString("]}")

	rec := postRaw(t, srv, "/serial/ports/COM3/command", body.String())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 5.15: Run — should fail (still 501)**

```bash
go test ./internal/api/ -run TestPostSerialCommand_Bad -count=1 -v
go test ./internal/api/ -run TestPostSerialCommand_UnknownField -count=1 -v
go test ./internal/api/ -run TestPostSerialCommand_BodyTooLarge -count=1 -v
```

Expected: 4 FAIL with "got 501, want 400".

- [ ] **Step 5.16: Implement param/body validation**

Replace the trailing placeholder block in `handlePostSerialCommand` (the `writeError(... 501 ...)` line) with:

```go
	params, err := parseCmdParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	cmd, err := parseCommandBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	// I/O follows in Task 6.
	_ = params
	_ = cmd
	writeError(w, http.StatusNotImplemented, "not implemented", "")
```

- [ ] **Step 5.17: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_Bad -count=1 -v
go test ./internal/api/ -run TestPostSerialCommand_UnknownField -count=1 -v
go test ./internal/api/ -run TestPostSerialCommand_BodyTooLarge -count=1 -v
```

Expected: all PASS.

- [ ] **Step 5.18: Run full api suite — earlier paths must still work**

```bash
go test ./internal/api/ -count=1
```

Expected: all PASS.

- [ ] **Step 5.19: Commit**

```bash
git add internal/api/handlers.go internal/api/raw_serial.go internal/api/raw_serial_test.go
git commit -m "feat(api): POST /serial/ports/{port}/command gating and validation"
```

---

## Task 6: `POST /serial/ports/{port}/command` — I/O paths

This task replaces the trailing 501 with the actual open / drain / write / [read] / close logic.

**Files:**
- Modify: `internal/api/raw_serial.go`
- Modify: `internal/api/raw_serial_test.go`

- [ ] **Step 6.1: Test — happy path with reply**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_HappyPath(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{99, 88, 77})
	opener.Add(fp)
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []int{99, 88, 77}
	if len(resp.Response) != len(want) {
		t.Fatalf("response: got %v, want %v", resp.Response, want)
	}
	for i := range want {
		if resp.Response[i] != want[i] {
			t.Errorf("response[%d]: got %d, want %d", i, resp.Response[i], want[i])
		}
	}
	written := fp.Written()
	wantWritten := []byte{1, 2, 3, 4, 0}
	if string(written) != string(wantWritten) {
		t.Errorf("written: got %v, want %v", written, wantWritten)
	}
}
```

- [ ] **Step 6.2: Run — should fail (still 501)**

```bash
go test ./internal/api/ -run TestPostSerialCommand_HappyPath -count=1 -v
```

Expected: FAIL with "got 501".

- [ ] **Step 6.3: Implement I/O**

In `internal/api/raw_serial.go`:

- Replace the import block with:

```go
import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)
```

- Replace the trailing block of `handlePostSerialCommand` (everything from `params, err := parseCmdParams(r)` onwards) with:

```go
	params, err := parseCmdParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	cmd, err := parseCommandBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	start := time.Now()
	logOutcome := func(outcome string, resp []byte) {
		slog.Info("raw_serial_command",
			"port", port,
			"cmd_bytes", len(cmd),
			"resp_bytes", len(resp),
			"duration_ms", time.Since(start).Milliseconds(),
			"outcome", outcome,
		)
		slog.Debug("raw_serial_command bytes",
			"port", port,
			"cmd", bytesToInts(cmd),
			"resp", bytesToInts(resp),
		)
	}

	conn, err := s.opener.Open(port)
	if err != nil {
		logOutcome("open_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "port open failed", err.Error())
		return
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Drain(discovery.DrainDuration); err != nil {
		logOutcome("drain_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "port drain failed", err.Error())
		return
	}
	if _, err := conn.Write(cmd); err != nil {
		logOutcome("write_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "port write failed", err.Error())
		return
	}
	if !params.waitForReply {
		logOutcome("ok", nil)
		writeJSON(w, http.StatusOK, CommandResponse{Response: bytesToInts(nil)})
		return
	}
	resp, err := labserial.ReadFrame(conn, params.timeout, params.interByte, params.expectedN)
	if err != nil {
		logOutcome("read_failed", resp)
		writeError(w, http.StatusServiceUnavailable, "port read failed", fmt.Sprintf("read: %v", err))
		return
	}
	logOutcome("ok", resp)
	writeJSON(w, http.StatusOK, CommandResponse{Response: bytesToInts(resp)})
```

(`port` is the local variable bound earlier from `r.PathValue("port")`.)

- [ ] **Step 6.4: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_HappyPath -count=1 -v
```

Expected: PASS.

- [ ] **Step 6.5: Test — no reply within timeout**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_NoReply(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?timeout_ms=20", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
}
```

- [ ] **Step 6.6: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_NoReply -count=1 -v
```

Expected: PASS.

- [ ] **Step 6.7: Test — `wait_for_response=false`**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_WaitForResponseFalse(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{99, 88}) // would be returned, but caller opts out
	opener.Add(fp)
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?wait_for_response=false", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
	if string(fp.Written()) != string([]byte{1, 2, 3}) {
		t.Errorf("written: got %v, want [1 2 3]", fp.Written())
	}
}
```

- [ ] **Step 6.8: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_WaitForResponseFalse -count=1 -v
```

Expected: PASS.

- [ ] **Step 6.9: Test — `expected_response_bytes` early stop**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_ExpectedBytesStopsEarly(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{1, 2, 3, 4, 99, 99, 99}) // more than 4 — must stop at 4
	opener.Add(fp)
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?expected_response_bytes=4", `{"command":[1]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Response) != 4 {
		t.Errorf("response: got %v, want 4 bytes", resp.Response)
	}
}
```

- [ ] **Step 6.10: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_ExpectedBytesStopsEarly -count=1 -v
```

Expected: PASS.

- [ ] **Step 6.11: Test — write fails (port already closed) → 503**

Append to `internal/api/raw_serial_test.go`:

```go
func TestPostSerialCommand_WriteFails(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	opener.Add(fp)
	// FakeOpener.Open resets closed=false on each call, so a port closed
	// AFTER it's first opened by the handler is what we want to simulate.
	// Achieve that with a stub opener that wraps FakeOpener and returns the
	// already-closed port without resetting the flag.
	srv := rawSrv(t, reg, &alreadyClosedOpener{inner: opener, target: "COM3", port: fp}, true)
	_ = fp.Close()

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port write failed") &&
		!strings.Contains(rec.Body.String(), "port drain failed") {
		t.Errorf("body should mention drain or write failure, got: %s", rec.Body.String())
	}
}

// alreadyClosedOpener returns a pre-closed port for `target`, bypassing
// FakeOpener.Open's auto-reopen behavior, so the handler observes I/O errors.
type alreadyClosedOpener struct {
	inner  *serial.FakeOpener
	target string
	port   *serial.FakePort
}

func (o *alreadyClosedOpener) List() ([]string, error) { return o.inner.List() }
func (o *alreadyClosedOpener) Open(name string) (serial.Port, error) {
	if name == o.target {
		return o.port, nil
	}
	return o.inner.Open(name)
}
```

- [ ] **Step 6.12: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_WriteFails -count=1 -v
```

Expected: PASS. (Drain on a closed FakePort returns `ErrClosed` first, so the body says "port drain failed" — both messages are accepted by the test.)

- [ ] **Step 6.13: Test — open fails → 503**

This test uses `io.ErrUnexpectedEOF`. Add `"io"` to the import block in `internal/api/raw_serial_test.go`:

```go
import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)
```

Then append:

```go
// listOnlyOpener wraps FakeOpener and adds names that List returns but Open
// rejects. Used to simulate the OS-level race where a port disappears between
// enumeration and Open().
type listOnlyOpener struct {
	*serial.FakeOpener
	listOnly map[string]error
}

func (o *listOnlyOpener) List() ([]string, error) {
	base, err := o.FakeOpener.List()
	if err != nil {
		return nil, err
	}
	for n := range o.listOnly {
		base = append(base, n)
	}
	return base, nil
}

func (o *listOnlyOpener) Open(name string) (serial.Port, error) {
	if err, ok := o.listOnly[name]; ok {
		return nil, err
	}
	return o.FakeOpener.Open(name)
}

func TestPostSerialCommand_OpenFails(t *testing.T) {
	reg := registry.New()
	opener := &listOnlyOpener{
		FakeOpener: serial.NewFakeOpener(),
		listOnly:   map[string]error{"COM3": io.ErrUnexpectedEOF},
	}
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "port open failed") {
		t.Errorf("body: %s", rec.Body.String())
	}
}
```

- [ ] **Step 6.14: Run — should pass**

```bash
go test ./internal/api/ -run TestPostSerialCommand_OpenFails -count=1 -v
```

Expected: PASS.

- [ ] **Step 6.15: Run full suite with race detector**

```bash
go test -race -count=1 ./...
```

Expected: all PASS, no races.

- [ ] **Step 6.16: Commit**

```bash
git add internal/api/raw_serial.go internal/api/raw_serial_test.go
git commit -m "feat(api): POST /serial/ports/{port}/command I/O implementation"
```

---

## Task 7: Panel label and README

**Files:**
- Modify: `internal/panel/panel.go`
- Modify: `README.md`

- [ ] **Step 7.1: Add panel label assignment**

In `internal/panel/panel.go`:

- Add a new label var alongside the existing ones (`serverLbl`, `remotePort`, `restPort`, `discoveryLbl`, `logLevel`). Insert after `logLevel`:

```go
		rawSerialLbl *walk.Label
```

- In the `refresh` closure, after the `logLevel.SetText(...)` line, append:

```go
		if cfg.RawSerial.Enabled {
			rawSerialLbl.SetText("Raw serial:       enabled")
		} else {
			rawSerialLbl.SetText("Raw serial:       disabled")
		}
```

- In the `MainWindow{...}` `Children:` slice, after the line `Label{AssignTo: &logLevel},` add:

```go
				Label{AssignTo: &rawSerialLbl},
```

- [ ] **Step 7.2: Verify the package still builds (Windows-only file; macOS host can only `go vet` it via cross-build flag)**

The panel file is `//go:build windows`. On a non-Windows host, run:

```bash
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: build succeeds. (No automated test — the panel file is GUI-only and covered manually on the Windows lab machine.)

- [ ] **Step 7.3: Update README**

In `README.md`, find the REST API table:

```markdown
| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes; optionally read a reply |
```

Replace it with:

```markdown
| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes to a discovered device; optionally read a reply |
| `GET`  | `/serial/ports` | List enumerated serial ports, annotated with discovery state (gated by `raw_serial.enabled`) |
| `POST` | `/serial/ports/{port}/command` | Send raw bytes to a port without a discovered device (gated by `raw_serial.enabled`) |
```

- [ ] **Step 7.4: Commit**

```bash
git add internal/panel/panel.go README.md
git commit -m "feat(panel): show raw_serial flag; docs(readme): list new endpoints"
```

---

## Task 8: Final verification

**Files:** none modified.

- [ ] **Step 8.1: Format check**

```bash
gofmt -l .
```

Expected: no output.

If output appears: `gofmt -w <listed files>`, re-run, then `git add -p` + commit.

- [ ] **Step 8.2: Vet**

```bash
go vet ./...
```

Expected: no output.

- [ ] **Step 8.3: Lint**

```bash
golangci-lint run
```

Expected: no output.

- [ ] **Step 8.4: Race-tested test run**

```bash
go test -race -count=1 ./...
```

Expected: all PASS.

- [ ] **Step 8.5: Cross-compile for Windows**

```bash
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: success, no output.

- [ ] **Step 8.6: Vulnerability scan**

```bash
govulncheck ./...
```

Expected: no findings affecting this code.

- [ ] **Step 8.7: Manual smoke (skip if no live hardware)**

If a Windows lab machine is available:

1. `task build` → drop the `.exe` next to a `SerialHop_config.yaml` with `raw_serial.enabled: true`.
2. Run `--foreground`. Hit `GET http://127.0.0.1:<rest_port>/serial/ports` — should list COM ports with `discovered: false` for unconnected ones.
3. With `raw_serial.enabled: false`, both endpoints return 403.

This is not gating on the PR but is recommended before merging.

- [ ] **Step 8.8: Confirm clean working tree**

```bash
git status
git log --oneline -10
```

Expected: clean tree, six task commits + the spec commit visible.

---

## Definition of done

- All eight tasks ticked.
- `go test -race -count=1 ./...` green.
- `gofmt -l .` empty, `go vet ./...` and `golangci-lint run` clean, `govulncheck ./...` clean.
- `GOOS=windows GOARCH=amd64 go build ./...` succeeds.
- New endpoints behave per spec; old endpoints unchanged; default config keeps the feature off.
- One PR titled `feat(api): raw serial port endpoints` (release-please will minor-bump on next release).
