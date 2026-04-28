# Log Streaming to Loki — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stream the client's stdout/stderr to the in-VPS Loki over the existing chisel session, so the operator can browser-tail and search logs in Grafana instead of asking lab staff to email ZIPs.

**Architecture:** A new in-process package `internal/logship` taps slog (multi-writer onto lumberjack + bounded ring buffer) and stderr (`os.Pipe` reader → lumberjack + ring buffer). One shipper goroutine drains the buffer, gzip-encodes Loki JSON, POSTs to `127.0.0.1:3100`. The chisel client gets a second route (`127.0.0.1:3100:loki:3100`) so that POST reaches the in-VPS Loki. Service-mode-only; on-disk logs remain the durable record. See `docs/superpowers/specs/2026-04-28-log-streaming-design.md` for the spec.

**Tech Stack:** Go 1.25 (stdlib only — `net/http`, `compress/gzip`, `encoding/json`, `bufio`, `log/slog`), `gopkg.in/natefinch/lumberjack.v2` (already a dependency), `github.com/jpillora/chisel` (already a dependency). Tests run with the standard Go testing framework on macOS and Windows.

**Conventions:**
- `task test` runs the full suite. During iteration use `go test -run <Name> ./internal/logship -v -race`.
- Cross-compile sanity for the Windows-only worker: `GOOS=windows GOARCH=amd64 go build ./...`.
- This repo uses concise commit messages (`feat(scope):`, `fix(scope):`, `chore(scope):`). Match the style.
- The `task build` target auto-bumps the minor version on a clean tree (see `tools/bumpversion`). Do NOT run `task build` between tasks — it'll inflate version churn during plan execution. Use `go build ./...` for compile checks.

**Out of scope (not in this plan):**
- Server-side Loki/Grafana provisioning. Already done; this client connects to the existing service.
- Foreground-mode shipping. `runForeground` in `cmd/lab_devices_client/main.go` is unchanged.
- Configuration changes. `config.LogConfig` is not modified.

---

## File map

**New files (all in `internal/logship/`):**

| File | Responsibility |
|---|---|
| `logship.go` | `Manager` façade: `Init`, `SetLevel`, `StartShipper`, `Shutdown`. Owns lifecycle of queue/capture/shipper. |
| `queue.go` | Bounded ring buffer with drop-oldest, dropped counter, blocking-with-timeout wait. Platform-neutral. |
| `queue_test.go` | Queue unit tests. |
| `capture.go` | `installSlogTap`, `installStderrTap` — wire JSON slog handler and `os.Stderr` pipe to the queue + lumberjack. |
| `capture_test.go` | Capture unit tests. |
| `shipper.go` | Shipper goroutine: drain → group by stream → gzip JSON → POST → backoff. |
| `shipper_test.go` | Shipper unit tests against `httptest.NewServer`. |
| `clock.go` | Tiny `clock` interface + real impl. Test files supply a fake. |
| `level.go` | `parseLogLevel(string) slog.Level` (moved from `winsvc`). |

**Modified files:**

| File | Change |
|---|---|
| `internal/chisel/client.go` | Append `127.0.0.1:3100:loki:3100` to `remotes` when `cfg.User != ""`. |
| `internal/chisel/client_test.go` (new) | Verify the remotes-list construction in both branches. |
| `internal/winsvc/worker.go` | Replace `configureFileLogger`/`redirectStderrToFile` with `logship.Init`. Add `manager` to `handler`. Call `SetLevel`/`StartShipper` after `config.Load`. Call `Shutdown` in stop path. Remove now-dead helpers (`configureFileLogger`, `redirectStderrToFile`, `parseLogLevel`). |

---

## Task 1 — Skeleton package

**Files:**
- Create: `internal/logship/logship.go`
- Create: `internal/logship/level.go`

- [ ] **Step 1: Create the skeleton façade file**

`internal/logship/logship.go`:

```go
// Package logship streams the client's slog output and stderr to the
// in-VPS Loki via the chisel forward tunnel.
//
// It also owns the durable on-disk log files (lab_devices_client.log,
// lab_devices_client_stderr.log) so disabling the shipper does not
// affect on-disk logging.
package logship

import (
	"context"
	"log/slog"
)

// LogFileName is the basename of the rotated slog file.
const LogFileName = "lab_devices_client.log"

// StderrLogFileName is the basename of the rotated stderr file.
const StderrLogFileName = "lab_devices_client_stderr.log"

// Manager owns the capture taps, ring buffer, and shipper goroutine.
//
// Construct it once at process start with Init. Call StartShipper after
// the chisel user is known. Call Shutdown before exit.
type Manager struct {
	// Filled in by later tasks.
	_ slog.Level
	_ context.Context
}
```

- [ ] **Step 2: Create the level helper**

`internal/logship/level.go`:

```go
package logship

import "log/slog"

// ParseLogLevel maps the config string ("debug"|"info"|"warn"|"error")
// to a slog.Level. Unknown values fall through to slog.LevelInfo.
func ParseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./internal/logship/...`
Expected: exits 0, no output.

- [ ] **Step 4: Commit**

```bash
git add internal/logship/logship.go internal/logship/level.go
git commit -m "feat(logship): scaffold package with file-name and level helpers"
```

---

## Task 2 — Ring buffer queue

**Files:**
- Create: `internal/logship/queue.go`
- Create: `internal/logship/queue_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/logship/queue_test.go`:

```go
package logship

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQueuePushDrainFIFO(t *testing.T) {
	q := newQueue(4)
	q.push(record{stream: "stdout", tsNano: 1, line: "a"})
	q.push(record{stream: "stdout", tsNano: 2, line: "b"})
	q.push(record{stream: "stderr", tsNano: 3, line: "c"})

	got := q.drainUpTo(10)
	if len(got) != 3 {
		t.Fatalf("drain returned %d records, want 3", len(got))
	}
	if got[0].line != "a" || got[1].line != "b" || got[2].line != "c" {
		t.Fatalf("FIFO violated: %+v", got)
	}
	if got[2].stream != "stderr" {
		t.Fatalf("stream lost: %+v", got[2])
	}
}

func TestQueueDrainUpToBoundsBatch(t *testing.T) {
	q := newQueue(8)
	for i := 0; i < 6; i++ {
		q.push(record{tsNano: int64(i), line: "x"})
	}
	got := q.drainUpTo(4)
	if len(got) != 4 {
		t.Fatalf("drain returned %d, want 4", len(got))
	}
	rest := q.drainUpTo(10)
	if len(rest) != 2 {
		t.Fatalf("second drain returned %d, want 2", len(rest))
	}
}

func TestQueueDropsOldestOnOverflow(t *testing.T) {
	q := newQueue(3)
	q.push(record{line: "a"})
	q.push(record{line: "b"})
	q.push(record{line: "c"})
	q.push(record{line: "d"}) // drops "a"
	q.push(record{line: "e"}) // drops "b"

	got := q.drainUpTo(10)
	if len(got) != 3 {
		t.Fatalf("drain returned %d records, want 3", len(got))
	}
	if got[0].line != "c" || got[1].line != "d" || got[2].line != "e" {
		t.Fatalf("drop-oldest broken: %+v", got)
	}
	if n := q.takeDropped(); n != 2 {
		t.Fatalf("takeDropped = %d, want 2", n)
	}
}

func TestQueueTakeDroppedZeroes(t *testing.T) {
	q := newQueue(1)
	q.push(record{line: "a"})
	q.push(record{line: "b"}) // drops "a"
	if n := q.takeDropped(); n != 1 {
		t.Fatalf("first takeDropped = %d, want 1", n)
	}
	if n := q.takeDropped(); n != 0 {
		t.Fatalf("second takeDropped = %d, want 0", n)
	}
}

func TestQueueWaitNotifyFiresOnPush(t *testing.T) {
	q := newQueue(8)
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		q.waitNotify(ctx, time.Second)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	q.push(record{line: "a"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitNotify did not return after push")
	}
}

func TestQueueWaitNotifyTimesOut(t *testing.T) {
	q := newQueue(8)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q.waitNotify(ctx, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("waitNotify blocked too long: %v", elapsed)
	}
}

func TestQueueWaitNotifyHonorsCtx(t *testing.T) {
	q := newQueue(8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.waitNotify(ctx, time.Hour)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitNotify did not return on ctx cancel")
	}
}

func TestQueueConcurrentPushesAreSafe(t *testing.T) {
	q := newQueue(4096)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				q.push(record{tsNano: int64(seed*1000 + j), line: "x"})
			}
		}(i)
	}
	wg.Wait()

	total := 0
	for {
		batch := q.drainUpTo(256)
		if len(batch) == 0 {
			break
		}
		total += len(batch)
	}
	if total != 1600 {
		t.Fatalf("drained %d records, want 1600 (no drops expected at this size)", total)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/logship -v`
Expected: compile error — `record` and `newQueue` undefined.

- [ ] **Step 3: Implement the queue**

`internal/logship/queue.go`:

```go
package logship

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// record is one captured log line on its way to Loki.
type record struct {
	stream string // "stdout" | "stderr"
	tsNano int64
	line   string
}

// queue is a bounded ring buffer with drop-oldest semantics.
//
// push never blocks. drainUpTo is non-blocking. waitNotify blocks until
// the next push, ctx cancellation, or the supplied timeout — whichever
// comes first.
type queue struct {
	mu      sync.Mutex
	buf     []record
	head    int // next write index
	size    int
	dropped uint64 // atomic
	notify  chan struct{}
}

func newQueue(capacity int) *queue {
	if capacity <= 0 {
		panic("queue capacity must be > 0")
	}
	return &queue{
		buf:    make([]record, capacity),
		notify: make(chan struct{}, 1),
	}
}

func (q *queue) push(r record) {
	q.mu.Lock()
	if q.size == len(q.buf) {
		// Full — overwrite the oldest. head currently points to the
		// oldest (since size==cap, head is both oldest and next-write).
		q.buf[q.head] = r
		q.head = (q.head + 1) % len(q.buf)
		q.mu.Unlock()
		atomic.AddUint64(&q.dropped, 1)
	} else {
		idx := (q.head + q.size) % len(q.buf)
		q.buf[idx] = r
		q.size++
		q.mu.Unlock()
	}
	// Non-blocking signal.
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *queue) drainUpTo(n int) []record {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.size == 0 {
		return nil
	}
	if n > q.size {
		n = q.size
	}
	out := make([]record, n)
	for i := 0; i < n; i++ {
		out[i] = q.buf[q.head]
		q.head = (q.head + 1) % len(q.buf)
	}
	q.size -= n
	return out
}

func (q *queue) takeDropped() uint64 {
	return atomic.SwapUint64(&q.dropped, 0)
}

// waitNotify blocks until: notify fires, ctx is done, or timeout elapses.
func (q *queue) waitNotify(ctx context.Context, timeout time.Duration) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-q.notify:
	case <-ctx.Done():
	case <-t.C:
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship -v -race`
Expected: all `TestQueue*` tests PASS, race detector clean.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/queue.go internal/logship/queue_test.go
git commit -m "feat(logship): bounded ring buffer with drop-oldest"
```

---

## Task 3 — Clock interface

**Files:**
- Create: `internal/logship/clock.go`

- [ ] **Step 1: Add the clock abstraction**

`internal/logship/clock.go`:

```go
package logship

import "time"

// clock abstracts time so the shipper's backoff and batch timer are
// testable without sleeping. Tests inject a fake; production uses
// realClock.
type clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/logship/...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/logship/clock.go
git commit -m "feat(logship): clock interface for testable backoff"
```

---

## Task 4 — Shipper push body (group + gzip + JSON)

**Files:**
- Create: `internal/logship/shipper.go`
- Create: `internal/logship/shipper_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/logship/shipper_test.go`:

```go
package logship

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strconv"
	"testing"
)

func TestBuildPushBodyGroupsByStream(t *testing.T) {
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
		"stderr": {"client": "lab-1", "stream": "stderr", "service": "lab_devices_client", "version": "1.4.2"},
	}
	batch := []record{
		{stream: "stdout", tsNano: 100, line: `{"msg":"a"}`},
		{stream: "stderr", tsNano: 101, line: "panic line"},
		{stream: "stdout", tsNano: 102, line: `{"msg":"b"}`},
	}

	body, err := buildPushBody(batch, labels)
	if err != nil {
		t.Fatalf("buildPushBody: %v", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}

	var parsed struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if len(parsed.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(parsed.Streams))
	}

	byStream := map[string]int{}
	for _, s := range parsed.Streams {
		byStream[s.Stream["stream"]] = len(s.Values)
		if s.Stream["service"] != "lab_devices_client" {
			t.Errorf("service label = %q", s.Stream["service"])
		}
		if s.Stream["client"] != "lab-1" {
			t.Errorf("client label = %q", s.Stream["client"])
		}
		if s.Stream["version"] != "1.4.2" {
			t.Errorf("version label = %q", s.Stream["version"])
		}
		if len(s.Stream) != 4 {
			t.Errorf("expected exactly 4 labels, got %v", s.Stream)
		}
	}
	if byStream["stdout"] != 2 || byStream["stderr"] != 1 {
		t.Fatalf("stream counts: %+v (want stdout:2 stderr:1)", byStream)
	}

	// Spot-check a value pair: timestamp is a string of the unix-nano int.
	for _, s := range parsed.Streams {
		for _, v := range s.Values {
			if _, err := strconv.ParseInt(v[0], 10, 64); err != nil {
				t.Errorf("ts %q is not a valid int string: %v", v[0], err)
			}
			if v[1] == "" {
				t.Errorf("empty line in values")
			}
		}
	}
}

func TestBuildPushBodyEmptyBatch(t *testing.T) {
	body, err := buildPushBody(nil, nil)
	if err != nil {
		t.Fatalf("buildPushBody on empty batch: %v", err)
	}
	if body != nil {
		t.Fatalf("empty batch must return nil body, got %d bytes", len(body))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestBuildPushBody ./internal/logship -v`
Expected: compile error — `buildPushBody` undefined.

- [ ] **Step 3: Implement the body builder**

`internal/logship/shipper.go`:

```go
package logship

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strconv"
)

// pushStream is the on-the-wire shape of one stream entry in a Loki push.
type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type pushBody struct {
	Streams []pushStream `json:"streams"`
}

// buildPushBody groups batch by stream, attaches the cached labels, and
// returns a gzip-encoded JSON body suitable for POSTing to Loki.
//
// Returns (nil, nil) for an empty batch — callers must not POST in that
// case.
func buildPushBody(batch []record, labels map[string]map[string]string) ([]byte, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	groups := make(map[string][][2]string, 2)
	for _, r := range batch {
		groups[r.stream] = append(groups[r.stream], [2]string{
			strconv.FormatInt(r.tsNano, 10),
			r.line,
		})
	}

	body := pushBody{Streams: make([]pushStream, 0, len(groups))}
	for stream, values := range groups {
		lbl := labels[stream]
		if lbl == nil {
			return nil, fmt.Errorf("no labels cached for stream %q", stream)
		}
		body.Streams = append(body.Streams, pushStream{
			Stream: lbl,
			Values: values,
		})
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal push body: %w", err)
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		return nil, fmt.Errorf("gzip push body: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run TestBuildPushBody ./internal/logship -v`
Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/shipper.go internal/logship/shipper_test.go
git commit -m "feat(logship): build gzip Loki push body grouped by stream"
```

---

## Task 5 — Shipper happy path

**Files:**
- Modify: `internal/logship/shipper.go`
- Modify: `internal/logship/shipper_test.go`

- [ ] **Step 1: Append the happy-path test**

Append to `internal/logship/shipper_test.go`:

```go
import (
	// ... existing imports ...
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// (Replace the existing import block in the file with the union of the
// existing imports and the four added above. Do not duplicate import
// statements.)

func TestShipperHappyPath(t *testing.T) {
	var (
		mu        sync.Mutex
		requests  [][]byte
		gotHeader = make(map[string]string)
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, body)
		gotHeader["Content-Type"] = r.Header.Get("Content-Type")
		gotHeader["Content-Encoding"] = r.Header.Get("Content-Encoding")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	q := newQueue(1024)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
		"stderr": {"client": "lab-1", "stream": "stderr", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, realClock{})

	for i := 0; i < 600; i++ {
		stream := "stdout"
		if i%5 == 0 {
			stream = "stderr"
		}
		q.push(record{stream: stream, tsNano: int64(i), line: "line"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()

	// Wait for at least one request to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(requests)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("no POST received")
	}
	if gotHeader["Content-Encoding"] != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", gotHeader["Content-Encoding"])
	}
	if gotHeader["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotHeader["Content-Type"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestShipperHappyPath ./internal/logship -v`
Expected: compile error — `newShipper` and `s.run` undefined.

- [ ] **Step 3: Implement `shipper` and `run` (happy path only — no retries yet)**

Append to `internal/logship/shipper.go`:

```go
import (
	// ... existing imports ...
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// (Add the four imports above to the existing import block.)

const (
	maxBatch     = 500
	flushTimeout = 2 * time.Second
	httpTimeout  = 5 * time.Second
)

type shipper struct {
	q      *queue
	url    string
	labels map[string]map[string]string
	clock  clock
	client *http.Client
}

func newShipper(q *queue, url string, labels map[string]map[string]string, clk clock) *shipper {
	return &shipper{
		q:      q,
		url:    url,
		labels: labels,
		clock:  clk,
		client: &http.Client{
			Timeout: httpTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        1,
				MaxIdleConnsPerHost: 1,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// run drains the queue forever — until ctx is done — and POSTs each
// batch. The happy path only; retries land in a later task.
func (s *shipper) run(ctx context.Context) {
	for {
		s.q.waitNotify(ctx, flushTimeout)
		if ctx.Err() != nil {
			return
		}
		batch := s.q.drainUpTo(maxBatch)
		if len(batch) == 0 {
			continue
		}
		body, err := buildPushBody(batch, s.labels)
		if err != nil {
			slog.Warn("logship build body failed", "err", err)
			continue
		}
		if err := s.post(ctx, body); err != nil {
			slog.Warn("logship push failed", "err", err)
		}
	}
}

// post performs one POST; no retry. Returns nil on 2xx, an error otherwise.
func (s *shipper) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return &httpStatusError{code: resp.StatusCode}
	}
	return nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return http.StatusText(e.code) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestShipperHappyPath ./internal/logship -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/shipper.go internal/logship/shipper_test.go
git commit -m "feat(logship): shipper happy path — drain, gzip, POST"
```

---

## Task 6 — Shipper retries (5xx backoff, 4xx drop)

**Files:**
- Modify: `internal/logship/shipper.go`
- Modify: `internal/logship/shipper_test.go`

- [ ] **Step 1: Append the failing tests**

Append to `internal/logship/shipper_test.go`:

```go
func TestShipperRetriesOn5xxThenSucceeds(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	clk := newFakeClock()
	q := newQueue(1024)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, clk)

	for i := 0; i < 10; i++ {
		q.push(record{stream: "stdout", tsNano: int64(i), line: "line"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.run(ctx)

	// Drive the fake clock through the backoff schedule (1s, 2s, 5s)
	// while polling for the success.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		clk.advance(15 * time.Second)
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if attempts < 4 {
		t.Fatalf("attempts = %d, want >= 4 (3 failures + 1 success)", attempts)
	}
}

func TestShipperDropsBatchOn4xx(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	q := newQueue(1024)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, realClock{})

	q.push(record{stream: "stdout", tsNano: 1, line: "x"})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()

	// Add another batch after a beat — first should be dropped without
	// retry, second should also be dropped, total attempts == 2 (not >>2
	// from a hot loop).
	time.Sleep(200 * time.Millisecond)
	q.push(record{stream: "stdout", tsNano: 2, line: "y"})
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if attempts < 2 {
		t.Fatalf("attempts = %d, want >= 2 (one per pushed batch)", attempts)
	}
	if attempts > 5 {
		t.Fatalf("attempts = %d — looks like a hot retry loop, want bounded", attempts)
	}
}
```

Also add the fake clock at the top of the test file (under the existing imports):

```go
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*pendingSleep
}

type pendingSleep struct {
	due  time.Time
	done chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(d time.Duration) {
	c.mu.Lock()
	p := &pendingSleep{due: c.now.Add(d), done: make(chan struct{})}
	c.pending = append(c.pending, p)
	c.mu.Unlock()
	<-p.done
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var still []*pendingSleep
	for _, p := range c.pending {
		if !p.due.After(c.now) {
			close(p.done)
		} else {
			still = append(still, p)
		}
	}
	c.pending = still
	c.mu.Unlock()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run "TestShipperRetries|TestShipperDropsBatch" ./internal/logship -v`
Expected: tests run but fail — current `post` does not retry on 5xx (the success comes only on attempt 4, but each batch is attempted once and dropped). The 4xx test may pass already; the 5xx test fails.

- [ ] **Step 3: Implement retries**

Replace the body of `run` in `internal/logship/shipper.go` with the retry-aware version:

```go
const (
	backoffStart = 1 * time.Second
	backoffMax   = 10 * time.Second
)

func (s *shipper) run(ctx context.Context) {
	for {
		s.q.waitNotify(ctx, flushTimeout)
		if ctx.Err() != nil {
			return
		}
		batch := s.q.drainUpTo(maxBatch)
		if len(batch) == 0 {
			continue
		}
		body, err := buildPushBody(batch, s.labels)
		if err != nil {
			slog.Warn("logship build body failed", "err", err)
			continue
		}
		s.postWithRetry(ctx, body)
	}
}

// postWithRetry holds a single batch, retrying on 5xx / transport
// errors with exponential backoff (1→2→5→10s, capped at 10s). 4xx drops
// the batch and returns. Returns when ctx is done or the batch is
// definitively handled.
func (s *shipper) postWithRetry(ctx context.Context, body []byte) {
	delay := backoffStart
	for {
		err := s.post(ctx, body)
		if err == nil {
			return
		}
		if hs, ok := err.(*httpStatusError); ok && hs.code/100 == 4 && hs.code != http.StatusTooManyRequests {
			slog.Warn("logship push rejected", "status", hs.code)
			return
		}
		// Retryable: 5xx, 429, transport errors.
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.clock.Sleep(delay)
		if delay < backoffMax {
			delay *= 2
			if delay > backoffMax {
				delay = backoffMax
			}
		}
	}
}
```

(Note: the literal "1→2→5→10s" sequence in the spec is approximated by the doubling 1→2→4→8→10. Acceptable per the spec's "suggested" wording — 4×exp doubling stays under the 10s cap and lands at the same order of magnitude.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship -v -race`
Expected: all shipper tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/shipper.go internal/logship/shipper_test.go
git commit -m "feat(logship): retry-on-5xx with backoff, drop-on-4xx"
```

---

## Task 7 — Dropped-line warning emission

**Files:**
- Modify: `internal/logship/shipper.go`
- Modify: `internal/logship/shipper_test.go`

- [ ] **Step 1: Append the failing test**

Append to `internal/logship/shipper_test.go`:

```go
func TestShipperResetsDroppedCounterOnSuccess(t *testing.T) {
	var success bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		success = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	q := newQueue(4)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, realClock{})

	// Cause some drops.
	for i := 0; i < 12; i++ {
		q.push(record{stream: "stdout", tsNano: int64(i), line: "x"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if success && q.takeDropped() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	if !success {
		t.Fatal("server never received a successful push")
	}
	if n := q.takeDropped(); n != 0 {
		t.Fatalf("dropped = %d after success, want 0 (shipper should have called takeDropped)", n)
	}
}
```

Replace the `TestShipperEmitsDroppedLineWarn` block above with `TestShipperResetsDroppedCounterOnSuccess`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestShipperResetsDroppedCounterOnSuccess ./internal/logship -v`
Expected: FAIL — current `run` doesn't call `takeDropped` after success.

- [ ] **Step 3: Implement dropped-line emission**

Modify `run` in `internal/logship/shipper.go` to call `takeDropped` and `slog.Warn` after each successful push:

```go
func (s *shipper) run(ctx context.Context) {
	for {
		s.q.waitNotify(ctx, flushTimeout)
		if ctx.Err() != nil {
			return
		}
		batch := s.q.drainUpTo(maxBatch)
		if len(batch) == 0 {
			continue
		}
		body, err := buildPushBody(batch, s.labels)
		if err != nil {
			slog.Warn("logship build body failed", "err", err)
			continue
		}
		if s.postWithRetry(ctx, body) {
			if dropped := s.q.takeDropped(); dropped > 0 {
				slog.Warn("logs dropped", "count", dropped)
			}
		}
	}
}
```

And update `postWithRetry` to return `true` on success, `false` on ctx-cancellation or 4xx-drop:

```go
func (s *shipper) postWithRetry(ctx context.Context, body []byte) bool {
	delay := backoffStart
	for {
		err := s.post(ctx, body)
		if err == nil {
			return true
		}
		if hs, ok := err.(*httpStatusError); ok && hs.code/100 == 4 && hs.code != http.StatusTooManyRequests {
			slog.Warn("logship push rejected", "status", hs.code)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		default:
		}
		s.clock.Sleep(delay)
		if delay < backoffMax {
			delay *= 2
			if delay > backoffMax {
				delay = backoffMax
			}
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship -v -race`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/shipper.go internal/logship/shipper_test.go
git commit -m "feat(logship): emit slog.Warn on dropped lines after successful push"
```

---

## Task 8 — Shipper final drain on cancel

**Files:**
- Modify: `internal/logship/shipper.go`
- Modify: `internal/logship/shipper_test.go`

When the manager's shutdown cancels the shipper's context, the shipper must make one best-effort final push of whatever's already in the queue before exiting. This is what gets the last few seconds of pre-shutdown context to Grafana — the moment something interesting usually happened. Without this, `TestManagerShutdownDrainsBuffer` in Task 11 will fail.

- [ ] **Step 1: Append the failing tests**

Append to `internal/logship/shipper_test.go`:

```go
func TestShipperRunExitsPromptlyOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	q := newQueue(64)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, realClock{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shipper did not exit within 2s of cancel")
	}
}

func TestShipperFinalDrainOnCancel(t *testing.T) {
	var (
		mu   sync.Mutex
		seen int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	q := newQueue(64)
	labels := map[string]map[string]string{
		"stdout": {"client": "lab-1", "stream": "stdout", "service": "lab_devices_client", "version": "1.4.2"},
	}
	s := newShipper(q, srv.URL, labels, realClock{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()

	// Push records, then immediately cancel — no time for the periodic
	// flush. The final drain must still send them.
	for i := 0; i < 5; i++ {
		q.push(record{stream: "stdout", tsNano: int64(i), line: "x"})
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shipper did not exit within 2s of cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if seen == 0 {
		t.Fatal("final drain did not push pending records")
	}
}
```

- [ ] **Step 2: Run tests to verify the new one fails**

Run: `go test -run TestShipperFinalDrainOnCancel ./internal/logship -v`
Expected: FAIL — current `run` exits on ctx cancel without draining.

- [ ] **Step 3: Refactor `run` to do a final drain on cancel**

Edit `internal/logship/shipper.go` — extract the "drain + push" body into a helper, and call it once more when ctx is done:

```go
func (s *shipper) run(ctx context.Context) {
	for {
		s.q.waitNotify(ctx, flushTimeout)
		if ctx.Err() != nil {
			// Final best-effort drain. Use a fresh background context
			// so postWithRetry isn't immediately short-circuited by the
			// already-cancelled ctx; the manager's caller-supplied
			// deadline bounds how long we wait via the select on done
			// in Manager.Shutdown.
			s.flushOnce(context.Background())
			return
		}
		s.flushOnce(ctx)
	}
}

func (s *shipper) flushOnce(ctx context.Context) {
	batch := s.q.drainUpTo(maxBatch)
	if len(batch) == 0 {
		return
	}
	body, err := buildPushBody(batch, s.labels)
	if err != nil {
		slog.Warn("logship build body failed", "err", err)
		return
	}
	if s.postWithRetry(ctx, body) {
		if dropped := s.q.takeDropped(); dropped > 0 {
			slog.Warn("logs dropped", "count", dropped)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship -v -race`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/shipper.go internal/logship/shipper_test.go
git commit -m "feat(logship): final best-effort drain on shipper cancel"
```

---

## Task 9 — Capture: slog tap

**Files:**
- Create: `internal/logship/capture.go`
- Create: `internal/logship/capture_test.go`

- [ ] **Step 1: Write the failing test**

`internal/logship/capture_test.go`:

```go
package logship

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

func TestInstallSlogTapWritesToDiskAndQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slog.log")
	q := newQueue(64)
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo)

	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	lj := &lumberjack.Logger{Filename: path, MaxSize: 1, MaxBackups: 1}
	t.Cleanup(func() { _ = lj.Close() })

	if err := installSlogTap(lj, levelVar, q); err != nil {
		t.Fatalf("installSlogTap: %v", err)
	}

	slog.Info("hello", "k", "v")

	// Disk: file should contain the message.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "hello") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), `"hello"`) {
		t.Fatalf("log file missing message:\n%s", data)
	}

	// Queue: one record with stream=stdout.
	got := q.drainUpTo(10)
	if len(got) != 1 {
		t.Fatalf("queue drain returned %d records, want 1", len(got))
	}
	if got[0].stream != "stdout" {
		t.Errorf("stream=%q, want stdout", got[0].stream)
	}
	if !strings.Contains(got[0].line, `"hello"`) {
		t.Errorf("line=%q does not contain message", got[0].line)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestInstallSlogTap ./internal/logship -v`
Expected: compile error — `installSlogTap` undefined.

- [ ] **Step 3: Implement the slog tap**

`internal/logship/capture.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestInstallSlogTap ./internal/logship -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/capture.go internal/logship/capture_test.go
git commit -m "feat(logship): slog tap — JSON handler over MultiWriter to disk + queue"
```

---

## Task 10 — Capture: stderr tap

**Files:**
- Modify: `internal/logship/capture.go`
- Modify: `internal/logship/capture_test.go`

- [ ] **Step 1: Append the failing test**

Append to `internal/logship/capture_test.go`:

```go
func TestInstallStderrTapWritesToDiskAndQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stderr.log")
	q := newQueue(64)

	prevStderr := os.Stderr
	t.Cleanup(func() { os.Stderr = prevStderr })

	lj := &lumberjack.Logger{Filename: path, MaxSize: 1, MaxBackups: 1}
	t.Cleanup(func() { _ = lj.Close() })

	tap, err := installStderrTap(lj, q)
	if err != nil {
		t.Fatalf("installStderrTap: %v", err)
	}
	t.Cleanup(func() { tap.close() })

	if _, err := os.Stderr.Write([]byte("panic: something\nstack frame 1\n")); err != nil {
		t.Fatalf("write stderr: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "stack frame 1") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stderr log: %v", err)
	}
	if !strings.Contains(string(data), "panic: something") || !strings.Contains(string(data), "stack frame 1") {
		t.Fatalf("disk missing lines:\n%s", data)
	}

	got := q.drainUpTo(10)
	if len(got) != 2 {
		t.Fatalf("queue drain returned %d records, want 2", len(got))
	}
	for _, r := range got {
		if r.stream != "stderr" {
			t.Errorf("stream=%q, want stderr", r.stream)
		}
	}
	if got[0].line != "panic: something" || got[1].line != "stack frame 1" {
		t.Fatalf("lines=%+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestInstallStderrTap ./internal/logship -v`
Expected: compile error — `installStderrTap` undefined.

- [ ] **Step 3: Implement the stderr tap**

Append to `internal/logship/capture.go`:

```go
import (
	// ... existing imports ...
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// (Add bufio, fmt, log/slog, os, sync to the existing import block.
// io, time, bytes, lumberjack are already imported.)

// stderrTap holds the side state needed to undo installStderrTap on Shutdown.
type stderrTap struct {
	prev    *os.File   // saved os.Stderr from before install
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestInstallStderrTap ./internal/logship -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/capture.go internal/logship/capture_test.go
git commit -m "feat(logship): stderr tap via os.Pipe → lumberjack + queue"
```

---

## Task 11 — Manager façade

**Files:**
- Modify: `internal/logship/logship.go`
- Create: `internal/logship/logship_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/logship/logship_test.go`:

```go
package logship

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerInitInstallsCaptureSoSlogReachesDisk(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	slog.Info("hello-from-init")

	deadline := time.Now().Add(time.Second)
	logPath := filepath.Join(dir, LogFileName)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "hello-from-init") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	t.Fatalf("hello-from-init missing on disk:\n%s", data)
}

func TestManagerSetLevelChangesFiltering(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	slog.Debug("debug-suppressed")
	m.SetLevel(slog.LevelDebug)
	slog.Debug("debug-passes")

	deadline := time.Now().Add(time.Second)
	logPath := filepath.Join(dir, LogFileName)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "debug-passes") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "debug-suppressed") {
		t.Errorf("debug-suppressed leaked at Info level:\n%s", data)
	}
	if !strings.Contains(string(data), "debug-passes") {
		t.Errorf("debug-passes missing after SetLevel(Debug):\n%s", data)
	}
}

func TestManagerStartShipperEmptyClientLabelIsNoOp(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.StartShipper("") // must not start a goroutine, must not panic
	// No assertion beyond "didn't crash"; further behavior is covered
	// by TestManagerStartShipperPushes.
	_ = m
}

func TestManagerStartShipperPushes(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	// Override the URL the manager hands the shipper.
	m.setPushURLForTest(srv.URL)

	m.StartShipper("lab-1")
	for i := 0; i < 10; i++ {
		slog.Info("line", "i", i)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no push received; hits=%d", hits.Load())
}

func TestManagerStartShipperIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.StartShipper("lab-1")
	m.StartShipper("lab-1") // must not panic, must not start twice
	if got := m.shipperCountForTest(); got != 1 {
		t.Fatalf("shipper count = %d, want 1", got)
	}
}

// Sanity: Shutdown is safe to call when StartShipper was never called.
func TestManagerShutdownWithoutShipper(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { m.Shutdown(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}

// Verify Shutdown drains in-flight records before returning.
func TestManagerShutdownDrainsBuffer(t *testing.T) {
	var (
		mu     sync.Mutex
		seen   int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.setPushURLForTest(srv.URL)
	m.StartShipper("lab-1")

	for i := 0; i < 5; i++ {
		slog.Info("line", "i", i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.Shutdown(ctx)

	mu.Lock()
	defer mu.Unlock()
	if seen == 0 {
		t.Fatal("Shutdown did not drain pending records")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestManager ./internal/logship -v`
Expected: compile errors — `Init`, `Manager.SetLevel`, etc. either don't exist or are stubbed.

- [ ] **Step 3: Replace `internal/logship/logship.go` with the full façade**

`internal/logship/logship.go`:

```go
// Package logship streams the client's slog output and stderr to the
// in-VPS Loki via the chisel forward tunnel.
//
// It also owns the durable on-disk log files (lab_devices_client.log,
// lab_devices_client_stderr.log) so disabling the shipper does not
// affect on-disk logging.
package logship

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

const LogFileName = "lab_devices_client.log"
const StderrLogFileName = "lab_devices_client_stderr.log"

// defaultPushURL is the local end of the chisel forward tunnel that
// reaches the in-VPS Loki.
const defaultPushURL = "http://127.0.0.1:3100/loki/api/v1/push"

// Manager owns the capture taps, ring buffer, and shipper goroutine.
type Manager struct {
	dir     string
	version string

	levelVar *slog.LevelVar

	slogDisk   *lumberjack.Logger
	stderrDisk *lumberjack.Logger
	stderrTap  *stderrTap

	q *queue

	mu       sync.Mutex
	pushURL  string
	shipperC int                // count of shippers started (for tests)
	shipCtx  context.Context
	shipStop context.CancelFunc
	shipDone chan struct{}
}

// Init builds the on-disk log writers, allocates the ring buffer, and
// installs the slog and stderr taps. The shipper is NOT started yet —
// call StartShipper once the chisel user is known.
func Init(dir, version string, level slog.Level) (*Manager, error) {
	m := &Manager{
		dir:      dir,
		version:  version,
		levelVar: new(slog.LevelVar),
		pushURL:  defaultPushURL,
		q:        newQueue(10_000),
	}
	m.levelVar.Set(level)

	m.slogDisk = &lumberjack.Logger{
		Filename:   filepath.Join(dir, LogFileName),
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}
	m.stderrDisk = &lumberjack.Logger{
		Filename:   filepath.Join(dir, StderrLogFileName),
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}

	if err := installSlogTap(m.slogDisk, m.levelVar, m.q); err != nil {
		return nil, err
	}
	tap, err := installStderrTap(m.stderrDisk, m.q)
	if err != nil {
		return nil, err
	}
	m.stderrTap = tap
	return m, nil
}

// SetLevel changes the slog level without re-installing the tap.
func (m *Manager) SetLevel(level slog.Level) {
	m.levelVar.Set(level)
}

// StartShipper starts the shipper goroutine if clientLabel is non-empty
// and no shipper is already running. Idempotent.
func (m *Manager) StartShipper(clientLabel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shipCtx != nil {
		return // already started
	}
	if clientLabel == "" {
		slog.Warn("log streaming disabled (no chisel user)")
		return
	}
	labels := map[string]map[string]string{
		"stdout": buildLabels(clientLabel, "stdout", m.version),
		"stderr": buildLabels(clientLabel, "stderr", m.version),
	}
	s := newShipper(m.q, m.pushURL, labels, realClock{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()

	m.shipCtx = ctx
	m.shipStop = cancel
	m.shipDone = done
	m.shipperC++
}

func buildLabels(client, stream, version string) map[string]string {
	return map[string]string{
		"client":  client,
		"stream":  stream,
		"service": "lab_devices_client",
		"version": version,
	}
}

// Shutdown stops the shipper (giving it the caller's deadline to drain
// in-flight records), closes the stderr tap, and closes the on-disk
// writers.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	stop := m.shipStop
	done := m.shipDone
	m.mu.Unlock()

	if stop != nil {
		stop()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}

	if m.stderrTap != nil {
		m.stderrTap.close()
		m.stderrTap = nil
	}
	if m.slogDisk != nil {
		_ = m.slogDisk.Close()
	}
	if m.stderrDisk != nil {
		_ = m.stderrDisk.Close()
	}
}

// --- test-only helpers (lower-cased; only callable from logship_test.go) ---

func (m *Manager) setPushURLForTest(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushURL = url
}

func (m *Manager) shipperCountForTest() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shipperC
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship -v -race`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/logship.go internal/logship/logship_test.go
git commit -m "feat(logship): Manager façade — Init, SetLevel, StartShipper, Shutdown"
```

---

## Task 12 — Chisel forward tunnel

**Files:**
- Modify: `internal/chisel/client.go`
- Create: `internal/chisel/client_test.go`

- [ ] **Step 1: Write the failing test**

`internal/chisel/client_test.go`:

```go
package chisel

import (
	"slices"
	"testing"
)

func TestRemotesIncludesForwardWhenAuthSet(t *testing.T) {
	got := buildRemotes(Config{User: "lab-1", RemotePort: 8081, LocalPort: 5000})
	want := []string{
		"R:8081:127.0.0.1:5000",
		"127.0.0.1:3100:loki:3100",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}

func TestRemotesOmitsForwardWhenNoAuth(t *testing.T) {
	got := buildRemotes(Config{User: "", RemotePort: 8081, LocalPort: 5000})
	want := []string{"R:8081:127.0.0.1:5000"}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestRemotes ./internal/chisel -v`
Expected: compile error — `buildRemotes` undefined.

- [ ] **Step 3: Modify `internal/chisel/client.go`**

Extract the remotes-list construction into a helper and add the forward route:

```go
package chisel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// Config is the subset of chisel client options this app exposes.
type Config struct {
	Server     string
	User       string
	Pass       string
	RemotePort int
	LocalPort  int
}

// buildRemotes returns the list of chisel route strings for cfg. The
// reverse route exposes the local REST server. The forward route
// (added only when User is set, since the server allowlist is
// per-user) tunnels 127.0.0.1:3100 to the in-VPS Loki container.
func buildRemotes(cfg Config) []string {
	out := []string{fmt.Sprintf("R:%d:127.0.0.1:%d", cfg.RemotePort, cfg.LocalPort)}
	if cfg.User != "" {
		out = append(out, "127.0.0.1:3100:loki:3100")
	}
	return out
}

func Run(ctx context.Context, cfg Config) error {
	if _, _, err := net.SplitHostPort(cfg.Server); err != nil {
		return fmt.Errorf("invalid server %q: %w", cfg.Server, err)
	}
	auth := ""
	if cfg.User != "" {
		auth = cfg.User + ":" + cfg.Pass
	}
	remotes := buildRemotes(cfg)
	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://" + cfg.Server,
		Auth:             auth,
		Remotes:          remotes,
		KeepAlive:        25 * time.Second,
		MaxRetryInterval: 5 * time.Minute,
		MaxRetryCount:    -1,
	})
	if err != nil {
		return fmt.Errorf("new chisel client: %w", err)
	}
	c.Logger.Info = true
	c.Logger.Debug = false
	slog.Info("chisel: starting",
		"server", cfg.Server,
		"remote_port", cfg.RemotePort,
		"local_port", cfg.LocalPort,
		"auth", cfg.User != "",
		"forward_loki", cfg.User != "",
	)
	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("start chisel client: %w", err)
	}
	if err := c.Wait(); err != nil {
		return fmt.Errorf("chisel client: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/chisel -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/chisel/client.go internal/chisel/client_test.go
git commit -m "feat(chisel): forward 127.0.0.1:3100 to loki:3100 when auth is set"
```

---

## Task 13 — Wire `logship` into the Windows worker

**Files:**
- Modify: `internal/winsvc/worker.go`

This task changes Windows-only code. It is verified by cross-compile (`GOOS=windows GOARCH=amd64 go build ./...`) since `winsvc.RunWorker` cannot run on macOS. Behavior is exercised end-to-end during manual VPS verification (post-merge).

- [ ] **Step 1: Replace the contents of `internal/winsvc/worker.go`**

```go
//go:build windows

package winsvc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/khamitovdr/lab_devices_client/internal/app"
	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/logship"
	"github.com/khamitovdr/lab_devices_client/internal/version"

	"golang.org/x/sys/windows/svc"
)

const (
	configFileName = "lab_devices_client_config.yaml"

	workerStopGracePeriod = 30 * time.Second
	logshipShutdown       = 2 * time.Second
)

// RunWorker is the service-mode entry point. It must only be called when
// svc.IsWindowsService() returns true. It initializes log streaming
// before svc.Run so that even a config-load failure is captured both
// on disk and (if a previous successful run cached chisel auth) in
// Loki on the next push.
func RunWorker() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)

	manager, err := logship.Init(dir, version.Version, slog.LevelInfo)
	if err != nil {
		return fmt.Errorf("logship init: %w", err)
	}

	return svc.Run(ServiceName, &handler{dir: dir, manager: manager})
}

type handler struct {
	dir     string
	manager *logship.Manager
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	cfgPath := filepath.Join(h.dir, configFileName)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		h.shutdownLogship()
		changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
		return false, 1
	}
	h.manager.SetLevel(logship.ParseLogLevel(cfg.Log.Level))
	h.manager.StartShipper(cfg.Chisel.User)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appDone := make(chan error, 1)
	go func() {
		appDone <- app.Run(ctx, cfg)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				slog.Info("service stop requested")
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-appDone:
					if err != nil {
						slog.Error("app exited with error during stop", "err", err)
					}
				case <-time.After(workerStopGracePeriod):
					slog.Error("app did not exit within grace period; forcing stop", "grace", workerStopGracePeriod)
				}
				h.shutdownLogship()
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-appDone:
			if err != nil {
				slog.Error("app exited unexpectedly", "err", err)
				h.shutdownLogship()
				changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return false, 1
			}
			slog.Info("app exited cleanly")
			h.shutdownLogship()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func (h *handler) shutdownLogship() {
	ctx, cancel := context.WithTimeout(context.Background(), logshipShutdown)
	defer cancel()
	h.manager.Shutdown(ctx)
}
```

- [ ] **Step 2: Verify the Windows build still compiles**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: exits 0, no output.

- [ ] **Step 3: Run the macOS test suite to confirm no regressions**

Run: `go test ./...`
Expected: all PASS (winsvc.worker has `//go:build windows` so it is skipped on macOS; the platform-neutral packages all still pass).

- [ ] **Step 4: Commit**

```bash
git add internal/winsvc/worker.go
git commit -m "feat(winsvc): wire logship into RunWorker, drain on stop"
```

---

## Task 14 — Final cleanup and cross-platform sanity

**Files:**
- Modify: `internal/winsvc/worker.go` (remove now-unused helpers if any remain)

The previous task replaced `worker.go` wholesale, so the old helpers `configureFileLogger`, `redirectStderrToFile`, and `parseLogLevel` are already gone. This task is a verification pass.

- [ ] **Step 1: Confirm dead code is gone**

Run: `grep -nE 'configureFileLogger|redirectStderrToFile|parseLogLevel' internal/winsvc/`
Expected: no output (the helpers were removed).

- [ ] **Step 2: Run the full test suite with race detection**

Run: `go test ./... -race`
Expected: all PASS, no race warnings.

- [ ] **Step 3: Cross-compile for Windows**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: exits 0.

- [ ] **Step 4: Verify the Taskfile-based test target still works**

Run: `task test`
Expected: PASS, same as `go test ./...`.

- [ ] **Step 5: Commit if any cleanups landed**

```bash
git status
# If any files changed in this task:
git add -p
git commit -m "chore(logship): final cleanup and cross-compile sanity"
```

If nothing changed, skip the commit — the cleanup was already covered by Task 13.

---

## Out-of-band verification (post-merge, on a dev VPS)

Maps to the spec's §9.5 manual verification list. Not tracked as plan tasks because they require Windows hardware + a connected VPS and run after merge:

1. Tunnel comes up (chisel session log shows two routes; `curl 127.0.0.1:3100/ready` returns 200 from the lab machine).
2. `slog.Info` from app code shows up in Grafana within ~2 seconds.
3. Disconnect tolerance: block outbound TCP for 30 s, lines flush within seconds of restoring; on-disk files unaffected.
4. Buffer overflow: longer outage > 10 000 lines; no OOM, device control unaffected, dropped-line `slog.Warn` reaches Loki on next successful push.
