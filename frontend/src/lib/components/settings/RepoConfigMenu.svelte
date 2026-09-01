<script lang="ts">
  import { IconButton, autoReposition, dismissable, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import { tick } from "svelte";

  interface Props {
    repoLabel: string;
    hidden: boolean;
    embedded: boolean;
    visibilityPending: boolean;
    onEditLocalClone: () => void;
    onToggleVisibility: () => void;
  }

  let { repoLabel, hidden, embedded, visibilityPending, onEditLocalClone, onToggleVisibility }: Props =
    $props();

  let open = $state(false);
  let root = $state<HTMLDivElement>();
  let panel = $state<HTMLUListElement>();
  let panelStyle = $state("");

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
    position();
    await tick();
    position();
    panel?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus();
  }

  function position(): void {
    const triggerElement = trigger();
    if (!triggerElement || !panel) return;
    panelStyle = floatingPopoverStyle({
      trigger: triggerElement.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: panel.offsetWidth,
      popoverHeight: panel.offsetHeight,
      align: "end",
    });
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
    const cleanups = [
      dismissable({
        owners: () => [panel, trigger()],
        dismiss: close,
        escapeFocus: trigger,
      }),
      autoReposition(() => [panel], position),
    ];
    return () => cleanups.forEach((cleanup) => cleanup());
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
    <ul
      bind:this={panel}
      class="repo-config-popover kit-popover-card"
      style={panelStyle}
      role="menu"
      aria-label={`Configure ${repoLabel}`}
    >
      <li>
        <button type="button" role="menuitem" disabled={embedded} onclick={editLocalClone}>
          Edit local clone path…
        </button>
      </li>
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
    position: fixed;
    z-index: var(--z-popover);
    min-width: 180px;
    max-width: calc(100vw - 16px);
    max-height: calc(100vh - 16px);
    overflow-y: auto;
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
