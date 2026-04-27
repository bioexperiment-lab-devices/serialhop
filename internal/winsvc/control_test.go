package winsvc

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Fake SCMConn --------------------------------------------------------

type fakeService struct {
	name      string
	state     ServiceState
	started   bool
	deleted   bool
	startErr  error
	stopErr   error
	deleteErr error
	queryErr  error

	// Sequenced state changes: when len(stateProgression)>0, each Query() pops
	// the head and uses it as the current state. Lets a test simulate
	// "Running → StopPending → Stopped" over multiple polls.
	stateProgression []ServiceState

	mu sync.Mutex
}

func (s *fakeService) Query() (ServiceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queryErr != nil {
		return 0, s.queryErr
	}
	if len(s.stateProgression) > 0 {
		s.state = s.stateProgression[0]
		s.stateProgression = s.stateProgression[1:]
	}
	return s.state, nil
}

func (s *fakeService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startErr != nil {
		return s.startErr
	}
	s.started = true
	s.state = StateStartPending
	return nil
}

func (s *fakeService) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopErr != nil {
		return s.stopErr
	}
	s.state = StateStopPending
	return nil
}

func (s *fakeService) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = true
	return nil
}

func (s *fakeService) Close() error { return nil }

type fakeSCM struct {
	services map[string]*fakeService

	openErr   error
	createErr error
}

func newFakeSCM() *fakeSCM {
	return &fakeSCM{services: map[string]*fakeService{}}
}

func (f *fakeSCM) Disconnect() error { return nil }

func (f *fakeSCM) OpenService(name string) (SCMService, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	s, ok := f.services[name]
	if !ok || s.deleted {
		return nil, ErrServiceMissing
	}
	return s, nil
}

func (f *fakeSCM) CreateService(name string, cfg ServiceConfig) (SCMService, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if _, ok := f.services[name]; ok {
		return nil, ErrServiceExists
	}
	s := &fakeService{name: name, state: StateStopped}
	f.services[name] = s
	return s, nil
}

// --- install --------------------------------------------------------------

func TestInstall_Success(t *testing.T) {
	scm := newFakeSCM()
	if err := install(scm, "C:\\bin\\lab.exe"); err != nil {
		t.Fatalf("install: %v", err)
	}
	s := scm.services[ServiceName]
	if s == nil {
		t.Fatal("service was not created")
	}
	if !s.started {
		t.Error("service was created but Start() was not called")
	}
}

func TestInstall_AlreadyExists(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateRunning}
	err := install(scm, "C:\\bin\\lab.exe")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "already installed") {
		t.Errorf("err: %v", err)
	}
}

// --- uninstall ------------------------------------------------------------

func TestUninstall_StoppedService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateStopped}
	if err := uninstall(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !scm.services[ServiceName].deleted {
		t.Error("service was not deleted")
	}
}

func TestUninstall_RunningServiceStopsThenDeletes(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopPending, StateStopped},
	}
	if err := uninstall(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !scm.services[ServiceName].deleted {
		t.Error("service was not deleted")
	}
}

func TestUninstall_StopTimeout(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateRunning} // stays Running forever
	err := uninstall(scm, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "did not stop") {
		t.Errorf("err: %v", err)
	}
}

func TestUninstall_NotInstalled(t *testing.T) {
	scm := newFakeSCM()
	err := uninstall(scm, 100*time.Millisecond, time.Millisecond)
	if !errors.Is(err, ErrServiceMissing) {
		t.Errorf("err: %v", err)
	}
}

// --- restart --------------------------------------------------------------

func TestRestart_RunningService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	if err := restart(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !scm.services[ServiceName].started {
		t.Error("Start() not called")
	}
}

func TestRestart_StoppedService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateStopped,
		stateProgression: []ServiceState{StateStopped, StateStartPending, StateRunning},
	}
	if err := restart(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func TestRestart_StartTimeout(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateStopped,
		stateProgression: []ServiceState{StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped},
	}
	err := restart(scm, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected start timeout")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("err: %v", err)
	}
}

func TestRestart_NotInstalled(t *testing.T) {
	scm := newFakeSCM()
	err := restart(scm, 100*time.Millisecond, time.Millisecond)
	if !errors.Is(err, ErrServiceMissing) {
		t.Errorf("err: %v", err)
	}
}
