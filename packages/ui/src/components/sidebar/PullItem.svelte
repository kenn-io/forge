<script lang="ts">
  import type { PullRequest } from "../../api/types.js";
  import type { Action } from "../../types.js";
  import { getStores, getHostState } from "../../context.js";
  import { timeAgo } from "../../utils/time.js";
  import { repoColor } from "../../utils/repo-color.js";
  import Chip from "../shared/Chip.svelte";
  import GitHubLabels from "../shared/GitHubLabels.svelte";

  const { pulls } = getStores();
  const hostState = getHostState();

  interface Props {
    pr: PullRequest;
    selected: boolean;
    showRepo: boolean;
    onclick: () => void;
    importAction?: Action | undefined;
  }

  const {
    pr,
    selected,
    showRepo,
    onclick,
    importAction,
  }: Props = $props();

  const repoSlug = $derived(
    `${pr.repo_owner ?? ""}/${pr.repo_name ?? ""}`,
  );

  function handleStarClick(e: MouseEvent): void {
    e.stopPropagation();
    void pulls.togglePRStar(
      pr.repo_owner ?? "",
      pr.repo_name ?? "",
      pr.Number,
      pr.Starred,
    );
  }

  let el: HTMLButtonElement;

  $effect(() => {
    if (selected && el) {
      el.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  });

  const kanbanLabels: Record<string, string> = {
    new: "New",
    reviewing: "Reviewing",
    waiting: "Waiting",
    awaiting_merge: "Ready",
  };

  const statusLabel = $derived(kanbanLabels[pr.KanbanStatus] ?? pr.KanbanStatus);
  const statusClass = $derived(`status-chip--${pr.KanbanStatus.replace("_", "-")}`);
  const ago = $derived(timeAgo(pr.LastActivityAt));
  const hasWorktree = $derived(
    (pr.worktree_links?.length ?? 0) > 0,
  );
  const isActiveWorktree = $derived.by(() => {
    const key = hostState.getActiveWorktreeKey?.();
    if (!key || !pr.worktree_links) return false;
    return pr.worktree_links.some(
      (l) => l.worktree_key === key,
    );
  });

  type PRState = "open" | "draft" | "closed" | "merged";
  const prState = $derived.by((): PRState => {
    if (pr.State === "merged") return "merged";
    if (pr.State === "closed") return "closed";
    if (pr.IsDraft) return "draft";
    return "open";
  });

  const stateColors: Record<PRState, string> = {
    open: "var(--accent-green)",
    draft: "var(--accent-amber)",
    closed: "var(--accent-red)",
    merged: "var(--accent-purple)",
  };

  const worktreeName = $derived(
    pr.worktree_links?.[0]?.worktree_branch ??
    pr.worktree_links?.[0]?.worktree_key,
  );

  const showImport = $derived(
    importAction &&
    !hasWorktree &&
    pr.State === "open",
  );
  const labels = $derived(pr.labels ?? []);

  function handleImportClick(e: MouseEvent): void {
    e.stopPropagation();
    importAction?.handler({
      surface: "pull-list",
      owner: pr.repo_owner ?? "",
      name: pr.repo_name ?? "",
      number: pr.Number,
    });
  }
</script>

<button
  class="pull-item"
  class:selected
  class:active-worktree={isActiveWorktree}
  bind:this={el}
  onclick={onclick}
>
  <p class="title">
    <span class="state-dot" style="background: {stateColors[prState]}"></span>
    {pr.Title}
  </p>
  {#if labels.length > 0}
    <GitHubLabels {labels} mode="compact" />
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
      #{pr.Number} · {pr.Author}
    </span>
    <span class="meta-right">
      {#if showImport}
        <span
          class="import-btn"
          role="button"
          tabindex="0"
          onclick={handleImportClick}
          onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); handleImportClick(e as unknown as MouseEvent); } }}
          title="Import to worktree"
        >
          <svg width="11" height="11" viewBox="0 0 16 16" fill="currentColor">
            <path d="M8 1a.75.75 0 01.75.75v6.19l1.72-1.72a.75.75 0 111.06 1.06l-3 3a.75.75 0 01-1.06 0l-3-3a.75.75 0 011.06-1.06l1.72 1.72V1.75A.75.75 0 018 1zM3.5 10a.75.75 0 01.75.75v1.5c0 .138.112.25.25.25h7a.25.25 0 00.25-.25v-1.5a.75.75 0 011.5 0v1.5A1.75 1.75 0 0111.5 14h-7A1.75 1.75 0 012.75 12.25v-1.5A.75.75 0 013.5 10z"/>
          </svg>
        </span>
      {/if}
      {#if hasWorktree && worktreeName}
        <span class="worktree-name" title="Linked to {worktreeName}">{worktreeName}</span>
      {:else if hasWorktree}
        <span class="worktree-badge" title="Linked to worktree">
          <svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor">
            <path d="M5 3.25a.75.75 0 11-1.5 0 .75.75 0 011.5 0zm0 2.122a2.25 2.25 0 10-1.5 0v.878A2.25 2.25 0 005.75 8.5h1.5v2.128a2.251 2.251 0 101.5 0V8.5h1.5a2.25 2.25 0 002.25-2.25v-.878a2.25 2.25 0 10-1.5 0v.878a.75.75 0 01-.75.75h-5.5a.75.75 0 01-.75-.75v-.878zM8 12.25a.75.75 0 11-1.5 0 .75.75 0 011.5 0zm3.25-9.75a.75.75 0 100 1.5.75.75 0 000-1.5z"/>
          </svg>
        </span>
      {/if}
      {#if pr.CIStatus === "success"}
        <span class="ci-icon ci-icon--success" title="CI passing">
          <svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor">
            <path d="M13.78 4.22a.75.75 0 010 1.06l-7.25 7.25a.75.75 0 01-1.06 0L2.22 9.28a.75.75 0 011.06-1.06L6 10.94l6.72-6.72a.75.75 0 011.06 0z"/>
          </svg>
        </span>
      {:else if pr.CIStatus === "failure"}
        <span class="ci-icon ci-icon--failure" title="CI failing">
          <svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor">
            <path d="M3.72 3.72a.75.75 0 011.06 0L8 6.94l3.22-3.22a.75.75 0 111.06 1.06L9.06 8l3.22 3.22a.75.75 0 11-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 01-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 010-1.06z"/>
          </svg>
        </span>
      {:else if pr.CIStatus === "pending"}
        <span class="ci-icon ci-icon--pending" title="CI pending">
          <svg width="10" height="10" viewBox="0 0 16 16">
            <circle cx="8" cy="8" r="4" fill="currentColor"/>
          </svg>
        </span>
      {/if}
      {#if pr.MergeableState === "dirty"}
        <span class="conflict-icon" title="Has merge conflicts">
          <!-- git-merge-conflict icon, ISC License, Copyright (c) Lucide Icons and Contributors -->
          <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M12 6h4a2 2 0 0 1 2 2v7" />
            <path d="M6 12v9" />
            <path d="M9 3 3 9" />
            <path d="M9 9 3 3" />
            <circle cx="18" cy="18" r="3" />
          </svg>
        </span>
      {/if}
      <span
        class="star-btn"
        role="button"
        tabindex="0"
        onclick={handleStarClick}
        onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); handleStarClick(e as unknown as MouseEvent); } }}
        title={pr.Starred ? "Unstar" : "Star"}
      >
        {#if pr.Starred}
          <svg class="star-icon star-icon--active" width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
            <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25z"/>
          </svg>
        {:else}
          <svg class="star-icon" width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
            <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25zm0 2.445L6.615 5.5a.75.75 0 01-.564.41l-3.097.45 2.24 2.184a.75.75 0 01.216.664l-.528 3.084 2.769-1.456a.75.75 0 01.698 0l2.77 1.456-.53-3.084a.75.75 0 01.216-.664l2.24-2.183-3.096-.45a.75.75 0 01-.564-.41L8 2.694z"/>
          </svg>
        {/if}
      </span>
      {#if statusLabel}
        <Chip size="sm" class={`status-chip ${statusClass}`}>{statusLabel}</Chip>
      {/if}
      <span class="time">{ago}</span>
    </span>
  </div>
</button>

<style>
  .pull-item {
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

  .pull-item:hover {
    background: var(--bg-surface-hover);
  }

  .pull-item.selected {
    background: var(--bg-inset);
    border-left-color: var(--accent-blue);
  }

  .pull-item.active-worktree {
    border-left-color: var(--accent-teal, var(--accent-green));
  }

  .pull-item.selected.active-worktree {
    border-left-color: var(--accent-teal, var(--accent-green));
  }

  .title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    margin-bottom: 4px;
  }

  .state-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
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

  :global(.status-chip) {
    flex-shrink: 0;
  }

  :global(.status-chip--new) {
    background: color-mix(in srgb, var(--kanban-new) 18%, transparent);
    color: var(--kanban-new);
  }

  :global(.status-chip--reviewing) {
    background: color-mix(in srgb, var(--accent-amber) 18%, transparent);
    color: var(--accent-amber);
  }

  :global(.status-chip--waiting) {
    background: color-mix(in srgb, var(--accent-purple) 18%, transparent);
    color: var(--accent-purple);
  }

  :global(.status-chip--awaiting-merge) {
    background: color-mix(in srgb, var(--accent-green) 18%, transparent);
    color: var(--accent-green);
  }

  .worktree-badge {
    display: flex;
    align-items: center;
    color: var(--accent-teal, var(--accent-green));
    flex-shrink: 0;
  }

  .worktree-name {
    font-size: var(--font-size-2xs);
    font-weight: 500;
    color: var(--accent-teal, var(--accent-green));
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 80px;
    flex-shrink: 1;
    min-width: 0;
  }

  .ci-icon {
    display: flex;
    align-items: center;
    flex-shrink: 0;
  }

  .ci-icon--success {
    color: var(--accent-green);
  }

  .ci-icon--failure {
    color: var(--accent-red);
  }

  .ci-icon--pending {
    color: var(--accent-amber);
  }

  .import-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    opacity: 0;
    transition: opacity 0.15s;
    cursor: pointer;
    color: var(--text-muted);
  }

  .pull-item:hover .import-btn {
    opacity: 0.6;
  }

  .import-btn:hover {
    opacity: 1 !important;
    color: var(--accent-blue);
  }

  .conflict-icon {
    display: flex;
    align-items: center;
    color: var(--accent-amber);
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

  .pull-item:hover .star-btn {
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

  :global(.mobile-main) .pull-item {
    min-height: calc(var(--focus-mobile-hit-target, 2.85rem) * 1.65);
    font-size: var(--font-size-mobile-body);
    padding: var(--focus-mobile-space-sm, 0.75rem) var(--focus-mobile-space-md, 1rem);
    border-bottom: thin solid var(--border-muted);
    border-left-width: 0.25rem;
  }

  :global(.mobile-main) .title {
    gap: var(--focus-mobile-space-xs, 0.5rem);
    margin-bottom: var(--focus-mobile-space-xs, 0.5rem);
    font-size: var(--font-size-mobile-title);
    line-height: 1.3;
    white-space: normal;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }

  :global(.mobile-main) .state-dot {
    width: 0.75rem;
    height: 0.75rem;
  }

  :global(.mobile-main) .repo-row {
    margin-bottom: var(--focus-mobile-space-xs, 0.5rem);
  }

  :global(.mobile-main) .meta-row {
    gap: var(--focus-mobile-space-sm, 0.75rem);
  }

  :global(.mobile-main) .meta-left,
  :global(.mobile-main) .time,
  :global(.mobile-main) .worktree-name {
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
