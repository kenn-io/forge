<script lang="ts">
  import { IconButton, dismissable } from "@kenn-io/kit-ui";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import { tick } from "svelte";

  interface Props {
    repoLabel: string;
    hidden: boolean;
    isGlob: boolean;
    embedded: boolean;
    visibilityPending: boolean;
    onEditLocalClone: () => void;
    onToggleVisibility: () => void;
  }

  let {
    repoLabel,
    hidden,
    isGlob,
    embedded,
    visibilityPending,
    onEditLocalClone,
    onToggleVisibility,
  }: Props = $props();

  let open = $state(false);
  let root = $state<HTMLDivElement>();

  function trigger(): HTMLButtonElement | null {
    return root?.querySelector<HTMLButtonElement>(".repo-config-trigger") ?? null;
  }

  function close(): void {
    open = false;
  }

  async function toggle(): Promise<void> {
    open = !open;
    if (!open) return;
    await tick();
    root?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus();
  }

  function editLocalClone(): void {
    close();
    onEditLocalClone();
  }

  function toggleVisibility(): void {
    close();
    onToggleVisibility();
  }

  $effect(() => {
    if (!open) return;
    return dismissable({
      owners: () => [root],
      dismiss: close,
      escapeFocus: trigger,
    });
  });
</script>

<div class="repo-config-menu" bind:this={root}>
  <IconButton
    class="repo-config-trigger"
    size="sm"
    tone="info"
    ariaLabel={`Configure ${repoLabel}`}
    ariaHaspopup="menu"
    ariaExpanded={open}
    onclick={() => void toggle()}
  ><SettingsIcon size={14} aria-hidden="true" /></IconButton>
  {#if open}
    <ul class="repo-config-popover kit-popover-card" role="menu" aria-label={`Configure ${repoLabel}`}>
      {#if !isGlob}
        <li>
          <button type="button" role="menuitem" disabled={embedded} onclick={editLocalClone}>
            Edit local clone path…
          </button>
        </li>
      {/if}
      <li>
        <button
          type="button"
          role="menuitem"
          disabled={embedded || visibilityPending}
          onclick={toggleVisibility}
        >
          {hidden ? "Show in UI" : "Hide from UI"}
        </button>
      </li>
    </ul>
  {/if}
</div>

<style>
  .repo-config-menu {
    position: relative;
  }

  .repo-config-popover {
    position: absolute;
    top: calc(100% + var(--space-2));
    right: 0;
    z-index: var(--z-popover);
    min-width: 180px;
    margin: 0;
    padding: var(--space-2);
    list-style: none;
  }

  .repo-config-popover button {
    width: 100%;
    padding: var(--space-3) var(--space-4);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    white-space: nowrap;
    cursor: pointer;
  }

  .repo-config-popover button:hover:not(:disabled),
  .repo-config-popover button:focus-visible {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .repo-config-popover button:disabled {
    color: var(--text-faint);
    cursor: default;
  }
</style>
