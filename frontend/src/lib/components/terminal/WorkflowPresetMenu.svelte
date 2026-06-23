<script lang="ts">
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import PlayIcon from "@lucide/svelte/icons/play";
  import SaveIcon from "@lucide/svelte/icons/save";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import type { WorkflowPreset } from "./workflow-presets";

  interface Props {
    presets: WorkflowPreset[];
    selectedPresetId?: string | null;
    applying?: boolean;
    disabled?: boolean;
    onSaveNew?: (() => void) | undefined;
    onUpdate?: ((presetId: string) => void) | undefined;
    onApply?: ((presetId: string) => void) | undefined;
    onDelete?: ((presetId: string) => void) | undefined;
  }

  const {
    presets,
    selectedPresetId = null,
    applying = false,
    disabled = false,
    onSaveNew,
    onUpdate,
    onApply,
    onDelete,
  }: Props = $props();

  let open = $state(false);
  let rootEl = $state<HTMLDivElement | null>(null);

  const selectedPreset = $derived(
    presets.find((preset) => preset.id === selectedPresetId) ?? null,
  );

  $effect(() => {
    if (disabled) open = false;
  });

  $effect(() => {
    if (!open) return;

    function onPointerDown(ev: PointerEvent): void {
      if (rootEl && ev.target instanceof Node && rootEl.contains(ev.target)) {
        return;
      }
      open = false;
    }

    function onKeydown(ev: KeyboardEvent): void {
      if (ev.key === "Escape") {
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

<div class="preset-menu" bind:this={rootEl}>
  <button
    class="preset-trigger"
    type="button"
    aria-label="Workflow presets"
    aria-haspopup="true"
    aria-expanded={open}
    disabled={disabled || applying}
    onclick={() => {
      if (disabled) return;
      open = !open;
    }}
  >
    <SaveIcon size="12" strokeWidth="2" aria-hidden="true" />
    <span>Presets</span>
    <ChevronDownIcon size="12" strokeWidth="2" aria-hidden="true" />
  </button>
  {#if open}
    <div class="preset-popover" role="dialog" aria-label="Workflow presets">
      <div class="popover-heading">Workflow presets</div>
      <button
        class="preset-command"
        type="button"
        disabled={disabled}
        onclick={() => {
          if (disabled) return;
          open = false;
          onSaveNew?.();
        }}
      >
        <SaveIcon size="13" strokeWidth="2" aria-hidden="true" />
        <span>Save as preset</span>
      </button>
      <button
        class="preset-command"
        type="button"
        disabled={disabled || !selectedPreset}
        onclick={() => {
          if (disabled) return;
          if (!selectedPreset) return;
          open = false;
          onUpdate?.(selectedPreset.id);
        }}
      >
        <SaveIcon size="13" strokeWidth="2" aria-hidden="true" />
        <span>Update selected</span>
      </button>
      <div class="preset-list">
        {#if presets.length === 0}
          <div class="empty-presets">No presets saved</div>
        {:else}
          {#each presets as preset (preset.id)}
            <div class={["preset-row", { selected: preset.id === selectedPresetId }]}>
              <button
                class="preset-apply"
                type="button"
                disabled={disabled || applying}
                onclick={() => {
                  if (disabled) return;
                  open = false;
                  onApply?.(preset.id);
                }}
              >
                <PlayIcon size="12" strokeWidth="2.2" aria-hidden="true" />
                <span>{preset.name}</span>
              </button>
              <button
                class="preset-delete"
                type="button"
                aria-label={`Delete ${preset.name}`}
                disabled={disabled}
                onclick={() => {
                  if (disabled) return;
                  onDelete?.(preset.id);
                }}
              >
                <TrashIcon size="12" strokeWidth="2.2" aria-hidden="true" />
              </button>
            </div>
          {/each}
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .preset-menu {
    position: relative;
  }

  .preset-trigger {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 22px;
    padding: 0 7px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: pointer;
  }

  .preset-trigger:hover:not(:disabled),
  .preset-trigger[aria-expanded="true"] {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--accent-blue);
  }

  .preset-trigger:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .preset-popover {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 25;
    width: 260px;
    padding: 4px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow:
      0 1px 2px rgba(0, 0, 0, 0.04),
      0 4px 16px rgba(0, 0, 0, 0.12);
  }

  .popover-heading {
    padding: 4px 8px 6px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border-muted);
    margin-bottom: 3px;
  }

  .preset-command,
  .preset-apply,
  .preset-delete {
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .preset-command {
    display: grid;
    grid-template-columns: 16px 1fr;
    align-items: center;
    gap: 8px;
    width: 100%;
    height: 26px;
    padding: 0 8px;
    text-align: left;
  }

  .preset-command:hover:not(:disabled),
  .preset-command:focus-visible,
  .preset-apply:hover:not(:disabled),
  .preset-apply:focus-visible,
  .preset-delete:hover,
  .preset-delete:focus-visible {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--accent-blue);
    outline: none;
  }

  .preset-command:disabled,
  .preset-apply:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .preset-list {
    margin-top: 4px;
    padding-top: 4px;
    border-top: 1px solid var(--border-muted);
  }

  .empty-presets {
    padding: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .preset-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 24px;
    align-items: center;
    gap: 2px;
  }

  .preset-row.selected {
    background: color-mix(in srgb, var(--accent-blue) 8%, transparent);
  }

  .preset-apply {
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr);
    align-items: center;
    gap: 8px;
    height: 26px;
    padding: 0 8px;
    text-align: left;
  }

  .preset-apply span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .preset-delete {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    color: var(--text-muted);
  }
</style>
