<script lang="ts">
  import type { PullRequest } from "../../api/types.js";
  import { Card, formatRelativeTime } from "@kenn-io/kit-ui";
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

<div
  class="kanban-card-drag"
  role="presentation"
  draggable="true"
  ondragstart={handleDragStart}
>
  <Card level="raised" padding="sm" class="kanban-card" {onclick}>
    <p class="card-title">{pr.Title}</p>
    <p class="card-meta">{repoLabel} #{pr.Number}</p>
    <div class="card-footer">
      <span class="card-author">{pr.Author}</span>
      <span class="card-time">{ago}</span>
    </div>
  </Card>
</div>

<style>
  .kanban-card-drag {
    cursor: grab;
  }

  .kanban-card-drag:active {
    cursor: grabbing;
    opacity: 0.7;
  }

  :global(.kanban-card.kit-card) {
    width: 100%;
    cursor: grab;
  }

  .kanban-card-drag:active :global(.kanban-card.kit-card) {
    cursor: grabbing;
  }

  :global(.kanban-card:hover) {
    border-color: var(--border-default);
    box-shadow: var(--shadow-md);
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
