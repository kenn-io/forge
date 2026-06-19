<script lang="ts">
  import FunnelIcon from "@lucide/svelte/icons/funnel";
  import ArrowUpDownIcon from "@lucide/svelte/icons/arrow-up-down";
  import EllipsisIcon from "@lucide/svelte/icons/ellipsis";
  import { tick } from "svelte";
  import { floatingPopoverStyle } from "./floatingPosition.js";

  interface FilterDropdownItem {
    id: string;
    label: string;
    description?: string;
    active: boolean;
    color?: string;
    disabled?: boolean;
    closeOnSelect?: boolean;
    onSelect: () => void;
  }

  interface FilterDropdownSection {
    title?: string;
    items: FilterDropdownItem[];
  }

  interface Props {
    label: string;
    detail?: string;
    title?: string;
    active?: boolean;
    badgeCount?: number;
    showBadge?: boolean;
    disabled?: boolean;
    sections: FilterDropdownSection[];
    resetLabel?: string;
    onReset?: () => void;
    minWidth?: string;
    align?: "start" | "end";
    icon?: "filter" | "sort" | "more";
  }

  let {
    label,
    detail,
    title,
    active = false,
    badgeCount = 0,
    showBadge = true,
    disabled = false,
    sections,
    resetLabel,
    onReset,
    minWidth = "200px",
    align = "start",
    icon = "filter",
  }: Props = $props();

  let isOpen = $state(false);
  let buttonRef = $state<HTMLButtonElement>();
  let dropdownRef = $state<HTMLDivElement>();
  let dropdownStyle = $state("");

  const isActive = $derived(active || badgeCount > 0);
  const hasReset = $derived(
    resetLabel !== undefined && onReset !== undefined,
  );

  $effect(() => {
    if (disabled && isOpen) isOpen = false;
  });

  $effect(() => {
    if (!isOpen) return;

    function updatePosition(): void {
      positionDropdown();
    }

    function handleMousedown(event: MouseEvent): void {
      const target = event.target as Node;
      if (dropdownRef?.contains(target)) return;
      if (buttonRef?.contains(target)) return;
      isOpen = false;
    }

    function handleKeydown(event: KeyboardEvent): void {
      if (event.key === "Escape") {
        isOpen = false;
      }
    }

    document.addEventListener("mousedown", handleMousedown);
    document.addEventListener("keydown", handleKeydown);
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      document.removeEventListener(
        "mousedown",
        handleMousedown,
      );
      document.removeEventListener(
        "keydown",
        handleKeydown,
      );
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  });

  function positionDropdown(): void {
    if (!buttonRef || !dropdownRef) return;

    dropdownStyle = floatingPopoverStyle({
      trigger: buttonRef.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: dropdownRef.offsetWidth,
      popoverHeight: dropdownRef.offsetHeight,
      align,
    });
  }

  async function openDropdown(): Promise<void> {
    isOpen = true;
    await tick();
    positionDropdown();
  }

  async function toggleOpen(): Promise<void> {
    if (disabled) return;
    if (isOpen) {
      isOpen = false;
      return;
    }
    await openDropdown();
  }

  async function handleSelect(item: FilterDropdownItem): Promise<void> {
    if (disabled || item.disabled) return;
    item.onSelect();
    if (item.closeOnSelect) {
      isOpen = false;
      return;
    }
    await tick();
    positionDropdown();
  }

  async function handleReset(): Promise<void> {
    if (disabled) return;
    onReset?.();
    await tick();
    positionDropdown();
  }

  function itemDescriptionId(item: FilterDropdownItem): string {
    return `filter-item-description-${item.id.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  }
</script>

<div class="filter-wrap">
  <button
    class="filter-btn"
    class:filter-active={isActive}
    bind:this={buttonRef}
    onclick={toggleOpen}
    aria-label={label}
    {title}
    {disabled}
    type="button"
  >
    {#if icon === "sort"}
      <ArrowUpDownIcon size={12} strokeWidth={2} aria-hidden="true" />
    {:else if icon === "more"}
      <EllipsisIcon size={14} strokeWidth={2.2} aria-hidden="true" />
    {:else}
      <FunnelIcon size={12} strokeWidth={2} aria-hidden="true" />
    {/if}
    <span class="filter-trigger-label">{label}</span>
    {#if detail}
      <span class="filter-trigger-detail">{detail}</span>
    {/if}
    {#if showBadge && badgeCount > 0}
      <span class="filter-badge">{badgeCount}</span>
    {/if}
  </button>

  {#if isOpen}
    <div
      class="filter-dropdown"
      class:filter-dropdown--align-end={align === "end"}
      bind:this={dropdownRef}
      style={dropdownStyle}
      style:min-width={minWidth}
    >
      {#each sections as section, index (section.title ?? `section-${index}`)}
        {#if index > 0}
          <div class="filter-divider"></div>
        {/if}
        {#if section.title}
          <div class="filter-section-title">{section.title}</div>
        {/if}
        {#each section.items as item (item.id)}
          <button
            class="filter-item"
            class:active={item.active}
            onclick={() => handleSelect(item)}
            disabled={disabled || item.disabled}
            title={item.description}
            aria-describedby={item.description
              ? itemDescriptionId(item)
              : undefined}
            type="button"
          >
            <span
              class="filter-dot"
              style:background={item.active
                ? (item.color ?? "var(--accent-blue)")
                : "var(--border-muted)"}
            ></span>
            <span class="filter-label">{item.label}</span>
            <span class="filter-check" class:on={item.active}>
              {#if item.active}
                <svg width="10" height="10" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
                  <path d="M13.78 4.22a.75.75 0 010 1.06l-7.25 7.25a.75.75 0 01-1.06 0L2.22 9.28a.75.75 0 011.06-1.06L6 10.94l6.72-6.72a.75.75 0 011.06 0z"/>
                </svg>
              {/if}
            </span>
          </button>
          {#if item.description}
            <span id={itemDescriptionId(item)} class="sr-only">
              {item.description}
            </span>
          {/if}
        {/each}
      {/each}
      {#if hasReset}
        <button
          class="filter-reset"
          onclick={handleReset}
          {disabled}
          type="button"
        >
          {resetLabel}
        </button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .filter-wrap {
    position: relative;
  }

  .filter-btn {
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 3px 10px;
    font-size: var(--font-size-xs);
    font-weight: 500;
    color: var(--text-muted);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: border-color 0.12s, color 0.12s;
    position: relative;
    min-height: 24px;
  }

  .filter-btn:hover:not(:disabled) {
    border-color: var(--border-default);
    color: var(--text-secondary);
  }

  .filter-btn:disabled {
    cursor: default;
    opacity: 0.5;
  }

  .filter-btn.filter-active {
    color: var(--accent-blue);
    border-color: var(--accent-blue);
  }

  .filter-trigger-detail {
    color: var(--text-secondary);
  }

  .filter-badge {
    font-size: 0.9em;
    font-weight: 700;
    background: var(--accent-blue);
    color: white;
    border-radius: 6px;
    padding: 0 4px;
    min-width: 14px;
    text-align: center;
    line-height: 14px;
  }

  .filter-dropdown {
    position: fixed;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-md);
    z-index: 1000;
    padding: 4px 0;
  }

  .filter-dropdown--align-end {
    transform-origin: top right;
  }

  .filter-section-title {
    padding: 4px 12px 4px;
    font-size: 0.9em;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .filter-divider {
    height: 1px;
    background: var(--border-muted);
    margin: 4px 8px;
  }

  .filter-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 4px 12px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    text-align: left;
    cursor: pointer;
    transition: background 0.08s;
    background: transparent;
    border: 0;
  }

  .filter-item:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .filter-item:not(.active) {
    opacity: 0.5;
  }

  .filter-item:disabled {
    cursor: default;
  }

  .filter-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
    transition: background 0.1s;
  }

  .filter-label {
    flex: 1;
  }

  .filter-check {
    width: 14px;
    height: 14px;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--accent-green);
    flex-shrink: 0;
  }

  .sr-only {
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

  .filter-reset {
    display: block;
    width: calc(100% - 16px);
    margin: 4px 8px 2px;
    padding: 4px 8px;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    text-align: center;
    border: 0;
    border-top: 1px solid var(--border-muted);
    background: transparent;
    padding-top: 8px;
    cursor: pointer;
    transition: color 0.1s;
  }

  .filter-reset:hover {
    color: var(--text-primary);
  }
</style>
