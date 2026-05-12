# Remote Firmware Flashing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /flash/{port}` (plus the two supporting endpoints `POST /devices/disconnect` and `GET /serial/ports/detailed`) so a lab-bridge operator can remotely flash an Arduino Uno on a chosen COM port, with an automatic pre-flash flash-memory backup, post-program byte-verify, optional operator-supplied test pair, and auto-rollback to the backup on any post-backup failure.

**Architecture:** New self-contained `internal/flasher` package implements optiboot's STK500v1 protocol in native Go (no `avrdude.exe`). Stages run in-process; backup is saved on disk *and* returned inline. The API layer enforces preflight (registry empty, flashing enabled, single-flight) and translates a `flasher.Result` into HTTP JSON. `internal/serial.Port` gains `SetDTR` and `SetBaudRate`; `internal/serial.Opener` gains `OpenWithBaud` and `ListDetailed`. Everything else (`discovery`, `chisel`, `winsvc`, `logship`, `panel`, `updater`) is untouched.

**Tech Stack:** Go 1.22+, `go.bug.st/serial` (already a dependency — exposes DTR/SetMode/GetDetailedPortsList), `gopkg.in/yaml.v3` (config), `crypto/sha256` (backup checksums), stdlib `net/http` path-pattern routing.

**Spec:** `docs/superpowers/specs/2026-05-12-remote-firmware-flashing-design.md`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/config/config.go` | Modify | Add `FlashingConfig` struct, field on `Config`, `Default()` value, scaffold section. |
| `internal/config/config_test.go` | Modify | Default-value coverage; scaffold round-trip preserves new section. |
| `internal/config/load.go` | Modify | `Validate` rejects `flashing.enabled: true` + relative `backup_dir` and `keep_n < 0`. |
| `internal/config/load_test.go` | Modify | Parse `flashing:` block; validation negative cases. |
| `internal/paths/paths.go` | Modify | Add `BackupsDir() string` (`<DataDir>/backups`). |
| `internal/paths/paths_test.go` | Modify | Coverage for `BackupsDir()` honoring `SERIALHOP_DATA_DIR` and `""` when unset. |
| `internal/registry/registry.go` | Modify | Add `DisconnectAll() int` — close-all + replace-with-empty, returning prior count. |
| `internal/registry/registry_test.go` | Modify | Tests for `DisconnectAll()` on empty and populated registries. |
| `internal/serial/port.go` | Modify | Extend `Port` with `SetDTR(bool) error` and `SetBaudRate(int) error`. Extend `Opener` with `OpenWithBaud(name string, baud int) (Port, error)` and `ListDetailed() ([]DetailedPort, error)`. Add `DetailedPort` struct. |
| `internal/serial/fake.go` | Modify | Mirror additions: programmable `OpenWithBaud`, `ListDetailed`, `SetDTR`, `SetBaudRate`. |
| `internal/serial/fake_test.go` | Modify | Coverage for `OpenWithBaud`, `SetDTR` recording, `SetBaudRate` recording, `ListDetailed`. |
| `internal/flasher/avr/uno.go` | Create | Per-chip constants (`FlashSize`, `BootloaderSize`, `PageSize`, `BootloaderBaud`, `TargetBaud`). |
| `internal/flasher/intelhex.go` | Create | `ParseIntelHex([]byte) ([]byte, error)`; `RenderIntelHex([]byte) string`. |
| `internal/flasher/intelhex_test.go` | Create | Round-trip, parser error cases, fuzz. |
| `internal/flasher/testdata/uno_blink.hex` | Create | Golden Intel HEX fixture — small Uno sketch (blink) checked into repo. |
| `internal/flasher/testing/fake_optiboot.go` | Create | `FakeOptiboot`: in-memory STK500v1 responder implementing `serial.Port`. Error-injection knobs. |
| `internal/flasher/testing/fake_optiboot_test.go` | Create | Self-test of the fake's state machine. |
| `internal/flasher/stk500v1.go` | Create | STK500v1 client: `Sync`, `GetSignOn`, `LoadAddress`, `ProgPage`, `ReadPage`, `ChipErase`, `LeaveProgMode`. |
| `internal/flasher/stk500v1_test.go` | Create | Tests against `FakeOptiboot`. |
| `internal/flasher/backupstore.go` | Create | `SaveBackup`, `LockBackup`, `PruneBackups`, sha256 helper. |
| `internal/flasher/backupstore_test.go` | Create | Tempdir-based save/lock/prune tests. |
| `internal/flasher/flasher.go` | Create | `Flasher` interface, `flasherImpl` struct, `New(...)`, `ErrBusy`, public `Flash(ctx, port, Request) (*Result, error)`. |
| `internal/flasher/stages.go` | Create | Stage functions: `runPreflight`, `runBackup`, `runErase`, `runProgram`, `runVerify`, `runTest`, `runRollback`. |
| `internal/flasher/stages_test.go` | Create | End-to-end runs of `Flasher.Flash()` against the fake — one test per row in the outcome taxonomy. |
| `internal/api/types.go` | Modify | Add `FlashRequest`, `FlashResponse`, `StageDTO`, `BackupDTO`, `TestResultDTO`, `DetailedPortDTO`, `DisconnectResponse`. |
| `internal/api/flash.go` | Create | `handlePostDevicesDisconnect`, `handleGetSerialPortsDetailed`, `handlePostFlashPort`. |
| `internal/api/flash_test.go` | Create | HTTP-layer tests using a stub `Flasher`. |
| `internal/api/server.go` | Modify | Register the three new routes. |
| `internal/api/handlers.go` | Modify | `Server` struct gains `flasher Flasher` and `flashingEnabled bool` fields; `New(...)` signature grows two params. |
| `internal/api/handlers_test.go` | Modify | Update `newTestServer` and other `New(...)` call sites to pass new params. |
| `internal/api/raw_serial_test.go` | Modify | Update `New(...)` call sites for new params. |
| `internal/app/app.go` | Modify | Resolve backup dir, construct `flasher.New(...)`, pass to `api.New(...)`. |
| `README.md` | Modify | Add three new endpoints to the REST API table; mention `flashing:` config block. |

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

## Task 1: Add `flashing` config block

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`

- [ ] **Step 1.1: Write failing test for `Default().Flashing`**

Append to `internal/config/config_test.go` after the existing `TestDefaultConfig*`:

```go
func TestDefaultConfig_FlashingDefaults(t *testing.T) {
	c := Default()
	if c.Flashing.Enabled {
		t.Errorf("flashing.enabled: got true, want false")
	}
	if c.Flashing.BackupDir != "" {
		t.Errorf("flashing.backup_dir: got %q, want \"\"", c.Flashing.BackupDir)
	}
	if c.Flashing.KeepN != 10 {
		t.Errorf("flashing.keep_n: got %d, want 10", c.Flashing.KeepN)
	}
}
```

- [ ] **Step 1.2: Run — should fail**

```bash
go test ./internal/config/ -run TestDefaultConfig_FlashingDefaults -count=1 -v
```

Expected: compile error — `c.Flashing undefined (type Config has no field or method Flashing)`.

- [ ] **Step 1.3: Add the struct + field + default**

Edit `internal/config/config.go`:

After `type AutoUpdateConfig struct { ... }`, add:

```go
type FlashingConfig struct {
	Enabled   bool   `yaml:"enabled"`
	BackupDir string `yaml:"backup_dir"`
	KeepN     int    `yaml:"keep_n"`
}
```

In `type Config struct`, after the `AutoUpdate` field, add:

```go
	Flashing   FlashingConfig   `yaml:"flashing"`
```

In `Default()`, after the `AutoUpdate` field, add:

```go
		Flashing:   FlashingConfig{Enabled: false, BackupDir: "", KeepN: 10},
```

- [ ] **Step 1.4: Run — should pass**

```bash
go test ./internal/config/ -run TestDefaultConfig_FlashingDefaults -count=1 -v
```

Expected: PASS.

- [ ] **Step 1.5: Update scaffold to include the new section**

Edit the `scaffoldTemplate` constant in `internal/config/config.go`. After the `auto_update:` block and before the closing backtick, append:

```
flashing:
  enabled: false                  # allow POST /flash/{port}. higher risk than
                                  # raw_serial — a bad .hex bricks the board
                                  # (ISP recovery required). independent of
                                  # raw_serial.enabled.
  backup_dir: ""                  # absolute path for pre-flash backups.
                                  # empty -> %ProgramData%\SerialHop\backups
  keep_n: 10                      # retain this many backups per COM port;
                                  # oldest pruned after each completed flash.
                                  # 0 = keep all.
```

- [ ] **Step 1.6: Add scaffold round-trip assertion for the new section**

In `internal/config/config_test.go`, locate `TestWriteScaffold_RoundTrip` (or the equivalent — find with `grep -n "WriteScaffold" internal/config/config_test.go`). After the existing `parsed.AutoUpdate.Enabled` assertion (or last assertion if missing), append:

```go
	if parsed.Flashing.Enabled {
		t.Errorf("round-trip flashing.enabled: got true, want false (default)")
	}
	if parsed.Flashing.BackupDir != "" {
		t.Errorf("round-trip flashing.backup_dir: got %q, want \"\"", parsed.Flashing.BackupDir)
	}
	if parsed.Flashing.KeepN != 10 {
		t.Errorf("round-trip flashing.keep_n: got %d, want 10", parsed.Flashing.KeepN)
	}
```

- [ ] **Step 1.7: Write failing tests for validation**

Append to `internal/config/load_test.go`:

```go
func TestValidate_FlashingRejectsRelativeBackupDir(t *testing.T) {
	c := Default()
	c.LabBridge.Host = "h"
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	c.Flashing.Enabled = true
	c.Flashing.BackupDir = "relative/path"
	err := Validate(&c)
	if err == nil {
		t.Fatal("expected error for relative backup_dir, got nil")
	}
	if !strings.Contains(err.Error(), "backup_dir") {
		t.Errorf("error message %q must mention backup_dir", err)
	}
}

func TestValidate_FlashingRejectsNegativeKeepN(t *testing.T) {
	c := Default()
	c.LabBridge.Host = "h"
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	c.Flashing.KeepN = -1
	err := Validate(&c)
	if err == nil {
		t.Fatal("expected error for negative keep_n, got nil")
	}
	if !strings.Contains(err.Error(), "keep_n") {
		t.Errorf("error message %q must mention keep_n", err)
	}
}

func TestValidate_FlashingAcceptsEmptyBackupDirWhenDisabled(t *testing.T) {
	c := Default()
	c.LabBridge.Host = "h"
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	c.Flashing.Enabled = false
	c.Flashing.BackupDir = "" // empty + disabled = fine
	if err := Validate(&c); err != nil {
		t.Errorf("Validate: %v", err)
	}
}
```

Make sure `import "strings"` is present (add to imports if missing).

- [ ] **Step 1.8: Run — should fail (validate has no `flashing` handling yet)**

```bash
go test ./internal/config/ -run TestValidate_Flashing -count=1 -v
```

Expected: two failures (negative cases) — currently `Validate` returns nil for both.

- [ ] **Step 1.9: Add validation rules**

In `internal/config/load.go`, before the final `return nil` in `Validate`, add:

```go
	if c.Flashing.KeepN < 0 {
		return fmt.Errorf("flashing.keep_n must be >= 0 (got %d)", c.Flashing.KeepN)
	}
	if c.Flashing.Enabled && c.Flashing.BackupDir != "" && !filepath.IsAbs(c.Flashing.BackupDir) {
		return fmt.Errorf("flashing.backup_dir must be absolute when flashing.enabled (got %q)", c.Flashing.BackupDir)
	}
```

Add `"path/filepath"` to the imports.

- [ ] **Step 1.10: Run — should pass**

```bash
go test ./internal/config/ -count=1
```

Expected: all PASS.

- [ ] **Step 1.11: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/load.go internal/config/load_test.go
git commit -m "feat(config): add flashing config block, default off"
```

---

## Task 2: Add `paths.BackupsDir()`

**Files:**
- Modify: `internal/paths/paths.go`
- Modify: `internal/paths/paths_test.go`

- [ ] **Step 2.1: Write failing test**

Append to `internal/paths/paths_test.go` (create the file if missing — its structure mirrors `internal/paths/paths.go` and any existing test conventions you can see with `ls internal/paths/`):

```go
func TestBackupsDir_UnderDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/tmp/sh")
	got := BackupsDir()
	want := filepath.Join("/tmp/sh", "backups")
	if got != want {
		t.Errorf("BackupsDir: got %q, want %q", got, want)
	}
}

func TestBackupsDir_EmptyWhenNoDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	got := BackupsDir()
	if got != "" {
		t.Errorf("BackupsDir: got %q, want \"\"", got)
	}
}
```

Ensure imports include `"path/filepath"` and `"testing"`.

- [ ] **Step 2.2: Run — should fail**

```bash
go test ./internal/paths/ -run TestBackupsDir -count=1 -v
```

Expected: compile error — undefined `BackupsDir`.

- [ ] **Step 2.3: Implement `BackupsDir()`**

Edit `internal/paths/paths.go`. After `LogsDir()` (around line 49), add:

```go
// BackupsDir returns <DataDir>/backups, or "" if DataDir is empty.
func BackupsDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "backups")
}
```

- [ ] **Step 2.4: Run — should pass**

```bash
go test ./internal/paths/ -count=1
```

Expected: all PASS.

- [ ] **Step 2.5: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat(paths): add BackupsDir helper"
```

---

## Task 3: Add `Registry.DisconnectAll()`

**Files:**
- Modify: `internal/registry/registry.go`
- Modify: `internal/registry/registry_test.go`

- [ ] **Step 3.1: Write failing tests**

Append to `internal/registry/registry_test.go`:

```go
func TestDisconnectAll_EmptyRegistry(t *testing.T) {
	r := New()
	got := r.DisconnectAll()
	if got != 0 {
		t.Errorf("DisconnectAll on empty: got %d, want 0", got)
	}
	if len(r.List()) != 0 {
		t.Errorf("registry not empty after DisconnectAll")
	}
}

func TestDisconnectAll_PopulatedRegistry(t *testing.T) {
	r := New()
	devs := []*Device{
		{ID: "a", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3")},
		{ID: "b", Type: "valve", TypeCode: 30, Port: "COM4", Conn: serial.NewFakePort("COM4")},
		{ID: "c", Type: "densitometer", TypeCode: 70, Port: "COM5", Conn: serial.NewFakePort("COM5")},
	}
	r.Replace(devs)

	got := r.DisconnectAll()
	if got != 3 {
		t.Errorf("DisconnectAll: got %d, want 3", got)
	}
	if len(r.List()) != 0 {
		t.Errorf("registry not empty after DisconnectAll")
	}
}
```

Ensure `import "github.com/bioexperiment-lab-devices/serialhop/internal/serial"` is present (alias as needed if there is a name collision in the test file).

- [ ] **Step 3.2: Run — should fail**

```bash
go test ./internal/registry/ -run TestDisconnectAll -count=1 -v
```

Expected: compile error — undefined `DisconnectAll`.

- [ ] **Step 3.3: Implement**

In `internal/registry/registry.go`, after `CloseAll()` (around line 96), add:

```go
// DisconnectAll closes every device port in the registry, empties the map,
// and returns the count of devices that were removed. Safe on an empty
// registry. Used by POST /devices/disconnect before a flash operation.
func (r *Registry) DisconnectAll() int {
	r.mu.Lock()
	n := len(r.devices)
	old := r.devices
	r.devices = map[string]*Device{}
	r.mu.Unlock()

	for _, d := range old {
		if d.Conn != nil {
			_ = d.Conn.Close()
		}
	}
	return n
}
```

- [ ] **Step 3.4: Run — should pass**

```bash
go test ./internal/registry/ -count=1
```

Expected: all PASS.

- [ ] **Step 3.5: Commit**

```bash
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat(registry): add DisconnectAll helper for /devices/disconnect"
```

---

## Task 4: Extend `internal/serial` for bootloader entry + detailed listing

**Files:**
- Modify: `internal/serial/port.go`
- Modify: `internal/serial/fake.go`
- Modify: `internal/serial/fake_test.go`

- [ ] **Step 4.1: Write failing tests**

Append to `internal/serial/fake_test.go`:

```go
func TestFakePort_SetDTR_Records(t *testing.T) {
	p := NewFakePort("COM3")
	if err := p.SetDTR(false); err != nil {
		t.Fatalf("SetDTR(false): %v", err)
	}
	if err := p.SetDTR(true); err != nil {
		t.Fatalf("SetDTR(true): %v", err)
	}
	got := p.DTRSequence()
	want := []bool{false, true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DTRSequence: got %v, want %v", got, want)
	}
}

func TestFakePort_SetBaudRate_Records(t *testing.T) {
	p := NewFakePort("COM3")
	if err := p.SetBaudRate(115200); err != nil {
		t.Fatalf("SetBaudRate(115200): %v", err)
	}
	if err := p.SetBaudRate(9600); err != nil {
		t.Fatalf("SetBaudRate(9600): %v", err)
	}
	got := p.BaudSequence()
	want := []int{115200, 9600}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BaudSequence: got %v, want %v", got, want)
	}
}

func TestFakeOpener_OpenWithBaud(t *testing.T) {
	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	p, err := o.OpenWithBaud("COM3", 115200)
	if err != nil {
		t.Fatalf("OpenWithBaud: %v", err)
	}
	fp, ok := p.(*FakePort)
	if !ok {
		t.Fatalf("returned port is not *FakePort")
	}
	if got := fp.BaudSequence(); len(got) != 1 || got[0] != 115200 {
		t.Errorf("BaudSequence after OpenWithBaud: got %v, want [115200]", got)
	}
}

func TestFakeOpener_ListDetailed(t *testing.T) {
	o := NewFakeOpener()
	o.Add(NewFakePort("COM3"))
	o.SetDetail("COM3", DetailedPort{
		Name: "COM3", IsUSB: true, VID: "2341", PID: "0043",
		SerialNumber: "ABC123", Product: "Arduino Uno",
	})
	got, err := o.ListDetailed()
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ListDetailed): got %d, want 1", len(got))
	}
	if got[0].Product != "Arduino Uno" || got[0].VID != "2341" {
		t.Errorf("ListDetailed[0]: got %+v", got[0])
	}
}
```

Add `"reflect"` to the test imports.

- [ ] **Step 4.2: Run — should fail**

```bash
go test ./internal/serial/ -run 'TestFakePort_SetDTR_Records|TestFakePort_SetBaudRate_Records|TestFakeOpener_OpenWithBaud|TestFakeOpener_ListDetailed' -count=1 -v
```

Expected: compile errors for `SetDTR`, `SetBaudRate`, `OpenWithBaud`, `ListDetailed`, `DetailedPort`, `DTRSequence`, `BaudSequence`, `SetDetail`.

- [ ] **Step 4.3: Extend the `Port` and `Opener` interfaces**

Edit `internal/serial/port.go`. Replace the `Port` interface block with:

```go
// Port is the abstraction the rest of the code uses.
// All read methods respect the most recent SetReadTimeout call.
type Port interface {
	Read(p []byte) (int, error) // returns (0, nil) on read-timeout, never blocks past it
	Write(p []byte) (int, error)
	SetReadTimeout(d time.Duration) error
	Drain(d time.Duration) error // discard all RX bytes available within d
	SetDTR(level bool) error     // toggle DTR line — used to reset Arduino into bootloader
	SetBaudRate(rate int) error  // change baud on the open handle (e.g., 115200 -> 9600 between bootloader and sketch)
	Close() error
	Name() string
}
```

Replace the `Opener` interface block with:

```go
// Opener creates new Ports and lists available port names.
type Opener interface {
	Open(name string) (Port, error)                  // 9600 / 8N1, no flow control
	OpenWithBaud(name string, baud int) (Port, error) // arbitrary baud, 8N1, no flow control
	List() ([]string, error)
	ListDetailed() ([]DetailedPort, error)
}

// DetailedPort carries USB descriptors from the OS — the arduino-cli board list analog.
type DetailedPort struct {
	Name         string
	IsUSB        bool
	VID, PID     string
	SerialNumber string
	Product      string
}
```

- [ ] **Step 4.4: Implement on `realOpener`/`realPort`**

In the same file, after `func (realOpener) Open(name string) (Port, error) { ... }`, add:

```go
func (realOpener) OpenWithBaud(name string, baud int) (Port, error) {
	mode := &bugst.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   bugst.NoParity,
		StopBits: bugst.OneStopBit,
	}
	p, err := bugst.Open(name, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s @ %d: %w", name, baud, err)
	}
	return &realPort{name: name, p: p}, nil
}

func (realOpener) ListDetailed() ([]DetailedPort, error) {
	raw, err := bugstenum.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	out := make([]DetailedPort, 0, len(raw))
	for _, r := range raw {
		out = append(out, DetailedPort{
			Name:         r.Name,
			IsUSB:        r.IsUSB,
			VID:          r.VID,
			PID:          r.PID,
			SerialNumber: r.SerialNumber,
			Product:      r.Product,
		})
	}
	return out, nil
}
```

Add the alias import at the top:

```go
bugstenum "go.bug.st/serial/enumerator"
```

Add the two methods on `realPort` after `func (r *realPort) SetReadTimeout(d time.Duration) error { ... }`:

```go
func (r *realPort) SetDTR(level bool) error { return r.p.SetDTR(level) }

func (r *realPort) SetBaudRate(rate int) error {
	return r.p.SetMode(&bugst.Mode{
		BaudRate: rate,
		DataBits: 8,
		Parity:   bugst.NoParity,
		StopBits: bugst.OneStopBit,
	})
}
```

- [ ] **Step 4.5: Implement on `FakePort` and `FakeOpener`**

Edit `internal/serial/fake.go`. In the `FakePort` struct definition (around line 13), add fields:

```go
	dtrSeq  []bool
	baudSeq []int
```

After the `Close()` method (around line 119), add:

```go
func (f *FakePort) SetDTR(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.dtrSeq = append(f.dtrSeq, level)
	return nil
}

func (f *FakePort) SetBaudRate(rate int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.baudSeq = append(f.baudSeq, rate)
	return nil
}

// DTRSequence returns a snapshot of every value passed to SetDTR. Test-only.
func (f *FakePort) DTRSequence() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bool, len(f.dtrSeq))
	copy(out, f.dtrSeq)
	return out
}

// BaudSequence returns a snapshot of every value passed to SetBaudRate.
// OpenWithBaud appends the initial baud as the first entry. Test-only.
func (f *FakePort) BaudSequence() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.baudSeq))
	copy(out, f.baudSeq)
	return out
}
```

In the `FakeOpener` struct (around line 122), add a `details` map field:

```go
	details map[string]DetailedPort
```

Update `NewFakeOpener` to initialize it:

```go
func NewFakeOpener() *FakeOpener {
	return &FakeOpener{
		ports:   map[string]*FakePort{},
		details: map[string]DetailedPort{},
	}
}
```

After the `Open` method, add:

```go
func (o *FakeOpener) OpenWithBaud(name string, baud int) (Port, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.ports[name]
	if !ok {
		return nil, errUnknownPort{name}
	}
	p.mu.Lock()
	p.closed = false
	p.baudSeq = append(p.baudSeq, baud)
	p.mu.Unlock()
	return p, nil
}

// SetDetail registers metadata returned by ListDetailed. Test-only.
func (o *FakeOpener) SetDetail(name string, d DetailedPort) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.details[name] = d
}

func (o *FakeOpener) ListDetailed() ([]DetailedPort, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]DetailedPort, 0, len(o.ports))
	for name := range o.ports {
		if d, ok := o.details[name]; ok {
			out = append(out, d)
		} else {
			out = append(out, DetailedPort{Name: name})
		}
	}
	return out, nil
}
```

- [ ] **Step 4.6: Run — should pass**

```bash
go test ./internal/serial/ -count=1
```

Expected: all PASS.

- [ ] **Step 4.7: Build the whole module to surface any consumer breakage**

```bash
go build ./...
```

Expected: builds clean. If any consumer of `Opener`/`Port` (e.g., test helpers) breaks because of the interface extension, fix it by adding the missing methods. The most likely affected files are test stubs in `internal/discovery/` or `internal/api/`. Search with:

```bash
grep -rn "serial.Opener" --include="*.go"
grep -rn "type .*Opener struct" --include="*.go"
```

Add no-op methods to any local stubs that satisfy the interface.

- [ ] **Step 4.8: Commit**

```bash
git add internal/serial/port.go internal/serial/fake.go internal/serial/fake_test.go
git commit -m "feat(serial): add SetDTR/SetBaudRate, OpenWithBaud, ListDetailed"
```

---

## Task 5: AVR Uno per-chip constants

**Files:**
- Create: `internal/flasher/avr/uno.go`
- Create: `internal/flasher/avr/uno_test.go`

- [ ] **Step 5.1: Write failing test**

Create `internal/flasher/avr/uno_test.go`:

```go
package avr

import "testing"

func TestUnoConstants(t *testing.T) {
	if FlashSize != 32*1024 {
		t.Errorf("FlashSize: got %d, want %d", FlashSize, 32*1024)
	}
	if BootloaderSize != 512 {
		t.Errorf("BootloaderSize: got %d, want 512", BootloaderSize)
	}
	if UserSpace := FlashSize - BootloaderSize; UserSpace != 32256 {
		t.Errorf("user space: got %d, want 32256", UserSpace)
	}
	if PageSize != 128 {
		t.Errorf("PageSize: got %d, want 128", PageSize)
	}
	if BootloaderBaud != 115200 {
		t.Errorf("BootloaderBaud: got %d, want 115200", BootloaderBaud)
	}
	if TargetBaud != 9600 {
		t.Errorf("TargetBaud: got %d, want 9600", TargetBaud)
	}
}
```

- [ ] **Step 5.2: Run — should fail (no package yet)**

```bash
go test ./internal/flasher/avr/ -count=1 -v
```

Expected: package not found.

- [ ] **Step 5.3: Implement**

Create `internal/flasher/avr/uno.go`:

```go
// Package avr holds per-chip constants for the AVR family of targets.
// Only the Arduino Uno R3 (ATmega328P with optiboot) is supported today;
// add new files (e.g., mega2560.go) when widening coverage.
package avr

const (
	// FlashSize is the total program-flash capacity in bytes.
	FlashSize = 32 * 1024

	// BootloaderSize is the optiboot region at the top of flash.
	// The user sketch is constrained to FlashSize - BootloaderSize bytes.
	BootloaderSize = 512

	// PageSize is the program-flash page size in bytes. STK500v1 writes and
	// reads happen in page-sized chunks.
	PageSize = 128

	// BootloaderBaud is the line rate the optiboot bootloader speaks.
	BootloaderBaud = 115200

	// TargetBaud is the line rate the user sketch is built against —
	// matches discovery / /command across this codebase.
	TargetBaud = 9600
)
```

- [ ] **Step 5.4: Run — should pass**

```bash
go test ./internal/flasher/avr/ -count=1 -v
```

Expected: PASS.

- [ ] **Step 5.5: Commit**

```bash
git add internal/flasher/avr/
git commit -m "feat(flasher): add AVR Uno per-chip constants"
```

---

## Task 6: Intel HEX parser + renderer

**Files:**
- Create: `internal/flasher/intelhex.go`
- Create: `internal/flasher/intelhex_test.go`

- [ ] **Step 6.1: Write failing tests for the parser**

Create `internal/flasher/intelhex_test.go`:

```go
package flasher

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseIntelHex_TwoDataRecordsAndEOF(t *testing.T) {
	// Two 4-byte data records at addresses 0x0000 and 0x0004, then EOF.
	// Record 1: :04 0000 00 01020304 F2
	// Record 2: :04 0004 00 05060708 DE
	// EOF:      :00 0000 01 FF
	input := strings.Join([]string{
		":0400000001020304F2",
		":0400040005060708DE",
		":00000001FF",
	}, "\n")
	got, err := ParseIntelHex([]byte(input))
	if err != nil {
		t.Fatalf("ParseIntelHex: %v", err)
	}
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	if !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

func TestParseIntelHex_GapsPadWithFF(t *testing.T) {
	// Record at 0x0000 (1 byte) then at 0x0004 (1 byte). Bytes at 0x0001..0x0003
	// must be 0xFF (the AVR erased state).
	input := strings.Join([]string{
		":01000000AA55",
		":010004005503",
		":00000001FF",
	}, "\n")
	got, err := ParseIntelHex([]byte(input))
	if err != nil {
		t.Fatalf("ParseIntelHex: %v", err)
	}
	want := []byte{0xAA, 0xFF, 0xFF, 0xFF, 0x55}
	if !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

func TestParseIntelHex_BadChecksum(t *testing.T) {
	// Same as the happy-path first record but with checksum 0x00 instead of 0xF2.
	input := ":0400000001020304 00\n:00000001FF"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected checksum error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error %q must mention checksum", err)
	}
}

func TestParseIntelHex_BadHexDigit(t *testing.T) {
	input := ":0400000001020Z04F2\n:00000001FF"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected hex-digit error, got nil")
	}
}

func TestParseIntelHex_MissingEOFRecord(t *testing.T) {
	input := ":0400000001020304F2"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected missing-EOF error, got nil")
	}
}

func TestParseIntelHex_RejectsUnsupportedRecordType(t *testing.T) {
	// Type 04 (extended linear address) not supported in this scope.
	input := ":02000004FFFFFC\n:00000001FF"
	_, err := ParseIntelHex([]byte(input))
	if err == nil {
		t.Fatal("expected unsupported-record-type error, got nil")
	}
	if !strings.Contains(err.Error(), "record type") {
		t.Errorf("error %q must mention record type", err)
	}
}

func TestParseIntelHex_TolerantOfWhitespaceAndBOM(t *testing.T) {
	input := "﻿  :0400000001020304F2  \r\n :00000001FF \r\n"
	got, err := ParseIntelHex([]byte(input))
	if err != nil {
		t.Fatalf("ParseIntelHex: %v", err)
	}
	if !bytes.Equal(got, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("got % X", got)
	}
}
```

- [ ] **Step 6.2: Run — should fail (no implementation)**

```bash
go test ./internal/flasher/ -run TestParseIntelHex -count=1 -v
```

Expected: undefined `ParseIntelHex`.

- [ ] **Step 6.3: Implement `ParseIntelHex`**

Create `internal/flasher/intelhex.go`:

```go
// Package flasher implements remote firmware flashing for AVR / optiboot
// targets (Arduino Uno R3 and pin-compatible clones) using the STK500v1
// protocol over a serial port.
package flasher

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
)

const eraseFill = 0xFF // AVR flash erased state — used to pad gaps between records.

// ParseIntelHex parses an Intel HEX document and returns a flat byte image
// starting at address 0x0000. Gaps between records are padded with 0xFF
// (AVR's erased-flash state). Only record types 00 (data) and 01 (EOF) are
// supported; 02–05 are rejected explicitly. The returned image is trimmed
// to the highest address referenced by a data record.
//
// Tolerates a leading UTF-8 BOM, trailing whitespace, and \r\n line endings.
func ParseIntelHex(input []byte) ([]byte, error) {
	// Strip an optional UTF-8 BOM.
	input = bytes.TrimPrefix(input, []byte{0xEF, 0xBB, 0xBF})

	var img []byte
	sawEOF := false
	for lineNum, raw := range bytes.Split(input, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		if line[0] != ':' {
			return nil, fmt.Errorf("intelhex: line %d: missing ':' prefix", lineNum+1)
		}
		body := line[1:]
		if len(body)%2 != 0 {
			return nil, fmt.Errorf("intelhex: line %d: odd-length record", lineNum+1)
		}
		buf, err := hex.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("intelhex: line %d: %w", lineNum+1, err)
		}
		if len(buf) < 5 {
			return nil, fmt.Errorf("intelhex: line %d: record too short (%d bytes)", lineNum+1, len(buf))
		}
		length := int(buf[0])
		addr := int(buf[1])<<8 | int(buf[2])
		rtype := buf[3]
		data := buf[4 : 4+length]
		// Checksum: two's complement of the sum of bytes 0..len(buf)-2 must
		// equal buf[len(buf)-1].
		var sum byte
		for _, b := range buf[:len(buf)-1] {
			sum += b
		}
		expected := byte(-int8(sum))
		got := buf[len(buf)-1]
		if got != expected {
			return nil, fmt.Errorf("intelhex: line %d: bad checksum (got %02X, want %02X)", lineNum+1, got, expected)
		}

		switch rtype {
		case 0x00:
			end := addr + length
			if end > len(img) {
				// Grow image, padding with 0xFF.
				grown := make([]byte, end)
				for i := range grown {
					grown[i] = eraseFill
				}
				copy(grown, img)
				img = grown
			}
			copy(img[addr:], data)
		case 0x01:
			sawEOF = true
		default:
			return nil, fmt.Errorf("intelhex: line %d: unsupported record type 0x%02X", lineNum+1, rtype)
		}

		if sawEOF {
			break
		}
	}
	if !sawEOF {
		return nil, fmt.Errorf("intelhex: missing EOF record (type 01)")
	}
	return img, nil
}
```

- [ ] **Step 6.4: Run — parser tests pass**

```bash
go test ./internal/flasher/ -run TestParseIntelHex -count=1 -v
```

Expected: all PASS.

- [ ] **Step 6.5: Write failing tests for the renderer**

Append to `internal/flasher/intelhex_test.go`:

```go
func TestRenderIntelHex_SingleSmallImage(t *testing.T) {
	img := []byte{0x01, 0x02, 0x03, 0x04}
	out := RenderIntelHex(img)
	// Expect one 16-byte-or-less data record plus EOF.
	if !strings.Contains(out, ":04000000") {
		t.Errorf("output missing data-record prefix: %q", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), ":00000001FF") {
		t.Errorf("output missing EOF record: %q", out)
	}
}

func TestIntelHex_RoundTrip(t *testing.T) {
	img := make([]byte, 256)
	for i := range img {
		img[i] = byte(i)
	}
	rendered := RenderIntelHex(img)
	parsed, err := ParseIntelHex([]byte(rendered))
	if err != nil {
		t.Fatalf("ParseIntelHex(rendered): %v", err)
	}
	if !bytes.Equal(img, parsed) {
		t.Errorf("round-trip mismatch")
	}
}
```

- [ ] **Step 6.6: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestRenderIntelHex|TestIntelHex_RoundTrip' -count=1 -v
```

Expected: undefined `RenderIntelHex`.

- [ ] **Step 6.7: Implement `RenderIntelHex`**

Append to `internal/flasher/intelhex.go`:

```go
// RenderIntelHex serializes a flat byte image (starting at address 0x0000)
// into Intel HEX text. Records are at most 16 data bytes each, terminated
// with the EOF record (type 01). The output is parseable by ParseIntelHex
// and by any STK500v1-compliant tool (avrdude, arduino-cli, ...).
func RenderIntelHex(img []byte) string {
	const recordSize = 16
	var sb strings.Builder
	sb.Grow(len(img) * 3) // rough overhead estimate

	for off := 0; off < len(img); off += recordSize {
		n := recordSize
		if off+n > len(img) {
			n = len(img) - off
		}
		// Header: length, address-hi, address-lo, type=00
		hdr := []byte{byte(n), byte(off >> 8), byte(off & 0xFF), 0x00}
		var sum byte
		for _, b := range hdr {
			sum += b
		}
		for _, b := range img[off : off+n] {
			sum += b
		}
		cks := byte(-int8(sum))

		sb.WriteByte(':')
		writeHex(&sb, hdr)
		writeHex(&sb, img[off:off+n])
		writeHex(&sb, []byte{cks})
		sb.WriteByte('\n')
	}
	// EOF record.
	sb.WriteString(":00000001FF\n")
	return sb.String()
}

func writeHex(sb *strings.Builder, b []byte) {
	const digits = "0123456789ABCDEF"
	for _, v := range b {
		sb.WriteByte(digits[v>>4])
		sb.WriteByte(digits[v&0x0F])
	}
}
```

- [ ] **Step 6.8: Run — should pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS.

- [ ] **Step 6.9: Add fuzz test**

Append to `internal/flasher/intelhex_test.go`:

```go
func FuzzParseIntelHex(f *testing.F) {
	f.Add([]byte(":0400000001020304F2\n:00000001FF"))
	f.Add([]byte(":00000001FF"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = ParseIntelHex(in) // must not panic
	})
}
```

Run briefly:

```bash
go test ./internal/flasher/ -run TestNothingMatchesThis -fuzz FuzzParseIntelHex -fuzztime 5s
```

Expected: no panics in 5 seconds.

- [ ] **Step 6.10: Commit**

```bash
git add internal/flasher/intelhex.go internal/flasher/intelhex_test.go
git commit -m "feat(flasher): Intel HEX parser and renderer"
```

---

## Task 7: Fake optiboot bootloader

**Files:**
- Create: `internal/flasher/testing/fake_optiboot.go`
- Create: `internal/flasher/testing/fake_optiboot_test.go`

This task builds the in-memory STK500v1 responder that every subsequent test relies on. The fake implements `serial.Port` so it can plug into the STK500v1 client unchanged. It maintains a 32 KB flash array and a single 16-bit word-address register.

- [ ] **Step 7.1: Write failing test**

Create `internal/flasher/testing/fake_optiboot_test.go`:

```go
package testing_test

import (
	"bytes"
	"testing"
	"time"

	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
)

const (
	stkGetSync       byte = 0x30
	stkCrcEop        byte = 0x20
	stkInSync        byte = 0x14
	stkOK            byte = 0x10
)

func TestFakeOptiboot_RespondsToSync(t *testing.T) {
	f := ft.NewFakeOptiboot()
	if err := f.SetReadTimeout(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{stkGetSync, stkCrcEop}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readN(t, f, 2, 200*time.Millisecond)
	want := []byte{stkInSync, stkOK}
	if !bytes.Equal(got, want) {
		t.Errorf("sync reply: got % X, want % X", got, want)
	}
}

func TestFakeOptiboot_FailSyncTimes(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailSyncTimes(2)
	_ = f.SetReadTimeout(20 * time.Millisecond)

	// First two syncs should produce no reply.
	for i := 0; i < 2; i++ {
		_, _ = f.Write([]byte{stkGetSync, stkCrcEop})
		buf := make([]byte, 2)
		n, _ := f.Read(buf)
		if n != 0 {
			t.Errorf("attempt %d: expected silence, got % X", i+1, buf[:n])
		}
	}
	// Third attempt should succeed.
	_, _ = f.Write([]byte{stkGetSync, stkCrcEop})
	got := readN(t, f, 2, 200*time.Millisecond)
	if !bytes.Equal(got, []byte{stkInSync, stkOK}) {
		t.Errorf("third sync reply: got % X", got)
	}
}

func readN(t *testing.T, f *ft.FakeOptiboot, n int, total time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(total)
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timeout after %v; got % X so far", total, out)
		}
		_ = f.SetReadTimeout(remaining)
		got, err := f.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		out = append(out, buf[:got]...)
	}
	return out
}
```

- [ ] **Step 7.2: Run — should fail (package missing)**

```bash
go test ./internal/flasher/testing/ -count=1 -v
```

Expected: package not found.

- [ ] **Step 7.3: Implement the fake**

Create `internal/flasher/testing/fake_optiboot.go`:

```go
// Package testing provides an in-memory STK500v1 responder that simulates
// optiboot. Used by the flasher's unit tests. It implements
// internal/serial.Port so it can be substituted for a real port.
package testing

import (
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// STK500v1 wire opcodes used by optiboot (subset).
const (
	stkGetSync        byte = 0x30
	stkGetSignOn      byte = 0x31
	stkLoadAddress    byte = 0x55
	stkProgPage       byte = 0x64
	stkReadPage       byte = 0x74
	stkChipErase      byte = 0x52
	stkLeaveProgMode  byte = 0x51

	stkCrcEop  byte = 0x20
	stkInSync  byte = 0x14
	stkOK      byte = 0x10
	stkNoSync  byte = 0x15
)

// FakeOptiboot is the in-memory bootloader. The zero value is unusable; use NewFakeOptiboot.
type FakeOptiboot struct {
	mu sync.Mutex

	flash       [avr.FlashSize]byte
	wordAddr    uint16 // STK_LOAD_ADDRESS sets this (word address = byte/2)
	rx          []byte // bytes the client has written; consumed by the responder
	tx          []byte // bytes the responder has produced; drained by Read
	readTimeout time.Duration
	rxSignal    chan struct{}
	txSignal    chan struct{}
	closed      bool

	// error-injection knobs
	failSync          int  // remaining ignored STK_GET_SYNC requests
	corruptNextRead   bool // flip a byte in the next STK_READ_PAGE reply
	corruptOffset     int  // offset into the page to flip
	dropWriteBytes    int  // number of incoming bytes to drop silently
	ackButDontPersist bool // ack the next prog_page but skip the actual write
	failChipErase     bool // respond NOSYNC to the next chip-erase
	failNextProgPage  bool // respond NOSYNC to the next prog_page
	failNextReadPage  bool // respond NOSYNC to the next read_page

	dtrSeq  []bool
	baudSeq []int
}

// NewFakeOptiboot returns a FakeOptiboot with all flash bytes 0xFF (the AVR
// erased state).
func NewFakeOptiboot() *FakeOptiboot {
	f := &FakeOptiboot{
		rxSignal:    make(chan struct{}, 1),
		txSignal:    make(chan struct{}, 1),
		readTimeout: 100 * time.Millisecond,
	}
	for i := range f.flash {
		f.flash[i] = 0xFF
	}
	return f
}

// FailSyncTimes makes the next n STK_GET_SYNC requests produce no reply.
func (f *FakeOptiboot) FailSyncTimes(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSync = n
}

// CorruptNextReadPageAt schedules a one-byte corruption of the next
// STK_READ_PAGE reply at the given offset into the page.
func (f *FakeOptiboot) CorruptNextReadPageAt(off int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.corruptNextRead = true
	f.corruptOffset = off
}

// AckButDontPersistNextProgPage makes the next STK_PROG_PAGE return OK but
// skip the in-memory write. Use to simulate "page write silently failed."
func (f *FakeOptiboot) AckButDontPersistNextProgPage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackButDontPersist = true
}

// FailNextChipErase makes the next STK_CHIP_ERASE respond NOSYNC.
func (f *FakeOptiboot) FailNextChipErase() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failChipErase = true
}

// FailNextProgPage makes the next STK_PROG_PAGE respond NOSYNC.
func (f *FakeOptiboot) FailNextProgPage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNextProgPage = true
}

// FailNextReadPage makes the next STK_READ_PAGE respond NOSYNC.
func (f *FakeOptiboot) FailNextReadPage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNextReadPage = true
}

// FlashImage returns a copy of the current flash contents.
func (f *FakeOptiboot) FlashImage() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, len(f.flash))
	copy(out, f.flash[:])
	return out
}

// PreloadFlash writes data starting at byte address 0. Test helper for
// seeding a known image before sync.
func (f *FakeOptiboot) PreloadFlash(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy(f.flash[:], data)
}

// --- serial.Port surface --------------------------------------------------

func (f *FakeOptiboot) Name() string { return "fake-optiboot" }

func (f *FakeOptiboot) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *FakeOptiboot) SetReadTimeout(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return labserial.ErrClosed
	}
	f.readTimeout = d
	return nil
}

func (f *FakeOptiboot) SetDTR(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return labserial.ErrClosed
	}
	f.dtrSeq = append(f.dtrSeq, level)
	return nil
}

func (f *FakeOptiboot) SetBaudRate(rate int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return labserial.ErrClosed
	}
	f.baudSeq = append(f.baudSeq, rate)
	return nil
}

func (f *FakeOptiboot) Drain(d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return labserial.ErrClosed
		}
		f.tx = nil
		f.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

func (f *FakeOptiboot) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, labserial.ErrClosed
	}
	in := p
	if f.dropWriteBytes > 0 {
		drop := f.dropWriteBytes
		if drop > len(in) {
			drop = len(in)
		}
		in = in[drop:]
		f.dropWriteBytes -= drop
	}
	f.rx = append(f.rx, in...)
	f.mu.Unlock()
	// Drive the responder as far as possible with what we have.
	f.processRX()
	return len(p), nil
}

func (f *FakeOptiboot) Read(p []byte) (int, error) {
	deadline := time.Now().Add(f.currentTimeout())
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, labserial.ErrClosed
		}
		if len(f.tx) > 0 {
			n := copy(p, f.tx)
			f.tx = f.tx[n:]
			f.mu.Unlock()
			return n, nil
		}
		f.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil
		}
		select {
		case <-f.txSignal:
		case <-time.After(remaining):
		}
	}
}

func (f *FakeOptiboot) currentTimeout() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readTimeout
}

// processRX consumes whole STK500v1 commands from f.rx and appends replies
// to f.tx. Caller has already pushed bytes into f.rx. Safe for concurrent
// calls because the work is done under f.mu.
func (f *FakeOptiboot) processRX() {
	for {
		f.mu.Lock()
		if len(f.rx) == 0 {
			f.mu.Unlock()
			return
		}
		// Every well-formed command ends with stkCrcEop. If the EOP isn't
		// in the buffer yet, wait for more bytes.
		eop := indexByte(f.rx, stkCrcEop)
		if eop < 0 {
			f.mu.Unlock()
			return
		}
		cmd := append([]byte(nil), f.rx[:eop+1]...)
		f.rx = f.rx[eop+1:]

		reply := f.dispatch(cmd)
		if len(reply) > 0 {
			f.tx = append(f.tx, reply...)
			select {
			case f.txSignal <- struct{}{}:
			default:
			}
		}
		f.mu.Unlock()
	}
}

func (f *FakeOptiboot) dispatch(cmd []byte) []byte {
	// cmd includes the trailing stkCrcEop.
	if len(cmd) < 2 || cmd[len(cmd)-1] != stkCrcEop {
		return []byte{stkNoSync}
	}
	op := cmd[0]
	body := cmd[1 : len(cmd)-1]

	switch op {
	case stkGetSync:
		if f.failSync > 0 {
			f.failSync--
			return nil
		}
		return []byte{stkInSync, stkOK}

	case stkGetSignOn:
		return append([]byte{stkInSync}, append([]byte("AVR ISP"), stkOK)...)

	case stkLoadAddress:
		if len(body) != 2 {
			return []byte{stkNoSync}
		}
		// STK500v1: low byte first, then high byte. Word address.
		f.wordAddr = uint16(body[0]) | uint16(body[1])<<8
		return []byte{stkInSync, stkOK}

	case stkProgPage:
		if f.failNextProgPage {
			f.failNextProgPage = false
			return []byte{stkNoSync}
		}
		// body = high-len, low-len, memtype, data...
		if len(body) < 3 {
			return []byte{stkNoSync}
		}
		n := int(body[0])<<8 | int(body[1])
		// body[2] is 'F' for flash; we ignore EEPROM in this fake.
		if len(body) < 3+n {
			return []byte{stkNoSync}
		}
		data := body[3 : 3+n]
		byteAddr := int(f.wordAddr) * 2
		if !f.ackButDontPersist {
			copy(f.flash[byteAddr:], data)
		}
		f.ackButDontPersist = false
		// Advance the word address by the count of words just written.
		f.wordAddr += uint16(n / 2)
		return []byte{stkInSync, stkOK}

	case stkReadPage:
		if f.failNextReadPage {
			f.failNextReadPage = false
			return []byte{stkNoSync}
		}
		if len(body) < 3 {
			return []byte{stkNoSync}
		}
		n := int(body[0])<<8 | int(body[1])
		byteAddr := int(f.wordAddr) * 2
		if byteAddr+n > len(f.flash) {
			return []byte{stkNoSync}
		}
		page := append([]byte(nil), f.flash[byteAddr:byteAddr+n]...)
		if f.corruptNextRead {
			if f.corruptOffset >= 0 && f.corruptOffset < len(page) {
				page[f.corruptOffset] ^= 0xFF
			}
			f.corruptNextRead = false
		}
		f.wordAddr += uint16(n / 2)
		reply := append([]byte{stkInSync}, page...)
		return append(reply, stkOK)

	case stkChipErase:
		if f.failChipErase {
			f.failChipErase = false
			return []byte{stkNoSync}
		}
		for i := range f.flash {
			f.flash[i] = 0xFF
		}
		return []byte{stkInSync, stkOK}

	case stkLeaveProgMode:
		return []byte{stkInSync, stkOK}
	}
	return []byte{stkNoSync}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 7.4: Run — should pass**

```bash
go test ./internal/flasher/testing/ -count=1 -v
```

Expected: PASS for both fake self-tests.

- [ ] **Step 7.5: Commit**

```bash
git add internal/flasher/testing/
git commit -m "feat(flasher): in-memory optiboot STK500v1 fake for tests"
```

---

## Task 8: STK500v1 client — Sync + GetSignOn

**Files:**
- Create: `internal/flasher/stk500v1.go`
- Create: `internal/flasher/stk500v1_test.go`

- [ ] **Step 8.1: Write failing tests**

Create `internal/flasher/stk500v1_test.go`:

```go
package flasher

import (
	"errors"
	"testing"
	"time"

	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
)

func TestSTK_Sync_HappyPath(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Errorf("Sync: %v", err)
	}
}

func TestSTK_Sync_RetriesThenSucceeds(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailSyncTimes(2)
	c := newSTKClient(f)
	if err := c.Sync(2 * time.Second); err != nil {
		t.Errorf("Sync after 2 ignored attempts: %v", err)
	}
}

func TestSTK_Sync_ExhaustsRetries(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailSyncTimes(100)
	c := newSTKClient(f)
	err := c.Sync(800 * time.Millisecond)
	if err == nil {
		t.Fatal("expected sync exhaustion error, got nil")
	}
	if !errors.Is(err, errBootloaderUnresponsive) {
		t.Errorf("error: got %v, want %v", err, errBootloaderUnresponsive)
	}
}

func TestSTK_GetSignOn_ReturnsVendorString(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	signOn, err := c.GetSignOn(150 * time.Millisecond)
	if err != nil {
		t.Fatalf("GetSignOn: %v", err)
	}
	if signOn == "" {
		t.Error("expected non-empty sign-on")
	}
}
```

- [ ] **Step 8.2: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestSTK_' -count=1 -v
```

Expected: undefined `newSTKClient`, `errBootloaderUnresponsive`.

- [ ] **Step 8.3: Implement**

Create `internal/flasher/stk500v1.go`:

```go
package flasher

import (
	"errors"
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// STK500v1 wire opcodes (subset used by optiboot).
const (
	stkGetSync       byte = 0x30
	stkGetSignOn     byte = 0x31
	stkLoadAddress   byte = 0x55
	stkProgPage      byte = 0x64
	stkReadPage      byte = 0x74
	stkChipErase     byte = 0x52
	stkLeaveProgMode byte = 0x51

	stkCrcEop byte = 0x20
	stkInSync byte = 0x14
	stkOK     byte = 0x10
)

const (
	bootloaderSyncRetries = 5
	syncAttemptGap        = 200 * time.Millisecond
)

var errBootloaderUnresponsive = errors.New("bootloader unresponsive")

// stkClient wraps a serial.Port with the STK500v1 transactions optiboot supports.
type stkClient struct {
	p serial.Port
}

func newSTKClient(p serial.Port) *stkClient { return &stkClient{p: p} }

// Sync waits for the bootloader to reply to STK_GET_SYNC within the total
// budget. Retries up to bootloaderSyncRetries with a fixed gap between
// attempts.
func (c *stkClient) Sync(totalBudget time.Duration) error {
	per := totalBudget / time.Duration(bootloaderSyncRetries)
	if per <= 0 {
		per = 100 * time.Millisecond
	}
	for i := 0; i < bootloaderSyncRetries; i++ {
		if err := c.p.SetReadTimeout(per); err != nil {
			return fmt.Errorf("sync: set read timeout: %w", err)
		}
		if _, err := c.p.Write([]byte{stkGetSync, stkCrcEop}); err != nil {
			return fmt.Errorf("sync: write: %w", err)
		}
		buf := make([]byte, 2)
		n, err := c.p.Read(buf)
		if err != nil {
			return fmt.Errorf("sync: read: %w", err)
		}
		if n == 2 && buf[0] == stkInSync && buf[1] == stkOK {
			return nil
		}
		time.Sleep(syncAttemptGap)
	}
	return errBootloaderUnresponsive
}

// GetSignOn returns the bootloader's vendor string. Optiboot replies
// "AVR ISP"; tolerate any vendor that returns a non-empty string between
// the INSYNC and OK markers.
func (c *stkClient) GetSignOn(timeout time.Duration) (string, error) {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return "", fmt.Errorf("sign_on: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkGetSignOn, stkCrcEop}); err != nil {
		return "", fmt.Errorf("sign_on: write: %w", err)
	}
	// Reply shape: INSYNC, <vendor bytes...>, OK
	buf := make([]byte, 64)
	out := make([]byte, 0, 16)
	seenInSync := false
	deadline := time.Now().Add(timeout)
	for {
		n, err := c.p.Read(buf)
		if err != nil {
			return "", fmt.Errorf("sign_on: read: %w", err)
		}
		out = append(out, buf[:n]...)
		if !seenInSync {
			if len(out) == 0 {
				if time.Now().After(deadline) {
					return "", errors.New("sign_on: timeout waiting for INSYNC")
				}
				continue
			}
			if out[0] != stkInSync {
				return "", fmt.Errorf("sign_on: expected INSYNC, got 0x%02X", out[0])
			}
			out = out[1:]
			seenInSync = true
		}
		// Look for OK terminator.
		for i, b := range out {
			if b == stkOK {
				return string(out[:i]), nil
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("sign_on: timeout waiting for OK")
		}
	}
}
```

- [ ] **Step 8.4: Run — should pass**

```bash
go test ./internal/flasher/ -run 'TestSTK_' -count=1 -v
```

Expected: all PASS.

- [ ] **Step 8.5: Commit**

```bash
git add internal/flasher/stk500v1.go internal/flasher/stk500v1_test.go
git commit -m "feat(flasher): STK500v1 Sync and GetSignOn"
```

---

## Task 9: STK500v1 — LoadAddress, ProgPage, ReadPage

**Files:**
- Modify: `internal/flasher/stk500v1.go`
- Modify: `internal/flasher/stk500v1_test.go`

- [ ] **Step 9.1: Write failing tests**

Append to `internal/flasher/stk500v1_test.go`:

```go
func TestSTK_LoadAddress_RoundTrip(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0040); err != nil {
		t.Errorf("LoadAddress: %v", err)
	}
}

func TestSTK_ProgPage_WritesToFakeFlash(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	page := make([]byte, 128)
	for i := range page {
		page[i] = byte(i)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0000); err != nil {
		t.Fatal(err)
	}
	if err := c.ProgPage(500*time.Millisecond, page); err != nil {
		t.Fatalf("ProgPage: %v", err)
	}
	got := f.FlashImage()[:128]
	for i, b := range got {
		if b != byte(i) {
			t.Fatalf("flash[%d]: got %02X, want %02X", i, b, byte(i))
		}
	}
}

func TestSTK_ReadPage_ReadsFakeFlash(t *testing.T) {
	f := ft.NewFakeOptiboot()
	src := make([]byte, 128)
	for i := range src {
		src[i] = byte(255 - i)
	}
	f.PreloadFlash(src)

	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0000); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadPage(500*time.Millisecond, 128)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	for i, b := range got {
		if b != src[i] {
			t.Fatalf("read[%d]: got %02X, want %02X", i, b, src[i])
		}
	}
}
```

- [ ] **Step 9.2: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestSTK_LoadAddress|TestSTK_ProgPage|TestSTK_ReadPage' -count=1 -v
```

Expected: undefined methods.

- [ ] **Step 9.3: Implement**

Append to `internal/flasher/stk500v1.go`:

```go
// LoadAddress sets the bootloader's word-address pointer for the next ProgPage / ReadPage.
// wordAddr is the byte address divided by 2 — that's the STK500v1 convention.
func (c *stkClient) LoadAddress(timeout time.Duration, wordAddr uint16) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("load_address: set read timeout: %w", err)
	}
	// LSB first then MSB.
	msg := []byte{stkLoadAddress, byte(wordAddr & 0xFF), byte(wordAddr >> 8), stkCrcEop}
	if _, err := c.p.Write(msg); err != nil {
		return fmt.Errorf("load_address: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "load_address")
}

// ProgPage writes the page (flash memory type) at the current word address.
// The bootloader advances the word address by len(page)/2 after a successful write.
func (c *stkClient) ProgPage(timeout time.Duration, page []byte) error {
	n := len(page)
	header := []byte{stkProgPage, byte(n >> 8), byte(n & 0xFF), 'F'}
	msg := make([]byte, 0, len(header)+n+1)
	msg = append(msg, header...)
	msg = append(msg, page...)
	msg = append(msg, stkCrcEop)
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("prog_page: set read timeout: %w", err)
	}
	if _, err := c.p.Write(msg); err != nil {
		return fmt.Errorf("prog_page: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "prog_page")
}

// ReadPage reads n bytes from flash at the current word address.
// The bootloader advances the word address by n/2 after a successful read.
func (c *stkClient) ReadPage(timeout time.Duration, n int) ([]byte, error) {
	msg := []byte{stkReadPage, byte(n >> 8), byte(n & 0xFF), 'F', stkCrcEop}
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return nil, fmt.Errorf("read_page: set read timeout: %w", err)
	}
	if _, err := c.p.Write(msg); err != nil {
		return nil, fmt.Errorf("read_page: write: %w", err)
	}
	// Reply: INSYNC, <n bytes>, OK
	out := make([]byte, 0, n+2)
	buf := make([]byte, 256)
	deadline := time.Now().Add(timeout)
	for len(out) < n+2 {
		got, err := c.p.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("read_page: read: %w", err)
		}
		out = append(out, buf[:got]...)
		if time.Now().After(deadline) && len(out) < n+2 {
			return nil, fmt.Errorf("read_page: timeout (got %d of %d bytes)", len(out), n+2)
		}
	}
	if out[0] != stkInSync {
		return nil, fmt.Errorf("read_page: expected INSYNC, got 0x%02X", out[0])
	}
	if out[n+1] != stkOK {
		return nil, fmt.Errorf("read_page: expected OK, got 0x%02X", out[n+1])
	}
	return out[1 : n+1], nil
}

// expectInSyncOK reads two bytes and verifies they are INSYNC and OK.
func (c *stkClient) expectInSyncOK(timeout time.Duration, op string) error {
	buf := make([]byte, 2)
	out := make([]byte, 0, 2)
	deadline := time.Now().Add(timeout)
	for len(out) < 2 {
		n, err := c.p.Read(buf[:2-len(out)])
		if err != nil {
			return fmt.Errorf("%s: read: %w", op, err)
		}
		out = append(out, buf[:n]...)
		if time.Now().After(deadline) && len(out) < 2 {
			return fmt.Errorf("%s: timeout waiting for INSYNC/OK (got %d bytes)", op, len(out))
		}
	}
	if out[0] != stkInSync {
		return fmt.Errorf("%s: expected INSYNC, got 0x%02X", op, out[0])
	}
	if out[1] != stkOK {
		return fmt.Errorf("%s: expected OK, got 0x%02X", op, out[1])
	}
	return nil
}
```

- [ ] **Step 9.4: Run — should pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS.

- [ ] **Step 9.5: Commit**

```bash
git add internal/flasher/stk500v1.go internal/flasher/stk500v1_test.go
git commit -m "feat(flasher): STK500v1 LoadAddress, ProgPage, ReadPage"
```

---

## Task 10: STK500v1 — ChipErase + LeaveProgMode

**Files:**
- Modify: `internal/flasher/stk500v1.go`
- Modify: `internal/flasher/stk500v1_test.go`

- [ ] **Step 10.1: Write failing tests**

Append to `internal/flasher/stk500v1_test.go`:

```go
func TestSTK_ChipErase_ZeroesFlash(t *testing.T) {
	f := ft.NewFakeOptiboot()
	src := []byte{0xAA, 0xBB, 0xCC}
	f.PreloadFlash(src)

	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.ChipErase(500 * time.Millisecond); err != nil {
		t.Fatalf("ChipErase: %v", err)
	}
	got := f.FlashImage()[:3]
	for i, b := range got {
		if b != 0xFF {
			t.Errorf("flash[%d] after erase: got %02X, want FF", i, b)
		}
	}
}

func TestSTK_LeaveProgMode(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LeaveProgMode(150 * time.Millisecond); err != nil {
		t.Errorf("LeaveProgMode: %v", err)
	}
}

func TestSTK_ChipErase_FailureReportsError(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailNextChipErase()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.ChipErase(150 * time.Millisecond); err == nil {
		t.Fatal("expected error from ChipErase, got nil")
	}
}
```

- [ ] **Step 10.2: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestSTK_ChipErase|TestSTK_LeaveProgMode' -count=1 -v
```

Expected: undefined methods.

- [ ] **Step 10.3: Implement**

Append to `internal/flasher/stk500v1.go`:

```go
// ChipErase clears the entire flash to 0xFF. Optiboot auto-erases per page on
// ProgPage, but we still send the explicit erase to fail-fast on a wedged chip.
func (c *stkClient) ChipErase(timeout time.Duration) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("chip_erase: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkChipErase, stkCrcEop}); err != nil {
		return fmt.Errorf("chip_erase: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "chip_erase")
}

// LeaveProgMode tells optiboot to hand control to the user sketch.
// Optiboot does NOT reset the chip; the user code starts running and the
// UART hardware switches to whatever baud the sketch configures.
func (c *stkClient) LeaveProgMode(timeout time.Duration) error {
	if err := c.p.SetReadTimeout(timeout); err != nil {
		return fmt.Errorf("leave_progmode: set read timeout: %w", err)
	}
	if _, err := c.p.Write([]byte{stkLeaveProgMode, stkCrcEop}); err != nil {
		return fmt.Errorf("leave_progmode: write: %w", err)
	}
	return c.expectInSyncOK(timeout, "leave_progmode")
}
```

- [ ] **Step 10.4: Run — should pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS.

- [ ] **Step 10.5: Commit**

```bash
git add internal/flasher/stk500v1.go internal/flasher/stk500v1_test.go
git commit -m "feat(flasher): STK500v1 ChipErase and LeaveProgMode"
```

---

## Task 11: Backup store (save, lock, prune, sha256)

**Files:**
- Create: `internal/flasher/backupstore.go`
- Create: `internal/flasher/backupstore_test.go`

- [ ] **Step 11.1: Write failing tests**

Create `internal/flasher/backupstore_test.go`:

```go
package flasher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveBackup_WritesFileAndComputesSha(t *testing.T) {
	dir := t.TempDir()
	info, err := SaveBackup(dir, "COM3", ":00000001FF\n")
	if err != nil {
		t.Fatalf("SaveBackup: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(info.Path), "COM3-") {
		t.Errorf("filename: got %q, want prefix COM3-", info.Path)
	}
	if !strings.HasSuffix(info.Path, "Z.hex") {
		t.Errorf("filename: got %q, want suffix Z.hex", info.Path)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if info.SHA256 == "" {
		t.Error("sha256 empty")
	}
	if info.SizeBytes == 0 {
		t.Error("size_bytes zero")
	}
}

func TestLockBackup_RenamesWithLockedMarker(t *testing.T) {
	dir := t.TempDir()
	info, err := SaveBackup(dir, "COM3", "data")
	if err != nil {
		t.Fatal(err)
	}
	locked, err := LockBackup(info.Path)
	if err != nil {
		t.Fatalf("LockBackup: %v", err)
	}
	if !strings.Contains(filepath.Base(locked), "-LOCKED-") {
		t.Errorf("locked name: %q must contain -LOCKED-", locked)
	}
	if _, err := os.Stat(locked); err != nil {
		t.Fatalf("locked file missing: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Errorf("original file should be gone, stat err = %v", err)
	}
}

func TestPruneBackups_KeepsNNewest(t *testing.T) {
	dir := t.TempDir()
	// Create 6 fake backups with predictable timestamps.
	for i := 0; i < 6; i++ {
		name := filepath.Join(dir, "COM3-2026-05-12T14-22-"+padTwo(i)+"Z.hex")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneBackups(dir, "COM3", 3); err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("got %d entries after prune, want 3", len(entries))
	}
	// The three kept must be the three newest (highest seconds suffix).
	for _, e := range entries {
		// We seeded seconds 00..05; the three newest are 03, 04, 05.
		suffix := e.Name()
		if !(strings.Contains(suffix, "-03Z") || strings.Contains(suffix, "-04Z") || strings.Contains(suffix, "-05Z")) {
			t.Errorf("kept unexpected file: %s", suffix)
		}
	}
}

func TestPruneBackups_SkipsLockedFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, "COM3-2026-05-12T14-22-"+padTwo(i)+"Z.hex")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Lock the oldest.
	locked := filepath.Join(dir, "COM3-LOCKED-2026-05-12T14-22-00Z.hex")
	if err := os.Rename(filepath.Join(dir, "COM3-2026-05-12T14-22-00Z.hex"), locked); err != nil {
		t.Fatal(err)
	}
	if err := PruneBackups(dir, "COM3", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(locked); err != nil {
		t.Errorf("locked file pruned: %v", err)
	}
}

func TestPruneBackups_KeepN_Zero_DoesNothing(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := filepath.Join(dir, "COM3-2026-05-12T14-22-"+padTwo(i)+"Z.hex")
		_ = os.WriteFile(name, []byte("x"), 0o644)
	}
	if err := PruneBackups(dir, "COM3", 0); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("keep_n=0 should preserve all; got %d", len(entries))
	}
}

func padTwo(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
```

- [ ] **Step 11.2: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestSaveBackup|TestLockBackup|TestPruneBackups' -count=1 -v
```

Expected: undefined symbols.

- [ ] **Step 11.3: Implement**

Create `internal/flasher/backupstore.go`:

```go
package flasher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupInfo describes a saved pre-flash flash-memory backup.
type BackupInfo struct {
	Path      string
	SHA256    string
	SizeBytes int
}

// SaveBackup writes hex content to <dir>/<port>-<ISO8601-Z>.hex and returns
// the path, sha256, and size. ISO8601 uses hyphen separators in the time
// component because colons are illegal in Windows filenames; the format
// remains lexicographically sortable.
func SaveBackup(dir, port, hexText string) (BackupInfo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BackupInfo{}, fmt.Errorf("backup: mkdir %s: %w", dir, err)
	}
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	name := fmt.Sprintf("%s-%s.hex", port, stamp)
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(hexText), 0o644); err != nil {
		return BackupInfo{}, fmt.Errorf("backup: write %s: %w", full, err)
	}
	sum := sha256.Sum256([]byte(hexText))
	return BackupInfo{
		Path:      full,
		SHA256:    hex.EncodeToString(sum[:]),
		SizeBytes: len(hexText),
	}, nil
}

// LockBackup renames a backup file to insert the -LOCKED- marker so the
// pruner will skip it indefinitely. Returns the new path.
func LockBackup(path string) (string, error) {
	dir, base := filepath.Split(path)
	idx := strings.Index(base, "-")
	if idx < 0 {
		return "", fmt.Errorf("backup: malformed filename %q", base)
	}
	locked := base[:idx] + "-LOCKED" + base[idx:]
	newPath := filepath.Join(dir, locked)
	if err := os.Rename(path, newPath); err != nil {
		return "", fmt.Errorf("backup: lock %s: %w", path, err)
	}
	return newPath, nil
}

// PruneBackups deletes all but the newest keepN files matching
// <port>-<timestamp>.hex in dir. Files containing "-LOCKED-" in the name
// are never deleted. keepN == 0 disables pruning.
func PruneBackups(dir, port string, keepN int) error {
	if keepN == 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backup: readdir %s: %w", dir, err)
	}
	prefix := port + "-"
	var candidates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, ".hex") {
			continue
		}
		if strings.Contains(name, "-LOCKED-") {
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) <= keepN {
		return nil
	}
	// Lexicographic == chronological given the ISO8601 prefix.
	sort.Strings(candidates)
	for _, name := range candidates[:len(candidates)-keepN] {
		full := filepath.Join(dir, name)
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("backup: prune %s: %w", full, err)
		}
	}
	return nil
}
```

- [ ] **Step 11.4: Run — should pass**

```bash
go test ./internal/flasher/ -run 'TestSaveBackup|TestLockBackup|TestPruneBackups' -count=1 -v
```

Expected: all PASS.

- [ ] **Step 11.5: Commit**

```bash
git add internal/flasher/backupstore.go internal/flasher/backupstore_test.go
git commit -m "feat(flasher): backup store with save/lock/prune + sha256"
```

---

## Task 12: Flasher type, Request/Result/Outcome

**Files:**
- Create: `internal/flasher/flasher.go`

This task only adds the type definitions and constructor. Behavior (`Flash`) gets added in Task 18.

- [ ] **Step 12.1: Write failing test**

Create `internal/flasher/flasher_test.go`:

```go
package flasher

import (
	"testing"
	"time"

	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func TestOutcomeString(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeSuccess:                "success",
		OutcomeRolledBackVerifyFailed: "rolled_back_verify_failed",
		OutcomeRolledBackTestFailed:   "rolled_back_test_failed",
		OutcomeFailedPreflight:        "failed_preflight",
		OutcomeFailedBackup:           "failed_backup",
		OutcomeFailedNoRecovery:       "failed_no_recovery",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Outcome %d: got %q, want %q", int(o), got, want)
		}
	}
}

func TestNewFlasher_RejectsEmptyBackupDir(t *testing.T) {
	op := labserial.NewFakeOpener()
	_, err := New(op, "", 10, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for empty backup dir, got nil")
	}
}
```

- [ ] **Step 12.2: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestOutcomeString|TestNewFlasher' -count=1 -v
```

Expected: undefined `Outcome`, `New`, etc.

- [ ] **Step 12.3: Implement**

Create `internal/flasher/flasher.go`:

```go
package flasher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// ErrBusy is returned by Flash when another Flash is already in flight.
var ErrBusy = errors.New("flasher: another flash is in flight")

// Flasher is the public interface used by internal/api. Concrete impl is
// returned by New; tests pass a stub.
type Flasher interface {
	Flash(ctx context.Context, port string, req Request) (*Result, error)
}

// Request is the input to Flash. Firmware is the parsed flash image
// (parsed from Intel HEX by the API layer before invoking Flash).
// An empty TestCommand means "skip the test phase".
type Request struct {
	Firmware         []byte
	TestCommand      []byte
	ExpectedResponse []byte
	Timeout          time.Duration
	InterByte        time.Duration
	PostOpenSettle   time.Duration
}

// Outcome is one of the six terminal states described in the spec.
type Outcome int

const (
	OutcomeSuccess Outcome = iota
	OutcomeRolledBackVerifyFailed
	OutcomeRolledBackTestFailed
	OutcomeFailedPreflight
	OutcomeFailedBackup
	OutcomeFailedNoRecovery
)

// String returns the JSON wire form.
func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeRolledBackVerifyFailed:
		return "rolled_back_verify_failed"
	case OutcomeRolledBackTestFailed:
		return "rolled_back_test_failed"
	case OutcomeFailedPreflight:
		return "failed_preflight"
	case OutcomeFailedBackup:
		return "failed_backup"
	case OutcomeFailedNoRecovery:
		return "failed_no_recovery"
	}
	return "unknown"
}

// StageResult is the per-stage record carried in Result.Stages.
type StageResult struct {
	Status              string        // "ok" | "failed" | "skipped" | "n/a"
	Duration            time.Duration
	Error               string        // non-empty when Status == "failed"
	FirstMismatchOffset *int          // verify only
	VerifyStatus        string        // rollback only: "ok" | "failed"
}

// TestResult describes the result of the post-flash test phase.
type TestResult struct {
	Sent     []byte
	Expected []byte
	Received []byte
	Match    bool
}

// Result is the output of Flash.
type Result struct {
	Outcome      Outcome
	Port         string
	Stages       map[string]StageResult
	Backup       BackupInfo
	BackupHex    string
	TestResult   *TestResult
	RecoveryHint string
}

// flasherImpl is the production implementation of Flasher.
type flasherImpl struct {
	opener         labserial.Opener
	backupDir      string
	keepN          int
	settleAfterOpen time.Duration

	mu sync.Mutex // serializes Flash invocations (single-flight)
}

// New constructs a Flasher. backupDir must be a non-empty absolute path;
// the directory is created on demand by SaveBackup. settleAfterOpen is
// the default sleep between reopening the port at 9600 and sending the
// operator's test command (matches discovery.PostOpenSettle).
func New(opener labserial.Opener, backupDir string, keepN int, settleAfterOpen time.Duration) (Flasher, error) {
	if backupDir == "" {
		return nil, fmt.Errorf("flasher: backupDir must be non-empty")
	}
	if keepN < 0 {
		return nil, fmt.Errorf("flasher: keepN must be >= 0 (got %d)", keepN)
	}
	return &flasherImpl{
		opener:          opener,
		backupDir:       backupDir,
		keepN:           keepN,
		settleAfterOpen: settleAfterOpen,
	}, nil
}
```

- [ ] **Step 12.4: Run — should pass**

```bash
go test ./internal/flasher/ -run 'TestOutcomeString|TestNewFlasher' -count=1 -v
```

Expected: all PASS.

- [ ] **Step 12.5: Commit**

```bash
git add internal/flasher/flasher.go internal/flasher/flasher_test.go
git commit -m "feat(flasher): Flasher type, Request/Result/Outcome"
```

---

## Task 13: Stages — Flash orchestrator with preflight-only path

**Files:**
- Modify: `internal/flasher/flasher.go`
- Create: `internal/flasher/stages.go`
- Create: `internal/flasher/stages_test.go`

This task builds just enough of the state machine to handle the `failed_preflight` and `ErrBusy` cases. The successful-flash, rollback, and no-recovery paths are added in Tasks 14–17.

- [ ] **Step 13.1: Write failing tests**

Create `internal/flasher/stages_test.go`:

```go
package flasher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// fakeOpenerForFlasher wires a single FakeOptiboot into a serial.Opener for tests.
type fakeOpenerForFlasher struct {
	port    string
	fake    *ft.FakeOptiboot
	openErr error
}

func (f *fakeOpenerForFlasher) Open(name string) (labserial.Port, error) {
	if name != f.port {
		return nil, errors.New("unknown port")
	}
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.fake, nil
}
func (f *fakeOpenerForFlasher) OpenWithBaud(name string, _ int) (labserial.Port, error) {
	return f.Open(name)
}
func (f *fakeOpenerForFlasher) List() ([]string, error) { return []string{f.port}, nil }
func (f *fakeOpenerForFlasher) ListDetailed() ([]labserial.DetailedPort, error) {
	return []labserial.DetailedPort{{Name: f.port, IsUSB: true}}, nil
}

func TestFlash_FailedPreflight_FirmwareTooLarge(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Firmware:       make([]byte, avr.FlashSize-avr.BootloaderSize+1),
		Timeout:        100 * time.Millisecond,
		InterByte:      25 * time.Millisecond,
		PostOpenSettle: 0,
	}
	res, err := fl.Flash(context.Background(), "COM3", req)
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.Outcome != OutcomeFailedPreflight {
		t.Errorf("Outcome: got %s, want failed_preflight", res.Outcome)
	}
	st := res.Stages["preflight"]
	if st.Status != "failed" {
		t.Errorf("preflight stage status: got %q, want failed", st.Status)
	}
}

func TestFlash_SingleFlight_SecondCallReturnsErrBusy(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Slow the first call by holding the fake's read forever.
	op.fake.FailSyncTimes(1000)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = fl.Flash(context.Background(), "COM3", Request{
			Firmware:  []byte{0x00, 0x01},
			Timeout:   50 * time.Millisecond,
			InterByte: 10 * time.Millisecond,
		})
	}()

	// Give the first call a moment to take the lock.
	time.Sleep(20 * time.Millisecond)
	_, err = fl.Flash(context.Background(), "COM3", Request{
		Firmware:  []byte{0x00, 0x01},
		Timeout:   50 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("second concurrent Flash: got err=%v, want ErrBusy", err)
	}
	wg.Wait()
}
```

- [ ] **Step 13.2: Run — should fail**

```bash
go test ./internal/flasher/ -run 'TestFlash_' -count=1 -v
```

Expected: undefined `Flash` method.

- [ ] **Step 13.3: Implement preflight stage + orchestrator skeleton**

Create `internal/flasher/stages.go`:

```go
package flasher

import (
	"context"
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
)

// runState carries mutable state between stages of a single Flash run.
type runState struct {
	port        string
	req         Request
	res         *Result
	backupBytes []byte // raw flash image read in the backup stage
}

func (s *runState) recordStage(name, status, errMsg string, dur time.Duration) {
	st := StageResult{Status: status, Duration: dur, Error: errMsg}
	s.res.Stages[name] = st
}

func (s *runState) skipDownstream(stages ...string) {
	for _, name := range stages {
		if _, ok := s.res.Stages[name]; !ok {
			s.res.Stages[name] = StageResult{Status: "skipped"}
		}
	}
}

// runPreflight validates the request shape and returns true on success.
// On failure it populates res.Outcome and marks downstream stages skipped.
func runPreflight(s *runState) bool {
	start := time.Now()
	if len(s.req.Firmware) == 0 {
		s.recordStage("preflight", "failed", "firmware empty", time.Since(start))
		s.res.Outcome = OutcomeFailedPreflight
		s.skipDownstream("backup", "erase", "program", "verify", "test", "rollback")
		return false
	}
	maxSize := avr.FlashSize - avr.BootloaderSize
	if len(s.req.Firmware) > maxSize {
		s.recordStage("preflight", "failed",
			fmt.Sprintf("firmware %d bytes exceeds user space %d", len(s.req.Firmware), maxSize),
			time.Since(start))
		s.res.Outcome = OutcomeFailedPreflight
		s.skipDownstream("backup", "erase", "program", "verify", "test", "rollback")
		return false
	}
	// Test pair asymmetry: caller must set both or neither.
	if (len(s.req.TestCommand) == 0) != (len(s.req.ExpectedResponse) == 0) {
		s.recordStage("preflight", "failed", "test_command and expected_response must both be set or both omitted", time.Since(start))
		s.res.Outcome = OutcomeFailedPreflight
		s.skipDownstream("backup", "erase", "program", "verify", "test", "rollback")
		return false
	}
	s.recordStage("preflight", "ok", "", time.Since(start))
	return true
}
```

Append to `internal/flasher/flasher.go`:

```go
// Flash runs the full state machine. Returns (nil, ErrBusy) if another
// Flash is in flight; otherwise returns a populated *Result (and nil
// error) describing every stage that ran.
func (f *flasherImpl) Flash(ctx context.Context, port string, req Request) (*Result, error) {
	if !f.mu.TryLock() {
		return nil, ErrBusy
	}
	defer f.mu.Unlock()

	s := &runState{
		port: port,
		req:  req,
		res: &Result{
			Port:   port,
			Stages: map[string]StageResult{},
		},
	}

	if !runPreflight(s) {
		return s.res, nil
	}

	// Stages 2-7 are added in later tasks. For now, success path is unreachable.
	s.res.Stages["rollback"] = StageResult{Status: "n/a"}
	s.res.Outcome = OutcomeSuccess
	return s.res, nil
}
```

Add `"context"` to the existing imports if not already present.

- [ ] **Step 13.4: Run — should pass**

```bash
go test ./internal/flasher/ -run 'TestFlash_FailedPreflight_FirmwareTooLarge|TestFlash_SingleFlight' -count=1 -v
```

Expected: PASS.

- [ ] **Step 13.5: Commit**

```bash
git add internal/flasher/flasher.go internal/flasher/stages.go internal/flasher/stages_test.go
git commit -m "feat(flasher): Flash orchestrator with preflight stage + single-flight"
```

---

## Task 14: Stages — backup + erase + program + verify + happy path

**Files:**
- Modify: `internal/flasher/flasher.go`
- Modify: `internal/flasher/stages.go`
- Modify: `internal/flasher/stages_test.go`

- [ ] **Step 14.1: Write failing tests**

Append to `internal/flasher/stages_test.go`:

```go
func TestFlash_Success_HappyPath(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	// Pre-seed a previous-firmware image so backup is non-trivial.
	prev := make([]byte, 128)
	for i := range prev {
		prev[i] = byte(i)
	}
	op.fake.PreloadFlash(prev)

	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	// New firmware: 128 bytes of byte(255-i).
	newImg := make([]byte, 128)
	for i := range newImg {
		newImg[i] = byte(255 - i)
	}

	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  newImg,
		Timeout:   500 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
		// No test pair -> test stage skipped.
	})
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome: got %s, want success", res.Outcome)
	}
	for _, name := range []string{"preflight", "backup", "erase", "program", "verify"} {
		if got := res.Stages[name].Status; got != "ok" {
			t.Errorf("stage %s: got %q, want ok", name, got)
		}
	}
	if got := res.Stages["test"].Status; got != "skipped" {
		t.Errorf("stage test: got %q, want skipped", got)
	}
	if got := res.Stages["rollback"].Status; got != "n/a" {
		t.Errorf("stage rollback: got %q, want n/a", got)
	}
	// Fake's flash should now equal newImg in the first 128 bytes.
	img := op.fake.FlashImage()
	for i := 0; i < 128; i++ {
		if img[i] != newImg[i] {
			t.Fatalf("flash[%d]: got %02X, want %02X", i, img[i], newImg[i])
		}
	}
	if res.BackupHex == "" {
		t.Error("BackupHex empty")
	}
	if res.Backup.Path == "" {
		t.Error("Backup.Path empty")
	}
}
```

- [ ] **Step 14.2: Run — should fail**

```bash
go test ./internal/flasher/ -run TestFlash_Success_HappyPath -count=1 -v
```

Expected: result outcome `success` but stages other than preflight are missing — the Task 13 skeleton returns success without doing real work.

- [ ] **Step 14.3: Implement backup/erase/program/verify stages**

Append to `internal/flasher/stages.go`:

```go
// runBackup opens the port at the bootloader baud, pulses DTR to enter
// optiboot, syncs, then page-reads the entire flash. The image bytes are
// stored on s.backupBytes for use by rollback. The image is rendered to
// Intel HEX, saved to disk, and the inline copy stored on s.res.BackupHex.
// On any failure, marks downstream stages as skipped and returns false.
//
// The returned port handle is set on s for use by subsequent stages.
func runBackup(s *runState, c *stkClient) bool {
	start := time.Now()
	img := make([]byte, avr.FlashSize)
	for off := 0; off < avr.FlashSize; off += avr.PageSize {
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			s.recordStage("backup", "failed", "load_address: "+err.Error(), time.Since(start))
			s.res.Outcome = OutcomeFailedBackup
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			return false
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			s.recordStage("backup", "failed", "read_page: "+err.Error(), time.Since(start))
			s.res.Outcome = OutcomeFailedBackup
			s.skipDownstream("erase", "program", "verify", "test", "rollback")
			return false
		}
		copy(img[off:], page)
	}
	s.backupBytes = img
	s.recordStage("backup", "ok", "", time.Since(start))
	return true
}

// runErase issues STK_CHIP_ERASE. On failure transitions into rollback.
func runErase(s *runState, c *stkClient) bool {
	start := time.Now()
	if err := c.ChipErase(s.req.Timeout); err != nil {
		s.recordStage("erase", "failed", err.Error(), time.Since(start))
		return false
	}
	s.recordStage("erase", "ok", "", time.Since(start))
	return true
}

// runProgram writes the request's firmware image one page at a time.
func runProgram(s *runState, c *stkClient) bool {
	start := time.Now()
	img := s.req.Firmware
	for off := 0; off < len(img); off += avr.PageSize {
		end := off + avr.PageSize
		if end > len(img) {
			end = len(img)
		}
		page := img[off:end]
		// Pad short final page to PageSize with 0xFF.
		if len(page) < avr.PageSize {
			padded := make([]byte, avr.PageSize)
			for i := range padded {
				padded[i] = 0xFF
			}
			copy(padded, page)
			page = padded
		}
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			s.recordStage("program", "failed", "load_address: "+err.Error(), time.Since(start))
			return false
		}
		if err := c.ProgPage(s.req.Timeout, page); err != nil {
			s.recordStage("program", "failed", "prog_page: "+err.Error(), time.Since(start))
			return false
		}
	}
	s.recordStage("program", "ok", "", time.Since(start))
	return true
}

// runVerify page-reads the programmed region and compares against the
// source image. Returns true on byte-exact match. On mismatch, populates
// the verify stage with FirstMismatchOffset and returns false.
func runVerify(s *runState, c *stkClient) bool {
	start := time.Now()
	img := s.req.Firmware
	readback := make([]byte, 0, len(img))
	for off := 0; off < len(img); off += avr.PageSize {
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			s.recordStage("verify", "failed", "load_address: "+err.Error(), time.Since(start))
			return false
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			s.recordStage("verify", "failed", "read_page: "+err.Error(), time.Since(start))
			return false
		}
		readback = append(readback, page...)
	}
	for i, b := range img {
		if readback[i] != b {
			off := i
			st := StageResult{
				Status:              "failed",
				Duration:            time.Since(start),
				Error:               fmt.Sprintf("mismatch at offset 0x%04X (got %02X, want %02X)", off, readback[i], b),
				FirstMismatchOffset: &off,
			}
			s.res.Stages["verify"] = st
			return false
		}
	}
	s.recordStage("verify", "ok", "", time.Since(start))
	return true
}
```

- [ ] **Step 14.4: Replace the `Flash` body in `flasher.go`**

In `internal/flasher/flasher.go`, replace the `Flash` method body with:

```go
func (f *flasherImpl) Flash(ctx context.Context, port string, req Request) (*Result, error) {
	if !f.mu.TryLock() {
		return nil, ErrBusy
	}
	defer f.mu.Unlock()

	s := &runState{
		port: port,
		req:  req,
		res: &Result{
			Port:   port,
			Stages: map[string]StageResult{},
		},
	}

	if !runPreflight(s) {
		return s.res, nil
	}

	// Open port at bootloader baud, pulse DTR, sync.
	p, err := f.opener.OpenWithBaud(port, avr.BootloaderBaud)
	if err != nil {
		s.res.Stages["backup"] = StageResult{Status: "failed", Error: "open: " + err.Error()}
		s.skipDownstream("erase", "program", "verify", "test", "rollback")
		s.res.Outcome = OutcomeFailedBackup
		return s.res, nil
	}
	defer func() { _ = p.Close() }()

	_ = p.SetDTR(false)
	time.Sleep(50 * time.Millisecond)
	_ = p.SetDTR(true)
	time.Sleep(50 * time.Millisecond)

	c := newSTKClient(p)
	if err := c.Sync(bootloaderSyncRetries * syncAttemptGap); err != nil {
		s.res.Stages["backup"] = StageResult{Status: "failed", Error: "sync: " + err.Error()}
		s.skipDownstream("erase", "program", "verify", "test", "rollback")
		s.res.Outcome = OutcomeFailedBackup
		return s.res, nil
	}

	if !runBackup(s, c) {
		return s.res, nil
	}

	// Save backup to disk + in-memory hex for response.
	hexText := RenderIntelHex(s.backupBytes)
	s.res.BackupHex = hexText
	info, saveErr := SaveBackup(f.backupDir, port, hexText)
	if saveErr != nil {
		// Backup read succeeded but disk write didn't. Treat as failed_backup —
		// we don't want to proceed without a recoverable backup on disk.
		st := s.res.Stages["backup"]
		st.Status = "failed"
		st.Error = "save: " + saveErr.Error()
		s.res.Stages["backup"] = st
		s.skipDownstream("erase", "program", "verify", "test", "rollback")
		s.res.Outcome = OutcomeFailedBackup
		return s.res, nil
	}
	s.res.Backup = info

	if !runErase(s, c) {
		return runRollback(s, c, p)
	}
	if !runProgram(s, c) {
		return runRollback(s, c, p)
	}
	if !runVerify(s, c) {
		return runRollback(s, c, p)
	}

	// Test phase + success transition are added in Tasks 15 + 16.
	s.res.Stages["test"] = StageResult{Status: "skipped"}
	s.res.Stages["rollback"] = StageResult{Status: "n/a"}
	s.res.Outcome = OutcomeSuccess

	_ = PruneBackups(f.backupDir, port, f.keepN)
	return s.res, nil
}
```

Also add a placeholder rollback that always returns `failed_no_recovery` so the file compiles. Append to `internal/flasher/stages.go`:

```go
// runRollback is replaced in Task 17. This stub keeps the package compiling.
func runRollback(s *runState, c *stkClient, p labserialPort) (*Result, error) {
	s.res.Stages["rollback"] = StageResult{Status: "failed", Error: "rollback not implemented yet"}
	s.res.Outcome = OutcomeFailedNoRecovery
	s.res.RecoveryHint = "rollback path not yet wired (Task 17)"
	return s.res, nil
}

// labserialPort is a local alias to avoid leaking the import in this signature.
type labserialPort = interface {
	SetDTR(bool) error
	SetBaudRate(int) error
}
```

(The temporary `labserialPort` alias is replaced with the real `serial.Port` type in Task 17.)

- [ ] **Step 14.5: Run — should pass**

```bash
go test ./internal/flasher/ -run TestFlash_Success_HappyPath -count=1 -v
```

Expected: PASS.

- [ ] **Step 14.6: Confirm prior preflight tests still pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS.

- [ ] **Step 14.7: Commit**

```bash
git add internal/flasher/flasher.go internal/flasher/stages.go internal/flasher/stages_test.go
git commit -m "feat(flasher): backup/erase/program/verify stages + happy-path Flash"
```

---

## Task 15: Stages — test phase + rolled_back_test_failed

**Files:**
- Modify: `internal/flasher/flasher.go`
- Modify: `internal/flasher/stages.go`
- Modify: `internal/flasher/stages_test.go`

The test phase: LeaveProgMode, switch baud to 9600, settle, drain, send `test_command`, read exactly `len(expected_response)` bytes, compare. On mismatch or read error → transition into rollback. (Rollback impl is still the stub from Task 14; the test phase wires the trigger.)

- [ ] **Step 15.1: Write failing tests**

Append to `internal/flasher/stages_test.go`:

```go
func TestFlash_Success_WithTestPair(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	op.fake.SetSketchResponse([]byte{0xAA, 0xBB, 0xCC})

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:         []byte{0x00, 0x01, 0x02},
		TestCommand:      []byte{0x10, 0x20},
		ExpectedResponse: []byte{0xAA, 0xBB, 0xCC},
		Timeout:          200 * time.Millisecond,
		InterByte:        20 * time.Millisecond,
		PostOpenSettle:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Errorf("Outcome: got %s, want success", res.Outcome)
	}
	if res.TestResult == nil {
		t.Fatal("TestResult nil")
	}
	if !res.TestResult.Match {
		t.Errorf("TestResult.Match: got false, want true; received=% X", res.TestResult.Received)
	}
	if res.Stages["test"].Status != "ok" {
		t.Errorf("stage test: %q", res.Stages["test"].Status)
	}
}

func TestFlash_RolledBackTestFailed_WhenMismatch(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	op.fake.SetSketchResponse([]byte{0x99}) // not the expected response

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:         []byte{0x00, 0x01, 0x02},
		TestCommand:      []byte{0x10},
		ExpectedResponse: []byte{0xAA},
		Timeout:          100 * time.Millisecond,
		InterByte:        20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Note: rollback impl is still the stub in this task; this assertion is
	// refined to OutcomeRolledBackTestFailed in Task 17 when the real
	// rollback is wired. For now we accept either failed_no_recovery (stub)
	// or the rolled-back variant.
	if res.Outcome != OutcomeRolledBackTestFailed && res.Outcome != OutcomeFailedNoRecovery {
		t.Errorf("Outcome: got %s, want rolled_back_test_failed or failed_no_recovery (stub)", res.Outcome)
	}
	if res.TestResult == nil {
		t.Fatal("TestResult nil after test_failed")
	}
	if res.TestResult.Match {
		t.Errorf("Match: got true, want false")
	}
	if len(res.TestResult.Received) == 0 || res.TestResult.Received[0] != 0x99 {
		t.Errorf("Received: got % X, want [99]", res.TestResult.Received)
	}
}
```

- [ ] **Step 15.2: Extend the fake to model a running sketch**

Append to `internal/flasher/testing/fake_optiboot.go`:

```go
// sketchResponse is the bytes the fake emits after LeaveProgMode in
// response to the operator's test_command. Used to simulate a freshly-
// flashed sketch.
type sketchMode struct {
	enabled  bool
	response []byte
}

// SetSketchResponse enables sketch mode and configures the canned reply
// the fake will emit on the first Write that arrives after the bootloader
// has left programming mode.
func (f *FakeOptiboot) SetSketchResponse(reply []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sketch.enabled = true
	f.sketch.response = append([]byte(nil), reply...)
}

// (Add this field to the FakeOptiboot struct in fake_optiboot.go:
//   sketch sketchMode
//  And modify dispatch's stkLeaveProgMode case to set f.sketch.armed = true.
//  Modify Write to short-circuit: when sketch.enabled AND we have an armed
//  reply, the next operator Write triggers the canned response, bypassing
//  the STK dispatch loop.)
```

To wire this concretely:

In `internal/flasher/testing/fake_optiboot.go`, add a field to the struct:

```go
	sketch sketchMode
	sketchArmed bool
```

Modify the `stkLeaveProgMode` case in `dispatch`:

```go
	case stkLeaveProgMode:
		f.sketchArmed = f.sketch.enabled
		return []byte{stkInSync, stkOK}
```

Modify the start of `Write`:

```go
func (f *FakeOptiboot) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, labserial.ErrClosed
	}
	if f.sketchArmed && len(p) > 0 {
		// Sketch is running; respond with the canned reply rather than
		// trying to interpret p as STK500v1.
		f.tx = append(f.tx, f.sketch.response...)
		select {
		case f.txSignal <- struct{}{}:
		default:
		}
		f.mu.Unlock()
		return len(p), nil
	}
	in := p
	// ... rest unchanged
```

- [ ] **Step 15.3: Implement the test stage**

Append to `internal/flasher/stages.go`:

```go
// runTest exits programming mode, switches the open port to TargetBaud,
// waits PostOpenSettle, drains, sends TestCommand, and reads exactly
// len(ExpectedResponse) bytes. Compares exact-match. Returns true on match,
// false on any failure (read error, mismatch, length mismatch).
func runTest(s *runState, c *stkClient, p labserialPort) bool {
	start := time.Now()
	// Skip if operator omitted the test pair.
	if len(s.req.TestCommand) == 0 {
		s.res.Stages["test"] = StageResult{Status: "skipped"}
		return true
	}

	if err := c.LeaveProgMode(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "leave_progmode: "+err.Error(), time.Since(start))
		return false
	}
	if err := p.SetBaudRate(avr.TargetBaud); err != nil {
		s.recordStage("test", "failed", "set_baud: "+err.Error(), time.Since(start))
		return false
	}
	time.Sleep(s.req.PostOpenSettle)
	if drainer, ok := p.(interface {
		Drain(time.Duration) error
	}); ok {
		_ = drainer.Drain(50 * time.Millisecond)
	}

	rw, ok := p.(interface {
		Write([]byte) (int, error)
		Read([]byte) (int, error)
		SetReadTimeout(time.Duration) error
	})
	if !ok {
		s.recordStage("test", "failed", "port does not support write+read", time.Since(start))
		return false
	}
	if _, err := rw.Write(s.req.TestCommand); err != nil {
		s.recordStage("test", "failed", "write: "+err.Error(), time.Since(start))
		return false
	}

	expected := s.req.ExpectedResponse
	received := make([]byte, 0, len(expected))
	if err := rw.SetReadTimeout(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "set_read_timeout: "+err.Error(), time.Since(start))
		return false
	}
	deadline := time.Now().Add(s.req.Timeout)
	buf := make([]byte, len(expected))
	for len(received) < len(expected) {
		n, err := rw.Read(buf[:len(expected)-len(received)])
		if err != nil {
			s.res.TestResult = &TestResult{
				Sent: s.req.TestCommand, Expected: expected, Received: received, Match: false,
			}
			s.recordStage("test", "failed", "read: "+err.Error(), time.Since(start))
			return false
		}
		received = append(received, buf[:n]...)
		if time.Now().After(deadline) {
			break
		}
	}

	match := bytesEqual(received, expected)
	s.res.TestResult = &TestResult{
		Sent: s.req.TestCommand, Expected: expected, Received: received, Match: match,
	}
	if !match {
		s.recordStage("test", "failed",
			fmt.Sprintf("test response mismatch (got %d bytes, want %d)", len(received), len(expected)),
			time.Since(start))
		return false
	}
	s.recordStage("test", "ok", "", time.Since(start))
	return true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 15.4: Wire the test stage into `Flash`**

In `internal/flasher/flasher.go`, replace the success-path tail (the lines after `if !runVerify(s, c) { ... }`) with:

```go
	if !runTest(s, c, p) {
		return runRollback(s, c, p)
	}

	s.res.Stages["rollback"] = StageResult{Status: "n/a"}
	s.res.Outcome = OutcomeSuccess

	_ = PruneBackups(f.backupDir, port, f.keepN)
	return s.res, nil
```

- [ ] **Step 15.5: Run — should pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS (including the new test-pair test cases; the mismatched-test case still falls through the rollback stub).

- [ ] **Step 15.6: Commit**

```bash
git add internal/flasher/testing/fake_optiboot.go internal/flasher/stages.go internal/flasher/flasher.go internal/flasher/stages_test.go
git commit -m "feat(flasher): post-flash test stage with exact-match"
```

---

## Task 16: Stages — real rollback path (success cases)

**Files:**
- Modify: `internal/flasher/stages.go`
- Modify: `internal/flasher/stages_test.go`

Replaces the Task-14 stub with a real rollback: erase + reprogram from `s.backupBytes` + page-read verify against `s.backupBytes`. On success, the outcome is `rolled_back_verify_failed` or `rolled_back_test_failed` depending on which upstream stage failed.

- [ ] **Step 16.1: Write failing tests**

Append to `internal/flasher/stages_test.go`:

```go
func TestFlash_RolledBackVerifyFailed(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	// Pre-seed previous firmware so backup is recoverable.
	prev := make([]byte, avr.PageSize)
	for i := range prev {
		prev[i] = 0xA5
	}
	op.fake.PreloadFlash(prev)
	// Cause the post-program readback to return wrong bytes.
	op.fake.AckButDontPersistNextProgPage()

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  make([]byte, avr.PageSize), // new image: all zeroes
		Timeout:   500 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRolledBackVerifyFailed {
		t.Fatalf("Outcome: got %s, want rolled_back_verify_failed", res.Outcome)
	}
	if res.Stages["rollback"].Status != "ok" {
		t.Errorf("rollback stage: %q", res.Stages["rollback"].Status)
	}
	if res.Stages["rollback"].VerifyStatus != "ok" {
		t.Errorf("rollback.verify_status: %q", res.Stages["rollback"].VerifyStatus)
	}
}

func TestFlash_RolledBackTestFailed_RealRollback(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	prev := make([]byte, avr.PageSize)
	for i := range prev {
		prev[i] = 0x5A
	}
	op.fake.PreloadFlash(prev)
	op.fake.SetSketchResponse([]byte{0x99}) // wrong test response

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:         make([]byte, avr.PageSize),
		TestCommand:      []byte{0x10},
		ExpectedResponse: []byte{0xAA},
		Timeout:          200 * time.Millisecond,
		InterByte:        20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRolledBackTestFailed {
		t.Fatalf("Outcome: got %s, want rolled_back_test_failed", res.Outcome)
	}
	if res.Stages["rollback"].Status != "ok" {
		t.Errorf("rollback stage: %q", res.Stages["rollback"].Status)
	}
}
```

- [ ] **Step 16.2: Implement real rollback**

Replace the placeholder `runRollback` and `labserialPort` alias in `internal/flasher/stages.go` with:

```go
import (
	// existing imports...
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// runRollback re-flashes the device with the backup image read in stage 2
// and verifies the rollback by reading the flash back. On success returns
// (res, nil) with outcome rolled_back_verify_failed OR rolled_back_test_failed
// depending on which upstream stage triggered the rollback. On failure of any
// step inside rollback, outcome is failed_no_recovery and the backup file
// is locked (renamed with -LOCKED-).
func runRollback(s *runState, c *stkClient, p labserial.Port) (*Result, error) {
	start := time.Now()
	st := StageResult{Status: "ok", VerifyStatus: "ok"}

	// Determine which upstream stage triggered us. Inspect the recorded
	// stages: first "failed" past preflight wins.
	trigger := "verify"
	for _, name := range []string{"erase", "program", "verify", "test"} {
		if r, ok := s.res.Stages[name]; ok && r.Status == "failed" {
			trigger = name
			break
		}
	}

	if err := c.ChipErase(s.req.Timeout); err != nil {
		return rollbackFailed(s, st, start, "chip_erase: "+err.Error())
	}
	// Reprogram from backupBytes (use only the user-space subset to keep parity
	// with what we read out during backup — bootloader region is read-only via
	// optiboot anyway and would be rejected).
	prog := s.backupBytes
	for off := 0; off < len(prog); off += avr.PageSize {
		end := off + avr.PageSize
		if end > len(prog) {
			end = len(prog)
		}
		page := prog[off:end]
		if len(page) < avr.PageSize {
			padded := make([]byte, avr.PageSize)
			for i := range padded {
				padded[i] = 0xFF
			}
			copy(padded, page)
			page = padded
		}
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			return rollbackFailed(s, st, start, "load_address: "+err.Error())
		}
		if err := c.ProgPage(s.req.Timeout, page); err != nil {
			return rollbackFailed(s, st, start, "prog_page: "+err.Error())
		}
	}
	// Verify the rollback by reading back the entire image and comparing to backupBytes.
	for off := 0; off < len(prog); off += avr.PageSize {
		if err := c.LoadAddress(s.req.Timeout, uint16(off/2)); err != nil {
			return rollbackFailed(s, st, start, "verify load_address: "+err.Error())
		}
		page, err := c.ReadPage(s.req.Timeout, avr.PageSize)
		if err != nil {
			return rollbackFailed(s, st, start, "verify read_page: "+err.Error())
		}
		end := off + avr.PageSize
		if end > len(prog) {
			end = len(prog)
		}
		for i := off; i < end; i++ {
			if page[i-off] != prog[i] {
				st.VerifyStatus = "failed"
				return rollbackFailed(s, st, start,
					fmt.Sprintf("verify mismatch at 0x%04X (got %02X, want %02X)", i, page[i-off], prog[i]))
			}
		}
	}

	st.Duration = time.Since(start)
	s.res.Stages["rollback"] = st
	switch trigger {
	case "test":
		s.res.Outcome = OutcomeRolledBackTestFailed
	default:
		s.res.Outcome = OutcomeRolledBackVerifyFailed
	}
	return s.res, nil
}

func rollbackFailed(s *runState, st StageResult, start time.Time, errMsg string) (*Result, error) {
	st.Status = "failed"
	st.Error = errMsg
	st.Duration = time.Since(start)
	s.res.Stages["rollback"] = st
	s.res.Outcome = OutcomeFailedNoRecovery
	if s.res.Backup.Path != "" {
		locked, err := LockBackup(s.res.Backup.Path)
		if err == nil {
			s.res.Backup.Path = locked
		}
		s.res.RecoveryHint = fmt.Sprintf(
			"Rollback failed: %s. The device may need ISP-level recovery (e.g. AVRISP mkII). The saved backup at %s is the last known good image.",
			errMsg, s.res.Backup.Path)
	} else {
		s.res.RecoveryHint = "Rollback failed: " + errMsg
	}
	return s.res, nil
}
```

Drop the `labserialPort` type alias — it's no longer needed.

Also update the signature of `runTest` to take `labserial.Port` directly, replacing the type-alias-based interface assertions. The body's interface-cast logic can be simplified now that `p` is concretely `serial.Port`:

```go
func runTest(s *runState, c *stkClient, p labserial.Port) bool {
	start := time.Now()
	if len(s.req.TestCommand) == 0 {
		s.res.Stages["test"] = StageResult{Status: "skipped"}
		return true
	}
	if err := c.LeaveProgMode(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "leave_progmode: "+err.Error(), time.Since(start))
		return false
	}
	if err := p.SetBaudRate(avr.TargetBaud); err != nil {
		s.recordStage("test", "failed", "set_baud: "+err.Error(), time.Since(start))
		return false
	}
	time.Sleep(s.req.PostOpenSettle)
	_ = p.Drain(50 * time.Millisecond)

	if _, err := p.Write(s.req.TestCommand); err != nil {
		s.recordStage("test", "failed", "write: "+err.Error(), time.Since(start))
		return false
	}

	expected := s.req.ExpectedResponse
	received := make([]byte, 0, len(expected))
	if err := p.SetReadTimeout(s.req.Timeout); err != nil {
		s.recordStage("test", "failed", "set_read_timeout: "+err.Error(), time.Since(start))
		return false
	}
	deadline := time.Now().Add(s.req.Timeout)
	buf := make([]byte, len(expected))
	for len(received) < len(expected) {
		n, err := p.Read(buf[:len(expected)-len(received)])
		if err != nil {
			s.res.TestResult = &TestResult{
				Sent: s.req.TestCommand, Expected: expected, Received: received, Match: false,
			}
			s.recordStage("test", "failed", "read: "+err.Error(), time.Since(start))
			return false
		}
		received = append(received, buf[:n]...)
		if time.Now().After(deadline) {
			break
		}
	}

	match := bytesEqual(received, expected)
	s.res.TestResult = &TestResult{
		Sent: s.req.TestCommand, Expected: expected, Received: received, Match: match,
	}
	if !match {
		s.recordStage("test", "failed",
			fmt.Sprintf("test response mismatch (got %d bytes, want %d)", len(received), len(expected)),
			time.Since(start))
		return false
	}
	s.recordStage("test", "ok", "", time.Since(start))
	return true
}
```

- [ ] **Step 16.3: Run — should pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS.

- [ ] **Step 16.4: Commit**

```bash
git add internal/flasher/stages.go internal/flasher/stages_test.go
git commit -m "feat(flasher): real rollback with verify; rolled_back_* outcomes"
```

---

## Task 17: Stages — failed_no_recovery cases + failed_backup cases

**Files:**
- Modify: `internal/flasher/stages_test.go`

This task only adds tests — the no-recovery code paths are already implemented (Task 16's `rollbackFailed` covers them; Task 14's backup-fail-on-sync paths set `OutcomeFailedBackup`).

- [ ] **Step 17.1: Write failing tests**

Append to `internal/flasher/stages_test.go`:

```go
func TestFlash_FailedNoRecovery_RollbackChipEraseFails(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	op.fake.AckButDontPersistNextProgPage() // triggers rollback
	op.fake.FailNextChipErase()             // first erase is during program phase…
	// Re-arm chip-erase failure to fire during rollback specifically: we need
	// the first erase (programming) to succeed and the rollback erase to fail.
	// AckButDontPersist is a programming-side failure, so the program-stage
	// erase has already happened. After verify fails, rollback calls erase
	// again -> hit FailNextChipErase.

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  make([]byte, avr.PageSize),
		Timeout:   500 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFailedNoRecovery {
		t.Errorf("Outcome: got %s, want failed_no_recovery", res.Outcome)
	}
	if res.RecoveryHint == "" {
		t.Error("RecoveryHint empty for failed_no_recovery")
	}
	// Backup file should have been locked.
	if res.Backup.Path == "" {
		t.Fatal("Backup.Path empty")
	}
	if !contains(res.Backup.Path, "-LOCKED-") {
		t.Errorf("backup path %q should contain -LOCKED-", res.Backup.Path)
	}
}

func TestFlash_FailedBackup_SyncTimeout(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	op.fake.FailSyncTimes(1000)

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  []byte{0x00, 0x01, 0x02},
		Timeout:   100 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFailedBackup {
		t.Errorf("Outcome: got %s, want failed_backup", res.Outcome)
	}
}

func TestFlash_BackupPruning_KeepsN(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	dir := t.TempDir()
	fl, _ := New(op, dir, 3, 0)

	for i := 0; i < 5; i++ {
		// Allow the timestamp to tick between runs so filenames sort distinctly.
		time.Sleep(1100 * time.Millisecond)
		op.fake = ft.NewFakeOptiboot() // reset fake between flashes
		_, err := fl.Flash(context.Background(), "COM3", Request{
			Firmware:  []byte{byte(i)},
			Timeout:   500 * time.Millisecond,
			InterByte: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Flash %d: %v", i, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("after 5 flashes with keep_n=3: got %d files, want 3", len(entries))
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

Add `"os"` to the test file's imports if missing.

- [ ] **Step 17.2: Run — should pass**

```bash
go test ./internal/flasher/ -count=1
```

Expected: all PASS. (Note: the pruning test takes ~5 s because of the 1-second sleeps to advance the timestamp granularity.)

- [ ] **Step 17.3: Commit**

```bash
git add internal/flasher/stages_test.go
git commit -m "test(flasher): failed_no_recovery, failed_backup, pruning"
```

---

## Task 18: API DTOs

**Files:**
- Modify: `internal/api/types.go`

- [ ] **Step 18.1: Append DTOs**

Append to `internal/api/types.go`:

```go
type DetailedPortDTO struct {
	Name         string `json:"name"`
	IsUSB        bool   `json:"is_usb"`
	VID          string `json:"vid"`
	PID          string `json:"pid"`
	SerialNumber string `json:"serial_number"`
	Product      string `json:"product"`
	Discovered   bool   `json:"discovered"`
	DeviceID     string `json:"device_id,omitempty"`
}

type DetailedPortsResponse struct {
	Ports []DetailedPortDTO `json:"ports"`
}

type DisconnectResponse struct {
	Released int `json:"released"`
}

type FlashRequest struct {
	Firmware         string `json:"firmware"`
	TestCommand      string `json:"test_command,omitempty"`
	ExpectedResponse string `json:"expected_response,omitempty"`
	TimeoutMs        *int   `json:"timeout_ms,omitempty"`
	InterByteMs      *int   `json:"inter_byte_ms,omitempty"`
	PostOpenSettleMs *int   `json:"post_open_settle_ms,omitempty"`
}

type StageDTO struct {
	Status              string `json:"status"`
	DurationMs          int64  `json:"duration_ms,omitempty"`
	Error               string `json:"error,omitempty"`
	FirstMismatchOffset string `json:"first_mismatch_offset,omitempty"`
	VerifyStatus        string `json:"verify_status,omitempty"`
}

type BackupDTO struct {
	Hex       string `json:"hex"`
	SavedPath string `json:"saved_path"`
	SHA256    string `json:"sha256"`
	SizeBytes int    `json:"size_bytes"`
	Scope     string `json:"scope"`
}

type TestResultDTO struct {
	Sent     string `json:"sent"`
	Expected string `json:"expected"`
	Received string `json:"received"`
	Match    bool   `json:"match"`
}

type FlashResponse struct {
	Outcome      string              `json:"outcome"`
	Port         string              `json:"port"`
	Stages       map[string]StageDTO `json:"stages"`
	Backup       BackupDTO           `json:"backup"`
	TestResult   *TestResultDTO      `json:"test_result,omitempty"`
	RecoveryHint string              `json:"recovery_hint,omitempty"`
}
```

- [ ] **Step 18.2: Build**

```bash
go build ./internal/api/...
```

Expected: builds clean.

- [ ] **Step 18.3: Commit**

```bash
git add internal/api/types.go
git commit -m "feat(api): DTOs for flash, disconnect, detailed ports"
```

---

## Task 19: API handler — `POST /devices/disconnect`

**Files:**
- Modify: `internal/api/handlers.go`
- Create: `internal/api/flash.go`
- Create: `internal/api/flash_test.go`

Wire only the disconnect handler in this task. The flash handler and detailed-ports handler come in Tasks 20 and 21.

- [ ] **Step 19.1: Update `Server` struct + `New(...)` signature**

In `internal/api/handlers.go`, replace the `Server` struct and `New` function with:

```go
type Server struct {
	reg              *registry.Registry
	discover         DiscoverFn
	opener           labserial.Opener
	rawSerialEnabled bool
	flasher          flasher.Flasher
	flashingEnabled  bool
}

func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	rawSerialEnabled bool,
	fl flasher.Flasher,
	flashingEnabled bool,
) *Server {
	return &Server{
		reg:              reg,
		discover:         discover,
		opener:           opener,
		rawSerialEnabled: rawSerialEnabled,
		flasher:          fl,
		flashingEnabled:  flashingEnabled,
	}
}
```

Add the import:

```go
"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
```

- [ ] **Step 19.2: Fix call sites**

```bash
grep -rn "api.New(" --include="*.go"
```

Update each call site to pass `nil` for `fl` and `false` for `flashingEnabled` (these don't exercise flashing). The known sites:

- `internal/app/app.go` line ~58: change `api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled)` to `api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, nil, false)`. (Task 22 replaces `nil` with a real flasher.)
- `internal/api/handlers_test.go`: any `newTestServer` helper. Change to pass `nil, false` (or accept new params).
- `internal/api/raw_serial_test.go`: similarly.

- [ ] **Step 19.3: Write failing handler test**

Create `internal/api/flash_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func newTestServerForFlash(t *testing.T) (*Server, *registry.Registry, *labserial.FakeOpener) {
	t.Helper()
	reg := registry.New()
	op := labserial.NewFakeOpener()
	s := New(reg, nil, op, true, nil, false)
	return s, reg, op
}

func TestDisconnect_EmptyRegistry(t *testing.T) {
	s, _, _ := newTestServerForFlash(t)
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"released":0`) {
		t.Errorf("body: got %q, want released:0", rr.Body.String())
	}
}

func TestDisconnect_PopulatedRegistry(t *testing.T) {
	s, reg, _ := newTestServerForFlash(t)
	reg.Replace([]*registry.Device{
		{ID: "a", Type: "pump", TypeCode: 10, Port: "COM3", Conn: labserial.NewFakePort("COM3")},
		{ID: "b", Type: "valve", TypeCode: 30, Port: "COM4", Conn: labserial.NewFakePort("COM4")},
	})
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"released":2`) {
		t.Errorf("body: %q", rr.Body.String())
	}
	if len(reg.List()) != 0 {
		t.Errorf("registry not empty after disconnect")
	}
}
```

- [ ] **Step 19.4: Run — should fail**

```bash
go test ./internal/api/ -run TestDisconnect -count=1 -v
```

Expected: 404 (route not registered yet).

- [ ] **Step 19.5: Implement the handler**

Create `internal/api/flash.go`:

```go
package api

import (
	"log/slog"
	"net/http"
)

func (s *Server) handlePostDevicesDisconnect(w http.ResponseWriter, r *http.Request) {
	n := s.reg.DisconnectAll()
	slog.Info("disconnect", "released", n)
	writeJSON(w, http.StatusOK, DisconnectResponse{Released: n})
}
```

Register in `internal/api/server.go`, inside `Handler()`:

```go
	mux.HandleFunc("POST /devices/disconnect", s.handlePostDevicesDisconnect)
```

- [ ] **Step 19.6: Run — should pass**

```bash
go test ./internal/api/ -count=1
```

Expected: all PASS.

- [ ] **Step 19.7: Commit**

```bash
git add internal/api/handlers.go internal/api/server.go internal/api/flash.go internal/api/flash_test.go internal/api/handlers_test.go internal/api/raw_serial_test.go internal/app/app.go
git commit -m "feat(api): POST /devices/disconnect + Flasher field on Server"
```

---

## Task 20: API handler — `GET /serial/ports/detailed`

**Files:**
- Modify: `internal/api/flash.go`
- Modify: `internal/api/flash_test.go`
- Modify: `internal/api/server.go`

- [ ] **Step 20.1: Write failing test**

Append to `internal/api/flash_test.go`:

```go
func TestDetailedPorts_ReturnsAnnotatedPorts(t *testing.T) {
	s, reg, op := newTestServerForFlash(t)
	op.Add(labserial.NewFakePort("COM3"))
	op.Add(labserial.NewFakePort("COM4"))
	op.SetDetail("COM3", labserial.DetailedPort{
		Name: "COM3", IsUSB: true, VID: "2341", PID: "0043", Product: "Arduino Uno",
	})
	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: labserial.NewFakePort("COM3")},
	})

	req := httptest.NewRequest(http.MethodGet, "/serial/ports/detailed", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"COM3"`) {
		t.Errorf("missing COM3 in body: %s", body)
	}
	if !strings.Contains(body, `"name":"COM4"`) {
		t.Errorf("missing COM4 in body: %s", body)
	}
	if !strings.Contains(body, `"discovered":true`) {
		t.Errorf("expected discovered:true for COM3: %s", body)
	}
	if !strings.Contains(body, `"device_id":"pump_1"`) {
		t.Errorf("expected device_id pump_1: %s", body)
	}
}
```

- [ ] **Step 20.2: Run — should fail**

```bash
go test ./internal/api/ -run TestDetailedPorts -count=1 -v
```

Expected: 404.

- [ ] **Step 20.3: Implement**

Append to `internal/api/flash.go`:

```go
import (
	"sort"
)

func (s *Server) handleGetSerialPortsDetailed(w http.ResponseWriter, r *http.Request) {
	ports, err := s.opener.ListDetailed()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	out := make([]DetailedPortDTO, 0, len(ports))
	for _, p := range ports {
		dto := DetailedPortDTO{
			Name:         p.Name,
			IsUSB:        p.IsUSB,
			VID:          p.VID,
			PID:          p.PID,
			SerialNumber: p.SerialNumber,
			Product:      p.Product,
		}
		if id, ok := s.reg.HasPort(p.Name); ok {
			dto.Discovered = true
			dto.DeviceID = id
		}
		out = append(out, dto)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, DetailedPortsResponse{Ports: out})
}
```

Merge the `sort` import into the existing import block at the top of the file.

Register in `internal/api/server.go`, inside `Handler()`:

```go
	mux.HandleFunc("GET /serial/ports/detailed", s.handleGetSerialPortsDetailed)
```

- [ ] **Step 20.4: Run — should pass**

```bash
go test ./internal/api/ -count=1
```

Expected: all PASS.

- [ ] **Step 20.5: Commit**

```bash
git add internal/api/flash.go internal/api/flash_test.go internal/api/server.go
git commit -m "feat(api): GET /serial/ports/detailed"
```

---

## Task 21: API handler — `POST /flash/{port}`

**Files:**
- Modify: `internal/api/flash.go`
- Modify: `internal/api/flash_test.go`
- Modify: `internal/api/server.go`

This is the biggest handler. Split steps into two sub-tasks for sanity: preflight rejections (21A) and the happy/result-mapping path (21B).

### Task 21A: preflight rejections

- [ ] **Step 21A.1: Write failing tests**

Append to `internal/api/flash_test.go`:

```go
import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
)

// stubFlasher records the latest Flash call and returns a canned Result.
type stubFlasher struct {
	res  *flasher.Result
	err  error
	last struct {
		Port string
		Req  flasher.Request
	}
}

func (s *stubFlasher) Flash(_ context.Context, port string, req flasher.Request) (*flasher.Result, error) {
	s.last.Port = port
	s.last.Req = req
	return s.res, s.err
}

func newTestServerWithFlash(t *testing.T, fl flasher.Flasher, enabled bool) (*Server, *registry.Registry, *labserial.FakeOpener) {
	t.Helper()
	reg := registry.New()
	op := labserial.NewFakeOpener()
	s := New(reg, nil, op, true, fl, enabled)
	return s, reg, op
}

func TestFlash_403_FlashingDisabled(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, false)
	op.Add(labserial.NewFakePort("COM3"))
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
}

func TestFlash_404_UnknownPort(t *testing.T) {
	s, _, _ := newTestServerWithFlash(t, &stubFlasher{}, true)
	req := httptest.NewRequest(http.MethodPost, "/flash/COMNOPE", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_409_RegistryNotEmpty(t *testing.T) {
	s, reg, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	reg.Replace([]*registry.Device{
		{ID: "x", Type: "pump", TypeCode: 10, Port: "COM3", Conn: labserial.NewFakePort("COM3")},
	})
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/devices/disconnect") {
		t.Errorf("expected hint about /devices/disconnect in body: %s", rr.Body.String())
	}
}

func TestFlash_409_DiscoveryInProgress(t *testing.T) {
	s, reg, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	if !reg.LockDiscovery() {
		t.Fatal("could not acquire discovery gate")
	}
	defer reg.UnlockDiscovery()

	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_400_BadJSON(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`not json`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_400_TestPairAsymmetric(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","test_command":"010203"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "both or neither") {
		t.Errorf("expected 'both or neither' in body: %s", rr.Body.String())
	}
}

func TestFlash_400_BadHex(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","test_command":"GGGG","expected_response":"AABB"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestFlash_409_FlashInFlight(t *testing.T) {
	stub := &stubFlasher{err: flasher.ErrBusy}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":0100000000FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d", rr.Code)
	}
}
```

Add `"context"` and the flasher import to the existing imports at the top.

- [ ] **Step 21A.2: Run — should fail**

```bash
go test ./internal/api/ -run 'TestFlash_' -count=1 -v
```

Expected: 404s from missing route.

- [ ] **Step 21A.3: Implement preflight branches**

Append to `internal/api/flash.go`:

```go
import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
)

const maxFlashBodyBytes = 256 * 1024

func (s *Server) handlePostFlashPort(w http.ResponseWriter, r *http.Request) {
	if !s.flashingEnabled {
		writeError(w, http.StatusForbidden, "flashing disabled", "set flashing.enabled: true in config")
		return
	}
	port := r.PathValue("port")

	r.Body = http.MaxBytesReader(w, r.Body, maxFlashBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body FlashRequest
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Firmware == "" {
		writeError(w, http.StatusBadRequest, "invalid request body", "firmware: required")
		return
	}
	if (body.TestCommand == "") != (body.ExpectedResponse == "") {
		writeError(w, http.StatusBadRequest, "invalid request body",
			"test_command and expected_response must both be set or both omitted")
		return
	}
	testCmd, err := decodeHexField("test_command", body.TestCommand)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	expected, err := decodeHexField("expected_response", body.ExpectedResponse)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	firmware, err := flasher.ParseIntelHex([]byte(body.Firmware))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "firmware: "+err.Error())
		return
	}
	timeoutMs := 100
	if body.TimeoutMs != nil {
		if *body.TimeoutMs < 1 || *body.TimeoutMs > 60000 {
			writeError(w, http.StatusBadRequest, "invalid request body",
				fmt.Sprintf("timeout_ms must be 1..60000 (got %d)", *body.TimeoutMs))
			return
		}
		timeoutMs = *body.TimeoutMs
	}
	interByteMs := 25
	if body.InterByteMs != nil {
		if *body.InterByteMs < 1 || *body.InterByteMs > 1000 {
			writeError(w, http.StatusBadRequest, "invalid request body",
				fmt.Sprintf("inter_byte_ms must be 1..1000 (got %d)", *body.InterByteMs))
			return
		}
		interByteMs = *body.InterByteMs
	}
	settleMs := 2000
	if body.PostOpenSettleMs != nil {
		if *body.PostOpenSettleMs < 0 || *body.PostOpenSettleMs > 60000 {
			writeError(w, http.StatusBadRequest, "invalid request body",
				fmt.Sprintf("post_open_settle_ms must be 0..60000 (got %d)", *body.PostOpenSettleMs))
			return
		}
		settleMs = *body.PostOpenSettleMs
	}

	// Port presence
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
	if len(s.reg.List()) > 0 {
		writeError(w, http.StatusConflict, "registry not empty", "POST /devices/disconnect first")
		return
	}
	if s.reg.IsDiscovering() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}

	res, err := s.flasher.Flash(r.Context(), port, flasher.Request{
		Firmware:         firmware,
		TestCommand:      testCmd,
		ExpectedResponse: expected,
		Timeout:          time.Duration(timeoutMs) * time.Millisecond,
		InterByte:        time.Duration(interByteMs) * time.Millisecond,
		PostOpenSettle:   time.Duration(settleMs) * time.Millisecond,
	})
	if err != nil {
		if errors.Is(err, flasher.ErrBusy) {
			writeError(w, http.StatusConflict, "flash in flight", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "flash failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mapFlashResult(res, port))
}

func decodeHexField(name, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	out, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return out, nil
}
```

Add a placeholder `mapFlashResult` (real version comes in Task 21B):

```go
func mapFlashResult(res *flasher.Result, port string) FlashResponse {
	return FlashResponse{
		Outcome: res.Outcome.String(),
		Port:    port,
	}
}
```

Register the route in `internal/api/server.go`, inside `Handler()`:

```go
	mux.HandleFunc("POST /flash/{port}", s.handlePostFlashPort)
```

- [ ] **Step 21A.4: Run — should pass**

```bash
go test ./internal/api/ -run 'TestFlash_' -count=1 -v
```

Expected: all PASS.

- [ ] **Step 21A.5: Commit**

```bash
git add internal/api/flash.go internal/api/flash_test.go internal/api/server.go
git commit -m "feat(api): POST /flash/{port} preflight + body validation"
```

### Task 21B: result-to-response mapping

- [ ] **Step 21B.1: Write failing tests**

Append to `internal/api/flash_test.go`:

```go
func TestFlash_200_SuccessShape(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome: flasher.OutcomeSuccess,
			Port:    "COM3",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok", Duration: 12 * time.Millisecond},
				"backup":    {Status: "ok", Duration: 8000 * time.Millisecond},
				"erase":     {Status: "ok", Duration: 90 * time.Millisecond},
				"program":   {Status: "ok", Duration: 7900 * time.Millisecond},
				"verify":    {Status: "ok", Duration: 3100 * time.Millisecond},
				"test":      {Status: "skipped"},
				"rollback":  {Status: "n/a"},
			},
			Backup:    flasher.BackupInfo{Path: "/tmp/x.hex", SHA256: "abc", SizeBytes: 32},
			BackupHex: ":00000001FF\n",
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"success"`,
		`"port":"COM3"`,
		`"hex":":00000001FF\n"`,
		`"sha256":"abc"`,
		`"scope":"flash_only"`,
		`"status":"ok"`,
		`"status":"skipped"`,
		`"status":"n/a"`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
	if strings.Contains(body2, `"test_result"`) {
		t.Errorf("test_result must be omitted when nil: %s", body2)
	}
}

func TestFlash_200_RolledBackShape_WithTestResult(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome: flasher.OutcomeRolledBackTestFailed,
			Port:    "COM3",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok"},
				"backup":    {Status: "ok"},
				"erase":     {Status: "ok"},
				"program":   {Status: "ok"},
				"verify":    {Status: "ok"},
				"test":      {Status: "failed", Error: "mismatch"},
				"rollback":  {Status: "ok", VerifyStatus: "ok"},
			},
			Backup:    flasher.BackupInfo{Path: "/tmp/x.hex"},
			BackupHex: ":00000001FF\n",
			TestResult: &flasher.TestResult{
				Sent: []byte{0x01}, Expected: []byte{0xAA}, Received: []byte{0xBB}, Match: false,
			},
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","test_command":"01","expected_response":"AA"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"rolled_back_test_failed"`,
		`"sent":"01"`,
		`"expected":"AA"`,
		`"received":"BB"`,
		`"match":false`,
		`"verify_status":"ok"`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
}

func TestFlash_200_FailedNoRecoveryShape(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome:      flasher.OutcomeFailedNoRecovery,
			Port:         "COM3",
			RecoveryHint: "ISP recovery required",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok"},
				"backup":    {Status: "ok"},
				"erase":     {Status: "ok"},
				"program":   {Status: "ok"},
				"verify":    {Status: "failed"},
				"test":      {Status: "skipped"},
				"rollback":  {Status: "failed", Error: "chip erase: NOSYNC"},
			},
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"failed_no_recovery"`,
		`"recovery_hint":"ISP recovery required"`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
}
```

- [ ] **Step 21B.2: Run — should fail**

```bash
go test ./internal/api/ -run 'TestFlash_200_' -count=1 -v
```

Expected: failures because `mapFlashResult` is currently the placeholder.

- [ ] **Step 21B.3: Implement the real `mapFlashResult`**

Replace `mapFlashResult` in `internal/api/flash.go` with:

```go
func mapFlashResult(res *flasher.Result, port string) FlashResponse {
	out := FlashResponse{
		Outcome:      res.Outcome.String(),
		Port:         port,
		Stages:       map[string]StageDTO{},
		RecoveryHint: res.RecoveryHint,
	}
	for name, st := range res.Stages {
		dto := StageDTO{
			Status:     st.Status,
			DurationMs: st.Duration.Milliseconds(),
			Error:      st.Error,
		}
		if st.FirstMismatchOffset != nil {
			dto.FirstMismatchOffset = fmt.Sprintf("0x%04X", *st.FirstMismatchOffset)
		}
		if st.VerifyStatus != "" {
			dto.VerifyStatus = st.VerifyStatus
		}
		out.Stages[name] = dto
	}
	out.Backup = BackupDTO{
		Hex:       res.BackupHex,
		SavedPath: res.Backup.Path,
		SHA256:    res.Backup.SHA256,
		SizeBytes: res.Backup.SizeBytes,
		Scope:     "flash_only",
	}
	if res.TestResult != nil {
		out.TestResult = &TestResultDTO{
			Sent:     hex.EncodeToString(res.TestResult.Sent),
			Expected: hex.EncodeToString(res.TestResult.Expected),
			Received: hex.EncodeToString(res.TestResult.Received),
			Match:    res.TestResult.Match,
		}
	}
	return out
}
```

The hex encoder emits lowercase by default; that's fine for the wire — tests assert exact substrings that we control.

- [ ] **Step 21B.4: Run — should pass**

```bash
go test ./internal/api/ -count=1
```

Expected: all PASS.

- [ ] **Step 21B.5: Commit**

```bash
git add internal/api/flash.go internal/api/flash_test.go
git commit -m "feat(api): map flasher.Result to FlashResponse JSON"
```

---

## Task 22: Wire flasher in `internal/app/app.go`

**Files:**
- Modify: `internal/app/app.go`

- [ ] **Step 22.1: Construct flasher in `Run`**

In `internal/app/app.go`, replace the line:

```go
	srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, nil, false)
```

with:

```go
	backupDir := cfg.Flashing.BackupDir
	if backupDir == "" {
		backupDir = paths.BackupsDir()
	}
	if backupDir == "" {
		slog.Warn("flashing: no backup dir available; flashing forced off")
	}
	var fl flasher.Flasher
	if backupDir != "" {
		var err error
		fl, err = flasher.New(opener, backupDir, cfg.Flashing.KeepN, discovery.PostOpenSettle)
		if err != nil {
			return fmt.Errorf("flasher init: %w", err)
		}
	}
	flashingEnabled := cfg.Flashing.Enabled && fl != nil
	srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, fl, flashingEnabled)
```

Add the imports:

```go
"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
```

- [ ] **Step 22.2: Build and run all tests**

```bash
go build ./...
go test -count=1 ./...
```

Expected: everything PASS.

- [ ] **Step 22.3: Commit**

```bash
git add internal/app/app.go
git commit -m "feat(app): instantiate flasher and pass to api.New"
```

---

## Task 23: slog instrumentation in stages

**Files:**
- Modify: `internal/flasher/stages.go`
- Modify: `internal/flasher/flasher.go`

Add the structured log lines specified in spec §8. No new tests — slog calls don't change observable behavior. Verify with `go test -count=1 ./internal/flasher/` after the edit.

- [ ] **Step 23.1: Add per-stage slog calls**

In `internal/flasher/stages.go`, add the import:

```go
"log/slog"
```

At the end of each `recordStage` call site for stages `backup`, `erase`, `program`, `verify`, `test`, immediately *after* the recordStage line, emit:

```go
slog.Info("flash_stage",
    "port", s.port,
    "stage", "<stage_name>",
    "status", "<status>",
    "duration_ms", time.Since(start).Milliseconds(),
)
```

Concretely, in `runBackup`, replace the success-record line with:

```go
	s.recordStage("backup", "ok", "", time.Since(start))
	slog.Info("flash_stage", "port", s.port, "stage", "backup", "status", "ok", "duration_ms", time.Since(start).Milliseconds())
```

Do the same for the failure branches in `runBackup`, `runErase`, `runProgram`, `runVerify`, `runTest`, and the success/failure branches in `runRollback`.

For `runVerify` failure, also include `first_mismatch_offset`:

```go
	slog.Info("flash_stage", "port", s.port, "stage", "verify", "status", "failed",
		"duration_ms", time.Since(start).Milliseconds(),
		"first_mismatch_offset", fmt.Sprintf("0x%04X", off))
```

For `runRollback` success, include `verify_status`:

```go
	slog.Info("flash_stage", "port", s.port, "stage", "rollback", "status", "ok",
		"duration_ms", time.Since(start).Milliseconds(),
		"verify_status", st.VerifyStatus)
```

- [ ] **Step 23.2: Add the final summary log in `Flash`**

In `internal/flasher/flasher.go`, before each `return s.res, nil`, add:

```go
	logFlashSummary(s, port)
```

At the bottom of `flasher.go`, add the helper:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
)

func logFlashSummary(s *runState, port string) {
	fwSum := sha256.Sum256(s.req.Firmware)
	totalMs := int64(0)
	for _, st := range s.res.Stages {
		totalMs += st.Duration.Milliseconds()
	}
	slog.Info("flash_summary",
		"port", port,
		"outcome", s.res.Outcome.String(),
		"firmware_sha256", hex.EncodeToString(fwSum[:]),
		"backup_sha256", s.res.Backup.SHA256,
		"total_duration_ms", totalMs,
	)
}
```

(If `crypto/sha256` / `encoding/hex` are already imported in `flasher.go`, don't add them twice.)

- [ ] **Step 23.3: Run tests**

```bash
go test -count=1 ./internal/flasher/ ./internal/api/
```

Expected: all PASS. The log output is silenced by default in tests; nothing to assert against.

- [ ] **Step 23.4: Commit**

```bash
git add internal/flasher/stages.go internal/flasher/flasher.go
git commit -m "feat(flasher): slog per-stage and summary log lines"
```

---

## Task 24: README + final verification

**Files:**
- Modify: `README.md`

- [ ] **Step 24.1: Update README**

Open `README.md`. Find the REST API table (or the relevant endpoint list — search for `serial/ports` to locate the existing endpoint section). Add rows for the three new endpoints:

```
| `POST /devices/disconnect`         | Close every serial handle in the registry. Always available.                                              |
| `GET /serial/ports/detailed`       | List ports with USB descriptors (VID/PID/SerialNumber/Product). Always available.                         |
| `POST /flash/{port}`               | Pre-backup, flash, byte-verify, optional test, auto-rollback. Gated by `flashing.enabled`.                |
```

Add a short paragraph above or below the table about the `flashing:` config block — point to the spec for full detail:

```
**Remote firmware flashing.** With `flashing.enabled: true`, the operator can POST an Intel HEX firmware image to `POST /flash/{port}` and receive a complete stage-by-stage outcome including a pre-flash backup (saved on the lab machine *and* returned inline). On any post-backup failure, SerialHop attempts to roll back to the backup automatically. AVR / optiboot only (Arduino Uno R3). Off by default. See `docs/superpowers/specs/2026-05-12-remote-firmware-flashing-design.md` for the full design.
```

- [ ] **Step 24.2: Run the full pre-PR check**

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Expected: all clean.

- [ ] **Step 24.3: Smoke build**

```bash
task build
```

Expected: `dist/SerialHop.exe` built successfully. Inspect the binary size — should be comparable to before (no new dependencies).

- [ ] **Step 24.4: Commit**

```bash
git add README.md
git commit -m "docs: document remote firmware flashing endpoints"
```

---

## Final Verification

- [ ] **All tests pass on all targets**

```bash
go test -race -count=1 ./...
GOOS=linux go test -count=1 ./...
GOOS=windows go test -count=1 ./...
```

Expected: all green on all three GOOS values (the `_windows.go` / `_other.go` build-tag split is unchanged; the new code is platform-neutral).

- [ ] **Manual smoke (optional, requires hardware)**

If an Arduino Uno is available on the dev machine:

1. Build a small sketch (`Blink.ino`), export `.hex` via `arduino-cli compile --output-dir /tmp/blink ...`.
2. Run SerialHop in foreground mode with `flashing.enabled: true` and `raw_serial.enabled: true`.
3. `curl -sS http://127.0.0.1:<rest>/serial/ports/detailed` → confirm the Uno appears with VID `2341`.
4. `curl -sS -X POST http://127.0.0.1:<rest>/devices/disconnect` → `{"released":...}`.
5. `curl -sS -X POST http://127.0.0.1:<rest>/flash/COM3 -H 'Content-Type: application/json' --data @flash_request.json` where `flash_request.json` contains `firmware` + an empty test pair.
6. Confirm response has `"outcome":"success"` and the backup file exists at `%ProgramData%\SerialHop\backups\`.
7. Confirm the LED on the board is blinking.

- [ ] **Open PR**

PR title: `feat(api): remote firmware flashing`

Body should reference the spec and the plan. Conventional Commits: `feat:` will trigger a minor bump on the next release-please PR.

---

## Spec Coverage Self-Check

| Spec section | Implemented in task(s) |
|---|---|
| §1 Purpose & scope | Whole plan |
| §2 Outcome taxonomy | Tasks 12, 13, 14, 15, 16, 17 |
| §3 Configuration | Task 1 |
| §4.1 POST /devices/disconnect | Task 19 |
| §4.2 GET /serial/ports/detailed | Task 20 |
| §4.3 POST /flash/{port} | Tasks 21A + 21B |
| §5 State machine | Tasks 13–17 |
| §5.1 timing constants | Task 8 |
| §5.2 bootloader entry | Task 14 (DTR pulse + Sync) |
| §5.3 Rollback | Task 16 |
| §5.4 Backup file lifecycle | Tasks 11, 14, 16, 17 |
| §6 Internal package layout | Tasks 4, 5–11, 18–22 |
| §6.1 Public surface | Task 12 |
| §6.2 Dependency direction | Implicitly enforced by the import statements in each task |
| §7 Testing | Distributed across every task |
| §8 Logging | Handler-level slog in Task 19 (disconnect); per-stage `flash_stage` lines + `flash_summary` line in Task 23 |
| §9 Concurrency | Task 12 (mutex) + Task 21A (preflight checks) |
| §10 Error response shape | Task 21A |
| §11 Compatibility | All additive; verified by `go test ./...` after every task |
| §12 Build / release | Task 24 (`task build` smoke test) |
