<script lang="ts">
  // PR rows with stack-tree grouping, shared by the desktop sidebar and the
  // focus and phone lists so both render the same rows for the same filters.
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import type { Snippet } from "svelte";
  import { getStores } from "../../context.js";
  import type { PullRequest } from "../../api/types.js";
  import type { PullSidebarRow } from "../../utils/pull-stack-tree.js";
  import PullItem from "./PullItem.svelte";

  interface Props {
    rows: PullSidebarRow[];
    showRepo: boolean;
    repoLabel: (pr: PullRequest) => string;
    isSelected: (pr: PullRequest) => boolean;
    isMultiSelected: (pr: PullRequest) => boolean;
    onclick: (event: MouseEvent, pr: PullRequest) => void;
    oncontextmenu: (event: MouseEvent, pr: PullRequest) => void;
    /** Content under a row, such as the selected PR's file list. */
    below?: Snippet<[PullRequest, boolean]>;
  }

  const { rows, showRepo, repoLabel, isSelected, isMultiSelected, onclick, oncontextmenu, below }: Props = $props();
  const { pulls } = getStores();
</script>

{#each rows as row (row.pr.ID)}
  {@const pr = row.pr}
  {@const prSelected = isSelected(pr)}
  <div
    class="stack-row"
    class:stack-row--member={row.depth > 0}
    class:stack-row--root={row.memberCount > 0}
  >
    <div class="stack-row-content">
      {#if row.memberCount > 0 && pr.stack && (pr.stack.position > 1 || row.memberCount < pr.stack.size)}
        <span class="stack-context">{row.memberCount} of {pr.stack.size} PRs in this view{pr.stack.position > 1 ? ` · starts at ${pr.stack.position}/${pr.stack.size}` : ""}</span>
      {/if}
      <PullItem
        {pr}
        repoLabel={repoLabel(pr)}
        {showRepo}
        selected={prSelected}
        multiSelected={isMultiSelected(pr)}
        onclick={(event) => onclick(event, pr)}
        oncontextmenu={(event) => oncontextmenu(event, pr)}
      />
      {@render below?.(pr, prSelected)}
    </div>
    {#if row.memberCount > 0}
      {@const countLabel = row.memberCount === pr.stack?.size
        ? `${row.memberCount} PRs in stack`
        : `${row.memberCount} of ${pr.stack?.size} PRs in this view`}
      <div class="stack-control">
        {#if row.memberCount > 1}
          <button
            type="button"
            class="stack-toggle"
            aria-expanded={row.expanded}
            aria-label={`${row.expanded ? "Collapse" : "Expand"} stack at #${pr.Number}: ${countLabel}`}
            title={countLabel}
            onclick={() => pulls.toggleStack(row.stackKey, row.expanded)}
          >
            <span>{row.memberCount}</span>
            <ChevronDownIcon size={12} class={row.expanded ? "" : "stack-chevron--collapsed"} aria-hidden="true" />
          </button>
        {:else}
          <span class="stack-partial" title={countLabel} aria-label={countLabel}>1/{pr.stack?.size}</span>
        {/if}
      </div>
    {/if}
  </div>
{/each}

<style>
  .stack-row {
    display: flex;
    min-width: 0;
  }

  .stack-row-content {
    flex: 1;
    min-width: 0;
  }

  .stack-row--root,
  .stack-row--member {
    --sidebar-row-bg: color-mix(in srgb, var(--accent-blue) 6%, var(--bg-surface));
    background: var(--sidebar-row-bg);
  }

  .stack-row--root {
    margin-top: var(--space-1);
    border-top: 1px solid var(--sidebar-list-border-muted, var(--border-muted));
  }

  .stack-row--member {
    padding-left: var(--space-4);
  }

  .stack-control {
    display: flex;
    align-items: flex-start;
    justify-content: center;
    width: 44px;
    flex-shrink: 0;
    padding-top: var(--space-1);
  }

  .stack-toggle {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-1);
    width: 40px;
    min-height: 40px;
    color: var(--text-secondary);
    font-size: var(--font-size-2xs);
    font-variant-numeric: tabular-nums;
    border-radius: var(--radius-sm);
    cursor: pointer;
  }

  .stack-toggle:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .stack-toggle:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
  }

  .stack-toggle :global(.stack-chevron--collapsed) {
    transform: rotate(-90deg);
  }

  .stack-partial {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .stack-context {
    display: block;
    padding: 6px 12px 0;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

</style>
