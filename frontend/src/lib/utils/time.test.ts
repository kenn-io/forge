import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { localChatTimestamp, localDateTimeLabel, localDateLabel, parseAPITimestamp } from "./time.js";

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

  describe("localChatTimestamp", () => {
    const now = new Date(2026, 9, 27, 15, 30);

    it("shows only the time for a message from today", () => {
      expect(localChatTimestamp(new Date(2026, 9, 27, 0, 5), now, "en-US")).toEqual({ time: "12:05 AM" });
    });

    it("adds the short date for a message from an earlier day", () => {
      expect(localChatTimestamp(new Date(2026, 9, 26, 23, 59), now, "en-US")).toEqual({
        date: "Oct 26",
        time: "11:59 PM",
      });
      expect(localChatTimestamp(new Date(2026, 9, 25, 14, 9), now, "en-US")).toEqual({
        date: "Oct 25",
        time: "2:09 PM",
      });
    });

    it("adds the year when the message is from another year", () => {
      expect(localChatTimestamp(new Date(2025, 11, 31, 9, 0), now, "en-US")).toEqual({
        date: "Dec 31, 2025",
        time: "9:00 AM",
      });
    });
  });
});
