<script lang="ts">
  import { Chip, formatRelativeTime, IconButton } from "@kenn-io/kit-ui";
  import AlertTriangleIcon from "@lucide/svelte/icons/alert-triangle";
  import XIcon from "@lucide/svelte/icons/x";
  import type { WorklistItem } from "../../api/worklists.js";
  import { worklistChangeMarkers } from "./worklist-items.js";

  interface Props {
    item: WorklistItem;
    createdBy: string;
    onClose: () => void;
  }

  let { item, createdBy, onClose }: Props = $props();

  const markers = $derived(worklistChangeMarkers(item));
</script>

<section class="worklist-context" aria-label="Worklist item context">
  <div class="worklist-context-head">
    {#if item.score !== null}
      <Chip size="sm" tone="neutral" uppercase={false}>Score {item.score}</Chip>
    {/if}
    {#if item.section}
      <Chip size="sm" tone="muted" uppercase={false}>{item.section}</Chip>
    {/if}
    <span class="worklist-context-ref">{item.repo.owner}/{item.repo.name}#{item.number}</span>
    <IconButton size="sm" ariaLabel="Close worklist item" onclick={onClose}>
      <XIcon size={14} aria-hidden="true" />
    </IconButton>
  </div>
  {#if item.note}
    <p class="worklist-context-note">{item.note}</p>
  {/if}
  {#if item.changes.new_commits}
    <p class="worklist-context-aside">Review notes may cover an older head</p>
  {/if}
  {#if item.caveat}
    <p class="worklist-context-caveat"><AlertTriangleIcon size={12} aria-hidden="true" />{item.caveat}</p>
  {/if}
  <div class="worklist-context-divider">
    <span>{createdBy.trim() || "An agent"} wrote this item {formatRelativeTime(item.written_at)}</span>
  </div>
  {#if markers.length > 0}
    <ul class="worklist-context-events" aria-label="Changes since">
      {#each markers as marker (marker.label)}
        <li>
          <Chip size="xs" tone="info" uppercase={false}>New</Chip>
          <span>{marker.label}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .worklist-context {
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-4) var(--space-5);
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .worklist-context-head {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }

  .worklist-context-ref {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .worklist-context-note,
  .worklist-context-aside,
  .worklist-context-caveat {
    margin: 0;
    overflow-wrap: anywhere;
  }

  .worklist-context-aside {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .worklist-context-caveat {
    display: flex;
    align-items: baseline;
    gap: var(--space-2);
    color: var(--text-secondary);
  }

  .worklist-context-divider {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .worklist-context-divider::before,
  .worklist-context-divider::after {
    content: "";
    flex: 1;
    border-top: 1px solid var(--border-muted);
  }

  .worklist-context-events {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2) var(--space-5);
    margin: 0;
    padding: 0;
    list-style: none;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .worklist-context-events li {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
  }
</style>
