package forbidsecretlog_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/bioexperiment-lab-devices/serialhop/tools/forbidsecretlog"
)

func TestAnalyzer(t *testing.T) {
	wd, _ := filepath.Abs("testdata")
	analysistest.Run(t, wd, forbidsecretlog.Analyzer,
		"github.com/bioexperiment-lab-devices/serialhop/testpkg/badcase",
		"github.com/bioexperiment-lab-devices/serialhop/testpkg/goodcase",
	)
}
