import { describe, expect, test } from "vite-plus/test";
import type { TreeNode } from "./types";
import {
  buildFolderIndex,
  joinFolderPath,
  parseWikilink,
  resolveRelativeDocPath,
  resolveWikilink,
} from "./folderLinks";

const tree: TreeNode = {
  name: "Notes",
  rel_path: "",
  is_dir: true,
  children: [
    {
      name: "Projects",
      rel_path: "Projects",
      is_dir: true,
      children: [
        { name: "alpha.md", rel_path: "Projects/alpha.md", is_dir: false, size: 1 },
        { name: "obsidian.md", rel_path: "Projects/obsidian.md", is_dir: false, size: 1 },
      ],
    },
    {
      name: "Daily",
      rel_path: "Daily",
      is_dir: true,
      children: [{ name: "alpha.md", rel_path: "Daily/alpha.md", is_dir: false, size: 1 }],
    },
    { name: "README.md", rel_path: "README.md", is_dir: false, size: 1 },
  ],
};

describe("buildFolderIndex", () => {
  test("indexes by exact path and by basename", () => {
    const index = buildFolderIndex(tree);
    expect(index.byPath.get("projects/alpha.md")).toBe("Projects/alpha.md");
    expect(index.byPath.get("projects/alpha")).toBe("Projects/alpha.md");
    expect(index.byPath.get("readme.md")).toBe("README.md");
    expect(index.byBasename.get("alpha")).toEqual(expect.arrayContaining(["Projects/alpha.md", "Daily/alpha.md"]));
  });
});

describe("parseWikilink", () => {
  test("splits alias and anchor", () => {
    expect(parseWikilink("Foo")).toEqual({ raw: "Foo", target: "Foo" });
    expect(parseWikilink("Foo|Display Text")).toEqual({
      raw: "Foo|Display Text",
      target: "Foo",
      alias: "Display Text",
    });
    expect(parseWikilink("Foo#section-1")).toEqual({
      raw: "Foo#section-1",
      target: "Foo",
      anchor: "section-1",
    });
    expect(parseWikilink("folder/Foo#sec|Custom")).toEqual({
      raw: "folder/Foo#sec|Custom",
      target: "folder/Foo",
      anchor: "sec",
      alias: "Custom",
    });
  });
});

describe("resolveWikilink", () => {
  const index = buildFolderIndex(tree);

  test("unambiguous basename resolves to single path", () => {
    expect(resolveWikilink("obsidian", index)).toEqual({
      kind: "resolved",
      path: "Projects/obsidian.md",
    });
  });

  test("ambiguous basename returns all candidates", () => {
    expect(resolveWikilink("alpha", index)).toEqual({
      kind: "ambiguous",
      candidates: expect.arrayContaining(["Projects/alpha.md", "Daily/alpha.md"]),
    });
  });

  test("path-qualified target resolves exactly even when basename is ambiguous", () => {
    expect(resolveWikilink("Projects/alpha", index)).toEqual({
      kind: "resolved",
      path: "Projects/alpha.md",
    });
  });

  test("missing target is reported", () => {
    expect(resolveWikilink("nonexistent", index)).toEqual({ kind: "missing" });
  });
});

describe("resolveRelativeDocPath", () => {
  test("joins relative segments against the current doc's directory", () => {
    expect(resolveRelativeDocPath("Projects/alpha.md", "../README.md")).toBe("README.md");
    expect(resolveRelativeDocPath("Projects/alpha.md", "./obsidian.md")).toBe("Projects/obsidian.md");
    expect(resolveRelativeDocPath("README.md", "Projects/obsidian.md")).toBe("Projects/obsidian.md");
  });

  test("returns null for non-markdown hrefs", () => {
    expect(resolveRelativeDocPath("Projects/alpha.md", "../assets/logo.png")).toBeNull();
  });

  test("returns null when traversal escapes the folder root", () => {
    expect(resolveRelativeDocPath("README.md", "../escape.md")).toBeNull();
  });

  test("resolves leading slash paths from the docs folder root", () => {
    expect(resolveRelativeDocPath("Projects/alpha.md", "/README.md")).toBe("README.md");
    expect(joinFolderPath("Projects/alpha.md", "/assets/logo.png")).toBe("assets/logo.png");
  });
});
