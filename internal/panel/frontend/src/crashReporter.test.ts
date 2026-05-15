import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as App from "./wails/go/main/App";
import { reportCrash, buildCrashReport } from "./crashReporter";

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

  it("calls RecordFrontendCrash with message and stack from an Error", () => {
    const err = new Error("boom");
    err.stack = "stack-text";
    reportCrash(err, "tab:devices");
    expect(App.RecordFrontendCrash).toHaveBeenCalledTimes(1);
    expect(App.RecordFrontendCrash).toHaveBeenCalledWith(
      "boom",
      "tab:devices",
      "stack-text",
    );
  });

  it("stringifies non-Error reasons", () => {
    reportCrash("string-reason", "window.error");
    expect(App.RecordFrontendCrash).toHaveBeenCalledWith(
      "string-reason",
      "window.error",
      "",
    );
  });

  it("does not throw when the binding rejects", async () => {
    vi.mocked(App.RecordFrontendCrash).mockRejectedValueOnce(new Error("bridge dead"));
    expect(() => reportCrash(new Error("x"), "any")).not.toThrow();
    // Let the swallowed rejection settle before the next test runs.
    await Promise.resolve();
  });

  it("does not throw when the binding throws synchronously", () => {
    vi.mocked(App.RecordFrontendCrash).mockImplementationOnce(() => {
      throw new Error("nope");
    });
    expect(() => reportCrash(new Error("x"), "any")).not.toThrow();
  });
});

describe("buildCrashReport", () => {
  it("renders a plain-text report with all fields", () => {
    const text = buildCrashReport({
      scope: "tab:devices",
      message: "boom",
      stack: "at line 1",
      componentStack: "in DevicesTab",
      version: "0.20.0",
      now: new Date("2026-05-15T12:34:56Z"),
    });
    expect(text).toContain("scope:   tab:devices");
    expect(text).toContain("version: 0.20.0");
    expect(text).toContain("boom");
    expect(text).toContain("at line 1");
    expect(text).toContain("in DevicesTab");
    expect(text).toContain("2026-05-15T12:34:56.000Z");
  });

  it("uses fallback strings when stack/component stack are empty", () => {
    const text = buildCrashReport({
      scope: "app",
      message: "x",
      stack: "",
      componentStack: "",
      version: "v",
      now: new Date(0),
    });
    expect(text).toContain("(no stack)");
    expect(text).toContain("(no component stack)");
  });
});
