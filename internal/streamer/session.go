package streamer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// SessionConfig is the input to StartSession.
type SessionConfig struct {
	Argv           []string
	Env            []string // extra env passed alongside os.Environ()
	GracefulPeriod time.Duration
}

// Session is a running ffmpeg child.
type Session struct {
	cfg SessionConfig

	cmd  *exec.Cmd
	done chan struct{}

	mu         sync.Mutex
	lastStderr string
	exitCode   int
	exitErr    error
}

// StartSession launches the child.
func StartSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if len(cfg.Argv) == 0 {
		return nil, fmt.Errorf("streamer: empty argv")
	}
	if cfg.GracefulPeriod == 0 {
		cfg.GracefulPeriod = DefaultGracefulStopGrace
	}
	cmd := exec.CommandContext(ctx, cfg.Argv[0], cfg.Argv[1:]...) //nolint:gosec // argv is constructed by trusted callers (Manager builds it from FFmpegResolver.Path + BuildWHIPArgs), not user input
	cmd.Env = append(os.Environ(), cfg.Env...)
	applyPlatformAttrs(cmd) // session_windows / session_other
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("streamer: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("streamer: start: %w", err)
	}
	s := &Session{
		cfg:  cfg,
		cmd:  cmd,
		done: make(chan struct{}),
	}
	go s.drainStderr(stderr)
	go s.wait()
	return s, nil
}

func (s *Session) drainStderr(r io.Reader) {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 16*1024), 64*1024)
	for scan.Scan() {
		s.mu.Lock()
		s.lastStderr = scan.Text()
		s.mu.Unlock()
	}
}

func (s *Session) wait() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.exitErr = err
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	s.mu.Unlock()
	close(s.done)
}

// Done is closed when the child exits.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitCode is the child's exit status; zero if still running.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// LastError returns the last stderr line (best-effort, not guaranteed to
// be the most informative one).
func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStderr
}

// PID returns the OS process id.
func (s *Session) PID() int { return s.cmd.Process.Pid }

// Stop asks the child to exit gracefully, then hard-kills it after
// SessionConfig.GracefulPeriod.
func (s *Session) Stop(ctx context.Context) error {
	_ = signalGraceful(s.cmd) // graceful may fail; fall through to grace+kill
	select {
	case <-s.done:
		return nil
	case <-time.After(s.cfg.GracefulPeriod):
	}
	return hardKill(s.cmd)
}
