package streamer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// stderrTailMaxBytes caps the captured stderr+stdout per session.
// ffmpeg's startup output is typically < 4 KB; a verbose failure log is
// usually under 16 KB. We cap at 64 KB to avoid unbounded memory if the
// child writes a flood before exiting.
const stderrTailMaxBytes = 64 * 1024

// Session is a running ffmpeg child.
//
// stderr (and stdout — combined) is captured into an internal
// bytes.Buffer by exec.Cmd's own internal copy goroutines, not by a
// manual drainer. Go's os/exec guarantees those goroutines finish
// before cmd.Wait() returns, so accessing the buffer after Done() is
// race-free without our own synchronization. This replaces an earlier
// pipe+Scanner design that had a subtle race for fast-exiting children
// (the drainer hadn't read the pipe yet when consumers queried it).
type Session struct {
	cfg SessionConfig

	cmd    *exec.Cmd
	output *bytes.Buffer // populated by exec.Cmd; safe to read after <-Done()
	done   chan struct{}

	mu       sync.Mutex
	exitCode int
	exitErr  error
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

	// Combined stdout+stderr capture into a single buffer. We wrap with
	// a limited writer so a runaway child can't grow the buffer past
	// stderrTailMaxBytes. exec.Cmd serializes writes from its two
	// internal copy goroutines (one per stream) through this single
	// Writer, so the combined buffer sees an interleaved-but-complete
	// transcript with no data race.
	buf := &bytes.Buffer{}
	limited := &cappedWriter{buf: buf, cap: stderrTailMaxBytes}
	cmd.Stdout = limited
	cmd.Stderr = limited

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("streamer: start: %w", err)
	}
	s := &Session{
		cfg:    cfg,
		cmd:    cmd,
		output: buf,
		done:   make(chan struct{}),
	}
	go s.wait()
	return s, nil
}

func (s *Session) wait() {
	// exec.Cmd's Wait blocks until the child exits AND the internal
	// stdout/stderr copy goroutines finish. After Wait returns, our
	// output buffer is stable — no more writes can occur — so
	// consumers reading via Done() see the complete transcript.
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

// LastError returns the last non-blank line from the captured
// transcript. Kept for backwards compat; for richer debugging use
// StderrTail.
func (s *Session) LastError() string {
	all := s.StderrTail()
	if all == "" {
		return ""
	}
	lines := strings.Split(all, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if ln := strings.TrimSpace(lines[i]); ln != "" {
			return ln
		}
	}
	return ""
}

// StderrTail returns the captured stdout+stderr transcript. Only safe
// to call after Done() is closed; before that, exec.Cmd's internal
// goroutines may still be writing.
func (s *Session) StderrTail() string {
	return s.output.String()
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

// cappedWriter wraps a bytes.Buffer to enforce an upper bound on the
// captured transcript. ffmpeg can be verbose if it doesn't die
// immediately; without a cap a long-running session would slowly
// accumulate megabytes of debug output in panel memory.
type cappedWriter struct {
	buf *bytes.Buffer
	cap int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remain := w.cap - w.buf.Len()
	if remain <= 0 {
		// Pretend we consumed it; the producer keeps writing but we
		// silently drop. We return n=len(p) so exec.Cmd's copy loop
		// doesn't error out.
		return len(p), nil
	}
	if len(p) > remain {
		w.buf.Write(p[:remain])
		w.buf.WriteString("\n... (truncated)")
		return len(p), nil
	}
	return w.buf.Write(p)
}
