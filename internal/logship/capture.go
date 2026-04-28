package logship

import (
	"bytes"
	"io"
	"log/slog"
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
