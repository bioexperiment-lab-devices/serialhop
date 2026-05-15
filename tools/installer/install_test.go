package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// fakeFS records writes and renames; it does not touch disk.
type fakeFS struct {
	mu      sync.Mutex
	files   map[string][]byte
	dirs    map[string]bool
	renames []renameOp

	writeErr  error
	renameErr error
}

type renameOp struct{ from, to string }

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string][]byte{}, dirs: map[string]bool{}}
}

func (f *fakeFS) MkdirAll(path string, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[path] = true
	return nil
}

func (f *fakeFS) WriteFile(path string, data []byte, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.files[path] = cp
	return nil
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[path]
	if !ok {
		return nil, errors.New("fakeFS: not found")
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func (f *fakeFS) Rename(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renameErr != nil {
		return f.renameErr
	}
	b, ok := f.files[from]
	if !ok {
		return errors.New("fakeFS: src missing for rename")
	}
	f.files[to] = b
	delete(f.files, from)
	f.renames = append(f.renames, renameOp{from, to})
	return nil
}

func (f *fakeFS) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, path)
	return nil
}

func (f *fakeFS) Stat(path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[path]
	return ok, nil
}

func (f *fakeFS) Copy(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[from]
	if !ok {
		return errors.New("fakeFS: src missing for copy")
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	f.files[to] = cp
	return nil
}

// fakeVersionReader returns a configured version for any path.
type fakeVersionReader struct {
	versions map[string]string
	err      error
}

func (f *fakeVersionReader) Read(path string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.versions[path]
	if !ok {
		return "", errors.New("fakeVersionReader: no version configured")
	}
	return v, nil
}

// fakeShortcutWriter records the last opts and can be configured to fail.
type fakeShortcutWriter struct {
	called bool
	last   shortcutOpts
	err    error
}

func (f *fakeShortcutWriter) Write(opts shortcutOpts) error {
	f.called = true
	f.last = opts
	return f.err
}

// fakeLauncher records the path and can be configured to fail.
type fakeLauncher struct {
	called bool
	path   string
	err    error
}

func (f *fakeLauncher) Launch(path string) error {
	f.called = true
	f.path = path
	return f.err
}

// fakeInstaller assembles a Runner with all fakes wired up. Helper for tests.
func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{
		FS:             newFakeFS(),
		VersionReader:  &fakeVersionReader{versions: map[string]string{}},
		ShortcutWriter: &fakeShortcutWriter{},
		Launcher:       &fakeLauncher{},
		// SCM and DialSCM are stubbed by tests that exercise the SCM path.
		BundledVersion: "0.7.0",
		Payload:        []byte("payload bytes v0.7.0"),
	}
}

func TestRun_FreshInstall_HappyPath(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if res.State != StateFresh {
		t.Errorf("state = %v; want fresh", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
	sw := r.ShortcutWriter.(*fakeShortcutWriter)
	if !sw.called {
		t.Errorf("expected shortcut to be written on fresh install")
	}
	if sw.last.Target != filepath.Join(opts.InstallDir, "SerialHop.exe") {
		t.Errorf("shortcut target = %q; want unversioned SerialHop.exe under install dir", sw.last.Target)
	}
	l := r.Launcher.(*fakeLauncher)
	if !l.called {
		t.Errorf("expected panel to be launched on fresh install")
	}
}

// fakeSCMDialer + noOpSCM let tests exercise the DialSCM path without going
// through the real Windows SCM. noOpSCM satisfies winsvc.SCMConn; OpenService
// always returns ErrServiceMissing so InstallOrUpgrade's "service not installed"
// branch is taken — which matches the fresh-install case.
type fakeSCMDialer struct {
	conn winsvc.SCMConn
	err  error
}

func (f *fakeSCMDialer) Dial() (winsvc.SCMConn, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

type noOpSCM struct{}

func (noOpSCM) Disconnect() error                             { return nil }
func (noOpSCM) OpenService(string) (winsvc.SCMService, error) { return nil, winsvc.ErrServiceMissing }
func (noOpSCM) CreateService(string, winsvc.ServiceConfig) (winsvc.SCMService, error) {
	return nil, errors.New("noOpSCM: CreateService not implemented")
}

func TestRun_SameVersion_NoOp(t *testing.T) {
	r := newTestRunner(t)
	installDir := `C:\Program Files\SerialHop`
	target := filepath.Join(installDir, "SerialHop.exe")
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("pretend this is the installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = r.BundledVersion // same as installer

	// DialSCM should NOT be called on same-version path. Wire a dialer that
	// fails the test if invoked.
	r.DialSCM = func() (winsvc.SCMConn, error) {
		t.Fatalf("DialSCM must not be called on same-version path")
		return nil, nil
	}

	opts := options{InstallDir: installDir}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if res.State != StateSame {
		t.Errorf("state = %v; want same", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
	// Same-version still refreshes shortcut and launches.
	sw := r.ShortcutWriter.(*fakeShortcutWriter)
	if !sw.called {
		t.Errorf("expected shortcut to be refreshed on same-version re-run")
	}
	l := r.Launcher.(*fakeLauncher)
	if !l.called {
		t.Errorf("expected panel to be launched on same-version re-run")
	}
	// No payload should have been written.
	for path := range fs.files {
		if filepath.Base(path) == "SerialHop-v"+r.BundledVersion+".exe" {
			t.Errorf("unexpected staged payload at %s on same-version path", path)
		}
	}
}

func TestRun_DowngradeRefused(t *testing.T) {
	r := newTestRunner(t)
	installDir := `C:\Program Files\SerialHop`
	target := filepath.Join(installDir, "SerialHop.exe")
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = "0.8.0" // newer than r.BundledVersion="0.7.0"

	r.DialSCM = func() (winsvc.SCMConn, error) {
		t.Fatalf("DialSCM must not be called when downgrade is refused")
		return nil, nil
	}

	opts := options{InstallDir: installDir} // no AllowDowngrade
	res := r.Run(opts)
	if res.Err == nil {
		t.Fatalf("expected error refusing downgrade; got nil")
	}
	if res.ExitCode != 1 {
		t.Errorf("exit code = %d; want 1", res.ExitCode)
	}
	if res.State != StateDowngrade {
		t.Errorf("state = %v; want downgrade", res.State)
	}
	// Shortcut and launcher should not have been called.
	if r.ShortcutWriter.(*fakeShortcutWriter).called {
		t.Errorf("shortcut writer should not run on refused downgrade")
	}
	if r.Launcher.(*fakeLauncher).called {
		t.Errorf("launcher should not run on refused downgrade")
	}
}

func TestRun_DowngradeWithFlag_Proceeds(t *testing.T) {
	r := newTestRunner(t)
	installDir := `C:\Program Files\SerialHop`
	target := filepath.Join(installDir, "SerialHop.exe")
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("newer installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = "0.8.0"
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial

	opts := options{InstallDir: installDir, AllowDowngrade: true}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run with --allow-downgrade: %v", res.Err)
	}
	if res.State != StateDowngrade {
		t.Errorf("state = %v; want downgrade", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
}

func TestRun_Upgrade_HappyPath(t *testing.T) {
	r := newTestRunner(t)
	installDir := `C:\Program Files\SerialHop`
	target := filepath.Join(installDir, "SerialHop.exe")
	fs := r.FS.(*fakeFS)
	fs.files[target] = []byte("old installed exe")
	vr := r.VersionReader.(*fakeVersionReader)
	vr.versions[target] = "0.6.1" // older than r.BundledVersion="0.7.0"
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial

	opts := options{InstallDir: installDir}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run upgrade: %v", res.Err)
	}
	if res.State != StateUpgrade {
		t.Errorf("state = %v; want upgrade", res.State)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0", res.ExitCode)
	}
	// Target should now hold the new payload bytes after the rename swap.
	got, err := fs.ReadFile(target)
	if err != nil {
		t.Fatalf("read back target: %v", err)
	}
	if string(got) != string(r.Payload) {
		t.Errorf("target content = %q; want %q (payload)", got, r.Payload)
	}
}

func TestRun_NoShortcutFlag(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`, NoShortcut: true}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if r.ShortcutWriter.(*fakeShortcutWriter).called {
		t.Errorf("shortcut writer must not be called when --no-shortcut is set")
	}
}

func TestRun_NoLaunchFlag(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`, NoLaunch: true}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if r.Launcher.(*fakeLauncher).called {
		t.Errorf("launcher must not be called when --no-launch is set")
	}
}

func TestRun_SilentImpliesNoLaunch(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	opts := options{InstallDir: `C:\Program Files\SerialHop`, Silent: true}

	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if r.Launcher.(*fakeLauncher).called {
		t.Errorf("launcher must not be called when --silent is set")
	}
}

func TestRun_ShortcutFailure_IsNonFatal(t *testing.T) {
	r := newTestRunner(t)
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial
	sw := r.ShortcutWriter.(*fakeShortcutWriter)
	sw.err = errors.New("shortcut path not writable")

	opts := options{InstallDir: `C:\Program Files\SerialHop`}
	res := r.Run(opts)
	if res.Err != nil {
		t.Fatalf("expected nil err on non-fatal shortcut failure; got %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d; want 0 (shortcut failure is non-fatal)", res.ExitCode)
	}
	if !strings.Contains(res.Message, "desktop shortcut creation failed") {
		t.Errorf("message should mention shortcut failure; got %q", res.Message)
	}
}

// tamperingFakeFS wraps fakeFS and corrupts the readback to simulate a
// silent on-disk corruption between WriteFile and ReadFile. Used to test
// the SHA-256 self-check.
type tamperingFakeFS struct{ *fakeFS }

func (t *tamperingFakeFS) ReadFile(path string) ([]byte, error) {
	b, err := t.fakeFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		b[0] ^= 0xff
	}
	return b, nil
}

func TestRun_PayloadShaMismatch(t *testing.T) {
	r := newTestRunner(t)
	r.FS = &tamperingFakeFS{fakeFS: newFakeFS()}
	scm := &fakeSCMDialer{conn: &noOpSCM{}}
	r.DialSCM = scm.Dial

	opts := options{InstallDir: `C:\Program Files\SerialHop`}
	res := r.Run(opts)
	if res.Err == nil {
		t.Fatalf("expected SHA mismatch error; got nil")
	}
	if res.ExitCode != 1 {
		t.Errorf("exit code = %d; want 1", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "integrity check failed") {
		t.Errorf("err should mention integrity check; got %v", res.Err)
	}
}
