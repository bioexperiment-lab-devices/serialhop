import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ConfigTab } from "./ConfigTab";

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

  it("shows inline error when verifyCredentials returns unauthorized", async () => {
    (App.VerifyCredentials as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ outcome: "unauthorized" });
    render(<ConfigTab onDirtyChange={() => {}} />);
    await waitFor(() => screen.getByDisplayValue("alice"));
    fireEvent.change(screen.getByDisplayValue("alice"), { target: { value: "bob" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => screen.getByText(/rejected these credentials/));
    expect(App.SaveConfig).not.toHaveBeenCalled();
  });
});
