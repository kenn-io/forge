<script lang="ts">
  import { autoReposition, Button, floatingPopoverStyle } from "@kenn-io/kit-ui";
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
  let menuEl = $state<HTMLDivElement>();
  let menuStyle = $state("");

  const commits = $derived(diffStore.getCommits());
  const commitsLoading = $derived(diffStore.isCommitsLoading());
  const commitsError = $derived(diffStore.getCommitsError());
  const scope = $derived(diffStore.getScope());
  const scopeLabel = $derived(formatDiffScopeLabel(scope));

  $effect(() => {
    if (disabled) open = false;
  });

  // The menu is fixed-positioned so a clipping ancestor (the phone Files
  // view hides overflow) cannot cut it off, and clamped to the viewport.
  $effect(() => {
    if (!open || !menuEl) return;
    positionMenu();
    return autoReposition(() => [menuEl, pickerRef], positionMenu);
  });

  function positionMenu(): void {
    if (!pickerRef || !menuEl) return;
    const narrow = window.innerWidth <= 760;
    menuStyle = floatingPopoverStyle({
      trigger: pickerRef.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverHeight: menuEl.offsetHeight,
      align: narrow ? "start" : "end",
      edgeGap: 8,
      triggerGap: 4,
      maxWidth: narrow ? 360 : 520,
      constrainWidth: true,
    });
  }

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
    <div class="diff-scope-picker__menu" bind:this={menuEl} style={menuStyle}>
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
              showStats
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
    position: fixed;
    z-index: var(--z-popover);
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
    letter-spacing: var(--letter-spacing-label, 0.06em);
    text-transform: var(--label-transform, uppercase);
  }

  .diff-scope-picker__reset {
    border: 0;
    background: transparent;
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .diff-scope-picker__list {
    display: grid;
    grid-template-columns: max-content max-content minmax(0, 1fr) max-content max-content max-content;
    column-gap: var(--space-3);
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
    .diff-scope-picker__list {
      grid-template-columns: max-content max-content minmax(0, 1fr) max-content;
    }
  }
</style>
