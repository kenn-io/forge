<script lang="ts">
  import { getStores } from "../../context.js";
  import CommitListSection from "./CommitListSection.svelte";
  import PierreFileTree from "./PierreFileTree.svelte";

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

  function handleTreeSelection(path: string): void {
    diff.requestScrollToFile(path);
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
  const activeFile = $derived(diff.getActiveFile());
  const activeFileRevealKey = $derived(diff.getActiveFileRevealKey());
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
    <PierreFileTree
      files={filteredDiffFiles}
      selectedPath={activeFile}
      selectedPathRevealKey={activeFileRevealKey}
      onSelect={handleTreeSelection}
    />
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

</style>
