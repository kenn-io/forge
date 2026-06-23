<script lang="ts">
  import { FileTree, preparePresortedFileTreeInput } from "@pierre/trees";
  import type { FileTreeOptions } from "@pierre/trees";
  import { onMount, untrack } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import type { FileTreeEntry } from "./file-tree-entry.js";

  type TreeGitStatus = NonNullable<FileTreeOptions["gitStatus"]>[number];
  type TreeRowDecoration = NonNullable<FileTreeOptions["renderRowDecoration"]>;

  interface Props {
    files: readonly DiffFile[] | null | undefined;
    entries?: readonly FileTreeEntry[] | null | undefined;
    selectedPath?: string | null;
    selectedPathRevealKey?: number;
    ariaLabel?: string;
    onSelect?: (path: string) => void;
  }

  const {
    files,
    entries = undefined,
    selectedPath = null,
    selectedPathRevealKey = 0,
    ariaLabel = "Changed files",
    onSelect,
  }: Props = $props();

  let host: HTMLElement | undefined = $state();
  let tree: FileTree | undefined;
  let renderedTreeKey = "";
  let syncingSelection = false;
  let selectedPathScrollFrame = 0;
  let lastSelectedPathRevealKey: number | null = null;

  const safeEntries = $derived.by<FileTreeEntry[]>(() => {
    if (entries) return [...entries];
    return (files ?? []).map((file) => ({
      path: file.path,
      status: file.status === "copied" ? "renamed" : file.status,
    }));
  });
  const treePaths = $derived(safeEntries.map((file) => file.path));
  const preparedTreeInput = $derived(preparePresortedFileTreeInput(treePaths));
  const treeGitStatus = $derived(
    safeEntries.flatMap((file): TreeGitStatus[] => file.status ? [{
      path: file.path,
      status: file.status,
    }] : []),
  );
  const treeDecorations = $derived(new Map(safeEntries.map((file) => [file.path, file])));
  const treeKey = $derived(
    `${treePaths.join("\0")}\n${treeGitStatus.map((item) => `${item.path}:${item.status}`).join("\0")}\n${safeEntries.map((item) => `${item.path}:${item.decoration ?? ""}`).join("\0")}`,
  );
  const renderRowDecoration = $derived.by<TreeRowDecoration>(() => (context) => {
    if (context.row.kind !== "file") return null;
    const entry = treeDecorations.get(context.item.path);
    if (!entry?.decoration) return null;
    return {
      text: entry.decoration,
      ...(entry.decorationTitle && { title: entry.decorationTitle }),
    };
  });
  const treeOptions = $derived<FileTreeOptions>({
    preparedInput: preparedTreeInput,
    initialExpansion: "open",
    initialVisibleRowCount: Math.max(100, treePaths.length * 4),
    overscan: Math.max(100, treePaths.length * 4),
    flattenEmptyDirectories: true,
    density: "compact",
    icons: { set: "complete", colored: false },
    gitStatus: treeGitStatus,
    renderRowDecoration,
    onSelectionChange: handleTreeSelection,
    unsafeCSS: `
      [data-type='item'] {
        box-sizing: border-box;
        max-width: calc(100% - var(--trees-item-margin-x) * 2);
        overflow: hidden;
      }
      [data-item-section='icon'],
      [data-item-section='git'],
      [data-item-section='action'] {
        flex-shrink: 0;
      }
      [data-item-section='content'] {
        flex: 1 1 auto;
        max-width: none;
      }
      [data-truncate-group-container='middle'],
      [data-truncate-container],
      [data-truncate-grid] {
        width: 100%;
        max-width: 100%;
      }
      [data-truncate-group-container='middle'] > div {
        min-width: 0;
        overflow: hidden;
      }
      [data-item-git-status='deleted'] [data-item-section='content'] {
        text-decoration: line-through;
        opacity: 0.7;
      }
    `,
  });

  onMount(() => {
    return () => {
      cancelAnimationFrame(selectedPathScrollFrame);
      tree?.cleanUp();
      tree = undefined;
    };
  });

  $effect(() => {
    if (!host) return;
    if (tree && renderedTreeKey === treeKey) {
      untrack(syncSelectedPath);
      return;
    }
    tree?.cleanUp();
    tree = new FileTree(treeOptions);
    tree.render({ fileTreeContainer: host });
    renderedTreeKey = treeKey;
    untrack(syncSelectedPath);
  });

  $effect(() => {
    syncSelectedPath();
  });

  function handleTreeSelection(paths: readonly string[]): void {
    if (syncingSelection) return;
    const selection = window.getSelection();
    if (selection && !selection.isCollapsed) return;
    const path = paths[0];
    if (path) onSelect?.(path);
  }

  function syncSelectedPath(): void {
    if (!tree) return;
    syncingSelection = true;
    for (const selected of tree.getSelectedPaths()) {
      if (selected !== selectedPath) {
        tree.getItem(selected)?.deselect();
      }
    }
    const selectedItem = selectedPath ? tree.getItem(selectedPath) : undefined;
    if (selectedItem) {
      selectedItem.select();
    }
    revealSelectedPathIfRequested(selectedPath, !!selectedItem);
    syncingSelection = false;
  }

  function revealSelectedPathIfRequested(path: string | null, canRevealPath: boolean): void {
    const isInitialSync = lastSelectedPathRevealKey === null;
    if (!isInitialSync && selectedPathRevealKey === lastSelectedPathRevealKey) return;
    lastSelectedPathRevealKey = selectedPathRevealKey;
    if (selectedPathRevealKey === 0) return;
    if (!path || !canRevealPath) return;
    tree?.focusNearestPath(path);
    scheduleSelectedPathIntoView(path);
  }

  function scheduleSelectedPathIntoView(path: string): void {
    scrollPathIntoView(path);
    cancelAnimationFrame(selectedPathScrollFrame);
    selectedPathScrollFrame = requestAnimationFrame(() => {
      selectedPathScrollFrame = 0;
      scrollPathIntoView(path);
    });
  }

  function scrollPathIntoView(path: string): void {
    const root = host?.shadowRoot;
    if (!root) return;
    for (const item of root.querySelectorAll<HTMLElement>("[data-item-path]")) {
      if (item.dataset.itemPath !== path) continue;
      if (typeof item.scrollIntoView !== "function") return;
      item.scrollIntoView({ block: "nearest" });
      return;
    }
  }
</script>

<div
  class="diff-file-tree"
  bind:this={host}
  style:--trees-fg-override="var(--text-primary)"
  style:--trees-fg-muted-override="var(--text-secondary)"
  style:--trees-bg-override="transparent"
  style:--trees-bg-muted-override="var(--bg-surface-hover)"
  style:--trees-accent-override="var(--accent-blue)"
  style:--trees-selected-fg-override="var(--text-primary)"
  style:--trees-selected-bg-override="color-mix(in srgb, var(--accent-blue) 10%, transparent)"
  style:--trees-border-color-override="transparent"
  style:--trees-font-family-override="var(--font-mono)"
  style:--trees-font-size-override="var(--font-size-xs)"
  style:--trees-border-radius-override="var(--radius-sm)"
  style:--trees-padding-inline-override="8px"
  style:--trees-item-padding-x-override="6px"
  style:--trees-item-margin-x-override="4px"
  style:--trees-icon-width-override="14px"
  style:--trees-git-lane-width-override="16px"
  style:--trees-action-lane-width-override="0px"
  style:--trees-file-icon-color="var(--text-muted)"
  style:--trees-git-added-color-override="var(--accent-green)"
  style:--trees-git-deleted-color-override="var(--accent-red)"
  style:--trees-git-modified-color-override="var(--accent-amber)"
  style:--trees-git-renamed-color-override="var(--accent-blue)"
  aria-label={ariaLabel}
></div>

<style>
  .diff-file-tree {
    display: block;
    flex: 1 1 auto;
    width: 100%;
    height: 100%;
    min-height: 0;
  }
</style>
