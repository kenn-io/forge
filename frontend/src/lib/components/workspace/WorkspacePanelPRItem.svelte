<script lang="ts">
  import type { PullRequest } from "../../api/types.js";

  interface Props {
    pull: PullRequest;
    onSelect: (number: number) => void;
    onCreateWorktree: (number: number) => void;
    onNavigateWorktree: (worktreeKey: string) => void;
  }

  const {
    pull,
    onSelect,
    onCreateWorktree,
    onNavigateWorktree,
  }: Props = $props();

  type PRState = "open" | "draft" | "closed" | "merged";

  const prState = $derived.by((): PRState => {
    if (pull.State === "merged") return "merged";
    if (pull.State === "closed") return "closed";
    if (pull.IsDraft) return "draft";
    return "open";
  });

  const stateLabel: Record<PRState, string> = {
    open: "Open",
    draft: "Draft",
    closed: "Closed",
    merged: "Merged",
  };

  const hasWorktree = $derived(
    (pull.worktree_links?.length ?? 0) > 0,
  );

  function handleCreateClick(e: MouseEvent): void {
    e.stopPropagation();
    onCreateWorktree(pull.Number);
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="panel-pr-item"
  role="button"
  tabindex="0"
  onclick={() => onSelect(pull.Number)}
  onkeydown={(e) => {
    if (e.target !== e.currentTarget) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect(pull.Number);
    }
  }}
>
  <div class="title-row">
    <span class="pr-number">#{pull.Number}</span>
    <span class="pr-title">{pull.Title}</span>
  </div>
  <div class="meta-row">
    <span class="state-pill state-pill--{prState}">
      {stateLabel[prState]}
    </span>
    {#if hasWorktree}
      {#each pull.worktree_links ?? [] as link (link.worktree_key)}
        <button
          class="wt-chip"
          onclick={(e) => {
            e.stopPropagation();
            onNavigateWorktree(link.worktree_key);
          }}
          title="Open worktree: {link.worktree_key}"
        >
          {link.worktree_key.split("/").pop()}
        </button>
      {/each}
    {:else if pull.State === "open"}
      <button
        class="create-wt-btn"
        onclick={handleCreateClick}
      >+ Worktree</button>
    {/if}
  </div>
</div>

<style>
  .panel-pr-item {
    display: block;
    width: 100%;
    text-align: left;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
    cursor: pointer;
    transition: background 0.1s;
  }

  .panel-pr-item:hover {
    background: var(--bg-surface-hover);
  }

  .title-row {
    display: flex;
    align-items: baseline;
    gap: 6px;
    margin-bottom: 4px;
  }

  .pr-number {
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .pr-title {
    font-size: var(--font-size-sm);
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .meta-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .state-pill {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    padding: 1px 6px;
    border-radius: 8px;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    flex-shrink: 0;
  }

  .state-pill--open {
    background: color-mix(
      in srgb,
      var(--accent-green) 18%,
      transparent
    );
    color: var(--accent-green);
  }

  .state-pill--draft {
    background: color-mix(
      in srgb,
      var(--accent-amber) 18%,
      transparent
    );
    color: var(--accent-amber);
  }

  .state-pill--closed {
    background: color-mix(
      in srgb,
      var(--accent-red) 18%,
      transparent
    );
    color: var(--accent-red);
  }

  .state-pill--merged {
    background: color-mix(
      in srgb,
      var(--accent-purple) 18%,
      transparent
    );
    color: var(--accent-purple);
  }

  .wt-chip {
    font-size: var(--font-size-2xs);
    padding: 1px 5px;
    border: 1px solid var(--border-muted);
    border-radius: 3px;
    background: var(--bg-inset);
    color: var(--text-muted);
    cursor: pointer;
    visibility: hidden;
  }

  .panel-pr-item:hover .wt-chip,
  .panel-pr-item:focus-within .wt-chip {
    visibility: visible;
  }

  .wt-chip:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .create-wt-btn {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    padding: 1px 6px;
    border: 1px solid var(--border-muted);
    border-radius: 6px;
    background: var(--bg-inset);
    color: var(--text-muted);
    cursor: pointer;
    transition:
      color 0.1s,
      border-color 0.1s;
    white-space: nowrap;
  }

  .create-wt-btn:hover {
    color: var(--accent-blue);
    border-color: var(--accent-blue);
  }
</style>
