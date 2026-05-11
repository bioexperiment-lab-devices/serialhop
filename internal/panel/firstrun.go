package panel

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

// FirstRunAction is the decision returned by decideFirstRun.
type FirstRunAction int

const (
	FirstRunOpenPanel FirstRunAction = iota
	FirstRunShowDialog
)

// FirstRunState describes everything decideFirstRun needs about the
// on-disk config to choose an action.
type FirstRunState struct {
	Exists   bool          // config file exists
	ParseErr error         // non-nil iff YAML parse failed
	Cfg      config.Config // populated from Default() and overlaid with whatever parsed cleanly
}

// readFirstRunState inspects path and returns a FirstRunState describing
// the file's existence and parsed contents.
func readFirstRunState(path string) FirstRunState {
	s := FirstRunState{Cfg: config.Default()}
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s
		}
		s.Exists = true
		s.ParseErr = err
		return s
	}
	s.Exists = true
	if uerr := yaml.Unmarshal(data, &s.Cfg); uerr != nil {
		s.ParseErr = uerr
		s.Cfg = config.Default()
	}
	return s
}

// decideFirstRun returns ShowDialog when the file is missing or both
// credentials are absent; otherwise OpenPanel. Malformed YAML opens the
// panel (the existing validation-warning label surfaces the parse error
// — we don't silently overwrite a file we cannot understand).
func decideFirstRun(s FirstRunState) FirstRunAction {
	if !s.Exists {
		return FirstRunShowDialog
	}
	if s.ParseErr != nil {
		return FirstRunOpenPanel
	}
	if s.Cfg.LabBridge.User == "" || s.Cfg.LabBridge.Pass == "" {
		return FirstRunShowDialog
	}
	return FirstRunOpenPanel
}
