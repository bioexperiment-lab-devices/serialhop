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

function powerCard(): HTMLElement {
  return screen.getByRole("button", { name: /keep system awake/i });
}

describe("StatusTab — Power section", () => {
  it("renders Off state with 'Click to enable' chip", () => {
    renderTab({ keepAwake: { active: false, reachable: true } });
    const card = powerCard();
    expect(card).toBeEnabled();
    expect(card).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(screen.getByText("Click to keep the system awake.")).toBeInTheDocument();
    expect(screen.getByText("Click to enable")).toBeInTheDocument();
  });

  it("renders On state with 'Click to disable' chip", () => {
    renderTab({ keepAwake: { active: true, reachable: true } });
    const card = powerCard();
    expect(card).toBeEnabled();
    expect(card).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("On")).toBeInTheDocument();
    expect(screen.getByText("System will not sleep or auto-shutdown.")).toBeInTheDocument();
    expect(screen.getByText("Click to disable")).toBeInTheDocument();
  });

  it("renders unreachable state as disabled card with no chip", () => {
    renderTab({ keepAwake: { active: false, reachable: false } });
    const card = powerCard();
    expect(card).toBeDisabled();
    expect(card).toHaveAttribute("aria-disabled", "true");
    expect(card).not.toHaveAttribute("aria-pressed");
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText(/service unreachable/i)).toBeInTheDocument();
    expect(screen.queryByText(/click to/i)).not.toBeInTheDocument();
  });

  it("calls EnableKeepAwake when an off-state card is clicked", async () => {
    mocks.EnableKeepAwake.mockResolvedValueOnce({ active: true, reachable: true });
    const { setKeepAwake } = renderTab({ keepAwake: { active: false, reachable: true } });
    fireEvent.click(powerCard());
    await waitFor(() => expect(mocks.EnableKeepAwake).toHaveBeenCalledTimes(1));
    expect(setKeepAwake).toHaveBeenLastCalledWith({ active: true, reachable: true });
  });

  it("calls DisableKeepAwake when an on-state card is clicked", async () => {
    mocks.DisableKeepAwake.mockResolvedValueOnce({ active: false, reachable: true });
    const { setKeepAwake } = renderTab({ keepAwake: { active: true, reachable: true } });
    fireEvent.click(powerCard());
    await waitFor(() => expect(mocks.DisableKeepAwake).toHaveBeenCalledTimes(1));
    expect(setKeepAwake).toHaveBeenLastCalledWith({ active: false, reachable: true });
  });

  it("shows 'Enabling…' chip and disables the card while a toggle is in flight", async () => {
    let resolve: (v: KeepAwakePayload) => void = () => {};
    mocks.EnableKeepAwake.mockReturnValueOnce(new Promise<KeepAwakePayload>(r => { resolve = r; }));
    renderTab({ keepAwake: { active: false, reachable: true } });
    const card = powerCard();
    fireEvent.click(card);
    expect(card).toBeDisabled();
    expect(card).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText("Enabling…")).toBeInTheDocument();
    resolve({ active: true, reachable: true });
    await waitFor(() => expect(mocks.EnableKeepAwake).toHaveBeenCalled());
  });
});
