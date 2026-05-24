<script lang="ts">
  import { FileTree } from "@pierre/trees";
  import type { FileTreeOptions } from "@pierre/trees";
  import { onMount } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import CommitListSection from "./CommitListSection.svelte";

  // Reusable file-tree + commit-list panel for the diff Files view.
  // Used by PullList (inlined under the selected PR row in the
  // standalone PR browser) and by PullDetail (as the left pane of
  // the Files tab inside the activity/kanban drawers).
  const { diff, pulls } = getStores();

  interface Props {
    showCommits?: boolean;
    resetKey?: string;
  }

  const { showCommits = true, resetKey = "" }: Props = $props();
  let treeHost: HTMLElement | undefined = $state();
  let tree: FileTree | undefined;
  let renderedTreeKey = "";
  let syncingSelection = false;
  type TreeGitStatus = NonNullable<FileTreeOptions["gitStatus"]>[number];

  function handleTreeSelection(paths: readonly string[]): void {
    if (syncingSelection) return;
    const selection = window.getSelection();
    if (selection && !selection.isCollapsed) return;
    const path = paths[0];
    if (path) diff.requestScrollToFile(path);
  }

  // Per-diff file filter input (shown when 10+ files in diff).
  let fileFilterText = $state("");
  // Reset filter whenever selected PR changes so stale filter text
  // does not silently hide files in the next PR.
  $effect(() => {
    const key = resetKey;
    pulls.getSelectedPR();
    if (key === "\0") return;
    fileFilterText = "";
  });
  const showFileFilter = $derived(
    (diff.getVisibleFileList()?.files.length ?? 0) >= 10,
  );
  const filteredFileList = $derived.by(() => {
    const list = diff.getVisibleFileList();
    if (!list) return null;
    // Only apply filter when the filter UI is visible to avoid
    // silent hiding when the next PR has fewer files.
    if (!showFileFilter) return list;
    const q = fileFilterText.trim().toLowerCase();
    if (!q) return list;
    const files = list.files.filter((f) => f.path.toLowerCase().includes(q));
    return {
      ...list,
      files,
    };
  });
  const filteredDiffFiles = $derived(filteredFileList?.files ?? null);
  const treePaths = $derived(filteredDiffFiles?.map((file) => file.path) ?? []);
  const treeGitStatus = $derived(
    filteredDiffFiles?.map((file): TreeGitStatus => ({
      path: file.path,
      status: file.status === "copied" ? "renamed" : file.status,
    })) ?? [],
  );
  const treeKey = $derived(
    `${treePaths.join("\0")}\n${treeGitStatus.map((item) => `${item.path}:${item.status}`).join("\0")}`,
  );
  const treeOptions = $derived<FileTreeOptions>({
    paths: treePaths,
    initialExpansion: "open",
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
    if (!treeHost || !filteredDiffFiles) return;
    if (tree && renderedTreeKey === treeKey) return;
    tree?.cleanUp();
    tree = new FileTree(treeOptions);
    tree.render({ fileTreeContainer: treeHost });
    renderedTreeKey = treeKey;
    syncActiveFileSelection();
  });

  $effect(() => {
    diff.getActiveFile();
    syncActiveFileSelection();
  });

  function syncActiveFileSelection(): void {
    if (!tree) return;
    const activeFile = diff.getActiveFile();
    syncingSelection = true;
    for (const selectedPath of tree.getSelectedPaths()) {
      if (selectedPath !== activeFile) {
        tree.getItem(selectedPath)?.deselect();
      }
    }
    if (activeFile && tree.getItem(activeFile)) {
      tree.getItem(activeFile)?.select();
      tree.focusNearestPath(activeFile);
    }
    syncingSelection = false;
  }
</script>

{#if showCommits}
  <CommitListSection />
{/if}
<div class="diff-files">
  {#if diff.isFileListLoading() && !diff.getFileList()}
    <div class="diff-files-state diff-files-state--loading">Loading files</div>
  {:else if filteredDiffFiles}
    {#if showFileFilter}
      <div class="diff-files-filter">
        <input
          type="text"
          class="diff-files-filter__input"
          placeholder="Filter files..."
          bind:value={fileFilterText}
        />
      </div>
    {/if}
    <div
      class="diff-file-tree"
      bind:this={treeHost}
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
      aria-label="Changed files"
    ></div>
  {/if}
</div>

<style>
  .diff-files {
    border-bottom: 1px solid var(--border-muted);
    padding: 4px 0;
    display: flex;
    flex: 1 1 auto;
    flex-direction: column;
    min-height: 0;
    overflow: hidden;
  }

  .diff-files-filter {
    padding: 4px 10px 6px 24px;
  }

  .diff-files-filter__input {
    width: 100%;
    font-size: var(--font-size-xs);
    padding: 3px 8px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-muted);
    background: var(--bg-inset);
    color: var(--text-primary);
  }

  .diff-files-filter__input:focus {
    border-color: var(--accent-blue);
    outline: none;
  }

  .diff-files-state {
    padding: 6px 24px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .diff-files-state--loading {
    animation: pulse 1.5s ease-in-out infinite;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 1; }
  }

  .diff-file-tree {
    display: block;
    flex: 1 1 auto;
    width: 100%;
    height: 100%;
    min-height: 0;
  }
</style>
