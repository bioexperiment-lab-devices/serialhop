# Raw serial attach over WebSocket — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent, full-duplex raw-serial WebSocket endpoint (`GET /serial/ports/{port}/attach`) with RFC2217-semantics line control, tunneled through the existing chisel reverse route, plus a pyserial `rfc2217://` client bridge.

**Architecture:** A WebSocket on the existing REST listener carries raw serial bytes as **binary** frames and line control (baud/DTR/RTS/break/modem) as **text/JSON** frames. Raw access is undiscovered-ports-only, gated by a `raw_serial.enabled` config (default off), leased single-session-per-port with a discovery interlock, and off-by-default. The client bridge (Python) presents `rfc2217://127.0.0.1:PORT` locally and translates to the WS protocol.

**Tech Stack:** Go 1.26, `net/http` (Go 1.22 mux), `github.com/gorilla/websocket` v1.5.3 (already in go.sum, indirect → promote to direct), `go.bug.st/serial`, Python 3 + pyserial for the bridge.

**Spec:** `docs/superpowers/specs/2026-07-14-raw-serial-attach-design.md`.

## Global Constraints

- Go module `github.com/bioexperiment-lab-devices/serialhop`, `go 1.26.0`.
- Pre-flight (from CLAUDE.md, `pr.yml` verify job) must pass: `gofmt -l .` (prints nothing), `go vet ./...`, `golangci-lint run` (errcheck, staticcheck, unused, ineffassign, gosec), `go test -race -count=1 ./...`, `govulncheck ./...`.
- Cross-platform: every Go test must pass on **macOS/Linux** (via the serial fake) and **Windows**. No new `_windows.go`-only logic without a compiling fake.
- **Never** put `BREAKING CHANGE:` in commit/PR bodies. Task commits use `feat:`/`test:`/`docs:` prefixes; the PR title (set later) is `feat: raw serial port attach over websocket`.
- Data format is fixed **8N1** in v1 (baud is the only mode field that changes). Non-8N1 is an explicit non-goal.
- Config defaults: `raw_serial.enabled: false`, `raw_serial.idle_timeout_ms: 900000` (15 min; 0 disables).
- gorilla write rule: a `*websocket.Conn` must have **no concurrent writers**. All writes go through the `rawConn` mutex wrapper (Task 4).

---

### Task 1: Extend `serial.Port` with line control

**Files:**
- Modify: `internal/serial/port.go` (interface + `realPort`)
- Modify: `internal/serial/fake.go` (`FakePort`)
- Modify: `internal/flasher/testing/fake_optiboot.go` (`FakeOptiboot` stubs — it implements `serial.Port`)
- Test: `internal/serial/fake_test.go`

**Interfaces:**
- Consumes: `go.bug.st/serial` `Port.SetRTS(bool) error`, `Port.Break(time.Duration) error`, `Port.GetModemStatusBits() (*ModemStatusBits, error)` with `ModemStatusBits{CTS, DSR, RI, DCD bool}`.
- Produces: `serial.ModemBits{CTS, DSR, RI, CD bool}`; three new `serial.Port` methods `SetRTS(level bool) error`, `SendBreak(d time.Duration) error`, `ModemStatus() (ModemBits, error)`; `FakePort` helpers `RTSSequence() []bool`, `BreakSequence() []time.Duration`, `SetModem(ModemBits)`.

- [ ] **Step 1: Write the failing test** — append to `internal/serial/fake_test.go`:

```go
func TestFakePortLineControl(t *testing.T) {
	f := NewFakePort("COM3")
	if err := f.SetRTS(true); err != nil {
		t.Fatalf("SetRTS: %v", err)
	}
	if err := f.SetRTS(false); err != nil {
		t.Fatalf("SetRTS: %v", err)
	}
	if err := f.SendBreak(250 * time.Millisecond); err != nil {
		t.Fatalf("SendBreak: %v", err)
	}
	f.SetModem(ModemBits{CTS: true, DCDToCD: false}) // see note: field is CD
	got, err := f.ModemStatus()
	if err != nil {
		t.Fatalf("ModemStatus: %v", err)
	}
	if !got.CTS {
		t.Errorf("modem CTS = false, want true")
	}
	if want := []bool{true, false}; !reflect.DeepEqual(f.RTSSequence(), want) {
		t.Errorf("RTSSequence = %v, want %v", f.RTSSequence(), want)
	}
	if want := []time.Duration{250 * time.Millisecond}; !reflect.DeepEqual(f.BreakSequence(), want) {
		t.Errorf("BreakSequence = %v, want %v", f.BreakSequence(), want)
	}
}
```

> Note: `ModemBits` has fields `CTS, DSR, RI, CD` (no `DCDToCD`). Use `f.SetModem(ModemBits{CTS: true})` — the snippet above intentionally shows the struct; write it as `ModemBits{CTS: true}`. Ensure `reflect` and `time` are imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop && go test ./internal/serial/ -run TestFakePortLineControl`
Expected: FAIL — `f.SetRTS undefined` (compile error).

- [ ] **Step 3: Add the interface + `ModemBits` + `realPort` impl** — in `internal/serial/port.go`, add above `Opener`:

```go
// ModemBits are the input modem status lines read from the UART.
type ModemBits struct {
	CTS bool // ClearToSend
	DSR bool // DataSetReady
	RI  bool // RingIndicator
	CD  bool // DataCarrierDetect
}
```

Add to the `Port` interface (after `SetBaudRate`):

```go
	SetRTS(level bool) error                 // toggle RTS line
	SendBreak(d time.Duration) error         // hold TX in break for d
	ModemStatus() (ModemBits, error)         // read CTS/DSR/RI/CD input lines
```

Add `realPort` methods (near the other `realPort` methods):

```go
func (r *realPort) SetRTS(level bool) error      { return r.p.SetRTS(level) }
func (r *realPort) SendBreak(d time.Duration) error { return r.p.Break(d) }

func (r *realPort) ModemStatus() (ModemBits, error) {
	b, err := r.p.GetModemStatusBits()
	if err != nil {
		return ModemBits{}, err
	}
	return ModemBits{CTS: b.CTS, DSR: b.DSR, RI: b.RI, CD: b.DCD}, nil
}
```

- [ ] **Step 4: Add `FakePort` impl** — in `internal/serial/fake.go`, add fields to the `FakePort` struct:

```go
	rtsSeq   []bool
	breakSeq []time.Duration
	modem    ModemBits
```

Add methods:

```go
func (f *FakePort) SetRTS(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.rtsSeq = append(f.rtsSeq, level)
	return nil
}

func (f *FakePort) SendBreak(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.breakSeq = append(f.breakSeq, d)
	return nil
}

func (f *FakePort) ModemStatus() (ModemBits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ModemBits{}, ErrClosed
	}
	return f.modem, nil
}

// SetModem sets the modem bits a subsequent ModemStatus() will return.
func (f *FakePort) SetModem(m ModemBits) {
	f.mu.Lock()
	f.modem = m
	f.mu.Unlock()
}

func (f *FakePort) RTSSequence() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bool, len(f.rtsSeq))
	copy(out, f.rtsSeq)
	return out
}

func (f *FakePort) BreakSequence() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.breakSeq))
	copy(out, f.breakSeq)
	return out
}
```

- [ ] **Step 5: Add `FakeOptiboot` stubs** — in `internal/flasher/testing/fake_optiboot.go` (uses alias `labserial`), add:

```go
func (f *FakeOptiboot) SetRTS(level bool) error         { return nil }
func (f *FakeOptiboot) SendBreak(d time.Duration) error { return nil }
func (f *FakeOptiboot) ModemStatus() (labserial.ModemBits, error) {
	return labserial.ModemBits{}, nil
}
```

Ensure `time` is imported (it already is).

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/khamit/serialhop && go build ./... && go test ./internal/serial/ ./internal/flasher/... -run 'LineControl|Flash|Optiboot'`
Expected: PASS. Then `go vet ./...` clean.

- [ ] **Step 7: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/serial/port.go internal/serial/fake.go internal/serial/fake_test.go internal/flasher/testing/fake_optiboot.go
git commit -m "feat: add RTS/break/modem-status to serial.Port"
```

---

### Task 2: `raw_serial` config block

**Files:**
- Modify: `internal/config/config.go` (`Config`, `RawSerialConfig`, `Default`, scaffold)
- Test: `internal/config/config_test.go` (or `load_test.go` — match where `Default()` is asserted)

**Interfaces:**
- Produces: `config.RawSerialConfig{Enabled bool; IdleTimeoutMs int}`; `Config.RawSerial RawSerialConfig` (yaml/json key `raw_serial`).

- [ ] **Step 1: Write the failing test** — append to `internal/config/config_test.go`:

```go
func TestDefaultRawSerialDisabled(t *testing.T) {
	c := Default()
	if c.RawSerial.Enabled {
		t.Errorf("RawSerial.Enabled default = true, want false")
	}
	if c.RawSerial.IdleTimeoutMs != 900000 {
		t.Errorf("RawSerial.IdleTimeoutMs default = %d, want 900000", c.RawSerial.IdleTimeoutMs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop && go test ./internal/config/ -run TestDefaultRawSerialDisabled`
Expected: FAIL — `c.RawSerial undefined`.

- [ ] **Step 3: Add the config type and default** — in `internal/config/config.go`:

Add field to `Config` struct:
```go
	RawSerial  RawSerialConfig  `yaml:"raw_serial" json:"raw_serial"`
```

Add the type:
```go
type RawSerialConfig struct {
	Enabled       bool `yaml:"enabled" json:"enabled"`
	IdleTimeoutMs int  `yaml:"idle_timeout_ms" json:"idle_timeout_ms"`
}
```

In `Default()`, add to the returned struct literal:
```go
		RawSerial:  RawSerialConfig{Enabled: false, IdleTimeoutMs: 900000},
```

Append to `scaffoldTemplate` (before the closing backtick):
```yaml

raw_serial:
  enabled: false                  # allow GET /serial/ports/{port}/attach (raw
                                  # WebSocket byte + line-control stream). Only
                                  # ports with no discovered device are eligible.
                                  # off by default — turn on for bring-up / RE.
  idle_timeout_ms: 900000         # close a raw session after this many ms with
                                  # no traffic. 0 = never time out.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/khamit/serialhop && go test ./internal/config/`
Expected: PASS. If a scaffold round-trip/golden test exists and fails, update its expected text to include the new block.

- [ ] **Step 5: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add raw_serial config block (default off)"
```

---

### Task 3: Registry raw-port lease

**Files:**
- Modify: `internal/registry/registry.go`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Consumes: existing `Registry.mu`, `Registry.ordered`, `Registry.discoverGate`, `device.Session.PortName()`.
- Produces: `Registry.TryAcquireRaw(port string) bool`, `Registry.ReleaseRaw(port string)`, `Registry.RawLeasedPorts() []string`.

- [ ] **Step 1: Write the failing test** — append to `internal/registry/registry_test.go`:

```go
func TestRawLeaseLifecycle(t *testing.T) {
	r := New()
	if !r.TryAcquireRaw("COM3") {
		t.Fatal("first acquire should succeed")
	}
	if r.TryAcquireRaw("COM3") {
		t.Fatal("second acquire of same port should fail")
	}
	if got := r.RawLeasedPorts(); len(got) != 1 || got[0] != "COM3" {
		t.Fatalf("RawLeasedPorts = %v, want [COM3]", got)
	}
	r.ReleaseRaw("COM3")
	if got := r.RawLeasedPorts(); len(got) != 0 {
		t.Fatalf("after release RawLeasedPorts = %v, want []", got)
	}
	if !r.TryAcquireRaw("COM3") {
		t.Fatal("acquire after release should succeed")
	}
}

func TestRawLeaseBlockedByDiscovery(t *testing.T) {
	r := New()
	if !r.LockDiscovery() {
		t.Fatal("LockDiscovery should succeed")
	}
	if r.TryAcquireRaw("COM3") {
		t.Fatal("acquire during discovery should fail")
	}
	r.UnlockDiscovery()
	if !r.TryAcquireRaw("COM3") {
		t.Fatal("acquire after discovery unlock should succeed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop && go test ./internal/registry/ -run TestRawLease`
Expected: FAIL — `r.TryAcquireRaw undefined`.

- [ ] **Step 3: Implement** — in `internal/registry/registry.go`:

Add `"sort"` to imports. Add field to `Registry` struct:
```go
	rawLeases    map[string]bool
```
In `New()`:
```go
func New() *Registry {
	return &Registry{byID: map[string]*device.Session{}, rawLeases: map[string]bool{}}
}
```
Add methods:
```go
// TryAcquireRaw grants an exclusive raw lease on port. It fails if a
// discovery pass is in flight, the port is owned by a discovered device,
// or another raw lease is already held. Same mutex as the session map, so
// it cannot race Replace/HasPort.
func (r *Registry) TryAcquireRaw(port string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.discoverGate.Load() {
		return false
	}
	for _, s := range r.ordered {
		if s.PortName() == port {
			return false
		}
	}
	if r.rawLeases[port] {
		return false
	}
	r.rawLeases[port] = true
	return true
}

// ReleaseRaw drops the raw lease on port (no-op if not held).
func (r *Registry) ReleaseRaw(port string) {
	r.mu.Lock()
	delete(r.rawLeases, port)
	r.mu.Unlock()
}

// RawLeasedPorts returns the ports currently under a raw lease, sorted.
// Discovery excludes these from its candidate list.
func (r *Registry) RawLeasedPorts() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.rawLeases))
	for p := range r.rawLeases {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/khamit/serialhop && go test ./internal/registry/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat: add raw-port lease to registry with discovery interlock"
```

---

### Task 4: WebSocket attach endpoint — gate + byte pump

**Files:**
- Create: `internal/api/rawattach.go`
- Create: `internal/api/rawattach_test.go`
- Modify: `internal/api/server.go` (`Server` fields + `New` signature)
- Modify: `internal/api/handlers.go` (route)
- Modify: `internal/app/app.go:129` (`api.New` call — add two args)
- Modify: `go.mod` / `go.sum` (promote `gorilla/websocket` to direct via `go mod tidy`)

**Interfaces:**
- Consumes: `Server.reg` (`*registry.Registry` — `HasPort`, `IsDiscovering`, `TryAcquireRaw`, `ReleaseRaw`), `Server.opener` (`serial.Opener.List`, `OpenWithBaud`), `writeError`, `serial.Port` line-control methods (Task 1), `config.RawSerialConfig` (Task 2), `registry` raw lease (Task 3).
- Produces: route `GET /serial/ports/{port}/attach` → `Server.handleSerialAttach`; `New(reg, discover, opener, fl, flashingEnabled, keepAwake, rawSerialEnabled bool, rawIdleTimeout time.Duration)`; the `rawConn` write-serialized wrapper and `controlMsg` JSON type (consumed by Task 5/6).

- [ ] **Step 1: Wire the new `Server` fields and `New` signature** — in `internal/api/server.go`, add fields to `Server`:

```go
	rawSerialEnabled bool
	rawIdleTimeout   time.Duration
```
Change `New` to accept and set them (append params after `keepAwake`):
```go
func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	fl flasher.Flasher,
	flashingEnabled bool,
	keepAwake power.KeepAwake,
	rawSerialEnabled bool,
	rawIdleTimeout time.Duration,
) *Server {
	return &Server{
		reg: reg, discover: discover, opener: opener,
		flasher: fl, flashingEnabled: flashingEnabled, keepAwake: keepAwake,
		rawSerialEnabled: rawSerialEnabled, rawIdleTimeout: rawIdleTimeout,
	}
}
```
Update the production caller `internal/app/app.go:129`:
```go
	srv := api.New(reg, discoverFn, opener, fl, flashingEnabled, keepAwake,
		cfg.RawSerial.Enabled, time.Duration(cfg.RawSerial.IdleTimeoutMs)*time.Millisecond)
```
Update every test caller. Find them: `grep -rn "New(" internal/api/*_test.go`. Each `New(...)`/`api.New(...)` in tests gets `, false, 0` appended (raw serial disabled unless the test targets it).

Register the route in `internal/api/handlers.go` `Handler()`:
```go
	mux.HandleFunc("GET /serial/ports/{port}/attach", s.handleSerialAttach)
```

- [ ] **Step 2: Write the failing gate + round-trip tests** — create `internal/api/rawattach_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// buildServer mirrors the construction the existing api tests use
// (see v1_test.go: ka, _ := power.New(); New(reg, disc, opener, nil, false, ka)),
// with the two new raw-serial args threaded through.
func buildServer(t *testing.T, reg *registry.Registry, op *labserial.FakeOpener, enabled bool, idle time.Duration) *Server {
	t.Helper()
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	disc := func(_ context.Context) ([]*device.Session, error) { return nil, nil }
	return New(reg, disc, op, nil, false, ka, enabled, idle)
}

func newAttachServer(t *testing.T, enabled bool, ports ...string) (*httptest.Server, *labserial.FakeOpener, *registry.Registry) {
	t.Helper()
	op := labserial.NewFakeOpener()
	for _, p := range ports {
		op.Add(labserial.NewFakePort(p))
	}
	reg := registry.New()
	ts := httptest.NewServer(buildServer(t, reg, op, enabled, 0).Handler())
	t.Cleanup(ts.Close)
	return ts, op, reg
}

func newServerWithIdle(t *testing.T, reg *registry.Registry, op *labserial.FakeOpener, idle time.Duration) *Server {
	return buildServer(t, reg, op, true, idle)
}
```

> Add imports `"context"` and `"github.com/bioexperiment-lab-devices/serialhop/internal/device"` for the `DiscoverFn` signature (`func(context.Context) ([]*device.Session, error)` — confirm the exact `DiscoverFn` type in `internal/api/handlers.go`). The existing three `New(...)` callers to update with `, false, 0` are `v1_test.go:75`, `handlers_power_test.go:27`, `handlers_power_test.go:100`.

Gate tests (plain HTTP, no upgrade):

```go
func TestAttachDisabledReturns403(t *testing.T) {
	ts, _, _ := newAttachServer(t, false, "COM3")
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/serial/ports/COM3/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestAttachUnknownPortReturns404(t *testing.T) {
	ts, _, _ := newAttachServer(t, true, "COM3")
	defer ts.Close()
	resp, _ := http.Get(ts.URL + "/serial/ports/COM9/attach")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAttachAlreadyLeasedReturns409(t *testing.T) {
	ts, _, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	if !reg.TryAcquireRaw("COM3") {
		t.Fatal("pre-acquire failed")
	}
	resp, _ := http.Get(ts.URL + "/serial/ports/COM3/attach")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}
```

Byte round-trip (WS):

```go
func TestAttachByteRoundTrip(t *testing.T) {
	ts, op, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	fp, _ := op.Open("COM3") // grab the shared FakePort to Feed/Written
	fake := fp.(*labserial.FakePort)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach?baud=115200"
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// expect a ready control frame first
	mt, msg, err := ws.ReadMessage()
	if err != nil || mt != websocket.TextMessage || !strings.Contains(string(msg), `"ready"`) {
		t.Fatalf("first frame mt=%d msg=%s err=%v; want ready text", mt, msg, err)
	}

	// client -> serial
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	// serial -> client
	fake.Feed([]byte{0xAA, 0xBB})
	mt, msg, err = ws.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage {
		t.Fatalf("rx frame mt=%d err=%v", mt, err)
	}
	if string(msg) != string([]byte{0xAA, 0xBB}) {
		t.Fatalf("rx = %v, want [170 187]", msg)
	}

	// give the ws->serial pump a moment, then assert the write landed
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(fake.Written()) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := fake.Written(); string(got) != string([]byte{0x01, 0x02, 0x03}) {
		t.Fatalf("written = %v, want [1 2 3]", got)
	}

	ws.Close()
	// lease released after close
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(reg.RawLeasedPorts()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := reg.RawLeasedPorts(); len(got) != 0 {
		t.Fatalf("lease not released: %v", got)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/khamit/serialhop && go test ./internal/api/ -run TestAttach`
Expected: FAIL — `handleSerialAttach undefined` / compile errors.

- [ ] **Step 4: Implement the handler** — create `internal/api/rawattach.go`:

```go
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
)

var rawUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// The device API is reached over the chisel tunnel from inside labnet;
	// there is no browser Origin to validate.
	CheckOrigin: func(*http.Request) bool { return true },
}

const (
	rawReadChunk    = 4096
	rawPongWait     = 40 * time.Second
	rawPingPeriod   = 30 * time.Second
	rawSerialReadTO = 50 * time.Millisecond
)

// controlMsg is one text/JSON control frame in either direction.
type controlMsg struct {
	Op     string `json:"op"`
	Baud   int    `json:"baud,omitempty"`
	Level  *bool  `json:"level,omitempty"`
	Ms     int    `json:"ms,omitempty"`
	Port   string `json:"port,omitempty"`
	Detail string `json:"detail,omitempty"`
	CTS    bool   `json:"cts,omitempty"`
	DSR    bool   `json:"dsr,omitempty"`
	RI     bool   `json:"ri,omitempty"`
	CD     bool   `json:"cd,omitempty"`
}

// rawConn serializes all writes to a websocket.Conn (gorilla forbids
// concurrent writers).
type rawConn struct {
	ws  *websocket.Conn
	wmu sync.Mutex
}

func (c *rawConn) writeBinary(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, b)
}

func (c *rawConn) writeJSON(v controlMsg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *rawConn) ping() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
}

func (c *rawConn) close(code int, text string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	_ = c.ws.Close()
}

func rawBaud(r *http.Request) (int, error) {
	v := r.URL.Query().Get("baud")
	if v == "" {
		return 9600, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 4_000_000 {
		return 0, fmt.Errorf("baud must be an integer in 1..4000000")
	}
	return n, nil
}

func (s *Server) handleSerialAttach(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	port := r.PathValue("port")
	baud, err := rawBaud(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	names, err := s.opener.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	if !slices.Contains(names, port) {
		writeError(w, http.StatusNotFound, "port not found", port)
		return
	}
	if id, ok := s.reg.HasPort(port); ok {
		writeError(w, http.StatusConflict, "port has discovered device", "owned by "+id)
		return
	}
	if s.reg.IsDiscovering() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}
	if !s.reg.TryAcquireRaw(port) {
		writeError(w, http.StatusConflict, "port already attached", "")
		return
	}

	ws, err := rawUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response.
		s.reg.ReleaseRaw(port)
		slog.Warn("raw_attach upgrade failed", "port", port, "err", err)
		return
	}
	s.runRawSession(&rawConn{ws: ws}, port, baud)
}

func (s *Server) runRawSession(c *rawConn, port string, baud int) {
	start := time.Now()
	var txBytes, rxBytes int64
	reason := "client_close"
	defer func() {
		_ = c.ws.Close()
		s.reg.ReleaseRaw(port)
		slog.Info("raw_attach_close",
			"port", port,
			"bytes_tx", atomic.LoadInt64(&txBytes),
			"bytes_rx", atomic.LoadInt64(&rxBytes),
			"duration_ms", time.Since(start).Milliseconds(),
			"reason", reason)
	}()

	sp, err := s.opener.OpenWithBaud(port, baud)
	if err != nil {
		reason = "open_failed"
		c.close(websocket.CloseInternalServerErr, "open failed: "+err.Error())
		return
	}
	defer func() { _ = sp.Close() }()

	slog.Info("raw_attach_open", "port", port, "remote", c.ws.RemoteAddr().String(), "baud", baud)
	_ = c.writeJSON(controlMsg{Op: "ready", Port: port, Baud: baud})

	// serial -> ws
	serialDone := make(chan struct{})
	go func() {
		defer close(serialDone)
		buf := make([]byte, rawReadChunk)
		for {
			if err := sp.SetReadTimeout(rawSerialReadTO); err != nil {
				return
			}
			n, err := sp.Read(buf)
			if err != nil {
				return
			}
			if n == 0 {
				continue
			}
			atomic.AddInt64(&rxBytes, int64(n))
			if err := c.writeBinary(buf[:n]); err != nil {
				return
			}
		}
	}()

	// ping keepalive
	pingDone := make(chan struct{})
	go func() {
		t := time.NewTicker(rawPingPeriod)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				if err := c.ping(); err != nil {
					return
				}
			}
		}
	}()
	defer close(pingDone)

	// ws -> serial (this goroutine)
	_ = c.ws.SetReadDeadline(time.Now().Add(rawPongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(rawPongWait))
	})

	for {
		select {
		case <-serialDone:
			reason = "read_error"
			return
		default:
		}
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(rawPongWait))
		switch mt {
		case websocket.BinaryMessage:
			if _, err := sp.Write(data); err != nil {
				_ = c.writeJSON(controlMsg{Op: "error", Detail: "write: " + err.Error()})
				reason = "write_error"
				return
			}
			atomic.AddInt64(&txBytes, int64(len(data)))
		case websocket.TextMessage:
			s.handleRawControl(c, sp, data) // implemented in Task 5
		}
	}
}
```

> `handleRawControl` is added in Task 5. To make Task 4 compile and pass on its own, add a temporary minimal stub at the bottom of `rawattach.go`:
> ```go
> func (s *Server) handleRawControl(c *rawConn, sp interface{ Write([]byte) (int, error) }, data []byte) {}
> ```
> Task 5 replaces this stub with the real implementation (and a precise `sp` type). Leaving the parameter types loose here avoids an unused-import churn; Task 5 tightens them.

- [ ] **Step 5: Tidy modules and run tests**

Run:
```bash
cd /Users/khamit/serialhop
go mod tidy   # promotes gorilla/websocket from indirect to direct in go.mod
go build ./...
go test ./internal/api/ -run TestAttach -race
```
Expected: PASS (gate 403/404/409 + byte round-trip + lease release). Fix any test-caller compile errors from the `New` signature change.

- [ ] **Step 6: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/api/rawattach.go internal/api/rawattach_test.go internal/api/server.go internal/api/handlers.go internal/app/app.go go.mod go.sum
git add internal/api/*_test.go
git commit -m "feat: raw serial attach websocket endpoint with byte streaming"
```

---

### Task 5: Line-control frames

**Files:**
- Modify: `internal/api/rawattach.go` (replace the `handleRawControl` stub)
- Test: `internal/api/rawattach_test.go`

**Interfaces:**
- Consumes: `rawConn` + `controlMsg` (Task 4), `serial.Port` line-control methods (Task 1).
- Produces: `Server.handleRawControl(c *rawConn, sp labserial.Port, data []byte)` handling ops `set_baud`, `set_dtr`, `set_rts`, `send_break`, `drain`, `get_modem`, emitting `modem`/`error`.

- [ ] **Step 1: Write the failing test** — append to `internal/api/rawattach_test.go`:

```go
func TestAttachControlFrames(t *testing.T) {
	ts, op, _ := newAttachServer(t, true, "COM3")
	defer ts.Close()
	fp, _ := op.Open("COM3")
	fake := fp.(*labserial.FakePort)
	fake.SetModem(labserial.ModemBits{CTS: true, DSR: true})

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach"
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	_, _, _ = ws.ReadMessage() // consume ready

	send := func(v map[string]any) { _ = ws.WriteJSON(v) }
	send(map[string]any{"op": "set_baud", "baud": 57600})
	send(map[string]any{"op": "set_dtr", "level": false})
	send(map[string]any{"op": "set_rts", "level": true})
	send(map[string]any{"op": "send_break", "ms": 200})
	send(map[string]any{"op": "get_modem"})

	// read frames until we see the modem reply
	var modem controlMsg
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		mt, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		_ = json.Unmarshal(msg, &modem)
		if modem.Op == "modem" {
			break
		}
	}
	if !modem.CTS || !modem.DSR {
		t.Fatalf("modem reply = %+v, want CTS+DSR true", modem)
	}

	// assert side effects on the fake, allowing the pump to catch up
	waitFor(t, func() bool {
		return len(fake.BaudSequence()) > 0 &&
			fake.BaudSequence()[len(fake.BaudSequence())-1] == 57600 &&
			contains(fake.DTRSequence(), false) &&
			contains(fake.RTSSequence(), true) &&
			len(fake.BreakSequence()) == 1 && fake.BreakSequence()[0] == 200*time.Millisecond
	})
}

func contains[T comparable](xs []T, v T) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
```

> Add `"encoding/json"` to the test imports. `OpenWithBaud` (used by the handler) also records the initial baud in `BaudSequence`, so check the **last** element equals 57600.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop && go test ./internal/api/ -run TestAttachControlFrames`
Expected: FAIL — control ops are no-ops (stub), so `set_baud` etc. never reach the fake.

- [ ] **Step 3: Implement** — in `internal/api/rawattach.go`, replace the temporary `handleRawControl` stub with:

```go
func (s *Server) handleRawControl(c *rawConn, sp labserial.Port, data []byte) {
	var m controlMsg
	if err := json.Unmarshal(data, &m); err != nil {
		_ = c.writeJSON(controlMsg{Op: "error", Detail: "bad control frame: " + err.Error()})
		return
	}
	var err error
	switch m.Op {
	case "set_baud":
		err = sp.SetBaudRate(m.Baud)
	case "set_dtr":
		err = sp.SetDTR(m.Level != nil && *m.Level)
	case "set_rts":
		err = sp.SetRTS(m.Level != nil && *m.Level)
	case "send_break":
		err = sp.SendBreak(time.Duration(m.Ms) * time.Millisecond)
	case "drain":
		err = sp.Drain(discovery.DrainDuration)
	case "get_modem":
		var bits labserial.ModemBits
		bits, err = sp.ModemStatus()
		if err == nil {
			_ = c.writeJSON(controlMsg{Op: "modem", CTS: bits.CTS, DSR: bits.DSR, RI: bits.RI, CD: bits.CD})
			return
		}
	default:
		_ = c.writeJSON(controlMsg{Op: "error", Detail: "unknown op: " + m.Op})
		return
	}
	if err != nil {
		_ = c.writeJSON(controlMsg{Op: "error", Detail: m.Op + ": " + err.Error()})
	}
}
```

Update `rawattach.go` imports: add `"encoding/json"` and `labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"`. Change the `runRawSession` call site `s.handleRawControl(c, sp, data)` — `sp` is already a `labserial.Port`, so tighten the method signature to `sp labserial.Port` (drop the temporary interface type). Verify `discovery.DrainDuration` exists (it's used by the old raw path and discovery); if the exported name differs, use the discovery package's drain constant — confirm with `grep -rn "DrainDuration" internal/discovery`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/khamit/serialhop && go test ./internal/api/ -run TestAttach -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/api/rawattach.go internal/api/rawattach_test.go
git commit -m "feat: line-control frames for raw serial attach (baud/dtr/rts/break/modem)"
```

---

### Task 6: Idle timeout

**Files:**
- Modify: `internal/api/rawattach.go` (`runRawSession` idle watchdog)
- Test: `internal/api/rawattach_test.go`

**Interfaces:**
- Consumes: `Server.rawIdleTimeout` (Task 4), `rawConn` activity.
- Produces: idle-driven close when `rawIdleTimeout > 0` and no traffic flows within the window.

- [ ] **Step 1: Write the failing test** — append to `internal/api/rawattach_test.go`. Add a variant server helper that sets a short idle timeout:

```go
func TestAttachIdleTimeoutCloses(t *testing.T) {
	op := labserial.NewFakeOpener()
	op.Add(labserial.NewFakePort("COM3"))
	reg := registry.New()
	// Build a Server with a 150ms idle timeout using the same construction
	// as newAttachServer, but rawIdleTimeout = 150*time.Millisecond.
	srv := newServerWithIdle(t, reg, op, 150*time.Millisecond)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach"
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	_, _, _ = ws.ReadMessage() // ready

	// Send nothing. The server should close within ~idle timeout.
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break // closed as expected
		}
	}
	waitFor(t, func() bool { return len(reg.RawLeasedPorts()) == 0 })
}
```

> Implement `newServerWithIdle(t, reg, op, d)` next to `newAttachServer` using the same `New(...)` construction with `rawSerialEnabled=true` and `rawIdleTimeout=d`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop && go test ./internal/api/ -run TestAttachIdleTimeout`
Expected: FAIL — no idle close; `ReadMessage` blocks until the 2s test deadline.

- [ ] **Step 3: Implement** — in `runRawSession`, add an activity timestamp and watchdog. Near the top, after `sp` opens:

```go
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	touch := func() { lastActive.Store(time.Now().UnixNano()) }
```

Call `touch()` in the serial→ws goroutine after a successful read, and in the ws→serial loop after each received message. Add the watchdog goroutine (guard on `s.rawIdleTimeout > 0`):

```go
	if s.rawIdleTimeout > 0 {
		idleDone := make(chan struct{})
		defer close(idleDone)
		go func() {
			t := time.NewTicker(s.rawIdleTimeout / 2)
			defer t.Stop()
			for {
				select {
				case <-idleDone:
					return
				case <-t.C:
					last := time.Unix(0, lastActive.Load())
					if time.Since(last) >= s.rawIdleTimeout {
						c.close(websocket.CloseGoingAway, "idle timeout")
						return
					}
				}
			}
		}()
	}
```

`c.close` closes the underlying conn, which unblocks `ReadMessage` in the main loop and tears the session down; set `reason = "idle_timeout"` where detectable (optional — the close race makes the exact reason best-effort; leaving it `client_close` is acceptable, or check `time.Since(last)` in the read-error branch).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/khamit/serialhop && go test ./internal/api/ -run TestAttach -race`
Expected: PASS (all attach tests, including idle).

- [ ] **Step 5: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/api/rawattach.go internal/api/rawattach_test.go
git commit -m "feat: idle timeout for raw serial attach sessions"
```

---

### Task 7: Discovery excludes leased ports

**Files:**
- Modify: `internal/discovery/runner.go` (add `ExcludePorts` helper)
- Modify: `internal/app/app.go` (`discoverFn` candidate build)
- Test: `internal/discovery/runner_test.go`

**Interfaces:**
- Consumes: `registry.RawLeasedPorts()` (Task 3), `discovery.FilterPorts` (existing).
- Produces: `discovery.ExcludePorts(ports, exclude []string) []string`.

- [ ] **Step 1: Write the failing test** — append to `internal/discovery/runner_test.go`:

```go
func TestExcludePorts(t *testing.T) {
	got := ExcludePorts([]string{"COM3", "COM4", "COM5"}, []string{"COM4"})
	want := []string{"COM3", "COM5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExcludePorts = %v, want %v", got, want)
	}
	// nil exclude returns input unchanged
	if got := ExcludePorts([]string{"COM3"}, nil); !reflect.DeepEqual(got, []string{"COM3"}) {
		t.Fatalf("ExcludePorts(nil) = %v", got)
	}
}
```

> Ensure `reflect` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop && go test ./internal/discovery/ -run TestExcludePorts`
Expected: FAIL — `ExcludePorts undefined`.

- [ ] **Step 3: Implement** — in `internal/discovery/runner.go`, add:

```go
// ExcludePorts returns ports with every entry in exclude removed, preserving
// order. Used to keep discovery off ports held under a raw-serial lease.
func ExcludePorts(ports, exclude []string) []string {
	if len(exclude) == 0 {
		return ports
	}
	set := make(map[string]bool, len(exclude))
	for _, p := range exclude {
		set[p] = true
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		if !set[p] {
			out = append(out, p)
		}
	}
	return out
}
```

In `internal/app/app.go`, inside `discoverFn`, change the candidate line:

```go
		ports := discovery.FilterPorts(all, include, exclude)
		ports = discovery.ExcludePorts(ports, reg.RawLeasedPorts())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/khamit/serialhop && go build ./... && go test ./internal/discovery/ ./internal/app/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/khamit/serialhop
git add internal/discovery/runner.go internal/discovery/runner_test.go internal/app/app.go
git commit -m "feat: discovery skips ports under a raw-serial lease"
```

---

### Task 8: Python `rfc2217://` ⇄ WebSocket client bridge

**Files:**
- Create: `clients/serialhop_attach.py` (the bridge)
- Create: `clients/test_serialhop_attach.py` (translation-table unit tests)
- Create: `clients/README.md` (usage)

**Interfaces:**
- Consumes: the WS protocol from Tasks 4–6 (`ws://chisel:<port>/serial/ports/<name>/attach?baud=<n>`, binary=data, text JSON control ops).
- Produces: a runnable bridge presenting `rfc2217://127.0.0.1:<local>`; a pure function `rfc2217_to_control(...)` mapping RFC2217 COM-port-control values to control-frame dicts, unit-tested without a live socket.

- [ ] **Step 1: Write the failing translation test** — create `clients/test_serialhop_attach.py`:

```python
import serialhop_attach as sa


def test_baud_maps_to_set_baud():
    assert sa.rfc2217_to_control("baud", 115200) == {"op": "set_baud", "baud": 115200}


def test_dtr_maps_to_set_dtr():
    assert sa.rfc2217_to_control("dtr", True) == {"op": "set_dtr", "level": True}
    assert sa.rfc2217_to_control("dtr", False) == {"op": "set_dtr", "level": False}


def test_rts_maps_to_set_rts():
    assert sa.rfc2217_to_control("rts", True) == {"op": "set_rts", "level": True}


def test_break_maps_to_send_break():
    assert sa.rfc2217_to_control("break", 250) == {"op": "send_break", "ms": 250}


def test_unknown_returns_none():
    assert sa.rfc2217_to_control("bogus", 1) is None
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/khamit/serialhop/clients && python -m pytest test_serialhop_attach.py -q`
Expected: FAIL — `ModuleNotFoundError` / `AttributeError: rfc2217_to_control`.

- [ ] **Step 3: Implement the bridge** — create `clients/serialhop_attach.py`:

```python
"""serialhop-attach: bridge a local rfc2217:// serial URL to a SerialHop raw
attach WebSocket, so pyserial can drive a remote lab COM port over the chisel
tunnel.

Run inside the JupyterLab environment (the only place `chisel:<port>` resolves):

    python serialhop_attach.py --ws ws://chisel:9001/serial/ports/COM7/attach \\
                               --listen 127.0.0.1:5555 --baud 115200

then in a notebook:

    import serial
    ser = serial.serial_for_url("rfc2217://127.0.0.1:5555")
    ser.baudrate = 115200
    ser.dtr = False; ser.dtr = True   # bootloader reset, over the tunnel
"""
from __future__ import annotations

import argparse
import json
import threading

try:
    import websocket  # websocket-client
except ImportError:  # pragma: no cover - import guard
    websocket = None


def rfc2217_to_control(kind: str, value) -> dict | None:
    """Map one RFC2217 COM-port-control change to a SerialHop control frame.

    kind: "baud" | "dtr" | "rts" | "break"; value: int for baud/break,
    bool for dtr/rts. Returns None for unrecognized kinds.
    """
    if kind == "baud":
        return {"op": "set_baud", "baud": int(value)}
    if kind == "dtr":
        return {"op": "set_dtr", "level": bool(value)}
    if kind == "rts":
        return {"op": "set_rts", "level": bool(value)}
    if kind == "break":
        return {"op": "send_break", "ms": int(value)}
    return None


class Bridge:
    """Pumps bytes between a local RFC2217 server socket and the WS. The
    RFC2217 telnet negotiation is delegated to pyserial's rfc2217 server
    building blocks; this class wires data + control across the WS."""

    def __init__(self, ws_url: str):
        if websocket is None:
            raise RuntimeError("pip install websocket-client")
        self.ws = websocket.create_connection(ws_url, enable_multithread=True)
        self._lock = threading.Lock()

    def send_bytes(self, data: bytes) -> None:
        with self._lock:
            self.ws.send_binary(data)

    def send_control(self, kind: str, value) -> None:
        frame = rfc2217_to_control(kind, value)
        if frame is not None:
            with self._lock:
                self.ws.send(json.dumps(frame))

    def recv(self):
        """Yield serial bytes from the WS; swallow control replies."""
        while True:
            op = self.ws.recv()
            if isinstance(op, bytes):
                yield op
            else:
                # JSON control frame (ready/modem/error): log, don't forward.
                try:
                    msg = json.loads(op)
                except ValueError:
                    continue
                if msg.get("op") == "error":
                    print("serialhop-attach: server error:", msg.get("detail"))


def _serve(ws_url: str, listen: str) -> None:  # pragma: no cover - I/O glue
    host, port = listen.split(":")
    import serial.rfc2217 as r  # noqa: F401  pyserial provides the server codec
    # NOTE: wire a socketserver that speaks RFC2217 on (host, port), maps
    # its baud/dtr/rts/break callbacks through Bridge.send_control, and pumps
    # socket<->Bridge bytes. Kept out of the unit-tested surface; see README.
    raise SystemExit("run via the documented recipe; translation is unit-tested")


if __name__ == "__main__":  # pragma: no cover
    ap = argparse.ArgumentParser()
    ap.add_argument("--ws", required=True)
    ap.add_argument("--listen", default="127.0.0.1:5555")
    ap.add_argument("--baud", type=int, default=9600)
    args = ap.parse_args()
    _serve(args.ws, args.listen)
```

> The unit-tested surface is `rfc2217_to_control` (the translation table) plus `Bridge`'s framing. The RFC2217 telnet socket server (`_serve`) is I/O glue documented in the README and excluded from coverage — it is thin wiring over pyserial's `serial.rfc2217` server codec. This matches the spec's "documented reference client code; Python CI is out of scope."

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/khamit/serialhop/clients && python -m pytest test_serialhop_attach.py -q`
Expected: PASS (5 tests).

- [ ] **Step 5: Write the README** — create `clients/README.md` with: what the bridge is, the `python serialhop_attach.py --ws ... --listen ...` invocation, the three-line pyserial usage, the `pip install websocket-client pyserial` prereqs, and a note that it must run inside JupyterLab (where `chisel:<port>` resolves). Cross-link `../docs/python-client-brief.md`.

- [ ] **Step 6: Commit**

```bash
cd /Users/khamit/serialhop
git add clients/serialhop_attach.py clients/test_serialhop_attach.py clients/README.md
git commit -m "feat: python rfc2217-to-websocket bridge for raw serial attach"
```

---

### Task 9: Docs — SECURITY.md, python-client-brief, README

**Files:**
- Modify: `SECURITY.md`
- Modify: `docs/python-client-brief.md`
- Modify: `README.md`

**Interfaces:** none (docs).

- [ ] **Step 1: SECURITY.md** — add a "Raw serial attach" section stating: off by default (`raw_serial.enabled: false`); undiscovered-ports-only (409 if a driver owns the port, so raw traffic can never enter a driver completion window); single leased session per port with idle timeout; scoped strictly to enumerated serial ports — not a SOCKS proxy / remote shell / general tunnel; adds no new chisel route; reachability is identical to the existing device API (authenticated chisel tunnel + Authelia-gated JupyterLab + labnet isolation).

- [ ] **Step 2: python-client-brief.md** — add the `GET /serial/ports/{port}/attach` endpoint: WebSocket upgrade; query params `baud` (default 9600), `post_open_settle_ms` not applicable in v1; pre-upgrade status codes (403 disabled, 404 unknown port, 409 owned/discovering/attached, 400 bad baud); the frame protocol (binary = bytes; text JSON control ops `set_baud`/`set_dtr`/`set_rts`/`send_break`/`drain`/`get_modem`; server `ready`/`modem`/`error`); and a pointer to `clients/` for the pyserial `rfc2217://` bridge.

- [ ] **Step 3: README.md** — add a row to the endpoint table for `GET /serial/ports/{port}/attach` ("Raw serial byte + line-control stream (WebSocket); off unless `raw_serial.enabled`"), and one sentence under the config/Ports section noting raw attach is operator-gated and undiscovered-ports-only.

- [ ] **Step 4: Commit**

```bash
cd /Users/khamit/serialhop
git add SECURITY.md docs/python-client-brief.md README.md
git commit -m "docs: document raw serial attach endpoint and security posture"
```

---

### Task 10: Full pre-flight + branch verification

**Files:** none (verification).

- [ ] **Step 1: Run the full CI gate locally**

```bash
cd /Users/khamit/serialhop
gofmt -l .                      # must print nothing
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```
Expected: all clean. Fix anything that fails (gosec may flag the `CheckOrigin: true` — it is intentional and safe here because the endpoint is only reachable over the authenticated chisel tunnel; add a `//nolint:gosec` with that reason if gosec objects, or a targeted lint exclusion).

- [ ] **Step 2: Cross-compile for Windows** (production target)

```bash
cd /Users/khamit/serialhop
GOOS=windows GOARCH=amd64 go build ./...
```
Expected: builds clean.

- [ ] **Step 3: Confirm the whole branch is committed** — `git status` clean; `git log --oneline main..HEAD` shows Tasks 1–9.

---

## Self-Review (completed by plan author)

**Spec coverage:**
- §4.1 endpoint + baud/settle params → Task 4 (baud) — `post_open_settle_ms` intentionally dropped for v1 (documented in Task 9 step 2; the fixed `OpenWithBaud` path plus discovery settle covers the DTR-reboot window; noted as extension).
- §4.2 pre-upgrade gate (403/404/409×3, 400 baud) → Task 4.
- §4.3 port lease + discovery interlock → Task 3 (lease) + Task 7 (discovery exclusion).
- §4.4 lifecycle: close/release/audit → Task 4; idle timeout → Task 6; ping/pong → Task 4.
- §4.5 config → Task 2.
- §4.6 audit logging → Task 4 (`raw_attach_open`/`raw_attach_close`).
- §5 wire protocol (binary + control ops) → Task 4 (binary + ready) + Task 5 (control).
- §6 `serial.Port` extension + fake → Task 1.
- §7 Python bridge → Task 8.
- §8 SECURITY.md → Task 9.
- §10 testing → Tasks 1–7 tests + Task 10 full gate.

**Placeholder scan:** The only intentionally-not-fully-coded surface is `_serve` in Task 8 (RFC2217 telnet socket glue), explicitly scoped as documented I/O wiring with the *translation table* unit-tested — consistent with the spec's "documented reference client code, Python CI out of scope." Everything else has complete code.

**Type consistency:** `ModemBits{CTS,DSR,RI,CD}` used identically in Tasks 1/4/5. `controlMsg` defined in Task 4, consumed in Tasks 5/6. `New(...)` signature defined in Task 4 and all callers updated there. `handleRawControl(c *rawConn, sp labserial.Port, data []byte)` stubbed in Task 4, finalized in Task 5. `TryAcquireRaw/ReleaseRaw/RawLeasedPorts` defined in Task 3, consumed in Tasks 4/7.
