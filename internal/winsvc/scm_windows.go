//go:build windows

package winsvc

import (
	"errors"

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
