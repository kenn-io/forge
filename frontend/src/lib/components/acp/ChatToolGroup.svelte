<!-- Copied from the shared console chat; imports and touch sizing adapted for Forge. -->
<script lang="ts">
  import { Ban, Check, ChevronDown, Clock, Wrench, X } from "@lucide/svelte"
  import { Spinner } from "@kenn-io/kit-ui"
  import type { ChatMessage } from "./chat-types.js"

  let { messages }: { messages: ChatMessage[] } = $props()
  const id = $props.id()
  let open = $state(false)

  const failed = $derived(
    messages.filter((message) => message.status === "failed").length,
  )
  const running = $derived(
    messages.some(
      (message) =>
        message.status === "pending" || message.status === "in_progress",
    ),
  )
  const summary = $derived(
    [
      `${messages.length} ${messages.length === 1 ? "tool" : "tools"}`,
      failed ? `${failed} failed` : "",
      running ? "running" : "",
    ]
      .filter(Boolean)
      .join(" · "),
  )
</script>

<div class="tool-group">
  <button
    type="button"
    class="chip kit-control-states"
    class:failed={failed > 0}
    aria-expanded={open}
    aria-controls={`${id}-list`}
    onclick={() => (open = !open)}
  >
    {#if running}<Spinner size={12} label="Tools running" />{:else}<Wrench
        size={12}
        aria-hidden="true"
      />{/if}
    <span>{summary}</span>
    <ChevronDown size={12} class="chevron" aria-hidden="true" />
  </button>
  {#if open}
    <ul id={`${id}-list`} class="list">
      {#each messages as message, index (index)}
        <li class={`status-${message.status ?? "completed"}`}>
          <span class="icon" aria-hidden="true">
            {#if message.status === "failed"}<X size={12} />
            {:else if message.status === "in_progress"}<Spinner
                size={12}
                label=""
              />
            {:else if message.status === "pending"}<Clock size={12} />
            {:else if message.status === "cancelled"}<Ban size={12} />
            {:else}<Check size={12} />{/if}
          </span>
          <span class="tool-name">{message.text}</span>
          <span
            class="state"
            title={message.createdAt
              ? new Date(message.createdAt).toLocaleString()
              : undefined}
            >{message.status?.replaceAll("_", " ") ?? "completed"}</span
          >
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .tool-group {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--space-3);
    min-width: 0;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
    min-height: 24px;
    padding: 0 var(--space-4);
    border: 1px solid var(--border-default);
    border-radius: 999px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
    cursor: pointer;
    transition:
      background var(--transition-fast) ease-out,
      border-color var(--transition-fast) ease-out,
      color var(--transition-fast) ease-out;
  }
  .chip:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
  .chip:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 2px;
  }
  .chip[aria-expanded="true"] {
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }
  .chip.failed {
    border-color: color-mix(
      in srgb,
      var(--accent-red) 30%,
      var(--border-default)
    );
  }
  .chip :global(.chevron) {
    transition: transform var(--transition-fast) ease-out;
  }
  .chip[aria-expanded="true"] :global(.chevron) {
    transform: rotate(180deg);
  }

  .list {
    align-self: stretch;
    margin: 0;
    padding: var(--space-2) 0;
    list-style: none;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    font-size: var(--font-size-xs);
  }
  li {
    display: grid;
    grid-template-columns: 14px minmax(0, 1fr) auto;
    align-items: baseline;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-5);
  }
  .icon {
    display: inline-flex;
    align-self: center;
    color: var(--accent-green);
  }
  .status-failed .icon,
  .status-failed .state {
    color: var(--accent-red);
  }
  .status-pending .icon,
  .status-in_progress .icon,
  .status-cancelled .icon {
    color: var(--text-secondary);
  }
  .tool-name {
    min-width: 0;
    overflow-wrap: anywhere;
    color: var(--text-primary);
  }
  .state {
    color: var(--text-secondary);
    white-space: nowrap;
  }

  @media (pointer: coarse) {
    .chip { min-height: 44px; }
  }

  @media (prefers-reduced-motion: reduce) {
    .chip,
    .chip :global(.chevron) {
      transition: none;
    }
    .tool-group :global(.kit-spinner) {
      animation: none;
    }
  }
</style>
