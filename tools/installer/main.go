package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

const defaultInstallDir = `C:\Program Files\SerialHop`

var (
	flagDir            = flag.String("dir", defaultInstallDir, "install directory (absolute path)")
	flagSilent         = flag.Bool("silent", false, "no dialog; proceed with defaults; output to stderr")
	flagNoLaunch       = flag.Bool("no-launch", false, "do not launch the panel after install")
	flagNoShortcut     = flag.Bool("no-shortcut", false, "do not create the desktop shortcut")
	flagAllowDowngrade = flag.Bool("allow-downgrade", false, "proceed even if the installed version is newer than this installer's payload")
	flagVersion        = flag.Bool("version", false, "print installer + payload version and exit")
)

func main() {
	flag.Parse()

	if *flagVersion {
		// Installer + payload share a version by construction (CI bumps both
		// version.json files atomically). The output prints them separately so
		// a future decoupling is straightforward.
		fmt.Printf("SerialHop Installer v%s (payload v%s)\n",
			internalversion.Base(), internalversion.Base())
		return
	}

	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "fatal: unexpected positional arguments:", flag.Args())
		os.Exit(2)
	}

	if !filepath.IsAbs(*flagDir) {
		fmt.Fprintln(os.Stderr, "fatal: --dir must be an absolute path:", *flagDir)
		os.Exit(2)
	}

	if err := enforceElevation(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}

	opts := &options{
		InstallDir:     *flagDir,
		Silent:         *flagSilent,
		NoLaunch:       *flagNoLaunch,
		NoShortcut:     *flagNoShortcut,
		AllowDowngrade: *flagAllowDowngrade,
	}

	configureLogging()

	if *flagSilent {
		os.Exit(runSilent(opts))
		return
	}
	os.Exit(runDialog(opts))
}

func runSilent(opts *options) int {
	r := newProductionRunner()
	res := r.Run(*opts)
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, "error:", res.Err)
		return res.ExitCode
	}
	if res.Message != "" {
		fmt.Println(res.Message)
	}
	return res.ExitCode
}

// newProductionRunner wires the production dependencies into a Runner.
// Called from both runSilent and the dialog's Install handler.
func newProductionRunner() *Runner {
	return &Runner{
		FS:             realFS{},
		VersionReader:  peReader{},
		ShortcutWriter: realShortcutWriter{},
		Launcher:       realLauncher{},
		DialSCM:        winsvc.DialSCM,
		BundledVersion: internalversion.Base(),
		Payload:        payload,
	}
}

func configureLogging() {
	// Diagnostic log file in %TEMP%. Spec §11.
	tmp := os.TempDir()
	logPath := filepath.Join(tmp, fmt.Sprintf("SerialHop-installer-%s.log", internalversion.Base()))
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		// Logging is best-effort; if we can't write the file, fall back to stderr.
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// realFS satisfies fsOps using the os package.
type realFS struct{}

func (realFS) MkdirAll(path string, mode uint32) error { return os.MkdirAll(path, os.FileMode(mode)) }
func (realFS) WriteFile(path string, data []byte, mode uint32) error {
	return os.WriteFile(path, data, os.FileMode(mode))
}
func (realFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (realFS) Rename(from, to string) error         { return os.Rename(from, to) }
func (realFS) Remove(path string) error             { return os.Remove(path) }
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

// peReader satisfies versionReader by delegating to readPEFileVersion (which
// has _windows / _other build-tag variants).
type peReader struct{}

func (peReader) Read(path string) (string, error) { return readPEFileVersion(path) }

// realLauncher starts the panel detached so the installer can exit.
type realLauncher struct{}

func (realLauncher) Launch(path string) error {
	cmd := exec.Command(path)
	// Inherits stdout/stderr to nowhere (windowsgui binary); the parent
	// installer process can exit while the child keeps running.
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach: do not Wait. The Go runtime will keep the child process going
	// after this process exits (the OS reaps it when its parent goes away
	// only on Unix; on Windows the child is independent unless explicitly
	// added to a job object).
	return nil
}
