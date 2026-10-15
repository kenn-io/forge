import { describe, expect, it } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { TreeNode } from "../../api/docs/types";
import FolderTree from "./FolderTree.svelte";

const tree: TreeNode = {
  name: "Notes",
  rel_path: "",
  is_dir: true,
  children: [
    {
      name: "guides",
      rel_path: "guides",
      is_dir: true,
      children: [{ name: "setup.md", rel_path: "guides/setup.md", is_dir: false, size: 1 }],
    },
    { name: "README.md", rel_path: "README.md", is_dir: false, size: 1 },
  ],
};

// The tree fills its parent, so give it a sized flex parent to lay out in.
// Returns the label of the focused row, which lives in the tree's shadow root.
async function renderTree(): Promise<() => string | null | undefined> {
  const { container } = await render(FolderTree, {
    props: { tree, gitEntries: [], activePath: null },
  });
  container.style.cssText = "display: flex; flex-direction: column; height: 400px;";
  const host = container.querySelector(".folder-tree");
  return () => host?.shadowRoot?.activeElement?.getAttribute("aria-label");
}

describe("folder tree context menu (browser)", () => {
  it("opens no menu on a folder and leaves the tree navigable", async () => {
    const focusedRow = await renderTree();
    const folder = page.getByRole("treeitem", { name: "guides" });
    await expect.element(folder).toBeVisible();

    await folder.click({ button: "right" });
    await expect.element(page.getByRole("menu")).not.toBeInTheDocument();
    await expect.poll(focusedRow).toBe("guides");

    // While the tree thinks a menu is open it swallows arrow keys.
    await userEvent.keyboard("{ArrowDown}");
    await expect.poll(focusedRow).toBe("README.md");
  });

  it("opens the file menu on a file", async () => {
    await renderTree();
    await page.getByRole("treeitem", { name: "README.md" }).click({ button: "right" });
    await expect.element(page.getByRole("menuitem", { name: /Rename/ })).toHaveFocus();
  });
});
