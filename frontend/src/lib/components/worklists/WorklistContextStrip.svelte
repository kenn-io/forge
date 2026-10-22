<script lang="ts">
  import { Chip, formatRelativeTime, IconButton } from "@kenn-io/kit-ui";
  import CheckIcon from "@lucide/svelte/icons/check";
  import XIcon from "@lucide/svelte/icons/x";
  import type { WorklistItem } from "../../api/worklists.js";
  import WorklistCaveatIcon from "./WorklistCaveatIcon.svelte";
  import { worklistChangeMarkers } from "./worklist-items.js";

  interface Props {
    item: WorklistItem;
    createdBy: string;
    onToggleHandled: () => void;
    onClose: () => void;
  }

  let { item, createdBy, onToggleHandled, onClose }: Props = $props();

  const markers = $derived(worklistChangeMarkers(item));
  const handled = $derived(item.handled_at !== null);
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
    <button
      type="button"
      class={["worklist-context-handled", { "worklist-context-handled--on": handled }]}
      aria-pressed={handled}
      onclick={onToggleHandled}
    >
      <CheckIcon size={13} strokeWidth={2.4} aria-hidden="true" />
      {handled ? "Handled" : "Mark handled"}
    </button>
    <IconButton size="sm" ariaLabel="Close worklist item" onclick={onClose}>
      <XIcon size={14} aria-hidden="true" />
    </IconButton>
  </div>
  <!-- Long notes scroll inside the strip so the detail pane keeps its room. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="worklist-context-body" role="region" aria-label="Item notes" tabindex="0">
    {#if item.note}
      <p class="worklist-context-note">{item.note}</p>
    {/if}
    {#if item.changes.new_commits}
      <p class="worklist-context-aside">Review notes may cover an older head</p>
    {/if}
    {#if item.caveat}
      <p class="worklist-context-caveat"><WorklistCaveatIcon level={item.caveat_level} size={12} />{item.caveat}</p>
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
  </div>
</section>

<style>
  .worklist-context {
    flex-shrink: 0;
    max-height: 40%;
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
    flex-shrink: 0;
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

  .worklist-context-handled {
    display: inline-flex;
    flex-shrink: 0;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) var(--space-3);
    border: 1px solid color-mix(in srgb, var(--accent-green) 45%, var(--border-default));
    border-radius: var(--radius-sm, 4px);
    background: color-mix(in srgb, var(--accent-green) 12%, transparent);
    color: var(--accent-green);
    font: inherit;
    font-size: var(--font-size-xs);
    font-weight: 500;
    white-space: nowrap;
    cursor: pointer;
  }

  .worklist-context-handled--on {
    border-color: var(--border-muted);
    background: transparent;
    color: var(--text-secondary);
  }

  .worklist-context-handled:focus-visible {
    outline: none;
    box-shadow: 0 0 0 1px var(--accent-blue);
  }

  .worklist-context-body {
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  .worklist-context-body:focus-visible {
    outline: none;
    box-shadow: inset 0 0 0 1px var(--accent-blue);
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
