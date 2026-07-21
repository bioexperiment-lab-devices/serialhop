package winsvc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

const (
	ServiceName = "SerialHop"
	DisplayName = "SerialHop"
	Description = "Exposes serial-port lab devices via chisel reverse tunnel."

	productionStopTimeout  = 15 * time.Second
	productionStartTimeout = 15 * time.Second
	productionPollInterval = 250 * time.Millisecond
)

// errWaitTimeout is returned by waitForState when the deadline elapses before
// the service reaches the target state.
var errWaitTimeout = errors.New("wait deadline exceeded")

// RunAdminAction is the entry point used by the main dispatcher when the
// binary is launched with --admin-action=<name>. It connects to SCM, runs
// the requested action, writes any error to errorFile (UTF-8), and returns
// 0 on success or 1 on failure.
func RunAdminAction(action, errorFile, updateSrc, resultPath, fromVersion, toVersion string) int {
	err := func() error {
		scm, err := DialSCM()
		if err != nil {
			return fmt.Errorf("connect SCM: %w", err)
		}
		defer scm.Disconnect() //nolint:errcheck // best-effort disconnect; error cannot be handled in defer

		switch action {
		case "install":
			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate executable: %w", err)
			}
			return install(scm, exePath)
		case "uninstall":
			return uninstall(scm, productionStopTimeout, productionPollInterval)
		case "restart":
			return restart(scm, productionStartTimeout, productionPollInterval)
		case "update":
			return runUpdate(scm, updateSrc, resultPath, fromVersion, toVersion)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}()
	if err != nil {
		_ = os.WriteFile(errorFile, []byte(err.Error()), 0o600)
		return 1
	}
	return 0
}

// runUpdate validates updateSrc, derives the target install path from the
// running exe, and dispatches to runUpdateWithResult with production
// timeouts and the real filesystem. resultPath/fromVersion/toVersion are set
// only by the remote-update flow (the service passes them); the panel-driven
// UAC flow leaves resultPath empty and no result file is written.
func runUpdate(scm SCMConn, updateSrc, resultPath, fromVersion, toVersion string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	return runUpdateWithResult(scm, realFS{}, updateSrc, exePath, resultPath, fromVersion, toVersion,
		productionStartTimeout, productionPollInterval, 250*time.Millisecond)
}

// runUpdateWithResult wraps runUpdateWithDeps, writing the update-result file
// when resultPath != "". Empty resultPath preserves the panel path exactly
// (no file written). The result write is best-effort — it never changes the
// update's own success/failure.
func runUpdateWithResult(scm SCMConn, fs FS, updateSrc, exePath, resultPath, fromVersion, toVersion string,
	opTimeout, pollInterval, renameBackoff time.Duration) error {

	if resultPath != "" {
		writeUpdateResult(resultPath, updateresult.StateInstalling, fromVersion, toVersion, "")
	}
	err := runUpdateWithDeps(scm, fs, updateSrc, exePath, opTimeout, pollInterval, renameBackoff)
	if resultPath != "" {
		if err != nil {
			writeUpdateResult(resultPath, updateresult.StateRolledBack, fromVersion, toVersion, err.Error())
		} else {
			writeUpdateResult(resultPath, updateresult.StateSucceeded, fromVersion, toVersion, "")
		}
	}
	return err
}

// writeUpdateResult reads-preserving started_at, sets terminal fields, writes.
// Best-effort: a failed result write is swallowed (the update outcome is
// already decided by the caller).
func writeUpdateResult(path, state, from, to, errMsg string) {
	r, _ := updateresult.Read(path)
	r.State, r.From, r.To, r.Error = state, from, to, errMsg
	now := time.Now().UTC().Format(time.RFC3339)
	if r.StartedAt == "" {
		r.StartedAt = now
	}
	if state != updateresult.StateInstalling {
		r.FinishedAt = now
	}
	_ = updateresult.Write(path, r)
}

// runUpdateWithDeps is the testable form of runUpdate. The panel stages
// downloads under %LOCALAPPDATA%\SerialHop\updates\, which is on a
// different directory (and possibly volume) from the install dir
// (typically C:\Program Files\SerialHop\). When updateSrc lives outside
// the install dir, we copy it in (cross-volume safe) and run the swap
// against the copy; the original is left in place so the panel can
// resume-from-disk on the next launch.
//
// If updateSrc is already in the install dir (operator-staged), we skip
// the copy and rename in place — preserving the pre-installer code path.
func runUpdateWithDeps(scm SCMConn, fs FS, updateSrc, exePath string,
	opTimeout, pollInterval, renameBackoff time.Duration) error {
	if updateSrc == "" {
		return fmt.Errorf("update action requires --update-src")
	}
	base := filepath.Base(updateSrc)
	if !strings.HasPrefix(base, "SerialHop-v") || !strings.HasSuffix(base, ".exe") {
		return fmt.Errorf("update-src filename must match SerialHop-v*.exe (got %q)", base)
	}
	exists, err := fs.Stat(updateSrc)
	if err != nil {
		return fmt.Errorf("update-src not accessible: %w", err)
	}
	if !exists {
		return fmt.Errorf("update-src not accessible: %w", os.ErrNotExist)
	}

	installDir := filepath.Dir(exePath)
	renameSrc := updateSrc
	if !strings.EqualFold(filepath.Clean(filepath.Dir(updateSrc)), filepath.Clean(installDir)) {
		dst := filepath.Join(installDir, base)
		if err := fs.Copy(updateSrc, dst); err != nil {
			// Best-effort: make sure no half-written copy is left behind in
			// the install dir. (realFS.Copy is stage-and-rename so a partial
			// already cleans itself up, but defense-in-depth against alternate
			// FS implementations that may not.)
			_ = fs.Remove(dst)
			return fmt.Errorf("copy update-src into install dir: %w", err)
		}
		renameSrc = dst
	}

	return updateBinary(scm, fs, renameSrc, exePath,
		opTimeout, pollInterval, renameBackoff)
}

func install(scm SCMConn, exePath string) error {
	cfg := ServiceConfig{
		DisplayName: DisplayName,
		Description: Description,
		BinaryPath:  exePath,
		AutoStart:   true,
		// ServiceStartName "" → LocalSystem
	}
	s, err := scm.CreateService(ServiceName, cfg)
	if err != nil {
		if errors.Is(err, ErrServiceExists) {
			return errors.New("service already installed")
		}
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close() //nolint:errcheck // best-effort cleanup; error cannot be handled in defer

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w (service is installed; use Restart after fixing the underlying issue)", err)
	}
	return nil
}

func uninstall(scm SCMConn, stopTimeout, pollInterval time.Duration) error {
	s, err := scm.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // best-effort cleanup; error cannot be handled in defer

	state, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if state == StateRunning || state == StateStartPending {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := waitForState(s, StateStopped, stopTimeout, pollInterval); err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("service did not stop within %s; check the log file or kill the process manually", stopTimeout)
			}
			return fmt.Errorf("query while stopping: %w", err)
		}
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

func restart(scm SCMConn, timeout, pollInterval time.Duration) error {
	s, err := scm.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // best-effort cleanup; error cannot be handled in defer

	state, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if state == StateRunning || state == StateStartPending {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := waitForState(s, StateStopped, timeout, pollInterval); err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("service did not stop within %s; check the log file", timeout)
			}
			return fmt.Errorf("query while stopping: %w", err)
		}
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := waitForState(s, StateRunning, timeout, pollInterval); err != nil {
		if errors.Is(err, errWaitTimeout) {
			return errors.New("service failed to start; check log file")
		}
		return fmt.Errorf("query while starting: %w", err)
	}
	return nil
}

// FS is the small subset of filesystem operations updateBinary needs.
// Exported so callers in other packages (notably tools/installer) can supply
// their own implementation and reuse the rename-with-rollback machinery.
type FS interface {
	Rename(from, to string) error
	Remove(path string) error
	Stat(path string) (exists bool, err error)
	// Copy duplicates `from` to `to`. The implementation must be
	// atomic-on-success — partial writes must not leave a file at `to`.
	// Used by runUpdateWithDeps to move the panel-staged binary from
	// %LOCALAPPDATA% into the install dir before the same-volume rename
	// swap; both volumes may differ, so os.Rename isn't sufficient.
	Copy(from, to string) error
}

type realFS struct{}

func (realFS) Rename(from, to string) error { return os.Rename(from, to) }
func (realFS) Remove(path string) error     { return os.Remove(path) }
func (realFS) Stat(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (realFS) Copy(from, to string) error {
	in, err := os.Open(from) //nolint:gosec // from is the elevated child's caller-validated update-src path.
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // close on a read-only handle; error cannot meaningfully be handled here.

	partial := to + ".partial"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // partial = to + ".partial"; to is the install-dir destination derived by the elevated child.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, to); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return nil
}

// Compile-time assertion: realFS satisfies FS.
var _ FS = realFS{}

// RealFS returns a production FS that delegates all calls to the os package.
// Pass this to InstallOrUpgrade from non-test code paths.
func RealFS() FS {
	return realFS{}
}

// updateBinary performs the in-place .exe swap described in
// docs/superpowers/specs/2026-05-11-auto-update-design.md §5.
//
//  1. Stop service if running.
//  2. Rename target → target.old (with retries; AV may briefly hold a handle).
//  3. Rename src → target.
//  4. Start service (if it was running before).
//  5. Best-effort delete target.old.
//
// On failure at any step after stop, the function rolls back as far as
// possible and restarts the service so the operator isn't left with a
// stopped service.
func updateBinary(scm SCMConn, fs FS, src, target string, opTimeout, pollInterval, renameBackoff time.Duration) error {
	oldPath := target + ".old"

	// --- step 1: query service, stop if running ---
	svc, svcErr := scm.OpenService(ServiceName)
	var (
		hadService        bool
		serviceWasRunning bool
	)
	if svcErr == nil {
		hadService = true
		defer svc.Close() //nolint:errcheck
		state, err := svc.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		if state == StateRunning || state == StateStartPending {
			serviceWasRunning = true
			if err := svc.Stop(); err != nil {
				return fmt.Errorf("stop service: %w", err)
			}
			if err := waitForState(svc, StateStopped, opTimeout, pollInterval); err != nil {
				return fmt.Errorf("wait for stop: %w", err)
			}
		}
	} else if !errors.Is(svcErr, ErrServiceMissing) {
		return fmt.Errorf("open service: %w", svcErr)
	}

	// Helper to attempt restart of the previously-running service. Errors are
	// wrapped into the returned error so the operator sees both the original
	// failure and any restart issue.
	restartIfNeeded := func(original error) error {
		if !hadService || !serviceWasRunning {
			return original
		}
		if err := svc.Start(); err != nil {
			return fmt.Errorf("%w (and restart failed: %v)", original, err)
		}
		if err := waitForState(svc, StateRunning, opTimeout, pollInterval); err != nil {
			return fmt.Errorf("%w (and restart wait failed: %v)", original, err)
		}
		return original
	}

	// --- step 2: clean up any stale .old, then preserve current target as .old ---
	_ = fs.Remove(oldPath)

	targetExists, err := fs.Stat(target)
	if err != nil {
		return restartIfNeeded(fmt.Errorf("stat target %s: %w", target, err))
	}
	if targetExists {
		const renameRetries = 5
		if err := renameWithRetry(fs, target, oldPath, renameRetries, renameBackoff); err != nil {
			return restartIfNeeded(fmt.Errorf("rename %s → %s: %w", target, oldPath, err))
		}
	}
	// If target doesn't exist (fresh install with no prior binary), we skip the
	// rename-to-.old step entirely. There's nothing to preserve. Subsequent
	// rollback paths likewise become no-ops when oldPath doesn't exist (the
	// fs.Rename(oldPath, target) attempts simply fail silently as best-effort).

	// --- step 3: rename src → target ---
	if err := fs.Rename(src, target); err != nil {
		// Rollback step 2.
		_ = fs.Rename(oldPath, target)
		return restartIfNeeded(fmt.Errorf("rename %s → %s: %w", src, target, err))
	}

	// --- step 4: start service if it was running ---
	if hadService && serviceWasRunning {
		if err := svc.Start(); err != nil {
			// Rollback steps 2-3: preserve new binary under its original name.
			_ = fs.Rename(target, src)
			_ = fs.Rename(oldPath, target)
			return restartIfNeeded(fmt.Errorf("start service after swap: %w", err))
		}
		if err := waitForState(svc, StateRunning, opTimeout, pollInterval); err != nil {
			_ = fs.Rename(target, src)
			_ = fs.Rename(oldPath, target)
			return restartIfNeeded(fmt.Errorf("start service after swap (timed out): %w", err))
		}
	}

	// --- step 5: best-effort cleanup ---
	_ = fs.Remove(oldPath)
	return nil
}

func renameWithRetry(fs FS, from, to string, attempts int, backoff time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := fs.Rename(from, to); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < attempts-1 {
			time.Sleep(backoff)
		}
	}
	return lastErr
}

func waitForState(s SCMService, target ServiceState, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st == target {
			return nil
		}
		time.Sleep(poll)
	}
	return errWaitTimeout
}

// InstallOrUpgrade extracts the in-place update sequence into a public entry
// point so callers can reuse the rename-with-rollback machinery the panel's
// auto-update relies on. updateBinary gracefully handles the "service not
// yet installed" case (it skips the SCM stop and start), so this single call
// covers both first-install and upgrade.
//
// src must already exist at the desired path and live in the same directory
// as target (same-volume rename requirement). target is the canonical install
// location (e.g., C:\Program Files\SerialHop\SerialHop.exe). The fs parameter
// lets tests and unconventional callers substitute their own filesystem
// implementation; production callers pass winsvc.RealFS() or assemble one
// that satisfies the FS interface.
func InstallOrUpgrade(scm SCMConn, fs FS, src, target string) error {
	return updateBinary(scm, fs, src, target,
		productionStartTimeout, productionPollInterval, 250*time.Millisecond)
}
