# Panel Crash Safety Net Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make it impossible for a React render error in any tab to blank
the panel window. Catch JS-side crashes, show a recoverable fallback,
keep `TitleBar` always mounted, and persist a JSON-lines crash journal
under `%ProgramData%\SerialHop\logs\`.

**Architecture:** A `<ErrorBoundary>` React class component wraps each tab
body individually, plus a second outer boundary wraps the inner shell.
`main.tsx` adds `window.error` / `unhandledrejection` listeners. All paths
call a new `RecordFrontendCrash(message, source, stack)` Wails binding,
which appends one JSON line per crash to a new file capped at 64 KiB. No
existing behavior changes; no bound method takes `context.Context`.

**Tech Stack:** Go (Wails v2.12), React 18 (TSX), Vitest, Testing Library.

---

### Task 1: Add `paths.PanelCrashJournalPath()` helper + constant

**Files:**
- Modify: `internal/paths/paths.go`
- Test: `internal/paths/paths_test.go` (if exists; otherwise this is part of Task 4's Go test via integration — the helper is trivial)

- [ ] **Step 1: Check whether `internal/paths/paths_test.go` exists**

Run: `ls internal/paths/`
Expected: list of files; note presence/absence of `paths_test.go`.

- [ ] **Step 2: Modify `internal/paths/paths.go` — add constant and helper**

Find the existing `PanelErrorLogFileName` constant near line 25 and the
`PanelErrorLogPath()` function. Add the new constant right next to the
`PanelErrorLogFileName` constant, and add the new helper right after
`PanelErrorLogPath()`:

```go
// PanelCrashJournalFileName is the on-disk name of the JSON-lines crash
// journal the panel writes via RecordFrontendCrash. One line per caught
// JS-side error; the file is capped at ~64 KiB by appendCapped in
// internal/panel/crash_journal.go.
const PanelCrashJournalFileName = "SerialHop_panel_crash.log"

// PanelCrashJournalPath returns the absolute path to the crash journal
// under LogsDir. Returns "" when DataDir is unavailable so callers can
// no-op silently — matching how the binding swallows write errors.
func PanelCrashJournalPath() string {
    if DataDir() == "" {
        return ""
    }
    return filepath.Join(LogsDir(), PanelCrashJournalFileName)
}
```

- [ ] **Step 3: Verify Go code still compiles**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/paths/paths.go
git commit -m "feat(paths): add PanelCrashJournalPath helper"
```

---

### Task 2: Implement `appendCapped` + `appendCrashJournal` with failing tests first

**Files:**
- Create: `internal/panel/crash_journal.go`
- Create: `internal/panel/crash_journal_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/panel/crash_journal_test.go`:

```go
package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendCapped_AppendsLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j.log")
	if err := appendCapped(p, []byte("alpha\n"), 1024); err != nil {
		t.Fatalf("appendCapped: %v", err)
	}
	if err := appendCapped(p, []byte("beta\n"), 1024); err != nil {
		t.Fatalf("appendCapped: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(b), "alpha\nbeta\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestAppendCapped_TrimsToLastNBytesAtLineBoundary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j.log")
	for i := 0; i < 200; i++ {
		line := strings.Repeat("x", 80) + "\n" // 81 bytes per line
		if err := appendCapped(p, []byte(line), 1024); err != nil {
			t.Fatalf("appendCapped i=%d: %v", i, err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if int64(len(b)) > 1024 {
		t.Fatalf("file size %d exceeds cap 1024", len(b))
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("file does not end with newline: %q", string(b))
	}
	// Surviving content must start at a line boundary (no partial leading line).
	first := strings.IndexByte(string(b), '\n')
	if first < 0 {
		t.Fatalf("no newline in trimmed content")
	}
	if first != len(strings.Repeat("x", 80)) && first != 0 {
		// Either the cut landed on a clean boundary (first char of new line at index 0)
		// or the trimmed file starts at the start of an x-line of length 80.
		// A length other than 80 means a partial line was kept.
		t.Fatalf("first line length = %d, want 0 or 80", first)
	}
}

func TestAppendCrashJournal_EmptyPathIsNoop(t *testing.T) {
	// Override the path helper via a small indirection: appendCrashJournal
	// must not panic and must not return an error visible to callers when
	// PanelCrashJournalPath() returns "".
	// We achieve this by setting an env var the helper consults; see
	// crash_journal.go for the SERIALHOP_PANEL_CRASH_JOURNAL_PATH override.
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_PATH", "")
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_DISABLE", "1")
	// Should not panic.
	appendCrashJournal("msg", "src", "stack", "v0", time.Now())
}

func TestAppendCrashJournal_WritesJSONLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j.log")
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_PATH", p)
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_DISABLE", "")
	now := time.Date(2026, 5, 15, 12, 34, 56, 0, time.UTC)
	appendCrashJournal("boom", "tab:devices", "at line 1", "0.20.0", now)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got crashEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &got); err != nil {
		t.Fatalf("unmarshal: %v -- raw %q", err, b)
	}
	if got.Message != "boom" || got.Source != "tab:devices" ||
		got.Stack != "at line 1" || got.Version != "0.20.0" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.Time != now.Format(time.RFC3339Nano) {
		t.Fatalf("Time = %q, want %q", got.Time, now.Format(time.RFC3339Nano))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/panel/ -run 'TestAppendCapped|TestAppendCrashJournal' -count=1`
Expected: FAIL (undefined: appendCapped / appendCrashJournal / crashEntry).

- [ ] **Step 3: Write `internal/panel/crash_journal.go`**

```go
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

// crashJournalMaxBytes caps the on-disk journal so a noisy crash loop
// cannot fill the disk. 64 KiB holds many entries while staying small
// enough to paste into a bug report.
const crashJournalMaxBytes int64 = 64 * 1024

// crashEntry is the JSON shape written per crash. Keep field order stable
// — operators read this file by hand.
type crashEntry struct {
	Time    string `json:"time"`
	Version string `json:"version"`
	Source  string `json:"source"`
	Message string `json:"message"`
	Stack   string `json:"stack"`
}

// crashJournalPath returns the journal path, with an env-var override for
// tests. An empty result means "no journal here"; callers must no-op.
func crashJournalPath() string {
	if v, ok := os.LookupEnv("SERIALHOP_PANEL_CRASH_JOURNAL_PATH"); ok {
		return v
	}
	if os.Getenv("SERIALHOP_PANEL_CRASH_JOURNAL_DISABLE") == "1" {
		return ""
	}
	return paths.PanelCrashJournalPath()
}

// appendCrashJournal writes one JSON line per crash. Best-effort: any
// error is recorded via writePanelDebugLog and swallowed.
func appendCrashJournal(message, source, stack, ver string, now time.Time) {
	defer func() {
		// Last-ditch panic guard: we are called from RecordFrontendCrash,
		// which is itself called from React's componentDidCatch. A panic
		// here cannot be allowed to propagate.
		_ = recover()
	}()
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

// appendCapped appends `data` to `path`, then if the resulting file
// exceeds `max` bytes, rewrites it keeping only the trailing `max` bytes
// aligned to the next newline (so the first surviving entry isn't
// truncated). Best-effort; returns the first I/O error encountered.
//
// Single-process panel ⇒ no cross-process locking is required.
func appendCapped(path string, data []byte, max int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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

	// File is over the cap. Read the trailing `max` bytes, then trim the
	// partial leading line and rewrite.
	rf, err := os.Open(path)
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/panel/ -run 'TestAppendCapped|TestAppendCrashJournal' -count=1 -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Run the full Go test suite to confirm no regressions**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/crash_journal.go internal/panel/crash_journal_test.go
git commit -m "feat(panel): add crash_journal append-with-cap helper"
```

---

### Task 3: Add `RecordFrontendCrash` Wails binding

**Files:**
- Modify: `internal/panel/bindings.go`
- Verify: `internal/panel/bindings_ctx_check_test.go` continues to pass

- [ ] **Step 1: Read `internal/panel/bindings_ctx_check_test.go` to confirm what it asserts**

Run: `cat internal/panel/bindings_ctx_check_test.go | head -80`
Expected: see the reflection test enumerating bound methods; confirm it
will run on macOS (no `//go:build windows` tag — or if there is one, the
test stays Windows-only and we accept that the Wails verification runs
in CI / on Windows).

- [ ] **Step 2: Append the new binding to `internal/panel/bindings.go`**

Add at the end of the file, after the existing helpers:

```go
// RecordFrontendCrash appends one JSON line to the panel crash journal.
// Called by the React ErrorBoundary fallback and by the JS-side global
// `error` / `unhandledrejection` listeners.
//
// This method is intentionally string-only and does NOT take
// context.Context — methods on *panel.App reached via main.App embedding
// must not take ctx (see TestApp_NoBoundMethodTakesContextContext for
// the regression gate). The journal write is fully synchronous and best
// effort; any failure is swallowed inside appendCrashJournal.
func (a *App) RecordFrontendCrash(message, source, stack string) {
	appendCrashJournal(message, source, stack, version.Base(), time.Now().UTC())
}
```

- [ ] **Step 3: Build and run all Go tests including the reflection gate**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS — `TestApp_NoBoundMethodTakesContextContext` confirms the
new method does not take ctx.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/bindings.go
git commit -m "feat(panel): bind RecordFrontendCrash for SPA crash reporting"
```

---

### Task 4: Add the manual binding stubs (TS) + preview-shim no-op

**Files:**
- Modify: `internal/panel/frontend/src/wails/go/main/App.ts`
- Modify: `internal/panel/frontend/src/preview-shim/bindings.ts`

- [ ] **Step 1: Read both files to find the right insertion point**

Run:
```
cat internal/panel/frontend/src/wails/go/main/App.ts
cat internal/panel/frontend/src/preview-shim/bindings.ts
```

Expected: existing functions like `GetVersion`, `Diagnostics`, etc. in
`App.ts`; matching keys in the preview shim's `App` object.

- [ ] **Step 2: Modify `App.ts` — add the new export**

Add (placement: alphabetical or end-of-file, matching the existing style):

```ts
export function RecordFrontendCrash(message: string, source: string, stack: string): Promise<void>;
```

If the file uses an implementation re-export pattern instead of bare
declarations, mirror that pattern. Match the exact style used for
`OpenLogsFolder` / `OpenConfigInEditor`, both of which return
`Promise<void>` in Go.

- [ ] **Step 3: Modify `preview-shim/bindings.ts` — add a no-op fake**

In the `App` object (the `Record<string, (...args: any[]) => Promise<any>>`
keyed by method name), add:

```ts
RecordFrontendCrash: async (_message: string, _source: string, _stack: string) => {
  // Preview shim no-op; crash journaling only runs under the real Wails
  // bridge.
},
```

- [ ] **Step 4: Type-check the frontend**

Run: `cd internal/panel/frontend && npm install --no-audit --no-fund && npx tsc --noEmit`
Expected: 0 errors.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/frontend/src/wails/go/main/App.ts internal/panel/frontend/src/preview-shim/bindings.ts
git commit -m "feat(frontend): declare RecordFrontendCrash binding + preview fake"
```

---

### Task 5: Add `crashReporter.ts` helper with failing tests first

**Files:**
- Create: `internal/panel/frontend/src/crashReporter.ts`
- Create: `internal/panel/frontend/src/crashReporter.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `internal/panel/frontend/src/crashReporter.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as App from "./wails/go/main/App";
import { reportCrash } from "./crashReporter";

vi.mock("./wails/go/main/App", () => ({
  RecordFrontendCrash: vi.fn(async () => {}),
}));

describe("reportCrash", () => {
  beforeEach(() => {
    vi.mocked(App.RecordFrontendCrash).mockClear();
    vi.mocked(App.RecordFrontendCrash).mockResolvedValue(undefined as unknown as void);
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("calls RecordFrontendCrash with message, source, stack from an Error", () => {
    const err = new Error("boom");
    err.stack = "stack-text";
    reportCrash(err, "tab:devices");
    expect(App.RecordFrontendCrash).toHaveBeenCalledTimes(1);
    expect(App.RecordFrontendCrash).toHaveBeenCalledWith("boom", "tab:devices", "stack-text");
  });

  it("handles non-Error values by stringifying them", () => {
    reportCrash("string-reason", "window.error");
    expect(App.RecordFrontendCrash).toHaveBeenCalledWith("string-reason", "window.error", "");
  });

  it("does not throw when the binding rejects", async () => {
    vi.mocked(App.RecordFrontendCrash).mockRejectedValueOnce(new Error("bridge dead"));
    expect(() => reportCrash(new Error("x"), "any")).not.toThrow();
    // Allow the swallowed rejection to settle before the test ends.
    await Promise.resolve();
  });

  it("does not throw when the binding throws synchronously", () => {
    vi.mocked(App.RecordFrontendCrash).mockImplementationOnce(() => {
      throw new Error("nope");
    });
    expect(() => reportCrash(new Error("x"), "any")).not.toThrow();
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd internal/panel/frontend && npx vitest run src/crashReporter.test.ts`
Expected: FAIL — `crashReporter` module not found.

- [ ] **Step 3: Write `internal/panel/frontend/src/crashReporter.ts`**

```ts
import { RecordFrontendCrash } from "./wails/go/main/App";

// reportCrash is the single entry point used by the React ErrorBoundary
// and by the global window.error / unhandledrejection listeners in
// main.tsx. Fire-and-forget: callers are not allowed to throw inside a
// crash-recording path, so every failure mode here is swallowed.
export function reportCrash(reason: unknown, source: string): void {
  let message: string;
  let stack = "";
  if (reason instanceof Error) {
    message = reason.message || reason.name || "Error";
    stack = reason.stack ?? "";
  } else if (typeof reason === "string") {
    message = reason;
  } else {
    try {
      message = JSON.stringify(reason);
    } catch {
      message = String(reason);
    }
  }
  try {
    void RecordFrontendCrash(message, source, stack).catch(() => {});
  } catch {
    // Synchronous throws from the Wails bridge are swallowed here so
    // callers (ErrorBoundary.componentDidCatch, global listeners) never
    // see an exception bubble out.
  }
}

// buildCrashReport returns the plain-text "Copy report" bundle the
// ErrorBoundary fallback puts on the clipboard. Pure function — easy to
// snapshot-test if we ever need to.
export function buildCrashReport(input: {
  scope: string;
  message: string;
  stack: string;
  componentStack: string;
  version: string;
  now: Date;
}): string {
  return [
    `SerialHop panel crash report`,
    `time:    ${input.now.toISOString()}`,
    `version: ${input.version}`,
    `scope:   ${input.scope}`,
    ``,
    `message:`,
    input.message,
    ``,
    `stack:`,
    input.stack || "(no stack)",
    ``,
    `component stack:`,
    input.componentStack || "(no component stack)",
  ].join("\n");
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd internal/panel/frontend && npx vitest run src/crashReporter.test.ts`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/panel/frontend/src/crashReporter.ts internal/panel/frontend/src/crashReporter.test.ts
git commit -m "feat(frontend): add crashReporter helper + tests"
```

---

### Task 6: Implement `ErrorBoundary` with failing test first

**Files:**
- Create: `internal/panel/frontend/src/components/ErrorBoundary.tsx`
- Create: `internal/panel/frontend/src/components/ErrorBoundary.test.tsx`

- [ ] **Step 1: Write the failing test**

Create `internal/panel/frontend/src/components/ErrorBoundary.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ErrorBoundary } from "./ErrorBoundary";
import * as App from "../wails/go/main/App";

vi.mock("../wails/go/main/App", () => ({
  RecordFrontendCrash: vi.fn(async () => {}),
  OpenLogsFolder: vi.fn(async () => {}),
  GetVersion: vi.fn(async () => "test"),
}));

function Boom({ when }: { when: boolean }): JSX.Element {
  if (when) throw new Error("kaboom");
  return <div>inner ok</div>;
}

describe("ErrorBoundary", () => {
  beforeEach(() => {
    vi.mocked(App.RecordFrontendCrash).mockClear();
    vi.mocked(App.OpenLogsFolder).mockClear();
  });

  it("renders children when no error", () => {
    render(
      <ErrorBoundary scope="tab:test">
        <Boom when={false} />
      </ErrorBoundary>,
    );
    expect(screen.getByText("inner ok")).toBeInTheDocument();
  });

  it("renders fallback and records crash on child throw", () => {
    // Vitest + RTL surface React's "error during render" message in the
    // test output even when caught by an ErrorBoundary. That's expected.
    render(
      <ErrorBoundary scope="tab:test">
        <Boom when={true} />
      </ErrorBoundary>,
    );
    expect(screen.getByText(/Something went wrong/i)).toBeInTheDocument();
    expect(screen.queryByText("inner ok")).not.toBeInTheDocument();
    expect(App.RecordFrontendCrash).toHaveBeenCalledTimes(1);
    expect(App.RecordFrontendCrash).toHaveBeenCalledWith(
      "kaboom",
      "tab:test",
      expect.stringContaining("kaboom"),
    );
  });

  it("Open logs folder button calls the binding", () => {
    render(
      <ErrorBoundary scope="tab:test">
        <Boom when={true} />
      </ErrorBoundary>,
    );
    fireEvent.click(screen.getByRole("button", { name: /open logs folder/i }));
    expect(App.OpenLogsFolder).toHaveBeenCalledTimes(1);
  });

  it("Try again resets state and re-renders children", () => {
    function Toggle({ ref }: { ref: { count: number } }) {
      if (ref.count === 0) {
        ref.count = 1;
        throw new Error("first only");
      }
      return <div>recovered</div>;
    }
    const ref = { count: 0 };
    render(
      <ErrorBoundary scope="tab:test">
        <Toggle ref={ref} />
      </ErrorBoundary>,
    );
    expect(screen.getByText(/Something went wrong/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /try again/i }));
    expect(screen.getByText("recovered")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd internal/panel/frontend && npx vitest run src/components/ErrorBoundary.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `internal/panel/frontend/src/components/ErrorBoundary.tsx`**

```tsx
import { Component, type ErrorInfo, type ReactNode } from "react";
import { OpenLogsFolder } from "../wails/go/main/App";
import { reportCrash, buildCrashReport } from "../crashReporter";

interface Props {
  scope: string;
  children: ReactNode;
  version?: string; // optional override for tests; falls back to "unknown"
}

interface State {
  error: Error | null;
  componentStack: string;
  detailsOpen: boolean;
  copied: boolean;
}

const initialState: State = {
  error: null,
  componentStack: "",
  detailsOpen: false,
  copied: false,
};

export class ErrorBoundary extends Component<Props, State> {
  state: State = initialState;

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    this.setState({ componentStack: info.componentStack ?? "" });
    // Synthesize a stack that includes both the JS error stack and React's
    // component stack so operators see where in the tree it threw.
    const combinedStack = `${error.stack ?? ""}\n--- component stack ---${
      info.componentStack ?? ""
    }`;
    try {
      const e = new Error(error.message);
      e.stack = combinedStack;
      reportCrash(e, this.props.scope);
    } catch {
      // reportCrash should never throw, but be defensive.
    }
  }

  reset = (): void => {
    this.setState(initialState);
  };

  toggleDetails = (): void => {
    this.setState(s => ({ detailsOpen: !s.detailsOpen }));
  };

  copyReport = (): void => {
    const { error, componentStack } = this.state;
    if (!error) return;
    const text = buildCrashReport({
      scope: this.props.scope,
      message: error.message,
      stack: error.stack ?? "",
      componentStack,
      version: this.props.version ?? "unknown",
      now: new Date(),
    });
    const setCopied = () => this.setState({ copied: true });
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(setCopied, () => setCopied());
    } else {
      setCopied();
    }
  };

  openLogs = (): void => {
    OpenLogsFolder().catch(() => {});
  };

  render(): ReactNode {
    const { error, componentStack, detailsOpen, copied } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="shp-empty" role="alert">
        <div className="shp-empty__body">
          <p>
            <b>Something went wrong in the {this.props.scope} view.</b>
          </p>
          <p style={{ marginTop: 4 }}>
            The rest of the window is still usable. You can copy the
            report, open the logs folder, or try rendering this view
            again.
          </p>
          <div className="shp-btn-row" style={{ marginTop: 12, marginBottom: 12 }}>
            <button
              type="button"
              className="shp-btn"
              onClick={this.copyReport}
              aria-label="Copy crash report to clipboard"
            >
              {copied ? "Copied ✓" : "Copy report"}
            </button>
            <button
              type="button"
              className="shp-btn"
              onClick={this.openLogs}
              aria-label="Open logs folder"
            >
              Open logs folder
            </button>
            <button
              type="button"
              className="shp-btn shp-btn--primary"
              onClick={this.reset}
              aria-label="Try again"
            >
              Try again
            </button>
          </div>
          <button
            type="button"
            className="shp-btn shp-btn--ghost"
            onClick={this.toggleDetails}
            aria-expanded={detailsOpen}
            style={{ marginBottom: 8 }}
          >
            {detailsOpen ? "Hide details" : "Show details"}
          </button>
          {detailsOpen && (
            <pre className="shp-mono-view" style={{ maxHeight: 280 }}>
              {error.message}
              {"\n\n"}
              {error.stack ?? "(no stack)"}
              {componentStack ? "\n--- component stack ---" + componentStack : ""}
            </pre>
          )}
        </div>
      </div>
    );
  }
}
```

- [ ] **Step 4: Inspect existing button class names**

Run: `grep -n "shp-btn" internal/panel/frontend/src/components/Button.tsx`
Expected: confirm whether the project uses `shp-btn` / `shp-btn--primary`
/ `shp-btn--ghost` or different names. If the names differ, edit the JSX
above to match. The shipped `Button.tsx` is the source of truth.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd internal/panel/frontend && npx vitest run src/components/ErrorBoundary.test.tsx`
Expected: PASS (all four tests). Note: React will print "The above error
occurred in the <Boom> component" to the console — that is expected and
harmless inside a vitest run.

- [ ] **Step 6: Run the full Vitest suite to confirm no regressions**

Run: `cd internal/panel/frontend && npx vitest run`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/frontend/src/components/ErrorBoundary.tsx internal/panel/frontend/src/components/ErrorBoundary.test.tsx
git commit -m "feat(frontend): add ErrorBoundary with copy/logs/retry fallback"
```

---

### Task 7: Wrap each tab in `App.tsx` with an `ErrorBoundary`

**Files:**
- Modify: `internal/panel/frontend/src/App.tsx`

- [ ] **Step 1: Apply the wrap**

Open `internal/panel/frontend/src/App.tsx`. Add the import:

```tsx
import { ErrorBoundary } from "./components/ErrorBoundary";
```

Replace the conditional-tab block (currently five lines `{tab === "status" && <StatusTab ... />}` etc.) with the same five lines, each wrapped in `<ErrorBoundary scope="tab:NAME" version={version}>...</ErrorBoundary>`.

Also wrap the inner shell so non-tab UI is protected as well: change
```
<div className="shp-content">
  <div className="shp-content__pad" data-tab={tab}>
    ...tabs...
  </div>
</div>
```
to:
```
<ErrorBoundary scope="app" version={version}>
  <div className="shp-content">
    <div className="shp-content__pad" data-tab={tab}>
      ...wrapped tabs...
    </div>
  </div>
</ErrorBoundary>
```

TitleBar, TabBar, Warning, Footer, and the dirty-config Modal stay
outside any boundary.

- [ ] **Step 2: Type-check + run tests**

Run: `cd internal/panel/frontend && npx tsc --noEmit && npx vitest run`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/panel/frontend/src/App.tsx
git commit -m "feat(frontend): wrap each tab in ErrorBoundary"
```

---

### Task 8: Wire global error + unhandledrejection listeners in `main.tsx`

**Files:**
- Modify: `internal/panel/frontend/src/main.tsx`

- [ ] **Step 1: Replace `main.tsx` body**

```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import { reportCrash } from "./crashReporter";
import "./styles/global.css";

window.addEventListener("error", (ev) => {
  reportCrash(ev.error ?? ev.message ?? "window.error", "window.error");
});

window.addEventListener("unhandledrejection", (ev) => {
  reportCrash(ev.reason ?? "unhandledrejection", "unhandledrejection");
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
```

- [ ] **Step 2: Type-check + run tests**

Run: `cd internal/panel/frontend && npx tsc --noEmit && npx vitest run`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/panel/frontend/src/main.tsx
git commit -m "feat(frontend): report window.error and unhandledrejection to crash journal"
```

---

### Task 9: Final pre-flight (Go format/lint/test + frontend type+test)

- [ ] **Step 1: Run Go format and lint**

Run:
```
gofmt -l .
go vet ./...
golangci-lint run
```
Expected: each prints nothing (or the lint command emits 0 issues).

- [ ] **Step 2: Run full Go test suite with race detector**

Run: `go test -race -count=1 ./...`
Expected: PASS.

- [ ] **Step 3: Run frontend type-check and vitest**

Run: `cd internal/panel/frontend && npx tsc --noEmit && npx vitest run`
Expected: PASS.

- [ ] **Step 4: Push branch and open PR**

```bash
git push -u origin worktree-safety-net-error-boundary
gh pr create --title "feat: panel crash safety net (error boundary + crash journal)" --body "..."
```

PR body (HEREDOC):

> ## Summary
> - Adds a React `ErrorBoundary` around each tab so a render error can no longer blank the window. `TitleBar`, `TabBar`, `Warning`, `Footer` stay mounted.
> - Adds global `window.error` / `unhandledrejection` listeners that record async crashes to the same journal.
> - New Wails binding `RecordFrontendCrash` (string-only — does not take `context.Context`) appends one JSON line to `%ProgramData%\SerialHop\logs\SerialHop_panel_crash.log`, capped at 64 KiB.
>
> Companion to the upcoming fix for the Devices-tab `null`-slice TypeError (PR #TBD). Verifiable while that bug is still present per spec §6.
>
> ## Test plan
> - [ ] Install build on a clean Windows VM **without** running Install service.
> - [ ] Open the panel → Devices tab. Expect ErrorBoundary fallback, TitleBar Close still works.
> - [ ] Click Status / Config / Ports — tab switching still works.
> - [ ] Inspect `%ProgramData%\SerialHop\logs\SerialHop_panel_crash.log` — one JSON line with `source: "tab:devices"` and message about reading `.length` of null.
> - [ ] CI: `go test -race`, `golangci-lint`, `vitest`, `tsc --noEmit` all green.

---

## Self-Review Notes

- Spec §3.1–§3.6 each map to Tasks 1–8.
- Spec §5.1 → Task 6; §5.2 → Task 5; §5.3 → Task 2; §5.4 is automatic via existing test.
- No placeholders. Every code step shows the actual code.
- `reportCrash`/`buildCrashReport`/`appendCrashJournal`/`appendCapped`
  names are used consistently across Tasks 5/6/2.
- `RecordFrontendCrash` is the binding name used in Tasks 3, 4, 5, 6
  consistently. Method signature `(message, source, stack string)` is
  identical in Go (Task 3) and TS (Tasks 4, 5).
