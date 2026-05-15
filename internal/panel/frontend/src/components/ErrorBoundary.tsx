import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./Button";
import { OpenLogsFolder } from "../wails/go/main/App";
import { reportCrash, buildCrashReport } from "../crashReporter";

interface Props {
  scope: string;
  children: ReactNode;
  // version is included in the copy-to-clipboard bundle. App.tsx passes
  // the resolved panel version; tests use the fallback "unknown".
  version?: string;
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
    const componentStack = info.componentStack ?? "";
    this.setState({ componentStack });
    // Synthesize an Error whose stack includes both the JS stack and
    // React's component stack, so the journal line shows where in the
    // tree the throw originated.
    try {
      const e = new Error(error.message);
      e.stack = `${error.stack ?? ""}\n--- component stack ---${componentStack}`;
      reportCrash(e, this.props.scope);
    } catch {
      // reportCrash itself shouldn't throw, but a render-error path
      // must never re-throw.
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
    const markCopied = () => this.setState({ copied: true });
    if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(markCopied, markCopied);
    } else {
      markCopied();
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
            <b>Something went wrong in the {scopeLabel(this.props.scope)}.</b>
          </p>
          <div
            className="shp-btn-row"
            style={{ marginTop: 12, marginBottom: 12, justifyContent: "center" }}
          >
            <Button onClick={this.copyReport} aria-label="Copy crash report to clipboard">
              {copied ? "Copied ✓" : "Copy report"}
            </Button>
            <Button onClick={this.openLogs} aria-label="Open logs folder">
              Open logs folder
            </Button>
            <Button variant="primary" onClick={this.reset} aria-label="Try again">
              Try again
            </Button>
          </div>
          <Button
            variant="ghost"
            onClick={this.toggleDetails}
            aria-expanded={detailsOpen}
          >
            {detailsOpen ? "Hide details" : "Show details"}
          </Button>
          {detailsOpen && (
            <pre className="shp-mono-view" style={{ maxHeight: 280, marginTop: 8 }}>
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

// scopeLabel converts a boundary scope ("tab:devices", "app", ...) into
// a phrase that fits "Something went wrong in the ___." The internal
// scope strings stay machine-grep'able in the crash journal; the user
// gets the humanized version.
function scopeLabel(scope: string): string {
  if (scope.startsWith("tab:")) {
    const name = scope.slice("tab:".length);
    return `${name.charAt(0).toUpperCase() + name.slice(1)} view`;
  }
  return "panel";
}
