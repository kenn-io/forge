import { describe, expect, it } from "vite-plus/test";
import type { DiffFile } from "../../api/types.js";
import { categorizeDiffFile, summarizeDiffFiles } from "./diff-summary.js";

function file(path: string, additions: number, deletions: number): DiffFile {
  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions,
    deletions,
    patch: "",
    hunks: [],
  };
}

describe("diff summary categorization", () => {
  it("puts generated files into generated", () => {
    expect(
      categorizeDiffFile({
        ...file("src/api/client.ts", 1, 1),
        is_generated: true,
      }),
    ).toBe("generated");
    expect(categorizeDiffFile(file("bun.lock", 1, 1))).toBe("generated");
    expect(categorizeDiffFile(file("package-lock.json", 1, 1))).toBe("generated");
  });

  it("honors explicit non-generated API metadata before filename heuristics", () => {
    expect(
      categorizeDiffFile({
        ...file("bun.lock", 1, 1),
        is_generated: false,
      }),
    ).toBe("other");
  });

  it("puts documentation and planning paths into plans/docs", () => {
    expect(categorizeDiffFile("docs/rollout-plan.md")).toBe("plansDocs");
    expect(categorizeDiffFile("context/ui-design-system.md")).toBe("plansDocs");
    expect(categorizeDiffFile("README.md")).toBe("plansDocs");
  });

  it("does not treat broad context and plan directories as documentation", () => {
    expect(categorizeDiffFile("src/context/AuthContext.ts")).toBe("code");
    expect(categorizeDiffFile("src/plan/PricingPlan.js")).toBe("code");
  });

  it("prefers tests over code when test paths use code extensions", () => {
    expect(categorizeDiffFile("internal/server/api_test.go")).toBe("tests");
    expect(categorizeDiffFile("packages/ui/src/Button.test.ts")).toBe("tests");
    expect(categorizeDiffFile("tests/e2e/pull-pane.spec.ts")).toBe("tests");
  });

  it("summarizes added and deleted lines by category", () => {
    const summary = summarizeDiffFiles([
      file("docs/plan.md", 10, 2),
      file("bun.lock", 5, 1),
      file("internal/server/api.go", 30, 4),
      file("internal/server/api_test.go", 20, 8),
      file("mise.toml", 1, 1),
    ]);

    expect(summary.plansDocs).toEqual({ additions: 10, deletions: 2 });
    expect(summary.generated).toEqual({ additions: 5, deletions: 1 });
    expect(summary.code).toEqual({ additions: 30, deletions: 4 });
    expect(summary.tests).toEqual({ additions: 20, deletions: 8 });
    expect(summary.other).toEqual({ additions: 1, deletions: 1 });
    expect(summary.total).toEqual({ additions: 66, deletions: 16 });
  });
});
