import { render, screen, fireEvent, act } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { Help } from "./Help";

describe("Help", () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }); });
  afterEach(() => { vi.useRealTimers(); });

  test("renders ? anchor with no popover initially", () => {
    render(<Help title="T" what="W" />);
    expect(screen.getByRole("button")).toHaveTextContent("?");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("opens immediately on anchor mouseEnter", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toHaveTextContent("W");
  });

  test("closes 120ms after anchor mouseLeave", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    fireEvent.mouseLeave(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    act(() => { vi.advanceTimersByTime(119); });
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    act(() => { vi.advanceTimersByTime(2); });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("popover mouseEnter cancels pending close", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    fireEvent.mouseLeave(screen.getByRole("button"));
    fireEvent.mouseEnter(screen.getByRole("tooltip"));
    act(() => { vi.advanceTimersByTime(500); });
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
  });

  test("popover mouseLeave schedules close", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    fireEvent.mouseEnter(screen.getByRole("tooltip"));
    fireEvent.mouseLeave(screen.getByRole("tooltip"));
    act(() => { vi.advanceTimersByTime(121); });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("click makes popover sticky; mouseLeave does not close it", () => {
    render(<Help title="T" what="W" />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    fireEvent.mouseLeave(screen.getByRole("button"));
    act(() => { vi.advanceTimersByTime(500); });
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
  });

  test("Esc closes sticky popover", () => {
    render(<Help title="T" what="W" />);
    fireEvent.click(screen.getByRole("button"));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("focus opens, blur schedules close after 120ms", () => {
    render(<Help title="T" what="W" />);
    fireEvent.focus(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    fireEvent.blur(screen.getByRole("button"));
    act(() => { vi.advanceTimersByTime(121); });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("click on sticky toggles back to closed", () => {
    render(<Help title="T" what="W" />);
    const anchor = screen.getByRole("button");
    fireEvent.click(anchor);
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    fireEvent.click(anchor);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("renders defaultVal and when blocks when provided", () => {
    render(<Help title="T" what="W" defaultVal="DV" when="WH" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    expect(screen.getByText("DV")).toBeInTheDocument();
    expect(screen.getByText("WH")).toBeInTheDocument();
  });
});
