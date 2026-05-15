package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// State is the result of comparing the installed version to the bundled version.
type State int

const (
	StateFresh State = iota
	StateUpgrade
	StateSame
	StateDowngrade
)

func (s State) String() string {
	switch s {
	case StateFresh:
		return "fresh"
	case StateUpgrade:
		return "upgrade"
	case StateSame:
		return "same"
	case StateDowngrade:
		return "downgrade"
	default:
		return "unknown"
	}
}

// options captures the parsed CLI/dialog choices the install flow needs.
type options struct {
	InstallDir     string
	Silent         bool
	NoLaunch       bool
	NoShortcut     bool
	AllowDowngrade bool
}

// fsOps abstracts the filesystem ops the install flow needs. Production wires
// realFS{} which delegates to the os package; tests inject fakeFS{}.
type fsOps interface {
	MkdirAll(path string, mode uint32) error
	WriteFile(path string, data []byte, mode uint32) error
	ReadFile(path string) ([]byte, error)
	Rename(from, to string) error
	Remove(path string) error
	Stat(path string) (exists bool, err error)
}

// versionReader abstracts reading the PE FileVersion. Production wires
// peReader{} which calls readPEFileVersion; tests inject fakeVersionReader.
type versionReader interface {
	Read(path string) (string, error)
}

// shortcutWriter abstracts desktop shortcut creation. Production wires
// realShortcutWriter{} which calls writeShortcut; tests inject fakeShortcutWriter.
type shortcutWriter interface {
	Write(opts shortcutOpts) error
}

// launcher abstracts the "start the panel in a detached child" step.
type launcher interface {
	Launch(path string) error
}

// scmDialer abstracts the SCM connection; production wires winsvc.DialSCM.
type scmDialer func() (winsvc.SCMConn, error)

// Runner holds the dependencies for an installer run. main.go assembles the
// production Runner; tests inject fakes.
type Runner struct {
	FS             fsOps
	VersionReader  versionReader
	ShortcutWriter shortcutWriter
	Launcher       launcher
	DialSCM        scmDialer // may be nil; tests that don't exercise SCM leave it nil
	BundledVersion string    // set by main from internal/version.Version
	Payload        []byte    // set by main from the //go:embed payload
}

// Result reports what happened so the UI (or stdout in silent mode) can
// surface the right status. Status messages from spec §12.
type Result struct {
	State        State
	InstalledVer string
	BundledVer   string
	Message      string
	Err          error
	ExitCode     int
}

// Run executes the install flow. See spec §4.4 (dispatch) and §5 (flow).
func (r *Runner) Run(opts options) Result {
	targetExe := filepath.Join(opts.InstallDir, "SerialHop.exe")

	state, installedVer, err := r.detectState(targetExe)
	if err != nil {
		return Result{Err: fmt.Errorf("detect installed version: %w", err), ExitCode: 1}
	}

	slog.Info("version_check",
		"installed", installedVer,
		"bundled", r.BundledVersion,
		"decision", state.String())

	switch state {
	case StateSame:
		return r.runSameVersion(opts, targetExe, installedVer)
	case StateDowngrade:
		if !opts.AllowDowngrade {
			return Result{
				State:        state,
				InstalledVer: installedVer,
				BundledVer:   r.BundledVersion,
				Err: fmt.Errorf(
					"installed version (v%s) is newer than this installer (v%s); "+
						"re-run with --allow-downgrade to proceed anyway",
					installedVer, r.BundledVersion),
				ExitCode: 1,
			}
		}
		fallthrough
	case StateFresh, StateUpgrade:
		return r.runInstallOrUpgrade(opts, targetExe, state, installedVer)
	default:
		return Result{Err: fmt.Errorf("unknown state %v", state), ExitCode: 1}
	}
}

// detectState checks whether targetExe exists and, if so, reads its PE version.
func (r *Runner) detectState(targetExe string) (State, string, error) {
	exists, err := r.FS.Stat(targetExe)
	if err != nil {
		return 0, "", err
	}
	if !exists {
		return StateFresh, "", nil
	}
	installed, err := r.VersionReader.Read(targetExe)
	if err != nil {
		return 0, "", fmt.Errorf("read installed version from %s: %w", targetExe, err)
	}
	cmp, err := updater.Compare(installed, r.BundledVersion)
	if err != nil {
		return 0, installed, fmt.Errorf("compare versions: %w", err)
	}
	switch {
	case cmp < 0:
		return StateUpgrade, installed, nil
	case cmp == 0:
		return StateSame, installed, nil
	default:
		return StateDowngrade, installed, nil
	}
}

// runSameVersion is the no-op-equivalent path: refresh shortcut, optionally
// launch, exit 0. No file writes, no SCM ops.
func (r *Runner) runSameVersion(opts options, targetExe, installedVer string) Result {
	res := Result{
		State:        StateSame,
		InstalledVer: installedVer,
		BundledVer:   r.BundledVersion,
		Message: fmt.Sprintf(
			"SerialHop v%s is already installed. Refreshed desktop shortcut.",
			installedVer),
	}
	r.maybeShortcut(opts, targetExe, &res)
	r.maybeLaunch(opts, targetExe, &res)
	return res
}

// runInstallOrUpgrade handles fresh installs and upgrades (and downgrades when
// the operator passed --allow-downgrade). Spec §5.
func (r *Runner) runInstallOrUpgrade(opts options, targetExe string, state State, installedVer string) Result {
	res := Result{
		State:        state,
		InstalledVer: installedVer,
		BundledVer:   r.BundledVersion,
	}

	// Step 1: ensure install dir exists.
	if err := r.FS.MkdirAll(opts.InstallDir, 0o755); err != nil {
		res.Err = fmt.Errorf("create install dir %s: %w", opts.InstallDir, err)
		res.ExitCode = 1
		return res
	}

	// Steps 2-4: stage payload + SHA-256 self-check.
	stagedName := fmt.Sprintf("SerialHop-v%s.exe", r.BundledVersion)
	stagedPath := filepath.Join(opts.InstallDir, stagedName)
	if err := r.FS.WriteFile(stagedPath, r.Payload, 0o644); err != nil {
		res.Err = fmt.Errorf("stage payload to %s: %w", stagedPath, err)
		res.ExitCode = 1
		return res
	}
	written, err := r.FS.ReadFile(stagedPath)
	if err != nil {
		res.Err = fmt.Errorf("read back staged payload: %w", err)
		res.ExitCode = 1
		return res
	}
	if sha256.Sum256(written) != sha256.Sum256(r.Payload) {
		_ = r.FS.Remove(stagedPath)
		res.Err = errors.New("bundled payload integrity check failed; the installer may be corrupted")
		res.ExitCode = 1
		return res
	}
	slog.Info("payload_extracted", "path", stagedPath, "size", len(r.Payload))

	// Steps 5-6: connect to SCM, stop service if running, rename payload into
	// place, restart service. We drive the SCM and file rename ourselves so
	// that the fakeFS injected by tests participates in all file operations.
	if r.DialSCM == nil {
		// Tests that don't exercise SCM skip this branch by leaving DialSCM nil.
		// Production always sets it.
		_ = r.FS.Remove(stagedPath)
		res.Err = errors.New("internal: DialSCM not configured")
		res.ExitCode = 1
		return res
	}
	scm, err := r.DialSCM()
	if err != nil {
		_ = r.FS.Remove(stagedPath)
		res.Err = fmt.Errorf("connect to Service Control Manager: %w", err)
		res.ExitCode = 1
		return res
	}
	defer func() { _ = scm.Disconnect() }()

	if err := r.swapBinary(scm, stagedPath, targetExe); err != nil {
		res.Err = fmt.Errorf("install/upgrade failed: %w", err)
		res.ExitCode = 1
		return res
	}
	slog.Info("install_or_upgrade_completed", "version", r.BundledVersion)

	// Steps 7-8: shortcut + launch (non-fatal on failure).
	r.maybeShortcut(opts, targetExe, &res)
	r.maybeLaunch(opts, targetExe, &res)

	if res.Message == "" {
		res.Message = fmt.Sprintf("Installed SerialHop v%s to %s.", r.BundledVersion, opts.InstallDir)
	}
	return res
}

// swapBinary stops the service (if present and running), renames src → target
// using r.FS (so tests with fakeFS participate fully), then restarts the
// service. This mirrors the rename-with-rollback logic in winsvc.updateBinary
// but operates through the injected fsOps so the install flow is unit-testable
// on macOS/Linux.
func (r *Runner) swapBinary(scm winsvc.SCMConn, src, target string) error {
	oldPath := target + ".old"

	// Query and optionally stop the service.
	svc, svcErr := scm.OpenService(winsvc.ServiceName)
	var (
		hadService        bool
		serviceWasRunning bool
	)
	if svcErr == nil {
		hadService = true
		defer func() { _ = svc.Close() }()
		state, err := svc.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		if state == winsvc.StateRunning || state == winsvc.StateStartPending {
			serviceWasRunning = true
			if err := svc.Stop(); err != nil {
				return fmt.Errorf("stop service: %w", err)
			}
		}
	} else if !errors.Is(svcErr, winsvc.ErrServiceMissing) {
		return fmt.Errorf("open service: %w", svcErr)
	}

	// Best-effort clean up any stale .old.
	_ = r.FS.Remove(oldPath)

	// Preserve existing target as .old (if it exists).
	targetExists, err := r.FS.Stat(target)
	if err != nil {
		return fmt.Errorf("stat target: %w", err)
	}
	if targetExists {
		if err := r.FS.Rename(target, oldPath); err != nil {
			return fmt.Errorf("rename %s → %s: %w", target, oldPath, err)
		}
	}

	// Rename staged payload into final position.
	if err := r.FS.Rename(src, target); err != nil {
		// Rollback: restore old binary.
		_ = r.FS.Rename(oldPath, target)
		return fmt.Errorf("rename %s → %s: %w", src, target, err)
	}

	// Restart service if it was running before.
	if hadService && serviceWasRunning {
		if err := svc.Start(); err != nil {
			// Rollback the rename.
			_ = r.FS.Rename(target, src)
			_ = r.FS.Rename(oldPath, target)
			return fmt.Errorf("start service after swap: %w", err)
		}
	}

	// Best-effort cleanup of .old.
	_ = r.FS.Remove(oldPath)
	return nil
}

// maybeShortcut creates the desktop shortcut unless --no-shortcut. Failures
// are recorded in the Result but never raise the exit code (spec §5 step 7
// note: shortcut failure does not fail the install).
func (r *Runner) maybeShortcut(opts options, targetExe string, res *Result) {
	if opts.NoShortcut {
		return
	}
	shortcutPath := publicDesktopShortcutPath()
	err := r.ShortcutWriter.Write(shortcutOpts{
		Path:         shortcutPath,
		Target:       targetExe,
		WorkingDir:   filepath.Dir(targetExe),
		IconLocation: targetExe + ",0",
		Description:  "SerialHop control panel",
	})
	if err != nil {
		slog.Warn("shortcut_failed", "path", shortcutPath, "err", err)
		// Preserve a successful binary install message while appending the warning.
		res.Message = fmt.Sprintf(
			"Install succeeded but desktop shortcut creation failed: %v. "+
				"You can launch SerialHop from %s.",
			err, targetExe)
	}
}

// maybeLaunch starts the panel unless --no-launch / --silent. Failures are
// recorded in the Result but never raise the exit code (spec §5 step 8 note).
func (r *Runner) maybeLaunch(opts options, targetExe string, res *Result) {
	if opts.NoLaunch || opts.Silent {
		return
	}
	if err := r.Launcher.Launch(targetExe); err != nil {
		slog.Warn("launch_failed", "path", targetExe, "err", err)
		// Augment the message rather than overwriting a shortcut-failure note.
		if res.Message == "" {
			res.Message = fmt.Sprintf(
				"Install succeeded but launching SerialHop failed: %v. "+
					"Double-click the desktop shortcut to start it.",
				err)
		}
	}
}

// publicDesktopShortcutPath returns the canonical all-users Desktop path.
// On Windows that's C:\Users\Public\Desktop\SerialHop.lnk. The constant is
// extracted so tests can override it via the var if needed.
func publicDesktopShortcutPath() string {
	return `C:\Users\Public\Desktop\SerialHop.lnk`
}
