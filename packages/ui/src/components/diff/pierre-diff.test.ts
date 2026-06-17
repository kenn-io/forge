import { describe, expect, it, vi } from "vite-plus/test";
import type { DiffFile } from "../../api/types.js";
import {
  diffFileWithPatch,
  parsePierreFileDiff,
  parsePierreFileDiffWithContents,
  patchPath,
  pierreFileContents,
  sparseContextMayDistortSyntax,
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

function makeSparseTemplateGapFile(): DiffFile {
  const path = "src/example.test.ts";
  return {
    ...makeFile(path, "-old line\n+new line"),
    additions: 5,
    deletions: 0,
    patch: `diff --git a/${path} b/${path}
--- a/${path}
+++ b/${path}
@@ -10,3 +10,4 @@ function render() {
 const html = \`
+  <span>new</span>
   <div>
@@ -80,2 +81,5 @@ afterRender();
+vi.doMock("./worker", () => ({
+  run: () => undefined,
+}));
 function makeFile() {}
`,
    hunks: [
      {
        old_start: 10,
        old_count: 3,
        new_start: 10,
        new_count: 4,
        section: "function render() {",
        lines: [
          { type: "context", content: "const html = `", old_num: 10, new_num: 10 },
          { type: "add", content: "  <span>new</span>", new_num: 11 },
          { type: "context", content: "  <div>", old_num: 11, new_num: 12 },
        ],
      },
      {
        old_start: 80,
        old_count: 2,
        new_start: 81,
        new_count: 5,
        section: "afterRender();",
        lines: [
          { type: "add", content: 'vi.doMock("./worker", () => ({', new_num: 81 },
          { type: "add", content: "  run: () => undefined,", new_num: 82 },
          { type: "add", content: "}));", new_num: 83 },
          { type: "context", content: "function makeFile() {}", old_num: 80, new_num: 84 },
        ],
      },
    ],
  };
}

function makeSparseTemplateGapFileWithBothSidesOpen(): DiffFile {
  const path = "src/example.test.ts";
  return {
    ...makeFile(path, "-old line\n+new line"),
    additions: 4,
    deletions: 1,
    patch: `diff --git a/${path} b/${path}
--- a/${path}
+++ b/${path}
@@ -10,2 +10,2 @@ function render() {
-const oldHtml = \`
+const newHtml = \`
   <div>
@@ -80,2 +80,4 @@ afterRender();
+vi.doMock("./worker", () => ({
+  run: () => undefined,
+}));
 function makeFile() {}
`,
    hunks: [
      {
        old_start: 10,
        old_count: 2,
        new_start: 10,
        new_count: 2,
        section: "function render() {",
        lines: [
          { type: "delete", content: "const oldHtml = `", old_num: 10 },
          { type: "add", content: "const newHtml = `", new_num: 10 },
          { type: "context", content: "  <div>", old_num: 11, new_num: 11 },
        ],
      },
      {
        old_start: 80,
        old_count: 2,
        new_start: 80,
        new_count: 4,
        section: "afterRender();",
        lines: [
          { type: "add", content: 'vi.doMock("./worker", () => ({', new_num: 80 },
          { type: "add", content: "  run: () => undefined,", new_num: 81 },
          { type: "add", content: "}));", new_num: 82 },
          { type: "context", content: "function makeFile() {}", old_num: 80, new_num: 83 },
        ],
      },
    ],
  };
}

describe("Pierre diff parsing", () => {
  it("uses distinct cache keys for different patch contents", () => {
    const first = parsePierreFileDiff(makeFile("src/foo.ts", "-old line\n+new line"));
    const second = parsePierreFileDiff(makeFile("src/foo.ts", "-other line\n+changed line"));

    expect(first).toBeDefined();
    expect(second).toBeDefined();
    expect(first?.cacheKey).toBeDefined();
    expect(second?.cacheKey).toBeDefined();
    expect(first?.cacheKey).not.toBe(second?.cacheKey);
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
    expect(parsed?.cacheKey).toBeDefined();
    expect(full?.cacheKey).toBeDefined();
    expect(full?.cacheKey).not.toBe(parsed?.cacheKey);
  });

  it("keeps Pierre diff debug logging disabled by default", () => {
    window.localStorage.removeItem("middleman:debug:diff");
    const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
    try {
      parsePierreFileDiff(makeFile("src/foo.ts", "-old line\n+new line"), {
        enableDemandContextExpansion: true,
      });

      expect(debug).not.toHaveBeenCalled();
    } finally {
      debug.mockRestore();
    }
  });

  it("emits Pierre diff debug logging when explicitly enabled", () => {
    window.localStorage.setItem("middleman:debug:diff", "1");
    const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
    try {
      parsePierreFileDiff(makeFile("src/foo.ts", "-old line\n+new line"), {
        enableDemandContextExpansion: true,
      });

      expect(debug).toHaveBeenCalledWith(
        "[middleman:diff]",
        "parse sparse context diff",
        expect.objectContaining({
          kind: "sparse",
          path: "src/foo.ts",
        }),
      );
    } finally {
      window.localStorage.removeItem("middleman:debug:diff");
      debug.mockRestore();
    }
  });

  it("renders partial hunk payloads whose headers keep provider line counts", () => {
    const file = makeFile("src/foo.ts", "-old line\n+new line");
    file.patch = file.patch.replace("@@ -1,2 +1,2 @@", "@@ -1,3 +1,5 @@");
    file.hunks[0]!.old_count = 3;
    file.hunks[0]!.new_count = 5;

    const parsed = parsePierreFileDiff(file);
    const full = parsePierreFileDiffWithContents(file, {
      oldFile: pierreFileContents("src/foo.ts", "line 1\nold line\n", "full-old"),
      newFile: pierreFileContents("src/foo.ts", "line 1\nnew line\n", "full-new"),
    });

    expect(parsed).toBeDefined();
    expect(parsed?.deletionLines).toContain("old line\n");
    expect(parsed?.additionLines).toContain("new line\n");
    expect(full).toBeDefined();
    expect(full?.isPartial).toBe(false);
  });

  it("falls back to patch-only parsing for huge sparse line ranges", () => {
    const parsed = parsePierreFileDiff(makeLargeLineFile(), {
      enableDemandContextExpansion: true,
    });

    expect(parsed).toBeDefined();
    expect((parsed as { isPartial?: boolean } | undefined)?.isPartial).toBe(true);
  });

  it("detects sparse context gaps that can carry syntax state", () => {
    expect(sparseContextMayDistortSyntax(makeSparseTemplateGapFile())).toBe(true);
    expect(sparseContextMayDistortSyntax(makeSparseTemplateGapFileWithBothSidesOpen())).toBe(true);
    expect(sparseContextMayDistortSyntax(makeFile("src/foo.ts", "-old line\n+new line"))).toBe(false);
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

  it("synthesizes Git new-file metadata for added files", () => {
    const file: DiffFile = {
      ...makeFile("src/foo.go", "+package main"),
      status: "added",
      old_path: "",
      deletions: 0,
      patch: "",
      hunks: [
        {
          old_start: 0,
          old_count: 0,
          new_start: 1,
          new_count: 1,
          lines: [{ type: "add", content: "package main", new_num: 1 }],
        },
      ],
    };

    const patched = diffFileWithPatch(file);
    const parsed = parsePierreFileDiff(file);

    expect(patched.patch).toContain("new file mode 100644\n");
    expect(parsed?.type).toBe("new");
  });

  it("wraps hunk-only added-file patches with Git file metadata", () => {
    const file: DiffFile = {
      ...makeFile("src/foo.go", "+package main"),
      status: "added",
      old_path: "",
      deletions: 0,
      patch: "@@ -0,0 +1,1 @@\n+package main\n",
      hunks: [
        {
          old_start: 0,
          old_count: 0,
          new_start: 1,
          new_count: 1,
          lines: [{ type: "add", content: "package main", new_num: 1 }],
        },
      ],
    };

    const patched = diffFileWithPatch(file);
    const parsed = parsePierreFileDiff(file);

    expect(patched.patch).toContain("new file mode 100644\n");
    expect(patched.patch).toContain("@@ -0,0 +1,1 @@\n+package main\n");
    expect(parsed?.type).toBe("new");
    expect(parsed?.lang).toBe("go");
    expect(parsed?.additionLines).toContain("package main\n");
    expect(parsed?.cacheKey).toBeDefined();
  });

  it("wraps hunk-only patch text when structured hunks are absent", () => {
    const file: DiffFile = {
      ...makeFile("src/foo.go", "+package main"),
      status: "added",
      old_path: "",
      deletions: 0,
      patch: "@@ -0,0 +1,1 @@\n+package main\n",
      hunks: [],
    };

    const patched = diffFileWithPatch(file);
    const parsed = parsePierreFileDiff(file);

    expect(patched.patch).toContain("new file mode 100644\n");
    expect(patched.patch).toContain("@@ -0,0 +1,1 @@\n+package main\n");
    expect(parsed?.type).toBe("new");
    expect(parsed?.additionLines).toContain("package main\n");
    expect(parsed?.cacheKey).toBeDefined();
  });

  it("preserves trailing whitespace in hunk-only patch text", () => {
    const file: DiffFile = {
      ...makeFile("src/foo.go", "+package main"),
      status: "added",
      old_path: "",
      deletions: 0,
      patch: "@@ -0,0 +1,2 @@\n+package main\n+const padded = true \t",
      hunks: [],
    };

    const parsed = parsePierreFileDiff(file);

    expect(parsed?.type).toBe("new");
    expect(parsed?.additionLines).toContain("const padded = true \t\n");
  });

  it("sets language metadata for complete added-file patches", () => {
    const path = "internal/hosted/roborev/webhook_secret_resolver.go";
    const file: DiffFile = {
      ...makeFile(path, "+package roborev"),
      status: "added",
      old_path: path,
      deletions: 0,
      patch: `diff --git a/${path} b/${path}
new file mode 100644
--- /dev/null
+++ b/${path}
@@ -0,0 +1,1 @@
+package roborev
`,
      hunks: [
        {
          old_start: 0,
          old_count: 0,
          new_start: 1,
          new_count: 1,
          lines: [{ type: "add", content: "package roborev", new_num: 1 }],
        },
      ],
    };

    const parsed = parsePierreFileDiff(file);

    expect(parsed?.type).toBe("new");
    expect(parsed?.lang).toBe("go");
    expect(parsed?.cacheKey).toBeDefined();
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
