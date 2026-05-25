import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TabBar } from "./TabBar";

describe("TabBar", () => {
  it("renders all six tabs when nothing is hidden", () => {
    render(<TabBar active="status" onChange={vi.fn()} />);
    for (const label of ["Status", "Config", "Devices", "Ports", "Cameras", "Logs"]) {
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    }
  });

  it("hides the Cameras tab when hiddenTabs includes 'cameras'", () => {
    render(<TabBar active="status" onChange={vi.fn()} hiddenTabs={["cameras"]} />);
    expect(screen.queryByRole("button", { name: "Cameras" })).not.toBeInTheDocument();
    // Other tabs still present.
    expect(screen.getByRole("button", { name: "Status" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Logs" })).toBeInTheDocument();
  });

  it("supports hiding multiple tabs", () => {
    render(<TabBar active="status" onChange={vi.fn()} hiddenTabs={["cameras", "ports"]} />);
    expect(screen.queryByRole("button", { name: "Cameras" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Ports" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Devices" })).toBeInTheDocument();
  });
});
