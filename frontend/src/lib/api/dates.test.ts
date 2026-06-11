import { describe, expect, test } from "vite-plus/test";
import { localDateString, relativeTime, shortDate } from "./dates";

describe("date helpers", () => {
  test("formats calendar dates from local date parts instead of UTC conversion", () => {
    const localParts = {
      getFullYear: () => 2026,
      getMonth: () => 4,
      getDate: () => 5,
    } as Date;

    expect(localDateString(localParts)).toBe("2026-05-05");
  });
});

describe("relativeTime", () => {
  const now = new Date("2026-05-16T12:00:00Z");
  const at = (offsetSeconds: number) => new Date(now.getTime() - offsetSeconds * 1000).toISOString();
  const localISO = (year: number, month: number, day: number) => new Date(year, month - 1, day, 12).toISOString();

  test("returns now for very recent timestamps", () => {
    expect(relativeTime(at(5), now)).toBe("now");
  });

  test("formats minutes within the past hour", () => {
    expect(relativeTime(at(60 * 5), now)).toBe("5m");
  });

  test("formats hours within the past day", () => {
    expect(relativeTime(at(60 * 60 * 3), now)).toBe("3h");
  });

  test("formats days within the past week", () => {
    expect(relativeTime(at(60 * 60 * 24 * 2), now)).toBe("2d");
  });

  test("uses month and day for older dates in the same year", () => {
    expect(relativeTime(localISO(2026, 2, 9), now)).toBe("Feb 9");
  });

  test("includes two-digit year for prior years", () => {
    expect(relativeTime(localISO(2024, 12, 1), now)).toBe("Dec 1 '24");
  });

  test("returns fallback for missing or unparseable input", () => {
    expect(relativeTime(undefined, now)).toBe("-");
    expect(relativeTime("not-a-date", now)).toBe("-");
  });
});

describe("shortDate", () => {
  const now = new Date("2026-05-16T12:00:00Z");

  test("renders MMM D for dates in the current year", () => {
    expect(shortDate("2026-05-01", now)).toBe("May 1");
    expect(shortDate("2026-12-25", now)).toBe("Dec 25");
  });

  test("appends a two-digit year for prior years", () => {
    expect(shortDate("2024-03-09", now)).toBe("Mar 9 '24");
  });

  test("returns empty string for missing input", () => {
    expect(shortDate(undefined, now)).toBe("");
    expect(shortDate("", now)).toBe("");
  });
});
