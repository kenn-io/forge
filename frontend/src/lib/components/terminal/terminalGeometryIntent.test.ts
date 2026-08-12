import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { hasTerminalGeometryIntent, markTerminalGeometryIntent } from "./terminalGeometryIntent.js";

describe("terminal geometry intent", () => {
  afterEach(() => {
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
  });

  it("expires after the layout delivery window", () => {
    vi.useFakeTimers();

    markTerminalGeometryIntent();

    expect(hasTerminalGeometryIntent()).toBe(true);
    vi.advanceTimersByTime(249);
    expect(hasTerminalGeometryIntent()).toBe(true);
    vi.advanceTimersByTime(1);
    expect(hasTerminalGeometryIntent()).toBe(false);
  });

  it("keeps the latest event alive through a continuing resize", () => {
    vi.useFakeTimers();

    markTerminalGeometryIntent();
    vi.advanceTimersByTime(200);
    markTerminalGeometryIntent();
    vi.advanceTimersByTime(50);

    expect(hasTerminalGeometryIntent()).toBe(true);
    expect(hasTerminalGeometryIntent()).toBe(true);
    vi.advanceTimersByTime(200);
    expect(hasTerminalGeometryIntent()).toBe(false);
  });
});
