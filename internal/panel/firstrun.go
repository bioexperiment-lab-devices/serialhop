package panel

import (
	"context"
	"errors"
	"net/http"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
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

// CredsCheckKind enumerates how the dialog should react to verifyCredentials.
type CredsCheckKind int

const (
	CredsOK           CredsCheckKind = iota // 200 — save.
	CredsUnauthorized                       // 401 — inline error, stay in dialog.
	CredsNeedsConfirm                       // 5xx or network — prompt the user to "save anyway?".
)

// CredsCheckResult is the verdict of verifyCredentials.
type CredsCheckResult struct {
	Kind   CredsCheckKind
	Detail string // human-readable reason for Confirm/Unauthorized; empty on OK.
}

// verifyCredentials makes one /api/public/clients/{user} call and
// classifies the outcome. base must be the scheme+host (e.g. "https://x").
func verifyCredentials(ctx context.Context, hc *http.Client, base, user, pass, userAgent string) CredsCheckResult {
	_, err := labbridge.FetchClient(ctx, hc, base, user, pass, userAgent)
	switch {
	case err == nil:
		return CredsCheckResult{Kind: CredsOK}
	case errors.Is(err, labbridge.ErrUnauthorized):
		return CredsCheckResult{Kind: CredsUnauthorized, Detail: "server rejected credentials"}
	default:
		return CredsCheckResult{Kind: CredsNeedsConfirm, Detail: err.Error()}
	}
}
