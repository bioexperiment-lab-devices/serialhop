//go:build windows

package panel

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// logTailController owns at most one FileTail goroutine. Switching
// streams stops the existing tailer and starts a new one.
type logTailController struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	tailer *FileTail // held so Close() can release the constructor-held file descriptor on stream switch
	stream string
}

// logBacklogBytes is the size of the recent-history slice ReadBacklog
// extracts from each log file before live tailing begins. 256 KB is
// large enough to cover several minutes of normal slog output but
// small enough to keep the panel's first paint snappy.
const logBacklogBytes = 256 * 1024

// start (re)attaches the controller to streamID. It returns the recent
// backlog lines (oldest first) so the caller can deliver them to the UI
// synchronously, atomically with the start of live streaming: the live
// tailer attaches at the same byte offset the backlog read returned, so
// there is no gap or overlap.
func (c *logTailController) start(streamID string, emit func(name string, data interface{})) []map[string]interface{} {
	c.stop()
	path, ok := streamPath(streamID)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	parse := streamID == "service" || streamID == "panel" // slog JSON streams

	encodeLine := func(line string) map[string]interface{} {
		payload := map[string]interface{}{"stream": streamID}
		if parse {
			var rec map[string]interface{}
			if json.Unmarshal([]byte(line), &rec) == nil {
				payload["record"] = rec
			} else {
				payload["raw"] = line
			}
		} else {
			payload["raw"] = line
		}
		return payload
	}
	onLine := func(line string) {
		emit("log:line", encodeLine(line))
	}
	onRotate := func() {
		emit("log:rotated", map[string]string{"stream": streamID})
	}

	tailer := NewFileTail(path, 500*time.Millisecond, onLine, onRotate)

	// Capture backlog from the same file handle Run() is about to take
	// over; ReadBacklog restores the EOF position so live tailing picks
	// up exactly where the backlog ended.
	backlogLines := tailer.ReadBacklog(logBacklogBytes)
	backlog := make([]map[string]interface{}, 0, len(backlogLines))
	for _, l := range backlogLines {
		backlog = append(backlog, encodeLine(l))
	}

	c.mu.Lock()
	c.cancel = cancel
	c.tailer = tailer
	c.stream = streamID
	c.mu.Unlock()

	go tailer.Run(ctx)
	return backlog
}

func (c *logTailController) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if c.tailer != nil {
		// Close releases the constructor-held file descriptor when Run hasn't fully drained yet.
		_ = c.tailer.Close()
		c.tailer = nil
	}
	c.stream = ""
}

// streamPath maps a binding-level stream id to the on-disk path.
// Returns ok=false for unknown ids — the binding silently no-ops.
func streamPath(id string) (string, bool) {
	switch id {
	case "service":
		return paths.ServiceLogPath(), true
	case "stderr":
		return paths.StderrLogPath(), true
	case "panel":
		return paths.PanelLogPath(), true
	}
	return "", false
}
