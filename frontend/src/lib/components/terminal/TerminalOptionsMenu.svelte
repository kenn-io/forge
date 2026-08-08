<script lang="ts">
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import { getStores } from "../../context.js";
  import { getStackDepth } from "../../stores/keyboard/modal-stack.svelte.js";
  import type { TerminalSettings as TerminalSettingsType } from "../../api/types.js";
  import TerminalSettings from "../settings/TerminalSettings.svelte";

  interface Props {
    disabled?: boolean;
    /** False while the workspace host is parked/hidden: closes the popover
     *  (and any nested dialog it hosts) so no invisible overlay, focus trap,
     *  or window listener outlives the visible surface. */
    hostVisible?: boolean;
    onSavingChange?: (saving: boolean) => void;
  }

  const {
    disabled = false,
    hostVisible = true,
    onSavingChange,
  }: Props = $props();
  const { settings: settingsStore } = getStores();

  let open = $state(false);
  let rootEl = $state<HTMLDivElement | null>(null);
  let terminal = $state<TerminalSettingsType>(
    settingsStore.getTerminalSettings(),
  );
  let childSaving = $state(false);

  $effect(() => {
    if (disabled || !hostVisible) open = false;
  });

  function toggleOpen(): void {
    if (disabled) return;
    if (childSaving) return;
    if (!open) {
      terminal = settingsStore.getTerminalSettings();
      childSaving = false;
    }
    open = !open;
  }

  $effect(() => {
    if (!open) return;

    function onPointerDown(ev: PointerEvent): void {
      if (rootEl && ev.target instanceof Node && rootEl.contains(ev.target)) {
        return;
      }
      if (childSaving) return;
      open = false;
    }
    function onKeydown(ev: KeyboardEvent): void {
      if (ev.key === "Escape") {
        // A dialog above this popover (the font picker Modal) owns Escape
        // via the modal stack; its kit-ui handler runs after this one, so
        // the stack — not defaultPrevented — is the signal to stand down.
        if (getStackDepth() > 0) return;
        if (childSaving) return;
        open = false;
      }
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeydown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeydown);
    };
  });
</script>

<div class="terminal-options-menu" bind:this={rootEl}>
  <button
    class="options-trigger"
    type="button"
    aria-label="Terminal options"
    aria-haspopup="true"
    aria-expanded={open}
    title="Terminal options"
    disabled={disabled}
    onclick={toggleOpen}
  >
    <SettingsIcon size="13" strokeWidth="2" aria-hidden="true" />
  </button>
  {#if open}
    <div
      class="options-popover"
      role="dialog"
      aria-label="Terminal options"
    >
      <div class="popover-heading">Terminal options</div>
      <TerminalSettings
        {terminal}
        compact={true}
        livePreview={true}
        onUpdate={(updated) => {
          terminal = updated;
        }}
        onSavingChange={(saving) => {
          childSaving = saving;
          onSavingChange?.(saving);
        }}
      />
    </div>
  {/if}
</div>

<style>
  .terminal-options-menu {
    position: relative;
  }

  .options-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 22px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    cursor: pointer;
    transition: background-color 80ms ease, border-color 80ms ease,
      color 80ms ease;
  }

  .options-trigger:hover,
  .options-trigger[aria-expanded="true"] {
    background: var(--bg-surface-hover);
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }

  .options-popover {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    /* The pane-controls popover that hosts this trigger sets
       `overflow-wrap: anywhere` so a long branch name cannot set its
       min-content width. That inherits in here, where it has no business:
       it let the settings form shrink its Save/Reset buttons below their
       labels, which then broke mid-word ("Sa/ve"). */
    overflow-wrap: normal;
    word-break: normal;
    z-index: 25;
    width: 372px;
    padding: 10px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }

  .popover-heading {
    padding: 0 0 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border-muted);
    margin-bottom: 8px;
  }

  @media (max-width: 640px) {
    .options-popover {
      width: min(372px, calc(100vw - 24px));
      right: -6px;
    }
  }
</style>
