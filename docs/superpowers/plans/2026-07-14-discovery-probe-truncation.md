# Discovery Probe Truncation Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop devices from randomly vanishing from discovery by widening the probe inter-byte slack (25 ms → 250 ms) and retrying once when a partial (1–3 byte) identify reply arrives, with distinguishable log lines for each no-match cause.

**Architecture:** All behavioral change is inside `internal/discovery/probe.go` (`Probe` gains a bounded retry; a `sendProbe` helper is extracted so both attempts share the write loop). `internal/discovery/runner.go` only changes its no-match logging. `Probe`'s signature and the "classification requires a full 4-byte frame" rule are unchanged — every driver's `Attach` consumes the payload bytes, so the fix completes frames rather than tolerating partial ones.

**Tech Stack:** Go, stdlib `log/slog`, in-repo test fakes (`internal/serial.FakePort`, `internal/slogtest.Recorder`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-14-discovery-probe-truncation-design.md`

## Global Constraints

- Branch: `fix/discovery-probe-truncation` (already exists, contains the spec commit). `main` is protected — everything lands via one squash-merged PR.
- PR title (load-bearing for release-please): `fix: retry truncated probe replies and widen discovery inter-byte slack` → patch bump.
- **Never write `BREAKING CHANGE:` in any commit or PR body.**
- Pre-flight before pushing: `gofmt -l .` (must print nothing), `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`, `govulncheck ./...`.
- Tests must pass on macOS and Windows; everything here is fake-based, no platform-specific code.
- Do not commit generated artifacts (`assets/manifest.xml`, `*.syso`, `dist/`, `*.exe`).
- Keep log messages ASCII and prefixed `discovery: ` to match the package's existing convention.

---

### Task 1: Probe retry + widened inter-byte slack

**Files:**
- Modify: `internal/discovery/probe.go`
- Test: `internal/discovery/probe_test.go`

**Interfaces:**
- Consumes: `labserial.ReadFrame(p, initialTimeout, interByteTimeout, max)` and `labserial.Port` (`Write`, `Drain`) — both existing, unchanged.
- Produces: `Probe(p labserial.Port) ([]byte, *ProbeResult, error)` — signature unchanged. New reply semantics Task 2 relies on: when no device is classified, the returned `reply` is the **longest** reply observed across the (up to two) attempts, so a partial first attempt is never masked by an empty retry. `ProbeInterByteSlack` becomes `250 * time.Millisecond`. Unexported helper `sendProbe(p labserial.Port) error` writes the 5 probe bytes with `ProbeByteGap` pacing.

- [ ] **Step 1: Write the failing tests**

In `internal/discovery/probe_test.go`, replace `TestProbe_FewerThan4Bytes` and `TestProbe_NoReply` with the versions below, and add the two new tests. (`probeBytes` is visible to the test — same package.)

```go
// A partial reply proves a device is present, so Probe retries exactly once.
// Here the retry gets silence: the result is still nil, the first attempt's
// bytes are returned for logging, and the probe was sent twice.
func TestProbe_FewerThan4Bytes(t *testing.T) {
	p := serial.NewFakePort("COM6")
	defer p.Close() //nolint:errcheck // test teardown
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{10, 1}) // only 2 bytes, then silence forever
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for partial reply, got %v", got)
	}
	// The partial bytes are the strongest evidence of a device — they must
	// survive the empty retry so callers can log them.
	if string(reply) != string([]byte{10, 1}) {
		t.Errorf("expected partial reply=[10 1], got %v", reply)
	}
	if want := 2 * len(probeBytes); len(p.Written()) != want {
		t.Errorf("probe written %d bytes, want %d (two attempts)", len(p.Written()), want)
	}
}

// A silent port is genuinely deviceless: no retry, single probe write.
func TestProbe_NoReply(t *testing.T) {
	p := serial.NewFakePort("COM8")
	defer p.Close() //nolint:errcheck // test teardown
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for no reply, got %v", got)
	}
	if len(reply) != 0 {
		t.Errorf("expected empty reply on timeout, got %v", reply)
	}
	if want := len(probeBytes); len(p.Written()) != want {
		t.Errorf("probe written %d bytes, want %d (no retry on silence)", len(p.Written()), want)
	}
}

// Partial first attempt, complete frame on the retry: classified normally.
func TestProbe_PartialThenCompleteOnRetry(t *testing.T) {
	p := serial.NewFakePort("COM3")
	defer p.Close() //nolint:errcheck // test teardown
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{10, 1}) // truncated frame → triggers retry
		// Wait for the second probe sequence to finish (drain during the
		// retry would wipe anything fed earlier), then answer properly.
		deadline := time.Now().Add(3 * time.Second)
		for len(p.Written()) < 2*len(probeBytes) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		p.Feed([]byte{10, 99, 88, 77})
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == nil || got.TypeCode != 10 || got.Type != "pump" {
		t.Fatalf("expected pump classification after retry, got %v", got)
	}
	if string(reply) != string([]byte{10, 99, 88, 77}) {
		t.Errorf("expected retry reply=[10 99 88 77], got %v", reply)
	}
}

// Regression for the original field bug: USB latency timers batch reply
// bytes with gaps far beyond the old 25 ms slack. 100 ms inter-byte gaps
// must classify on the first attempt.
func TestProbe_SlowInterByteArrival(t *testing.T) {
	p := serial.NewFakePort("COM4")
	defer p.Close() //nolint:errcheck // test teardown
	go func() {
		time.Sleep(300 * time.Millisecond)
		for _, b := range []byte{10, 99, 88, 77} {
			p.Feed([]byte{b})
			time.Sleep(100 * time.Millisecond)
		}
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == nil || got.Type != "pump" {
		t.Fatalf("expected pump despite slow byte arrival, got %v", got)
	}
	if string(reply) != string([]byte{10, 99, 88, 77}) {
		t.Errorf("reply=%v, want [10 99 88 77]", reply)
	}
	if want := len(probeBytes); len(p.Written()) != want {
		t.Errorf("probe written %d bytes, want %d (no retry needed)", len(p.Written()), want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race -count=1 -run 'TestProbe_' ./internal/discovery/`
Expected: FAIL —
- `TestProbe_FewerThan4Bytes`: written 5 bytes, want 10 (no retry exists yet)
- `TestProbe_PartialThenCompleteOnRetry`: nil classification (no retry exists yet)
- `TestProbe_SlowInterByteArrival`: nil classification (100 ms gap > 25 ms slack truncates after byte 1)
- `TestProbe_NoReply`, `TestProbe_Pump/Valve/Densitometer`, `TestProbe_UnknownTypeByte`: PASS

- [ ] **Step 3: Implement the fix in `internal/discovery/probe.go`**

Change the constant:

```go
const (
	DrainDuration    = 200 * time.Millisecond
	ProbeByteGap     = 10 * time.Millisecond
	ProbeReadTimeout = 1 * time.Second
	// ProbeInterByteSlack must absorb USB-serial latency-timer batching
	// (FTDI default 16 ms) plus OS scheduling jitter; 25 ms was measured
	// truncating real replies. A complete frame still returns the moment
	// its 4th byte lands, so the widened slack costs time only when a
	// device stalls mid-frame.
	ProbeInterByteSlack = 250 * time.Millisecond
)
```

Replace the body of `Probe` from the write loop onward, and add `sendProbe`. Update `Probe`'s doc comment final sentences to mention the retry:

```go
// Probe runs the universal device probe on the given open port and classifies
// the reply. A partial (1–3 byte) reply proves a device is present with a
// broken frame, so Probe drains and retries once; classification always
// requires the full 4-byte frame (drivers' Attach consumes the payload
// bytes). Returns the raw bytes received — on a failed retry, the longest
// reply observed — so callers can log what the port answered. The result is
// non-nil only when the reply could be classified to a known device type; it
// is nil when the port did not reply or returned an unknown type byte. A
// non-nil error indicates an actual I/O failure.
func Probe(p labserial.Port) ([]byte, *ProbeResult, error) {
	if err := p.Drain(DrainDuration); err != nil {
		return nil, nil, fmt.Errorf("drain: %w", err)
	}
	if err := sendProbe(p); err != nil {
		return nil, nil, err
	}
	reply, err := labserial.ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
	if err != nil {
		return reply, nil, fmt.Errorf("read probe reply: %w", err)
	}
	if len(reply) >= 1 && len(reply) < 4 {
		// Partial reply: a device is present but the frame broke (latency-
		// timer batching, drained mid-frame, desync). Flush any straggler
		// byte so it can't misalign the next frame, then probe once more.
		if err := p.Drain(DrainDuration); err != nil {
			return reply, nil, fmt.Errorf("drain before retry: %w", err)
		}
		if err := sendProbe(p); err != nil {
			return reply, nil, err
		}
		retry, err := labserial.ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
		if err != nil {
			return retry, nil, fmt.Errorf("read probe retry reply: %w", err)
		}
		// Keep the longest reply: an empty or shorter retry must not mask
		// the first attempt's bytes in the caller's no-match log.
		if len(retry) >= len(reply) {
			reply = retry
		}
	}
	if len(reply) < 4 {
		return reply, nil, nil
	}
	switch reply[0] {
	case 10:
		return reply, &ProbeResult{Type: "pump", TypeCode: 10}, nil
	case 30:
		return reply, &ProbeResult{Type: "valve", TypeCode: 30}, nil
	case 70:
		return reply, &ProbeResult{Type: "densitometer", TypeCode: 70}, nil
	default:
		return reply, nil, nil
	}
}

// sendProbe writes the probe sequence one byte at a time with ProbeByteGap
// pacing (the N1 firmware parser needs the gaps).
func sendProbe(p labserial.Port) error {
	for _, b := range probeBytes {
		if _, err := p.Write([]byte{b}); err != nil {
			return fmt.Errorf("write probe: %w", err)
		}
		time.Sleep(ProbeByteGap)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race -count=1 ./internal/discovery/`
Expected: PASS (all tests, including the untouched runner tests — `TestRun_SkipsUnknownPartialAndUnopenable`'s 3-byte COM5 port now takes one extra retry cycle ≈ 1.5 s longer, which is expected and harmless).

- [ ] **Step 5: Commit**

```bash
git add internal/discovery/probe.go internal/discovery/probe_test.go
git commit -m "fix: retry truncated probe replies and widen inter-byte slack

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Split the runner's no-match logging by cause

**Files:**
- Modify: `internal/discovery/runner.go:130-137` (the `res == nil` branch inside `Run`)
- Test: `internal/discovery/runner_test.go`

**Interfaces:**
- Consumes: `Probe`'s reply semantics from Task 1 — when `res == nil`, `reply` holds the longest reply observed (empty = truly silent port; 1–3 bytes = device present, frame incomplete even after the retry; ≥4 bytes = unknown type byte).
- Produces: log lines only. Messages (exact strings, nothing parses them but tests assert them): `"discovery: no device on port"` (Debug, unchanged), `"discovery: partial probe reply (device present, frame incomplete)"` (Warn), `"discovery: unknown device type"` (Warn). All three carry the existing `port`, `sent`, `reply` attrs.

- [ ] **Step 1: Write the failing tests**

In `internal/discovery/runner_test.go`:

1. In `TestRun_DebugLogsSentAndReplyPerPort`, COM4 (reply `{99,1,2,3}`) is now an unknown-type Warn. Replace the COM4 message assertion:

```go
	if got := byPort["COM4"].Msg; got != "discovery: unknown device type" {
		t.Errorf("COM4: msg=%q, want %q", got, "discovery: unknown device type")
	}
```

(COM3 "discovery: matched device" and COM5 "discovery: no device on port" assertions stay as they are.)

2. Add a new test (imports gain `"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"`):

```go
// A port that only ever produces a partial frame must be logged at Warn,
// distinguishable from a silent port — a truncated device hiding in Debug
// logs is how the original bug went unnoticed for 30 days.
func TestRun_WarnsOnPartialReply(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o := newOpener(t, map[string][]byte{
		"COM9": {30, 1}, // 2 bytes, then silence — retry also comes up empty
	})
	matches, err := Run(context.Background(), o, []string{"COM9"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %+v", matches)
	}
	if rec.Find(slog.LevelWarn,
		"discovery: partial probe reply (device present, frame incomplete)",
		map[string]any{"port": "COM9", "reply": []int{30, 1}}) == nil {
		t.Errorf("missing partial-reply warn; records=%+v", rec.Records())
	}
	if rec.Find(slog.LevelDebug, "discovery: no device on port",
		map[string]any{"port": "COM9"}) != nil {
		t.Errorf("partial reply must not be logged as 'no device on port'")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race -count=1 -run 'TestRun_DebugLogsSentAndReplyPerPort|TestRun_WarnsOnPartialReply' ./internal/discovery/`
Expected: FAIL — COM4 msg is still `"discovery: no device on port"`, and the partial-reply Warn record is not found.

- [ ] **Step 3: Implement the log split in `internal/discovery/runner.go`**

Replace the `res == nil` branch inside `Run` (currently one `slog.Debug` call):

```go
			if res == nil {
				switch {
				case len(reply) == 0:
					slog.Debug("discovery: no device on port",
						"port", portName,
						"sent", sent,
						"reply", bytesToInts(reply))
				case len(reply) < 4:
					// A partial frame that survived Probe's retry: a device
					// is on this port but its reply keeps breaking up.
					slog.Warn("discovery: partial probe reply (device present, frame incomplete)",
						"port", portName,
						"sent", sent,
						"reply", bytesToInts(reply))
				default:
					slog.Warn("discovery: unknown device type",
						"port", portName,
						"sent", sent,
						"reply", bytesToInts(reply))
				}
				_ = conn.Close()
				return
			}
```

- [ ] **Step 4: Run the package tests to verify they pass**

Run: `go test -race -count=1 ./internal/discovery/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/discovery/runner.go internal/discovery/runner_test.go
git commit -m "fix: log partial and unknown probe replies at warn

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Pre-flight verification, PR, merge

**Files:**
- None created/modified (verification + delivery only).

**Interfaces:**
- Consumes: the two commits from Tasks 1–2 on `fix/discovery-probe-truncation`.
- Produces: a squash-merged PR on `main` titled `fix: retry truncated probe replies and widen discovery inter-byte slack`.

- [ ] **Step 1: Run the full pre-flight suite (CLAUDE.md order)**

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Expected: `gofmt -l .` prints nothing; every other command exits 0. Fix anything that fails before proceeding (and re-run the full suite after any fix).

- [ ] **Step 2: Push the branch and open the PR**

```bash
git push -u origin fix/discovery-probe-truncation
gh pr create \
  --title "fix: retry truncated probe replies and widen discovery inter-byte slack" \
  --body "$(cat <<'EOF'
## Problem

USB-serial adapters batch RX bytes with latency-timer jitter beyond the probe's 25 ms inter-byte slack, so `discovery.Probe` regularly received 1–3 bytes of the 4-byte identify reply and threw them away as \"no known device\". Any device could silently vanish from ~10–20% of discovery rounds (field evidence: densitometer truncated 4/10 probes, valve 2/10, pump 1/6; matched counts fluctuated 3/4/5 across identical port sets). The background reattach path reuses `Probe`, so it inherited the same flakiness.

## Fix

- `ProbeInterByteSlack` 25 ms → 250 ms. Complete frames still return the instant byte 4 lands; silent ports still bail at the 1 s initial timeout.
- `Probe` retries once when a partial (1–3 byte) reply arrives — proof a device is present — draining first so a straggler byte can't misalign the second frame. Classification still requires the full 4-byte frame (every driver's `Attach` consumes the payload bytes).
- The runner's no-match log now distinguishes silent port (Debug) from partial reply / unknown type byte (Warn), so a truncating device can't hide in Debug logs again.

Design: `docs/superpowers/specs/2026-07-14-discovery-probe-truncation-design.md` (also records the COM6 pump #2 diagnosis: board never transmits at any settle/timeout, vendor software also runs 9600 → firmware/TX fault, not host-fixable).

## Testing

- New regression test: reply bytes arriving with 100 ms gaps must classify (fails on the old 25 ms slack).
- New tests: partial-then-complete retry classifies; double-partial returns the evidence bytes and probes exactly twice; silent port probes exactly once (no retry).
- Runner log split covered via slogtest recorder.
- Full suite: `go test -race -count=1 ./...` plus gofmt/vet/golangci-lint/govulncheck all clean.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL printed. (Note: no `BREAKING CHANGE:` anywhere in the body.)

- [ ] **Step 3: Wait for CI (`pr.yml` verify + title check) to go green**

```bash
gh pr checks --watch
```

Expected: all checks pass. If the title check fails, the title drifted from the Conventional Commits form above — fix with `gh pr edit --title`.

- [ ] **Step 4: Squash-merge**

```bash
gh pr merge --squash --delete-branch
```

Expected: merged into `main`; release-please will fold this into the next `chore(main): release 2.0.x` PR (no action needed — per CLAUDE.md, never create tags/releases manually).
