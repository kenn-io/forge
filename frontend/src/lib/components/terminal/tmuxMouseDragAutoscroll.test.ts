import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { createTmuxMouseDragAutoscroll } from "./tmuxMouseDragAutoscroll";

const leftDown = "\x1b[<0;10;5M";
const leftDrag = "\x1b[<32;10;5M";
const leftUp = "\x1b[<0;10;5m";
const bounds = {
  left: 100,
  right: 900,
  top: 200,
  bottom: 600,
  width: 800,
  height: 400,
};

afterEach(() => {
  vi.useRealTimers();
});

describe("tmux mouse drag autoscroll", () => {
  it("sends repeated wheel-up and edge-drag reports while a tmux drag is above the terminal", async () => {
    vi.useFakeTimers();
    const send = vi.fn();
    const autoscroll = createTmuxMouseDragAutoscroll({ send });

    autoscroll.observeTerminalData(leftDown + leftDrag);
    autoscroll.updatePointer({
      clientX: 500,
      clientY: 180,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(240);

    expect(send).toHaveBeenCalledWith("\x1b[<64;41;1M\x1b[<32;41;1M");
    expect(send.mock.calls.length).toBeGreaterThanOrEqual(3);
  });

  it("uses wheel-down and the last row below the terminal", async () => {
    vi.useFakeTimers();
    const send = vi.fn();
    const autoscroll = createTmuxMouseDragAutoscroll({ send });

    autoscroll.observeTerminalData(leftDown);
    autoscroll.updatePointer({
      clientX: 899,
      clientY: 620,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(80);

    expect(send).toHaveBeenCalledWith("\x1b[<65;80;24M\x1b[<32;80;24M");
  });

  it("stops when the pointer returns inside or tmux reports button release", async () => {
    vi.useFakeTimers();
    const send = vi.fn();
    const autoscroll = createTmuxMouseDragAutoscroll({ send });

    autoscroll.observeTerminalData(leftDown);
    autoscroll.updatePointer({
      clientX: 500,
      clientY: 620,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(80);
    const afterFirstScroll = send.mock.calls.length;

    autoscroll.updatePointer({
      clientX: 500,
      clientY: 300,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(240);
    expect(send).toHaveBeenCalledTimes(afterFirstScroll);

    autoscroll.updatePointer({
      clientX: 500,
      clientY: 620,
      bounds,
      cols: 80,
      rows: 24,
    });
    autoscroll.observeTerminalData(leftUp);
    await vi.advanceTimersByTimeAsync(240);
    expect(send).toHaveBeenCalledTimes(afterFirstScroll);
  });

  it("finalizes the tmux drag when the browser reports pointer release outside the terminal", async () => {
    vi.useFakeTimers();
    const send = vi.fn();
    const autoscroll = createTmuxMouseDragAutoscroll({ send });

    autoscroll.observeTerminalData(leftDown);
    autoscroll.updatePointer({
      clientX: 500,
      clientY: 180,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(80);
    const afterFirstScroll = send.mock.calls.length;

    autoscroll.endPointerGesture();
    await vi.advanceTimersByTimeAsync(240);

    expect(send).toHaveBeenCalledTimes(afterFirstScroll + 1);
    expect(send).toHaveBeenLastCalledWith("\x1b[<0;41;1m");
  });

  it("ignores edge movement unless terminal output established a tmux left-button drag", async () => {
    vi.useFakeTimers();
    const send = vi.fn();
    const autoscroll = createTmuxMouseDragAutoscroll({ send });

    autoscroll.updatePointer({
      clientX: 500,
      clientY: 620,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(240);
    expect(send).not.toHaveBeenCalled();

    autoscroll.observeTerminalData("\x1b[<64;10;5M");
    autoscroll.updatePointer({
      clientX: 500,
      clientY: 620,
      bounds,
      cols: 80,
      rows: 24,
    });
    await vi.advanceTimersByTimeAsync(240);
    expect(send).not.toHaveBeenCalled();
  });

  it("disposal stops an active edge drag", async () => {
    vi.useFakeTimers();
    const send = vi.fn();
    const autoscroll = createTmuxMouseDragAutoscroll({ send });

    autoscroll.observeTerminalData(leftDown);
    autoscroll.updatePointer({
      clientX: 500,
      clientY: 180,
      bounds,
      cols: 80,
      rows: 24,
    });
    autoscroll.dispose();
    await vi.advanceTimersByTimeAsync(240);

    expect(send).not.toHaveBeenCalled();
  });
});
