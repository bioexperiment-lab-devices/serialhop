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
//
// The trust boundary is split explicitly: BinaryPath is always
// server-controlled (set by the Manager from paths.FFmpegPath()), while
// Args carries flag values that may originate from external sources
// (e.g. the lab-bridge `/start` request body). Args values become
// individual argv strings to a non-shell exec, so they cannot be
// interpreted as flag NAMES by ffmpeg — only the structurally-fixed
// positional URL (last arg) needs URL-scheme validation, which Manager
// performs before BuildWHIPArgs sees the values.
type SessionConfig struct {
	BinaryPath     string
	Args           []string
	Env            []string // extra env passed alongside os.Environ()
	GracefulPeriod time.Duration
}

// stderrTailCap is the maximum number of stderr lines we keep per
// session. ffmpeg's startup output is normally < 20 lines and a
// failure log is usually short; 32 covers both cases without
// unbounded memory if the child goes chatty.
const stderrTailCap = 32

// Session is a running ffmpeg child.
type Session struct {
	cfg SessionConfig

	cmd  *exec.Cmd
	done chan struct{}

	mu         sync.Mutex
	stderrTail []string // ring of the last ≤stderrTailCap lines, oldest first
	exitCode   int
	exitErr    error
}

// StartSession launches the child.
func StartSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if cfg.BinaryPath == "" {
		return nil, fmt.Errorf("streamer: empty BinaryPath")
	}
	if cfg.GracefulPeriod == 0 {
		cfg.GracefulPeriod = DefaultGracefulStopGrace
	}
	// G204: BinaryPath is server-controlled (paths.FFmpegPath()); Args
	// values are validated at the Manager boundary (validateStartRequest)
	// and flow to a non-shell exec, so they cannot be reinterpreted as
	// flag names.
	cmd := exec.CommandContext(ctx, cfg.BinaryPath, cfg.Args...) //nolint:gosec // see SessionConfig godoc — trust boundary documented and validated at Manager.Start
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
		line := scan.Text()
		s.mu.Lock()
		if len(s.stderrTail) >= stderrTailCap {
			s.stderrTail = append(s.stderrTail[1:], line)
		} else {
			s.stderrTail = append(s.stderrTail, line)
		}
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

// LastError returns the last stderr line. Kept for backwards compat
// with callers that surface a one-line indicator; for richer
// debugging use StderrTail.
func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stderrTail) == 0 {
		return ""
	}
	return s.stderrTail[len(s.stderrTail)-1]
}

// StderrTail returns the recent stderr lines joined with newlines.
// Capped to stderrTailCap lines; empty when nothing has been captured
// (e.g. child exited before writing anything).
func (s *Session) StderrTail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stderrTail) == 0 {
		return ""
	}
	out := s.stderrTail[0]
	for _, ln := range s.stderrTail[1:] {
		out += "\n" + ln
	}
	return out
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
