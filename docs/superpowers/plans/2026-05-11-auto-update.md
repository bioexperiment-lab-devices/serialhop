# Auto-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an in-app auto-update flow to the Windows control panel: detect a newer GitHub release, download + SHA-256-verify the new `.exe`, and perform a UAC-gated rename-shuffle install with auto-rollback.

**Architecture:** A new pure-Go `internal/updater` package wraps the GitHub Releases API, file download, and checksum verification. A new `update` admin action in `internal/winsvc` performs the elevated rename + service-restart with auto-rollback. The panel grows a state machine (`internal/panel/update_state.go`) and a row of UI controls wired through the existing UAC mechanism.

**Tech Stack:** Go (stdlib `net/http`, `crypto/sha256`, `bufio`, `encoding/json`), Windows SCM via existing `internal/winsvc`, `lxn/walk` for the new UI row, PowerShell for the release workflow checksum fix.

**Reference spec:** `docs/superpowers/specs/2026-05-11-auto-update-design.md`

---

## File Structure

**New files:**

- `internal/updater/release.go` — `LatestRelease(ctx, http, url) (Release, error)` and types.
- `internal/updater/release_test.go` — httptest-driven coverage.
- `internal/updater/version.go` — `IsNewer(remote, local) (bool, error)` semver comparison.
- `internal/updater/version_test.go` — table-driven coverage.
- `internal/updater/download.go` — `Download(ctx, http, url, destPath, progress)` streams to a `.partial` file then renames into place.
- `internal/updater/download_test.go` — httptest-driven coverage incl. cancel.
- `internal/updater/verify.go` — `VerifyFile(filePath, sumsBody, filename) error`.
- `internal/updater/verify_test.go` — coverage for standard `sha256sum`-format parsing.
- `internal/panel/update_state.go` — pure `UpdateState` enum + transitions; no Windows deps.
- `internal/panel/update_state_test.go` — state-machine coverage.

**Modified files:**

- `internal/config/config.go` — add `AutoUpdateConfig`, default `true`, scaffold section.
- `internal/config/load_test.go` — coverage for the new field.
- `internal/winsvc/control.go` — `RunAdminAction` signature grows `updateSrc`; new `updateBinary` func + `fsOps` interface for testability.
- `internal/winsvc/control_test.go` — coverage for `updateBinary` happy path and rollbacks.
- `internal/panel/elevate.go` — `RunElevatedAdminAction(action, extraArgs ...string)`; existing callers compile unchanged.
- `internal/panel/panel.go` — new update row, periodic check goroutine, button wiring; on-startup cleanup of `SerialHop.exe.old`; post-check cleanup of stale `SerialHop-v*.exe`.
- `cmd/serialhop/main.go` — new `--update-src` flag; pass to `RunAdminAction`.
- `.github/workflows/release-please.yml` — fix `SHA256SUMS.txt` format to standard `sha256sum` shape.
- `README.md` — short "Auto-update" subsection.

**Untouched:** `internal/api/`, `internal/discovery/`, `internal/serial/`, `internal/chisel/`, `internal/registry/`, `internal/logship/`, `internal/version/`, `Taskfile.yaml`, release-please config.

---

## Branch setup

- [ ] **Switch off the spec branch onto a fresh feature branch**

Run:
```bash
git checkout main
git checkout -b feat/auto-update
git cherry-pick docs/auto-update-spec
```

Expected: `feat/auto-update` contains the spec commit and is based off `main`.

Alternative: keep working on `docs/auto-update-spec` and rename it at the end. Either works — the rest of the plan assumes `feat/auto-update`.

---

## Task 1: Release workflow — emit standard SHA256SUMS.txt

**Files:**
- Modify: `.github/workflows/release-please.yml:98-103` (the "rename and checksum" step)

The current `Get-FileHash | Format-List` produces unparseable output for both `shasum -c` (per the README) and our in-app parser. Replace with a one-line-per-file emit in the standard `<lowercase-hex>  <filename>` shape.

- [ ] **Step 1: Edit the workflow step**

Replace the body of the `rename and checksum` step:

```yaml
      - name: rename and checksum
        shell: pwsh
        run: |
          $tag = "${{ needs.release-please.outputs.tag_name }}"
          Move-Item dist\SerialHop.exe "dist\SerialHop-$tag.exe"
          Get-FileHash -Algorithm SHA256 dist\*.exe | ForEach-Object {
            "$($_.Hash.ToLower())  $([System.IO.Path]::GetFileName($_.Path))"
          } | Out-File -Encoding ascii dist\SHA256SUMS.txt
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/release-please.yml
git commit -m "ci: emit SHA256SUMS.txt in standard sha256sum format

Replaces PowerShell Format-List output (which was unparseable by shasum -c
per the README's documented manual-verify command) with the one-line-per-file
'<lowercase-hex>  <filename>' format. Required by the upcoming auto-update
parser and incidentally fixes the manual-verify path."
```

---

## Task 2: Config — `auto_update.enabled`

**Files:**
- Modify: `internal/config/config.go:8-14` (Config struct), `:41-58` (Default), `:60-86` (scaffold template)
- Modify: `internal/config/load_test.go` (add test case)

- [ ] **Step 1: Write the failing test**

Append to `internal/config/load_test.go`:

```go
func TestLoad_AutoUpdateDisabled(t *testing.T) {
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9000
rest:
  port: 0
log:
  level: "info"
auto_update:
  enabled: false
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AutoUpdate.Enabled {
		t.Errorf("auto_update.enabled: got true, want false")
	}
}

func TestLoad_AutoUpdateDefaultsToTrue(t *testing.T) {
	// A config file written by an older binary has no auto_update section.
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9000
rest:
  port: 0
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.AutoUpdate.Enabled {
		t.Errorf("auto_update.enabled: got false, want true (default)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/config/ -run 'TestLoad_AutoUpdate' -v
```

Expected: compile error — `AutoUpdate` field not defined on `Config`.

- [ ] **Step 3: Add the config struct, default, and scaffold**

Edit `internal/config/config.go`:

```go
type Config struct {
	Chisel     ChiselConfig     `yaml:"chisel"`
	Rest       RestConfig       `yaml:"rest"`
	Discovery  DiscoveryConfig  `yaml:"discovery"`
	Log        LogConfig        `yaml:"log"`
	RawSerial  RawSerialConfig  `yaml:"raw_serial"`
	AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
}
```

After `RawSerialConfig`:

```go
type AutoUpdateConfig struct {
	Enabled bool `yaml:"enabled"`
}
```

In `Default()`, append:

```go
		AutoUpdate: AutoUpdateConfig{Enabled: true},
```

Append to the `scaffoldTemplate` string (after the `raw_serial` block):

```
auto_update:
  enabled: true                   # check GitHub Releases for newer versions
                                  # and offer to install them from the panel.
                                  # set to false on air-gapped lab boxes.
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/config/ -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/load_test.go
git commit -m "feat(config): add auto_update.enabled flag (default true)"
```

---

## Task 3: `internal/updater/version.go` — semver comparison

**Files:**
- Create: `internal/updater/version.go`
- Create: `internal/updater/version_test.go`

Goal: a pure function that returns `true` iff a remote tag (e.g., `v0.7.0`) is strictly newer than a local version string (which may carry the `0.6.1+v0.6.1-7-gabc1234-dirty` build-meta suffix the existing `internal/version.Version` produces). No external semver dependency — we only need major / minor / patch comparison and stripping logic.

- [ ] **Step 1: Write the failing test**

Create `internal/updater/version_test.go`:

```go
package updater

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		local  string
		want   bool
		wantOK bool // false → expect error
	}{
		{"strict greater patch", "v0.7.0", "0.6.1", true, true},
		{"older patch", "v0.6.0", "0.6.1", false, true},
		{"equal", "v0.7.0", "0.7.0", false, true},
		{"strict greater minor", "v0.7.0", "0.6.9", true, true},
		{"strict greater major", "v1.0.0", "0.99.0", true, true},
		{"leading v on local too", "v0.7.0", "v0.6.1", true, true},
		{"no leading v on remote", "0.7.0", "0.6.1", true, true},
		{"local is dev build, remote is newer release", "v0.7.0", "0.6.1+v0.6.1-7-gabc1234-dirty", true, true},
		{"local is dev build matching base", "v0.6.1", "0.6.1+v0.6.1-7-gabc1234-dirty", false, true},
		{"local is dev build older than remote base", "v0.6.2", "0.6.1+v0.6.1-7-gabc1234-dirty", true, true},
		{"malformed remote", "garbage", "0.6.1", false, false},
		{"malformed local", "v0.7.0", "garbage", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsNewer(tc.remote, tc.local)
			if tc.wantOK && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("expected error, got nil")
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
go test ./internal/updater/ -v
```

Expected: package does not exist yet → "no Go files" or similar.

- [ ] **Step 3: Implement `version.go`**

Create `internal/updater/version.go`:

```go
// Package updater implements the in-app auto-update flow: latest-release
// discovery, download, SHA-256 verification, and orchestration of the
// elevated install. See docs/superpowers/specs/2026-05-11-auto-update-design.md.
package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// IsNewer reports whether `remote` is strictly newer than `local`.
//
// Both arguments may have a leading 'v' (e.g., "v0.7.0") and `local` may
// carry the "+buildmeta" suffix produced by the dev-build `-ldflags -X`
// (e.g., "0.6.1+v0.6.1-7-gabc1234-dirty"). Build metadata is stripped
// before comparison — dev builds are treated as equivalent to their base.
//
// Comparison is integer-wise on (major, minor, patch). Pre-release suffixes
// after a '-' on the SemVer side are not currently produced by this project
// and are not handled; if they appear, parse fails.
func IsNewer(remote, local string) (bool, error) {
	r, err := parse(remote)
	if err != nil {
		return false, fmt.Errorf("parse remote: %w", err)
	}
	l, err := parse(local)
	if err != nil {
		return false, fmt.Errorf("parse local: %w", err)
	}
	switch {
	case r.major != l.major:
		return r.major > l.major, nil
	case r.minor != l.minor:
		return r.minor > l.minor, nil
	default:
		return r.patch > l.patch, nil
	}
}

type semver struct{ major, minor, patch int }

func parse(v string) (semver, error) {
	s := strings.TrimPrefix(v, "v")
	// Drop "+buildmeta" if present.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("not X.Y.Z: %q", v)
	}
	out := semver{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, fmt.Errorf("bad component %q in %q", p, v)
		}
		switch i {
		case 0:
			out.major = n
		case 1:
			out.minor = n
		case 2:
			out.patch = n
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/updater/ -v
```

Expected: all cases pass.

- [ ] **Step 5: Commit**

```bash
git add internal/updater/version.go internal/updater/version_test.go
git commit -m "feat(updater): semver comparison with dev-build awareness"
```

---

## Task 4: `internal/updater/release.go` — GitHub API client

**Files:**
- Create: `internal/updater/release.go`
- Create: `internal/updater/release_test.go`

Fetch `https://api.github.com/repos/{owner}/{repo}/releases/latest` and decode the subset of JSON we need: tag name, html URL, and the list of release assets.

- [ ] **Step 1: Write the failing test**

Create `internal/updater/release_test.go`:

```go
package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleReleaseJSON = `{
  "tag_name": "v0.7.0",
  "html_url": "https://github.com/bioexperiment-lab-devices/serialhop/releases/tag/v0.7.0",
  "assets": [
    {"name": "SerialHop-v0.7.0.exe",   "browser_download_url": "https://example.com/serialhop.exe", "size": 41943040},
    {"name": "SHA256SUMS.txt",          "browser_download_url": "https://example.com/sums.txt",      "size": 128}
  ]
}`

func TestLatestRelease_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "SerialHop/") {
			t.Errorf("User-Agent: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleReleaseJSON))
	}))
	defer srv.Close()

	rel, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/0.6.1 (test)")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.TagName != "v0.7.0" {
		t.Errorf("TagName: got %q", rel.TagName)
	}
	if rel.HTMLURL == "" {
		t.Errorf("HTMLURL: empty")
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("Assets: got %d, want 2", len(rel.Assets))
	}
	exe := rel.AssetByName("SerialHop-v0.7.0.exe")
	if exe == nil {
		t.Fatal("AssetByName returned nil for the exe")
	}
	if exe.BrowserDownloadURL == "" {
		t.Errorf("BrowserDownloadURL: empty")
	}
}

func TestLatestRelease_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer srv.Close()

	_, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/0.6.1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("err should mention 403: %v", err)
	}
}

func TestLatestRelease_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{ not json"))
	}))
	defer srv.Close()

	_, err := LatestRelease(context.Background(), srv.Client(), srv.URL, "SerialHop/0.6.1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAssetByName_NotFound(t *testing.T) {
	rel := Release{Assets: []Asset{{Name: "other.exe"}}}
	if rel.AssetByName("missing.exe") != nil {
		t.Error("expected nil for missing asset")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/updater/ -run TestLatestRelease -v
```

Expected: compile error — `LatestRelease`, `Release`, `Asset`, `AssetByName` undefined.

- [ ] **Step 3: Implement `release.go`**

Create `internal/updater/release.go`:

```go
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// DefaultReleasesURL is the GitHub API endpoint for the project's latest release.
const DefaultReleasesURL = "https://api.github.com/repos/bioexperiment-lab-devices/serialhop/releases/latest"

// Release is the subset of the GitHub Releases API payload we care about.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one binary attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// AssetByName returns the first asset with the given filename, or nil if absent.
func (r Release) AssetByName(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// LatestRelease GETs `url` (typically DefaultReleasesURL) and returns the
// decoded release. The caller owns the timeout via ctx.
func LatestRelease(ctx context.Context, http *http.Client, url, userAgent string) (Release, error) {
	req, err := newRequest(ctx, url, userAgent)
	if err != nil {
		return Release{}, err
	}
	resp, err := http.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Release{}, fmt.Errorf("get %s: HTTP %d: %s", url, resp.StatusCode, string(body))
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("decode %s: %w", url, err)
	}
	return rel, nil
}

func newRequest(ctx context.Context, url, userAgent string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	return req, nil
}
```

Note: the `http *http.Client` parameter shadowing the package name is awkward — rename it. Replace the function body's `http.NewRequestWithContext` call accordingly. Use a different parameter name in the final code:

```go
func LatestRelease(ctx context.Context, hc *http.Client, url, userAgent string) (Release, error) {
	req, err := newRequest(ctx, url, userAgent)
	if err != nil {
		return Release{}, err
	}
	resp, err := hc.Do(req)
	// ... rest unchanged
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/updater/ -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/updater/release.go internal/updater/release_test.go
git commit -m "feat(updater): GitHub Releases API client"
```

---

## Task 5: `internal/updater/verify.go` — SHA-256 verification

**Files:**
- Create: `internal/updater/verify.go`
- Create: `internal/updater/verify_test.go`

Parse standard `<lowercase-hex>  <filename>` lines and compare the file's SHA-256 against the entry for the requested filename.

- [ ] **Step 1: Write the failing test**

Create `internal/updater/verify_test.go`:

```go
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifyFile_OK(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello world")
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", body)

	sums := sha256Hex(body) + "  SerialHop-v0.7.0.exe\n" +
		sha256Hex([]byte("other")) + "  other.exe\n"

	if err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe"); err != nil {
		t.Errorf("VerifyFile: %v", err)
	}
}

func TestVerifyFile_Mismatch(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", []byte("hello world"))

	sums := sha256Hex([]byte("DIFFERENT")) + "  SerialHop-v0.7.0.exe\n"
	err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("err: %v", err)
	}
}

func TestVerifyFile_FilenameNotInSums(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", []byte("hello world"))

	sums := sha256Hex([]byte("anything")) + "  SerialHop-v0.6.0.exe\n"
	err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err: %v", err)
	}
}

func TestVerifyFile_MalformedLineSkipped(t *testing.T) {
	// A malformed line should not crash the parser; the well-formed entry
	// after it should still resolve.
	dir := t.TempDir()
	body := []byte("hello world")
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", body)

	sums := "this line is junk\n" +
		sha256Hex(body) + "  SerialHop-v0.7.0.exe\n"

	if err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe"); err != nil {
		t.Errorf("VerifyFile: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/updater/ -run TestVerifyFile -v
```

Expected: `VerifyFile` undefined.

- [ ] **Step 3: Implement `verify.go`**

Create `internal/updater/verify.go`:

```go
package updater

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// VerifyFile computes the SHA-256 of filePath and compares it against the
// entry for `filename` in sumsBody. sumsBody is the body of a standard
// sha256sum-format file ("<lowercase-hex>  <filename>" per line).
//
// Returns an error if the file is missing, the filename isn't in sumsBody,
// or the hash differs.
func VerifyFile(filePath, sumsBody, filename string) error {
	want, ok := lookupSum(sumsBody, filename)
	if !ok {
		return fmt.Errorf("verify: %q not found in checksum file", filename)
	}
	got, err := hashFile(filePath)
	if err != nil {
		return fmt.Errorf("verify: hash %s: %w", filePath, err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("verify: SHA-256 mismatch for %s: expected %s, got %s", filename, want, got)
	}
	return nil
}

func lookupSum(body, filename string) (string, bool) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Standard format: "<hex><space><space><filename>". Some tools
		// emit a single space or a tab; accept any whitespace run between
		// the two fields.
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == filename {
			return fields[0], true
		}
	}
	return "", false
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is a local file we just downloaded; intentional
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/updater/ -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/updater/verify.go internal/updater/verify_test.go
git commit -m "feat(updater): SHA-256 verification against standard sums file"
```

---

## Task 6: `internal/updater/download.go` — streaming download

**Files:**
- Create: `internal/updater/download.go`
- Create: `internal/updater/download_test.go`

Stream the asset URL to `<destPath>.partial`, report progress, fsync, rename into place on success, delete the partial on cancel/error. Same-volume rename is OK because the caller passes a destPath inside the install dir.

- [ ] **Step 1: Write the failing test**

Create `internal/updater/download_test.go`:

```go
package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownload_Success(t *testing.T) {
	body := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "32")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	var lastReceived int64
	progress := func(received, total int64) {
		if received < lastReceived {
			t.Errorf("progress not monotonic: %d → %d", lastReceived, received)
		}
		lastReceived = received
	}

	if err := Download(context.Background(), srv.Client(), srv.URL, dest, "SerialHop/test", progress); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}

	// No .partial should remain.
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should not exist after success: %v", err)
	}
}

func TestDownload_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	err := Download(context.Background(), srv.Client(), srv.URL, dest, "SerialHop/test", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err should mention 404: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dest should not exist on failure")
	}
}

func TestDownload_ContextCancel(t *testing.T) {
	var started int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		// Write a small chunk and flush, then block.
		_, _ = w.Write(make([]byte, 16))
		if flusher != nil {
			flusher.Flush()
		}
		atomic.StoreInt32(&started, 1)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	errCh := make(chan error, 1)
	go func() {
		errCh <- Download(ctx, srv.Client(), srv.URL, dest, "SerialHop/test", nil)
	}()

	// Wait until the server has started streaming.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&started) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on cancel")
	}
	// The .partial file should be cleaned up.
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should be removed on cancel: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist on cancel: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/updater/ -run TestDownload -v
```

Expected: `Download` undefined.

- [ ] **Step 3: Implement `download.go`**

Create `internal/updater/download.go`:

```go
package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// ProgressFunc reports bytes received and total bytes if known (0 if not).
// Called from the download goroutine; implementations must marshal back to
// any UI thread themselves.
type ProgressFunc func(received, total int64)

// Download streams `url` into destPath via a `<destPath>.partial` staging
// file. On success the file is fsynced and atomically renamed to destPath.
// On context cancel or any error the partial file is removed; destPath is
// never partially populated.
//
// The caller owns timeouts via ctx — pass a `context.WithTimeout(parent, 5*time.Minute)`
// for asset downloads.
func Download(ctx context.Context, hc *http.Client, url, destPath, userAgent string, progress ProgressFunc) error {
	req, err := newRequest(ctx, url, userAgent)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("get %s: HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	partial := destPath + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", partial, err)
	}
	// Ensure the partial file is removed on any error path below.
	cleanup := func() { _ = os.Remove(partial) }
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	total := resp.ContentLength
	if err := streamWithProgress(f, resp.Body, total, progress); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync %s: %w", partial, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", partial, err)
	}
	if err := os.Rename(partial, destPath); err != nil {
		return fmt.Errorf("rename %s → %s: %w", partial, destPath, err)
	}
	cleanup = nil // success: keep destPath
	return nil
}

// streamWithProgress copies src → dst, invoking progress every ~64 KiB and
// at completion. Returns the first error from either side.
func streamWithProgress(dst io.Writer, src io.Reader, total int64, progress ProgressFunc) error {
	buf := make([]byte, 32*1024)
	var received int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			received += int64(n)
			if progress != nil {
				progress(received, total)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/updater/ -race -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/updater/download.go internal/updater/download_test.go
git commit -m "feat(updater): streaming download with progress and cancel"
```

---

## Task 7: `internal/winsvc/control.go` — `updateBinary` with rollback

**Files:**
- Modify: `internal/winsvc/control.go` (add `fsOps`, `updateBinary`, error wrapping)
- Modify: `internal/winsvc/control_test.go` (add `fakeFS`, coverage for the six branches)

We keep the `RunAdminAction` signature for this task and add the new dispatcher in Task 8. Building `updateBinary` first lets us test it in isolation.

- [ ] **Step 1: Write the failing test — happy path**

Append to `internal/winsvc/control_test.go`:

```go
// --- updateBinary ---------------------------------------------------------

// fakeFS records calls and lets tests inject failures at specific steps.
type fakeFS struct {
	existing  map[string]bool      // file paths that "exist"
	calls     []string             // ordered call log for assertions
	renameErr map[[2]string]error  // {from,to} → err to return
	removeErr map[string]error
}

func newFakeFS(files ...string) *fakeFS {
	f := &fakeFS{
		existing:  map[string]bool{},
		renameErr: map[[2]string]error{},
		removeErr: map[string]error{},
	}
	for _, p := range files {
		f.existing[p] = true
	}
	return f
}

func (f *fakeFS) Rename(from, to string) error {
	f.calls = append(f.calls, "rename:"+from+"→"+to)
	if err := f.renameErr[[2]string{from, to}]; err != nil {
		return err
	}
	if !f.existing[from] {
		return os.ErrNotExist
	}
	delete(f.existing, from)
	f.existing[to] = true
	return nil
}

func (f *fakeFS) Remove(path string) error {
	f.calls = append(f.calls, "remove:"+path)
	if err := f.removeErr[path]; err != nil {
		return err
	}
	delete(f.existing, path)
	return nil
}

func (f *fakeFS) Exists(path string) bool { return f.existing[path] }

func TestUpdateBinary_HappyPath_ServiceRunning(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("post-update SerialHop.exe missing")
	}
	if fs.Exists("C:\\bin\\SerialHop.exe.old") {
		t.Error(".old should be cleaned up best-effort on success")
	}
	if !scm.services[ServiceName].started {
		t.Error("service should be restarted")
	}
}

func TestUpdateBinary_ServiceNotInstalled(t *testing.T) {
	scm := newFakeSCM() // no services
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("post-update SerialHop.exe missing")
	}
}

func TestUpdateBinary_ServiceAlreadyStopped(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateStopped}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
	if scm.services[ServiceName].started {
		t.Error("service was stopped before the update; should not be restarted")
	}
}

func TestUpdateBinary_StaleOldGetsCleanedFirst(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS(
		"C:\\bin\\SerialHop.exe",
		"C:\\bin\\SerialHop.exe.old",
		"C:\\bin\\SerialHop-v0.7.0.exe",
	)
	if err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
}

func TestUpdateBinary_RenameTargetToOldFails_ServiceRestored(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")
	// Force every retry of the rename to fail.
	fs.renameErr[[2]string{"C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop.exe.old"}] = errors.New("AV holding handle")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected rename error")
	}
	if !scm.services[ServiceName].started {
		t.Error("service should be restarted on rollback")
	}
}

func TestUpdateBinary_RenameSrcToTargetFails_FullRollback(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")
	fs.renameErr[[2]string{"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe"}] = errors.New("cross-volume")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("rollback should restore SerialHop.exe")
	}
	if !scm.services[ServiceName].started {
		t.Error("service should be restarted on rollback")
	}
}

func TestUpdateBinary_StartFails_FullRollback(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped},
		startErr:         errors.New("new binary refuses to start"),
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	// After rollback: original exe back in place, new exe preserved under its versioned name.
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("rollback should restore SerialHop.exe")
	}
	if !fs.Exists("C:\\bin\\SerialHop-v0.7.0.exe") {
		t.Error("new exe should be preserved under its versioned name for inspection")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./internal/winsvc/ -run TestUpdateBinary -v
```

Expected: compile errors — `updateBinary`, `fsOps`, `os.ErrNotExist` import.

- [ ] **Step 3: Implement `updateBinary` + `fsOps`**

Add to `internal/winsvc/control.go` (top of file imports first — already has `time`, add `errors` if missing — it's there):

```go
// fsOps abstracts the file operations updateBinary needs so tests can
// substitute a fake. Production uses realFS{}, which calls os.Rename/os.Remove.
type fsOps interface {
	Rename(from, to string) error
	Remove(path string) error
}

type realFS struct{}

func (realFS) Rename(from, to string) error { return os.Rename(from, to) }
func (realFS) Remove(path string) error     { return os.Remove(path) }
```

Append the function:

```go
// updateBinary performs the in-place .exe swap described in
// docs/superpowers/specs/2026-05-11-auto-update-design.md §5.
//
//	1. Stop service if running.
//	2. Rename target → target.old (with retries; AV may briefly hold a handle).
//	3. Rename src → target.
//	4. Start service (if it was running before).
//	5. Best-effort delete target.old.
//
// On failure at any step after stop, the function rolls back as far as
// possible and restarts the service so the operator isn't left with a
// stopped service.
func updateBinary(scm SCMConn, fs fsOps, src, target string, opTimeout, pollInterval time.Duration) error {
	oldPath := target + ".old"

	// --- step 1: query service, stop if running ---
	svc, svcErr := scm.OpenService(ServiceName)
	var (
		hadService         bool
		serviceWasRunning  bool
	)
	if svcErr == nil {
		hadService = true
		defer svc.Close() //nolint:errcheck
		state, err := svc.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		if state == StateRunning || state == StateStartPending {
			serviceWasRunning = true
			if err := svc.Stop(); err != nil {
				return fmt.Errorf("stop service: %w", err)
			}
			if err := waitForState(svc, StateStopped, opTimeout, pollInterval); err != nil {
				return fmt.Errorf("wait for stop: %w", err)
			}
		}
	} else if !errors.Is(svcErr, ErrServiceMissing) {
		return fmt.Errorf("open service: %w", svcErr)
	}

	// Helper to attempt restart of the previously-running service. Errors are
	// wrapped into the returned error so the operator sees both the original
	// failure and any restart issue.
	restartIfNeeded := func(original error) error {
		if !hadService || !serviceWasRunning {
			return original
		}
		if err := svc.Start(); err != nil {
			return fmt.Errorf("%w (and restart failed: %v)", original, err)
		}
		if err := waitForState(svc, StateRunning, opTimeout, pollInterval); err != nil {
			return fmt.Errorf("%w (and restart wait failed: %v)", original, err)
		}
		return original
	}

	// --- step 2: clean up any stale .old, then rename target → .old ---
	// Stale .old may be left from a prior aborted update; remove best-effort.
	_ = fs.Remove(oldPath)

	const renameRetries = 5
	if err := renameWithRetry(fs, target, oldPath, renameRetries, 250*time.Millisecond); err != nil {
		return restartIfNeeded(fmt.Errorf("rename %s → %s: %w", target, oldPath, err))
	}

	// --- step 3: rename src → target ---
	if err := fs.Rename(src, target); err != nil {
		// Rollback step 2.
		_ = fs.Rename(oldPath, target)
		return restartIfNeeded(fmt.Errorf("rename %s → %s: %w", src, target, err))
	}

	// --- step 4: start service if it was running ---
	if hadService && serviceWasRunning {
		if err := svc.Start(); err != nil {
			// Rollback steps 2-3: preserve new binary under its original name.
			_ = fs.Rename(target, src)
			_ = fs.Rename(oldPath, target)
			return restartIfNeeded(fmt.Errorf("start service after swap: %w", err))
		}
		if err := waitForState(svc, StateRunning, opTimeout, pollInterval); err != nil {
			_ = fs.Rename(target, src)
			_ = fs.Rename(oldPath, target)
			// At this point the service is in an unknown state; one more Start
			// attempt under the original binary.
			_ = svc.Start()
			_ = waitForState(svc, StateRunning, opTimeout, pollInterval)
			return fmt.Errorf("start service after swap (rolled back): %w", err)
		}
	}

	// --- step 5: best-effort cleanup ---
	_ = fs.Remove(oldPath)
	return nil
}

func renameWithRetry(fs fsOps, from, to string, attempts int, backoff time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := fs.Rename(from, to); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(backoff)
	}
	return lastErr
}
```

Note: the current `control.go` already imports `"errors"`, `"fmt"`, `"os"`, `"time"`. The new code reuses those.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/winsvc/ -race -v
```

Expected: all tests pass (existing install / uninstall / restart unchanged; new `TestUpdateBinary_*` pass).

- [ ] **Step 5: Commit**

```bash
git add internal/winsvc/control.go internal/winsvc/control_test.go
git commit -m "feat(winsvc): updateBinary with auto-rollback on failure"
```

---

## Task 8: Wire `update` admin action into `RunAdminAction`

**Files:**
- Modify: `internal/winsvc/control.go` (signature change + new case)
- Modify: `cmd/serialhop/main.go` (pass through the new flag)

The signature of `RunAdminAction` grows a fourth parameter. `cmd/serialhop/main.go` is the only caller.

- [ ] **Step 1: Update `RunAdminAction` signature and add the `update` case**

Edit `internal/winsvc/control.go`. Change the function header and add the new case:

```go
func RunAdminAction(action, errorFile, updateSrc string) int {
	err := func() error {
		scm, err := DialSCM()
		if err != nil {
			return fmt.Errorf("connect SCM: %w", err)
		}
		defer scm.Disconnect() //nolint:errcheck

		switch action {
		case "install":
			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate executable: %w", err)
			}
			return install(scm, exePath)
		case "uninstall":
			return uninstall(scm, productionStopTimeout, productionPollInterval)
		case "restart":
			return restart(scm, productionStartTimeout, productionPollInterval)
		case "update":
			return runUpdate(scm, updateSrc)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}()
	if err != nil {
		_ = os.WriteFile(errorFile, []byte(err.Error()), 0o600)
		return 1
	}
	return 0
}

// runUpdate validates updateSrc, derives the target install path from the
// running exe, and dispatches to updateBinary.
func runUpdate(scm SCMConn, updateSrc string) error {
	if updateSrc == "" {
		return fmt.Errorf("update action requires --update-src")
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	installDir := filepath.Dir(exePath)
	srcDir := filepath.Dir(updateSrc)
	if !strings.EqualFold(filepath.Clean(srcDir), filepath.Clean(installDir)) {
		return fmt.Errorf("update-src must live in install dir (%q); got %q", installDir, updateSrc)
	}
	base := filepath.Base(updateSrc)
	if !strings.HasPrefix(base, "SerialHop-v") || !strings.HasSuffix(base, ".exe") {
		return fmt.Errorf("update-src filename must match SerialHop-v*.exe (got %q)", base)
	}
	if _, err := os.Stat(updateSrc); err != nil {
		return fmt.Errorf("update-src not accessible: %w", err)
	}
	return updateBinary(scm, realFS{}, updateSrc, exePath, productionStartTimeout, productionPollInterval)
}
```

Add the missing imports if not present at the top of the file:

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)
```

- [ ] **Step 2: Update `cmd/serialhop/main.go` to pass through the new flag**

Edit `cmd/serialhop/main.go`. In the `var (...)` block (around line 36):

```go
var (
	flagAdminAction = flag.String("admin-action", "", "internal: install|uninstall|restart|update (used by the GUI)")
	flagErrorFile   = flag.String("error-file", "", "internal: path the elevated child writes its error message to")
	flagUpdateSrc   = flag.String("update-src", "", "internal: path to the staged update .exe (used by --admin-action=update)")
	flagForeground  = flag.Bool("foreground", false, "run the device-client logic in the console (developer mode)")
	flagVersion     = flag.Bool("version", false, "print version and exit")
)
```

In the `main()` switch (around line 68), change:

```go
	case *flagAdminAction != "":
		os.Exit(winsvc.RunAdminAction(*flagAdminAction, *flagErrorFile, *flagUpdateSrc))
```

- [ ] **Step 3: Run tests to verify nothing broke**

Run:
```bash
go test ./internal/winsvc/ ./internal/config/ -race -v
go vet ./...
```

Expected: existing tests still pass; build is clean.

- [ ] **Step 4: Commit**

```bash
git add internal/winsvc/control.go cmd/serialhop/main.go
git commit -m "feat(winsvc): wire update admin action

Adds --admin-action=update --update-src=<path> to the elevated child path.
Validates that update-src lives in the install dir and matches SerialHop-v*.exe.
Existing install/uninstall/restart callers see an extra parameter on
RunAdminAction (ignored for those actions)."
```

---

## Task 9: Extend `RunElevatedAdminAction` to accept extra args

**Files:**
- Modify: `internal/panel/elevate.go:46-91`

Existing callers (`performAdmin("install"...)` etc.) call `RunElevatedAdminAction("install")`. We change to variadic so they keep compiling and the panel can pass `--update-src=...` for the update action.

- [ ] **Step 1: Change the signature and parameter composition**

Edit `internal/panel/elevate.go`. Replace the function body:

```go
// RunElevatedAdminAction relaunches the current executable elevated, asking
// it to perform an admin action. Returns the contents of the temp error
// file on failure (or an empty string on success). Returns ErrUserCancelled
// if the user dismissed the UAC prompt.
//
// `extraArgs` are appended verbatim to the elevated child's command line.
// Each entry is expected to be a single `--flag=value` token (no spaces).
// Used by the update action to pass `--update-src=<path>`; ignored by
// install/uninstall/restart.
func RunElevatedAdminAction(action string, extraArgs ...string) (errMsg string, err error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("SerialHop_admin_%d.err", os.Getpid()))
	defer os.Remove(errFile)

	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exePath)

	// Compose the command line. errFile and action are already controlled
	// inputs (action is a literal constant from panel.go, errFile is built
	// from os.TempDir + numeric PID). extraArgs are caller-supplied — the
	// only current caller passes a path produced by filepath.Join inside
	// the same dir as os.Executable(), which on Windows can contain spaces
	// in 'Program Files'-style installs. Quote the value half of each
	// extraArg's `--flag=value` token to handle spaces.
	args := fmt.Sprintf("--admin-action=%s --error-file=%s", action, errFile)
	for _, a := range extraArgs {
		args += " " + quoteFlagValue(a)
	}
	params, _ := windows.UTF16PtrFromString(args)

	info := shellExecuteInfoW{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		fMask:        seMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        1, // SW_SHOWNORMAL
	}
	r1, _, lastErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if lastErr == syscall.Errno(windows.ERROR_CANCELLED) {
			return "", ErrUserCancelled
		}
		return "", fmt.Errorf("ShellExecuteExW: %w", lastErr)
	}
	if info.hProcess == 0 {
		return "", errors.New("ShellExecuteExW returned no process handle")
	}

	hProc := windows.Handle(info.hProcess)
	defer windows.CloseHandle(hProc)
	if _, err := windows.WaitForSingleObject(hProc, windows.INFINITE); err != nil {
		return "", fmt.Errorf("WaitForSingleObject: %w", err)
	}

	data, readErr := os.ReadFile(errFile) //nolint:gosec // errFile is a temp path constructed in this function
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read error file: %w", readErr)
	}
	return strings.TrimSpace(string(data)), nil
}

// quoteFlagValue takes a "--flag=value" token and double-quotes the value
// half if it contains a space. Windows command-line parsing splits on
// unquoted spaces, so an install path under "C:\Program Files\..." would
// otherwise arrive as multiple args.
func quoteFlagValue(token string) string {
	eq := strings.IndexByte(token, '=')
	if eq < 0 {
		return token
	}
	flag := token[:eq]
	val := token[eq+1:]
	if !strings.ContainsAny(val, " \t") {
		return token
	}
	// We don't expect literal quotes inside the value (install paths are
	// not quoted by the OS), so no escaping beyond the wrapping is needed.
	return flag + `="` + val + `"`
}
```

- [ ] **Step 2: Build and smoke-test the panel package**

Run:
```bash
GOOS=windows go build ./...
```

Expected: builds cleanly.

- [ ] **Step 3: Commit**

```bash
git add internal/panel/elevate.go
git commit -m "feat(panel): variadic extraArgs on RunElevatedAdminAction"
```

---

## Task 10: `internal/panel/update_state.go` — state machine

**Files:**
- Create: `internal/panel/update_state.go`
- Create: `internal/panel/update_state_test.go`

A small enum + transition table for the update row. No Windows deps so it can be unit-tested on macOS.

- [ ] **Step 1: Write the failing test**

Create `internal/panel/update_state_test.go`:

```go
package panel

import "testing"

func TestUpdateState_TransitionsHappyPath(t *testing.T) {
	st := UpdateIdle
	if got := nextUpdateState(st, EvUpdateAvailable); got != UpdateAvailable {
		t.Errorf("idle + available: got %v", got)
	}
	st = UpdateAvailable
	if got := nextUpdateState(st, EvDownloadStart); got != UpdateDownloading {
		t.Errorf("available + downloadStart: got %v", got)
	}
	st = UpdateDownloading
	if got := nextUpdateState(st, EvDownloadOK); got != UpdateReady {
		t.Errorf("downloading + ok: got %v", got)
	}
	st = UpdateReady
	if got := nextUpdateState(st, EvInstallStart); got != UpdateInstalling {
		t.Errorf("ready + installStart: got %v", got)
	}
	st = UpdateInstalling
	if got := nextUpdateState(st, EvInstallOK); got != UpdateInstalled {
		t.Errorf("installing + ok: got %v", got)
	}
}

func TestUpdateState_DownloadFailGoesBackToAvailable(t *testing.T) {
	if got := nextUpdateState(UpdateDownloading, EvDownloadFail); got != UpdateDownloadFailed {
		t.Errorf("got %v", got)
	}
	if got := nextUpdateState(UpdateDownloadFailed, EvRetry); got != UpdateAvailable {
		t.Errorf("retry → available: got %v", got)
	}
}

func TestUpdateState_InstallFailGoesToRolledBack(t *testing.T) {
	if got := nextUpdateState(UpdateInstalling, EvInstallFail); got != UpdateInstallFailed {
		t.Errorf("got %v", got)
	}
	if got := nextUpdateState(UpdateInstallFailed, EvRetry); got != UpdateReady {
		t.Errorf("retry on rollback → ready: got %v", got)
	}
}

func TestUpdateState_NoChangeOnInvalidEvent(t *testing.T) {
	// Downloading + ev_install_start (impossible) — state stays put.
	if got := nextUpdateState(UpdateDownloading, EvInstallStart); got != UpdateDownloading {
		t.Errorf("unexpected transition: %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
go test ./internal/panel/ -run TestUpdateState -v
```

Expected: undefined identifiers.

- [ ] **Step 3: Implement the state machine**

Create `internal/panel/update_state.go`:

```go
package panel

// UpdateState is the state of the update row in the control panel.
// See docs/superpowers/specs/2026-05-11-auto-update-design.md §4.1.
type UpdateState int

const (
	UpdateIdle UpdateState = iota
	UpdateAvailable
	UpdateDownloading
	UpdateDownloadFailed
	UpdateReady
	UpdateInstalling
	UpdateInstalled
	UpdateInstallFailed
)

// UpdateEvent is an input to the state machine.
type UpdateEvent int

const (
	EvUpdateAvailable UpdateEvent = iota
	EvDownloadStart
	EvDownloadOK
	EvDownloadFail
	EvInstallStart
	EvInstallOK
	EvInstallFail
	EvRetry
	EvHide
)

// nextUpdateState returns the new state given the current state and an
// event. Unrecognized transitions leave the state unchanged so the panel
// goroutine doesn't have to know every "this can't happen" combination.
func nextUpdateState(cur UpdateState, ev UpdateEvent) UpdateState {
	switch cur {
	case UpdateIdle:
		if ev == EvUpdateAvailable {
			return UpdateAvailable
		}
	case UpdateAvailable:
		switch ev {
		case EvDownloadStart:
			return UpdateDownloading
		case EvHide:
			return UpdateIdle
		}
	case UpdateDownloading:
		switch ev {
		case EvDownloadOK:
			return UpdateReady
		case EvDownloadFail:
			return UpdateDownloadFailed
		}
	case UpdateDownloadFailed:
		if ev == EvRetry {
			return UpdateAvailable
		}
	case UpdateReady:
		if ev == EvInstallStart {
			return UpdateInstalling
		}
	case UpdateInstalling:
		switch ev {
		case EvInstallOK:
			return UpdateInstalled
		case EvInstallFail:
			return UpdateInstallFailed
		}
	case UpdateInstallFailed:
		if ev == EvRetry {
			return UpdateReady
		}
	case UpdateInstalled:
		if ev == EvHide {
			return UpdateIdle
		}
	}
	return cur
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./internal/panel/ -v
```

Expected: all tests pass on macOS (the `_other.go` stub file makes the package buildable cross-platform; state.go and update_state.go are pure Go).

- [ ] **Step 5: Commit**

```bash
git add internal/panel/update_state.go internal/panel/update_state_test.go
git commit -m "feat(panel): update-row state machine"
```

---

## Task 11: Wire the update flow into `panel.go`

**Files:**
- Modify: `internal/panel/panel.go` (add update row, goroutine, button handlers)

This is the largest and most Windows-specific change. There is no good unit-test seam for the panel itself (Walk widgets are Windows-only and require a real message loop), so we lean on the unit-tested pieces (`internal/updater`, `update_state`, `winsvc.updateBinary`) and verify the panel by building it on the Windows target.

The goroutine model:
- One background goroutine started at `panel.Run()` start, after the first `refresh()` completes (the spec says ~500 ms delay, but we can use `walk.Synchronize` to defer the kickoff until after the window is shown).
- The goroutine calls `updater.LatestRelease` → compares with `internal/version.Version` → if newer, captures the asset info into a struct guarded by `mw.Synchronize()` → marshals to GUI thread to update the label and reveal the row.
- A `walk.Timer` ticks every 6 h to retrigger the check.
- Button handlers spawn additional goroutines for `Download` and `Install` (so the GUI doesn't block on network or UAC).

- [ ] **Step 1: Add the update row, fields, and helpers**

Edit `internal/panel/panel.go`. At the top of the file's imports, add:

```go
import (
	// existing imports plus:
	"context"
	"net/http"
	"sync"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
)
```

In the `Run()` function, add new `walk` widget vars next to the existing ones:

```go
	var (
		// ... existing vars ...

		updateRow     *walk.Composite
		updateLabel   *walk.Label
		btnDownload   *walk.PushButton
		btnInstall2   *walk.PushButton
		btnRelease    *walk.PushButton
		btnRetry      *walk.PushButton
		btnCancelDL   *walk.PushButton
	)

	// `View error` button from spec §10 is deliberately omitted in v1: the
	// status-bar label already shows the failure detail, and plumbing the
	// elevated-child's temp error file path (cleaned up in elevate.go's defer)
	// to a UI handler is non-trivial. Add later if operators ask for it.
```

Add an "update controller" struct that owns the state machine + the latest known release. Define it just inside `Run()`, after the existing var block:

```go
	type updateCtl struct {
		mu        sync.Mutex
		state     UpdateState
		release   updater.Release
		exeAsset  *updater.Asset
		exeFile   string // full path to the staged .exe (when ready)
		sumsBody  string // cached SHA256SUMS.txt contents (filled at download)
		dlCancel  context.CancelFunc
		lastErr   error
	}
	ctl := &updateCtl{}

	httpClient := &http.Client{} // timeouts applied via per-request ctx
	userAgent := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"

	cfg, _ := config.LoadPartial(cfgPath)
	autoUpdateEnabled := cfg.AutoUpdate.Enabled
```

Add the row composite to the `Children` slice in the `MainWindow{...}` declaration, between the `warnLabel` and the existing button row:

```go
			Composite{
				AssignTo: &updateRow,
				Layout:   HBox{MarginsZero: true},
				Visible:  false,
				Children: []Widget{
					Label{AssignTo: &updateLabel, Text: ""},
					PushButton{AssignTo: &btnDownload, Text: "Download", Visible: false, OnClicked: func() { go ctlDownload(mw, ctl, httpClient, userAgent, dir, statusBar, applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL)) }},
					PushButton{AssignTo: &btnInstall2, Text: "Install update", Visible: false, OnClicked: func() { go ctlInstall(mw, ctl, statusBar, applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL)) }},
					PushButton{AssignTo: &btnRelease, Text: "Release notes", Visible: false, OnClicked: func() {
						if ctl.release.HTMLURL != "" {
							_ = OpenWithDefaultApp(ctl.release.HTMLURL)
						}
					}},
					PushButton{AssignTo: &btnRetry, Text: "Retry", Visible: false, OnClicked: func() {
						applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL)(EvRetry)
					}},
					PushButton{AssignTo: &btnCancelDL, Text: "Cancel", Visible: false, OnClicked: func() {
						ctl.mu.Lock()
						cancel := ctl.dlCancel
						ctl.mu.Unlock()
						if cancel != nil {
							cancel()
						}
					}},
				},
			},
```

After the `MainWindow.Create()` call succeeds, schedule the first update check:

```go
	if autoUpdateEnabled {
		// .old cleanup runs immediately (no network needed).
		_ = os.Remove(filepath.Join(dir, "SerialHop.exe.old"))

		go func() {
			// Small delay so the panel paints first.
			time.Sleep(500 * time.Millisecond)
			runUpdateCheck(mw, ctl, httpClient, userAgent, dir,
				applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
		}()

		// Periodic recheck (6 h).
		updateTicker, _ := newTickTimer(mw, 6*time.Hour, func() {
			go runUpdateCheck(mw, ctl, httpClient, userAgent, dir,
				applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
		})
		defer updateTicker.Dispose()
	}
```

- [ ] **Step 2: Add the controller functions to `panel.go`**

Append these to `internal/panel/panel.go`:

```go
// applyUpdateRow returns a function the caller invokes on the GUI thread
// (via mw.Synchronize) with a transition event. It runs the state machine,
// updates label text and button visibility, and is the single point where
// the panel UI reflects update state.
func applyUpdateRow(
	mw *walk.MainWindow,
	ctl *updateCtl,
	row *walk.Composite,
	label *walk.Label,
	btnDownload, btnInstall, btnRelease, btnRetry, btnCancel *walk.PushButton,
) func(ev UpdateEvent) {
	return func(ev UpdateEvent) {
		mw.Synchronize(func() {
			ctl.mu.Lock()
			ctl.state = nextUpdateState(ctl.state, ev)
			st := ctl.state
			tag := ctl.release.TagName
			ctl.mu.Unlock()

			row.SetVisible(st != UpdateIdle)
			// Hide every action button by default; the cases below opt-in.
			for _, b := range []*walk.PushButton{btnDownload, btnInstall, btnRelease, btnRetry, btnCancel} {
				b.SetVisible(false)
			}
			switch st {
			case UpdateAvailable:
				label.SetText("Update: " + tag + " available")
				label.SetTextColor(walk.RGB(0, 0, 0))
				btnDownload.SetVisible(true)
				btnRelease.SetVisible(true)
			case UpdateDownloading:
				label.SetText("Update: " + tag + " — downloading…")
				label.SetTextColor(walk.RGB(0, 0, 0))
				btnCancel.SetVisible(true)
			case UpdateDownloadFailed:
				label.SetText("Update: " + tag + " — download failed")
				label.SetTextColor(walk.RGB(192, 0, 0))
				btnRetry.SetVisible(true)
			case UpdateReady:
				label.SetText("Update: " + tag + " — ready to install")
				label.SetTextColor(walk.RGB(0, 0, 0))
				btnInstall.SetVisible(true)
				btnRelease.SetVisible(true)
			case UpdateInstalling:
				label.SetText("Update: installing…")
				label.SetTextColor(walk.RGB(0, 0, 0))
			case UpdateInstalled:
				label.SetText("Updated to " + tag + ". Close and reopen this window to load the new panel.")
				label.SetTextColor(walk.RGB(0, 128, 0))
			case UpdateInstallFailed:
				label.SetText("Update failed — service restored to previous version.")
				label.SetTextColor(walk.RGB(192, 0, 0))
				btnRetry.SetVisible(true)
			}
		})
	}
}

// runUpdateCheck fetches the latest release, compares against the current
// version, and emits the appropriate event. Called from a goroutine; uses
// `apply` (which marshals onto the GUI thread).
func runUpdateCheck(
	mw *walk.MainWindow,
	ctl *updateCtl,
	hc *http.Client,
	userAgent, installDir string,
	apply func(UpdateEvent),
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := updater.LatestRelease(ctx, hc, updater.DefaultReleasesURL, userAgent)
	if err != nil {
		writePanelDebugLog(installDir, "update_check_failed", err)
		return
	}
	newer, err := updater.IsNewer(rel.TagName, version.Version)
	if err != nil {
		writePanelDebugLog(installDir, "update_check_parse_failed", err)
		return
	}
	if !newer {
		return
	}
	// Locate the asset for this Windows binary.
	var exeAsset *updater.Asset
	for i := range rel.Assets {
		name := rel.Assets[i].Name
		if strings.HasPrefix(name, "SerialHop-v") && strings.HasSuffix(name, ".exe") {
			exeAsset = &rel.Assets[i]
			break
		}
	}
	if exeAsset == nil {
		writePanelDebugLog(installDir, "update_check_no_asset", fmt.Errorf("no SerialHop-v*.exe asset on release %s", rel.TagName))
		return
	}

	// Resume-from-disk: if a staged file under <installDir>/<assetName>
	// already exists, re-verify it against the current sums file. If it
	// matches, jump straight to UpdateReady.
	stagedPath := filepath.Join(installDir, exeAsset.Name)
	if _, err := os.Stat(stagedPath); err == nil {
		sumsAsset := rel.AssetByName("SHA256SUMS.txt")
		if sumsAsset != nil {
			body, ferr := fetchSums(hc, userAgent, sumsAsset.BrowserDownloadURL)
			if ferr == nil && updater.VerifyFile(stagedPath, body, exeAsset.Name) == nil {
				ctl.mu.Lock()
				ctl.release = rel
				ctl.exeAsset = exeAsset
				ctl.exeFile = stagedPath
				ctl.sumsBody = body
				ctl.mu.Unlock()
				apply(EvUpdateAvailable)
				apply(EvDownloadStart)
				apply(EvDownloadOK)
				cleanupStaleStagedFiles(installDir, exeAsset.Name)
				return
			}
		}
		// Stale or unverifiable staged file: delete it.
		_ = os.Remove(stagedPath)
	}

	cleanupStaleStagedFiles(installDir, exeAsset.Name)

	ctl.mu.Lock()
	ctl.release = rel
	ctl.exeAsset = exeAsset
	ctl.mu.Unlock()
	apply(EvUpdateAvailable)
}

func ctlDownload(
	mw *walk.MainWindow,
	ctl *updateCtl,
	hc *http.Client,
	userAgent, installDir string,
	statusBar *walk.Label,
	apply func(UpdateEvent),
) {
	ctl.mu.Lock()
	rel := ctl.release
	asset := ctl.exeAsset
	ctl.mu.Unlock()
	if asset == nil {
		return
	}
	apply(EvDownloadStart)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	ctl.mu.Lock()
	ctl.dlCancel = cancel
	ctl.mu.Unlock()
	defer func() {
		ctl.mu.Lock()
		ctl.dlCancel = nil
		ctl.mu.Unlock()
		cancel()
	}()

	dest := filepath.Join(installDir, asset.Name)
	var lastReport time.Time
	progress := func(received, total int64) {
		if time.Since(lastReport) < 200*time.Millisecond && (total <= 0 || received < total) {
			return
		}
		lastReport = time.Now()
		var msg string
		if total > 0 {
			pct := float64(received) / float64(total) * 100
			msg = fmt.Sprintf("Downloading %.0f%% (%.1f / %.1f MB)", pct, float64(received)/1e6, float64(total)/1e6)
		} else {
			msg = fmt.Sprintf("Downloading %.1f MB", float64(received)/1e6)
		}
		mw.Synchronize(func() { statusBar.SetText(msg) })
	}
	if err := updater.Download(ctx, hc, asset.BrowserDownloadURL, dest, userAgent, progress); err != nil {
		writePanelDebugLog(installDir, "update_download_failed", err)
		apply(EvDownloadFail)
		return
	}

	sumsAsset := rel.AssetByName("SHA256SUMS.txt")
	if sumsAsset == nil {
		_ = os.Remove(dest)
		writePanelDebugLog(installDir, "update_no_sums_asset", fmt.Errorf("release %s has no SHA256SUMS.txt", rel.TagName))
		apply(EvDownloadFail)
		return
	}
	body, err := fetchSums(hc, userAgent, sumsAsset.BrowserDownloadURL)
	if err != nil {
		_ = os.Remove(dest)
		writePanelDebugLog(installDir, "update_fetch_sums_failed", err)
		apply(EvDownloadFail)
		return
	}
	if err := updater.VerifyFile(dest, body, asset.Name); err != nil {
		_ = os.Remove(dest)
		writePanelDebugLog(installDir, "update_verify_failed", err)
		apply(EvDownloadFail)
		return
	}

	ctl.mu.Lock()
	ctl.exeFile = dest
	ctl.sumsBody = body
	ctl.mu.Unlock()

	mw.Synchronize(func() { statusBar.SetText("Download complete.") })
	apply(EvDownloadOK)
}

func ctlInstall(
	mw *walk.MainWindow,
	ctl *updateCtl,
	statusBar *walk.Label,
	apply func(UpdateEvent),
) {
	ctl.mu.Lock()
	src := ctl.exeFile
	ctl.mu.Unlock()
	if src == "" {
		return
	}
	apply(EvInstallStart)
	mw.Synchronize(func() { statusBar.SetText("Installing update…") })

	errMsg, err := RunElevatedAdminAction("update", "--update-src="+src)
	switch {
	case errors.Is(err, ErrUserCancelled):
		mw.Synchronize(func() { statusBar.SetText("Cancelled.") })
		apply(EvInstallFail)
		return
	case err != nil:
		mw.Synchronize(func() { statusBar.SetText("Failed: " + err.Error()) })
		apply(EvInstallFail)
		return
	case errMsg != "":
		mw.Synchronize(func() { statusBar.SetText("Failed: " + errMsg) })
		apply(EvInstallFail)
		return
	}

	mw.Synchronize(func() { statusBar.SetText("Update applied at " + time.Now().Format("15:04:05")) })
	apply(EvInstallOK)
}

func fetchSums(hc *http.Client, userAgent, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch sums: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cleanupStaleStagedFiles deletes any SerialHop-v*.exe inside installDir
// that doesn't match the current latest-release asset name. Best-effort.
func cleanupStaleStagedFiles(installDir, keep string) {
	matches, _ := filepath.Glob(filepath.Join(installDir, "SerialHop-v*.exe"))
	for _, m := range matches {
		if filepath.Base(m) == keep {
			continue
		}
		_ = os.Remove(m)
	}
}

// writePanelDebugLog appends a single line to SerialHop_panel_error.log.
// Used for failures the operator might want to inspect post-mortem without
// surfacing a popup. Best-effort.
func writePanelDebugLog(installDir, code string, err error) {
	line := fmt.Sprintf("%s %s: %v\n", time.Now().Format(time.RFC3339), code, err)
	f, ferr := os.OpenFile(filepath.Join(installDir, "SerialHop_panel_error.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.WriteString(line)
}
```

Add the missing imports at the top of `panel.go`:

```go
import (
	// existing imports plus:
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
)
```

(`errors`, `fmt`, `os`, `path/filepath`, `time` are already there.)

- [ ] **Step 3: Cross-platform build check**

Run:
```bash
go vet ./...
GOOS=windows go build ./...
go test ./internal/panel/ ./internal/updater/ ./internal/winsvc/ ./internal/config/ -race
```

Expected: builds cleanly on `windows`. Tests pass on the host platform (macOS/Linux). The panel.go file itself is `//go:build windows` so it doesn't compile on the host, but `update_state.go` does (no build tag) and is exercised by the tests in Task 10.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/panel.go
git commit -m "feat(panel): wire update row, periodic check, download, install"
```

---

## Task 12: README — Auto-update section

**Files:**
- Modify: `README.md` (insert short "Auto-update" subsection under "Install on a Windows lab machine")

- [ ] **Step 1: Append the new subsection**

In `README.md`, after the "Install on a Windows lab machine" block (after the bullet list ending with "Click **Open log file** to view the main log."), insert:

```markdown
### Auto-update

The control panel checks GitHub Releases on open (and every 6 h while it stays open) and, if a newer version is available, surfaces a row above the buttons:

```
Update: v0.7.0 available  [Download]  [Release notes]
```

**Download** fetches `SerialHop-vX.Y.Z.exe` and `SHA256SUMS.txt` into the install directory, verifies the SHA-256, and changes the button to **Install update**. **Install update** triggers a UAC prompt; if approved, the service is stopped, the current `SerialHop.exe` is renamed to `SerialHop.exe.old`, the staged binary is moved into place, the service is restarted, and `.old` is best-effort deleted on the next panel launch. If the service fails to come back up, the install is automatically rolled back to the previous version.

The panel process itself keeps running from the renamed `.old` until it's closed and reopened — the on-disk service binary swaps without taking down the panel window.

To disable update checks entirely (e.g., on air-gapped lab machines):

```yaml
auto_update:
  enabled: false
```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: README section for in-app auto-update"
```

---

## Task 13: End-to-end verification on Windows

Local TDD covers the unit-test layers (`internal/updater`, `internal/winsvc/updateBinary`, `update_state`). The panel itself can only be verified on Windows. This task is intentionally manual.

- [ ] **Step 1: Cross-compile a release-shaped build**

Run:
```bash
task test
task build
```

Expected: `dist/SerialHop.exe` builds cleanly. Tests pass with `-race`.

- [ ] **Step 2: Push the branch and open a PR for CI**

```bash
git push -u origin feat/auto-update
gh pr create --title "feat: in-app auto-update with SHA-256 verification" --body "$(cat <<'EOF'
## Summary

Adds an in-app auto-update flow to the SerialHop control panel:

- New `internal/updater` package wraps the GitHub Releases API, asset download with progress/cancel, and SHA-256 verification against the release's `SHA256SUMS.txt`.
- New `update` admin action in `internal/winsvc` performs the elevated rename-shuffle install (`stop → rename current → rename new → start`) with auto-rollback on failure.
- Panel grows an "Update available" row above the existing buttons; downloads the asset under its versioned name (e.g. `SerialHop-v0.7.0.exe`), verifies, then UAC-gates the install.
- New `auto_update.enabled` config flag (default `true`) lets operators opt out.
- Release workflow now emits `SHA256SUMS.txt` in standard `sha256sum` format so both `shasum -c` (per README) and our in-app parser work.

Spec: `docs/superpowers/specs/2026-05-11-auto-update-design.md`
Plan: `docs/superpowers/plans/2026-05-11-auto-update.md`

## Test plan

- [ ] CI `verify` job green (gofmt, vet, golangci-lint, race tests, govulncheck)
- [ ] `task build` produces `dist/SerialHop.exe` locally (GOOS=windows)
- [ ] On a Windows lab box: install previous release manually, then run the new binary and confirm the update row appears (point `DefaultReleasesURL` at a test repo if needed)
- [ ] Confirm UAC prompt fires on Install update
- [ ] Confirm service stops, swap, restarts to new version
- [ ] Confirm rollback path: stage a deliberately broken `SerialHop-vX.Y.Z.exe` (e.g., empty file) → Install update fails → service restored to previous version → `.old` and the broken `.exe` both still on disk for inspection
- [ ] Confirm `shasum -a 256 -c SHA256SUMS.txt` works on the next published release

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR opens, CI starts running. The `pr.yml` verify job runs gofmt / vet / golangci-lint / `go test -race -count=1 ./...` / govulncheck. Address any findings inline.

- [ ] **Step 3: Manual smoke-test on a Windows lab box**

(Outside the CI flow — requires hardware access.)

Run through the test-plan checkboxes from Step 2's PR body. Roll back any merge-blocking issues into follow-up commits on the same branch.

---

## Self-Review

Spec coverage check (run mentally against `docs/superpowers/specs/2026-05-11-auto-update-design.md`):

- §1 Purpose/scope → covered across all tasks.
- §2 Release-workflow fix → Task 1.
- §3 Config flag → Task 2.
- §4.1 Update row → Task 11 (composite + labels + buttons).
- §4.2 Check timing → Task 11 (500 ms initial delay, 6 h ticker).
- §4.3 Download incl. resume-from-disk + re-verify → Task 11 (`runUpdateCheck` + `ctlDownload`).
- §4.4 Cleanup → Task 11 (`.old` remove on launch + `cleanupStaleStagedFiles` after first successful check).
- §4.5 Install → Task 11 (`ctlInstall` calls `RunElevatedAdminAction("update", ...)`).
- §5 Elevated install with rollback → Task 7 (`updateBinary`).
- §6 Layout — every file in the spec's table is created or modified in some task.
- §7 Network behavior → Tasks 4 (release), 6 (download); UA, no-auth, anonymous rate-limit-friendly polling.
- §8 Testing — covered task-by-task; cross-platform pure-Go tests where possible.
- §9 Logging → `writePanelDebugLog` in Task 11; service-side `slog` in `updateBinary` is not explicitly added (the action's errMsg path conveys failure to the panel — sufficient for v1).
- §10 Error surface → covered by `applyUpdateRow` + `writePanelDebugLog`.
- §11 Compatibility → no signature changes to existing config or API; preserved.
- §12 Build/release → no new third-party deps; verified by no `go.mod` changes.
- §13 Security posture → SHA-256 is the verification boundary; Sigstore deferred (spec'd, not implemented).

No placeholders, no unimplemented references, no contradictory signatures. `nextUpdateState` / `UpdateState` / `UpdateEvent` names used consistently across tasks 10–11. `updateBinary(scm, fs, src, target, ...)` signature matches Task 8's caller. `RunElevatedAdminAction("update", "--update-src="+src)` matches Task 9's variadic signature.
