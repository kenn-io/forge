import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { startSessionDurationReporting } from "./session-duration.js";

describe("session duration reporting", () => {
  let now: number;
  let hidden: boolean;
  let stop: (() => void) | undefined;
  const send = vi.fn();
  const advance = (ms: number) => {
    now += ms;
    vi.advanceTimersByTime(ms);
  };
  const visibility = (value: boolean) => {
    hidden = value;
    document.dispatchEvent(new Event("visibilitychange"));
  };

  beforeEach(() => {
    now = 0;
    hidden = false;
    send.mockClear();
    vi.useFakeTimers();
    vi.spyOn(performance, "now").mockImplementation(() => now);
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  });
  afterEach(() => {
    stop?.();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("sums twenty visible minutes across tab switches into one session", () => {
    stop = startSessionDurationReporting(send);
    for (let i = 0; i < 20; i++) {
      advance(60_000);
      visibility(true);
      advance(300_000);
      visibility(false);
    }
    expect(send).not.toHaveBeenCalled();
    window.dispatchEvent(new Event("pagehide"));
    expect(send.mock.calls).toEqual([["5_to_30m"]]);
  });

  it("ends a session after thirty hidden minutes and starts fresh on return", () => {
    stop = startSessionDurationReporting(send);
    advance(120_000);
    visibility(true);
    advance(1_799_999);
    expect(send).not.toHaveBeenCalled();
    advance(1);
    expect(send.mock.calls).toEqual([["1_to_5m"]]);
    visibility(false);
    advance(30_000);
    window.dispatchEvent(new Event("pagehide"));
    expect(send.mock.calls).toEqual([["1_to_5m"], ["under_1m"]]);
  });

  it("ends a session hidden past thirty minutes when its timer was suspended", () => {
    stop = startSessionDurationReporting(send);
    advance(120_000);
    visibility(true);
    now += 2_400_000;
    vi.setSystemTime(Date.now() + 2_400_000);
    expect(send).not.toHaveBeenCalled();
    visibility(false);
    expect(send.mock.calls).toEqual([["1_to_5m"]]);
    advance(240_000);
    window.dispatchEvent(new Event("pagehide"));
    expect(send.mock.calls).toEqual([["1_to_5m"], ["1_to_5m"]]);
  });

  it("drops pending time and removes listeners on cleanup", () => {
    stop = startSessionDurationReporting(send);
    advance(120_000);
    visibility(true);
    stop();
    advance(1_800_000);
    visibility(false);
    window.dispatchEvent(new Event("pageshow"));
    advance(120_000);
    window.dispatchEvent(new Event("pagehide"));
    expect(send).not.toHaveBeenCalled();
  });
});
