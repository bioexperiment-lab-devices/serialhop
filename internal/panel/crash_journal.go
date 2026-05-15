package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// crashJournalMaxBytes caps the on-disk journal so a render-error loop
// cannot fill the disk. 64 KiB holds many entries while staying small
// enough to paste into a bug report.
const crashJournalMaxBytes int64 = 64 * 1024

// crashEntry is the JSON shape written per crash. Field order is kept
// stable — operators read this file by hand.
type crashEntry struct {
	Time    string `json:"time"`
	Version string `json:"version"`
	Source  string `json:"source"`
	Message string `json:"message"`
	Stack   string `json:"stack"`
}

// crashJournalPath returns the journal path, with env-var overrides used
// by tests. An empty result means "no journal here"; callers no-op.
func crashJournalPath() string {
	if os.Getenv("SERIALHOP_PANEL_CRASH_JOURNAL_DISABLE") == "1" {
		return ""
	}
	if v, ok := os.LookupEnv("SERIALHOP_PANEL_CRASH_JOURNAL_PATH"); ok {
		return v
	}
	return paths.PanelCrashJournalPath()
}

// appendCrashJournal writes one JSON line per crash. Best-effort: any
// error is recorded via writePanelDebugLog and swallowed. A panic guard
// is in place because this function runs inside React's componentDidCatch
// path — letting an exception escape would make the safety net itself a
// new crash source.
func appendCrashJournal(message, source, stack, ver string, now time.Time) {
	defer func() { _ = recover() }()
	path := crashJournalPath()
	if path == "" {
		return
	}
	entry := crashEntry{
		Time:    now.Format(time.RFC3339Nano),
		Version: ver,
		Source:  source,
		Message: message,
		Stack:   stack,
	}
	line, err := json.Marshal(&entry)
	if err != nil {
		writePanelDebugLog("crash_journal_marshal_failed", err)
		return
	}
	line = append(line, '\n')
	if err := appendCapped(path, line, crashJournalMaxBytes); err != nil {
		writePanelDebugLog("crash_journal_write_failed", err)
	}
}

// appendCapped appends data to path, then if the resulting file exceeds
// max bytes, rewrites it keeping only the trailing max bytes aligned to
// the next newline (so the first surviving entry isn't a fragment).
// Best-effort; returns the first I/O error encountered.
//
// Single-process panel ⇒ no cross-process locking is required.
func appendCapped(path string, data []byte, max int64) error {
	f, err := os.OpenFile(path, //nolint:gosec // path is paths.PanelCrashJournalPath() or test temp
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() <= max {
		return nil
	}

	rf, err := os.Open(path) //nolint:gosec // see above
	if err != nil {
		return err
	}
	defer rf.Close() //nolint:errcheck
	if _, err := rf.Seek(st.Size()-max, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, max)
	n, err := io.ReadFull(rf, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	buf = buf[:n]
	if idx := bytes.IndexByte(buf, '\n'); idx >= 0 && idx+1 < len(buf) {
		buf = buf[idx+1:]
	}
	return os.WriteFile(path, buf, 0o600)
}
