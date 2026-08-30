<script lang="ts">
  import FunnelIcon from "@lucide/svelte/icons/funnel";
  import CheckIcon from "@lucide/svelte/icons/check";
  import {
    Card,
    Typeahead,
    autoReposition,
    dismissable,
    floatingPopoverStyle,
    trapFocus,
    type FilterDropdownItem,
    type FilterDropdownSection,
    type TypeaheadOption,
  } from "@kenn-io/kit-ui";
  import { tick, untrack } from "svelte";

  import { pushModalFrame } from "../stores/keyboard/modal-stack.svelte.js";

  interface ActivityFilterSection extends FilterDropdownSection {
    selectionMode?: "single" | "multiple";
  }

  interface Props {
    author: string;
    authorOptions: TypeaheadOption[];
    authorLoading?: boolean;
    authorError?: string;
    detail?: string;
    compact?: boolean;
    badgeCount?: number;
    sections: ActivityFilterSection[];
    onAuthorSelect: (author: string) => void;
    onReset?: () => void;
  }

  let {
    author,
    authorOptions,
    authorLoading = false,
    authorError = "",
    detail,
    compact = false,
    badgeCount = 0,
    sections,
    onAuthorSelect,
    onReset,
  }: Props = $props();

  let isOpen = $state(false);
  let buttonRef = $state<HTMLButtonElement>();
  let panelRef = $state<HTMLDivElement>();
  let panelStyle = $state("");

  // kit-ui-check-ignore: trapFocus includes roving radio buttons with tabindex=-1, so this listener corrects its Tab wrap boundary
  const focusableSelector = "a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex='-1'])";

  const triggerDetail = $derived(compact && author ? `· ${author}` : detail);
  const triggerLabel = $derived(compact && author ? `Filters · ${author}` : "Filters");

  $effect(() => {
    if (!isOpen) return;
    const cleanups = [
      untrack(() => pushModalFrame("activity-filters", [])),
      dismissable({
        owners: () => [panelRef, buttonRef],
        dismiss: close,
        escapeFocus: () => buttonRef,
      }),
      autoReposition(() => [panelRef], position),
    ];
    return () => cleanups.forEach((cleanup) => cleanup());
  });

  function position(): void {
    if (!buttonRef || !panelRef) return;
    panelStyle = floatingPopoverStyle({
      trigger: buttonRef.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: panelRef.offsetWidth,
      popoverHeight: panelRef.offsetHeight,
      align: "start",
    });
  }

  function close(): void {
    isOpen = false;
  }

  function focusableElements(node: HTMLElement): HTMLElement[] {
    return Array.from(node.querySelectorAll<HTMLElement>(focusableSelector)).filter(
      (element) => element.tabIndex >= 0 && element.getClientRects().length > 0,
    );
  }

  function portalToBody(node: HTMLElement): () => void {
    function containFocus(event: KeyboardEvent): void {
      if (event.key !== "Tab" || event.defaultPrevented) return;
      const controls = focusableElements(node);
      const first = controls[0];
      const last = controls.at(-1);
      if (!first || !last) {
        event.preventDefault();
        node.focus();
        return;
      }

      const activeElement = document.activeElement;
      if (event.shiftKey && (activeElement === first || !node.contains(activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (activeElement === last || !node.contains(activeElement))) {
        event.preventDefault();
        first.focus();
      }
    }

    document.body.appendChild(node);
    node.addEventListener("keydown", containFocus);
    return () => {
      node.removeEventListener("keydown", containFocus);
      node.remove();
    };
  }

  async function toggle(): Promise<void> {
    if (isOpen) {
      close();
      return;
    }
    isOpen = true;
    await tick();
    position();
    const firstControl = panelRef && focusableElements(panelRef)[0];
    if (firstControl) firstControl.focus();
    else panelRef?.focus();
  }

  async function select(item: FilterDropdownItem): Promise<void> {
    item.onSelect();
    if (item.closeOnSelect) {
      close();
      return;
    }
    await tick();
    position();
  }

  async function reset(): Promise<void> {
    onReset?.();
    await tick();
    position();
  }

  function radioTabIndex(section: ActivityFilterSection, item: FilterDropdownItem): number | undefined {
    if (section.selectionMode !== "single") return undefined;
    if (item.disabled) return -1;
    const selected = section.items.find((candidate) => candidate.active && !candidate.disabled);
    const tabStop = selected ?? section.items.find((candidate) => !candidate.disabled);
    return item === tabStop ? 0 : -1;
  }

  async function handleRadioKeydown(
    event: KeyboardEvent,
    section: ActivityFilterSection,
    item: FilterDropdownItem,
  ): Promise<void> {
    if (section.selectionMode !== "single") return;
    const enabledItems = section.items.filter((candidate) => !candidate.disabled);
    const currentIndex = enabledItems.indexOf(item);
    if (currentIndex < 0) return;

    let nextIndex: number;
    switch (event.key) {
      case "ArrowDown":
      case "ArrowRight":
        nextIndex = (currentIndex + 1) % enabledItems.length;
        break;
      case "ArrowUp":
      case "ArrowLeft":
        nextIndex = (currentIndex - 1 + enabledItems.length) % enabledItems.length;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = enabledItems.length - 1;
        break;
      default:
        return;
    }

    event.preventDefault();
    const group = (event.currentTarget as HTMLElement).closest("[role='radiogroup']");
    const radios = group?.querySelectorAll<HTMLButtonElement>("[role='radio']:not(:disabled)");
    const nextRadio = radios?.[nextIndex];
    enabledItems[nextIndex]!.onSelect();
    await tick();
    position();
    nextRadio?.focus();
  }
</script>

<div class="activity-filters" class:compact>
  <Card
    level="inset"
    padding="none"
    selected={badgeCount > 0}
    class="activity-filters__trigger-shell"
  >
    <button
      bind:this={buttonRef}
      class:active={badgeCount > 0}
      class="activity-filters__trigger"
      type="button"
      aria-label={triggerLabel}
      aria-expanded={isOpen}
      title="View and filter activity"
      onclick={toggle}
    >
      <FunnelIcon size={12} strokeWidth={2} aria-hidden="true" />
      <span>Filters</span>
      {#if triggerDetail}
        <span class="activity-filters__detail">{triggerDetail}</span>
      {/if}
      {#if badgeCount > 0}
        <span class="activity-filters__badge" aria-label={`${badgeCount} active filters`}>
          {badgeCount}
        </span>
      {/if}
    </button>
  </Card>

  {#if isOpen}
    <div
      bind:this={panelRef}
      class="activity-filters__panel kit-popover-card"
      style={panelStyle}
      aria-label="Activity filters"
      tabindex="-1"
      {@attach portalToBody}
      {@attach trapFocus}
    >
      <section class="activity-filters__author">
        <div class="activity-filters__section-title">Author</div>
        <Typeahead
          options={authorOptions}
          value={author}
          fallbackLabel="Anyone"
          placeholder="Filter authors"
          title="Filter by PR or issue author"
          allowClear
          clearLabel="Anyone"
          loading={authorLoading}
          error={authorError}
          onselect={onAuthorSelect}
        />
      </section>

      {#each sections as section, index (section.title ?? `section-${index}`)}
        <div class="activity-filters__divider"></div>
        {#if section.title}
          <div class="activity-filters__section-title">{section.title}</div>
        {/if}
        <div
          class="activity-filters__items"
          role={section.selectionMode === "single" ? "radiogroup" : undefined}
          aria-label={section.selectionMode === "single" ? section.title : undefined}
        >
          {#each section.items as item (item.id)}
            <button
              class="activity-filters__item"
              class:active={item.active}
              type="button"
              role={section.selectionMode === "single" ? "radio" : undefined}
              aria-checked={section.selectionMode === "single" ? item.active : undefined}
              aria-pressed={section.selectionMode === "single" ? undefined : item.active}
              tabindex={radioTabIndex(section, item)}
              disabled={item.disabled}
              title={item.description}
              onclick={() => select(item)}
              onkeydown={(event) => handleRadioKeydown(event, section, item)}
            >
              <span
                class="activity-filters__dot"
                style:background={item.active
                  ? (item.color ?? "var(--accent-blue)")
                  : "var(--border-muted)"}
              ></span>
              <span class="activity-filters__label">{item.label}</span>
              {#if item.count !== undefined}
                <span class="activity-filters__count">{item.count}</span>
              {/if}
              <span class="activity-filters__check" class:on={item.active}>
                {#if item.active}
                  <CheckIcon size={10} strokeWidth={2.2} aria-hidden="true" />
                {/if}
              </span>
            </button>
          {/each}
        </div>
      {/each}

      {#if badgeCount > 0 && onReset}
        <button class="activity-filters__reset" type="button" onclick={reset}>
          Reset filters
        </button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .activity-filters {
    position: relative;
  }

  .activity-filters.compact {
    min-width: 0;
    max-width: 100%;
  }

  .activity-filters :global(.activity-filters__trigger-shell > .kit-card__body) {
    min-width: 0;
    display: flex;
  }

  .activity-filters__trigger {
    min-height: 22px;
    width: 100%;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: 3px 10px;
    border: 0;
    color: var(--text-muted);
    background: transparent;
    font-family: inherit;
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-medium, 500);
    cursor: pointer;
  }

  .activity-filters__trigger:hover {
    color: var(--text-secondary);
  }

  .activity-filters__trigger.active {
    color: var(--accent-blue);
  }

  .activity-filters__trigger:focus-visible,
  .activity-filters__item:focus-visible,
  .activity-filters__reset:focus-visible {
    outline: var(--focus-ring);
    outline-offset: -2px;
  }

  .activity-filters__detail {
    color: var(--text-secondary);
  }

  .compact .activity-filters__trigger {
    width: 100%;
    max-width: 100%;
  }

  .compact :global(.activity-filters__trigger-shell) {
    width: 100%;
  }

  .compact .activity-filters__detail {
    min-width: 0;
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .activity-filters__badge {
    min-width: 14px;
    padding: 0 4px;
    border-radius: 6px;
    color: white;
    background: var(--accent-blue);
    font-size: 0.9em;
    font-weight: var(--font-weight-bold, 700);
    line-height: 14px;
    text-align: center;
  }

  .activity-filters__panel {
    position: fixed;
    z-index: var(--z-popover);
    min-width: 240px;
    max-height: min(520px, calc(100vh - 16px));
    padding: 4px 0;
    overflow-y: auto;
    overscroll-behavior: contain;
  }

  .activity-filters__author :global(.kit-typeahead) {
    width: auto;
    margin: 0 8px 6px;
  }

  .activity-filters__author :global(.kit-typeahead__trigger) {
    width: 100%;
    justify-content: space-between;
  }

  .activity-filters__section-title {
    padding: 4px 12px;
    color: var(--text-muted);
    font-size: 0.9em;
    font-weight: var(--font-weight-semibold, 600);
    letter-spacing: var(--letter-spacing-label, 0.04em);
    text-transform: uppercase;
  }

  .activity-filters__divider {
    height: 1px;
    margin: 4px 8px;
    background: var(--border-muted);
  }

  .activity-filters__item {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 12px;
    border: 0;
    color: var(--text-secondary);
    background: transparent;
    font-family: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .activity-filters__item:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .activity-filters__item:not(.active) {
    opacity: 0.5;
  }

  .activity-filters__item:disabled {
    cursor: default;
  }

  .activity-filters__dot {
    width: 6px;
    height: 6px;
    flex-shrink: 0;
    border-radius: var(--radius-dot, 50%);
  }

  .activity-filters__label {
    flex: 1;
  }

  .activity-filters__count {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .activity-filters__check {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--accent-green);
  }

  .activity-filters__reset {
    width: calc(100% - 16px);
    display: block;
    margin: 4px 8px 2px;
    padding: 8px 8px 4px;
    border: 0;
    border-top: 1px solid var(--border-muted);
    color: var(--text-muted);
    background: transparent;
    font-family: inherit;
    font-size: var(--font-size-2xs);
    text-align: center;
    cursor: pointer;
  }

  .activity-filters__reset:hover {
    color: var(--text-primary);
  }
</style>
