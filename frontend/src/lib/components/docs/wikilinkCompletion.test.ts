import { describe, expect, test } from "vite-plus/test";
import { EditorState } from "@codemirror/state";
import { CompletionContext } from "@codemirror/autocomplete";
import { buildFolderIndex } from "../../api/docs/folderLinks";
import type { TreeNode } from "../../api/docs/types";
import { buildWikilinkCompletionSource } from "./wikilinkCompletion";

function file(rel_path: string): TreeNode {
  const name = rel_path.includes("/") ? rel_path.slice(rel_path.lastIndexOf("/") + 1) : rel_path;
  return { name, rel_path, is_dir: false } as TreeNode;
}

function dir(name: string, rel_path: string, children: TreeNode[]): TreeNode {
  return { name, rel_path, is_dir: true, children } as TreeNode;
}

function makeContext(text: string): CompletionContext {
  const state = EditorState.create({ doc: text });
  return new CompletionContext(state, text.length, false);
}

describe("buildWikilinkCompletionSource", () => {
  const root = dir("", "", [
    file("Alpha.md"),
    file("Beta.md"),
    file("Notes.md"),
    dir("Daily", "Daily", [file("Daily/2026-05-15.md")]),
    dir("Projects", "Projects", [file("Projects/Alpha.md"), file("Projects/Gamma.md")]),
  ]);
  const index = buildFolderIndex(root);
  const source = buildWikilinkCompletionSource(() => index);

  test("triggers on bare `[[` and lists all docs", () => {
    const result = source(makeContext("Start writing [["));
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label).sort();
    // Alpha appears twice (root + Projects). The root-level Alpha keeps
    // its extension so the resolver doesn't fall into the ambiguous
    // basename branch; the nested one drops the extension and uses its
    // path. Beta/Daily/Notes/Gamma are unique and collapse to basenames.
    expect(labels).toEqual(["2026-05-15", "Alpha.md", "Beta", "Gamma", "Notes", "Projects/Alpha"].sort());
  });

  test("filters by typed query", () => {
    const result = source(makeContext("see [[gam"));
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("Gamma");
    expect(labels).not.toContain("Beta");
  });

  test("filters nested docs by rel_path prefix even when the inserted target is the basename", () => {
    const result = source(makeContext("see [[Daily/2026"));
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("2026-05-15");
    expect(labels).not.toContain("Beta");
  });

  test("inserts basename when unique", () => {
    const result = source(makeContext("[[bet"));
    const beta = result!.options.find((o) => o.label === "Beta");
    expect(beta).toBeDefined();
    expect(typeof beta!.apply).toBe("function");
  });

  test("inserts rel_path when basename is shared across folders", () => {
    const result = source(makeContext("[[al"));
    const labels = result!.options.map((o) => o.label);
    // Both Alpha docs should appear as resolvable paths. The nested one
    // uses its rel_path without extension; the root one keeps the
    // extension so the resolver doesn't fall into the ambiguous
    // basename branch.
    expect(labels).toContain("Alpha.md");
    expect(labels).toContain("Projects/Alpha");
  });

  test("root-level ambiguous file inserts with extension so the resolver can disambiguate", () => {
    const result = source(makeContext("[[al"));
    const rootAlpha = result!.options.find((o) => o.label === "Alpha.md");
    expect(rootAlpha).toBeDefined();
    expect(typeof rootAlpha!.apply).toBe("function");
  });

  test("returns null when the line lacks `[[`", () => {
    expect(source(makeContext("plain text"))).toBeNull();
  });

  test("returns null when the folder has no markdown docs", () => {
    const empty = buildWikilinkCompletionSource(() => buildFolderIndex(null));
    expect(empty(makeContext("[["))).toBeNull();
  });

  test("ranks prefix matches before substring matches", () => {
    const result = source(makeContext("[[a"));
    const labels = result!.options.map((o) => o.label);
    // Alpha.md starts with "a", Gamma contains "a" — Alpha should rank
    // first.
    expect(labels.indexOf("Alpha.md")).toBeLessThan(labels.indexOf("Gamma"));
  });
});
