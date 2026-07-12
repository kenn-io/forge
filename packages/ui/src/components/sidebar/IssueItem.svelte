<script lang="ts">
  import type { Issue } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import { formatRelativeTime } from "@kenn-io/kit-ui";
  import { hashColor } from "@kenn-io/kit-ui";
  import { Chip } from "@kenn-io/kit-ui";
  import LabelRow from "../shared/LabelRow.svelte";
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
      {
        provider: issue.repo.provider,
        platformHost: issue.repo.platform_host,
        owner: issue.repo.owner,
        name: issue.repo.name,
        repoPath: issue.repo.repo_path,
      },
      issue.Number,
      issue.Starred,
    );
  }

  const labels = $derived(issue.labels ?? []);
  const ago = $derived(formatRelativeTime(issue.LastActivityAt));
  const stateLabel = $derived(
    issue.State === "open" ? "Open" : "Closed",
  );
</script>

<button class="issue-item" class:selected bind:this={el} onclick={onclick}>
  <p class="title">{issue.Title}</p>
  <LabelRow {labels} compact />
  {#if showRepo}
    <div class="repo-row">
      <Chip
        size="xs"
        uppercase={false}
        title={repoSlug}
        tone="muted" class="repo-chip"
        style={`color: ${hashColor(repoSlug)}; background: color-mix(in srgb, ${hashColor(repoSlug)} 15%, transparent);`}
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
      <Chip size="xs" tone={issue.State === "open" ? "success" : "merged"} class="state-chip">{stateLabel}</Chip>
      <span class="time">{ago}</span>
    </span>
  </div>
</button>

<style>
  .issue-item {
    display: block;
    width: 100%;
    text-align: left;
    padding: var(--sidebar-row-padding, 10px 12px);
    border-bottom: 1px solid var(--sidebar-list-border-muted, var(--border-muted));
    background: var(--sidebar-row-bg, var(--bg-surface));
    cursor: pointer;
    transition: background 0.1s;
    border-left: 3px solid transparent;
  }

  .issue-item:hover {
    background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
  }

  .issue-item.selected {
    background: var(--bg-row-selected, var(--bg-inset));
    border-left-color: var(--accent-blue);
  }

  .issue-item.selected:hover {
    background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
  }

  .issue-item:focus-visible {
    outline: none;
    box-shadow: inset 0 0 0 1px var(--accent-blue);
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

  :global(.kit-chip.repo-chip) {
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
    min-height: calc(var(--focus-mobile-hit-target, 37px) * 1.65);
    font-size: var(--font-size-md);
    padding: var(--focus-mobile-space-sm, 10px) var(--focus-mobile-space-md, 13px);
    border-bottom: thin solid var(--border-muted);
    border-left-width: 3px;
  }

  :global(.mobile-main) .title {
    margin-bottom: var(--focus-mobile-space-xs, 6.5px);
    font-size: var(--font-size-xl);
    line-height: 1.3;
    white-space: normal;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }

  :global(.mobile-main) .repo-row {
    margin-bottom: var(--focus-mobile-space-xs, 6.5px);
  }

  :global(.mobile-main) .meta-row {
    gap: var(--focus-mobile-space-sm, 8px);
  }

  :global(.mobile-main) .meta-left,
  :global(.mobile-main) .time {
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }

  :global(.mobile-main) .meta-right {
    gap: var(--focus-mobile-space-xs, 6px);
  }

  :global(.mobile-main) :global(.kit-chip),
  :global(.mobile-main) :global(.state-chip),
  :global(.mobile-main) :global(.status-chip) {
    min-height: calc(var(--focus-mobile-hit-target, 37px) * 0.65);
    padding: 2.5px var(--focus-mobile-space-xs, 6.5px);
    border-radius: 999px;
    font-size: var(--font-size-xs);
    line-height: 1.25;
  }
</style>
