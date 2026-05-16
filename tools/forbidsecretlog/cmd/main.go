package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/bioexperiment-lab-devices/serialhop/tools/forbidsecretlog"
)

func main() { singlechecker.Main(forbidsecretlog.Analyzer) }
