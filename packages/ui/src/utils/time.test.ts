import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { localDateTimeLabel, localDateLabel, parseAPITimestamp, timeAgo } from "./time.js";

describe("time helpers", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-11T12:00:00Z"));
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("parses UTC API timestamps as absolute instants", () => {
    expect(parseAPITimestamp("2026-04-11T08:00:00-04:00").toISOString()).toBe("2026-04-11T12:00:00.000Z");
  });

  it("uses local formatting only in the presentation helper", () => {
    const spy = vi.spyOn(Date.prototype, "toLocaleDateString");

    localDateLabel("2026-04-11T12:00:00Z");

    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("uses local date-time formatting only in the presentation helper", () => {
    const spy = vi.spyOn(Date.prototype, "toLocaleString").mockReturnValue("Apr 11, 2026, 8:00 AM");

    expect(localDateTimeLabel("2026-04-11T12:00:00Z")).toBe("Apr 11, 2026, 8:00 AM");
    expect(spy).toHaveBeenCalledWith(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
    });
  });

  it("computes relative time from canonical instants", () => {
    expect(timeAgo("2026-04-11T11:30:00Z")).toBe("30m ago");
  });
});
