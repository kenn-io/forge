<script lang="ts">
  import { Spinner } from "@kenn-io/kit-ui";
  import ChevronDown from "@lucide/svelte/icons/chevron-down";
  import Circle from "@lucide/svelte/icons/circle";
  import CircleCheck from "@lucide/svelte/icons/circle-check";
  import CircleDot from "@lucide/svelte/icons/circle-dot";
  import ListPlus from "@lucide/svelte/icons/list-plus";
  import ListTodo from "@lucide/svelte/icons/list-todo";
  import X from "@lucide/svelte/icons/x";
  import type { ChatState } from "./chat-types.js";
  import type { RunningSubagent } from "./chat-subagents.js";

  type PlanEntry = NonNullable<ChatState["plan"]>[number];
  type QueuedPrompt = NonNullable<ChatState["queue"]>[number];
  type Panel = "plan" | "subagents" | "queue";

  let { plan, subagents, queue, queuePaused, disabled, suppressed = false, onresume, onunqueue }: {
    plan: readonly PlanEntry[];
    subagents: readonly RunningSubagent[];
    queue: readonly QueuedPrompt[];
    queuePaused: boolean;
    disabled: boolean;
    /** Hides an open panel while another popover, such as the slash menu, owns this space. */
    suppressed?: boolean;
    onresume: () => void;
    onunqueue: (id: string) => void;
  } = $props();

  const id = $props.id();
  let open = $state<Panel | null>(null);
  const done = $derived(plan.filter((entry) => entry.status === "completed").length);
  const statusLabels: Record<string, string> = { completed: "Done", in_progress: "In progress", pending: "Pending" };
  // A panel whose content disappeared closes rather than lingering empty.
  const shown = $derived(
    suppressed ? null
    : open === "plan" && plan.length ? "plan"
    : open === "subagents" && subagents.length ? "subagents"
    : open === "queue" && queue.length ? "queue"
    : null,
  );

  function toggle(panel: Panel) {
    open = open === panel ? null : panel;
  }
</script>

<!-- One quiet line of status above the composer; details open on demand so
     the running turn never pushes the draft out of view. -->
<div class="rail">
  {#if shown === "plan"}
    <ol class="panel" id={`${id}-plan`} aria-label="Plan">
      {#each plan as entry, index (index)}
        <li class={`plan-entry status-${entry.status}`}>
          <span class="plan-icon" aria-hidden="true">
            {#if entry.status === "completed"}<CircleCheck size={13} />
            {:else if entry.status === "in_progress"}<CircleDot size={13} />
            {:else}<Circle size={13} />{/if}
          </span>
          <span class="kit-sr-only">{statusLabels[entry.status] ?? entry.status}: </span>
          <span class="plan-text">{entry.content}</span>
        </li>
      {/each}
    </ol>
  {:else if shown === "subagents"}
    <ul class="panel" id={`${id}-subagents`} aria-label="Running sub-agents">
      {#each subagents as subagent (subagent.toolCallId)}
        <li class="row">
          <span class="row-text" title={subagent.title}>{subagent.title}</span>
          <span class="row-meta">{subagent.children} {subagent.children === 1 ? "tool call" : "tool calls"}</span>
        </li>
      {/each}
    </ul>
  {:else if shown === "queue"}
    <div class="panel" id={`${id}-queue`}>
      <ol aria-label="Queued messages">
        {#each queue as queued (queued.id)}
          <li class="row">
            <span class="row-text" title={queued.text}>{queued.text}{#if queued.images?.length}{" "}({queued.images.length} {queued.images.length === 1 ? "image" : "images"}){/if}</span>
            <button type="button" class="remove" aria-label="Remove queued message" title="Remove" {disabled} onclick={() => onunqueue(queued.id)}><X size={13} /></button>
          </li>
        {/each}
      </ol>
    </div>
  {/if}
  <div class="chips">
    {#if plan.length}
      <button type="button" class="tb-chip" aria-expanded={shown === "plan"} aria-controls={`${id}-plan`} onclick={() => toggle("plan")}>
        <ListTodo size={13} aria-hidden="true" />
        <span>Plan <span class="count">{done}/{plan.length}</span></span>
        <ChevronDown size={12} class="chevron" aria-hidden="true" />
      </button>
    {/if}
    {#if subagents.length}
      <button type="button" class="tb-chip" aria-expanded={shown === "subagents"} aria-controls={`${id}-subagents`} onclick={() => toggle("subagents")}>
        <Spinner size={12} label="" />
        <span>{subagents.length} {subagents.length === 1 ? "sub-agent" : "sub-agents"} running</span>
        <ChevronDown size={12} class="chevron" aria-hidden="true" />
      </button>
    {/if}
    {#if queue.length}
      <button type="button" class="tb-chip" aria-expanded={shown === "queue"} aria-controls={`${id}-queue`} onclick={() => toggle("queue")}>
        <ListPlus size={13} aria-hidden="true" />
        <span>{queue.length} queued{#if queuePaused}<span class="paused"> · paused</span>{/if}</span>
        <ChevronDown size={12} class="chevron" aria-hidden="true" />
      </button>
      {#if queuePaused}
        <button type="button" class="tb-chip resume" {disabled} onclick={onresume}>Resume queue</button>
      {/if}
    {/if}
  </div>
</div>

<style>
  .rail {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1);
  }
  .count {
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
  }
  .paused {
    color: var(--accent-amber);
  }
  .resume {
    color: var(--accent-blue);
  }
  .rail :global(.chevron) {
    color: var(--text-muted);
    transition: transform var(--transition-fast) ease-out;
  }
  .rail [aria-expanded="true"] :global(.chevron) {
    transform: rotate(180deg);
  }
  .panel {
    max-width: 36rem;
  }
  .panel,
  .panel ol {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    max-height: 9rem;
    overflow-y: auto;
    scrollbar-width: thin;
    margin: 0;
    padding: 0 var(--space-3);
    list-style: none;
  }
  /* Rows start where the chip labels start: past the chip padding and icon. */
  .panel li {
    padding-left: calc(13px + var(--space-2));
  }
  .panel .plan-entry {
    padding-left: 0;
  }
  .panel ol {
    padding: 0;
  }
  .plan-entry,
  .row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
    min-height: 22px;
  }
  .plan-icon {
    display: inline-flex;
    flex: none;
    color: var(--text-muted);
  }
  .plan-text {
    min-width: 0;
    color: var(--text-primary);
    overflow-wrap: anywhere;
  }
  .status-in_progress .plan-icon {
    color: var(--accent-blue);
  }
  .status-completed .plan-icon {
    color: var(--accent-green);
  }
  .status-completed .plan-text {
    color: var(--text-muted);
  }
  .row-text {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
  }
  .row-meta {
    flex: none;
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
  }
  .remove {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: 22px;
    height: 22px;
    padding: 0;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .remove:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
  .remove:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 1px;
  }
  .remove:disabled {
    opacity: var(--opacity-disabled);
    cursor: default;
  }
  @media (pointer: coarse) {
    .rail {
      font-size: var(--font-size-sm);
    }
    .remove,
    .plan-entry,
    .row {
      min-height: var(--mobile-chrome-hit-target);
    }
    .remove {
      width: var(--mobile-chrome-hit-target);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .rail :global(.chevron) {
      transition: none;
    }
  }
</style>
