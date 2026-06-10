import { describe, expect, test } from "vite-plus/test";
import { flattenTreePaths } from "./folderTreePaths";
import type { TreeNode } from "../../api/docs/types";

describe("flattenTreePaths", () => {
  test("returns an empty list for null", () => {
    expect(flattenTreePaths(null)).toEqual([]);
  });

  test("walks nested directories and emits sorted file paths", () => {
    const root: TreeNode = {
      name: "folder",
      rel_path: "",
      is_dir: true,
      children: [
        {
          name: "Projects",
          rel_path: "Projects",
          is_dir: true,
          children: [
            { name: "b.md", rel_path: "Projects/b.md", is_dir: false, size: 1 },
            { name: "a.md", rel_path: "Projects/a.md", is_dir: false, size: 1 },
          ],
        },
        { name: "README.md", rel_path: "README.md", is_dir: false, size: 1 },
      ],
    };
    expect(flattenTreePaths(root)).toEqual(["Projects/a.md", "Projects/b.md", "README.md"]);
  });

  test("ignores entries without a rel_path", () => {
    const root: TreeNode = {
      name: "folder",
      rel_path: "",
      is_dir: true,
      children: [{ name: "", rel_path: "", is_dir: false }],
    };
    expect(flattenTreePaths(root)).toEqual([]);
  });
});
