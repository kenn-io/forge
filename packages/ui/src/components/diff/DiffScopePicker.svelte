<script lang="ts">
  import GitCommitHorizontalIcon from "@lucide/svelte/icons/git-commit-horizontal";
  import { getStores } from "../../context.js";
  import CommitListItem from "./CommitListItem.svelte";
  import DiffScopeLabel from "./DiffScopeLabel.svelte";
  import { formatDiffScopeLabel } from "./scope-label.js";

  interface Props {
    compact?: boolean;
  }

  const { compact = false }: Props = $props();
  const { diff: diffStore } = getStores();

  let open = $state(false);
  let pickerRef = $state<HTMLDivElement>();

  const commits = $derived(diffStore.getCommits());
  const commitsLoading = $derived(diffStore.isCommitsLoading());
  const commitsError = $derived(diffStore.getCommitsError());
  const scope = $derived(diffStore.getScope());
  const scopeLabel = $derived(formatDiffScopeLabel(scope));

  function toggle(): void {
    open = !open;
    if (open) {
      void diffStore.loadCommits();
    }
  }

  function close(): void {
    open = false;
  }

  function isActive(sha: string): boolean {
    if (scope.kind === "commit") return scope.sha === sha;
    if (scope.kind !== "range" || !commits) return false;
    const fromIdx = commits.findIndex((c) => c.sha === scope.fromSha);
    const toIdx = commits.findIndex((c) => c.sha === scope.toSha);
    const idx = commits.findIndex((c) => c.sha === sha);
    if (fromIdx === -1 || toIdx === -1 || idx === -1) return false;
    return idx >= toIdx && idx <= fromIdx;
  }

  function handleCommitClick(sha: string, shiftKey: boolean): void {
    if (shiftKey && scope.kind === "commit") {
      diffStore.selectRange(scope.sha, sha);
    } else {
      diffStore.selectCommit(sha);
    }
  }

  function handleDocumentClick(event: MouseEvent): void {
    if (!open) return;
    const target = event.target;
    if (target instanceof Node && pickerRef?.contains(target)) return;
    close();
  }

  function handleDocumentKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") close();
  }
</script>

<svelte:document onclick={handleDocumentClick} onkeydown={handleDocumentKeydown} />

<div
  class={["diff-scope-picker", compact && "diff-scope-picker--compact"]}
  bind:this={pickerRef}
>
  <div class="diff-scope-picker__control">
    <button
      class="diff-scope-picker__trigger"
      type="button"
      aria-label={`Select commit range: ${scopeLabel}`}
      aria-expanded={open}
      title="Commits"
      onclick={toggle}
    >
      {#if compact}
        <GitCommitHorizontalIcon size={16} strokeWidth={1.8} aria-hidden="true" />
        <DiffScopeLabel {scope} />
      {:else}
        <span class="diff-scope-picker__label">Commits</span>
        <DiffScopeLabel {scope} />
      {/if}
    </button>
  </div>

  {#if open}
    <div class="diff-scope-picker__menu">
      <div class="diff-scope-picker__menu-header">
        <span>Commit range</span>
        {#if scope.kind !== "head"}
          <button
            class="diff-scope-picker__reset"
            type="button"
            onclick={diffStore.resetToHead}
          >
            Clear
          </button>
        {/if}
      </div>

      {#if commitsLoading}
        <div class="diff-scope-picker__state">Loading commits</div>
      {:else if commitsError}
        <div class="diff-scope-picker__state diff-scope-picker__state--error">
          {commitsError}
        </div>
      {:else if commits && commits.length > 0}
        <div class="diff-scope-picker__list">
          {#each commits as commit (commit.sha)}
            <CommitListItem
              {commit}
              active={isActive(commit.sha)}
              onclick={handleCommitClick}
            />
          {/each}
        </div>
      {:else if commits}
        <div class="diff-scope-picker__state">No commits</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .diff-scope-picker {
    position: relative;
    min-width: 0;
    flex-shrink: 0;
  }

  .diff-scope-picker__control {
    display: inline-flex;
    align-items: center;
    box-sizing: border-box;
    gap: 6px;
    height: 26px;
    padding: 2px 6px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
  }

  .diff-scope-picker__control:hover {
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }

  .diff-scope-picker__trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    line-height: 1;
  }

  .diff-scope-picker--compact .diff-scope-picker__control {
    gap: 0;
    padding: 0;
    overflow: hidden;
  }

  .diff-scope-picker--compact .diff-scope-picker__trigger {
    gap: 6px;
    min-width: 78px;
    max-width: 128px;
    height: 24px;
    padding: 0 8px;
  }

  .diff-scope-picker__label {
    display: inline-flex;
    align-items: center;
    font-size: var(--font-size-xs);
    font-weight: 600;
    line-height: 1;
    white-space: nowrap;
  }

  .diff-scope-picker__menu {
    position: absolute;
    z-index: 1000;
    top: calc(100% + 4px);
    right: 0;
    width: min(420px, calc(100cqw - 20px));
    max-height: min(460px, 70vh);
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
  }

  .diff-scope-picker__menu-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 7px 10px;
    border-bottom: 1px solid var(--border-muted);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .diff-scope-picker__reset {
    border: 0;
    background: transparent;
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .diff-scope-picker__list {
    max-height: 390px;
    overflow-y: auto;
    padding: 3px 0;
  }

  .diff-scope-picker__state {
    padding: 12px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .diff-scope-picker__state--error {
    color: var(--accent-red);
  }

  @media (max-width: 720px) {
    .diff-scope-picker__menu {
      left: 0;
      right: auto;
      width: min(360px, 86vw);
    }
  }
</style>
