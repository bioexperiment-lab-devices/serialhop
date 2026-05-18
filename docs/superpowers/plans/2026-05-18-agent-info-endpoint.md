# Agent Info Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `GET /agent/info` endpoint to the agent's existing REST API that returns the running SerialHop's version, build SHA, OS, arch, hostname, machine ID (Windows-only), and uptime, so the lab-bridge server can pull this data via the existing reverse tunnel.

**Architecture:** New `internal/agentinfo` package owns data gathering; one new handler is appended to `internal/api/handlers.go` and registered on the existing mux. No changes to chisel routing, lab-bridge protocol, or any other component. Machine ID is read from `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` on Windows; omitted everywhere else.

**Tech Stack:** Go 1.22+ standard library (`net/http`, `runtime`, `os`, `encoding/json`); `golang.org/x/sys/windows/registry` for Windows MachineGuid (already in `go.sum`).

**Spec:** [docs/superpowers/specs/2026-05-18-agent-info-endpoint-design.md](../specs/2026-05-18-agent-info-endpoint-design.md)

---

## Task 1: Create `agentinfo` package with primary fields

Builds the package skeleton with `version`, `os`, `arch`, `hostname`, `uptime_seconds` fields. `machine_id` and `build_sha` come in later tasks.

**Files:**
- Create: `internal/agentinfo/agentinfo.go`
- Create: `internal/agentinfo/agentinfo_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/agentinfo/agentinfo_test.go`:

```go
package agentinfo

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

func TestSnapshot_VersionFromInternalVersion(t *testing.T) {
	orig := internalversion.Version
	t.Cleanup(func() { internalversion.Version = orig })
	internalversion.Version = "1.2.3+deadbee"

	got := Snapshot()
	if got.Version != "1.2.3+deadbee" {
		t.Errorf("Version: got %q, want %q", got.Version, "1.2.3+deadbee")
	}
}

func TestSnapshot_OSAndArchMatchRuntime(t *testing.T) {
	got := Snapshot()
	if got.OS != runtime.GOOS {
		t.Errorf("OS: got %q, want %q", got.OS, runtime.GOOS)
	}
	if got.Arch != runtime.GOARCH {
		t.Errorf("Arch: got %q, want %q", got.Arch, runtime.GOARCH)
	}
}

func TestSnapshot_HostnameMatchesOS(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	got := Snapshot()
	if got.Hostname != want {
		t.Errorf("Hostname: got %q, want %q", got.Hostname, want)
	}
}

func TestSnapshot_UptimeSecondsMonotonic(t *testing.T) {
	first := Snapshot().UptimeSeconds
	time.Sleep(1100 * time.Millisecond)
	second := Snapshot().UptimeSeconds
	if second < first {
		t.Errorf("uptime went backwards: first=%d second=%d", first, second)
	}
	if second-first < 1 {
		t.Errorf("uptime did not advance after 1.1s sleep: first=%d second=%d", first, second)
	}
}

func TestSnapshot_JSONShape(t *testing.T) {
	got := Snapshot()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{`"version"`, `"os"`, `"arch"`, `"hostname"`, `"uptime_seconds"`} {
		if !strings.Contains(s, key) {
			t.Errorf("required key %s missing from %s", key, s)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agentinfo/...`
Expected: FAIL — `agentinfo` package not found.

- [ ] **Step 3: Implement `agentinfo.go`**

Create `internal/agentinfo/agentinfo.go`:

```go
// Package agentinfo gathers the runtime self-description served by
// GET /agent/info. The endpoint exists so the lab-bridge server can pull
// version / OS / machine identity from each connected client; see
// docs/superpowers/specs/2026-05-18-agent-info-endpoint-design.md.
package agentinfo

import (
	"os"
	"runtime"
	"time"

	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// Info is the JSON payload returned by GET /agent/info.
type Info struct {
	Version       string `json:"version"`
	BuildSHA      string `json:"build_sha,omitempty"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	MachineID     string `json:"machine_id,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// startedAt is captured at package init. The agent imports agentinfo from
// the long-running app.Run path, so this is a close approximation of
// process start. Off by at most the time between binary entry and the
// first agentinfo reference.
var startedAt = time.Now()

// Snapshot returns the current Info. Each field is gathered independently
// — a failure in one (e.g. os.Hostname returning an error) sets that field
// to its zero value and continues. The endpoint is best-effort and must
// never fail.
func Snapshot() Info {
	host, _ := os.Hostname() // empty on error — handler still returns 200
	return Info{
		Version:       internalversion.Version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Hostname:      host,
		UptimeSeconds: int64(time.Since(startedAt).Seconds()),
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agentinfo/... -count=1`
Expected: PASS, all 5 test cases.

- [ ] **Step 5: Commit**

```bash
git add internal/agentinfo/agentinfo.go internal/agentinfo/agentinfo_test.go
git commit -m "feat: add internal/agentinfo package with primary fields"
```

---

## Task 2: Extract `build_sha` from version string

Adds the `BuildSHA` field, parsed as the segment after `+` in `internalversion.Version`. Omitted when there is no `+`.

**Files:**
- Modify: `internal/agentinfo/agentinfo.go`
- Modify: `internal/agentinfo/agentinfo_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/agentinfo/agentinfo_test.go`:

```go
func TestSnapshot_BuildSHAFromVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{"release with describe suffix", "0.27.1+abc1234", "abc1234"},
		{"plain semver", "0.27.1", ""},
		{"dev default", "dev", ""},
		{"multi-plus stays at first", "0.27.1+abc+xyz", "abc+xyz"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := internalversion.Version
			t.Cleanup(func() { internalversion.Version = orig })
			internalversion.Version = tc.version
			if got := Snapshot().BuildSHA; got != tc.want {
				t.Errorf("BuildSHA(%q): got %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

func TestInfoJSON_OmitsBuildSHAWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Info{Version: "dev", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"build_sha"`) {
		t.Errorf("build_sha should be omitted when empty: %s", b)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agentinfo/... -count=1 -run BuildSHA`
Expected: FAIL — `BuildSHA` is empty for `"0.27.1+abc1234"` (currently never populated).

- [ ] **Step 3: Implement `extractBuildSHA` and call it from `Snapshot`**

Edit `internal/agentinfo/agentinfo.go`. Add to the import block (above the existing imports):

```go
	"strings"
```

After the `Info` struct, add:

```go
// extractBuildSHA returns everything after the first '+' in v, or "" if
// there is no '+'. Mirrors the format produced by tools/buildcmd which
// concatenates the assets/version.json string with a git-describe suffix.
func extractBuildSHA(v string) string {
	i := strings.IndexByte(v, '+')
	if i < 0 {
		return ""
	}
	return v[i+1:]
}
```

Modify the `Snapshot` function — add the `BuildSHA` field:

```go
func Snapshot() Info {
	host, _ := os.Hostname()
	return Info{
		Version:       internalversion.Version,
		BuildSHA:      extractBuildSHA(internalversion.Version),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Hostname:      host,
		UptimeSeconds: int64(time.Since(startedAt).Seconds()),
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agentinfo/... -count=1`
Expected: PASS — all `BuildSHA` cases plus the `omitempty` JSON test.

- [ ] **Step 5: Commit**

```bash
git add internal/agentinfo/agentinfo.go internal/agentinfo/agentinfo_test.go
git commit -m "feat(agentinfo): extract build_sha from version string"
```

---

## Task 3: Cross-platform `machine_id` with Windows registry read

Adds two build-tagged files: one for Windows that reads `MachineGuid` from the registry, one for everything else that returns `""`. Wires the result into `Snapshot()`. Adds a Windows-only registry test.

**Files:**
- Create: `internal/agentinfo/machineid_windows.go` (`//go:build windows`)
- Create: `internal/agentinfo/machineid_other.go` (`//go:build !windows`)
- Create: `internal/agentinfo/machineid_windows_test.go` (`//go:build windows`)
- Modify: `internal/agentinfo/agentinfo.go`
- Modify: `internal/agentinfo/agentinfo_test.go`

- [ ] **Step 1: Write the failing cross-platform tests**

Append to `internal/agentinfo/agentinfo_test.go`:

```go
func TestInfoJSON_OmitsMachineIDWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Info{Version: "dev", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"machine_id"`) {
		t.Errorf("machine_id should be omitted when empty: %s", b)
	}
}

func TestInfoJSON_IncludesMachineIDWhenPresent(t *testing.T) {
	b, err := json.Marshal(Info{Version: "dev", OS: "windows", Arch: "amd64", MachineID: "AAAA-BBBB"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"machine_id":"AAAA-BBBB"`) {
		t.Errorf("machine_id should be present when set: %s", b)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agentinfo/... -count=1 -run MachineID`
Expected: One of the two tests fails — `TestInfoJSON_OmitsMachineIDWhenEmpty` may already pass (zero-value `Info{}` has empty `MachineID`), but `TestInfoJSON_IncludesMachineIDWhenPresent` requires the `MachineID` field exists on `Info` — it already does from Task 1. Verify behavior is correct before proceeding.

If both tests already pass, skip to Step 3 with this note: the JSON contract is already satisfied; we still need to *populate* `MachineID` in `Snapshot()` on Windows.

- [ ] **Step 3: Create the non-Windows stub**

Create `internal/agentinfo/machineid_other.go`:

```go
//go:build !windows

package agentinfo

// readMachineID returns the host's stable machine identifier. On non-Windows
// platforms there is no canonical source comparable to the Windows registry
// MachineGuid, so we return "" — Snapshot() then leaves Info.MachineID at
// its zero value and the field is omitted from JSON via `omitempty`.
//
// The production fleet runs on Windows; macOS/Linux are dev builds where
// the missing field is acceptable per the design spec.
func readMachineID() string {
	return ""
}
```

- [ ] **Step 4: Create the Windows implementation**

Create `internal/agentinfo/machineid_windows.go`:

```go
//go:build windows

package agentinfo

import (
	"log/slog"

	"golang.org/x/sys/windows/registry"
)

// readMachineID returns HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid,
// the stable per-Windows-install identifier. Returns "" on any error
// (registry locked, key missing, permission denied) — Snapshot() then
// omits the field from the JSON response rather than failing the request.
func readMachineID() string {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY,
	)
	if err != nil {
		slog.Warn("agentinfo: open Cryptography key", "err", err)
		return ""
	}
	defer k.Close()

	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		slog.Warn("agentinfo: read MachineGuid", "err", err)
		return ""
	}
	return v
}
```

`registry.WOW64_64KEY` is included so a 32-bit build reads the 64-bit hive (Windows redirects under WOW64); the production target is 64-bit but this avoids surprises on any 32-bit dev probe.

- [ ] **Step 5: Wire `readMachineID` into `Snapshot()`**

In `internal/agentinfo/agentinfo.go`, modify `Snapshot()`:

```go
func Snapshot() Info {
	host, _ := os.Hostname()
	return Info{
		Version:       internalversion.Version,
		BuildSHA:      extractBuildSHA(internalversion.Version),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Hostname:      host,
		MachineID:     readMachineID(),
		UptimeSeconds: int64(time.Since(startedAt).Seconds()),
	}
}
```

- [ ] **Step 6: Create the Windows-only registry test**

Create `internal/agentinfo/machineid_windows_test.go`:

```go
//go:build windows

package agentinfo

import (
	"regexp"
	"testing"
)

// machineGuidPattern matches the standard Windows MachineGuid format
// (UUID, lowercase, hyphenated). We do not require an exact UUID v4 —
// older Windows versions emit a slightly different encoding.
var machineGuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func TestReadMachineID_ReturnsRealGUID(t *testing.T) {
	got := readMachineID()
	if got == "" {
		t.Skip("MachineGuid registry read failed (locked-down CI?); skipping")
	}
	if !machineGuidPattern.MatchString(got) {
		t.Errorf("MachineGuid format unexpected: %q", got)
	}
}
```

- [ ] **Step 7: Tidy module dependencies**

The new Windows-only import `golang.org/x/sys/windows/registry` may or may not be already in `go.mod`'s `require` directive (it likely is — the service worker already uses sibling subpackages of `golang.org/x/sys/windows`). Run `go mod tidy` to make the requirement explicit if not.

Run: `go mod tidy`
Expected: no errors. `git diff go.mod go.sum` may show additions; that is correct and should be included in the commit.

- [ ] **Step 8: Run all tests cross-platform**

On the dev machine (macOS in this worktree):

Run: `go test ./internal/agentinfo/... -count=1`
Expected: PASS. The Windows-only test is excluded by the build tag.

Sanity: confirm the package builds for Windows too (no need to run tests):

Run: `GOOS=windows GOARCH=amd64 go build ./internal/agentinfo/...`
Expected: clean build, exit 0.

- [ ] **Step 9: Commit**

```bash
git add internal/agentinfo/machineid_windows.go internal/agentinfo/machineid_other.go internal/agentinfo/machineid_windows_test.go internal/agentinfo/agentinfo.go internal/agentinfo/agentinfo_test.go go.mod go.sum
git commit -m "feat(agentinfo): add machine_id field with Windows registry read"
```

---

## Task 4: Register `GET /agent/info` handler on the existing mux

Adds the handler method on `*Server`, registers it on the existing mux, and adds an integration test that goes through the same `Handler()` pipeline as every other route (so middleware is exercised).

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/api/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/api/handlers_test.go`:

```go
func TestGetAgentInfo_200JSON(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/agent/info", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}

	var got map[string]any
	decode(t, rec.Body, &got)
	for _, key := range []string{"version", "os", "arch", "hostname", "uptime_seconds"} {
		if _, ok := got[key]; !ok {
			t.Errorf("required key %q missing from response: %v", key, got)
		}
	}
}

func TestGetAgentInfo_RejectsNonGET(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/agent/info", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /agent/info: got %d, want 405", method, rec.Code)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/... -count=1 -run AgentInfo`
Expected: FAIL — `/agent/info` is not registered, so the mux returns 404 instead of 200/405.

- [ ] **Step 3: Add the handler method**

Edit `internal/api/handlers.go`. Add `"github.com/bioexperiment-lab-devices/serialhop/internal/agentinfo"` to the imports.

Add the route registration in `Handler()` between the existing `POST /flash/{port}` line and the `return logMiddleware(mux)`:

```go
	mux.HandleFunc("GET /agent/info", s.handleGetAgentInfo)
```

Append the handler implementation at the end of the file:

```go
// handleGetAgentInfo returns the agent's self-description for server-side
// polling. Best-effort: never fails. See
// docs/superpowers/specs/2026-05-18-agent-info-endpoint-design.md.
func (s *Server) handleGetAgentInfo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(agentinfo.Snapshot())
}
```

The handler is a method on `*Server` for consistency with the surrounding handlers, but it uses no fields of `Server` — the receiver is purely cosmetic. Reviewers may convert to a plain function; both shapes work with the existing mux.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/... -count=1 -run AgentInfo`
Expected: PASS — both `TestGetAgentInfo_200JSON` and `TestGetAgentInfo_RejectsNonGET`.

Then run the full `api` package test to confirm no regressions:

Run: `go test ./internal/api/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/handlers.go internal/api/handlers_test.go
git commit -m "feat(api): expose GET /agent/info for server-pulled agent state"
```

---

## Task 5: Pre-flight verification (matches `pr.yml` `verify` job)

Runs the full local CI suite per `CLAUDE.md`. Catches any formatting / linting / cross-platform-build / vuln-check failures before the PR.

**Files:** none (verification only).

- [ ] **Step 1: Format check**

Run: `gofmt -l .`
Expected: empty output (no files need formatting). If any file is listed, run `gofmt -w <file>`, re-run, then `git add` and amend the last commit or create a fixup commit.

- [ ] **Step 2: Static analysis**

Run: `go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Lint**

Run: `golangci-lint run`
Expected: no output, exit 0. If `golangci-lint` is not installed, install via `brew install golangci-lint` (macOS) or follow the project's existing tooling docs.

- [ ] **Step 4: Tests with race detector**

Run: `go test -race -count=1 ./...`
Expected: PASS. The `internal/agentinfo` and `internal/api` packages are the new test surface; everything else must keep passing.

- [ ] **Step 5: Cross-platform build sanity**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: clean build, exit 0. Confirms the Windows-only `machineid_windows.go` compiles.

Run: `GOOS=linux GOARCH=amd64 go build ./...`
Expected: clean build, exit 0. Confirms the `machineid_other.go` stub compiles.

- [ ] **Step 6: Vuln check**

Run: `govulncheck ./...`
Expected: no findings related to the new code. Existing transitive findings (if any) are out of scope.

- [ ] **Step 7: Confirm the goal is met**

Run a manual smoke against a local binary:

```bash
go run ./cmd/serialhop --help 2>&1 | head -5
```

This just verifies the build links cleanly with the new package. (Full end-to-end against a real lab-bridge requires the server-side polling work, which is out of scope per the spec.)

No commit needed — this task gates the PR rather than producing artifacts.

---

## Done. The PR title should be:

```
feat: expose GET /agent/info for server-pulled agent state
```

(`feat:` because release-please then bumps the minor version on next release per `CLAUDE.md`.)
