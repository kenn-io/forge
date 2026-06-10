<script lang="ts" module>
  export interface TypeaheadOption {
    value: string;
    label: string;
    meta?: string;
  }
</script>

<script lang="ts">
  import { tick } from "svelte";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";

  interface Props {
    ariaLabel: string;
    options: TypeaheadOption[];
    selected?: string | null;
    placeholder?: string;
    emptyLabel?: string;
    allowClear?: boolean;
    allowCustom?: boolean;
    clearLabel?: string;
    triggerPrefix?: string;
    triggerAriaLabel?: string;
    onChange: (value: string | null) => void | boolean | Promise<void | boolean>;
  }

  let {
    ariaLabel,
    options,
    selected = null,
    placeholder = "Filter...",
    emptyLabel = "No matches",
    allowClear = false,
    allowCustom = false,
    clearLabel = "None",
    triggerPrefix = "",
    triggerAriaLabel = undefined,
    onChange,
  }: Props = $props();

  let query = $state("");
  let open = $state(false);
  let highlightIndex = $state(0);
  let inputEl: HTMLInputElement | null = $state(null);
  let rootEl: HTMLDivElement | null = $state(null);
  const listboxID = `typeahead-${Math.random().toString(36).slice(2)}`;

  let selectedOption = $derived(options.find((option) => option.value === selected));
  let displayValue = $derived(selectedOption?.label ?? clearLabel);
  let filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((option) =>
      [option.label, option.value, option.meta ?? ""].some((part) => part.toLowerCase().includes(q)),
    );
  });
  let optionOffset = $derived(allowClear ? 1 : 0);
  let optionCount = $derived(filtered.length + optionOffset);

  async function openDropdown() {
    query = "";
    open = true;
    highlightIndex = allowClear && options.length > 0 ? 1 : 0;
    await tick();
    inputEl?.focus();
    inputEl?.select();
  }

  function closeDropdown() {
    open = false;
    query = "";
    highlightIndex = allowClear && filtered.length > 0 ? 1 : 0;
  }

  async function selectValue(value: string | null): Promise<void> {
    try {
      const ok = await onChange(value);
      if (ok !== false) {
        closeDropdown();
      }
    } catch {
      // Keep the query open so callers can surface their own error state
      // without losing the attempted custom value.
    }
  }

  function selectHighlighted() {
    const trimmed = query.trim();
    if (allowClear && highlightIndex === 0) {
      if (trimmed !== "" && filtered[0]) {
        void selectValue(filtered[0].value);
        return;
      }
      if (trimmed !== "" && allowCustom) {
        void selectValue(trimmed);
        return;
      }
      void selectValue(null);
      return;
    }
    const option = filtered[highlightIndex - optionOffset];
    if (option) {
      void selectValue(option.value);
      return;
    }
    if (allowCustom && trimmed !== "") {
      void selectValue(trimmed);
    }
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      highlightIndex = Math.min(highlightIndex + 1, Math.max(optionCount - 1, 0));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      highlightIndex = Math.max(highlightIndex - 1, 0);
    } else if (event.key === "Enter") {
      event.preventDefault();
      selectHighlighted();
    } else if (event.key === "Escape") {
      event.preventDefault();
      closeDropdown();
    }
  }

  function handleInput() {
    highlightIndex = filtered.length > 0 ? optionOffset : 0;
  }

  function handleBlur(event: FocusEvent) {
    const related = event.relatedTarget as Node | null;
    if (rootEl && related && rootEl.contains(related)) return;
    closeDropdown();
  }

  function preventBlur(event: MouseEvent) {
    event.preventDefault();
  }
</script>

<div class="typeahead" bind:this={rootEl}>
  {#if open}
    <input
      bind:this={inputEl}
      class="typeahead-input"
      role="combobox"
      aria-label={ariaLabel}
      aria-expanded="true"
      aria-controls={listboxID}
      aria-autocomplete="list"
      type="text"
      bind:value={query}
      {placeholder}
      autocomplete="off"
      oninput={handleInput}
      onkeydown={handleKeydown}
      onblur={handleBlur}
    />
    <ul
      id={listboxID}
      class="typeahead-list"
      data-surface="solid"
      role="listbox"
      aria-label={`${ariaLabel} options`}
      onmousedown={preventBlur}
    >
      {#if allowClear}
        <li
          class="typeahead-option"
          class:highlighted={highlightIndex === 0}
          class:selected={selected === null}
          role="option"
          aria-selected={selected === null}
          onmousedown={() => void selectValue(null)}
          onmouseenter={() => (highlightIndex = 0)}
        >
          <span class="option-label">{clearLabel}</span>
        </li>
      {/if}
      {#each filtered as option, index (option.value)}
        <li
          class="typeahead-option"
          class:highlighted={index + optionOffset === highlightIndex}
          class:selected={option.value === selected}
          role="option"
          aria-selected={option.value === selected}
          onmousedown={() => void selectValue(option.value)}
          onmouseenter={() => (highlightIndex = index + optionOffset)}
        >
          <span class="option-label">{option.label}</span>
          {#if option.meta}
            <span class="option-meta">{option.meta}</span>
          {/if}
        </li>
      {:else}
        <li class="typeahead-empty">{emptyLabel}</li>
      {/each}
    </ul>
  {:else}
    <button
      class="typeahead-trigger"
      type="button"
      aria-label={triggerAriaLabel ?? `${ariaLabel}: ${displayValue}`}
      aria-haspopup="listbox"
      onclick={openDropdown}
    >
      <span class="typeahead-value">
        {#if triggerPrefix}<span class="typeahead-prefix">{triggerPrefix}</span>{/if}
        <span>{displayValue}</span>
      </span>
      <ChevronDownIcon size={12} strokeWidth={2} />
    </button>
  {/if}
</div>

<style>
  .typeahead {
    position: relative;
    min-width: 0;
  }

  .typeahead-trigger,
  .typeahead-input {
    width: 100%;
    height: 26px;
    box-sizing: border-box;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
  }

  .typeahead-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: space-between;
    gap: 6px;
    padding: 0 8px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    color: var(--text-secondary);
    cursor: pointer;
    text-align: left;
  }

  .typeahead-trigger:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-default);
  }

  .typeahead-value {
    min-width: 0;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .typeahead-prefix {
    color: var(--text-muted);
  }

  .typeahead-input {
    padding: 0 8px;
    background: var(--bg-surface);
    border: 1px solid var(--accent-blue);
    color: var(--text-primary);
    outline: none;
    box-shadow: 0 0 0 3px var(--accent-blue-soft);
  }

  .typeahead-list {
    position: absolute;
    top: calc(100% + 3px);
    left: 0;
    right: 0;
    z-index: 80;
    max-height: min(320px, 50vh);
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 3px;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-lg);
  }

  .typeahead-option {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    min-height: 28px;
    padding: 5px 8px;
    border-radius: 3px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.2;
    cursor: pointer;
  }

  .typeahead-option.highlighted {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .typeahead-option.selected {
    color: var(--accent-blue);
    font-weight: 600;
  }

  .option-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .option-meta {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }

  .typeahead-empty {
    padding: 7px 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
</style>
