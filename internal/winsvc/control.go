package winsvc

import (
	"errors"
	"fmt"
	"os"
	"time"
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
func RunAdminAction(action, errorFile string) int {
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

// fsOps abstracts the file operations updateBinary needs so tests can
// substitute a fake. Production uses realFS{}, which calls os.Rename/os.Remove.
type fsOps interface {
	Rename(from, to string) error
	Remove(path string) error
}

type realFS struct{}

func (realFS) Rename(from, to string) error { return os.Rename(from, to) }
func (realFS) Remove(path string) error     { return os.Remove(path) }

// Compile-time assertion: realFS satisfies fsOps.
var _ fsOps = realFS{}

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
func updateBinary(scm SCMConn, fs fsOps, src, target string, opTimeout, pollInterval, renameBackoff time.Duration) error {
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

	// --- step 2: clean up any stale .old, then rename target → .old ---
	_ = fs.Remove(oldPath)

	const renameRetries = 5
	if err := renameWithRetry(fs, target, oldPath, renameRetries, renameBackoff); err != nil {
		return restartIfNeeded(fmt.Errorf("rename %s → %s: %w", target, oldPath, err))
	}

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

func renameWithRetry(fs fsOps, from, to string, attempts int, backoff time.Duration) error {
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
