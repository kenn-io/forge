<script lang="ts" module>
  export interface MembershipOption {
    key: string;
    label: string;
    /** Hex color for a label swatch. */
    color?: string | undefined;
    description?: string | undefined;
  }
</script>

<script lang="ts">
  import { SearchInput } from "@kenn-io/kit-ui";
  import CheckIcon from "@lucide/svelte/icons/check";
  import MinusIcon from "@lucide/svelte/icons/minus";
  import DialogButton from "../shared/DialogButton.svelte";
  import Modal from "../shared/Modal.svelte";

  // Edits one set-valued field (labels, assignees) across several items.
  // Each option shows whether all, some, or none of the items have it;
  // clicking cycles it so the user picks "add to all" or "remove from all",
  // and untouched options keep each item's current value.
  interface Props {
    open: boolean;
    title: string;
    options: readonly MembershipOption[];
    /** How many of the items have each option now. */
    counts: ReadonlyMap<string, number>;
    total: number;
    filterLabel: string;
    loading?: boolean;
    error?: string;
    emptyText: string;
    busy?: boolean;
    /** False limits changes to removals, for example when the valid options could not be loaded. */
    allowAdd?: boolean;
    frameId: string;
    /** Called as the filter changes, for options that come from a server search. */
    onfilter?: ((query: string) => void) | undefined;
    onCancel: () => void;
    onApply: (change: { add: string[]; remove: string[] }) => void;
  }

  let {
    open,
    title,
    options,
    counts,
    total,
    filterLabel,
    loading = false,
    error = "",
    emptyText,
    busy = false,
    allowAdd = true,
    frameId,
    onfilter,
    onCancel,
    onApply,
  }: Props = $props();

  let query = $state("");
  // Options the user changed, with the state they chose.
  let chosen = $state.raw<ReadonlyMap<string, "all" | "none">>(new Map());

  const filtered = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    return needle === "" ? options : options.filter((option) => option.label.toLowerCase().includes(needle));
  });
  const add = $derived([...chosen].flatMap(([key, state]) => (state === "all" ? [key] : [])));
  const remove = $derived([...chosen].flatMap(([key, state]) => (state === "none" ? [key] : [])));

  function currentState(key: string): "all" | "some" | "none" {
    const count = counts.get(key) ?? 0;
    if (count === 0) return "none";
    return count >= total ? "all" : "some";
  }

  function shownState(key: string): "all" | "some" | "none" {
    return chosen.get(key) ?? currentState(key);
  }

  // all -> none -> all; none -> all -> none; some -> all -> none -> some.
  // Without adds: all or some -> none -> back to the current state.
  function cycle(key: string): void {
    const current = currentState(key);
    const shown = shownState(key);
    const next = new Map(chosen);
    if (!allowAdd) {
      if (shown === "none") next.delete(key);
      else next.set(key, "none");
    } else if (shown === "all") next.set(key, "none");
    else if (shown === "none" && current === "some") next.delete(key);
    else next.set(key, "all");
    if (next.get(key) === current) next.delete(key);
    chosen = next;
  }

  function setQuery(value: string): void {
    query = value;
    onfilter?.(value);
  }

  function swatch(color: string | undefined): string {
    return `--label-color: #${(color || "6e7781").replace(/^#/, "")}`;
  }

  const changeCount = $derived(add.length + remove.length);
</script>

<Modal {open} {title} onClose={() => { if (!busy) onCancel(); }} width={420} {frameId}>
  <div class="membership">
    <SearchInput
      value={query}
      oninput={setQuery}
      onclear={() => setQuery("")}
      block
      autofocus
      placeholder={filterLabel}
      ariaLabel={filterLabel}
    />
    {#if error}
      <p class="membership__error" role="alert">{error}</p>
    {/if}
    <div class="membership__list" role="group" aria-label={title}>
      {#each filtered as option (option.key)}
        {@const state = shownState(option.key)}
        <button
          type="button"
          class={["membership__row", { "membership__row--changed": chosen.has(option.key) }]}
          role="checkbox"
          aria-checked={state === "all" ? "true" : state === "some" ? "mixed" : "false"}
          disabled={busy || (!allowAdd && currentState(option.key) === "none")}
          onclick={() => cycle(option.key)}
        >
          <span class="membership__box" aria-hidden="true">
            {#if state === "all"}
              <CheckIcon size="12" strokeWidth="2.6" />
            {:else if state === "some"}
              <MinusIcon size="12" strokeWidth="2.6" />
            {/if}
          </span>
          {#if option.color !== undefined}
            <span class="membership__swatch" style={swatch(option.color)} aria-hidden="true"></span>
          {/if}
          <span class="membership__text">
            <span class="membership__name">{option.label}</span>
            {#if option.description}
              <span class="membership__description">{option.description}</span>
            {/if}
          </span>
          {#if state === "some"}
            <span class="membership__count">{counts.get(option.key) ?? 0} of {total}</span>
          {/if}
        </button>
      {:else}
        <p class="membership__empty">{loading ? "Loading…" : emptyText}</p>
      {/each}
    </div>
  </div>

  {#snippet footer()}
    <DialogButton disabled={busy} onclick={onCancel}>Cancel</DialogButton>
    <DialogButton tone="primary" disabled={busy || loading || changeCount === 0} onclick={() => onApply({ add, remove })}>
      {busy ? "Applying…" : "Apply"}
    </DialogButton>
  {/snippet}
</Modal>

<style>
  .membership {
    display: grid;
    gap: var(--space-4);
  }

  .membership__list {
    display: grid;
    max-height: min(360px, 50vh);
    overflow-y: auto;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  .membership__row {
    display: grid;
    grid-template-columns: 16px auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--space-3);
    min-height: 34px;
    padding: 4px var(--space-4);
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .membership__row:hover:not(:disabled),
  .membership__row:focus-visible {
    background: var(--bg-surface-hover);
    outline: none;
  }

  .membership__row--changed {
    background: color-mix(in srgb, var(--accent-blue) 7%, transparent);
  }

  .membership__box {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 14px;
    height: 14px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    color: var(--accent-blue);
  }

  .membership__swatch {
    width: 10px;
    height: 10px;
    border-radius: 999px;
    background: var(--label-color);
  }

  .membership__text {
    display: grid;
    min-width: 0;
  }

  .membership__name,
  .membership__description {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .membership__description,
  .membership__count {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .membership__empty,
  .membership__error {
    margin: 0;
    padding: var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .membership__error {
    color: var(--accent-red);
  }
</style>
