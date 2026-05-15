import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ErrorBoundary } from "./ErrorBoundary";
import * as App from "../wails/go/main/App";

vi.mock("../wails/go/main/App", () => ({
  RecordFrontendCrash: vi.fn(async () => {}),
  OpenLogsFolder: vi.fn(async () => {}),
}));

function Boom({ when }: { when: boolean }) {
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
    render(
      <ErrorBoundary scope="tab:test">
        <Boom when={true} />
      </ErrorBoundary>,
    );
    expect(screen.getByText(/something went wrong/i)).toBeInTheDocument();
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

  it("Try again resets state so the child can re-render", () => {
    // Use a let-bound boolean so the second render (after Try again) sees
    // a healthy child. The test passes if the boundary's reset() clears
    // state.error so render() returns children instead of the fallback.
    let shouldThrow = true;
    function Toggle() {
      if (shouldThrow) throw new Error("first only");
      return <div>recovered</div>;
    }
    const { rerender } = render(
      <ErrorBoundary scope="tab:test">
        <Toggle />
      </ErrorBoundary>,
    );
    expect(screen.getByText(/something went wrong/i)).toBeInTheDocument();
    shouldThrow = false;
    fireEvent.click(screen.getByRole("button", { name: /try again/i }));
    // After reset, re-render the tree so React picks up the new closure
    // value of shouldThrow. (Reset alone is enough in production where the
    // child's own state drives the change; this test substitutes a rerender.)
    rerender(
      <ErrorBoundary scope="tab:test">
        <Toggle />
      </ErrorBoundary>,
    );
    expect(screen.getByText("recovered")).toBeInTheDocument();
  });

  it("Show details toggle reveals the stack", () => {
    render(
      <ErrorBoundary scope="tab:test">
        <Boom when={true} />
      </ErrorBoundary>,
    );
    expect(screen.queryByText(/kaboom/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /show details/i }));
    expect(screen.getByText(/kaboom/)).toBeInTheDocument();
  });

  it("humanizes the scope in the heading", () => {
    render(
      <ErrorBoundary scope="tab:devices">
        <Boom when={true} />
      </ErrorBoundary>,
    );
    expect(
      screen.getByText(/Something went wrong in the Devices view\./i),
    ).toBeInTheDocument();
  });

  it("falls back to 'panel' for non-tab scopes", () => {
    render(
      <ErrorBoundary scope="app">
        <Boom when={true} />
      </ErrorBoundary>,
    );
    expect(
      screen.getByText(/Something went wrong in the panel\./i),
    ).toBeInTheDocument();
  });

  it("Sibling chrome outside the boundary stays mounted on child throw", () => {
    render(
      <div>
        <div data-testid="chrome">title bar</div>
        <ErrorBoundary scope="tab:test">
          <Boom when={true} />
        </ErrorBoundary>
      </div>,
    );
    // Chrome is a sibling of the boundary — must not be unmounted.
    expect(screen.getByTestId("chrome")).toBeInTheDocument();
    expect(screen.getByText(/something went wrong/i)).toBeInTheDocument();
  });
});
