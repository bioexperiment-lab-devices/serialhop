import { createRef } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConfigTab, type ConfigTabHandle } from "./ConfigTab";

vi.mock("../wails/go/main/App", () => ({
  LoadConfigFromDisk: vi.fn(),
  ValidateConfig: vi.fn(),
  SaveConfig: vi.fn(),
  VerifyCredentials: vi.fn(),
  OpenConfigInEditor: vi.fn(),
  PickBackupDir: vi.fn(),
  RestartService: vi.fn(),
}));

const App = await import("../wails/go/main/App");

const seedCfg = () => ({
  lab_bridge: { host: "h", user: "alice", pass: "pw" },
  rest: { port: 0 },
  discovery: { include: [], exclude: [], post_open_settle_ms: 2000 },
  log: { level: "info" },
  raw_serial: { enabled: false },
  auto_update: { enabled: true },
  flashing: { enabled: false, backup_dir: "", keep_n: 10 },
});

beforeEach(() => {
  vi.clearAllMocks();
  (App.LoadConfigFromDisk as ReturnType<typeof vi.fn>).mockResolvedValue(seedCfg());
  (App.ValidateConfig as ReturnType<typeof vi.fn>).mockResolvedValue([]);
  (App.SaveConfig as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: true });
  (App.VerifyCredentials as ReturnType<typeof vi.fn>).mockResolvedValue({ outcome: "skipped" });
});

describe("ConfigTab", () => {
  it("marks form dirty when a field changes and clears on Discard", async () => {
    const onDirty = vi.fn();
    render(<ConfigTab onDirtyChange={onDirty} />);
    await waitFor(() => screen.getByDisplayValue("h"));
    fireEvent.change(screen.getByDisplayValue("h"), { target: { value: "h2" } });
    await waitFor(() => expect(onDirty).toHaveBeenCalledWith(true));
    fireEvent.click(screen.getByText("Discard changes"));
    await waitFor(() => expect(onDirty).toHaveBeenCalledWith(false));
  });

  it("paints both Username and Password red but renders the rejection message only under Password", async () => {
    (App.VerifyCredentials as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ outcome: "unauthorized" });
    render(<ConfigTab onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("alice"));
    fireEvent.change(screen.getByDisplayValue("alice"), { target: { value: "bob" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => {
      // Message appears once — under Password only.
      const hits = screen.getAllByText(/rejected these credentials/);
      expect(hits.length).toBe(1);
    });
    // Both inputs still carry the data-error decoration so both rows turn red.
    const userField = document.querySelector('[data-field="lab_bridge.user"] input');
    const passField = document.querySelector('[data-field="lab_bridge.pass"] input');
    expect(userField?.getAttribute("data-error")).toBe("true");
    expect(passField?.getAttribute("data-error")).toBe("true");
    expect(App.SaveConfig).not.toHaveBeenCalled();
  });

  it("preserves alsoRestart through the needs-confirm modal", async () => {
    (App.VerifyCredentials as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ outcome: "needs_confirm", detail: "no route to host" });
    render(<ConfigTab onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("alice"));
    fireEvent.change(screen.getByDisplayValue("alice"), { target: { value: "bob" } });
    fireEvent.click(screen.getByText("Save & restart"));
    await waitFor(() => screen.getByRole("button", { name: "Save anyway" }));
    fireEvent.click(screen.getByRole("button", { name: "Save anyway" }));
    await waitFor(() => expect(App.SaveConfig).toHaveBeenCalled());
    await waitFor(() => expect(App.RestartService).toHaveBeenCalled());
  });

  it("imperative handle: save() returns true on success and false on validation failure", async () => {
    const ref = createRef<ConfigTabHandle>();
    render(<ConfigTab ref={ref} onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("h"));

    // Dirty the form so save has something to do.
    fireEvent.change(screen.getByDisplayValue("h"), { target: { value: "h2" } });

    const ok = await ref.current!.save();
    expect(ok).toBe(true);
    expect(App.SaveConfig).toHaveBeenCalled();
  });

  it("imperative handle: save() returns false and sets errors on validation failure", async () => {
    (App.ValidateConfig as ReturnType<typeof vi.fn>).mockResolvedValueOnce([{ field: "lab_bridge.host", detail: "required" }]);
    const ref = createRef<ConfigTabHandle>();
    render(<ConfigTab ref={ref} onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("h"));
    fireEvent.change(screen.getByDisplayValue("h"), { target: { value: "" } });

    const ok = await ref.current!.save();
    expect(ok).toBe(false);
    expect(App.SaveConfig).not.toHaveBeenCalled();
  });

  it("integer fields can be cleared to empty (leading zero is erasable)", async () => {
    render(<ConfigTab onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("h"));
    // REST port loads as 0 — clearing the input must leave it empty,
    // not snap back to "0" via the form's number fallback.
    const portInput = document.querySelector(
      '[data-field="rest.port"] input',
    ) as HTMLInputElement;
    expect(portInput.value).toBe("0");
    fireEvent.change(portInput, { target: { value: "" } });
    expect(portInput.value).toBe("");
    // Typing a fresh value after clearing must work and not carry a leading zero.
    fireEvent.change(portInput, { target: { value: "8080" } });
    expect(portInput.value).toBe("8080");
  });

  it("discard restores integer fields to the loaded value", async () => {
    const ref = createRef<ConfigTabHandle>();
    render(<ConfigTab ref={ref} onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("h"));
    const settleInput = document.querySelector(
      '[data-field="discovery.post_open_settle_ms"] input',
    ) as HTMLInputElement;
    expect(settleInput.value).toBe("2000");
    fireEvent.change(settleInput, { target: { value: "" } });
    expect(settleInput.value).toBe("");
    ref.current!.discard();
    await waitFor(() => expect(settleInput.value).toBe("2000"));
  });

  it("imperative handle: discard() resets the form", async () => {
    const ref = createRef<ConfigTabHandle>();
    render(<ConfigTab ref={ref} onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("h"));
    fireEvent.change(screen.getByDisplayValue("h"), { target: { value: "changed" } });
    await waitFor(() => screen.getByDisplayValue("changed"));

    ref.current!.discard();
    await waitFor(() => screen.getByDisplayValue("h"));
  });
});
