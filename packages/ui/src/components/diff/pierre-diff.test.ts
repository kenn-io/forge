import { describe, expect, it } from "vitest";
import type { DiffFile } from "../../api/types.js";
import { parsePierreFileDiff } from "./pierre-diff.js";

function makeFile(path: string, patchBody: string): DiffFile {
  const patch = `diff --git a/${path} b/${path}
--- a/${path}
+++ b/${path}
@@ -1,2 +1,2 @@
 line 1
${patchBody}
`;

  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 1,
    patch,
    hunks: [{
      old_start: 1,
      old_count: 2,
      new_start: 1,
      new_count: 2,
      lines: [
        { type: "context", content: "line 1", old_num: 1, new_num: 1 },
        { type: "delete", content: "old line", old_num: 2 },
        { type: "add", content: "new line", new_num: 2 },
      ],
    }],
  };
}

function makeLargeLineFile(): DiffFile {
  const path = "src/large.ts";
  const patch = `diff --git a/${path} b/${path}
--- a/${path}
+++ b/${path}
@@ -1000000,1 +1000000,2 @@
 far line
+new far line
`;

  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 0,
    patch,
    hunks: [{
      old_start: 1_000_000,
      old_count: 1,
      new_start: 1_000_000,
      new_count: 2,
      lines: [
        {
          type: "context",
          content: "far line",
          old_num: 1_000_000,
          new_num: 1_000_000,
        },
        {
          type: "add",
          content: "new far line",
          new_num: 1_000_001,
        },
      ],
    }],
  };
}

describe("Pierre diff parsing", () => {
  it("does not assign reusable cache keys to untrusted patch input", () => {
    const first = parsePierreFileDiff(makeFile("src/foo.ts", "-old line\n+new line"));
    const second = parsePierreFileDiff(makeFile("src/foo.ts", "-other line\n+changed line"));

    expect(first).toBeDefined();
    expect(second).toBeDefined();
    expect((first as { cacheKey?: string } | undefined)?.cacheKey).toBeUndefined();
    expect((second as { cacheKey?: string } | undefined)?.cacheKey).toBeUndefined();
  });

  it("omits cache keys when sparse context files are supplied for expansion", () => {
    const parsed = parsePierreFileDiff(makeFile("src/foo.ts", "-old line\n+new line"), {
      enableDemandContextExpansion: true,
    });

    expect(parsed).toBeDefined();
    expect((parsed as { cacheKey?: string } | undefined)?.cacheKey).toBeUndefined();
  });

  it("falls back to patch-only parsing for huge sparse line ranges", () => {
    const parsed = parsePierreFileDiff(makeLargeLineFile(), {
      enableDemandContextExpansion: true,
    });

    expect(parsed).toBeDefined();
    expect((parsed as { isPartial?: boolean } | undefined)?.isPartial).toBe(true);
  });
});
