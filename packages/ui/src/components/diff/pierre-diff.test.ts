import { describe, expect, it } from "vite-plus/test";
import type { DiffFile } from "../../api/types.js";
import {
  diffFileWithPatch,
  parsePierreFileDiff,
  parsePierreFileDiffWithContents,
  patchPath,
  pierreFileContents,
} from "./pierre-diff.js";

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
    hunks: [
      {
        old_start: 1,
        old_count: 2,
        new_start: 1,
        new_count: 2,
        lines: [
          {
            type: "context",
            content: "line 1",
            old_num: 1,
            new_num: 1,
          },
          { type: "delete", content: "old line", old_num: 2 },
          { type: "add", content: "new line", new_num: 2 },
        ],
      },
    ],
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
    hunks: [
      {
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
      },
    ],
  };
}

function makeGitQuotedPathFile(patchBody = "-old line\n+new line"): DiffFile {
  const path = 'src/a"b.go';
  const patch = `diff --git "a/src/a\\"b.go" "b/src/a\\"b.go"
--- "a/src/a\\"b.go"
+++ "b/src/a\\"b.go"
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
    hunks: [
      {
        old_start: 1,
        old_count: 2,
        new_start: 1,
        new_count: 2,
        lines: [
          {
            type: "context",
            content: "line 1",
            old_num: 1,
            new_num: 1,
          },
          { type: "delete", content: "old line", old_num: 2 },
          { type: "add", content: "new line", new_num: 2 },
        ],
      },
    ],
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

  it("uses distinct cache keys for sparse and full context contents", () => {
    const file = makeFile("src/foo.ts", "-old line\n+new line");
    const parsed = parsePierreFileDiff(file, {
      enableDemandContextExpansion: true,
    });
    const sparseOld = pierreFileContents("src/foo.ts", "line 1\nold line\n", "sparse-old");
    const fullOld = pierreFileContents("src/foo.ts", "line 1\nold line\n", "full-old");
    const full = parsePierreFileDiffWithContents(file, {
      oldFile: fullOld,
      newFile: pierreFileContents("src/foo.ts", "line 1\nnew line\n", "full-new"),
    });

    expect(parsed).toBeDefined();
    expect(full).toBeDefined();
    expect(sparseOld.cacheKey).toBeDefined();
    expect(fullOld.cacheKey).toBeDefined();
    expect(fullOld.cacheKey).not.toBe(sparseOld.cacheKey);
  });

  it("falls back to patch-only parsing for huge sparse line ranges", () => {
    const parsed = parsePierreFileDiff(makeLargeLineFile(), {
      enableDemandContextExpansion: true,
    });

    expect(parsed).toBeDefined();
    expect((parsed as { isPartial?: boolean } | undefined)?.isPartial).toBe(true);
  });

  it("handles nullable hunk payloads when sparse context expansion is enabled", () => {
    const file = {
      ...makeFile("src/foo.ts", "-old line\n+new line"),
      hunks: null as unknown as DiffFile["hunks"],
    };

    const parsed = parsePierreFileDiff(file, {
      enableDemandContextExpansion: true,
    });

    expect(parsed).toBeDefined();
  });

  it("synthesizes patch text from structured hunks", () => {
    const file = {
      ...makeFile("src/foo.ts", "-old line\n+new line"),
      patch: "",
    };

    const patched = diffFileWithPatch(file);
    const parsed = parsePierreFileDiff(file);

    expect(patched.patch).toContain("@@ -1,2 +1,2 @@");
    expect(parsed).toBeDefined();
    expect(parsed?.deletionLines).toContain("old line\n");
    expect(parsed?.additionLines).toContain("new line\n");
  });

  it("quotes synthetic patch paths that can be parsed as patch control text", () => {
    expect(patchPath("a/src/normal.ts")).toBe("a/src/normal.ts");
    expect(patchPath("a/src/evil\n--- a/forged")).toBe('"a/src/evil\\n--- a/forged"');
    expect(patchPath('a/src/a"b.ts')).toBe('"a/src/a\\"b.ts"');
    expect(patchPath("a/src/ctl\u007f.ts")).toBe('"a/src/ctl\\u007f.ts"');
    expect(patchPath("/dev/null")).toBe("/dev/null");
  });

  it("quotes generated hunk-only patch paths", () => {
    const file = {
      ...makeFile("src/evil\n--- a/forged.ts", "-old line\n+new line"),
      patch: "",
    };

    const patched = diffFileWithPatch(file);

    expect(patched.patch).toContain('diff --git "a/src/evil\\n--- a/forged.ts" "b/src/evil\\n--- a/forged.ts"');
    expect(patched.patch).not.toContain("\n--- a/forged.ts");
  });

  it("falls back to safe Pierre headers for quoted synthetic paths", () => {
    const parsed = parsePierreFileDiff(makeGitQuotedPathFile());

    expect(parsed).toBeDefined();
    expect(parsed?.hunks).toHaveLength(1);
    expect(parsed?.deletionLines).toContain("old line\n");
    expect(parsed?.additionLines).toContain("new line\n");
  });

  it("falls back to safe Pierre headers when sparse context parsing sees quoted paths", () => {
    const parsed = parsePierreFileDiff(makeGitQuotedPathFile(), {
      enableDemandContextExpansion: true,
    });

    expect(parsed).toBeDefined();
    expect((parsed as { isPartial?: boolean } | undefined)?.isPartial).toBe(false);
  });

  it("preserves hunk body lines that look like patch headers during fallback", () => {
    const parsed = parsePierreFileDiff(makeGitQuotedPathFile("--- body deletion\n+++ body addition"));

    expect(parsed).toBeDefined();
    expect(parsed?.deletionLines).toContain("-- body deletion\n");
    expect(parsed?.additionLines).toContain("++ body addition\n");
  });
});
