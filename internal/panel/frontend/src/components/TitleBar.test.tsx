import { render, screen, fireEvent } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { TitleBar } from "./TitleBar";

describe("TitleBar", () => {
  const minimise = vi.fn();
  const quit = vi.fn();

  beforeEach(() => {
    minimise.mockReset();
    quit.mockReset();
    (window as unknown as { runtime: unknown }).runtime = {
      EventsOn: () => () => {},
      EventsOff: () => {},
      EventsEmit: () => {},
      WindowMinimise: minimise,
      Quit: quit,
    };
  });

  afterEach(() => {
    delete (window as unknown as { runtime?: unknown }).runtime;
  });

  test("renders app name and version chip", () => {
    render(<TitleBar version="1.2.3" />);
    expect(screen.getByText("SerialHop")).toBeInTheDocument();
    expect(screen.getByText("v1.2.3")).toBeInTheDocument();
  });

  test("title region declares itself draggable for Wails", () => {
    const { container } = render(<TitleBar version="1.2.3" />);
    const drag = container.querySelector(".shp-titlebar__drag") as HTMLElement;
    expect(drag).not.toBeNull();
    expect(drag.style.getPropertyValue("--wails-draggable")).toBe("drag");
  });

  test("minimise button calls runtime.WindowMinimise", () => {
    render(<TitleBar version="1.2.3" />);
    fireEvent.click(screen.getByRole("button", { name: /minimi[sz]e/i }));
    expect(minimise).toHaveBeenCalledTimes(1);
    expect(quit).not.toHaveBeenCalled();
  });

  test("close button calls runtime.Quit", () => {
    render(<TitleBar version="1.2.3" />);
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    expect(quit).toHaveBeenCalledTimes(1);
    expect(minimise).not.toHaveBeenCalled();
  });
});
