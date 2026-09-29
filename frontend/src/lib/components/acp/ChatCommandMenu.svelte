<script lang="ts">
  import type { AgentCommand } from "./chat-types.js";

  let { id, commands, active, onpick }: {
    id: string;
    commands: AgentCommand[];
    active: number;
    onpick: (command: AgentCommand) => void;
  } = $props();
</script>

<!-- Focus stays in the composer: it owns the keyboard and points at the
     highlighted row with aria-activedescendant. -->
<!-- kit-ui-check-ignore: slash commands apply only to the draft's first token and stay hidden when nothing matches; kit MentionTextarea opens on any word-boundary trigger (paths like " /tmp") and shows an empty state -->
<ul class="command-menu" {id} role="listbox" aria-label="Slash commands">
  {#each commands as command, index (command.name)}
    <li
      id={`${id}-${index}`}
      class={["command", index === active && "command--active"]}
      role="option"
      aria-selected={index === active}
      tabindex="-1"
      onpointerdown={(event) => event.preventDefault()}
      onclick={() => onpick(command)}
      onkeydown={(event) => { if (event.key === "Enter") onpick(command); }}
    >
      <span class="command__head">
        <span class="command__name">/{command.name}</span>
        {#if command.inputHint}<span class="command__hint">{command.inputHint}</span>{/if}
      </span>
      {#if command.description}<span class="command__description">{command.description}</span>{/if}
    </li>
  {/each}
</ul>

<style>
  .command-menu {
    position: absolute;
    inset-inline: var(--space-5);
    bottom: calc(100% - var(--space-2));
    z-index: 2;
    max-height: min(18rem, 40vh);
    overflow: auto;
    overscroll-behavior: contain;
    margin: 0;
    padding: var(--space-1);
    list-style: none;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-md);
  }
  .command {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-sm);
    cursor: pointer;
    min-width: 0;
  }
  .command:hover, .command--active { background: var(--bg-surface-hover); }
  .command--active { box-shadow: inset 2px 0 0 var(--accent-blue); }
  .command__head { display: flex; align-items: baseline; gap: var(--space-3); min-width: 0; }
  .command__name { flex: none; font-family: var(--font-mono); font-size: var(--font-size-sm); color: var(--text-primary); }
  .command__hint, .command__description {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-xs);
  }
  .command__hint { color: var(--text-muted); }
  .command__description { color: var(--text-secondary); }
  @media (pointer: coarse) {
    .command-menu { inset-inline: var(--space-4); }
    .command { justify-content: center; min-height: var(--mobile-chrome-hit-target); }
    .command__name { font-size: var(--font-size-touch-field); }
    .command__hint, .command__description { font-size: var(--font-size-sm); }
  }
</style>
