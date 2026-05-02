// buildcmd is the cross-platform driver for `task build`. It reads
// assets/version.json for the base version, runs `git describe` for the
// suffix, and execs `go build` with the resulting `-ldflags -X` injection.
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

	if dir := filepath.Dir(*out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			fail("mkdir %s: %v", dir, err)
		}
	}

	ldflags := fmt.Sprintf("-H windowsgui -X %s.Version=%s", versionPkg, version)
	args := []string{"build", "-trimpath", "-ldflags=" + ldflags, "-o", *out, "./cmd/serialhop"}

	cmd := exec.Command("go", args...) //nolint:gosec // args derived from build inputs (version.json, git describe, caller flags); this is a build-tool subprocess, not user-input handling
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
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "buildcmd: "+format+"\n", args...)
	os.Exit(1)
}
