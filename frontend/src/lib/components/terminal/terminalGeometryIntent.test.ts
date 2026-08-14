import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import {
  beginTerminalGeometryIntent,
  currentTerminalGeometryIntent,
  extendTerminalGeometryIntent,
  hasTerminalGeometryIntent,
} from "./terminalGeometryIntent.js";

describe("terminal geometry intent", () => {
  afterEach(() => {
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
  });

  it("expires after the layout delivery window", () => {
    vi.useFakeTimers();

    beginTerminalGeometryIntent();

    expect(hasTerminalGeometryIntent()).toBe(true);
    vi.advanceTimersByTime(249);
    expect(hasTerminalGeometryIntent()).toBe(true);
    vi.advanceTimersByTime(1);
    expect(hasTerminalGeometryIntent()).toBe(false);
  });

  it("keeps the latest event alive through a continuing resize", () => {
    vi.useFakeTimers();

    beginTerminalGeometryIntent();
    vi.advanceTimersByTime(200);
    extendTerminalGeometryIntent();
    vi.advanceTimersByTime(50);

    expect(hasTerminalGeometryIntent()).toBe(true);
    expect(hasTerminalGeometryIntent()).toBe(true);
    vi.advanceTimersByTime(200);
    expect(hasTerminalGeometryIntent()).toBe(false);
  });

  it("keeps one generation through a gesture and advances for the next gesture", () => {
    vi.useFakeTimers();

    const first = beginTerminalGeometryIntent();
    extendTerminalGeometryIntent();

    expect(currentTerminalGeometryIntent()).toBe(first);
    expect(beginTerminalGeometryIntent()).toBe(first + 1);
  });
});
