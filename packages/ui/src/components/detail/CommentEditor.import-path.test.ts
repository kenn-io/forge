import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

const currentDir = path.dirname(fileURLToPath(import.meta.url));
const commentEditorPath = path.join(currentDir, "CommentEditor.svelte");

describe("CommentEditor import paths", () => {
  it("avoids the tiptap re-export path that some editors fail to resolve", () => {
    const source = readFileSync(commentEditorPath, "utf8");

    expect(source).not.toContain('"@tiptap/pm/state"');
  });
});
