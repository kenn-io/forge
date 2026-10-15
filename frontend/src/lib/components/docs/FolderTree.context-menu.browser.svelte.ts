import { describe, expect, it, vi } from "vite-plus/test";
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
// Returns the element focused inside the tree's shadow root.
async function renderTree(
  onFileRename?: (from: string, to: string) => void,
): Promise<() => Element | null | undefined> {
  const { container } = await render(FolderTree, {
    props: { tree, gitEntries: [], activePath: null, onFileRename },
  });
  container.style.cssText = "display: flex; flex-direction: column; height: 400px;";
  const host = container.querySelector(".folder-tree");
  return () => host?.shadowRoot?.activeElement;
}

describe("folder tree context menu (browser)", () => {
  it("opens no menu on a folder and leaves the tree navigable", async () => {
    const focused = await renderTree();
    const focusedRow = () => focused()?.getAttribute("aria-label");
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

  it("hands focus to the inline rename field and renames the file", async () => {
    const onFileRename = vi.fn();
    const focused = await renderTree(onFileRename);
    await page.getByRole("treeitem", { name: "README.md" }).click({ button: "right" });
    await page.getByRole("menuitem", { name: /Rename/ }).click();

    await expect.poll(() => focused()?.tagName).toBe("INPUT");
    // The menu finishes closing on a later frame; focus must still be in the field.
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    expect(focused()?.tagName).toBe("INPUT");

    const input = focused() as HTMLInputElement;
    input.select();
    await userEvent.keyboard("guide.md{Enter}");
    await expect.poll(() => onFileRename.mock.calls).toEqual([["README.md", "guide.md"]]);
  });
});
