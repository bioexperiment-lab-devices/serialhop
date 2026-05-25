import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { CamerasTab } from "./CamerasTab";

vi.mock("../wails/go/main/App", () => ({
  ListCameras: vi.fn(),
  SetCameraArmed: vi.fn(),
  RefreshCameras: vi.fn(),
}));

vi.mock("../wailsEvents", () => ({
  useWailsEvent: () => () => {},
}));

import { ListCameras, SetCameraArmed } from "../wails/go/main/App";

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
});
