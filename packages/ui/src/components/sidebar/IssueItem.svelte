<script lang="ts">
  import type { Issue } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import { timeAgo } from "../../utils/time.js";
  import { repoColor } from "../../utils/repo-color.js";
  import Chip from "../shared/Chip.svelte";
  import GitHubLabels from "../shared/GitHubLabels.svelte";
  import WorkspaceIndicator from "../shared/WorkspaceIndicator.svelte";

  const { issues } = getStores();

  interface Props {
    issue: Issue;
    selected: boolean;
    showRepo: boolean;
    onclick: () => void;
  }

  const { issue, selected, showRepo, onclick }: Props = $props();

  const repoSlug = $derived(
    `${issue.repo_owner ?? ""}/${issue.repo_name ?? ""}`,
  );

  let el: HTMLButtonElement;

  $effect(() => {
    if (selected && el) {
      el.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  });

  function handleStarClick(e: MouseEvent): void {
    e.stopPropagation();
    void issues.toggleIssueStar(
      issue.repo_owner ?? "",
      issue.repo_name ?? "",
      issue.Number,
      issue.Starred,
    );
  }

  const labels = $derived(issue.labels ?? []);
  const ago = $derived(timeAgo(issue.LastActivityAt));
  const stateLabel = $derived(
    issue.State === "open" ? "Open" : "Closed",
  );
</script>

<button class="issue-item" class:selected bind:this={el} onclick={onclick}>
  <p class="title">{issue.Title}</p>
  {#if labels.length > 0}
    <GitHubLabels labels={labels} mode="compact" />
  {/if}
  {#if showRepo}
    <div class="repo-row">
      <Chip
        size="sm"
        uppercase={false}
        title={repoSlug}
        class="chip--muted repo-chip"
        style={`color: ${repoColor(repoSlug)}; background: color-mix(in srgb, ${repoColor(repoSlug)} 15%, transparent);`}
      >{repoSlug}</Chip>
    </div>
  {/if}
  <div class="meta-row">
    <span class="meta-left">
      #{issue.Number} · {issue.Author}
    </span>
    <span class="meta-right">
      {#if issue.workspace}
        <WorkspaceIndicator status={issue.workspace.status} />
      {/if}
      <span
        class="star-btn"
        role="button"
        tabindex="0"
        onclick={handleStarClick}
        onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); handleStarClick(e as unknown as MouseEvent); } }}
        title={issue.Starred ? "Unstar" : "Star"}
      >
        {#if issue.Starred}
          <svg class="star-icon star-icon--active" width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
            <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25z"/>
          </svg>
        {:else}
          <svg class="star-icon" width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
            <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25zm0 2.445L6.615 5.5a.75.75 0 01-.564.41l-3.097.45 2.24 2.184a.75.75 0 01.216.664l-.528 3.084 2.769-1.456a.75.75 0 01.698 0l2.77 1.456-.53-3.084a.75.75 0 01.216-.664l2.24-2.183-3.096-.45a.75.75 0 01-.564-.41L8 2.694z"/>
          </svg>
        {/if}
      </span>
      <Chip size="sm" class={`state-chip chip--${issue.State}`}>{stateLabel}</Chip>
      <span class="time">{ago}</span>
    </span>
  </div>
</button>

<style>
  .issue-item {
    display: block;
    width: 100%;
    text-align: left;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
    cursor: pointer;
    transition: background 0.1s;
    border-left: 3px solid transparent;
  }

  .issue-item:hover {
    background: var(--bg-surface-hover);
  }

  .issue-item.selected {
    background: var(--bg-inset);
    border-left-color: var(--accent-blue);
  }

  .title {
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    margin-bottom: 4px;
  }

  .meta-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .meta-left {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .repo-row {
    display: flex;
    min-width: 0;
    margin-bottom: 4px;
  }

  :global(.chip.repo-chip) {
    flex: 0 1 auto;
    justify-content: flex-start;
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
  }

  .meta-right {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-shrink: 0;
  }

  .time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  :global(.state-chip) {
    flex-shrink: 0;
  }

  .star-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    opacity: 0;
    transition: opacity 0.15s;
    cursor: pointer;
  }

  .issue-item:hover .star-btn {
    opacity: 0.6;
  }

  .star-btn:hover {
    opacity: 1 !important;
  }

  .star-btn:has(.star-icon--active) {
    opacity: 1;
  }

  .star-icon {
    color: var(--text-muted);
    transition: color 0.1s;
  }

  .star-btn:hover .star-icon {
    color: var(--accent-amber);
  }

  .star-icon--active {
    color: var(--accent-amber);
  }

  :global(.mobile-main) .issue-item {
    min-height: calc(var(--focus-mobile-hit-target, 2.85rem) * 1.65);
    font-size: var(--font-size-mobile-body);
    padding: var(--focus-mobile-space-sm, 0.75rem) var(--focus-mobile-space-md, 1rem);
    border-bottom: thin solid var(--border-muted);
    border-left-width: 0.25rem;
  }

  :global(.mobile-main) .title {
    margin-bottom: var(--focus-mobile-space-xs, 0.5rem);
    font-size: var(--font-size-mobile-title);
    line-height: 1.3;
    white-space: normal;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }

  :global(.mobile-main) .repo-row {
    margin-bottom: var(--focus-mobile-space-xs, 0.5rem);
  }

  :global(.mobile-main) .meta-row {
    gap: var(--focus-mobile-space-sm, 0.75rem);
  }

  :global(.mobile-main) .meta-left,
  :global(.mobile-main) .time {
    font-size: var(--font-size-mobile-sm);
    line-height: 1.35;
  }

  :global(.mobile-main) .meta-right {
    gap: var(--focus-mobile-space-xs, 0.5rem);
  }

  :global(.mobile-main) :global(.chip),
  :global(.mobile-main) :global(.state-chip),
  :global(.mobile-main) :global(.status-chip) {
    min-height: calc(var(--focus-mobile-hit-target, 2.85rem) * 0.65);
    padding: 0.2rem var(--focus-mobile-space-xs, 0.5rem);
    border-radius: 999rem;
    font-size: var(--font-size-mobile-xs);
    line-height: 1.25;
  }
</style>
