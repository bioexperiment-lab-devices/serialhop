// buildcmd is the cross-platform driver for `task build` and `task installer`.
// It reads assets/version.json for the base version, runs `git describe` for
// the suffix, and execs the appropriate build command with the resulting
// `-ldflags -X` injection.
//
// When called with a positional package argument (e.g. ./tools/installer) it
// uses plain `go build` (suitable for non-Wails binaries). Without a package
// argument it defaults to `wails build` for the main panel binary.
//
// This exists as a Go program rather than an inline shell pipeline because
// Task's embedded sh interpreter has Windows-specific quoting quirks that
// break shell scripts on windows-latest runners (verified in CI: a sed
// command with `|` delimiter and an awk command with single-quoted program
// both fail in mvdan.cc/sh on Windows but work everywhere else).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const versionPkg = "github.com/bioexperiment-lab-devices/serialhop/internal/version"

type versionFile struct {
	StringFileInfo struct {
		FileVersion string
	}
}

func main() {
	out := flag.String("o", "dist/SerialHop.exe", "output binary path")
	goos := flag.String("goos", os.Getenv("GOOS"), "GOOS for the build")
	goarch := flag.String("goarch", os.Getenv("GOARCH"), "GOARCH for the build")
	skipFrontend := flag.Bool("s", false, "skip frontend build (frontend already built)")
	tags := flag.String("tags", "", "comma-separated build tags forwarded to go build / wails build")
	flag.Parse()

	raw, err := os.ReadFile("assets/version.json")
	if err != nil {
		fail("read assets/version.json: %v", err)
	}
	var vf versionFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		fail("parse assets/version.json: %v", err)
	}
	if vf.StringFileInfo.FileVersion == "" {
		fail("assets/version.json: StringFileInfo.FileVersion is empty")
	}

	// Prefer an exact-match tag when HEAD is on one. Otherwise fall back to
	// the verbose `git describe` (commits-ahead + sha + optional -dirty).
	// The exact-match path matters for tagged release builds where a prior
	// workflow step (e.g. release-please.yml's integer-sync) may have left
	// the working tree dirty: we still want a clean `0.4.3+v0.4.3` suffix,
	// not `0.4.3+v0.4.3-dirty`, since the binary is genuinely built from
	// the tag's source.
	suffix := "unknown"
	if descOut, err := exec.Command("git", "describe", "--exact-match", "--tags", "HEAD").Output(); err == nil {
		if s := strings.TrimSpace(string(descOut)); s != "" {
			suffix = s
		}
	} else if descOut, err := exec.Command("git", "describe", "--tags", "--always", "--dirty", "--match=v*").Output(); err == nil {
		if s := strings.TrimSpace(string(descOut)); s != "" {
			suffix = s
		}
	}
	version := vf.StringFileInfo.FileVersion + "+" + suffix

	ldflags := fmt.Sprintf("-X %s.Version=%s", versionPkg, version)

	// When a positional package path is provided (e.g. ./tools/installer), use
	// plain `go build` with GOOS/GOARCH set in the environment. This path is
	// used for non-Wails binaries that still need the version ldflags injection.
	if pkg := flag.Arg(0); pkg != "" {
		destDir := filepath.Dir(*out)
		if destDir != "" && destDir != "." {
			if err := os.MkdirAll(destDir, 0o750); err != nil {
				fail("mkdir %s: %v", destDir, err)
			}
		}
		args := []string{"build", "-trimpath", "-ldflags=" + ldflags, "-o", *out}
		if *tags != "" {
			args = append(args, "-tags="+*tags)
		}
		args = append(args, pkg)
		cmd := exec.Command("go", args...) //nolint:gosec // args derived from build inputs; build-tool subprocess
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), "GOOS="+*goos, "GOARCH="+*goarch)
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				os.Exit(ee.ExitCode())
			}
			fail("go build: %v", err)
		}
		return
	}

	// No package argument: build the Wails panel binary.
	//
	// Determine the wails output path and final destination.
	// wails build always places the binary under build/bin/<name>[.exe].
	// We rename it to the caller-specified -o path after a successful build.
	platform := *goos + "/" + *goarch
	ext := ""
	if *goos == "windows" {
		ext = ".exe"
	}
	wailsBin := filepath.Join("build", "bin", "SerialHop"+ext)

	// wails build does not use -H windowsgui — it manages the subsystem flag
	// internally via its own linker flags for Windows GUI targets.
	//
	// -nopackage: this project already generates a Windows .syso file via
	// `tools/goversioninfo` in the `resource` Taskfile target (UAC manifest +
	// icon + version metadata via assets/version.json). Wails' own
	// `packageApplicationForWindows` would produce a second .syso in the same
	// directory as main.go, causing `too many .rsrc sections` at link time.
	// Skip Wails packaging; ours is already in place.
	wailsArgs := []string{"build", "-platform", platform, "-nopackage", "-trimpath", "-ldflags=" + ldflags}
	if *tags != "" {
		wailsArgs = append(wailsArgs, "-tags="+*tags)
	}
	if *skipFrontend {
		wailsArgs = append(wailsArgs, "-s")
	}

	cmd := exec.Command("wails", wailsArgs...) //nolint:gosec // args derived from build inputs (version.json, git describe, caller flags); this is a build-tool subprocess, not user-input handling
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// wails reads GOOS/GOARCH from -platform, not from env; keep env clean.
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fail("wails build: %v", err)
	}

	// Move the wails output to the expected destination path.
	destDir := filepath.Dir(*out)
	if destDir != "" && destDir != "." {
		if err := os.MkdirAll(destDir, 0o750); err != nil {
			fail("mkdir %s: %v", destDir, err)
		}
	}
	if err := os.Rename(wailsBin, *out); err != nil {
		fail("rename %s → %s: %v", wailsBin, *out, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "buildcmd: "+format+"\n", args...)
	os.Exit(1)
}
