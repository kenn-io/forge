import type { TreeNode } from "./types";

/**
 * Wikilink and intra-folder link resolution.
 *
 * Obsidian-style [[wikilink]] targets are matched against the folder tree
 * using both an exact path index and a basename index. The latter lets
 * users write bare [[Foo]] without remembering where in the tree it
 * lives. Where multiple notes share a basename, the resolver flags the
 * ambiguity so the UI can prompt the reader before navigating.
 */

export interface WikilinkTarget {
  // Raw inner text minus the brackets. Useful for fallback display.
  raw: string;
  // Target portion before "|" alias and before "#" anchor.
  target: string;
  alias?: string;
  anchor?: string;
}

export type WikilinkResolution =
  | { kind: "resolved"; path: string }
  | { kind: "ambiguous"; candidates: string[] }
  | { kind: "missing" };

export interface FolderIndex {
  byPath: Map<string, string>; // normalized path → canonical rel_path
  byBasename: Map<string, string[]>; // basename (no ext) → matching rel_paths
}

const MD_EXT = /\.(md|markdown)$/i;

export function buildFolderIndex(tree: TreeNode | null): FolderIndex {
  const byPath = new Map<string, string>();
  const byBasename = new Map<string, string[]>();
  if (!tree) return { byPath, byBasename };
  walk(tree);
  return { byPath, byBasename };

  function walk(node: TreeNode) {
    if (!node.is_dir && MD_EXT.test(node.name)) {
      const path = node.rel_path;
      const lowerPath = path.toLowerCase();
      const pathNoExt = lowerPath.replace(MD_EXT, "");
      // Index both with and without extension for forgiving matches.
      byPath.set(lowerPath, path);
      byPath.set(pathNoExt, path);
      const basename = node.name.replace(MD_EXT, "").toLowerCase();
      const existing = byBasename.get(basename) ?? [];
      existing.push(path);
      byBasename.set(basename, existing);
    }
    for (const child of node.children ?? []) walk(child);
  }
}

export function parseWikilink(inner: string): WikilinkTarget {
  let target = inner.trim();
  let alias: string | undefined;
  let anchor: string | undefined;
  const aliasIdx = target.indexOf("|");
  if (aliasIdx !== -1) {
    alias = target.slice(aliasIdx + 1).trim();
    target = target.slice(0, aliasIdx).trim();
  }
  const anchorIdx = target.indexOf("#");
  if (anchorIdx !== -1) {
    anchor = target.slice(anchorIdx + 1).trim();
    target = target.slice(0, anchorIdx).trim();
  }
  const parsed: WikilinkTarget = { raw: inner, target };
  if (alias !== undefined) parsed.alias = alias;
  if (anchor !== undefined) parsed.anchor = anchor;
  return parsed;
}

export function resolveWikilink(target: string, index: FolderIndex): WikilinkResolution {
  const lowered = target.toLowerCase();
  // Path-qualified target (contains "/" or has explicit .md extension) → exact lookup.
  if (lowered.includes("/") || MD_EXT.test(lowered)) {
    const direct = index.byPath.get(lowered) ?? index.byPath.get(lowered.replace(MD_EXT, ""));
    return direct ? { kind: "resolved", path: direct } : { kind: "missing" };
  }
  const matches = index.byBasename.get(lowered) ?? [];
  if (matches.length === 0) return { kind: "missing" };
  if (matches.length === 1) return { kind: "resolved", path: matches[0]! };
  return { kind: "ambiguous", candidates: [...matches] };
}

// Resolve a relative href like "../Projects/foo.md" or "assets/logo.png"
// against the directory the current doc lives in. Returns null when the
// traversal would escape the folder root.
export function joinFolderPath(currentPath: string, href: string): string | null {
  const currentDir = currentPath.includes("/") ? currentPath.slice(0, currentPath.lastIndexOf("/")) : "";
  const rootRelative = href.startsWith("/");
  const segments = !rootRelative && currentDir ? currentDir.split("/") : [];
  const parts = rootRelative ? href.replace(/^\/+/, "").split("/") : href.split("/");
  for (const part of parts) {
    if (part === "" || part === ".") continue;
    if (part === "..") {
      if (segments.length === 0) return null;
      segments.pop();
      continue;
    }
    segments.push(part);
  }
  return segments.join("/");
}

// Markdown-link-only wrapper around joinFolderPath. Returns null for
// hrefs that don't look like a markdown file so the renderer can leave
// them alone (e.g. asset references handled separately).
export function resolveRelativeDocPath(currentPath: string, href: string): string | null {
  if (!MD_EXT.test(href)) return null;
  return joinFolderPath(currentPath, href);
}
