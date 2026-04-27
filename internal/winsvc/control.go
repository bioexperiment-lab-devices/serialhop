package winsvc

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	ServiceName = "LabDevicesClient"
	DisplayName = "Lab Devices Client"
	Description = "Exposes serial-port lab devices via chisel reverse tunnel."

	productionStopTimeout  = 15 * time.Second
	productionStartTimeout = 15 * time.Second
	productionPollInterval = 250 * time.Millisecond
)

// RunAdminAction is the entry point used by the main dispatcher when the
// binary is launched with --admin-action=<name>. It connects to SCM, runs
// the requested action, writes any error to errorFile (UTF-8), and returns
// 0 on success or 1 on failure.
func RunAdminAction(action, errorFile string) int {
	err := func() error {
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate executable: %w", err)
		}
		scm, err := DialSCM()
		if err != nil {
			return fmt.Errorf("connect SCM: %w", err)
		}
		defer scm.Disconnect()

		switch action {
		case "install":
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
		_ = os.WriteFile(errorFile, []byte(err.Error()), 0o644)
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
			return errors.New("Service already installed.")
		}
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

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
	defer s.Close()

	state, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if state == StateRunning || state == StateStartPending {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := waitForState(s, StateStopped, stopTimeout, pollInterval); err != nil {
			return fmt.Errorf("Service did not stop within %s; check the log file or kill the process manually.", stopTimeout)
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
	defer s.Close()

	state, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if state == StateRunning || state == StateStartPending {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := waitForState(s, StateStopped, timeout, pollInterval); err != nil {
			return fmt.Errorf("Service did not stop within %s; check the log file.", timeout)
		}
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := waitForRunning(s, timeout, pollInterval); err != nil {
		return errors.New("Service failed to start; check log file.")
	}
	return nil
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
	return errors.New("timeout")
}

func waitForRunning(s SCMService, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st == StateRunning {
			return nil
		}
		// StartPending is fine; we keep polling until Running or timeout.
		time.Sleep(poll)
	}
	return errors.New("timeout")
}
