package panel

import (
	"bufio"
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
		if len(line) > 0 {
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
		if err == io.EOF {
			*stat = curStat
			break
		}
		if err != nil {
			break
		}
	}
}
