# Lab Devices Client — Log Streaming to Loki

**Date:** 2026-04-28
**Status:** Approved (brainstorming complete; pending spec review before plan)
**Builds on:** [`2026-04-27-windows-service-design.md`](./2026-04-27-windows-service-design.md)
**Companion (server side):** `2026-04-28-chisel-client-logs-implementation.md` (in the `lab-bridge` repo) — that document is the contract; this spec describes how this client honors it.
**Target platform:** Windows (amd64); cross-compile and tests on macOS continue to work.

## 1. Purpose

When a lab is misbehaving today, the operator has to ask lab staff to ZIP and email two log files (`lab_devices_client.log`, `lab_devices_client_stderr.log`). The server side now hosts a Loki + Grafana stack reachable from the client only through the existing chisel session. This change makes the client stream its stdout and stderr through that tunnel so the operator can browser-tail and search them at `https://<vps-host>/grafana/`.

It is purely additive. The on-disk rotated log files remain the durable record. Loki is a queryable mirror.

## 2. Goals and non-goals

### Goals

- Ship every line written to `lab_devices_client.log` and `lab_devices_client_stderr.log` over the existing chisel tunnel to Loki, in near real-time.
- Survive transient network drops — buffer in memory, drop oldest on overflow, never block the device-control hot path.
- Self-identify each stream with stable labels (`client`, `stream`, `service`, `version`) so the operator can filter in Grafana.
- No new public network surface, no new server credentials. Reuse the chisel auth that's already provisioned per device.
- No new Go module dependencies. Stdlib only (`net/http`, `compress/gzip`, `encoding/json`, `bufio`).

### Non-goals

- Persistent on-disk buffering of unsent log lines. The rotated log files on the lab machine remain the durable record.
- Structured-log inference, metric extraction, PII scrubbing — line bodies are forwarded verbatim.
- Application-layer authentication of the push to Loki. Chisel's auth is the gate.
- A configuration kill switch. The shipper runs whenever the service runs; if an operator needs to stop ingest, they stop the service.
- Foreground (developer) mode shipping. Foreground writes JSON to a real terminal stdout that the developer is already watching, and stderr is not file-redirected there.

## 3. Run-mode scope

Log streaming is **service-mode-only**. `winsvc.RunWorker` initializes the new subsystem; foreground, admin-action, and panel modes are unchanged. Tests run on macOS and Windows alike.

## 4. Architecture overview

A new package, `internal/logship`, owns three things, all in-process:

```
                                  ┌──────────────┐
                                  │  lumberjack  │ ← lab_devices_client.log (durable)
                                  │  (rotated)   │
slog.Default ── JSON handler ─────┤              │
                                  │              │
                                  │  shipQueue   │ ← in-memory ring, 10 000 records, drop-oldest
                                  └──────────────┘
                                          │
                  ┌──────────────┐        │
os.Stderr ──▶ os.Pipe ──▶ stderr tap ─────┤      ┌──────────┐
                  │            │          │      │  shipper │ ──gzip JSON POST──▶ 127.0.0.1:3100
                  ▼            │          │      │  goroutine│   /loki/api/v1/push
       lab_devices_client_     │          │      └──────────┘
         stderr.log (durable)  │          │            ▲
                               └──────────┘            │
                                                  flush every 2 s
                                                  or every 500 records
```

- **Capture layer** (two adapters):
  - **slog tap** — replaces today's `winsvc.configureFileLogger`. Builds a JSON `slog.Handler` whose writer is `io.MultiWriter(lumberjackLogger, queueWriter{stream:"stdout"})`. Each line slog writes goes both to disk and to the queue.
  - **stderr tap** — replaces today's `winsvc.redirectStderrToFile`. Creates `os.Pipe()`, sets `os.Stderr` to the write-end, runs a reader goroutine that writes each line to a lumberjack-rotated `lab_devices_client_stderr.log` AND to the queue with `stream:"stderr"`.

  Both adapters always feed the queue. The shipper goroutine — which drains the queue — is what's started lazily once the chisel user is known (§5.4). Until then, records pile into the queue and drop-oldest harmlessly; the on-disk logs are still durable.
- **Queue** — bounded ring buffer holding up to 10 000 `(stream, ts_unix_nano, line)` records. Drop-oldest on overflow; a monotonic `dropped` counter increments per dropped line.
- **Shipper** — one goroutine. Drains up to 500 records or waits up to 2 s, groups by stream label, gzips one JSON push body, POSTs to `http://127.0.0.1:3100/loki/api/v1/push`. Backoff retry on 5xx / transport errors; drop-batch on 4xx. After every successful push, flushes any pending dropped-line warning via `slog.Warn`.

The chisel forward tunnel `127.0.0.1:3100:loki:3100` is added to the remotes slice in `internal/chisel.Run`. It is gated on `cfg.Chisel.User != ""` (the server allowlist is per-user; without auth the server won't grant the route).

## 5. Components

### 5.1 `internal/logship/queue.go`

```go
type record struct {
    stream string  // "stdout" | "stderr"
    tsNano int64
    line   string
}

type queue struct {
    mu      sync.Mutex
    buf     []record   // capacity 10 000
    head    int        // next write index
    size    int
    dropped uint64     // monotonic; reset by takeDropped()
    notify  chan struct{}  // signals shipper when size crosses batch threshold
}

func newQueue(capacity int) *queue
func (q *queue) push(r record)              // drop-oldest on full, increments dropped
func (q *queue) drainUpTo(n int) []record   // FIFO, non-blocking
func (q *queue) waitNotify(ctx context.Context, timeout time.Duration) // returns when notify, ctx, or timeout
func (q *queue) takeDropped() uint64        // atomic swap → 0
func (q *queue) close()
```

No dependencies on `net/http` or the shipper. Unit-testable on any platform.

### 5.2 `internal/logship/capture.go`

Two adapters:

- `installSlogTap(path string, levelVar *slog.LevelVar, q *queue) error`
  Replaces what `winsvc.configureFileLogger` does today. Builds a `slog.NewJSONHandler` whose options use the shared `levelVar` (so `Manager.SetLevel` works), and whose writer is `io.MultiWriter(lumberjackLogger, queueWriter{q, "stdout"})`. Sets it via `slog.SetDefault`. `queueWriter.Write` splits on `\n` (slog JSON handler ends each record with `\n`), pushing each non-empty line as `record{stream:"stdout", tsNano: time.Now().UnixNano(), line: <line>}`.
- `installStderrTap(path string, q *queue) error`
  Replaces `winsvc.redirectStderrToFile`. Creates `os.Pipe()`, assigns `os.Stderr = pipeWriter`. Spawns a reader goroutine that:
  - reads with `bufio.Scanner`, `Buffer` set to 1 MiB to accommodate Go panic stack dumps,
  - on each line, writes it (with appended `\n`) to a `lumberjack.Logger` opened against `path`,
  - calls `q.push(record{stream:"stderr", tsNano: time.Now().UnixNano(), line: <line>})`,
  - on a transient `Scanner.Err()` (e.g. line longer than the 1 MiB buffer), logs `slog.Warn("stderr scanner error", ...)` and re-creates the scanner against the same pipe so the goroutine never dies while the process holds the pipe writer. Exiting the goroutine while writers are active would deadlock the next stderr write once the pipe buffer fills.

  The original `os.Stderr` handle is preserved on the side and restored on `Shutdown`.

Stderr file rotation: today `redirectStderrToFile` opens `lab_devices_client_stderr.log` with `O_APPEND` and lets it grow unbounded. While we are rewiring this path we switch to `lumberjack.Logger` with the same policy as the main log (10 MiB / 3 backups). This is in-scope cleanup, not a separate workstream.

### 5.3 `internal/logship/shipper.go`

```go
type shipper struct {
    q       *queue
    client  *http.Client
    url     string                // "http://127.0.0.1:3100/loki/api/v1/push"
    labels  map[string]map[string]string  // stream -> labels (built once)
    clock   clock                 // injectable for tests
}

func (s *shipper) run(ctx context.Context)
```

Loop body:

1. `q.waitNotify(ctx, 2 * time.Second)` — wakes on size threshold, ctx done, or timeout.
2. `batch := q.drainUpTo(500)`. If empty, continue.
3. Build push body: group `batch` by `stream`, attach the cached label map per group, JSON-encode, gzip with `gzip.DefaultCompression`.
4. `postWithRetry(ctx, body)` — see §6.
5. After a successful push, `n := q.takeDropped(); if n > 0 { slog.Warn("logs dropped", "count", n) }`.

The HTTP client is shared, with `Timeout: 5 * time.Second` and a `Transport{ MaxIdleConns: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: 90 * time.Second }` to keep one warm connection.

### 5.4 `internal/logship/logship.go` (façade)

```go
type Manager struct { /* unexported */ }

func Init(dir, version string, level slog.Level) (*Manager, error)
func (m *Manager) SetLevel(level slog.Level)
func (m *Manager) StartShipper(clientLabel string)   // idempotent; no-op if already started or clientLabel == ""
func (m *Manager) Shutdown(ctx context.Context)
```

Lifecycle: `Init` is called once at the very start of `RunWorker`, before any other code that might log; `StartShipper` is called once after `config.Load` succeeds. Splitting it this way avoids the `os.Stderr` reassignment dance that an init-twice approach would need.

Behavior:
- `Init`:
  - Builds lumberjack writers for both files (10 MiB / 3 backups each).
  - Allocates the queue (capacity 10 000) and the cached label maps stub (with `client = ""` for now).
  - Installs `installSlogTap` and `installStderrTap` with the real queue. Records start accumulating immediately. If `StartShipper` is never called, queue fills, drops oldest — harmless because the on-disk logs are durable.
  - Stores a `slog.LevelVar` and uses it in the `slog.HandlerOptions` so `SetLevel` can change the level without re-installing the tap.
- `SetLevel(l)`:
  - `levelVar.Set(l)`. Cheap, race-free.
- `StartShipper(clientLabel)`:
  - If already started, no-op.
  - If `clientLabel == ""`, logs `slog.Warn("log streaming disabled (no chisel user)")` and returns. No goroutine.
  - Otherwise, builds the four cached labels (`client`, `stream`, `service`, `version`), starts the shipper goroutine under an internal context.
- `Shutdown(ctx)`:
  - Cancels the shipper's internal context (no-op if never started). The goroutine attempts one final push and exits.
  - Waits up to the caller-supplied context's deadline (worker passes 2 s) for the goroutine to exit.
  - Closes the queue, restores `os.Stderr` to the saved original, closes the lumberjack writers.

### 5.5 `internal/chisel/client.go` (modification)

Append one route to the existing `remotes` slice when `cfg.User != ""`:

```go
if cfg.User != "" {
    remotes = append(remotes, "127.0.0.1:3100:loki:3100")
}
```

No other chisel changes.

### 5.6 `internal/winsvc/worker.go` (modification)

Replace the existing two calls in `RunWorker`:

```go
configureFileLogger(filepath.Join(dir, logFileName), slog.LevelInfo)
redirectStderrToFile(filepath.Join(dir, "lab_devices_client_stderr.log"))
```

with `logship.Init(dir, version.Version, slog.LevelInfo)`. The returned `*Manager` is stored on the `handler` struct so `Execute` can use it.

In `handler.Execute`, after `config.Load(cfgPath)` succeeds:

```go
h.manager.SetLevel(parseLogLevel(cfg.Log.Level))
h.manager.StartShipper(cfg.Chisel.User)   // no-op if empty
```

The stop path calls `h.manager.Shutdown(ctxWith2sDeadline)` *after* app cancel + grace, before the SCM `Stopped` status is reported.

The two helper functions `configureFileLogger` and `redirectStderrToFile` and the `parseLogLevel` switch (still useful) move into `internal/logship` or a small shared helper file. The `lab_devices_client_stderr.log` filename constant moves alongside `logFileName`.

## 6. Data flow and labels

**Per-record path:**

```
slog.Info(...)         →  JSON handler  →  MultiWriter
                                              ├─ lumberjack(lab_devices_client.log)
                                              └─ queueWriter{stream:"stdout"}  →  q.push

panic / chisel.Logger  →  os.Stderr (=pipe)  →  reader goroutine
                                                 ├─ lumberjack(stderr.log)
                                                 └─ q.push{stream:"stderr"}
```

**Timestamp:** `time.Now().UnixNano()` taken at `q.push` time. The slog body's own embedded timestamp is left untouched — Loki does not parse it.

**Push body:**

```json
{
  "streams": [
    {
      "stream": {
        "client":  "<cfg.Chisel.User>",
        "stream":  "stdout",
        "service": "lab_devices_client",
        "version": "<version.Version>"
      },
      "values": [
        ["1714329600000000000", "<verbatim log line>"]
      ]
    }
  ]
}
```

A push contains at most two `streams[]` entries (one per source). Labels are built **once at `StartShipper`** and cached as two maps; the shipper never mutates them.

**Headers:** `Content-Type: application/json`, `Content-Encoding: gzip`. Connection: keep-alive (default in the shared `http.Client`).

**Required labels — and only these:**

| label | value | source |
|---|---|---|
| `client` | chisel auth username | `cfg.Chisel.User`, passed into `StartShipper` |
| `stream` | `"stdout"` or `"stderr"` | from queue record |
| `service` | `"lab_devices_client"` | hardcoded constant |
| `version` | client semver | `version.Version` (build-time `-ldflags -X`) |

No other labels. The server enforces a 15-label / 1024-char-value ceiling and will return 400 if violated; this is a defensive guard, not a normal path.

## 7. Error handling

### 7.1 Push outcomes

| Result | Action |
|---|---|
| 2xx (204 expected) | Mark batch sent. Reset retry delay to 1 s. |
| 5xx, 429, request timeout, dial / EOF / connection reset | Hold batch in shipper. Sleep current backoff (1 s → 2 s → 5 s → 10 s, capped at 10 s). Retry the same batch. Loop until success or `ctx.Done()`. |
| 4xx (other than 429) | Drop batch. `slog.Warn("logs push rejected", "status", code, "body", first200chars)`. Reset backoff to 1 s. Continue with next batch. |

While a batch is held in the shipper, fresh lines pile into the queue. If the queue overflows, oldest lines drop — that is the spec's intended behavior.

### 7.2 Dropped-line accounting (recursion-safe)

```
q.push detects buf full
   → ringbuf overwrites oldest, atomic.AddUint64(&q.dropped, 1)

shipper, after a successful push:
   n := q.takeDropped()             // atomic swap → 0
   if n > 0 {
       slog.Warn("logs dropped", "count", n)   // re-enters the tap
   }
```

The Warn re-enters via the slog tap → goes to disk AND back into the queue, so the operator sees it both on the lab machine and (eventually) in Grafana. This is bounded:

- It only fires *after* a successful push, i.e. when the queue is not currently overflowing.
- `takeDropped` zeroes the counter, so a single Warn covers an arbitrarily large drop count.
- If that Warn itself races and gets dropped, the next overflow event resurfaces it.

No special bypass path is needed.

### 7.3 Stderr line-too-long

`bufio.Scanner.Buffer` is set to 1 MiB. If a single stderr line exceeds that, `Scanner.Err()` returns `bufio.ErrTooLong`. The reader logs `slog.Warn("logship stderr scanner error (recreating)", "err", err)`, drops the offending line, recreates the scanner against the same pipe, and continues. The goroutine never exits while the process holds the pipe writer — exiting would deadlock subsequent stderr writes once the pipe buffer fills.

### 7.4 Cardinality 4xx

Production code cannot trigger this — the label map is built once at `Init` and never mutated. The 4xx branch above (drop batch, log Warn, reset backoff) handles it without hot-looping if it ever does.

### 7.5 Empty `cfg.Chisel.User`

If no chisel auth is configured, the chisel forward tunnel is not added (§5.5) and `StartShipper("")` is a no-op (§5.4). Capture taps still install so the on-disk logs continue to work; queue records pile up and drop-oldest harmlessly with no consumer. `slog.Warn("log streaming disabled (no chisel user)")` is emitted once.

### 7.6 Shutdown sequence (in `winsvc.handler.Execute`)

```
service stop received
   → cancel app ctx                           (existing)
   → wait up to 30 s for app to exit          (existing)
   → call shutdown(ctxWith2sDeadline)         (new)
        → close q.notify
        → shipper drains queue with one final push attempt
        → goroutine exits within 2 s
   → SCM Stopped status                       (existing)
```

## 8. Failure-mode summary

| Failure | Effect | Required behavior |
|---|---|---|
| Chisel session down | Forward tunnel dead, pushes fail | Buffer; backoff; do not block writer; on-disk log unaffected |
| Loki 5xx (overload) | Push rejected with retry-able error | Backoff and retry the same batch |
| Loki 4xx (cardinality, etc.) | Push rejected permanently | Log at WARN, drop the batch, do not retry |
| Lab-machine clock skew >7 days | Loki rejects with `reject_old_samples` | Treated as 4xx — log at WARN, drop, recover when clock corrects |
| Buffer overflow | New lines drop oldest | Atomic counter; surfaced via `slog.Warn` after next successful push |
| `client` label mismatched to chisel auth | Wrong panel attribution in Grafana | Out of scope; documented as an integrity issue, not a confidentiality one |
| `cfg.Chisel.User == ""` | No tunnel, no shipper | Capture taps still install, on-disk logging unchanged, one-shot Warn at startup |

## 9. Testing strategy

The whole `internal/logship` package is platform-neutral Go. Tests run on macOS and Windows alike, same as the rest of the project.

### 9.1 `internal/logship/queue_test.go`

- push/drain FIFO ordering
- drop-oldest on overflow; `takeDropped` returns and zeros the count
- concurrent producers + one consumer; race detector clean

### 9.2 `internal/logship/shipper_test.go`

Drives the shipper against `httptest.NewServer`. A small `clock` interface (with a fake implementation) replaces `time.Sleep` and `time.AfterFunc` so tests don't actually sleep seconds.

- happy path: 600 records flush as one POST when the count threshold trips; body decodes as `{streams:[{stdout},{stderr}]}` with the four required labels and nanosecond string timestamps
- 2-second time-based flush fires when batch < 500
- gzip on by default; the test server decodes it
- 5xx → backoff → recovery: server returns 503 three times then 204; same batch eventually lands; backoff schedule observed via fake clock
- 4xx → batch dropped, no retry, `slog.Warn` recorded
- dropped-line counter: overflow the queue, then unblock the server; assert the next successful push body contains the synthetic Warn line
- `Shutdown(ctx)` flushes pending batch within the 2 s window
- empty-user no-op: `Init` followed by `StartShipper("")` produces no goroutine; capture into the queue still works (drops oldest as designed); on-disk logs are written normally

### 9.3 `internal/logship/capture_test.go`

- slog tap: install with a tempdir lumberjack target + a fake queue, emit `slog.Info`, assert the line landed both on disk and in the queue with `stream:"stdout"`
- stderr tap: install with a tempdir target, write to `os.Stderr`, assert disk + queue with `stream:"stderr"`. `t.Cleanup` restores real `os.Stderr`.

### 9.4 `internal/chisel/client_test.go` patch

Existing tests already cover server validation. Add: when `User != ""` the constructed remotes slice contains `127.0.0.1:3100:loki:3100` *in addition to* the reverse route, and when `User == ""` it does not.

### 9.5 Manual verification (dev VPS)

Maps to the server-side spec's verification list (steps 1–4):

1. **Tunnel comes up.** Start the client. Chisel session log shows two routes (existing reverse + new `127.0.0.1:3100` forward). On the lab machine, `curl -i http://127.0.0.1:3100/ready` returns 200.
2. **Push lands.** `slog.Info` from app code shows up in Grafana within ~2 s.
3. **Disconnect tolerance.** Block outbound TCP to the chisel port for 30 s (e.g. Windows firewall rule). Lines written during the window buffer and flush within seconds of restoring the connection. The on-disk log files contain every line whether or not the network was up.
4. **Buffer overflow.** Force a longer outage that exceeds the 10 000-line buffer. Confirm: no OOM, device control unaffected, dropped-line `slog.Warn` reaches Loki on the next successful push.

The cardinality-guard verification (server spec step 5) is covered by the §9.2 4xx unit test rather than a manual case, since production code cannot trigger an extra-label push.

## 10. Backward compatibility

- The on-disk rotated log files (`lab_devices_client.log`, `lab_devices_client_stderr.log`) remain the durable record. Continue to write them. Loki ingest is in addition to, not in replacement of, the existing files. (Stderr now goes through `lumberjack.Logger` instead of plain `O_APPEND`, so it gains 10 MiB / 3-backup rotation. This matches the main log.)
- The forward tunnel is purely additive. A client running an older binary without it will still pass chisel auth and continue to function for device-port forwarding; the server just sees no logs from that host.
- No config-file changes. `config.LogConfig` is unchanged.
