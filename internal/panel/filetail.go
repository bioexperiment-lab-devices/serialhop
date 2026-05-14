package panel

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"time"
)

// FileTail follows a file appended-to by an external process, calling
// onLine with each newly-appended line and onRotate when it detects the
// file has been replaced underneath it (lumberjack rotation: same path,
// different inode / smaller size).
//
// The tailer always seeks to end-of-file when it first opens the target
// so it never emits backlog. If the file does not exist yet, the tailer
// silently retries each poll interval; when the file appears it attaches
// at offset 0 (logs created since the tailer started belong to the
// stream — but logs that existed before the tailer started do not).
type FileTail struct {
	path      string
	pollEvery time.Duration
	onLine    func(string)
	onRotate  func()

	// initFile is set by NewFileTail when the target file already exists
	// at construction time. It is seeked to EOF so the first Run cycle
	// won't replay backlog. Ownership transfers to Run on first use.
	initFile *os.File
	initStat os.FileInfo
}

// NewFileTail constructs a FileTail. If the target file already exists it
// is opened immediately (so that any writes that occur before Run is called
// are still visible as "new" data — they land after the seek-to-EOF
// position). onLine receives one line at a time (without the trailing
// newline). onRotate is called once per detected rotation. Both callbacks
// are invoked from FileTail's own goroutine — they must not block; route
// work onto a different channel if needed.
//
// Callers that never call Run must call Close to release the pre-opened
// file descriptor.
func NewFileTail(path string, pollEvery time.Duration, onLine func(string), onRotate func()) *FileTail {
	ft := &FileTail{path: path, pollEvery: pollEvery, onLine: onLine, onRotate: onRotate}
	// Eagerly open so seeks happen before any concurrent writes can race
	// past us. Errors are silently ignored — Run will retry on the first tick.
	if nf, err := os.Open(path); err == nil { //nolint:gosec // path comes from paths.*LogPath()
		if _, err := nf.Seek(0, io.SeekEnd); err != nil {
			_ = nf.Close()
		} else if ns, err := nf.Stat(); err != nil {
			_ = nf.Close()
		} else {
			ft.initFile = nf
			ft.initStat = ns
		}
	}
	return ft
}

// ReadBacklog reads the last maxBytes of the tailed file (or the whole
// file if it is smaller) and returns the lines contained therein. The
// first partial line (when the read does not start at offset 0) is
// dropped so callers never see a truncated record.
//
// It uses the constructor-held file handle, which sits at EOF after
// NewFileTail, and restores the handle to EOF before returning so the
// subsequent Run() picks up exactly where the backlog left off — there
// is no window in which writes can slip past unseen.
//
// No-op (returns nil) if the file did not exist at construction time;
// Run will attach at offset 0 of the next-created file.
func (t *FileTail) ReadBacklog(maxBytes int64) []string {
	if t.initFile == nil || maxBytes <= 0 {
		return nil
	}
	end, err := t.initFile.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil
	}
	start := end - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := t.initFile.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	buf := make([]byte, end-start)
	n, _ := io.ReadFull(t.initFile, buf)
	// Restore EOF for Run() to take over.
	if _, err := t.initFile.Seek(end, io.SeekStart); err != nil {
		return nil
	}
	if n == 0 {
		return nil
	}
	chunk := buf[:n]
	// Drop the partial first record when we didn't start at offset 0.
	if start > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			return nil
		}
		chunk = chunk[i+1:]
	}
	if len(chunk) == 0 {
		return nil
	}
	// Trim a trailing newline so we don't emit a spurious empty line.
	chunk = bytes.TrimRight(chunk, "\n")
	rawLines := bytes.Split(chunk, []byte{'\n'})
	out := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		l = bytes.TrimRight(l, "\r")
		out = append(out, string(l))
	}
	return out
}

// Close releases the file handle held by the constructor if Run was never
// called. Safe to call multiple times. Calling Close after Run has started
// is a no-op (Run owns the handle from that point on).
func (t *FileTail) Close() error {
	if t.initFile != nil {
		err := t.initFile.Close()
		t.initFile = nil
		return err
	}
	return nil
}

// Run blocks until ctx is cancelled, polling the file at pollEvery and
// emitting any new lines via onLine. Rotation is detected by comparing
// the current file's size + identity to what the tailer has open.
func (t *FileTail) Run(ctx context.Context) {
	var (
		f      *os.File
		reader *bufio.Reader
		stat   os.FileInfo
	)

	// Inherit the handle that NewFileTail opened eagerly (if any).
	if t.initFile != nil {
		f, reader, stat = t.initFile, bufio.NewReader(t.initFile), t.initStat
		t.initFile = nil
		t.initStat = nil
	}

	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	ticker := time.NewTicker(t.pollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// (Re)open if we don't have a handle.
		if f == nil {
			nf, err := os.Open(t.path) //nolint:gosec // path comes from paths.*LogPath()
			if err != nil {
				continue
			}
			// Post-rotation open: start at offset 0 so all content in the
			// new file is treated as live stream data.
			ns, err := nf.Stat()
			if err != nil {
				_ = nf.Close()
				continue
			}
			f, reader, stat = nf, bufio.NewReader(nf), ns
			// Drain immediately so rotation content is not delayed a tick.
			t.drain(f, reader, &stat)
			continue
		}

		// Detect rotation: compare the path's current file to ours.
		// Two heuristics combined catch lumberjack's behavior on Windows:
		//   (a) os.SameFile — different inode means a new file replaced
		//       ours at the same path.
		//   (b) size shrinkage — same handle but the underlying file
		//       got truncated.
		curStat, statErr := os.Stat(t.path)
		switch {
		case os.IsNotExist(statErr):
			_ = f.Close()
			f, reader, stat = nil, nil, nil
			t.onRotate()
			continue
		case statErr != nil:
			continue
		case !os.SameFile(stat, curStat):
			_ = f.Close()
			f, reader, stat = nil, nil, nil
			t.onRotate()
			continue
		case curStat.Size() < stat.Size():
			_ = f.Close()
			f, reader, stat = nil, nil, nil
			t.onRotate()
			continue
		}

		// Read any new bytes.
		t.drain(f, reader, &stat)
	}
}

// drain reads all available complete lines from f via reader and emits them
// via onLine. It updates *stat to reflect the file's current size so that
// the rotation-detection heuristics in Run stay accurate.
func (t *FileTail) drain(f *os.File, reader *bufio.Reader, stat *os.FileInfo) {
	curStat, err := f.Stat()
	if err != nil {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			// remember the size we caught up to
			*stat = curStat
			break
		}
		if err != nil {
			// partial read on transient error — discard; next tick re-reads
			break
		}
		// err == nil → complete line ending in \n
		n := len(line)
		if n > 0 && line[n-1] == '\n' {
			line = line[:n-1]
			n--
		}
		if n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		t.onLine(line)
	}
}
