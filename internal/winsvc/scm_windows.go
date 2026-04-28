//go:build windows

package winsvc

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func dialSCM() (SCMConn, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	return &winSCM{m: m}, nil
}

// dialSCMReadOnly opens an SCM handle with only SC_MANAGER_CONNECT, which
// works for non-admin users. The returned SCMConn supports OpenService for
// query (SERVICE_QUERY_STATUS); CreateService and the mutating service
// methods will fail.
func dialSCMReadOnly() (SCMConn, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	return &winSCMReadOnly{handle: h}, nil
}

type winSCMReadOnly struct {
	handle windows.Handle
}

func (w *winSCMReadOnly) Disconnect() error { return windows.CloseServiceHandle(w.handle) }

func (w *winSCMReadOnly) OpenService(name string) (SCMService, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(w.handle, namePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, ErrServiceMissing
		}
		return nil, err
	}
	return &winServiceReadOnly{handle: h}, nil
}

func (w *winSCMReadOnly) CreateService(name string, cfg ServiceConfig) (SCMService, error) {
	return nil, errors.New("read-only SCM connection cannot create services")
}

type winServiceReadOnly struct {
	handle windows.Handle
}

func (w *winServiceReadOnly) Query() (ServiceState, error) {
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(w.handle, &status); err != nil {
		return 0, err
	}
	return mapState(svc.State(status.CurrentState)), nil
}

func (w *winServiceReadOnly) Start() error  { return errors.New("read-only SCM connection") }
func (w *winServiceReadOnly) Stop() error   { return errors.New("read-only SCM connection") }
func (w *winServiceReadOnly) Delete() error { return errors.New("read-only SCM connection") }
func (w *winServiceReadOnly) Close() error  { return windows.CloseServiceHandle(w.handle) }

type winSCM struct{ m *mgr.Mgr }

func (w *winSCM) Disconnect() error { return w.m.Disconnect() }

func (w *winSCM) OpenService(name string) (SCMService, error) {
	s, err := w.m.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, ErrServiceMissing
		}
		return nil, err
	}
	return &winService{s: s}, nil
}

func (w *winSCM) CreateService(name string, cfg ServiceConfig) (SCMService, error) {
	startType := uint32(mgr.StartManual)
	if cfg.AutoStart {
		startType = mgr.StartAutomatic
	}
	mgrCfg := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        startType,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      cfg.DisplayName,
		Description:      cfg.Description,
		ServiceStartName: cfg.ServiceStartName,
	}
	s, err := w.m.CreateService(name, cfg.BinaryPath, mgrCfg)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_EXISTS) {
			return nil, ErrServiceExists
		}
		return nil, err
	}
	if err := s.SetRecoveryActions(nil, 0); err != nil {
		// Non-fatal: recovery actions are optional. Ignore.
	}
	return &winService{s: s}, nil
}

type winService struct{ s *mgr.Service }

func (w *winService) Query() (ServiceState, error) {
	st, err := w.s.Query()
	if err != nil {
		return 0, err
	}
	return mapState(st.State), nil
}

func (w *winService) Start() error { return w.s.Start() }
func (w *winService) Stop() error {
	_, err := w.s.Control(svc.Stop)
	return err
}
func (w *winService) Delete() error { return w.s.Delete() }
func (w *winService) Close() error  { return w.s.Close() }

func mapState(s svc.State) ServiceState {
	switch s {
	case svc.Stopped:
		return StateStopped
	case svc.StartPending:
		return StateStartPending
	case svc.StopPending:
		return StateStopPending
	case svc.Running:
		return StateRunning
	default:
		return StateStopped
	}
}
