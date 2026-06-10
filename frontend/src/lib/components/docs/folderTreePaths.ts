import type { TreeNode } from "../../api/docs/types";

/**
 * Converts our recursive TreeNode (server contract) into the flat
 * list of file paths @pierre/trees expects. Directories are implicit
 * in path prefixes; empty directories are dropped because the docs
 * server already prunes them.
 *
 * Paths use forward slashes regardless of host OS — the server
 * already canonicalizes them.
 */
export function flattenTreePaths(root: TreeNode | null): string[] {
  if (!root) return [];
  const out: string[] = [];
  walk(root, out);
  out.sort((a, b) => a.localeCompare(b));
  return out;
}

function walk(node: TreeNode, out: string[]): void {
  if (!node.is_dir) {
    if (node.rel_path) out.push(node.rel_path);
    return;
  }
  for (const child of node.children ?? []) walk(child, out);
}
