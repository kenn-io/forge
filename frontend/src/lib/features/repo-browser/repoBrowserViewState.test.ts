import { describe, expect, it } from "vite-plus/test";
import {
  chooseRepoBrowserInitialPath,
  formatRepoBrowserCommitAge,
  formatRepoBrowserCommitDate,
  formatRepoBrowserFileSize,
  isRepoBrowserMarkdownPath,
} from "./repoBrowserViewState.js";

describe("repo browser view state", () => {
  it("prefers a root README when no path is selected", () => {
    expect(
      chooseRepoBrowserInitialPath([
        { path: "src/main.go", type: "blob" },
        { path: "docs/README.md", type: "blob" },
        { path: "README.md", type: "blob" },
      ]),
    ).toBe("README.md");
  });

  it("falls back to the first tracked file when no README exists", () => {
    expect(
      chooseRepoBrowserInitialPath([
        { path: "cmd/", type: "tree" },
        { path: "cmd/main.go", type: "blob" },
      ]),
    ).toBe("cmd/main.go");
  });

  it("recognizes Markdown files for preview mode", () => {
    expect(isRepoBrowserMarkdownPath("README.md")).toBe(true);
    expect(isRepoBrowserMarkdownPath("docs/design.MDX")).toBe(true);
    expect(isRepoBrowserMarkdownPath("src/main.go")).toBe(false);
  });

  it("formats compact file sizes", () => {
    expect(formatRepoBrowserFileSize(0)).toBe("0 B");
    expect(formatRepoBrowserFileSize(512)).toBe("512 B");
    expect(formatRepoBrowserFileSize(1536)).toBe("1.5 KB");
    expect(formatRepoBrowserFileSize(1024 * 1024 * 2.25)).toBe("2.3 MB");
  });

  it("formats commit timestamps as stable local dates", () => {
    expect(formatRepoBrowserCommitDate("2026-06-23T14:30:00Z")).toBe("Jun 23, 2026");
    expect(formatRepoBrowserCommitDate("not-a-date")).toBe("");
  });

  it("formats commit timestamps as compact file tree ages", () => {
    const now = new Date("2026-06-25T12:00:00Z");

    expect(formatRepoBrowserCommitAge("2026-06-25T11:45:00Z", now)).toBe("15m");
    expect(formatRepoBrowserCommitAge("2026-06-25T09:00:00Z", now)).toBe("3h");
    expect(formatRepoBrowserCommitAge("2026-06-22T12:00:00Z", now)).toBe("3d");
    expect(formatRepoBrowserCommitAge("2026-05-28T12:00:00Z", now)).toBe("4w");
    expect(formatRepoBrowserCommitAge("2025-06-25T12:00:00Z", now)).toBe("12mo");
    expect(formatRepoBrowserCommitAge("not-a-date", now)).toBe("");
  });
});
