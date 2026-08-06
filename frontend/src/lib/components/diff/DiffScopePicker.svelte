<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import GitCommitHorizontalIcon from "@lucide/svelte/icons/git-commit-horizontal";
  import { getStores } from "../../context.js";
  import CommitListItem from "./CommitListItem.svelte";
  import DiffScopeLabel from "./DiffScopeLabel.svelte";
  import { formatDiffScopeLabel } from "./scope-label.js";

  interface Props {
    compact?: boolean;
    disabled?: boolean;
  }

  const { compact = false, disabled = false }: Props = $props();
  const { diff: diffStore } = getStores();

  let open = $state(false);
  let pickerRef = $state<HTMLDivElement>();

  const commits = $derived(diffStore.getCommits());
  const commitsLoading = $derived(diffStore.isCommitsLoading());
  const commitsError = $derived(diffStore.getCommitsError());
  const scope = $derived(diffStore.getScope());
  const scopeLabel = $derived(formatDiffScopeLabel(scope));

  $effect(() => {
    if (disabled) open = false;
  });

  function toggle(): void {
    if (disabled) return;
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
    if (disabled) return;
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
  <Button
    class="diff-scope-picker__trigger"
    size="sm"
    ariaLabel={`Select commit range: ${scopeLabel}`}
    ariaExpanded={open}
    title="Commits"
    disabled={disabled}
    onclick={toggle}
  >
    <GitCommitHorizontalIcon size={14} strokeWidth={1.8} aria-hidden="true" />
    <DiffScopeLabel {scope} />
    <ChevronDownIcon
      class="diff-scope-picker__chevron"
      size={12}
      strokeWidth={2}
      aria-hidden="true"
    />
  </Button>

  {#if open}
    <div class="diff-scope-picker__menu">
      <div class="diff-scope-picker__menu-header">
        <span>Commit range</span>
        {#if scope.kind !== "head"}
          <button
            class="diff-scope-picker__reset"
            type="button"
            disabled={disabled}
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

  :global(.diff-scope-picker__trigger.kit-button) {
    height: 26px;
    gap: var(--space-3);
    padding: 0 var(--space-3);
    font-size: inherit;
    line-height: 1;
  }

  :global(.diff-scope-picker__trigger .diff-scope-label) {
    font-size: var(--font-size-xs);
  }

  :global(.diff-scope-picker__chevron) {
    flex-shrink: 0;
    opacity: 0.55;
  }

  .diff-scope-picker--compact :global(.diff-scope-picker__trigger.kit-button) {
    gap: var(--space-3);
    min-width: 80px;
    max-width: 130px;
    padding: 0 var(--space-4);
    overflow: hidden;
  }

  .diff-scope-picker__menu {
    position: absolute;
    z-index: var(--z-popover);
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

  @media (max-width: 760px) {
    .diff-scope-picker__menu {
      left: 0;
      right: auto;
      width: min(360px, 86vw);
    }
  }
</style>
