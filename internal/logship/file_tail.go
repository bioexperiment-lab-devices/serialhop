package logship

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// fileTail watches a slog JSON log file, persists its byte position to
// offsetPath, and pushes each new line into q as a panel-stream record.
// Designed as a single goroutine started by Manager.startPanelTailer.
type fileTail struct {
	q          *queue
	path       string
	offsetPath string
	stream     string
	poll       time.Duration

	// loggedMissing is flipped true on the first ENOENT to suppress
	// repeated INFOs for a file that hasn't been created yet.
	loggedMissing bool
}

const fileTailScannerBufferSize = 1 << 20 // 1 MiB; matches stderr tap

func (ft *fileTail) run(ctx context.Context) {
	t := time.NewTicker(ft.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ft.tick()
	}
}

func (ft *fileTail) tick() {
	st, err := os.Stat(ft.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !ft.loggedMissing {
				slog.Info("panel log not yet present, will retry", "path", ft.path)
				ft.loggedMissing = true
			}
			return
		}
		slog.Warn("panel log stat failed", "path", ft.path, "err", err)
		return
	}
	ft.loggedMissing = false

	saved, savedErr := readOffset(ft.offsetPath)
	startAt := int64(0)
	switch {
	case savedErr != nil && os.IsNotExist(savedErr):
		// Cold start — anchor to current EOF so we ship only new lines.
		startAt = st.Size()
		_ = writeOffsetAtomic(ft.offsetPath, offsetState{
			Size:          st.Size(),
			MTimeUnixNano: st.ModTime().UnixNano(),
			ByteOffset:    startAt,
		})
		return
	case savedErr != nil:
		// Corrupt — fall back to current EOF, log a warn once.
		slog.Warn("panel log offset reset", "reason", savedErr.Error(), "path", ft.offsetPath)
		startAt = st.Size()
		_ = writeOffsetAtomic(ft.offsetPath, offsetState{
			Size:          st.Size(),
			MTimeUnixNano: st.ModTime().UnixNano(),
			ByteOffset:    startAt,
		})
		return
	default:
		startAt = saved.ByteOffset
		if st.Size() < startAt {
			// Rotation or truncation: rebase to 0.
			startAt = 0
		}
	}

	if st.Size() == startAt {
		return
	}

	f, err := os.Open(ft.path) //nolint:gosec // ft.path is paths.PanelLogPath()
	if err != nil {
		slog.Warn("panel log open failed", "path", ft.path, "err", err)
		return
	}
	defer f.Close() //nolint:errcheck

	if _, err := f.Seek(startAt, io.SeekStart); err != nil {
		slog.Warn("panel log seek failed", "offset", startAt, "err", err)
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), fileTailScannerBufferSize)
	pos := startAt
	for scanner.Scan() {
		line := scanner.Text()
		pos += int64(len(scanner.Bytes())) + 1 // +1 for newline
		ft.q.push(record{
			stream: ft.stream,
			tsNano: time.Now().UnixNano(),
			line:   line,
		})
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("panel log scanner error", "err", err)
		// Advance past the broken segment so the next tick doesn't retry it.
		// We can't determine where the oversized line ends without re-reading,
		// so skip to the current EOF. A few trailing lines may be lost, but the
		// tailer becomes unstuck — which is what the spec (§8.2) requires.
		pos = st.Size()
	}

	_ = writeOffsetAtomic(ft.offsetPath, offsetState{
		Size:          st.Size(),
		MTimeUnixNano: st.ModTime().UnixNano(),
		ByteOffset:    pos,
	})
}

// startPanelTailer launches a fileTail goroutine and returns a stop func.
// Used by Manager to bind tailer lifetime to the manager's lifetime.
func startPanelTailer(q *queue, path, offsetPath string, poll time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: poll}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ft.run(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
