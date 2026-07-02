<script lang="ts">
  import type { PullRequest } from "../../api/types.js";
  import { formatRelativeTime } from "@kenn-io/kit-ui";
  import { kanbanDragPayloadFromPull } from "./drag.js";

  interface Props {
    pr: PullRequest;
    onclick: () => void;
  }

  const { pr, onclick }: Props = $props();

  const ago = $derived(formatRelativeTime(pr.LastActivityAt));
  const repoLabel = $derived(pr.repo.name);

  function handleDragStart(e: DragEvent): void {
    if (!e.dataTransfer) return;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", JSON.stringify(
      kanbanDragPayloadFromPull(pr),
    ));
  }
</script>

<button
  class="kanban-card"
  {onclick}
  draggable="true"
  ondragstart={handleDragStart}
>
  <p class="card-title">{pr.Title}</p>
  <p class="card-meta">{repoLabel} #{pr.Number}</p>
  <div class="card-footer">
    <span class="card-author">{pr.Author}</span>
    <span class="card-time">{ago}</span>
  </div>
</button>

<style>
  .kanban-card {
    display: block;
    width: 100%;
    text-align: left;
    padding: 10px 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    cursor: grab;
    transition: box-shadow 0.15s, border-color 0.15s, opacity 0.15s;
    box-shadow: var(--shadow-sm);
  }

  .kanban-card:hover {
    box-shadow: var(--shadow-md);
    border-color: var(--border-default);
  }

  .kanban-card:active {
    cursor: grabbing;
    opacity: 0.7;
  }

  .card-title {
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-primary);
    line-height: 1.4;
    margin-bottom: 4px;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .card-meta {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    margin-bottom: 8px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .card-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .card-author {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .card-time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }
</style>
