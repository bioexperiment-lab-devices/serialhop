package winsvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Fake SCMConn --------------------------------------------------------

type fakeService struct {
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
	s := &fakeService{state: StateStopped}
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

// --- updateBinary ---------------------------------------------------------

// fakeFS records calls and lets tests inject failures at specific steps.
type fakeFS struct {
	existing  map[string]bool     // file paths that "exist"
	calls     []string            // ordered call log for assertions
	renameErr map[[2]string]error // {from,to} → err to return
	removeErr map[string]error
	statErr   map[string]error
}

func newFakeFS(files ...string) *fakeFS {
	f := &fakeFS{
		existing:  map[string]bool{},
		renameErr: map[[2]string]error{},
		removeErr: map[string]error{},
		statErr:   map[string]error{},
	}
	for _, p := range files {
		f.existing[p] = true
	}
	return f
}

func (f *fakeFS) Rename(from, to string) error {
	f.calls = append(f.calls, "rename:"+from+"→"+to)
	if err := f.renameErr[[2]string{from, to}]; err != nil {
		return err
	}
	if !f.existing[from] {
		return os.ErrNotExist
	}
	delete(f.existing, from)
	f.existing[to] = true
	return nil
}

func (f *fakeFS) Remove(path string) error {
	f.calls = append(f.calls, "remove:"+path)
	if err := f.removeErr[path]; err != nil {
		return err
	}
	delete(f.existing, path)
	return nil
}

func (f *fakeFS) Stat(path string) (bool, error) {
	if err, ok := f.statErr[path]; ok {
		return false, err
	}
	return f.existing[path], nil
}

func (f *fakeFS) Exists(path string) bool { return f.existing[path] }

func TestUpdateBinary_HappyPath_ServiceRunning(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("post-update SerialHop.exe missing")
	}
	if fs.Exists("C:\\bin\\SerialHop.exe.old") {
		t.Error(".old should be cleaned up best-effort on success")
	}
	if !scm.services[ServiceName].started {
		t.Error("service should be restarted")
	}
}

func TestUpdateBinary_ServiceNotInstalled(t *testing.T) {
	scm := newFakeSCM() // no services
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("post-update SerialHop.exe missing")
	}
}

func TestUpdateBinary_ServiceAlreadyStopped(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateStopped}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
	if scm.services[ServiceName].started {
		t.Error("service was stopped before the update; should not be restarted")
	}
}

func TestUpdateBinary_StaleOldGetsCleanedFirst(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS(
		"C:\\bin\\SerialHop.exe",
		"C:\\bin\\SerialHop.exe.old",
		"C:\\bin\\SerialHop-v0.7.0.exe",
	)
	if err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("updateBinary: %v", err)
	}
}

func TestUpdateBinary_RenameTargetToOldFails_ServiceRestored(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")
	// Force every retry of the rename to fail.
	fs.renameErr[[2]string{"C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop.exe.old"}] = errors.New("AV holding handle")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected rename error")
	}
	if !scm.services[ServiceName].started {
		t.Error("service should be restarted on rollback")
	}
}

func TestUpdateBinary_RenameSrcToTargetFails_FullRollback(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")
	fs.renameErr[[2]string{"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe"}] = errors.New("cross-volume")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("rollback should restore SerialHop.exe")
	}
	if !scm.services[ServiceName].started {
		t.Error("service should be restarted on rollback")
	}
}

func TestUpdateBinary_StartFails_FullRollback(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped},
		startErr:         errors.New("new binary refuses to start"),
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	// After rollback: original exe back in place, new exe preserved under its versioned name.
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("rollback should restore SerialHop.exe")
	}
	if !fs.Exists("C:\\bin\\SerialHop-v0.7.0.exe") {
		t.Error("new exe should be preserved under its versioned name for inspection")
	}
}

func TestUpdateBinary_StartTimesOut_FullRollback(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state: StateRunning,
		stateProgression: []ServiceState{
			StateRunning,      // initial query in updateBinary
			StateStopped,      // after stop, waitForState succeeds
			StateStartPending, // first wait-for-Running poll after start
			StateStartPending, // never reaches Running; eventually times out
		},
	}
	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		20*time.Millisecond, time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err should mention 'timed out': %v", err)
	}
	// After rollback: original exe back at target, new exe preserved under its versioned name.
	if !fs.Exists("C:\\bin\\SerialHop.exe") {
		t.Error("rollback should restore SerialHop.exe")
	}
	if !fs.Exists("C:\\bin\\SerialHop-v0.7.0.exe") {
		t.Error("new exe should be preserved under its versioned name for inspection")
	}
}

func TestUpdateBinary_StatFails_ServiceRestored(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	svc := scm.services[ServiceName]

	fs := newFakeFS("C:\\bin\\SerialHop.exe", "C:\\bin\\SerialHop-v0.7.0.exe")
	fs.statErr["C:\\bin\\SerialHop.exe"] = errors.New("stat: permission denied")

	err := updateBinary(scm, fs,
		"C:\\bin\\SerialHop-v0.7.0.exe", "C:\\bin\\SerialHop.exe",
		time.Second, 50*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("expected stat error to propagate; got nil")
	}
	if !strings.Contains(err.Error(), "stat target") {
		t.Errorf("error should mention stat target; got %v", err)
	}
	if !svc.started {
		t.Errorf("service should have been restarted after stat failure")
	}
}

// --- InstallOrUpgrade ---------------------------------------------------------

func TestInstallOrUpgrade_FreshInstall_NoService(t *testing.T) {
	scm := newFakeSCM() // no service registered
	dir := t.TempDir()
	src := filepath.Join(dir, "SerialHop-v0.7.0.exe")
	target := filepath.Join(dir, "SerialHop.exe")
	// Only src exists; target is intentionally absent (true fresh install).
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := InstallOrUpgrade(scm, src, target); err != nil {
		t.Fatalf("InstallOrUpgrade: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target should exist after fresh install: %v", err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("src should be renamed away: stat err = %v", err)
	}
	// Verify the new content is in place.
	content, err := os.ReadFile(target) //nolint:gosec // target is a t.TempDir()-rooted path
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(content) != "payload" {
		t.Fatalf("target content = %q; want %q", content, "payload")
	}
}

func TestInstallOrUpgrade_UpgradeWithRunningService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "SerialHop-v0.7.0.exe")
	target := filepath.Join(dir, "SerialHop.exe")
	if err := os.WriteFile(src, []byte("new payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(target, []byte("old payload"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	if err := InstallOrUpgrade(scm, src, target); err != nil {
		t.Fatalf("InstallOrUpgrade: %v", err)
	}
	got, err := os.ReadFile(target) //nolint:gosec // target is a t.TempDir()-rooted path
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new payload" {
		t.Fatalf("target content = %q; want %q", got, "new payload")
	}
	svc := scm.services[ServiceName]
	if !svc.started {
		t.Errorf("service should have been started after swap")
	}
}

func TestUpdateBinary_FreshInstall_MissingTarget(t *testing.T) {
	scm := newFakeSCM() // no service
	dir := t.TempDir()
	src := filepath.Join(dir, "SerialHop-v0.7.0.exe")
	target := filepath.Join(dir, "SerialHop.exe")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	// target deliberately does NOT exist.

	err := updateBinary(scm, realFS{}, src, target, time.Second, 50*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("updateBinary on fresh install: %v", err)
	}
	got, err := os.ReadFile(target) //nolint:gosec // target is a t.TempDir()-rooted path
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("target content = %q; want %q", got, "payload")
	}
}
