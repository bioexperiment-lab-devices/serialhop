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
