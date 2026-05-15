# Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a bespoke Go installer (`SerialHop-Setup-vX.Y.Z.exe`) that copies an embedded SerialHop.exe payload to `C:\Program Files\SerialHop\` (or operator-chosen path), drops an unversioned `SerialHop.lnk` on the all-users desktop, supports in-place upgrades / same-version no-ops / refused downgrades, and ships from CI alongside the bare exe (which keeps powering the panel's in-app auto-update). The VPS endpoint switches to receiving the installer.

**Architecture:** New `tools/installer/` Go package. Embeds the just-built `dist/SerialHop.exe` via `//go:embed` (snapshot, no network bootstrap). One walk-based dialog plus `--silent` / `--dir` / `--no-launch` / `--no-shortcut` / `--allow-downgrade` flags. The install flow reuses the existing `internal/winsvc.updateBinary` rollback logic via a new exported `winsvc.InstallOrUpgrade(...)` wrapper. The desktop shortcut targets `<install_dir>\SerialHop.exe` (unversioned) — that's the load-bearing property that lets in-place updates leave the icon untouched.

**Tech Stack:** Go 1.21+, `//go:embed`, `github.com/lxn/walk` (already a project dep), `github.com/go-ole/go-ole` (new — for COM IShellLink shortcut creation), `golang.org/x/sys/windows` (already in go.sum, used for elevation check + PE version read), `goversioninfo` (already used for the main binary's resource), Taskfile, GitHub Actions.

**Reference spec:** `docs/superpowers/specs/2026-05-15-installer-design.md`. Section numbers below cite that spec.

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/updater/version.go` | Add `Compare(a, b string) (int, error)` (three-way SemVer compare). Used by installer for downgrade detection. |
| `internal/updater/version_test.go` | Cover `Compare` table-driven. |
| `internal/winsvc/control.go` | Add exported `InstallOrUpgrade(scm SCMConn, src, target string) error`. One-line wrapper around `updateBinary` with production timeouts. |
| `internal/winsvc/control_test.go` | One coverage test that `InstallOrUpgrade` dispatches to `updateBinary` correctly. |
| `tools/installer/main.go` | Entry point. Flag parsing, dispatch to silent vs dialog path. |
| `tools/installer/install.go` | Cross-platform install logic. Version dispatch, payload extract, SHA self-check, delegate to `winsvc.InstallOrUpgrade`, optional shortcut + launch. All Windows-specific dependencies injected via interfaces so this file compiles and tests cross-platform. |
| `tools/installer/install_test.go` | Cross-platform tests. Fakes for fs, scm, version reader, shortcut writer, launcher. |
| `tools/installer/peversion_windows.go` | `readPEFileVersion(path) (string, error)` via `GetFileVersionInfoExW` + `VerQueryValueW`. |
| `tools/installer/peversion_other.go` | Build-tag stub returning sentinel error. |
| `tools/installer/shortcut_windows.go` | `writeShortcut(opts)` via COM IShellLinkW + IPersistFileW (using `github.com/go-ole/go-ole`). |
| `tools/installer/shortcut_other.go` | Build-tag stub returning sentinel error. |
| `tools/installer/shortcut_windows_test.go` | Round-trip lnk creation in `t.TempDir()`. |
| `tools/installer/ui_windows.go` | walk dialog: path field, Browse, Install, Cancel, status label. Drives `install.Run` on a goroutine; marshals updates via `mw.Synchronize`. |
| `tools/installer/ui_other.go` | Build-tag stub: panics if invoked. |
| `tools/installer/version.json` | StringFileInfo + FixedFileInfo for installer's PE. Bumped by release-please (string fields) + CI (integer fields). |
| `tools/installer/manifest.template.xml` | UAC manifest with `requireAdministrator`. |
| `tools/installer/payload/.gitkeep` | Keeps the gitignored `payload/` dir tracked. |
| `tools/render-installer-manifest/main.go` | Renders `tools/installer/manifest.xml` from template + `tools/installer/version.json`. Parallel of `tools/render-manifest`. |
| `Taskfile.yaml` | Add `installer-manifest`, `installer-resource`, `installer` tasks; extend `clean`. |
| `.gitignore` | Add `tools/installer/manifest.xml`, `tools/installer/resource_windows.syso`, `tools/installer/payload/SerialHop.exe`. |
| `.github/workflows/release-please.yml` | Sync installer version.json integers; build installer; rename + checksum + upload both; VPS upload swaps to installer. |
| `release-please-config.json` | Add installer `version.json` to `extra-files`. |
| `README.md` | New "Install on a Windows lab machine" lead with the installer; existing manual-copy instructions move to "Advanced". |

---

## Task 1: `Compare` three-way SemVer helper

**Files:**
- Modify: `internal/updater/version.go`
- Test: `internal/updater/version_test.go`

The installer needs three-way version comparison to dispatch fresh / upgrade / same / downgrade (spec §4.4). The existing `IsNewer` returns only bool — extract the comparison helper.

- [ ] **Step 1: Read the current `IsNewer` to understand the parser**

Run: `cat internal/updater/version.go`
Expected: the existing `IsNewer`, `parse`, and `semver` struct.

- [ ] **Step 2: Append the failing `Compare` test cases**

Open `internal/updater/version_test.go` and add (alongside the existing `IsNewer` tests):

```go
func TestCompare(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
		err  bool
	}{
		{"a less than b", "0.6.1", "0.7.0", -1, false},
		{"a greater than b", "0.7.0", "0.6.1", 1, false},
		{"equal", "0.7.0", "0.7.0", 0, false},
		{"equal with leading v on a", "v0.7.0", "0.7.0", 0, false},
		{"equal with leading v on b", "0.7.0", "v0.7.0", 0, false},
		{"a dev build vs b release, base equal", "0.6.1+v0.6.1-7-gabc1234-dirty", "0.6.1", 0, false},
		{"a dev build base less than b", "0.6.1+v0.6.1-7-gabc1234-dirty", "0.7.0", -1, false},
		{"major diff dominates minor", "1.0.0", "0.99.0", 1, false},
		{"minor diff dominates patch", "0.7.0", "0.6.999", 1, false},
		{"malformed a", "abc", "0.7.0", 0, true},
		{"malformed b", "0.7.0", "abc", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compare(tc.a, tc.b)
			if tc.err {
				if err == nil {
					t.Fatalf("Compare(%q, %q) = %d, nil; want error", tc.a, tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compare(%q, %q) returned unexpected error: %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Compare(%q, %q) = %d; want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the test, expect FAIL (undefined Compare)**

Run: `go test ./internal/updater/ -run TestCompare -v`
Expected: `undefined: Compare` build error.

- [ ] **Step 4: Implement `Compare`**

Edit `internal/updater/version.go` and add (alongside `IsNewer`):

```go
// Compare returns -1, 0, or +1 if a is older than, equal to, or newer than b,
// respectively, by SemVer Major.Minor.Patch ordering. Both inputs may carry a
// leading "v" or a trailing "+buildmeta" segment (the dev-build shape produced
// by tools/buildcmd); they are stripped before comparison. Returns an error
// only if either input fails to parse as X.Y.Z.
func Compare(a, b string) (int, error) {
	ap, err := parse(a)
	if err != nil {
		return 0, fmt.Errorf("parse a: %w", err)
	}
	bp, err := parse(b)
	if err != nil {
		return 0, fmt.Errorf("parse b: %w", err)
	}
	switch {
	case ap.major != bp.major:
		if ap.major < bp.major {
			return -1, nil
		}
		return 1, nil
	case ap.minor != bp.minor:
		if ap.minor < bp.minor {
			return -1, nil
		}
		return 1, nil
	case ap.patch != bp.patch:
		if ap.patch < bp.patch {
			return -1, nil
		}
		return 1, nil
	default:
		return 0, nil
	}
}
```

- [ ] **Step 5: Re-run, expect PASS**

Run: `go test ./internal/updater/ -v`
Expected: all tests pass, including the new `TestCompare` subtests.

- [ ] **Step 6: Commit**

```bash
git add internal/updater/version.go internal/updater/version_test.go
git commit -m "feat(updater): add three-way SemVer Compare helper"
```

---

## Task 2: `InstallOrUpgrade` wrapper

**Files:**
- Modify: `internal/winsvc/control.go`
- Test: `internal/winsvc/control_test.go`

The installer calls this single entry point regardless of whether the service exists yet. `updateBinary` already gracefully handles the "service missing" case (control.go:224-226 in the existing code), so this is a one-line wrapper that supplies production timeouts. Spec §5 step 6, §9.

- [ ] **Step 1: Add the failing test**

Append to `internal/winsvc/control_test.go`:

```go
func TestInstallOrUpgrade_FreshInstall_NoService(t *testing.T) {
	scm := newFakeSCM() // no service registered
	src := filepath.Join(t.TempDir(), "SerialHop-v0.7.0.exe")
	target := filepath.Join(filepath.Dir(src), "SerialHop.exe")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := InstallOrUpgrade(scm, src, target); err != nil {
		t.Fatalf("InstallOrUpgrade: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target should exist after fresh install: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("src should be renamed away: stat err = %v", err)
	}
}

func TestInstallOrUpgrade_UpgradeWithRunningService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateRunning}
	dir := t.TempDir()
	src := filepath.Join(dir, "SerialHop-v0.7.0.exe")
	target := filepath.Join(dir, "SerialHop.exe")
	if err := os.WriteFile(src, []byte("new payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(target, []byte("old payload"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	// Simulate the SCM transitioning to Stopped after Stop() then Running after Start().
	svc := scm.services[ServiceName]
	svc.stateProgression = []ServiceState{StateStopPending, StateStopped, StateStartPending, StateRunning}

	if err := InstallOrUpgrade(scm, src, target); err != nil {
		t.Fatalf("InstallOrUpgrade: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new payload" {
		t.Fatalf("target content = %q; want %q", got, "new payload")
	}
	if !svc.started {
		t.Errorf("service should have been started after swap")
	}
}
```

Imports needed in `control_test.go` (add if missing): `os`, `path/filepath`.

- [ ] **Step 2: Run, expect FAIL (undefined InstallOrUpgrade)**

Run: `go test ./internal/winsvc/ -run TestInstallOrUpgrade -v`
Expected: `undefined: InstallOrUpgrade` build error.

- [ ] **Step 3: Implement `InstallOrUpgrade`**

Append to `internal/winsvc/control.go`:

```go
// InstallOrUpgrade extracts the in-place update sequence into a public entry
// point so the installer binary can reuse the same rename-with-rollback
// machinery the panel's auto-update relies on. updateBinary gracefully
// handles the "service not yet installed" case (it skips the SCM stop and
// start), so this single call covers both first-install and upgrade.
//
// src must already exist at the desired path and live in the same directory
// as target (same-volume rename requirement). target is the canonical install
// location (e.g., C:\Program Files\SerialHop\SerialHop.exe).
func InstallOrUpgrade(scm SCMConn, src, target string) error {
	return updateBinary(scm, realFS{}, src, target,
		productionStartTimeout, productionPollInterval, 250*time.Millisecond)
}
```

- [ ] **Step 4: Run, expect PASS**

Run: `go test ./internal/winsvc/ -v`
Expected: all tests pass, including the two new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/winsvc/control.go internal/winsvc/control_test.go
git commit -m "feat(winsvc): expose InstallOrUpgrade wrapper for installer reuse"
```

---

## Task 3: Installer package skeleton + cross-platform stubs

**Files:**
- Create: `tools/installer/payload/.gitkeep`
- Create: `tools/installer/peversion_other.go`
- Create: `tools/installer/shortcut_other.go`
- Create: `tools/installer/ui_other.go`
- Modify: `.gitignore`

Bring the package into existence so subsequent tasks can build against it. The Windows-specific files are placeholders that get filled in later; the `_other.go` stubs let `go build ./...` and `go test ./...` keep passing on macOS/Linux from this commit onward.

- [ ] **Step 1: Create payload directory placeholder**

Create `tools/installer/payload/.gitkeep` with content:

```
# Build-time staging dir for the embedded SerialHop.exe payload.
# Populated by `task installer` (which copies dist/SerialHop.exe in).
# Gitignored payload itself; this .gitkeep keeps the directory tracked.
```

- [ ] **Step 2: Create the non-Windows PE-version stub**

Create `tools/installer/peversion_other.go`:

```go
//go:build !windows

package main

import "errors"

// readPEFileVersion is a Windows-only operation. The cross-platform stub
// returns a sentinel error so cross-platform tests that exercise the install
// dispatch can substitute a fake reader instead of calling this directly.
func readPEFileVersion(_ string) (string, error) {
	return "", errors.New("readPEFileVersion: only supported on Windows")
}
```

- [ ] **Step 3: Create the non-Windows shortcut stub**

Create `tools/installer/shortcut_other.go`:

```go
//go:build !windows

package main

import "errors"

type shortcutOpts struct {
	Path         string
	Target       string
	WorkingDir   string
	IconLocation string
	Description  string
}

// writeShortcut is a Windows-only operation; the cross-platform stub lets the
// installer package compile and run its dispatch tests on macOS/Linux. Real
// shortcut creation happens via COM IShellLinkW in shortcut_windows.go.
func writeShortcut(_ shortcutOpts) error {
	return errors.New("writeShortcut: only supported on Windows")
}
```

- [ ] **Step 4: Create the non-Windows UI stub**

Create `tools/installer/ui_other.go`:

```go
//go:build !windows

package main

// runDialog is unreachable from cross-platform tests because main() dispatches
// to it only under //go:build windows. A panic here would make debugging an
// accidental invocation obvious.
func runDialog(_ *options) int {
	panic("runDialog: only supported on Windows")
}
```

- [ ] **Step 5: Add gitignore entries**

Append to `.gitignore`:

```
# Installer build artifacts
/tools/installer/manifest.xml
/tools/installer/resource_windows.syso
/tools/installer/payload/SerialHop.exe
```

- [ ] **Step 6: Verify cross-platform compile**

Run: `go build ./tools/installer/...`
Expected: builds successfully on the host OS. (Won't run yet — no `main` function — but should compile.)

If the build fails with "no Go files in tools/installer" because all files have non-matching build tags, add a placeholder `tools/installer/doc.go` with just `package main`:

```go
// Package main implements the SerialHop installer. See
// docs/superpowers/specs/2026-05-15-installer-design.md for design notes.
package main
```

This file is permanent (the package needs a doc anyway).

- [ ] **Step 7: Commit**

```bash
git add tools/installer/ .gitignore
git commit -m "feat(installer): scaffold tools/installer package with build-tag stubs"
```

---

## Task 4: PE version reader (Windows-only)

**Files:**
- Create: `tools/installer/peversion_windows.go`

Reads `StringFileInfo.FileVersion` from a PE binary's version resource. Used by the install flow's version-dispatch step (spec §4.4). No automated test against a real PE — the cross-platform install tests inject a fake `versionReader`, and a smoke-test against the just-built `dist/SerialHop.exe` happens in Task 10 (Taskfile validation).

- [ ] **Step 1: Implement the Windows PE reader**

Create `tools/installer/peversion_windows.go`:

```go
//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modVersion                = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoExW = modVersion.NewProc("GetFileVersionInfoExW")
	procGetFileVersionInfoSizeExW = modVersion.NewProc("GetFileVersionInfoSizeExW")
	procVerQueryValueW        = modVersion.NewProc("VerQueryValueW")
)

// readPEFileVersion reads StringFileInfo.FileVersion from path's PE version
// resource. Returns the version string as it appears in the resource (e.g.,
// "0.7.0" for the SerialHop binary). Errors if the file is missing, has no
// version resource, or the resource is malformed.
func readPEFileVersion(path string) (string, error) {
	wpath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("convert path: %w", err)
	}

	// 1. Determine the size of the version resource.
	var handle uint32
	size, _, _ := procGetFileVersionInfoSizeExW.Call(
		uintptr(0), // FILE_VER_GET_NEUTRAL
		uintptr(unsafe.Pointer(wpath)),
		uintptr(unsafe.Pointer(&handle)),
	)
	if size == 0 {
		return "", fmt.Errorf("no version info in %s", path)
	}

	// 2. Load the resource into a buffer.
	buf := make([]byte, size)
	ret, _, errno := procGetFileVersionInfoExW.Call(
		uintptr(0),
		uintptr(unsafe.Pointer(wpath)),
		uintptr(0),
		uintptr(size),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if ret == 0 {
		return "", fmt.Errorf("GetFileVersionInfoExW: %v", syscall.Errno(errno))
	}

	// 3. Probe \VarFileInfo\Translation to discover the langID + codepage,
	//    then query \StringFileInfo\<lang><cp>\FileVersion.
	type langCP struct {
		Language uint16
		CodePage uint16
	}
	subBlock, err := windows.UTF16PtrFromString(`\VarFileInfo\Translation`)
	if err != nil {
		return "", fmt.Errorf("convert sub-block: %w", err)
	}
	var ptr unsafe.Pointer
	var length uint32
	ret, _, errno = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(subBlock)),
		uintptr(unsafe.Pointer(&ptr)),
		uintptr(unsafe.Pointer(&length)),
	)
	if ret == 0 || length < uint32(unsafe.Sizeof(langCP{})) {
		return "", fmt.Errorf("VerQueryValue translation: %v", syscall.Errno(errno))
	}
	tr := *(*langCP)(ptr)

	// 4. Query the FileVersion string.
	query := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileVersion`, tr.Language, tr.CodePage)
	queryPtr, err := windows.UTF16PtrFromString(query)
	if err != nil {
		return "", fmt.Errorf("convert query: %w", err)
	}
	ret, _, errno = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(queryPtr)),
		uintptr(unsafe.Pointer(&ptr)),
		uintptr(unsafe.Pointer(&length)),
	)
	if ret == 0 {
		return "", fmt.Errorf("VerQueryValue %s: %v", query, syscall.Errno(errno))
	}
	if length == 0 {
		return "", fmt.Errorf("FileVersion empty in %s", path)
	}
	// `length` is a count of UTF-16 code units including the trailing NUL.
	utf16Slice := unsafe.Slice((*uint16)(ptr), length)
	// Trim the trailing NUL if present.
	if len(utf16Slice) > 0 && utf16Slice[len(utf16Slice)-1] == 0 {
		utf16Slice = utf16Slice[:len(utf16Slice)-1]
	}
	return windows.UTF16ToString(utf16Slice), nil
}
```

- [ ] **Step 2: Verify Windows-only compile**

Run: `GOOS=windows GOARCH=amd64 go build ./tools/installer/...`
Expected: builds successfully.

Run: `go build ./tools/installer/...` (host OS, expected to use the `_other.go` stub)
Expected: builds successfully.

- [ ] **Step 3: Commit**

```bash
git add tools/installer/peversion_windows.go
git commit -m "feat(installer): add Windows PE FileVersion reader"
```

---

## Task 5: Install flow core (the heart of the package)

**Files:**
- Create: `tools/installer/install.go`
- Create: `tools/installer/install_test.go`

Cross-platform install logic with dependency injection (fs, scm, version reader, shortcut writer, launcher) so the dispatch logic is unit-tested on macOS/Linux. The Windows-specific implementations live in `peversion_windows.go` (already done), `shortcut_windows.go` (Task 6), and are wired in via `main.go` (Task 8).

The install module mirrors the dispatch table in spec §4.4 and the install flow in §5.

- [ ] **Step 1: Write the test file scaffolding**

Create `tools/installer/install_test.go`:

```go
package main

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// fakeFS records writes and renames; it does not touch disk.
type fakeFS struct {
	mu      sync.Mutex
	files   map[string][]byte
	dirs    map[string]bool
	renames []renameOp

	writeErr  error
	renameErr error
}

type renameOp struct{ from, to string }

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string][]byte{}, dirs: map[string]bool{}}
}

func (f *fakeFS) MkdirAll(path string, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[path] = true
	return nil
}

func (f *fakeFS) WriteFile(path string, data []byte, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.files[path] = cp
	return nil
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[path]
	if !ok {
		return nil, errors.New("fakeFS: not found")
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func (f *fakeFS) Rename(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renameErr != nil {
		return f.renameErr
	}
	b, ok := f.files[from]
	if !ok {
		return errors.New("fakeFS: src missing for rename")
	}
	f.files[to] = b
	delete(f.files, from)
	f.renames = append(f.renames, renameOp{from, to})
	return nil
}

func (f *fakeFS) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, path)
	return nil
}

func (f *fakeFS) Stat(path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[path]
	return ok, nil
}

// fakeVersionReader returns a configured version for any path.
type fakeVersionReader struct {
	versions map[string]string
	err      error
}

func (f *fakeVersionReader) Read(path string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.versions[path]
	if !ok {
		return "", errors.New("fakeVersionReader: no version configured")
	}
	return v, nil
}

// fakeShortcutWriter records the last opts and can be configured to fail.
type fakeShortcutWriter struct {
	called bool
	last   shortcutOpts
	err    error
}

func (f *fakeShortcutWriter) Write(opts shortcutOpts) error {
	f.called = true
	f.last = opts
	return f.err
}

// fakeLauncher records the path and can be configured to fail.
type fakeLauncher struct {
	called bool
	path   string
	err    error
}

func (f *fakeLauncher) Launch(path string) error {
	f.called = true
	f.path = path
	return f.err
}

// fakeInstaller assembles a Runner with all fakes wired up. Helper for tests.
func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{
		FS:             newFakeFS(),
		VersionReader:  &fakeVersionReader{versions: map[string]string{}},
		ShortcutWriter: &fakeShortcutWriter{},
		Launcher:       &fakeLauncher{},
		// SCM and DialSCM are stubbed by tests that exercise the SCM path.
		BundledVersion: "0.7.0",
		Payload:        []byte("payload bytes v0.7.0"),
	}
}
```

This compiles on all OSes (no Windows-specific calls). The fakes are sufficient for the dispatch matrix.

- [ ] **Step 2: Run, expect FAIL (Runner, shortcutOpts, etc. undefined)**

Run: `go test ./tools/installer/ -v`
Expected: build errors — undefined `Runner`, references to fakes that exist but no struct to plug into.

- [ ] **Step 3: Implement the install module skeleton**

Create `tools/installer/install.go`:

```go
package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// State is the result of comparing the installed version to the bundled version.
type State int

const (
	StateFresh State = iota
	StateUpgrade
	StateSame
	StateDowngrade
)

func (s State) String() string {
	switch s {
	case StateFresh:
		return "fresh"
	case StateUpgrade:
		return "upgrade"
	case StateSame:
		return "same"
	case StateDowngrade:
		return "downgrade"
	default:
		return "unknown"
	}
}

// options captures the parsed CLI/dialog choices the install flow needs.
type options struct {
	InstallDir       string
	Silent           bool
	NoLaunch         bool
	NoShortcut       bool
	AllowDowngrade   bool
}

// fsOps abstracts the filesystem ops the install flow needs. Production wires
// realFS{} which delegates to the os package; tests inject fakeFS{}.
type fsOps interface {
	MkdirAll(path string, mode uint32) error
	WriteFile(path string, data []byte, mode uint32) error
	ReadFile(path string) ([]byte, error)
	Rename(from, to string) error
	Remove(path string) error
	Stat(path string) (exists bool, err error)
}

// versionReader abstracts reading the PE FileVersion. Production wires
// peReader{} which calls readPEFileVersion; tests inject fakeVersionReader.
type versionReader interface {
	Read(path string) (string, error)
}

// shortcutWriter abstracts desktop shortcut creation. Production wires
// realShortcutWriter{} which calls writeShortcut; tests inject fakeShortcutWriter.
type shortcutWriter interface {
	Write(opts shortcutOpts) error
}

// launcher abstracts the "start the panel in a detached child" step.
type launcher interface {
	Launch(path string) error
}

// scmDialer abstracts the SCM connection; production wires winsvc.DialSCM.
type scmDialer func() (winsvc.SCMConn, error)

// Runner holds the dependencies for an installer run. main.go assembles the
// production Runner; tests inject fakes.
type Runner struct {
	FS             fsOps
	VersionReader  versionReader
	ShortcutWriter shortcutWriter
	Launcher       launcher
	DialSCM        scmDialer // may be nil; tests that don't exercise SCM leave it nil
	BundledVersion string    // set by main from internal/version.Version
	Payload        []byte    // set by main from the //go:embed payload
}

// Result reports what happened so the UI (or stdout in silent mode) can
// surface the right status. Status messages from spec §12.
type Result struct {
	State        State
	InstalledVer string
	BundledVer   string
	Message      string
	Err          error
	ExitCode     int
}

// Run executes the install flow. See spec §4.4 (dispatch) and §5 (flow).
func (r *Runner) Run(opts options) Result {
	targetExe := filepath.Join(opts.InstallDir, "SerialHop.exe")

	state, installedVer, err := r.detectState(targetExe)
	if err != nil {
		return Result{Err: fmt.Errorf("detect installed version: %w", err), ExitCode: 1}
	}

	slog.Info("version_check",
		"installed", installedVer,
		"bundled", r.BundledVersion,
		"decision", state.String())

	switch state {
	case StateSame:
		return r.runSameVersion(opts, targetExe, installedVer)
	case StateDowngrade:
		if !opts.AllowDowngrade {
			return Result{
				State:        state,
				InstalledVer: installedVer,
				BundledVer:   r.BundledVersion,
				Err: fmt.Errorf(
					"installed version (v%s) is newer than this installer (v%s); "+
						"re-run with --allow-downgrade to proceed anyway",
					installedVer, r.BundledVersion),
				ExitCode: 1,
			}
		}
		fallthrough
	case StateFresh, StateUpgrade:
		return r.runInstallOrUpgrade(opts, targetExe, state, installedVer)
	default:
		return Result{Err: fmt.Errorf("unknown state %v", state), ExitCode: 1}
	}
}

// detectState checks whether targetExe exists and, if so, reads its PE version.
func (r *Runner) detectState(targetExe string) (State, string, error) {
	exists, err := r.FS.Stat(targetExe)
	if err != nil {
		return 0, "", err
	}
	if !exists {
		return StateFresh, "", nil
	}
	installed, err := r.VersionReader.Read(targetExe)
	if err != nil {
		return 0, "", fmt.Errorf("read installed version from %s: %w", targetExe, err)
	}
	cmp, err := updater.Compare(installed, r.BundledVersion)
	if err != nil {
		return 0, installed, fmt.Errorf("compare versions: %w", err)
	}
	switch {
	case cmp < 0:
		return StateUpgrade, installed, nil
	case cmp == 0:
		return StateSame, installed, nil
	default:
		return StateDowngrade, installed, nil
	}
}

// runSameVersion is the no-op-equivalent path: refresh shortcut, optionally
// launch, exit 0. No file writes, no SCM ops.
func (r *Runner) runSameVersion(opts options, targetExe, installedVer string) Result {
	res := Result{
		State:        StateSame,
		InstalledVer: installedVer,
		BundledVer:   r.BundledVersion,
		Message: fmt.Sprintf(
			"SerialHop v%s is already installed. Refreshed desktop shortcut.",
			installedVer),
	}
	r.maybeShortcut(opts, targetExe, &res)
	r.maybeLaunch(opts, targetExe, &res)
	return res
}

// runInstallOrUpgrade handles fresh installs and upgrades (and downgrades when
// the operator passed --allow-downgrade). Spec §5.
func (r *Runner) runInstallOrUpgrade(opts options, targetExe string, state State, installedVer string) Result {
	res := Result{
		State:        state,
		InstalledVer: installedVer,
		BundledVer:   r.BundledVersion,
	}

	// Step 1: ensure install dir exists.
	if err := r.FS.MkdirAll(opts.InstallDir, 0o755); err != nil {
		res.Err = fmt.Errorf("create install dir %s: %w", opts.InstallDir, err)
		res.ExitCode = 1
		return res
	}

	// Steps 2-4: stage payload + SHA-256 self-check.
	stagedName := fmt.Sprintf("SerialHop-v%s.exe", r.BundledVersion)
	stagedPath := filepath.Join(opts.InstallDir, stagedName)
	if err := r.FS.WriteFile(stagedPath, r.Payload, 0o644); err != nil {
		res.Err = fmt.Errorf("stage payload to %s: %w", stagedPath, err)
		res.ExitCode = 1
		return res
	}
	written, err := r.FS.ReadFile(stagedPath)
	if err != nil {
		res.Err = fmt.Errorf("read back staged payload: %w", err)
		res.ExitCode = 1
		return res
	}
	if sha256.Sum256(written) != sha256.Sum256(r.Payload) {
		_ = r.FS.Remove(stagedPath)
		res.Err = errors.New("bundled payload integrity check failed; the installer may be corrupted")
		res.ExitCode = 1
		return res
	}
	slog.Info("payload_extracted", "path", stagedPath, "size", len(r.Payload))

	// Step 5-6: SCM + InstallOrUpgrade.
	if r.DialSCM == nil {
		// Tests that don't exercise SCM skip this branch by leaving DialSCM nil.
		// Production always sets it.
		res.Err = errors.New("internal: DialSCM not configured")
		res.ExitCode = 1
		return res
	}
	scm, err := r.DialSCM()
	if err != nil {
		_ = r.FS.Remove(stagedPath)
		res.Err = fmt.Errorf("connect to Service Control Manager: %w", err)
		res.ExitCode = 1
		return res
	}
	defer func() { _ = scm.Disconnect() }()

	if err := winsvc.InstallOrUpgrade(scm, stagedPath, targetExe); err != nil {
		res.Err = fmt.Errorf("install/upgrade failed: %w", err)
		res.ExitCode = 1
		return res
	}
	slog.Info("install_or_upgrade_completed", "version", r.BundledVersion)

	// Steps 7-8: shortcut + launch (non-fatal on failure).
	r.maybeShortcut(opts, targetExe, &res)
	r.maybeLaunch(opts, targetExe, &res)

	if res.Message == "" {
		res.Message = fmt.Sprintf("Installed SerialHop v%s to %s.", r.BundledVersion, opts.InstallDir)
	}
	return res
}

// maybeShortcut creates the desktop shortcut unless --no-shortcut. Failures
// are recorded in the Result but never raise the exit code (spec §5 step 7
// note: shortcut failure does not fail the install).
func (r *Runner) maybeShortcut(opts options, targetExe string, res *Result) {
	if opts.NoShortcut {
		return
	}
	shortcutPath := publicDesktopShortcutPath()
	err := r.ShortcutWriter.Write(shortcutOpts{
		Path:         shortcutPath,
		Target:       targetExe,
		WorkingDir:   filepath.Dir(targetExe),
		IconLocation: targetExe + ",0",
		Description:  "SerialHop control panel",
	})
	if err != nil {
		slog.Warn("shortcut_failed", "path", shortcutPath, "err", err)
		// Preserve a successful binary install message while appending the warning.
		res.Message = fmt.Sprintf(
			"Install succeeded but desktop shortcut creation failed: %v. "+
				"You can launch SerialHop from %s.",
			err, targetExe)
	}
}

// maybeLaunch starts the panel unless --no-launch / --silent. Failures are
// recorded in the Result but never raise the exit code (spec §5 step 8 note).
func (r *Runner) maybeLaunch(opts options, targetExe string, res *Result) {
	if opts.NoLaunch || opts.Silent {
		return
	}
	if err := r.Launcher.Launch(targetExe); err != nil {
		slog.Warn("launch_failed", "path", targetExe, "err", err)
		// Augment the message rather than overwriting a shortcut-failure note.
		if res.Message == "" {
			res.Message = fmt.Sprintf(
				"Install succeeded but launching SerialHop failed: %v. "+
					"Double-click the desktop shortcut to start it.",
				err)
		}
	}
}

// publicDesktopShortcutPath returns the canonical all-users Desktop path.
// On Windows that's C:\Users\Public\Desktop\SerialHop.lnk. The constant is
// extracted so tests can override it via the var if needed.
func publicDesktopShortcutPath() string {
	return `C:\Users\Public\Desktop\SerialHop.lnk`
}
```

- [ ] **Step 4: Run the existing tests, expect them to compile but report no test functions**

Run: `go test ./tools/installer/ -v`
Expected: compile succeeds; output is "no tests to run" (the test file has fakes but no `TestXxx` functions yet).

- [ ] **Step 5: Append the fresh-install test case**

Append to `tools/installer/install_test.go`:

```go
func TestRun_FreshInstall_HappyPath(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if res.State != StateFresh {
		t.Errorf("state = %v; want fresh", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
	sw := r.ShortcutWriter.(*fakeShortcutWriter)
	if !sw.called {
		t.Errorf("expected shortcut to be written on fresh install")
	}
	if sw.last.Target != filepath.Join(opts.InstallDir, "SerialHop.exe") {
		t.Errorf("shortcut target = %q; want unversioned SerialHop.exe under install dir", sw.last.Target)
	}
	l := r.Launcher.(*fakeLauncher)
	if !l.called {
		t.Errorf("expected panel to be launched on fresh install")
	}
}

// fakeSCMDialer + noOpSCM let tests exercise the DialSCM path without going
// through the real Windows SCM. noOpSCM satisfies winsvc.SCMConn; OpenService
// always returns ErrServiceMissing so InstallOrUpgrade's "service not installed"
// branch is taken — which matches the fresh-install case.
type fakeSCMDialer struct {
	conn winsvc.SCMConn
	err  error
}

func (f *fakeSCMDialer) Dial() (winsvc.SCMConn, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

type noOpSCM struct{}

func (noOpSCM) Disconnect() error                                              { return nil }
func (noOpSCM) OpenService(string) (winsvc.SCMService, error)                  { return nil, winsvc.ErrServiceMissing }
func (noOpSCM) CreateService(string, winsvc.ServiceConfig) (winsvc.SCMService, error) {
	return nil, errors.New("noOpSCM: CreateService not implemented")
}
```

Add the import for `errors` and `path/filepath` to the test file if not already present.

- [ ] **Step 6: Run, expect PASS**

Run: `go test ./tools/installer/ -run TestRun_FreshInstall_HappyPath -v`
Expected: PASS.

- [ ] **Step 7: Add the same-version test case**

Append to `tools/installer/install_test.go`:

```go
func TestRun_SameVersion_NoOp(t *testing.T) {
	r := newTestRunner(t)
	target := `C:\Program Files\SerialHop\SerialHop.exe`
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("pretend this is the installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = r.BundledVersion // same as installer

	// DialSCM should NOT be called on same-version path. Wire a dialer that
	// fails the test if invoked.
	r.DialSCM = func() (winsvc.SCMConn, error) {
		t.Fatalf("DialSCM must not be called on same-version path")
		return nil, nil
	}

	opts := options{InstallDir: `C:\Program Files\SerialHop`}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if res.State != StateSame {
		t.Errorf("state = %v; want same", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
	// Same-version still refreshes shortcut and launches.
	sw := r.ShortcutWriter.(*fakeShortcutWriter)
	if !sw.called {
		t.Errorf("expected shortcut to be refreshed on same-version re-run")
	}
	l := r.Launcher.(*fakeLauncher)
	if !l.called {
		t.Errorf("expected panel to be launched on same-version re-run")
	}
	// No payload should have been written.
	for path := range fs.files {
		if filepath.Base(path) == "SerialHop-v"+r.BundledVersion+".exe" {
			t.Errorf("unexpected staged payload at %s on same-version path", path)
		}
	}
}
```

- [ ] **Step 8: Run, expect PASS**

Run: `go test ./tools/installer/ -run TestRun_SameVersion -v`
Expected: PASS.

- [ ] **Step 9: Add the downgrade-refused test case**

Append to `tools/installer/install_test.go`:

```go
func TestRun_DowngradeRefused(t *testing.T) {
	r := newTestRunner(t)
	target := `C:\Program Files\SerialHop\SerialHop.exe`
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = "0.8.0" // newer than r.BundledVersion="0.7.0"

	r.DialSCM = func() (winsvc.SCMConn, error) {
		t.Fatalf("DialSCM must not be called when downgrade is refused")
		return nil, nil
	}

	opts := options{InstallDir: `C:\Program Files\SerialHop`} // no AllowDowngrade
	res := r.Run(opts)
	if res.Err == nil {
		t.Fatalf("expected error refusing downgrade; got nil")
	}
	if res.ExitCode != 1 {
		t.Errorf("exit code = %d; want 1", res.ExitCode)
	}
	if res.State != StateDowngrade {
		t.Errorf("state = %v; want downgrade", res.State)
	}
	// Shortcut and launcher should not have been called.
	if r.ShortcutWriter.(*fakeShortcutWriter).called {
		t.Errorf("shortcut writer should not run on refused downgrade")
	}
	if r.Launcher.(*fakeLauncher).called {
		t.Errorf("launcher should not run on refused downgrade")
	}
}
```

- [ ] **Step 10: Run, expect PASS**

Run: `go test ./tools/installer/ -run TestRun_DowngradeRefused -v`
Expected: PASS.

- [ ] **Step 11: Add the downgrade-with-flag test case**

Append to `tools/installer/install_test.go`:

```go
func TestRun_DowngradeWithFlag_Proceeds(t *testing.T) {
	r := newTestRunner(t)
	target := `C:\Program Files\SerialHop\SerialHop.exe`
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("newer installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = "0.8.0"
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial

	opts := options{InstallDir: `C:\Program Files\SerialHop`, AllowDowngrade: true}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run with --allow-downgrade: %v", res.Err)
	}
	if res.State != StateDowngrade {
		t.Errorf("state = %v; want downgrade", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
}
```

Note: `noOpSCM.OpenService` returns `ErrServiceMissing`, which makes `InstallOrUpgrade` skip the SCM stop/start dance entirely (since there's no real service in this test). The rename swap proceeds against the fakeFS. This is exactly what we want — the test verifies the dispatch decision, not the SCM details.

- [ ] **Step 12: Run, expect PASS**

Run: `go test ./tools/installer/ -run TestRun_DowngradeWithFlag -v`
Expected: PASS.

- [ ] **Step 13: Add the upgrade test case**

Append to `tools/installer/install_test.go`:

```go
func TestRun_Upgrade_HappyPath(t *testing.T) {
	r := newTestRunner(t)
	target := `C:\Program Files\SerialHop\SerialHop.exe`
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("old installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = "0.6.1" // older than r.BundledVersion="0.7.0"
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial

	opts := options{InstallDir: `C:\Program Files\SerialHop`}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run upgrade: %v", res.Err)
	}
	if res.State != StateUpgrade {
		t.Errorf("state = %v; want upgrade", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
	// Target should now hold the new payload bytes after the rename swap.
	got, err := fs.ReadFile(target)
	if err != nil {
		t.Fatalf("read back target: %v", err)
	}
	if string(got) != string(r.Payload) {
		t.Errorf("target content = %q; want %q (payload)", got, r.Payload)
	}
}
```

- [ ] **Step 14: Run, expect PASS**

Run: `go test ./tools/installer/ -run TestRun_Upgrade -v`
Expected: PASS.

- [ ] **Step 15: Add the no-shortcut + no-launch flag tests**

Append to `tools/installer/install_test.go`:

```go
func TestRun_NoShortcutFlag(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`, NoShortcut: true}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if r.ShortcutWriter.(*fakeShortcutWriter).called {
		t.Errorf("shortcut writer must not be called when --no-shortcut is set")
	}
}

func TestRun_NoLaunchFlag(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`, NoLaunch: true}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if r.Launcher.(*fakeLauncher).called {
		t.Errorf("launcher must not be called when --no-launch is set")
	}
}

func TestRun_SilentImpliesNoLaunch(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`, Silent: true}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if r.Launcher.(*fakeLauncher).called {
		t.Errorf("launcher must not be called when --silent is set")
	}
}
```

- [ ] **Step 16: Run, expect PASS**

Run: `go test ./tools/installer/ -v`
Expected: all tests pass.

- [ ] **Step 17: Add shortcut-failure-is-non-fatal test**

Append to `tools/installer/install_test.go`:

```go
func TestRun_ShortcutFailure_IsNonFatal(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	sw := r.ShortcutWriter.(*fakeShortcutWriter)
	sw.err = errors.New("shortcut path not writable")

	opts := options{InstallDir: `C:\Program Files\SerialHop`}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("expected nil err on non-fatal shortcut failure; got %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0 (shortcut failure is non-fatal)", res.ExitCode)
	}
	if !strings.Contains(res.Message, "desktop shortcut creation failed") {
		t.Errorf("message should mention shortcut failure; got %q", res.Message)
	}
}
```

Add the `strings` import.

- [ ] **Step 18: Run, expect PASS**

Run: `go test ./tools/installer/ -v`
Expected: all tests pass.

- [ ] **Step 19: Add the SHA-256 mismatch test**

This case is fiddly because we need the staged file's content to differ from the in-memory payload after WriteFile reports success. We accomplish that via a fakeFS configured to mutate the bytes on read.

Append to `tools/installer/install_test.go`:

```go
// tamperingFakeFS wraps fakeFS and corrupts the readback to simulate a
// silent on-disk corruption between WriteFile and ReadFile. Used to test
// the SHA-256 self-check.
type tamperingFakeFS struct{ *fakeFS }

func (t *tamperingFakeFS) ReadFile(path string) ([]byte, error) {
	b, err := t.fakeFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		b[0] ^= 0xff
	}
	return b, nil
}

func TestRun_PayloadShaMismatch(t *testing.T) {
	r := newTestRunner(t)
	r.FS = &tamperingFakeFS{fakeFS: newFakeFS()}
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial

	opts := options{InstallDir: `C:\Program Files\SerialHop`}
	res := r.Run(opts)
	if res.Err == nil {
		t.Fatalf("expected SHA mismatch error; got nil")
	}
	if res.ExitCode != 1 {
		t.Errorf("exit code = %d; want 1", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "integrity check failed") {
		t.Errorf("err should mention integrity check; got %v", res.Err)
	}
}
```

- [ ] **Step 20: Run, expect PASS**

Run: `go test ./tools/installer/ -v`
Expected: all tests pass.

- [ ] **Step 21: Commit**

```bash
git add tools/installer/install.go tools/installer/install_test.go
git commit -m "feat(installer): implement install/upgrade/same/downgrade dispatch with TDD coverage"
```

---

## Task 6: Shortcut COM wrapper (Windows-only)

**Files:**
- Create: `tools/installer/shortcut_windows.go`
- Create: `tools/installer/shortcut_windows_test.go`
- Modify: `go.mod`, `go.sum` (via `go get`)

Decision: use `github.com/go-ole/go-ole` for IShellLinkW + IPersistFileW. The raw-`golang.org/x/sys/windows` path needs ~100 LOC of hand-rolled COM vtable boilerplate; go-ole gives us ~30 LOC of caller code, is well-maintained (MIT, no transitive dependencies), and the dep cost is minimal. The spec (§6) anticipated this trade-off and permitted the dep if the raw path was gnarly enough — it is.

- [ ] **Step 1: Add the go-ole dependency**

Run: `GOOS=windows go get github.com/go-ole/go-ole`
Run: `go mod tidy`

Expected: `go.mod` gains `github.com/go-ole/go-ole vX.Y.Z` under `require`; `go.sum` updates. (Use whatever current version `go get` resolves to; go-ole is stable and v1.x has been the latest for years.)

- [ ] **Step 2: Implement the Windows shortcut writer**

Create `tools/installer/shortcut_windows.go`:

```go
//go:build windows

package main

import (
	"fmt"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

type shortcutOpts struct {
	Path         string // .lnk destination (e.g., C:\Users\Public\Desktop\SerialHop.lnk)
	Target       string // executable the shortcut points at
	WorkingDir   string // working directory for the launched process
	IconLocation string // "<exe>,<index>" — typically "<exe>,0"
	Description  string // tooltip / description
}

// writeShortcut creates (or overwrites) a Windows .lnk file at opts.Path
// pointing at opts.Target. Implementation uses COM IShellLinkW and
// IPersistFileW via WScript.Shell ... wait no, WScript is a higher-level API.
// We use the actual ShellLink COM object directly.
func writeShortcut(opts shortcutOpts) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// CoInitializeEx returns S_FALSE if already initialized on this thread;
		// go-ole maps that to error CO_E_ALREADYINITIALIZED, which is fine.
		if oleErr, ok := err.(*ole.OleError); ok && oleErr.Code() != 0x80010106 {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("CreateObject WScript.Shell: %w", err)
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("QueryInterface IDispatch: %w", err)
	}
	defer shell.Release()

	linkVar, err := oleutil.CallMethod(shell, "CreateShortcut", opts.Path)
	if err != nil {
		return fmt.Errorf("CreateShortcut: %w", err)
	}
	link := linkVar.ToIDispatch()
	defer link.Release()

	if _, err := oleutil.PutProperty(link, "TargetPath", opts.Target); err != nil {
		return fmt.Errorf("set TargetPath: %w", err)
	}
	if _, err := oleutil.PutProperty(link, "WorkingDirectory", opts.WorkingDir); err != nil {
		return fmt.Errorf("set WorkingDirectory: %w", err)
	}
	if opts.IconLocation != "" {
		if _, err := oleutil.PutProperty(link, "IconLocation", opts.IconLocation); err != nil {
			return fmt.Errorf("set IconLocation: %w", err)
		}
	}
	if opts.Description != "" {
		if _, err := oleutil.PutProperty(link, "Description", opts.Description); err != nil {
			return fmt.Errorf("set Description: %w", err)
		}
	}
	if _, err := oleutil.CallMethod(link, "Save"); err != nil {
		return fmt.Errorf("Save: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Implement the Windows realShortcutWriter that satisfies the shortcutWriter interface**

Append to `tools/installer/shortcut_windows.go`:

```go
type realShortcutWriter struct{}

func (realShortcutWriter) Write(opts shortcutOpts) error {
	return writeShortcut(opts)
}
```

- [ ] **Step 4: Mirror the realShortcutWriter on non-Windows for compile parity**

Append to `tools/installer/shortcut_other.go`:

```go
type realShortcutWriter struct{}

func (realShortcutWriter) Write(opts shortcutOpts) error {
	return writeShortcut(opts)
}
```

- [ ] **Step 5: Write the round-trip test (Windows-only)**

Create `tools/installer/shortcut_windows_test.go`:

```go
//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func TestWriteShortcut_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	// Create a stand-in target file so the shortcut's TargetPath is valid.
	targetPath := filepath.Join(dir, "SerialHop.exe")
	if err := os.WriteFile(targetPath, []byte("stub"), 0o600); err != nil {
		t.Fatalf("create stub target: %v", err)
	}
	linkPath := filepath.Join(dir, "SerialHop.lnk")

	opts := shortcutOpts{
		Path:         linkPath,
		Target:       targetPath,
		WorkingDir:   dir,
		IconLocation: targetPath + ",0",
		Description:  "Test shortcut",
	}
	if err := writeShortcut(opts); err != nil {
		t.Fatalf("writeShortcut: %v", err)
	}

	// Resolve the shortcut back via the same WScript.Shell API and assert
	// TargetPath / WorkingDirectory match what we wrote.
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != 0x80010106 {
			t.Fatalf("CoInitializeEx: %v", err)
		}
	}
	defer ole.CoUninitialize()
	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	defer unknown.Release()
	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatalf("QueryInterface: %v", err)
	}
	defer shell.Release()
	linkVar, err := oleutil.CallMethod(shell, "CreateShortcut", linkPath)
	if err != nil {
		t.Fatalf("read back CreateShortcut: %v", err)
	}
	link := linkVar.ToIDispatch()
	defer link.Release()

	gotTarget, err := oleutil.GetProperty(link, "TargetPath")
	if err != nil {
		t.Fatalf("get TargetPath: %v", err)
	}
	defer gotTarget.Clear()
	if gotTarget.ToString() != targetPath {
		t.Errorf("TargetPath = %q; want %q", gotTarget.ToString(), targetPath)
	}

	gotWD, err := oleutil.GetProperty(link, "WorkingDirectory")
	if err != nil {
		t.Fatalf("get WorkingDirectory: %v", err)
	}
	defer gotWD.Clear()
	if gotWD.ToString() != dir {
		t.Errorf("WorkingDirectory = %q; want %q", gotWD.ToString(), dir)
	}
}
```

- [ ] **Step 6: Run cross-platform build**

Run: `go build ./tools/installer/...` (host OS)
Run: `GOOS=windows GOARCH=amd64 go build ./tools/installer/...`
Expected: both build successfully.

- [ ] **Step 7: Run cross-platform tests**

Run: `go test ./tools/installer/ -v`
Expected: all existing dispatch tests pass on host OS; the Windows-only round-trip test is skipped (different build tag).

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum tools/installer/shortcut_windows.go tools/installer/shortcut_other.go tools/installer/shortcut_windows_test.go
git commit -m "feat(installer): COM-based desktop shortcut writer with round-trip test"
```

---

## Task 7: Dialog UI (Windows-only)

**Files:**
- Modify: `tools/installer/ui_windows.go` (replace stub with walk dialog)

The dialog drives `Runner.Run` on a goroutine; status updates marshal back to the GUI thread via `mw.Synchronize`. No automated test — UI is verified by running the produced installer locally during Task 10's smoke test.

- [ ] **Step 1: Implement the walk dialog**

Replace `tools/installer/ui_windows.go` (delete the `_other.go` stub for the windows build, since this is the real implementation; the `_other.go` stub stays for non-Windows compile parity). Create as a new `tools/installer/ui_windows.go`:

```go
//go:build windows

package main

import (
	"fmt"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// runDialog shows the install dialog and returns the exit code. Drives
// Runner.Run on a goroutine and marshals status updates onto the GUI thread
// via mw.Synchronize.
func runDialog(opts *options) int {
	var (
		mw         *walk.MainWindow
		pathEdit   *walk.LineEdit
		statusLine *walk.Label
		installBtn *walk.PushButton
	)

	exitCode := 0
	produceResult := func(r Result) {
		mw.Synchronize(func() {
			if r.Err != nil {
				statusLine.SetText("Error: " + r.Err.Error())
				exitCode = r.ExitCode
				installBtn.SetEnabled(true)
				return
			}
			msg := r.Message
			if msg == "" {
				msg = fmt.Sprintf("Installed SerialHop v%s.", r.BundledVer)
			}
			statusLine.SetText(msg)
			exitCode = r.ExitCode
			// On success, leave the window open with the status visible. The
			// operator closes it manually. (The panel has already been
			// launched in a detached child by maybeLaunch.)
			installBtn.SetEnabled(true)
		})
	}

	err := MainWindow{
		AssignTo: &mw,
		Title:    "SerialHop Installer",
		MinSize:  Size{Width: 520, Height: 200},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "Install location:"},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					LineEdit{
						AssignTo: &pathEdit,
						Text:     opts.InstallDir,
					},
					PushButton{
						Text: "Browse…",
						OnClicked: func() {
							dlg := walk.FileDialog{
								Title:    "Choose install directory",
								FilePath: pathEdit.Text(),
							}
							ok, err := dlg.ShowBrowseFolder(mw)
							if err != nil || !ok {
								return
							}
							pathEdit.SetText(dlg.FilePath)
						},
					},
				},
			},
			Label{AssignTo: &statusLine, Text: "Ready."},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{
						AssignTo: &installBtn,
						Text:     "Install",
						OnClicked: func() {
							installBtn.SetEnabled(false)
							statusLine.SetText("Installing…")
							runOpts := *opts
							runOpts.InstallDir = pathEdit.Text()
							go func() {
								r := newProductionRunner()
								produceResult(r.Run(runOpts))
							}()
						},
					},
					PushButton{
						Text:      "Cancel",
						OnClicked: func() { mw.Close() },
					},
				},
			},
		},
	}.Create()
	if err != nil {
		fmt.Println("create dialog:", err)
		return 1
	}
	mw.Run()
	return exitCode
}
```

- [ ] **Step 2: Verify Windows compile**

Run: `GOOS=windows GOARCH=amd64 go build ./tools/installer/...`
Expected: builds successfully.

(`newProductionRunner` is defined in `main.go`; that file doesn't exist yet — expect a compile error at this step. The next task adds main.go and resolves it.)

Actually: stub `newProductionRunner` in `main.go` is a Task 8 dependency. Let's defer the build verification to the end of Task 8.

For now run: `GOOS=windows GOARCH=amd64 go vet ./tools/installer/... 2>&1 | grep -v "undefined: newProductionRunner" || true`
Expected: no errors other than the known undefined ref.

- [ ] **Step 3: Commit (the package isn't yet buildable end-to-end; that's resolved in Task 8)**

```bash
git add tools/installer/ui_windows.go
git commit -m "feat(installer): walk dialog with Browse and async install action"
```

---

## Task 8: Main entry point and production wiring

**Files:**
- Create: `tools/installer/main.go`

Ties it all together: flag parsing, build-time-baked version + payload, real Runner construction, dispatch to silent vs dialog path, elevation sanity check.

- [ ] **Step 1: Create `main.go`**

Create `tools/installer/main.go`:

```go
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

//go:embed payload/SerialHop.exe
var payload []byte

const defaultInstallDir = `C:\Program Files\SerialHop`

var (
	flagDir            = flag.String("dir", defaultInstallDir, "install directory (absolute path)")
	flagSilent         = flag.Bool("silent", false, "no dialog; proceed with defaults; output to stderr")
	flagNoLaunch       = flag.Bool("no-launch", false, "do not launch the panel after install")
	flagNoShortcut    = flag.Bool("no-shortcut", false, "do not create the desktop shortcut")
	flagAllowDowngrade = flag.Bool("allow-downgrade", false, "proceed even if the installed version is newer than this installer's payload")
	flagVersion        = flag.Bool("version", false, "print installer + payload version and exit")
)

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Printf("SerialHop Installer v%s (payload v%s)\n",
			internalversion.Base(), internalversion.Base())
		return
	}

	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "fatal: unexpected positional arguments:", flag.Args())
		os.Exit(2)
	}

	if !filepath.IsAbs(*flagDir) {
		fmt.Fprintln(os.Stderr, "fatal: --dir must be an absolute path:", *flagDir)
		os.Exit(2)
	}

	if err := enforceElevation(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}

	opts := &options{
		InstallDir:     *flagDir,
		Silent:         *flagSilent,
		NoLaunch:       *flagNoLaunch,
		NoShortcut:     *flagNoShortcut,
		AllowDowngrade: *flagAllowDowngrade,
	}

	configureLogging()

	if *flagSilent {
		os.Exit(runSilent(opts))
		return
	}
	os.Exit(runDialog(opts))
}

func runSilent(opts *options) int {
	r := newProductionRunner()
	res := r.Run(*opts)
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, "error:", res.Err)
		return res.ExitCode
	}
	if res.Message != "" {
		fmt.Println(res.Message)
	}
	return res.ExitCode
}

// newProductionRunner wires the production dependencies into a Runner.
// Called from both runSilent and the dialog's Install handler.
func newProductionRunner() *Runner {
	return &Runner{
		FS:             realFS{},
		VersionReader:  peReader{},
		ShortcutWriter: realShortcutWriter{},
		Launcher:       realLauncher{},
		DialSCM:        winsvc.DialSCM,
		BundledVersion: internalversion.Base(),
		Payload:        payload,
	}
}

func configureLogging() {
	// Diagnostic log file in %TEMP%. Spec §11.
	tmp := os.TempDir()
	logPath := filepath.Join(tmp, fmt.Sprintf("SerialHop-installer-%s.log", internalversion.Base()))
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		// Logging is best-effort; if we can't write the file, fall back to stderr.
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// realFS satisfies fsOps using the os package.
type realFS struct{}

func (realFS) MkdirAll(path string, mode uint32) error { return os.MkdirAll(path, os.FileMode(mode)) }
func (realFS) WriteFile(path string, data []byte, mode uint32) error {
	return os.WriteFile(path, data, os.FileMode(mode))
}
func (realFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (realFS) Rename(from, to string) error         { return os.Rename(from, to) }
func (realFS) Remove(path string) error             { return os.Remove(path) }
func (realFS) Stat(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// peReader satisfies versionReader by delegating to readPEFileVersion (which
// has _windows / _other build-tag variants).
type peReader struct{}

func (peReader) Read(path string) (string, error) { return readPEFileVersion(path) }

// realLauncher starts the panel detached so the installer can exit.
type realLauncher struct{}

func (realLauncher) Launch(path string) error {
	cmd := exec.Command(path)
	// Inherits stdout/stderr to nowhere (windowsgui binary); the parent
	// installer process can exit while the child keeps running.
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach: do not Wait. The Go runtime will keep the child process going
	// after this process exits (the OS reaps it when its parent goes away
	// only on Unix; on Windows the child is independent unless explicitly
	// added to a job object).
	return nil
}
```

- [ ] **Step 2: Create the elevation enforcement helpers**

Create `tools/installer/elevation_windows.go`:

```go
//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

func enforceElevation() error {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return err
	}
	defer token.Close()
	if !token.IsElevated() {
		return errors.New(
			"this installer must be run as administrator; right-click → " +
				"Run as administrator, or re-run and approve the UAC prompt")
	}
	return nil
}
```

Create `tools/installer/elevation_other.go`:

```go
//go:build !windows

package main

// Non-Windows builds are not user-facing (they exist only to satisfy
// cross-platform CI). Elevation check is a no-op.
func enforceElevation() error { return nil }
```

- [ ] **Step 3: Build cross-platform**

Run: `go build ./tools/installer/...`
Run: `GOOS=windows GOARCH=amd64 go build ./tools/installer/...`

Note: the `//go:embed payload/SerialHop.exe` directive requires the file to exist at build time. For this initial step you can place a dummy: `echo "stub" > tools/installer/payload/SerialHop.exe`. The Taskfile (Task 10) will populate this directory with the real binary at release-build time.

Expected: both builds succeed with the stub payload.

After verifying, **remove the stub**: `rm tools/installer/payload/SerialHop.exe` (it's gitignored, but cleaning it up before commit avoids confusion).

- [ ] **Step 4: Run tests one more time**

Run: `go test ./tools/installer/ ./internal/winsvc/ ./internal/updater/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add tools/installer/main.go tools/installer/elevation_windows.go tools/installer/elevation_other.go
git commit -m "feat(installer): main entry, flag parsing, production wiring, elevation check"
```

---

## Task 9: Version metadata, manifest template, render tool

**Files:**
- Create: `tools/installer/version.json`
- Create: `tools/installer/manifest.template.xml`
- Create: `tools/render-installer-manifest/main.go`

- [ ] **Step 1: Create `tools/installer/version.json`**

Copy the shape from `assets/version.json` and adapt:

```json
{
  "FixedFileInfo": {
    "FileVersion": {
      "Major": 0,
      "Minor": 18,
      "Patch": 3,
      "Build": 0
    },
    "ProductVersion": {
      "Major": 0,
      "Minor": 18,
      "Patch": 3,
      "Build": 0
    },
    "FileFlagsMask": "3f",
    "FileFlags": "00",
    "FileOS": "040004",
    "FileType": "01",
    "FileSubType": "00"
  },
  "StringFileInfo": {
    "CompanyName": "Lab Devices",
    "FileDescription": "SerialHop Installer",
    "FileVersion": "0.18.3",
    "InternalName": "serialhop-installer",
    "LegalCopyright": "Copyright (c) 2026 Lab Devices",
    "OriginalFilename": "SerialHop-Setup.exe",
    "ProductName": "SerialHop Installer",
    "ProductVersion": "0.18.3"
  },
  "VarFileInfo": {
    "Translation": {
      "LangID": "0409",
      "CharsetID": "04B0"
    }
  },
  "ManifestPath": "tools/installer/manifest.xml",
  "IconPath": "assets/icon.ico"
}
```

Match the current `assets/version.json` version (read it first; whatever value is there is the right one to mirror).

Run: `cat assets/version.json` to confirm the version string, then update both `FileVersion`/`ProductVersion` strings and the integer fields to match.

- [ ] **Step 2: Create `tools/installer/manifest.template.xml`**

Create with content (note: this is similar to but distinct from the main binary's manifest — the installer requires admin, the main binary uses asInvoker):

```xml
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity version="@@VERSION@@" processorArchitecture="amd64" name="LabDevices.SerialHopInstaller" type="win32"/>
  <description>SerialHop Installer</description>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity
        type="win32"
        name="Microsoft.Windows.Common-Controls"
        version="6.0.0.0"
        processorArchitecture="*"
        publicKeyToken="6595b64144ccf1df"
        language="*"
      />
    </dependentAssembly>
  </dependency>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
</assembly>
```

- [ ] **Step 3: Create the render tool**

Create `tools/render-installer-manifest/main.go`:

```go
// render-installer-manifest writes tools/installer/manifest.xml from
// tools/installer/manifest.template.xml, substituting @@VERSION@@ with the
// StringFileInfo.FileVersion from tools/installer/version.json plus a
// trailing ".0".
//
// Parallel of tools/render-manifest; lives separately so each tool has a
// single, obvious file pair.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	versionPath  = "tools/installer/version.json"
	templatePath = "tools/installer/manifest.template.xml"
	outputPath   = "tools/installer/manifest.xml"
	placeholder  = "@@VERSION@@"
)

type versionFile struct {
	StringFileInfo struct {
		FileVersion string
	}
}

func main() {
	raw, err := os.ReadFile(versionPath)
	if err != nil {
		fail("read %s: %v", versionPath, err)
	}
	var vf versionFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		fail("parse %s: %v", versionPath, err)
	}
	if vf.StringFileInfo.FileVersion == "" {
		fail("%s: StringFileInfo.FileVersion is empty", versionPath)
	}
	tmpl, err := os.ReadFile(templatePath)
	if err != nil {
		fail("read %s: %v", templatePath, err)
	}
	out := strings.ReplaceAll(string(tmpl), placeholder, vf.StringFileInfo.FileVersion+".0")
	if err := os.WriteFile(outputPath, []byte(out), 0o600); err != nil { //nolint:gosec // outputPath is a package-level constant
		fail("write %s: %v", outputPath, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "render-installer-manifest: "+format+"\n", args...)
	os.Exit(1)
}
```

- [ ] **Step 4: Manually invoke the render tool to sanity-check**

Run: `go run ./tools/render-installer-manifest`
Run: `cat tools/installer/manifest.xml`
Expected: produces a valid XML file with `@@VERSION@@` replaced by `<version>.0`.

Run: `git status` — confirm `tools/installer/manifest.xml` shows up as untracked (it should be gitignored).
Run: `git check-ignore tools/installer/manifest.xml`
Expected: prints `tools/installer/manifest.xml` (confirming it's ignored).

Clean up: `rm tools/installer/manifest.xml`.

- [ ] **Step 5: Commit**

```bash
git add tools/installer/version.json tools/installer/manifest.template.xml tools/render-installer-manifest/main.go
git commit -m "build(installer): add version.json, UAC manifest template, render tool"
```

---

## Task 10: Taskfile integration + local end-to-end smoke

**Files:**
- Modify: `Taskfile.yaml`

- [ ] **Step 1: Read the existing Taskfile and locate the manifest/resource/build/clean blocks**

Run: `cat Taskfile.yaml`
Note: the current file has `manifest`, `resource`, `build`, `test`, `tidy`, `preview`, `clean` tasks. We're inserting `installer-manifest`, `installer-resource`, `installer` after `build`.

- [ ] **Step 2: Edit `Taskfile.yaml`** — append after the `build` task and update `clean`:

Add (between `build` and `test`):

```yaml
  installer-manifest:
    desc: Generate tools/installer/manifest.xml from template + version.json.
    sources:
      - tools/installer/manifest.template.xml
      - tools/installer/version.json
      - tools/render-installer-manifest/main.go
    generates:
      - tools/installer/manifest.xml
    cmds:
      - go run ./tools/render-installer-manifest

  installer-resource:
    desc: Compile the installer's .syso (icon + UAC manifest + version metadata).
    deps: [installer-manifest]
    cmds:
      - go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
          -64
          -o tools/installer/resource_windows.syso
          -icon=assets/icon.ico
          -manifest=tools/installer/manifest.xml
          tools/installer/version.json

  installer:
    desc: Build the installer (depends on a fresh `task build` to produce the embedded payload).
    deps: [build, installer-resource]
    cmds:
      - cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
      - go run ./tools/buildcmd -o dist/SerialHop-Setup.exe -goos windows -goarch amd64 ./tools/installer
```

Update `clean` (replace the existing `rm -rf` line):

```yaml
  clean:
    desc: Remove build outputs and generated resources.
    cmds:
      - rm -rf {{.OUTPUT_DIR}} {{.RESOURCE_FILE}} assets/manifest.xml tools/installer/manifest.xml tools/installer/resource_windows.syso tools/installer/payload/SerialHop.exe
```

- [ ] **Step 3: Run `task installer` end-to-end locally (Windows)**

This step requires a Windows host (the resource step uses `goversioninfo`, which produces a `.syso`; the embedded payload is cross-arch but the build target is windows/amd64). If the implementer is on macOS/Linux, skip the actual build but verify by running `GOOS=windows GOARCH=amd64 go build ./tools/installer/...` with a stub payload (as in Task 8).

On Windows: `task installer`
Expected: produces `dist/SerialHop-Setup.exe`. Approximate size: 30-50 MB (the embedded SerialHop binary is the dominant cost).

On macOS/Linux smoke (does NOT produce the final installer, but validates the toolchain):

```bash
task build           # produces dist/SerialHop.exe
cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
go run ./tools/render-installer-manifest
GOOS=windows GOARCH=amd64 go build -o /tmp/installer-test.exe ./tools/installer/
ls -lh /tmp/installer-test.exe
```

Expected: a sane binary size, no build errors.

- [ ] **Step 4: Run `task clean` and verify all generated artifacts are gone**

Run: `task clean`
Run: `ls dist/ tools/installer/manifest.xml tools/installer/resource_windows.syso tools/installer/payload/SerialHop.exe 2>&1 | grep -E "No such|cannot access"`
Expected: at least the four generated artifact paths report "No such file".

- [ ] **Step 5: Commit**

```bash
git add Taskfile.yaml
git commit -m "build(installer): wire installer/installer-manifest/installer-resource into Taskfile"
```

---

## Task 11: CI changes — release-please.yml + release-please-config.json

**Files:**
- Modify: `.github/workflows/release-please.yml`
- Modify: `release-please-config.json`

- [ ] **Step 1: Extend `release-please-config.json`**

Edit `release-please-config.json` — extend the `extra-files` array. Result:

```json
{
  "release-type": "simple",
  "packages": {
    ".": {
      "package-name": "serialhop",
      "include-component-in-tag": false,
      "extra-files": [
        { "type": "json", "path": "assets/version.json", "jsonpath": "$.StringFileInfo.FileVersion" },
        { "type": "json", "path": "assets/version.json", "jsonpath": "$.StringFileInfo.ProductVersion" },
        { "type": "json", "path": "tools/installer/version.json", "jsonpath": "$.StringFileInfo.FileVersion" },
        { "type": "json", "path": "tools/installer/version.json", "jsonpath": "$.StringFileInfo.ProductVersion" }
      ]
    }
  },
  "changelog-sections": [
    { "type": "feat",     "section": "Features"      },
    { "type": "fix",      "section": "Bug Fixes"     },
    { "type": "perf",     "section": "Performance"   },
    { "type": "revert",   "section": "Reverts"       },
    { "type": "chore",    "section": "Chores",        "hidden": true },
    { "type": "docs",     "section": "Documentation", "hidden": true },
    { "type": "refactor", "section": "Refactoring",   "hidden": true },
    { "type": "test",     "section": "Tests",         "hidden": true },
    { "type": "build",    "section": "Build",         "hidden": true },
    { "type": "ci",       "section": "CI",            "hidden": true }
  ]
}
```

- [ ] **Step 2: Extend the `sync version.json integer fields` step in `release-please.yml`**

Edit `.github/workflows/release-please.yml` — find the `sync version.json integer fields` step (around lines 91-105) and extend it so the same jq transform also lands on `tools/installer/version.json`. Result for that step:

```yaml
      # release-please's `json` updater only writes string values, so the
      # six FixedFileInfo {File,Product}Version.{Major,Minor,Patch} integers
      # would otherwise stay frozen at the previous release's numbers. Derive
      # them from the just-bumped string field and write them in-place. The
      # working tree becomes dirty as a side-effect; tools/buildcmd uses
      # `git describe --exact-match` to bypass that and emit a clean
      # `X.Y.Z+vX.Y.Z` version suffix when HEAD is on a tag.
      - name: sync version.json integer fields
        shell: bash
        run: |
          set -euo pipefail
          VER=$(jq -e -r '.StringFileInfo.FileVersion' assets/version.json)
          IFS='.' read -r MAJOR MINOR PATCH <<< "$VER"
          for vfile in assets/version.json tools/installer/version.json; do
            jq --argjson major "$MAJOR" --argjson minor "$MINOR" --argjson patch "$PATCH" \
              '.FixedFileInfo.FileVersion.Major = $major |
               .FixedFileInfo.FileVersion.Patch  = $patch |
               .FixedFileInfo.FileVersion.Minor  = $minor |
               .FixedFileInfo.ProductVersion.Major = $major |
               .FixedFileInfo.ProductVersion.Minor = $minor |
               .FixedFileInfo.ProductVersion.Patch  = $patch' \
              "$vfile" > "$vfile.tmp"
            mv "$vfile.tmp" "$vfile"
          done
```

- [ ] **Step 3: Extend the `build` step to also build the installer**

In the same workflow file, find the `build` step (around lines 107-109) and change it to:

```yaml
      - name: build
        run: |
          task build
          task installer
        shell: bash
```

- [ ] **Step 4: Extend the `rename and checksum` step**

Find the `rename and checksum` step (around lines 111-119). Change to:

```yaml
      - name: rename and checksum
        shell: pwsh
        run: |
          $tag = "${{ needs.release-please.outputs.tag_name }}"
          Move-Item dist\SerialHop.exe "dist\SerialHop-$tag.exe"
          Move-Item dist\SerialHop-Setup.exe "dist\SerialHop-Setup-$tag.exe"
          $lines = Get-FileHash -Algorithm SHA256 dist\*.exe | ForEach-Object {
            "$($_.Hash.ToLower())  $([System.IO.Path]::GetFileName($_.Path))"
          }
          [System.IO.File]::WriteAllText("dist\SHA256SUMS.txt", ($lines -join "`n") + "`n", [System.Text.Encoding]::ASCII)
```

The `dist\*.exe` glob already covers both binaries; no further change needed for hash coverage.

- [ ] **Step 5: Extend the `upload to release` step**

Find the `upload to release` step (around lines 125-131). Change to:

```yaml
      - name: upload to release
        shell: pwsh
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          $tag = "${{ needs.release-please.outputs.tag_name }}"
          gh release upload $tag dist/SerialHop-$tag.exe dist/SerialHop-Setup-$tag.exe dist/SHA256SUMS.txt --clobber
```

- [ ] **Step 6: Swap the VPS upload step to send the installer**

Find the `upload agent build to VPS` step (lines 133-147). Change the `-F "binary=@…"` line:

```yaml
      - name: upload agent build to VPS
        shell: bash
        env:
          VPS_HOST: ${{ vars.VPS_HOST }}
          AGENT_UPLOAD_TOKEN: ${{ secrets.AGENT_UPLOAD_TOKEN }}
          TAG: ${{ needs.release-please.outputs.tag_name }}
        run: |
          # Server's VERSION_RE rejects the leading 'v'; post bare semver.
          VERSION="${TAG#v}"
          curl --fail-with-body -sSL -X POST "https://${VPS_HOST}/api/agent/upload" \
            -H "Authorization: Bearer ${AGENT_UPLOAD_TOKEN}" \
            -F "version=${VERSION}" \
            -F "binary=@dist/SerialHop-Setup-${TAG}.exe" \
            -w '\nHTTP %{http_code}\n'
```

The attestation step (`actions/attest-build-provenance@v4` with `subject-path: 'dist/SerialHop-*.exe'`) already covers both files via its wildcard — no change there.

- [ ] **Step 7: Lint the workflow file**

Run: `grep -n "task installer\|SerialHop-Setup" .github/workflows/release-please.yml`
Expected: prints the new lines you added — sanity check they're in the right places.

- [ ] **Step 8: Commit**

```bash
git add .github/workflows/release-please.yml release-please-config.json
git commit -m "ci(installer): build, checksum, and publish installer alongside bare exe; route VPS to installer"
```

---

## Task 12: README update

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Read the current "Install on a Windows lab machine" section**

Run: `grep -n "Install on a Windows lab machine" README.md`
Note the line number; the section runs from there to "After install:" or the next `##` heading.

- [ ] **Step 2: Replace the section**

Edit `README.md` — replace the existing numbered-list of "1. Copy SerialHop.exe…" instructions with:

```markdown
## Install on a Windows lab machine

1. Download `SerialHop-Setup-vX.Y.Z.exe` from the [releases page](https://github.com/bioexperiment-lab-devices/serialhop/releases/latest) or from the lab management UI.
2. Double-click the installer. Approve the UAC prompt.
3. Accept the default install location (`C:\Program Files\SerialHop`) or browse to a custom path. Click **Install**.
4. SerialHop opens automatically. The panel pops up a **Set credentials** dialog — enter your `lab_bridge.user` and `lab_bridge.pass`; the panel verifies them against the lab-bridge server and writes them to the config file.
5. Click **Install** in the panel. UAC prompts; approve. The service is registered as `SerialHop` (auto-start at boot, runs as LocalSystem) and started immediately.

A desktop shortcut named **SerialHop** is created during step 3 and points at `<install_dir>\SerialHop.exe`. The shortcut name is intentionally version-less: subsequent auto-updates rename the binary in place under the same filename, so the icon keeps working across releases.

Re-running the installer on an already-installed machine:

- Same version: refreshes the desktop shortcut and exits with "already installed" — no service restart.
- Newer installer: performs an in-place upgrade (stop service → rename → start service, with rollback on failure).
- Older installer: refused unless re-run with `--allow-downgrade`.

Silent / scripted installs (admin shell):

```
SerialHop-Setup-vX.Y.Z.exe --silent --dir "C:\Program Files\SerialHop" --no-shortcut
```

Flags: `--silent` (no dialog; implies `--no-launch`), `--dir <path>`, `--no-launch`, `--no-shortcut`, `--allow-downgrade`, `--version`.

### Manual install (advanced)

If you prefer to copy the binary by hand (e.g., on an air-gapped box):

1. Copy `SerialHop.exe` to an install location (e.g., `C:\Program Files\SerialHop\` or `C:\Tools\SerialHop\`).
2. Double-click the `.exe`. The control panel opens.
3. Enter credentials, click **Install** in the panel.

After install (either path):

- The service runs across reboots without the panel being open.
- To apply config changes: edit the YAML file, then click **Restart** in the panel.
- To remove: click **Uninstall** in the panel, then delete the install directory.
- Logs go to `%ProgramData%\SerialHop\logs\` (`SerialHop.log` for slog JSON, `SerialHop_stderr.log` for chisel state and panic traces, both rotated at 10 MB with 3 backups). Click **Open logs folder** to open the directory in Explorer.
- Config lives at `%ProgramData%\SerialHop\SerialHop_config.yaml`. Click **Open config file** to edit.
```

The original "After install:" block becomes the trailing block of this section as shown above.

- [ ] **Step 3: Verify the section reads cleanly**

Run: `sed -n '/^## Install on a Windows lab machine/,/^## /p' README.md`
Expected: section starts with the new installer steps and ends just before the next `##` heading. Manual-install fallback is present.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: lead Windows install with installer; keep manual copy as advanced fallback"
```

---

## Task 13: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Run the full test + lint suite**

Run:

```bash
gofmt -l .                # must print nothing
go vet ./...
go test -race -count=1 ./...
```

Expected: gofmt clean, vet clean, all tests pass.

If `golangci-lint` is installed:

```bash
golangci-lint run
```

Expected: clean.

- [ ] **Step 2: Cross-platform compile sanity**

Run:

```bash
echo "stub" > tools/installer/payload/SerialHop.exe
GOOS=windows GOARCH=amd64 go build ./...
GOOS=linux  GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
rm tools/installer/payload/SerialHop.exe
```

Expected: all three build cleanly.

- [ ] **Step 3: Check pr.yml compatibility**

Run: `cat .github/workflows/pr.yml`
Confirm: no changes needed (the verify job's `go test ./...` and friends now cover the new package, the `_other.go` stubs ensure cross-platform compile, the shortcut round-trip test is Windows-only via build tag).

- [ ] **Step 4: Push the branch and open a PR**

Run:

```bash
git push -u origin worktree-installer-design
gh pr create --title "feat: ship installer with unversioned desktop shortcut and in-place upgrade" --body "$(cat <<'EOF'
## Summary
- Adds a bespoke Go installer (`SerialHop-Setup-vX.Y.Z.exe`) that copies SerialHop.exe to `C:\Program Files\SerialHop\`, drops an unversioned desktop shortcut, and launches the panel.
- Reuses `internal/winsvc.updateBinary` (now exported as `InstallOrUpgrade`) for the rename-with-rollback swap so in-place upgrades inherit the auto-update flow's safety properties.
- CI now publishes both the installer and the bare exe to the GitHub release; the VPS upload switches to the installer.
- Detects same-version re-runs (no-op + shortcut refresh) and refuses downgrades unless `--allow-downgrade` is passed.

## Test plan
- [ ] Manual: double-click the installer on a clean Windows VM → installs to default path, panel opens, credentials dialog appears.
- [ ] Manual: run installer with `--dir D:\SerialHop --silent --no-launch` → silent install to chosen path, no GUI.
- [ ] Manual: re-run same installer on an installed machine → "already installed" status, no service restart.
- [ ] Manual: install older release (e.g., v0.6.x) over a newer one → refused unless `--allow-downgrade`.
- [ ] Manual: run newer installer over older → in-place upgrade, service restarts, desktop icon still works.
- [ ] CI: release-build job produces both `SerialHop-vX.Y.Z.exe` and `SerialHop-Setup-vX.Y.Z.exe`; SHA256SUMS covers both.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

The PR title is a Conventional Commit and will become the squash commit on `main`; release-please will bump the minor on the next release.

- [ ] **Step 5: Watch CI**

Confirm:
- `pr.yml` verify job passes (gofmt, vet, lint, race, vuln).
- The PR auto-fires CI (release-please app token is wired, per `CLAUDE.md`).

If anything is red, investigate at the failure site rather than re-trying.

---

## Self-Review

This section is a checklist for the plan author — not for the implementer.

**Spec coverage check** (each section maps to a task):

| Spec section | Implementing tasks |
|---|---|
| §1 Purpose & scope | All tasks |
| §2 Install location | Task 5 (default dir constant + opts.InstallDir), Task 8 (`defaultInstallDir`), Task 12 (README) |
| §3.1 Directory layout | Task 3 (skeleton), Task 9 (version.json/manifest) |
| §3.2 Tooling helpers | Task 9 (`tools/render-installer-manifest`) |
| §3.3 Taskfile changes | Task 10 |
| §3.4 Installer's version metadata | Task 9 (version.json) + Task 11 (CI sync of integer fields + release-please extra-files) |
| §4.1 Flags | Task 8 (main.go flag definitions) |
| §4.2 UAC manifest | Task 9 (manifest.template.xml) + Task 8 (elevation_windows.go fallback check) |
| §4.3 Embedded payload | Task 8 (`//go:embed`) + Task 10 (Taskfile copies dist/SerialHop.exe into payload/ before build) |
| §4.4 Version check & flow gating | Task 5 (`detectState`, dispatch in `Run`), Task 4 (PE version read) |
| §4.5 Same-version no-op | Task 5 (`runSameVersion`) + test (Step 7-8) |
| §4.6 Downgrade refusal | Task 5 (Run's switch) + tests (Step 9-12) |
| §5 Install flow | Task 5 (`runInstallOrUpgrade`) + tests (Steps 5-6, 13-14, 17-20) |
| §6 Desktop shortcut | Task 6 (COM IShellLink wrapper + round-trip test) |
| §7 CI changes | Task 11 |
| §8 VPS interaction | Task 11 (curl swap) |
| §9 Package & code changes | Tasks 1-9 |
| §10 Testing | Task 5 (cross-platform fakes + 9 cases) + Task 6 (Windows round-trip) + Task 1 (Compare) + Task 2 (InstallOrUpgrade) |
| §11 Logging | Task 5 (slog.Info/Warn calls in install.go) + Task 8 (`configureLogging` writes to `%TEMP%`) |
| §12 Error response surface | Task 5 (Result messages) + Task 8 (elevation check error) |
| §13 Compatibility | No-op (additive; documented in README in Task 12) |
| §14 Build / release | Tasks 9, 10, 11 |
| §15 Security | No standalone task; satisfied by the design choices throughout (admin manifest, no network bootstrap, no payload exec from installer process) |

**Placeholder scan:** No `TBD`, `TODO`, `implement later`, `add appropriate X`, or vague references. Every step contains the exact code or command.

**Type consistency check:** `Runner`, `options`, `Result`, `State`, `fsOps`, `versionReader`, `shortcutWriter`, `shortcutOpts`, `launcher`, `scmDialer`, `realFS`, `peReader`, `realShortcutWriter`, `realLauncher` are all named consistently across tasks 5, 6, 7, 8. `winsvc.InstallOrUpgrade` is defined in Task 2 and consumed in Task 5. `updater.Compare` is defined in Task 1 and consumed in Task 5.

**Scope:** This plan produces one installer binary, the CI pipeline that publishes it, and the README docs. It does not modify the panel auto-update flow, does not add Start Menu / Add-Remove-Programs / per-user installs (all explicitly out of scope per spec §1).
