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

func (c *logTailController) start(streamID string, emit func(name string, data interface{})) {
	c.stop()
	path, ok := streamPath(streamID)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	parse := streamID == "service" // service log is slog JSON; others are raw

	onLine := func(line string) {
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
		emit("log:line", payload)
	}
	onRotate := func() {
		emit("log:rotated", map[string]string{"stream": streamID})
	}

	tailer := NewFileTail(path, 500*time.Millisecond, onLine, onRotate)

	c.mu.Lock()
	c.cancel = cancel
	c.tailer = tailer
	c.stream = streamID
	c.mu.Unlock()

	go tailer.Run(ctx)
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
		return paths.PanelErrorLogPath(), true
	}
	return "", false
}
