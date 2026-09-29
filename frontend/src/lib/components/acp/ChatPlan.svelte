<script lang="ts">
  import ChevronDown from "@lucide/svelte/icons/chevron-down";
  import Circle from "@lucide/svelte/icons/circle";
  import CircleCheck from "@lucide/svelte/icons/circle-check";
  import CircleDot from "@lucide/svelte/icons/circle-dot";
  import ListTodo from "@lucide/svelte/icons/list-todo";
  import type { ChatState } from "./chat-types.js";

  type PlanEntry = NonNullable<ChatState["plan"]>[number];
  let { entries }: { entries: readonly PlanEntry[] } = $props();
  const id = $props.id();
  let open = $state(true);
  const done = $derived(entries.filter((entry) => entry.status === "completed").length);
  const statusLabels: Record<string, string> = { completed: "Done", in_progress: "In progress", pending: "Pending" };
</script>

<section class="plan" aria-label="Plan">
  <button type="button" class="toggle" aria-expanded={open} aria-controls={`${id}-entries`} onclick={() => (open = !open)}>
    <ListTodo size={12} aria-hidden="true" />
    <span class="title">Plan</span>
    <span class="count">{done}/{entries.length} done</span>
    <ChevronDown size={12} class="chevron" aria-hidden="true" />
  </button>
  {#if open}
    <ol id={`${id}-entries`}>
      {#each entries as entry, index (index)}
        <li class={`status-${entry.status}`}>
          <span class="icon" aria-hidden="true">
            {#if entry.status === "completed"}<CircleCheck size={12} />
            {:else if entry.status === "in_progress"}<CircleDot size={12} />
            {:else}<Circle size={12} />{/if}
          </span>
          <span class="kit-sr-only">{statusLabels[entry.status] ?? entry.status}: </span>
          <span class="content">{entry.content}</span>
          {#if entry.priority === "high"}<span class="priority">high</span>{/if}
        </li>
      {/each}
    </ol>
  {/if}
</section>

<style>
  .plan {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    padding: var(--space-2) var(--space-4);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }
  .toggle {
    display: inline-flex;
    align-items: center;
    align-self: flex-start;
    gap: var(--space-2);
    min-height: 22px;
    padding: 0 var(--space-2);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .toggle:hover {
    color: var(--text-primary);
  }
  .toggle:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 1px;
  }
  .title {
    color: var(--text-primary);
  }
  .count {
    font-variant-numeric: tabular-nums;
  }
  .toggle :global(.chevron) {
    transition: transform var(--transition-fast) ease-out;
  }
  .toggle[aria-expanded="true"] :global(.chevron) {
    transform: rotate(180deg);
  }
  ol {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    max-height: 9rem;
    overflow: auto;
    margin: 0;
    padding: 0 0 0 var(--space-2);
    list-style: none;
  }
  li {
    display: flex;
    align-items: baseline;
    gap: var(--space-3);
    min-width: 0;
  }
  .icon {
    display: inline-flex;
    align-self: center;
    flex: none;
  }
  .status-in_progress .icon,
  .status-in_progress .content {
    color: var(--accent-blue);
  }
  .status-completed .icon {
    color: var(--accent-green);
  }
  .status-completed .content {
    color: var(--text-muted);
    text-decoration: line-through;
  }
  .content {
    min-width: 0;
    overflow-wrap: anywhere;
    color: var(--text-primary);
  }
  .priority {
    flex: none;
    color: var(--accent-amber);
  }
  @media (pointer: coarse) {
    .plan {
      font-size: var(--font-size-sm);
    }
    .toggle {
      min-height: var(--mobile-chrome-hit-target);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .toggle :global(.chevron) {
      transition: none;
    }
  }
</style>
