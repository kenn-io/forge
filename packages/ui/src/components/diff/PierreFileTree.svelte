<script lang="ts">
  import { FileTree, preparePresortedFileTreeInput } from "@pierre/trees";
  import type { FileTreeOptions } from "@pierre/trees";
  import { onMount, untrack } from "svelte";
  import type { DiffFile } from "../../api/types.js";

  type TreeGitStatus = NonNullable<FileTreeOptions["gitStatus"]>[number];

  interface Props {
    files: readonly DiffFile[] | null | undefined;
    selectedPath?: string | null;
    ariaLabel?: string;
    onSelect?: (path: string) => void;
  }

  const {
    files,
    selectedPath = null,
    ariaLabel = "Changed files",
    onSelect,
  }: Props = $props();

  let host: HTMLElement | undefined = $state();
  let tree: FileTree | undefined;
  let renderedTreeKey = "";
  let syncingSelection = false;

  const safeFiles = $derived(files ?? []);
  const treePaths = $derived(safeFiles.map((file) => file.path));
  const preparedTreeInput = $derived(preparePresortedFileTreeInput(treePaths));
  const treeGitStatus = $derived(
    safeFiles.map((file): TreeGitStatus => ({
      path: file.path,
      status: file.status === "copied" ? "renamed" : file.status,
    })),
  );
  const treeKey = $derived(
    `${treePaths.join("\0")}\n${treeGitStatus.map((item) => `${item.path}:${item.status}`).join("\0")}`,
  );
  const treeOptions = $derived<FileTreeOptions>({
    preparedInput: preparedTreeInput,
    initialExpansion: "open",
    initialVisibleRowCount: Math.max(100, treePaths.length * 4),
    overscan: Math.max(100, treePaths.length * 4),
    flattenEmptyDirectories: true,
    density: "compact",
    icons: { set: "complete", colored: false },
    gitStatus: treeGitStatus,
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
    if (selectedPath && tree.getItem(selectedPath)) {
      tree.getItem(selectedPath)?.select();
      tree.focusNearestPath(selectedPath);
    }
    syncingSelection = false;
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
