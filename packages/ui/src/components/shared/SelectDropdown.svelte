<script module lang="ts">
  let nextSelectDropdownID = 0;

  function allocateSelectDropdownID(): string {
    nextSelectDropdownID += 1;
    return `select-dropdown-${nextSelectDropdownID}`;
  }
</script>

<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import type { SelectDropdownOption } from "./select-dropdown.js";

  interface Props {
    value: string;
    options: SelectDropdownOption[];
    onchange: (value: string) => void;
    title?: string;
    disabled?: boolean;
    class?: string;
  }

  let {
    value,
    options,
    onchange,
    title,
    disabled = false,
    class: className = "",
  }: Props = $props();

  let open = $state(false);
  let highlightedIndex = $state(0);
  let containerEl = $state<HTMLDivElement>();
  let buttonEl = $state<HTMLButtonElement>();

  const dropdownID = allocateSelectDropdownID();
  const listboxID = `${dropdownID}-listbox`;

  // Unlike a native select element, an unmatched `value` does not render blank:
  // it falls back to the first option. Callers that feed `options` from a
  // filtered or async list must keep `value` consistent with the visible
  // options (or derive anything used for submission with the same fallback),
  // otherwise the trigger can show one option while submit acts on a stale value.
  const selectedOption = $derived(
    options.find((option) => option.value === value) ?? options[0],
  );
  const triggerLabel = $derived(
    title
      ? `${title}: ${selectedOption?.triggerLabel ?? selectedOption?.label ?? value}`
      : (selectedOption?.triggerLabel ?? selectedOption?.label ?? value),
  );

  $effect(() => {
    if (!open) return;

    function handleMousedown(event: MouseEvent): void {
      const target = event.target as Node;
      if (containerEl?.contains(target)) return;
      open = false;
    }

    function handleKeydown(event: KeyboardEvent): void {
      if (event.key === "Escape") {
        open = false;
        buttonEl?.focus();
      }
    }

    document.addEventListener("mousedown", handleMousedown);
    document.addEventListener("keydown", handleKeydown);
    return () => {
      document.removeEventListener("mousedown", handleMousedown);
      document.removeEventListener("keydown", handleKeydown);
    };
  });

  function openDropdown(): void {
    if (disabled) return;
    open = !open;
    highlightedIndex = Math.max(
      0,
      options.findIndex((option) => option.value === value),
    );
  }

  function selectOption(option: SelectDropdownOption): void {
    if (disabled || option.disabled) return;
    onchange(option.value);
    open = false;
    buttonEl?.focus();
  }

  function moveHighlight(delta: number): void {
    if (options.length === 0) return;
    let next = highlightedIndex;
    for (let i = 0; i < options.length; i += 1) {
      next = (next + delta + options.length) % options.length;
      if (!options[next]?.disabled) {
        highlightedIndex = next;
        return;
      }
    }
  }

  function optionID(index: number): string {
    return `${dropdownID}-option-${index}`;
  }

  function onFocusout(event: FocusEvent): void {
    const nextTarget = event.relatedTarget as Node | null;
    if (nextTarget && containerEl?.contains(nextTarget)) return;
    open = false;
  }

  function onButtonKeydown(event: KeyboardEvent): void {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (!open) {
        openDropdown();
      } else {
        moveHighlight(1);
      }
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      if (!open) {
        openDropdown();
      } else {
        moveHighlight(-1);
      }
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (!open) {
        openDropdown();
        return;
      }
      const option = options[highlightedIndex];
      if (option) selectOption(option);
    }
  }
</script>

<div
  class={["select-dropdown", className]}
  bind:this={containerEl}
  onfocusout={onFocusout}
>
  <button
    bind:this={buttonEl}
    class="select-dropdown-trigger"
    type="button"
    role="combobox"
    onclick={openDropdown}
    onkeydown={onButtonKeydown}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={listboxID}
    aria-activedescendant={open ? optionID(highlightedIndex) : undefined}
    aria-label={triggerLabel}
    {title}
    {disabled}
  >
    <span class="select-dropdown-value">{selectedOption?.triggerLabel ?? selectedOption?.label ?? value}</span>
    <ChevronDownIcon
      class="select-dropdown-chevron"
      size="12"
      strokeWidth="2"
      aria-hidden="true"
    />
  </button>

  {#if open}
    <div id={listboxID} class="select-dropdown-list" role="listbox">
      {#each options as option, index (option.value)}
        <button
          id={optionID(index)}
          type="button"
          tabindex="-1"
          class="select-dropdown-option"
          class:highlighted={index === highlightedIndex}
          class:selected={option.value === value}
          role="option"
          aria-selected={option.value === value}
          disabled={disabled || option.disabled}
          onclick={() => selectOption(option)}
          onmouseenter={() => { highlightedIndex = index; }}
        >
          <span class="select-dropdown-option-label">{option.label}</span>
          <span class="select-dropdown-check">
            {#if option.value === value}
              <CheckIcon size="12" strokeWidth="2.2" aria-hidden="true" />
            {/if}
          </span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .select-dropdown {
    position: relative;
    min-width: 150px;
  }

  .select-dropdown-trigger {
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    height: 26px;
    padding: 0 8px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    cursor: pointer;
    font-family: inherit;
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-align: left;
    transition: border-color 0.15s, color 0.15s, background 0.15s;
  }

  .select-dropdown-trigger:hover:not(:disabled),
  .select-dropdown-trigger[aria-expanded="true"] {
    border-color: var(--border-default);
    color: var(--text-primary);
  }

  .select-dropdown-trigger:disabled {
    cursor: default;
    opacity: 0.6;
  }

  .select-dropdown-value {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  :global(.select-dropdown-chevron) {
    flex-shrink: 0;
    opacity: 0.55;
  }

  .select-dropdown-list {
    position: absolute;
    z-index: 100;
    top: 100%;
    right: 0;
    min-width: 100%;
    width: max-content;
    max-width: min(280px, 90vw);
    margin-top: 2px;
    padding: 2px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
  }

  .select-dropdown-option {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 5px 8px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    font-family: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    white-space: nowrap;
  }

  .select-dropdown-option.highlighted,
  .select-dropdown-option:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .select-dropdown-option.selected {
    color: var(--accent-blue);
    font-weight: 600;
  }

  .select-dropdown-option:disabled {
    cursor: default;
    opacity: 0.5;
  }

  .select-dropdown-option-label {
    flex: 1;
  }

  .select-dropdown-check {
    display: inline-flex;
    width: 12px;
    color: currentColor;
  }
</style>
