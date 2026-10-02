<!-- Copied from the shared console chat; imports and touch sizing adapted for Forge. -->
<script lang="ts">
  import { Ban, Bot, Check, ChevronDown, Clock, Wrench, X } from "@lucide/svelte"
  import { Spinner } from "@kenn-io/kit-ui"
  import ChatToolDetails from "./ChatToolDetails.svelte"
  import ChatContentBlock from "./ChatContentBlock.svelte"
  import { isImageContent } from "./chat-content.js"
  import { toolStatus, type ChatMessage } from "./chat-types.js"

  let {
    messages,
    childCounts = {},
  }: {
    messages: ChatMessage[]
    /** Tool calls made inside each sub-agent, keyed by its toolCallId. */
    childCounts?: Readonly<Record<string, number>>
  } = $props()
  const id = $props.id()
  let open = $state(false)
  let expanded = $state<Record<number, boolean>>({})
  function hasDetails(message: ChatMessage): boolean {
    return !!(
      message.kind ||
      message.locations?.length ||
      message.toolContent?.length ||
      message.rawInput ||
      message.rawOutput
    )
  }

  const failed = $derived(
    messages.filter((message) => toolStatus(message) === "failed").length,
  )
  const running = $derived(
    messages.some(
      (message) =>
        toolStatus(message) === "pending" ||
        toolStatus(message) === "in_progress",
    ),
  )
  const subagents = $derived(
    messages.filter((message) => message.subagent).length,
  )
  const summary = $derived(
    [
      `${messages.length} ${messages.length === 1 ? "tool" : "tools"}`,
      subagents ? `${subagents} ${subagents === 1 ? "sub-agent" : "sub-agents"}` : "",
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
    {#if running}<Spinner size={12} label="Tools running" />{:else if subagents}<Bot
        size={12}
        aria-hidden="true"
      />{:else}<Wrench
        size={12}
        aria-hidden="true"
      />{/if}
    <span>{summary}</span>
    <ChevronDown size={12} class="chevron" aria-hidden="true" />
  </button>
  {#if open}
    <ul id={`${id}-list`} class="list">
      {#each messages as message, index (index)}
        {@const status = toolStatus(message)}
        {@const expandable = hasDetails(message)}
        <li class={`status-${status}`}>
          {#snippet row()}
          <span class="icon" aria-hidden="true">
            {#if status === "failed"}<X size={12} />
            {:else if status === "in_progress"}<Spinner
                size={12}
                label=""
              />
            {:else if status === "pending"}<Clock size={12} />
            {:else if status === "cancelled"}<Ban size={12} />
            {:else}<Check size={12} />{/if}
          </span>
          <span class="tool-name">
            {#if message.subagent}
              {@const children = message.toolCallId ? (childCounts[message.toolCallId] ?? 0) : 0}
              <span class="subagent"
                ><Bot size={12} aria-hidden="true" />{children
                  ? `Sub-agent · ${children} ${children === 1 ? "tool call" : "tool calls"}`
                  : "Sub-agent"}</span
              >
            {/if}
            {message.text}
          </span>
          <span
            class="state"
            title={message.createdAt
              ? new Date(message.createdAt).toLocaleString()
              : undefined}
            >{status.replaceAll("_", " ")}</span
          >
          {/snippet}
          {#if expandable}
            <button
              type="button"
              class="row row--button"
              aria-expanded={!!expanded[index]}
              aria-controls={`${id}-tool-${index}`}
              onclick={() => (expanded[index] = !expanded[index])}
            >
              {@render row()}
            </button>
            {#if expanded[index]}
              <ChatToolDetails {message} id={`${id}-tool-${index}`} />
            {/if}
          {:else}
            <div class="row">{@render row()}</div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  {#each messages as message, messageIndex (messageIndex)}
    {#each message.toolContent ?? [] as item, contentIndex (contentIndex)}
      {#if item.content && isImageContent(item.content)}
        <ChatContentBlock content={item.content} />
      {/if}
    {/each}
  {/each}
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
  .row {
    display: grid;
    grid-template-columns: 14px minmax(0, 1fr) auto;
    align-items: baseline;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-5);
  }
  .row--button {
    width: 100%;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .row--button:hover {
    background: var(--bg-surface-hover);
  }
  .row--button:focus-visible {
    outline: var(--focus-ring);
    outline-offset: -2px;
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
  .subagent {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    margin-right: var(--space-3);
    padding: 0 var(--space-2);
    border: 1px solid color-mix(in srgb, var(--accent-purple) 35%, var(--border-default));
    border-radius: var(--radius-sm);
    color: var(--accent-purple);
    white-space: nowrap;
  }
  .state {
    color: var(--text-secondary);
    white-space: nowrap;
  }

  @media (pointer: coarse) {
    .chip { min-height: 44px; }
    .row--button { min-height: var(--mobile-chrome-hit-target); }
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
