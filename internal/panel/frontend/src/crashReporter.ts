import { RecordFrontendCrash } from "./wails/go/main/App";

// reportCrash is the single entry point used by the React ErrorBoundary
// and the global window.error / unhandledrejection listeners in
// main.tsx. Fire-and-forget: callers run inside paths that must never
// throw (componentDidCatch, global handlers), so every failure mode
// here is swallowed.
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
    // Synchronous throws from the Wails bridge are swallowed so callers
    // never see an exception bubble out of a crash-recording path.
  }
}

// buildCrashReport returns the plain-text bundle that the "Copy report"
// button in ErrorBoundary's fallback writes to the clipboard. Pure
// function — independent of DOM or React.
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
