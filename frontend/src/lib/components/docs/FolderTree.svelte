<script lang="ts">
  import { FileTree, type GitStatusEntry } from "@pierre/trees";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import { mount, unmount, untrack } from "svelte";
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
    onFileRename?: (from: string, to: string) => void;
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
  // The library drops menu nodes without telling us, so keep the one live
  // icon and unmount it when the next menu renders or the tree goes away.
  let menuIcon: ReturnType<typeof mount> | undefined;

  function unmountMenuIcon(): void {
    if (menuIcon) void unmount(menuIcon);
    menuIcon = undefined;
  }

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
      // in its local model immediately. The parent owns the asynchronous
      // rename and re-snapshots the canonical tree on success or failure.
      renaming: {
        canRename: (item) => !item.isFolder,
        onRename: (event) => {
          if (event.isFolder) return;
          onFileRename?.(event.sourcePath, event.destinationPath);
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
      unmountMenuIcon();
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
    context: { close: (options?: { restoreFocus?: boolean }) => void },
  ): HTMLElement {
    unmountMenuIcon();
    const menu = document.createElement("div");
    menu.className = "folder-tree-context-menu kit-popover-card";
    if (item.kind === "file") {
      menu.setAttribute("role", "menu");
      menu.setAttribute("aria-label", "File actions");
      const rename = document.createElement("button");
      rename.type = "button";
      rename.className = "folder-tree-context-menu-item kit-control-states";
      rename.setAttribute("role", "menuitem");
      // The tree binds F2 to inline rename.
      rename.setAttribute("aria-keyshortcuts", "F2");
      const icon = document.createElement("span");
      icon.className = "folder-tree-context-menu-icon";
      icon.setAttribute("aria-hidden", "true");
      menuIcon = mount(PencilIcon, { target: icon, props: { size: 14 } });
      const label = document.createElement("span");
      label.textContent = "Rename";
      const shortcut = document.createElement("span");
      shortcut.className = "folder-tree-context-menu-shortcut";
      shortcut.setAttribute("aria-hidden", "true");
      shortcut.textContent = "F2";
      rename.append(icon, label, shortcut);
      rename.addEventListener("click", () => {
        // Focus moves into the rename input, so the close must not pull it
        // back to the row.
        context.close({ restoreFocus: false });
        instance?.startRenaming(item.path);
      });
      menu.appendChild(rename);
    } else {
      // No role="menu": a menu with no items would announce as empty.
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
    /* The library defaults to a 16px inline gutter; the rest of the
       docs sidebar (folder chip header, folder dropdown) sits on the
       app's 8px gutter. This variable drives both the search row and
       the tree's scroll container, so they stay aligned with each
       other while matching the header. */
    --trees-padding-inline-override: 8px;

    --trees-selected-fg-override: var(--text-primary);
    --trees-selected-bg-override: var(--bg-surface-hover);
    --trees-selected-focused-border-color-override: var(--accent-blue);
  }

  /* The library renders the context menu through its own portal — at
     the document level, outside the shadow DOM and our component's
     scoped CSS. `:global` is required so these rules reach it. */
  /* Mirrors the kit-ui MenuItem metrics so the Docs and workspace context
     menus read as one family. */
  :global(.folder-tree-context-menu) {
    min-width: 160px;
    padding: var(--space-2);
  }
  :global(.folder-tree-context-menu-item) {
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    width: 100%;
    min-height: 28px;
    padding: var(--space-3) var(--space-4);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    white-space: nowrap;
    cursor: pointer;
  }
  :global(.folder-tree-context-menu-item:hover),
  :global(.folder-tree-context-menu-item:focus-visible) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
  :global(.folder-tree-context-menu-item:focus-visible) {
    outline-offset: -2px;
  }
  :global(.folder-tree-context-menu-icon) {
    display: inline-flex;
    flex: 0 0 16px;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
  }
  :global(.folder-tree-context-menu-shortcut) {
    margin-left: auto;
    padding-left: var(--space-7);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  :global(.folder-tree-context-menu-note) {
    padding: var(--space-3) var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
</style>
