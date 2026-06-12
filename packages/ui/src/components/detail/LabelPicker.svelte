<script lang="ts">
  import { onMount } from "svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import EraserIcon from "@lucide/svelte/icons/eraser";
  import XIcon from "@lucide/svelte/icons/x";
  import type { Label } from "../../api/types.js";

  interface Props {
    catalogLabels: Label[];
    selectedLabels: Label[];
    syncing?: boolean;
    pendingLabel?: string | null;
    error?: string | null;
    autofocusFilter?: boolean;
    /** Disables every mutation control (toggle rows, clear) — used
     * when label operations become unavailable while the picker is
     * already open. Closing stays enabled. */
    disabled?: boolean;
    disabledReason?: string | undefined;
    ontoggle: (name: string) => void | Promise<void>;
    onclear?: () => void | Promise<void>;
    onclose: () => void;
  }

  const {
    catalogLabels,
    selectedLabels,
    syncing = false,
    pendingLabel = null,
    error = null,
    autofocusFilter = false,
    disabled = false,
    disabledReason = undefined,
    ontoggle,
    onclear = undefined,
    onclose,
  }: Props = $props();

  let query = $state("");
  let filterInput: HTMLInputElement | undefined = $state();

  onMount(() => {
    if (autofocusFilter) filterInput?.focus();
  });

  const selectedNames = $derived(new Set(selectedLabels.map((label) => label.name)));
  const filteredLabels = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    if (needle === "") return catalogLabels;
    return catalogLabels.filter((label) =>
      `${label.name} ${label.description ?? ""}`.toLowerCase().includes(needle),
    );
  });

  function labelStyle(label: Label): string {
    const color = (label.color || "6e7781").replace(/^#/, "");
    return `--label-color: #${color}`;
  }

  function clearSelectedLabels(): void {
    if (disabled || pendingLabel !== null || selectedNames.size === 0) return;
    void onclear?.();
  }

  function toggleLabelRow(name: string): void {
    if (disabled) return;
    void ontoggle(name);
  }
</script>

<div class="label-picker" role="dialog" aria-label="Edit labels">
  <div class="label-picker__header">
    <div class="label-picker__title">
      <strong>Edit labels</strong>
      {#if syncing}
        <span class="label-picker__syncing">Refreshing…</span>
      {/if}
    </div>
    <div class="label-picker__header-actions">
      <button
        type="button"
        class="label-picker__icon-button"
        aria-label="Clear selected labels"
        title={disabled && disabledReason !== undefined ? disabledReason : "Clear selected labels"}
        disabled={disabled || pendingLabel !== null || selectedNames.size === 0 || onclear === undefined}
        onclick={clearSelectedLabels}
      >
        <EraserIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="label-picker__icon-button"
        aria-label="Close label picker"
        onclick={onclose}
      >
        <XIcon size="15" strokeWidth="2.2" aria-hidden="true" />
      </button>
    </div>
  </div>

  <label class="label-picker__filter">
    <span class="label-picker__sr-only">Filter labels</span>
    <input
      bind:this={filterInput}
      bind:value={query}
      type="search"
      placeholder="Filter labels"
      aria-label="Filter labels"
    />
  </label>

  {#if error}
    <div class="label-picker__error" role="alert">{error}</div>
  {/if}

  <div class="label-picker__list" role="menu" aria-label="Repository labels">
    {#each filteredLabels as label (label.name)}
      {@const selected = selectedNames.has(label.name)}
      <button
        type="button"
        class={["label-picker__row", { "label-picker__row--selected": selected }]}
        role="menuitemcheckbox"
        aria-checked={selected}
        disabled={disabled || pendingLabel !== null}
        title={disabled && disabledReason !== undefined ? disabledReason : undefined}
        onclick={() => toggleLabelRow(label.name)}
      >
        <span class="label-picker__color" style={labelStyle(label)} aria-hidden="true"></span>
        <span class="label-picker__text">
          <span class="label-picker__name">{label.name}</span>
          {#if label.description}
            <span class="label-picker__description">{label.description}</span>
          {/if}
        </span>
        <span class="label-picker__status">
          {#if pendingLabel === label.name}
            <span class="label-picker__pending">Saving…</span>
          {:else if selected}
            <CheckIcon size="14" strokeWidth="2.4" aria-hidden="true" />
          {/if}
        </span>
      </button>
    {:else}
      <div class="label-picker__empty">No labels found</div>
    {/each}
  </div>
</div>

<style>
  .label-picker {
    width: 100%;
    min-width: 0;
    max-height: var(--label-picker-max-height, min(390px, calc(100dvh - 64px)));
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
    color: var(--text-primary);
  }

  .label-picker__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 36px;
    padding: 5px 8px 5px 12px;
    border-bottom: 1px solid var(--border-muted);
  }

  .label-picker__title {
    min-width: 0;
    display: flex;
    align-items: baseline;
    gap: 6px;
    font-size: var(--font-size-sm);
  }

  .label-picker__syncing {
    margin-left: 4px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .label-picker__header-actions {
    display: inline-flex;
    align-items: center;
    gap: 2px;
  }

  .label-picker__icon-button {
    width: 26px;
    height: 26px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    padding: 0;
    transition: background 0.1s, color 0.1s, border-color 0.1s;
  }

  .label-picker__icon-button:hover:not(:disabled),
  .label-picker__icon-button:focus-visible {
    border-color: var(--border-muted);
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .label-picker__icon-button:disabled {
    cursor: default;
    opacity: 0.42;
  }

  .label-picker__filter {
    display: block;
    padding: 8px;
    border-bottom: 1px solid var(--border-muted);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .label-picker__filter input {
    width: 100%;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-primary);
    padding: 6px 9px;
    font: inherit;
    min-height: 32px;
    outline: none;
  }

  .label-picker__filter input:focus {
    border-color: var(--accent-blue);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .label-picker__error {
    margin: 6px 8px 0;
    border: 1px solid var(--accent-red);
    border-radius: var(--radius-sm);
    color: var(--accent-red);
    padding: 6px 8px;
    font-size: var(--font-size-sm);
  }

  .label-picker__list {
    overflow: auto;
    padding: 3px 0;
  }

  .label-picker__row {
    width: 100%;
    display: grid;
    grid-template-columns: 12px minmax(0, 1fr) 48px;
    align-items: center;
    gap: 9px;
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
    min-height: 36px;
    padding: 4px 8px 4px 12px;
    text-align: left;
    transition: background 0.08s, color 0.08s;
  }

  .label-picker__row:hover:not(:disabled),
  .label-picker__row:focus-visible {
    background: var(--bg-surface-hover);
    outline: none;
  }

  .label-picker__row--selected {
    background: color-mix(in srgb, var(--accent-blue) 7%, transparent);
  }

  .label-picker__row:disabled {
    cursor: wait;
    opacity: 0.7;
  }

  .label-picker__color {
    width: 12px;
    height: 12px;
    border-radius: 999px;
    background: var(--label-color);
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--label-color), black 20%);
  }

  .label-picker__text {
    min-width: 0;
    display: grid;
    gap: 1px;
  }

  .label-picker__name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
    font-size: var(--font-size-sm);
    line-height: 1.2;
  }

  .label-picker__description {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.15;
  }

  .label-picker__status {
    min-width: 0;
    display: flex;
    justify-content: flex-end;
    align-items: center;
    color: var(--accent-green);
    font-size: var(--font-size-xs);
  }

  .label-picker__pending {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .label-picker__empty {
    padding: 18px 12px;
    color: var(--text-secondary);
    text-align: center;
    font-size: var(--font-size-sm);
  }

  .label-picker__sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
