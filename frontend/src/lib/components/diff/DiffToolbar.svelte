<script lang="ts">
  import { IconButton, SelectDropdown, SidebarToggle, Toggle } from "@kenn-io/kit-ui";
  import ChevronsDownUp from "@lucide/svelte/icons/chevrons-down-up";
  import ChevronsUpDown from "@lucide/svelte/icons/chevrons-up-down";
  import MoreHorizontalIcon from "@lucide/svelte/icons/more-horizontal";
  import { getStores } from "../../context.js";
  import {
    diffFileCategoryOptions,
    type DiffFileCategoryFilter,
  } from "../../utils/diff-categories.js";
  import DiffScopePicker from "./DiffScopePicker.svelte";
  import FileJumpPicker from "./FileJumpPicker.svelte";

  interface Props {
    compact?: boolean;
    /** Phone layout: one row with a category dropdown, the commit scope,
     * file search, and the overflow menu, which also holds collapse-all. */
    phone?: boolean;
    fileListHidden?: boolean;
    onToggleFileList?: (() => void) | undefined;
    showRichPreview?: boolean;
    showScopePicker?: boolean;
    showFileJump?: boolean;
    disabled?: boolean;
  }

  let {
    compact = false,
    phone = false,
    fileListHidden = false,
    onToggleFileList,
    showRichPreview = true,
    showScopePicker = true,
    showFileJump = false,
    disabled = false,
  }: Props = $props();

  const { diff } = getStores();
  const tabOptions = [1, 2, 4, 8] as const;
  const categoryCounts = $derived(diff.getFileCategoryCounts());
  const activeCategory = $derived(diff.getFileCategoryFilter());
  const activeCategoryLabel = $derived(
    diffFileCategoryOptions.find((option) => option.value === activeCategory)?.label ??
      "All",
  );
  const visibleFileCount = $derived(
    diff.getVisibleFileList()?.files.length ?? diff.getVisibleDiffFiles().length,
  );
  const categorySelectOptions = $derived(
    diffFileCategoryOptions.map((option) => ({
      value: option.value,
      label: `${option.label} (${categoryCounts[option.value]})`,
    })),
  );
  const shouldShowFileJump = $derived(phone || showFileJump || visibleFileCount >= 10);
  const allVisibleFilesCollapsed = $derived(diff.areAllVisibleFilesCollapsed());
  const collapseAllLabel = $derived(
    allVisibleFilesCollapsed ? "Expand all diffs" : "Collapse all diffs",
  );
  let menuOpen = $state(false);
  let compactMenuRef = $state<HTMLDivElement>();

  $effect(() => {
    if (disabled) menuOpen = false;
  });

  function setFileCategoryFilter(value: DiffFileCategoryFilter): void {
    if (disabled) return;
    diff.setFileCategoryFilter(value);
  }

  function closeCompactMenu(): void {
    menuOpen = false;
  }

  function handleDocumentClick(event: MouseEvent): void {
    if (!menuOpen) return;
    const target = event.target;
    if (target instanceof Node && compactMenuRef?.contains(target)) return;
    closeCompactMenu();
  }

  function handleDocumentKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") closeCompactMenu();
  }
</script>

{#snippet menuContent(showFileFilters: boolean)}
  {#if phone}
    <div class="compact-menu-section">
      <button
        class="compact-menu-item compact-menu-item--action"
        type="button"
        disabled={disabled || visibleFileCount === 0}
        onclick={() => {
          diff.setAllVisibleFilesCollapsed(!allVisibleFilesCollapsed);
          closeCompactMenu();
        }}
      >
        {#if allVisibleFilesCollapsed}
          <ChevronsUpDown size={16} aria-hidden="true" />
        {:else}
          <ChevronsDownUp size={16} aria-hidden="true" />
        {/if}
        <span>{collapseAllLabel}</span>
      </button>
    </div>
  {/if}
  {#if showFileFilters}
    <div class="compact-menu-section">
      <div class="compact-menu-title">Files</div>
      <div class="compact-menu-grid" role="group" aria-label="Filter changed files">
        {#each diffFileCategoryOptions as option (option.value)}
          <button
            class="compact-menu-item"
            class:compact-menu-item--active={activeCategory === option.value}
            aria-pressed={activeCategory === option.value}
            type="button"
            disabled={disabled}
            onclick={() => setFileCategoryFilter(option.value)}
          >
            <span>{option.label}</span>
            <span class="category-count">({categoryCounts[option.value]})</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
  <div class="compact-menu-section">
    <div class="compact-menu-title">Tab width</div>
    <div class="compact-menu-grid" role="group" aria-label="Tab width">
      {#each tabOptions as opt (opt)}
        <button
          class="compact-menu-item"
          class:compact-menu-item--active={diff.getTabWidth() === opt}
          aria-pressed={diff.getTabWidth() === opt}
          type="button"
          disabled={disabled}
          onclick={() => diff.setTabWidth(opt)}
        >
          {opt}
        </button>
      {/each}
    </div>
  </div>
  <div class="compact-menu-section">
    {#if onToggleFileList}
      <Toggle
        class="compact-switch-row"
        checked={!fileListHidden}
        disabled={disabled}
        ariaLabel="File list"
        onchange={() => onToggleFileList()}
      >
        File list
      </Toggle>
    {/if}
    <Toggle
      class="compact-switch-row"
      checked={diff.getHideWhitespace()}
      disabled={disabled}
      ariaLabel="Hide whitespace changes"
      onchange={diff.setHideWhitespace}
    >
      Hide whitespace
    </Toggle>
    <Toggle
      class="compact-switch-row"
      checked={diff.getViewMode() === "split"}
      disabled={disabled}
      ariaLabel="Side-by-side diffs"
      onchange={(checked) => diff.setViewMode(checked ? "split" : "unified")}
    >
      Side-by-side
    </Toggle>
    <Toggle
      class="compact-switch-row"
      checked={diff.getWordWrap()}
      disabled={disabled}
      ariaLabel="Word wrap"
      onchange={diff.setWordWrap}
    >
      Word wrap
    </Toggle>
    {#if showRichPreview}
      <Toggle
        class="compact-switch-row"
        checked={diff.getRichPreview()}
        disabled={disabled}
        ariaLabel="Rich preview"
        onchange={diff.setRichPreview}
      >
        Rich preview
      </Toggle>
    {/if}
  </div>
{/snippet}

<svelte:document onclick={handleDocumentClick} onkeydown={handleDocumentKeydown} />

<div
  class={[
    "diff-toolbar",
    compact && "diff-toolbar--compact",
    phone && "diff-toolbar--phone",
  ]}
>
  {#if fileListHidden && onToggleFileList && !phone}
    <SidebarToggle
      state="collapsed"
      label="file tree"
      onclick={onToggleFileList}
      class="kit-sidebar-toggle--compact"
    />
  {/if}
  {#if phone}
    <SelectDropdown
      class="diff-toolbar__category-select"
      title="Files"
      value={activeCategory}
      options={categorySelectOptions}
      {disabled}
      onchange={(value) => setFileCategoryFilter(value as DiffFileCategoryFilter)}
    />
  {:else if compact}
    <div class="compact-summary">
      <span class="toolbar-label">Files</span>
      <span class="compact-summary-value">
        {activeCategoryLabel}
        <span class="category-count">({categoryCounts[activeCategory]})</span>
      </span>
      <span class="compact-summary-detail">Tab {diff.getTabWidth()}</span>
    </div>
  {:else}
    <div class="toolbar-group toolbar-group--category">
      <span class="toolbar-label">Files</span>
      <div class="category-toggle" role="group" aria-label="Filter changed files">
        {#each diffFileCategoryOptions as option (option.value)}
          <button
            class="category-btn"
            class:category-btn--active={activeCategory === option.value}
            aria-pressed={activeCategory === option.value}
            disabled={disabled}
            onclick={() => setFileCategoryFilter(option.value)}
          >
            <span>{option.label}</span> <span class="category-count">({categoryCounts[option.value]})</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
  {#if showScopePicker}
    <DiffScopePicker compact={phone} {disabled} />
  {/if}
  <div class="toolbar-actions">
    {#if !phone}
      <IconButton
        size="sm"
        ariaLabel={collapseAllLabel}
        title={collapseAllLabel}
        disabled={disabled || visibleFileCount === 0}
        onclick={() => diff.setAllVisibleFilesCollapsed(!allVisibleFilesCollapsed)}
      >
        {#if allVisibleFilesCollapsed}
          <ChevronsUpDown size={16} aria-hidden="true" />
        {:else}
          <ChevronsDownUp size={16} aria-hidden="true" />
        {/if}
      </IconButton>
    {/if}
    {#if shouldShowFileJump}
      <FileJumpPicker {disabled} />
    {/if}
    <div class="compact-menu-wrap" bind:this={compactMenuRef}>
      <button
        class="compact-more-btn"
        class:compact-more-btn--active={menuOpen}
        type="button"
        aria-label="More diff filters"
        aria-expanded={menuOpen}
        title="More diff filters"
        disabled={disabled}
        onclick={() => {
          if (disabled) return;
          menuOpen = !menuOpen;
        }}
      >
        <MoreHorizontalIcon size={16} strokeWidth={2} aria-hidden="true" />
      </button>
      {#if menuOpen}
        <div class="compact-menu">
          {@render menuContent(compact && !phone)}
        </div>
      {/if}
    </div>
  </div>
</div>

<style>
  .diff-toolbar {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 6px 16px;
    background: var(--diff-toolbar-bg);
    border-bottom: 1px solid var(--diff-border);
    flex-shrink: 0;
  }

  .diff-toolbar--compact {
    justify-content: space-between;
    gap: 8px;
    padding: 6px 10px;
  }

  .toolbar-group {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .toolbar-group--category {
    flex: 1 1 auto;
    min-width: 0;
  }

  .toolbar-label {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    user-select: none;
    white-space: nowrap;
  }

  .compact-summary {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    white-space: nowrap;
  }

  .compact-summary-value {
    color: var(--text-primary);
    font-weight: 600;
  }

  .compact-summary-detail {
    color: var(--text-muted);
  }

  .toolbar-actions {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-left: auto;
    flex-shrink: 0;
  }

  .compact-menu-wrap {
    position: relative;
    flex-shrink: 0;
  }

  .compact-more-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 24px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    background: var(--bg-surface);
  }

  .compact-more-btn:hover,
  .compact-more-btn--active {
    color: var(--accent-blue);
    border-color: var(--accent-blue);
  }

  .compact-menu {
    position: absolute;
    z-index: var(--z-popover);
    top: calc(100% + 4px);
    right: 0;
    width: min(224px, calc(100cqw - 20px));
    padding: 6px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
  }

  .compact-menu-section {
    padding: 5px 4px;
  }

  .compact-menu-section + .compact-menu-section {
    border-top: 1px solid var(--border-muted);
  }

  .compact-menu-title {
    margin-bottom: 5px;
    color: var(--text-muted);
    font-size: 0.9em;
    font-weight: 700;
    letter-spacing: var(--letter-spacing-label, 0.06em);
    text-transform: var(--label-transform, uppercase);
  }

  .compact-menu-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 4px;
  }

  .compact-menu-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-height: 26px;
    padding: 4px 6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    text-align: left;
  }

  .compact-menu-item {
    justify-content: space-between;
  }

  .compact-menu-item:hover,
  :global(.compact-switch-row:hover) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .compact-menu-item--active {
    background: var(--bg-inset);
    color: var(--text-primary);
    font-weight: 600;
  }

  :global(.compact-switch-row) {
    flex-direction: row-reverse;
    justify-content: space-between;
    width: 100%;
    min-height: 26px;
    padding: 4px 6px;
    border-radius: var(--radius-sm);
  }

  :global(.compact-switch-row .kit-toggle__label) {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  :global(.compact-switch-row:hover .kit-toggle__label) {
    color: var(--text-primary);
  }

  .category-toggle {
    display: flex;
    gap: 2px;
    min-width: 0;
    padding: 2px;
    background: var(--bg-inset);
    border-radius: 6px;
  }

  .category-btn {
    min-width: 56px;
    padding: 2px 8px;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    font-size: var(--font-size-xs);
    font-weight: 500;
    white-space: nowrap;
  }

  .category-btn:hover {
    color: var(--text-primary);
  }

  .category-btn--active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
  }

  .category-count {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  @media (max-width: 760px) {
    .diff-toolbar:not(.diff-toolbar--phone) {
      align-items: flex-start;
      flex-direction: column;
    }

    .diff-toolbar:not(.diff-toolbar--phone) .toolbar-actions {
      margin-left: 0;
    }
  }

  /* Phone: every control on one row at the phone chrome height. The category
     dropdown takes the leftover width so the row never wraps. */
  .diff-toolbar--phone {
    --kit-control-height: var(--mobile-chrome-hit-target);
    gap: 0.375rem;
    padding: 0.375rem 0.625rem;
  }

  .diff-toolbar--phone :global(.diff-toolbar__category-select) {
    flex: 1 1 auto;
    min-width: 0;
  }

  .diff-toolbar--phone :global(.diff-toolbar__category-select .kit-select-dropdown__trigger) {
    border-color: var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .diff-toolbar--phone :global(.diff-scope-picker__trigger.kit-button) {
    height: var(--mobile-chrome-hit-target);
    font-size: var(--font-size-sm);
  }

  .diff-toolbar--phone :global(.diff-scope-picker__trigger .diff-scope-label) {
    font-size: var(--font-size-sm);
  }

  .diff-toolbar--phone .toolbar-actions {
    gap: 0.375rem;
  }

  .diff-toolbar--phone :global(.file-jump .kit-icon-button),
  .diff-toolbar--phone .compact-more-btn {
    width: var(--mobile-chrome-hit-target);
    height: var(--mobile-chrome-hit-target);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
  }

  .compact-menu-item--action {
    justify-content: flex-start;
  }

  .compact-menu-item:disabled {
    opacity: var(--opacity-disabled);
  }
</style>
