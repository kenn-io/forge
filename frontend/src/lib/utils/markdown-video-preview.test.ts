import { describe, expect, it } from "vite-plus/test";
import { formatDuration } from "./markdown-video-preview.js";

describe("formatDuration", () => {
  it.each([
    [3, "0:03"],
    [59.6, "1:00"],
    [754, "12:34"],
    [3725, "1:02:05"],
  ])("shows %s seconds as %s", (seconds, shown) => {
    expect(formatDuration(seconds)).toBe(shown);
  });
});
