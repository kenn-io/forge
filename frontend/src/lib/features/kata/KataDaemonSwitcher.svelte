<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";

  import type { KataDaemonInfo } from "../../api/kata/daemons.js";

  interface Props {
    daemons: KataDaemonInfo[];
    activeId: string | undefined;
    onSelect: (id: string) => void;
  }

  let { daemons, activeId, onSelect }: Props = $props();

  let open = $state(false);
  const active = $derived(
    daemons.find((daemon) => daemon.id === activeId) ??
      daemons.find((daemon) => daemon.default) ??
      daemons[0],
  );

  function choose(id: string): void {
    open = false;
    if (id !== active?.id) onSelect(id);
  }
</script>

<div class="daemon-switcher">
  <button
    type="button"
    class="daemon-chip"
    data-testid="daemon-chip"
    aria-label={`Kata daemon: ${active?.id ?? "default"}`}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <span class={`dot dot--${active?.health ?? "down"}`} aria-hidden="true"></span>
    <span class="chip-label">{active?.id ?? "kata"}</span>
    <ChevronDownIcon size={12} strokeWidth={2} aria-hidden="true" />
  </button>

  {#if open}
    <div class="daemon-menu" data-align="start" role="menu" aria-label="Configured Kata daemons">
      <p class="menu-head">Kata daemon</p>
      {#each daemons as daemon (daemon.id)}
        <button
          type="button"
          class="daemon-row"
          class:selected={daemon.id === active?.id}
          data-testid={`daemon-row-${daemon.id}`}
          role="menuitemradio"
          aria-checked={daemon.id === active?.id}
          onclick={() => choose(daemon.id)}
        >
          <span class={`dot dot--${daemon.health}`} aria-hidden="true"></span>
          <span class="row-name">{daemon.id}</span>
          {#if daemon.id === active?.id}
            <CheckIcon class="check" size={13} strokeWidth={2} aria-hidden="true" />
          {:else if daemon.health === "auth_required"}
            <span class="row-meta">needs auth</span>
          {:else if daemon.health === "down"}
            <span class="row-meta">unreachable</span>
          {/if}
          {#if daemon.hint}
            <span class="row-hint">{daemon.hint}</span>
          {/if}
        </button>
      {/each}
      <p class="menu-foot">Configured Kata daemons</p>
    </div>
  {/if}
</div>

<style>
  .daemon-switcher {
    position: relative;
    display: inline-flex;
  }

  .daemon-chip {
    height: 28px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0 8px;
    font-size: var(--font-size-sm);
    line-height: 1;
    cursor: pointer;
  }

  .daemon-chip:hover {
    background: var(--bg-surface-hover);
  }

  .chip-label {
    min-width: 0;
    max-width: 120px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: var(--radius-pill);
    flex: none;
  }

  .dot--connected {
    background: var(--accent-green);
  }

  .dot--auth_required {
    background: var(--accent-amber);
  }

  .dot--down {
    background: var(--text-faint);
  }

  .daemon-menu {
    position: absolute;
    top: calc(100% + 6px);
    left: 0;
    right: auto;
    z-index: 30;
    width: min(280px, calc(100vw - 16px));
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-popover, 0 8px 24px rgb(15 23 42 / 16%));
    padding: 5px;
  }

  .menu-head,
  .menu-foot {
    margin: 0;
    padding: 5px 8px;
    color: var(--text-faint);
    font-size: var(--font-size-3xs);
  }

  .menu-foot {
    border-top: 1px solid var(--border-muted);
    margin-top: 3px;
  }

  .daemon-row {
    width: 100%;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-primary);
    display: grid;
    grid-template-columns: 14px minmax(0, 1fr) auto;
    align-items: center;
    gap: 9px;
    padding: 7px 8px;
    text-align: left;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .daemon-row:hover {
    background: var(--bg-surface-hover);
  }

  .daemon-row.selected {
    background: var(--bg-inset);
  }

  .row-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-meta {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .row-hint {
    grid-column: 2 / -1;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.3;
  }

  .check {
    color: var(--accent-blue);
  }
</style>
