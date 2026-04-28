// Bumps the Minor version in assets/version.json when the working tree has
// uncommitted source changes (anything other than version.json itself). Patch
// and Build are reset to 0 on each bump.
//
//	go run ./tools/bumpversion          # bump-if-dirty, then print final version
//	go run ./tools/bumpversion -print   # read-only: just print current version
//
// Targeted regex rewrites are used so the file's hand-tuned column alignment
// survives the round-trip; encoding/json's indented form would reflow it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type quad struct {
	Major int `json:"Major"`
	Minor int `json:"Minor"`
	Patch int `json:"Patch"`
	Build int `json:"Build"`
}

type fixedFileInfo struct {
	FileVersion    quad `json:"FileVersion"`
	ProductVersion quad `json:"ProductVersion"`
}

type versionFile struct {
	FixedFileInfo fixedFileInfo `json:"FixedFileInfo"`
}

const (
	versionRelPath  = "assets/version.json"
	manifestRelPath = "assets/manifest.xml"
)

func main() {
	printOnly := flag.Bool("print", false, "print current version and exit (no bump)")
	flag.Parse()

	repoRoot, err := findRepoRoot()
	if err != nil {
		fail(err)
	}
	versionPath := filepath.Join(repoRoot, versionRelPath)

	raw, err := os.ReadFile(versionPath)
	if err != nil {
		fail(fmt.Errorf("read %s: %w", versionPath, err))
	}

	var vf versionFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		fail(fmt.Errorf("parse %s: %w", versionPath, err))
	}
	cur := vf.FixedFileInfo.FileVersion

	if *printOnly {
		fmt.Println(formatTriple(cur))
		return
	}

	dirty, err := hasSourceChanges(repoRoot)
	if err != nil {
		// Fail open: if git is unavailable, treat as dirty so the build still
		// produces an incrementing artifact (e.g. CI tarball builds).
		fmt.Fprintf(os.Stderr, "bumpversion: git check failed (%v); bumping anyway\n", err)
		dirty = true
	}
	if !dirty {
		fmt.Fprintf(os.Stderr, "bumpversion: tree clean, keeping %s\n", formatTriple(cur))
		fmt.Println(formatTriple(cur))
		return
	}

	next := quad{Major: cur.Major, Minor: cur.Minor + 1, Patch: 0, Build: 0}
	nextStr := formatTriple(next)

	updated, err := rewrite(raw, next, nextStr)
	if err != nil {
		fail(err)
	}
	if err := atomicWrite(versionPath, updated); err != nil {
		fail(err)
	}

	manifestPath := filepath.Join(repoRoot, manifestRelPath)
	mraw, err := os.ReadFile(manifestPath)
	if err != nil {
		fail(fmt.Errorf("read %s: %w", manifestPath, err))
	}
	mNew, err := rewriteManifest(mraw, nextStr+".0")
	if err != nil {
		fail(err)
	}
	if err := atomicWrite(manifestPath, mNew); err != nil {
		fail(err)
	}

	fmt.Fprintf(os.Stderr, "bumpversion: %s -> %s\n", formatTriple(cur), nextStr)
	fmt.Println(nextStr)
}

func formatTriple(q quad) string {
	return fmt.Sprintf("%d.%d.%d", q.Major, q.Minor, q.Patch)
}

func findRepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	wd, werr := os.Getwd()
	if werr != nil {
		return "", err
	}
	return wd, nil
}

func hasSourceChanges(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain", "--", ".", ":!"+versionRelPath)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

var (
	// FixedFileInfo blocks: "...Version": { "Major": M, "Minor": m, "Patch": p, "Build": b }
	fixedFileQuadRe    = regexp.MustCompile(`("FileVersion":\s*)\{[^}\n]*\}`)
	fixedProductQuadRe = regexp.MustCompile(`("ProductVersion":\s*)\{[^}\n]*\}`)
	// StringFileInfo entries: "...Version": "x.y.z"
	stringFileRe    = regexp.MustCompile(`("FileVersion":\s*)"[^"]*"`)
	stringProductRe = regexp.MustCompile(`("ProductVersion":\s*)"[^"]*"`)
	// manifest.xml's own assemblyIdentity (version is the FIRST attribute, so this
	// won't match the deeper Microsoft.Windows.Common-Controls block, where
	// version comes after type/name).
	manifestVersionRe = regexp.MustCompile(`(<assemblyIdentity\s+version=")[^"]*(")`)
)

func rewriteManifest(raw []byte, version string) ([]byte, error) {
	s := string(raw)
	if manifestVersionRe.FindStringIndex(s) == nil {
		return nil, fmt.Errorf("assemblyIdentity version attribute not found in %s", manifestRelPath)
	}
	return []byte(manifestVersionRe.ReplaceAllString(s, "${1}"+version+"${2}")), nil
}

func rewrite(raw []byte, next quad, nextStr string) ([]byte, error) {
	s := string(raw)
	quadLit := fmt.Sprintf(`{"Major": %d, "Minor": %d, "Patch": %d, "Build": %d}`,
		next.Major, next.Minor, next.Patch, next.Build)

	for _, step := range []struct {
		name string
		re   *regexp.Regexp
		rep  string
	}{
		{"FixedFileInfo.FileVersion", fixedFileQuadRe, "${1}" + quadLit},
		{"FixedFileInfo.ProductVersion", fixedProductQuadRe, "${1}" + quadLit},
		{"StringFileInfo.FileVersion", stringFileRe, "${1}\"" + nextStr + "\""},
		{"StringFileInfo.ProductVersion", stringProductRe, "${1}\"" + nextStr + "\""},
	} {
		if step.re.FindStringIndex(s) == nil {
			return nil, fmt.Errorf("pattern for %s not found in version.json", step.name)
		}
		s = step.re.ReplaceAllString(s, step.rep)
	}
	return []byte(s), nil
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "bumpversion: %v\n", err)
	os.Exit(1)
}
