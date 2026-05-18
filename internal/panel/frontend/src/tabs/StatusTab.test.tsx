import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { StatusTab } from "./StatusTab";
import { UpdateState } from "../types";
import type { KeepAwakePayload } from "../types";

// Mock the Wails bindings. Each test resets the mocks in beforeEach.
const mocks = vi.hoisted(() => ({
  EnableKeepAwake: vi.fn<[], Promise<KeepAwakePayload>>(),
  DisableKeepAwake: vi.fn<[], Promise<KeepAwakePayload>>(),
}));

vi.mock("../wails/go/main/App", async () => {
  const actual: object = await vi.importActual("../wails/go/main/App");
  return {
    ...actual,
    EnableKeepAwake: mocks.EnableKeepAwake,
    DisableKeepAwake: mocks.DisableKeepAwake,
  };
});

const DEFAULT_LAMPS = {
  service: { tone: "green" as const, label: "OK" },
  server: { tone: "green" as const, label: "Reachable" },
  tunnel: { tone: "green" as const, label: "Up" },
};
const DEFAULT_BUTTONS = { install: false, uninstall: true, restart: true };
const DEFAULT_UPDATE = { state: UpdateState.Idle, release_tag: "" };

function renderTab(overrides?: { keepAwake?: KeepAwakePayload }) {
  const keepAwake: KeepAwakePayload = overrides?.keepAwake ?? { active: false, reachable: true };
  const setKeepAwake = vi.fn();
  render(
    <StatusTab
      lamps={DEFAULT_LAMPS}
      buttons={DEFAULT_BUTTONS}
      update={DEFAULT_UPDATE}
      keepAwake={keepAwake}
      setKeepAwake={setKeepAwake}
    />,
  );
  return { setKeepAwake };
}

beforeEach(() => {
  mocks.EnableKeepAwake.mockReset();
  mocks.DisableKeepAwake.mockReset();
});

describe("StatusTab — Power section", () => {
  it("renders Off lamp + Enable button when inactive", () => {
    renderTab({ keepAwake: { active: false, reachable: true } });
    expect(screen.getByText("Keep system awake")).toBeInTheDocument();
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Enable" })).toBeEnabled();
  });

  it("renders On lamp + Disable button when active", () => {
    renderTab({ keepAwake: { active: true, reachable: true } });
    expect(screen.getByText("On")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disable" })).toBeEnabled();
  });

  it("renders unreachable state with disabled button", () => {
    renderTab({ keepAwake: { active: false, reachable: false } });
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText(/service unreachable/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /enable/i })).toBeDisabled();
  });

  it("calls EnableKeepAwake on click and updates state from response", async () => {
    mocks.EnableKeepAwake.mockResolvedValueOnce({ active: true, reachable: true });
    const { setKeepAwake } = renderTab({ keepAwake: { active: false, reachable: true } });
    fireEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(mocks.EnableKeepAwake).toHaveBeenCalledTimes(1));
    expect(setKeepAwake).toHaveBeenLastCalledWith({ active: true, reachable: true });
  });

  it("calls DisableKeepAwake on click and updates state", async () => {
    mocks.DisableKeepAwake.mockResolvedValueOnce({ active: false, reachable: true });
    const { setKeepAwake } = renderTab({ keepAwake: { active: true, reachable: true } });
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() => expect(mocks.DisableKeepAwake).toHaveBeenCalledTimes(1));
    expect(setKeepAwake).toHaveBeenLastCalledWith({ active: false, reachable: true });
  });

  it("disables button while a toggle is in flight", async () => {
    let resolve: (v: KeepAwakePayload) => void = () => {};
    mocks.EnableKeepAwake.mockReturnValueOnce(new Promise<KeepAwakePayload>(r => { resolve = r; }));
    renderTab({ keepAwake: { active: false, reachable: true } });
    const btn = screen.getByRole("button", { name: "Enable" });
    fireEvent.click(btn);
    expect(btn).toBeDisabled();
    resolve({ active: true, reachable: true });
    await waitFor(() => expect(mocks.EnableKeepAwake).toHaveBeenCalled());
  });
});
