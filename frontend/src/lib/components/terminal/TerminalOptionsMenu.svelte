<script lang="ts">
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import { getStores } from "@middleman/ui";
  import type { ModeVisibility } from "@middleman/ui/api/types";
  import type { TerminalSettings as TerminalSettingsType } from "@middleman/ui/api/types";
  import ModeVisibilitySettings from "../settings/ModeVisibilitySettings.svelte";
  import TerminalSettings from "../settings/TerminalSettings.svelte";

  const { settings: settingsStore } = getStores();

  let open = $state(false);
  let rootEl = $state<HTMLDivElement | null>(null);
  let terminal = $state<TerminalSettingsType>(
    settingsStore.getTerminalSettings(),
  );
  let modes = $state<ModeVisibility>(settingsStore.getModeVisibility());
  let childSaving = $state(false);

  function toggleOpen(): void {
    if (childSaving) return;
    if (!open) {
      terminal = settingsStore.getTerminalSettings();
      modes = settingsStore.getModeVisibility();
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
        }}
      />
      <div class="popover-section">
        <div class="section-heading">Visible modes</div>
        <ModeVisibilitySettings
          {modes}
          compact={true}
          saveLabel="Save visible modes"
          onUpdate={(updated) => {
            modes = updated;
          }}
          onSavingChange={(saving) => {
            childSaving = saving;
          }}
        />
      </div>
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
    z-index: 25;
    width: 372px;
    padding: 10px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow:
      0 1px 2px rgba(0, 0, 0, 0.04),
      0 4px 16px rgba(0, 0, 0, 0.12);
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

  .popover-section {
    border-top: 1px solid var(--border-muted);
    margin-top: 10px;
    padding-top: 10px;
  }

  .section-heading {
    margin-bottom: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  @media (max-width: 620px) {
    .options-popover {
      width: min(372px, calc(100vw - 24px));
      right: -6px;
    }
  }
</style>
