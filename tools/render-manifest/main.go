// render-manifest writes assets/manifest.xml from assets/manifest.template.xml,
// substituting @@VERSION@@ with the StringFileInfo.FileVersion from
// assets/version.json plus a trailing ".0" (Windows app manifests require
// the four-part Major.Minor.Patch.Build form).
//
// This exists as a Go program rather than an inline shell substitution
// because Task's embedded sh interpreter has Windows-specific quoting
// quirks that break sed/awk pipelines on windows-latest runners.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	versionPath  = "assets/version.json"
	templatePath = "assets/manifest.template.xml"
	outputPath   = "assets/manifest.xml"
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
	if err := os.WriteFile(outputPath, []byte(out), 0o600); err != nil { //nolint:gosec // outputPath is a package-level constant, not user input
		fail("write %s: %v", outputPath, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "render-manifest: "+format+"\n", args...)
	os.Exit(1)
}
