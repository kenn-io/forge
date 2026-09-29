<script lang="ts">
  import type { AgentCommand } from "./chat-types.js";

  let { id, commands, active, onpick }: {
    id: string;
    commands: AgentCommand[];
    active: number;
    onpick: (command: AgentCommand) => void;
  } = $props();

  // Keep the keyboard-highlighted row visible as the highlight moves.
  $effect(() => {
    document.getElementById(`${id}-${active}`)?.scrollIntoView?.({ block: "nearest" });
  });
</script>

<!-- Focus stays in the composer: it owns the keyboard and points at the
     highlighted row with aria-activedescendant. -->
<!-- kit-ui-check-ignore: slash commands apply only to the draft's first token and stay hidden when nothing matches; kit MentionTextarea opens on any word-boundary trigger (paths like " /tmp") and shows an empty state -->
<ul class="command-menu kit-popover-card" {id} role="listbox" aria-label="Slash commands">
  {#each commands as command, index (command.name)}
    <li
      id={`${id}-${index}`}
      class={["command", index === active && "command--active"]}
      role="option"
      aria-selected={index === active}
      tabindex="-1"
      title={command.description || undefined}
      onpointerdown={(event) => event.preventDefault()}
      onclick={() => onpick(command)}
      onkeydown={(event) => { if (event.key === "Enter") onpick(command); }}
    >
      <span class="command__name">/{command.name}</span>
      {#if command.description}<span class="command__description">{command.description}</span>{/if}
    </li>
  {/each}
</ul>

<style>
  /* A compact popover sized to its content and anchored to the composer's
     edge, so a short command list never spans the whole composer. */
  .command-menu {
    position: absolute;
    left: 0;
    bottom: calc(100% + var(--space-2));
    z-index: var(--z-popover);
    width: max-content;
    min-width: 16rem;
    max-width: min(34rem, 100%);
    max-height: min(18rem, 40vh);
    overflow-y: auto;
    overscroll-behavior: contain;
    scrollbar-width: thin;
    scrollbar-color: var(--border-default) transparent;
    margin: 0;
    padding: var(--space-1);
    list-style: none;
  }
  .command {
    display: flex;
    align-items: baseline;
    gap: var(--space-3);
    min-width: 0;
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    line-height: 1.25;
    cursor: pointer;
  }
  .command:hover { background: var(--bg-surface-hover); }
  .command--active,
  .command--active:hover { background: color-mix(in srgb, var(--accent-blue) 12%, transparent); }
  .command__name {
    flex: none;
    font-weight: 500;
    color: var(--text-primary);
  }
  .command__description {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-muted);
  }
  @media (pointer: coarse) {
    .command { align-items: center; min-height: var(--mobile-chrome-hit-target); }
    .command__name { font-size: var(--font-size-touch-field); }
  }
</style>
