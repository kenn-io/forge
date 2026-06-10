<script lang="ts">
  import { FileTree, type GitStatusEntry } from "@pierre/trees";
  import { untrack } from "svelte";
  import type { TreeNode } from "../../api/docs/types";
  import { flattenTreePaths } from "./folderTreePaths";

  interface Props {
    tree: TreeNode | null;
    gitEntries: readonly GitStatusEntry[];
    activePath: string | null;
    onSelect?: (path: string | null) => void;
    onSearchChange?: (value: string | null) => void;
    // Called when the user finishes an inline rename (Enter / blur).
    // Should perform the actual filesystem rename and refresh the tree.
    // Returning a rejected promise lets the library surface the failure
    // back to the inline input.
    onFileRename?: (from: string, to: string) => Promise<void>;
  }

  let {
    tree,
    gitEntries,
    activePath,
    onSelect,
    onSearchChange,
    onFileRename,
  }: Props = $props();

  let host: HTMLDivElement | null = $state(null);
  let instance: FileTree | null = null;
  // Tracks which paths in the current tree are files; directory
  // selections (trailing slash) and stale paths are ignored so a
  // folder click doesn't trigger a doc read.
  let knownFiles = new Set<string>();

  $effect(() => {
    if (!host) return;
    const initialPaths = untrack(() => flattenTreePaths(tree));
    const initialGit = untrack(() => [...gitEntries]);
    const initialActive = untrack(() => activePath);
    knownFiles = new Set(initialPaths);
    const fileTree = new FileTree({
      paths: initialPaths,
      gitStatus: initialGit,
      search: true,
      // Files can be renamed inline (F2 or context menu); directories
      // need a different backend path so they're left non-renameable
      // for now.
      //
      // @pierre/trees calls onRename synchronously and moves the item
      // in its local model immediately. If the backend rejects (server
      // error, name collision, unsupported extension), we'd leave the
      // tree showing a phantom file. Catch the rejection, log it, and
      // let the parent's tree-data effect re-snapshot from the canonical
      // backend state on next render — the move shown by the library
      // gets corrected when fresh paths flow in.
      renaming: {
        canRename: (item) => !item.isFolder,
        onRename: (event) => {
          if (event.isFolder) return;
          void onFileRename?.(event.sourcePath, event.destinationPath).catch((err) => {
            console.error("rename failed", event.sourcePath, "→", event.destinationPath, err);
          });
        },
      },
      composition: {
        contextMenu: {
          enabled: true,
          triggerMode: "right-click",
          render: (item, context) => buildContextMenu(item, context),
        },
      },
      dragAndDrop: false,
      // Default to collapsed so the sidebar surfaces top-level structure
      // first and the user opts in to depth. Once cross-nav persistence
      // is in place this only governs the first-ever load anyway.
      initialExpansion: "closed",
      // The library's built-in MiddleTruncate keeps its "…" marker
      // anchored to the grid's intrinsic width — fine at wide
      // viewports, but in narrow sidebars the visible content gets
      // lexically clipped before the marker position, so the marker
      // falls outside the visible area and filenames render as
      // "2026-05-16-knowlemd" instead of "2026-05-16-knowle…md".
      // Replace the truncate geometry with a plain end-ellipsis on
      // each name/extension segment so the truncation point is
      // always visible. The split keeps the extension anchored to
      // the right edge. Injected via the library's unsafeCSS hook
      // because the original CSS lives in the file-tree shadow root
      // and is otherwise unreachable from app styles.
      unsafeCSS: `
        [data-truncate-container] {
          overflow: hidden;
          min-width: 0;
        }
        [data-truncate-grid] {
          display: flex;
          align-items: center;
          min-width: 0;
          width: 100%;
        }
        /* The OverflowContent wrapper div has no attribute we can hook
           on directly — it's just an anonymous flex item holding the
           visible + overflow copies. Without a min-width: 0 it keeps
           its intrinsic 'max-content' width, which is what lets the
           filename render past the truncate-container's clip edge. */
        [data-truncate-grid] > div {
          min-width: 0;
          max-width: 100%;
          overflow: hidden;
        }
        [data-truncate-content="visible"] {
          overflow: hidden;
          text-overflow: ellipsis;
          white-space: nowrap;
          min-width: 0;
        }
        [data-truncate-content="overflow"],
        [data-truncate-marker-cell] {
          display: none;
        }
      `,
      onSelectionChange: (paths) => {
        const candidate = paths[0];
        if (!candidate) {
          onSelect?.(null);
          return;
        }
        if (knownFiles.has(candidate)) onSelect?.(candidate);
      },
      onSearchChange: (value) => onSearchChange?.(value),
    });
    fileTree.render({ fileTreeContainer: host });
    if (initialActive) {
      expandAncestors(fileTree, initialActive);
      try {
        fileTree.focusPath(initialActive);
      } catch {
        // Active path may not exist in the tree yet — ignore.
      }
    }
    instance = fileTree;
    return () => {
      fileTree.unmount();
      fileTree.cleanUp();
      instance = null;
    };
  });

  // Push subsequent prop changes through the model without
  // rebuilding the EditorView. Each effect tracks just the input it
  // cares about so unrelated updates don't churn the tree.
  $effect(() => {
    if (!instance) return;
    const paths = flattenTreePaths(tree);
    knownFiles = new Set(paths);
    instance.resetPaths(paths);
  });

  $effect(() => {
    if (!instance) return;
    instance.setGitStatus([...gitEntries]);
  });

  $effect(() => {
    if (!instance || !activePath) return;
    expandAncestors(instance, activePath);
    try {
      instance.focusPath(activePath);
    } catch {
      // Active path not present after a tree update — ignore so the
      // user can still navigate.
    }
  });

  // Renders the right-click menu as a detached DOM element. The
  // library handles positioning via its own portal — we just return
  // the menu node and call context.close() when an action runs. The
  // menu lives outside the shadow DOM so it inherits app styles.
  function buildContextMenu(
    item: { kind: "file" | "directory"; path: string; name: string },
    context: { close: () => void },
  ): HTMLElement {
    const menu = document.createElement("div");
    menu.className = "folder-tree-context-menu";
    if (item.kind === "file") {
      const rename = document.createElement("button");
      rename.type = "button";
      rename.className = "folder-tree-context-menu-item";
      rename.textContent = "Rename";
      rename.addEventListener("click", () => {
        context.close();
        instance?.startRenaming(item.path);
      });
      menu.appendChild(rename);
    } else {
      const note = document.createElement("div");
      note.className = "folder-tree-context-menu-note";
      note.textContent = "No actions";
      menu.appendChild(note);
    }
    return menu;
  }

  // The tree opens collapsed by default so the user sees top-level
  // structure first, but a deep-linked or programmatically-selected
  // nested file would then stay hidden inside its collapsed ancestors.
  // Walk activePath upward and expand each directory handle so the
  // selection scrolls into view instead of being clipped from sight.
  function expandAncestors(fileTree: FileTree, path: string) {
    const parts = path.split("/");
    for (let i = 1; i < parts.length; i += 1) {
      const ancestor = parts.slice(0, i).join("/");
      try {
        const handle = fileTree.getItem(ancestor);
        if (handle && "expand" in handle) handle.expand();
      } catch {
        // Ancestor missing — bail; focusPath's own try/catch handles
        // the file-side miss.
      }
    }
  }
</script>

<div class="folder-tree" bind:this={host}></div>

<style>
  .folder-tree {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    /* @pierre/trees renders inside a shadow DOM and exposes
       `--trees-*-override` custom properties for theming. Custom
       properties pierce shadow DOM, so setting them on the host here
       reskins the library to the app's theme tokens — and lets it
       follow light/dark mode automatically since those tokens flip on
       :root.dark. */
    --trees-fg-override: var(--text-primary);
    --trees-fg-muted-override: var(--text-muted);
    --trees-bg-override: transparent;
    --trees-bg-muted-override: var(--bg-inset);
    --trees-accent-override: var(--accent-blue);
    --trees-border-color-override: var(--border-default);

    --trees-search-fg-override: var(--text-primary);
    --trees-search-bg-override: var(--bg-inset);

    --trees-selected-fg-override: var(--text-primary);
    --trees-selected-bg-override: var(--bg-surface-hover);
    --trees-selected-focused-border-color-override: var(--accent-blue);
  }

  /* The library renders the context menu through its own portal — at
     the document level, outside the shadow DOM and our component's
     scoped CSS. `:global` is required so these rules reach it. */
  :global(.folder-tree-context-menu) {
    min-width: 140px;
    padding: 4px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
    font-size: var(--font-size-sm);
  }
  :global(.folder-tree-context-menu-item) {
    display: block;
    width: 100%;
    padding: 6px 10px;
    border: none;
    background: transparent;
    color: var(--text-primary);
    text-align: left;
    border-radius: var(--radius-sm);
    cursor: pointer;
    font: inherit;
  }
  :global(.folder-tree-context-menu-item:hover) {
    background: var(--bg-surface-hover);
  }
  :global(.folder-tree-context-menu-note) {
    padding: 6px 10px;
    color: var(--text-muted);
    font-style: italic;
  }
</style>
