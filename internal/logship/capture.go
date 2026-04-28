package logship

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// installSlogTap replaces slog.Default with a JSON handler whose writer
// is io.MultiWriter(diskWriter, queueWriter). Each line slog emits goes
// both to the durable on-disk log and (if q != nil) to the in-memory
// queue. The level is read from levelVar at every log call.
func installSlogTap(disk *lumberjack.Logger, levelVar *slog.LevelVar, q *queue) error {
	writers := []io.Writer{disk}
	if q != nil {
		writers = append(writers, &queueWriter{q: q, stream: "stdout"})
	}
	w := io.MultiWriter(writers...)
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: levelVar})
	slog.SetDefault(slog.New(h))
	return nil
}

// queueWriter writes each \n-terminated line as one record into q.
//
// The slog JSON handler emits one JSON record per Write call ending in
// \n, so we treat each Write as one record. We still split on \n
// defensively in case a future writer batches.
type queueWriter struct {
	q      *queue
	stream string
}

func (w *queueWriter) Write(p []byte) (int, error) {
	if w.q == nil {
		return len(p), nil
	}
	now := time.Now().UnixNano()
	rest := p
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		var line []byte
		if i < 0 {
			line = rest
			rest = nil
		} else {
			line = rest[:i]
			rest = rest[i+1:]
		}
		if len(line) == 0 {
			continue
		}
		w.q.push(record{stream: w.stream, tsNano: now, line: string(line)})
	}
	return len(p), nil
}

// stderrTap holds the side state needed to undo installStderrTap on Shutdown.
type stderrTap struct {
	prev    *os.File // saved os.Stderr from before install
	pipeR   *os.File
	pipeW   *os.File
	disk    *lumberjack.Logger
	wg      sync.WaitGroup
	closing chan struct{}
}

func (t *stderrTap) close() {
	if t == nil {
		return
	}
	close(t.closing)
	// Closing the pipe writer unblocks the reader.
	_ = t.pipeW.Close()
	t.wg.Wait()
	_ = t.pipeR.Close()
	os.Stderr = t.prev
}

const stderrScannerBufferSize = 1 << 20 // 1 MiB

// installStderrTap re-points os.Stderr at a pipe whose reader fans each
// line out to disk (lumberjack) and to q (if non-nil). Returns a tap
// handle whose close() restores os.Stderr.
func installStderrTap(disk *lumberjack.Logger, q *queue) (*stderrTap, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("os.Pipe: %w", err)
	}
	prevStderr := os.Stderr
	os.Stderr = pw

	tap := &stderrTap{
		prev:    prevStderr,
		pipeR:   pr,
		pipeW:   pw,
		disk:    disk,
		closing: make(chan struct{}),
	}
	tap.wg.Add(1)
	go tap.runReader(q)
	return tap, nil
}

func (t *stderrTap) runReader(q *queue) {
	defer t.wg.Done()
	for {
		scanner := bufio.NewScanner(t.pipeR)
		scanner.Buffer(make([]byte, 64*1024), stderrScannerBufferSize)
		for scanner.Scan() {
			line := scanner.Text()
			if _, err := t.disk.Write([]byte(line + "\n")); err != nil {
				// Disk write failure — log via slog and keep going.
				slog.Warn("logship stderr disk write failed", "err", err)
			}
			if q != nil {
				q.push(record{stream: "stderr", tsNano: time.Now().UnixNano(), line: line})
			}
		}
		err := scanner.Err()
		select {
		case <-t.closing:
			return
		default:
		}
		if err != nil {
			slog.Warn("logship stderr scanner error (recreating)", "err", err)
			continue // recreate scanner on the same pipe — never exit while writers are active
		}
		// EOF without close: pipe writer was closed externally. Exit.
		return
	}
}
