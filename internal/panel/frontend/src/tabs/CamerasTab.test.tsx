import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { CamerasTab } from "./CamerasTab";

vi.mock("../wails/go/main/App", () => ({
  ListCameras: vi.fn(),
  SetCameraArmed: vi.fn(),
  RefreshCameras: vi.fn(),
  DiagnoseCameras: vi.fn(),
}));

vi.mock("../wailsEvents", () => ({
  useWailsEvent: () => () => {},
}));

import { ListCameras, SetCameraArmed, DiagnoseCameras } from "../wails/go/main/App";

describe("CamerasTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders empty state when no cameras", async () => {
    (ListCameras as any).mockResolvedValue({ cameras: [], ffmpeg_ok: true });
    render(<CamerasTab />);
    expect(await screen.findByText(/No cameras detected/i)).toBeInTheDocument();
  });

  it("renders one card per camera", async () => {
    (ListCameras as any).mockResolvedValue({
      cameras: [
        { id: "id-A", label: "Logitech C270", armed: false, connected: true, live: false },
        { id: "id-B", label: "Front Cam", armed: true, connected: true, live: false },
      ],
      ffmpeg_ok: true,
    });
    render(<CamerasTab />);
    expect(await screen.findByText("Logitech C270")).toBeInTheDocument();
    expect(await screen.findByText("Front Cam")).toBeInTheDocument();
  });

  it("toggles arming", async () => {
    (ListCameras as any).mockResolvedValue({
      cameras: [{ id: "id-A", label: "Cam A", armed: false, connected: true, live: false }],
      ffmpeg_ok: true,
    });
    (SetCameraArmed as any).mockResolvedValue(undefined);
    render(<CamerasTab />);
    const toggle = await screen.findByRole("switch", { name: /allow streaming/i });
    fireEvent.click(toggle);
    await waitFor(() => expect(SetCameraArmed).toHaveBeenCalledWith("id-A", true));
  });

  it("shows ffmpeg-unavailable banner", async () => {
    (ListCameras as any).mockResolvedValue({ cameras: [], ffmpeg_ok: false });
    render(<CamerasTab />);
    expect(await screen.findByText(/ffmpeg\.exe missing/i)).toBeInTheDocument();
  });

  it("surfaces last_enum_error in a warning banner", async () => {
    (ListCameras as any).mockResolvedValue({
      cameras: [],
      ffmpeg_ok: true,
      last_enum_error: "streamer: ffmpeg path unset",
    });
    render(<CamerasTab />);
    expect(await screen.findByText(/Enumeration error/i)).toBeInTheDocument();
    expect(await screen.findByText(/streamer: ffmpeg path unset/i)).toBeInTheDocument();
  });

  it("runs Diagnose and shows the raw output", async () => {
    (ListCameras as any).mockResolvedValue({ cameras: [], ffmpeg_ok: true });
    (DiagnoseCameras as any).mockResolvedValue({
      ffmpeg_path: "C:\\\\SerialHop\\\\ffmpeg.exe",
      binary_exists: true,
      version_line: "ffmpeg version 7.1.1-essentials_build",
      list_devices_raw: "[dshow @ 0x1] DirectShow video devices\n[dshow @ 0x1] (no devices listed)",
    });
    render(<CamerasTab />);
    const diagnoseBtn = await screen.findByRole("button", { name: /diagnose/i });
    fireEvent.click(diagnoseBtn);
    await waitFor(() => expect(DiagnoseCameras).toHaveBeenCalled());
    expect(await screen.findByText(/ffmpeg version 7\.1\.1/)).toBeInTheDocument();
    expect(await screen.findByText(/DirectShow video devices/)).toBeInTheDocument();
  });
});
