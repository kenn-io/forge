import { describe, expect, it } from "vitest";
import { lineDiff } from "./chat-content.js";

describe("lineDiff", () => {
  it("trims unchanged runs to context around each change", () => {
    const before = Array.from({ length: 20 }, (_, index) => `line ${index}`).join("\n");
    const after = before.replace("line 10", "LINE 10");
    expect(lineDiff(before, after)).toEqual([
      { kind: "gap", text: "" },
      { kind: "context", text: "line 7" },
      { kind: "context", text: "line 8" },
      { kind: "context", text: "line 9" },
      { kind: "removed", text: "line 10" },
      { kind: "added", text: "LINE 10" },
      { kind: "context", text: "line 11" },
      { kind: "context", text: "line 12" },
      { kind: "context", text: "line 13" },
      { kind: "gap", text: "" },
    ]);
  });

  it("shows a new file as added lines", () => {
    expect(lineDiff(null, "first\nsecond")).toEqual([
      { kind: "added", text: "first" },
      { kind: "added", text: "second" },
    ]);
  });

  it("diffs large files without falling back to a whole-file replacement", () => {
    const before = Array.from({ length: 5000 }, (_, index) => `line ${index}`).join("\n");
    const after = before.replace("line 2500\n", "changed 2500\n");
    const lines = lineDiff(before, after);
    expect(lines.filter((line) => line.kind === "removed")).toEqual([{ kind: "removed", text: "line 2500" }]);
    expect(lines.filter((line) => line.kind === "added")).toEqual([{ kind: "added", text: "changed 2500" }]);
    expect(lines).toHaveLength(10);
  });
});
