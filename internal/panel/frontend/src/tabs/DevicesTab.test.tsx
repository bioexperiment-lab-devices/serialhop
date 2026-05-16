import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { DevicesTab } from "./DevicesTab";
import * as App from "../wails/go/main/App";

vi.mock("../wails/go/main/App", () => ({
  GetDevices: vi.fn(),
  Discover: vi.fn(),
  DisconnectAll: vi.fn(),
  DisconnectPort: vi.fn(),
}));

describe("DevicesTab", () => {
  beforeEach(() => {
    vi.mocked(App.GetDevices).mockReset();
    vi.mocked(App.Discover).mockReset();
    vi.mocked(App.DisconnectAll).mockReset();
    vi.mocked(App.DisconnectPort).mockReset();
  });

  // Regression test for the silent UI-blank bug: when the Go side ever
  // hands the tab a null `devices` (e.g. an old service version, or a
  // future refactor that re-introduces nil-slice marshalling), the tab
  // must render the unreachable-state banner instead of throwing during
  // render. A throw here used to take down the whole window because
  // there was no surrounding ErrorBoundary.
  it("does not throw when GetDevices returns null devices", async () => {
    vi.mocked(App.GetDevices).mockResolvedValueOnce({
      devices: null,
      discovered_at: null,
      status: { reachable: false, reason: "unreachable" },
    });
    render(<DevicesTab />);
    await waitFor(() =>
      expect(screen.getByText(/can't reach the local service/i)).toBeInTheDocument(),
    );
  });

  it("renders the empty-state banner when devices=[] and reachable", async () => {
    vi.mocked(App.GetDevices).mockResolvedValueOnce({
      devices: [],
      discovered_at: null,
      status: { reachable: true },
    });
    render(<DevicesTab />);
    await waitFor(() =>
      expect(screen.getByText(/no devices yet/i)).toBeInTheDocument(),
    );
  });

  it("renders rows when devices are present", async () => {
    vi.mocked(App.GetDevices).mockResolvedValueOnce({
      devices: [{ id: "d1", type: "stim", type_code: 1, port: "COM3" }],
      discovered_at: new Date().toISOString(),
      status: { reachable: true },
    });
    render(<DevicesTab />);
    await waitFor(() => expect(screen.getByText("d1")).toBeInTheDocument());
    expect(screen.getByText("stim")).toBeInTheDocument();
    expect(screen.getByText("COM3")).toBeInTheDocument();
  });

  it("renders a Disconnect button per device row", async () => {
    vi.mocked(App.GetDevices).mockResolvedValueOnce({
      devices: [
        { id: "pump_1", type: "pump", type_code: 10, port: "COM3" },
        { id: "valve_1", type: "valve", type_code: 30, port: "COM4" },
      ],
      discovered_at: new Date().toISOString(),
      status: { reachable: true },
    });
    render(<DevicesTab />);
    await waitFor(() => expect(screen.getByText("pump_1")).toBeInTheDocument());
    expect(screen.getByLabelText("Disconnect pump_1")).toBeInTheDocument();
    expect(screen.getByLabelText("Disconnect valve_1")).toBeInTheDocument();
  });

  it("invokes DisconnectPort with the row's port and refreshes", async () => {
    vi.mocked(App.GetDevices)
      .mockResolvedValueOnce({
        devices: [{ id: "pump_1", type: "pump", type_code: 10, port: "COM3" }],
        discovered_at: new Date().toISOString(),
        status: { reachable: true },
      })
      .mockResolvedValueOnce({
        devices: [],
        discovered_at: new Date().toISOString(),
        status: { reachable: true },
      });
    vi.mocked(App.DisconnectPort).mockResolvedValueOnce({
      released: 1,
      status: { reachable: true },
    });

    render(<DevicesTab />);
    await waitFor(() => expect(screen.getByText("pump_1")).toBeInTheDocument());

    fireEvent.click(screen.getByLabelText("Disconnect pump_1"));

    await waitFor(() => expect(App.DisconnectPort).toHaveBeenCalledWith("COM3"));
    // refresh() runs after DisconnectPort — GetDevices called twice (initial + post-action).
    await waitFor(() => expect(App.GetDevices).toHaveBeenCalledTimes(2));
  });

});
