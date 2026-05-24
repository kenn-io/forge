<script lang="ts">
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
    fileListHidden?: boolean;
    onToggleFileList?: (() => void) | undefined;
    showRichPreview?: boolean;
    showScopePicker?: boolean;
    showFileJump?: boolean;
  }

  let {
    compact = false,
    fileListHidden = false,
    onToggleFileList,
    showRichPreview = true,
    showScopePicker = true,
    showFileJump = false,
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
  const shouldShowFileJump = $derived(showFileJump || visibleFileCount >= 10);
  const allVisibleFilesCollapsed = $derived(diff.areAllVisibleFilesCollapsed());
  const collapseAllLabel = $derived(
    allVisibleFilesCollapsed ? "Expand all diffs" : "Collapse all diffs",
  );
  let menuOpen = $state(false);
  let compactMenuRef = $state<HTMLDivElement>();

  function setFileCategoryFilter(value: DiffFileCategoryFilter): void {
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
    <button
      class="compact-menu-action"
      type="button"
      onclick={() => diff.setAllVisibleFilesCollapsed(!allVisibleFilesCollapsed)}
    >
      {collapseAllLabel}
    </button>
  </div>
  <div class="compact-menu-section">
    <div class="compact-menu-title">Tab width</div>
    <div class="compact-menu-grid" role="group" aria-label="Tab width">
      {#each tabOptions as opt (opt)}
        <button
          class="compact-menu-item"
          class:compact-menu-item--active={diff.getTabWidth() === opt}
          aria-pressed={diff.getTabWidth() === opt}
          type="button"
          onclick={() => diff.setTabWidth(opt)}
        >
          {opt}
        </button>
      {/each}
    </div>
  </div>
  <div class="compact-menu-section">
    {#if onToggleFileList}
      <button
        class="compact-switch-row"
        type="button"
        role="switch"
        aria-label="File list"
        aria-checked={!fileListHidden}
        onclick={onToggleFileList}
      >
        <span>File list</span>
        <span
          class="toggle-switch"
          class:toggle-switch--on={!fileListHidden}
          aria-hidden="true"
        >
          <span class="toggle-knob"></span>
        </span>
      </button>
    {/if}
    <button
      class="compact-switch-row"
      type="button"
      role="switch"
      aria-label="Hide whitespace changes"
      aria-checked={diff.getHideWhitespace()}
      onclick={() => diff.setHideWhitespace(!diff.getHideWhitespace())}
    >
      <span>Hide whitespace</span>
      <span
        class="toggle-switch"
        class:toggle-switch--on={diff.getHideWhitespace()}
        aria-hidden="true"
      >
        <span class="toggle-knob"></span>
      </span>
    </button>
    <button
      class="compact-switch-row"
      type="button"
      role="switch"
      aria-checked={diff.getWordWrap()}
      onclick={() => diff.setWordWrap(!diff.getWordWrap())}
    >
      <span>Word wrap</span>
      <span
        class="toggle-switch"
        class:toggle-switch--on={diff.getWordWrap()}
        aria-hidden="true"
      >
        <span class="toggle-knob"></span>
      </span>
    </button>
    {#if showRichPreview}
      <button
        class="compact-switch-row"
        type="button"
        role="switch"
        aria-checked={diff.getRichPreview()}
        onclick={() => diff.setRichPreview(!diff.getRichPreview())}
      >
        <span>Rich preview</span>
        <span
          class="toggle-switch"
          class:toggle-switch--on={diff.getRichPreview()}
          aria-hidden="true"
        >
          <span class="toggle-knob"></span>
        </span>
      </button>
    {/if}
  </div>
{/snippet}

<svelte:document onclick={handleDocumentClick} onkeydown={handleDocumentKeydown} />

<div class={["diff-toolbar", compact && "diff-toolbar--compact"]}>
  {#if compact}
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
            onclick={() => setFileCategoryFilter(option.value)}
          >
            <span>{option.label}</span> <span class="category-count">({categoryCounts[option.value]})</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
  {#if showScopePicker}
    <DiffScopePicker />
  {/if}
  <div class="toolbar-actions">
    {#if shouldShowFileJump}
      <FileJumpPicker />
    {/if}
    <div class="compact-menu-wrap" bind:this={compactMenuRef}>
      <button
        class="compact-more-btn"
        class:compact-more-btn--active={menuOpen}
        type="button"
        aria-label="More diff filters"
        aria-expanded={menuOpen}
        title="More diff filters"
        onclick={() => {
          menuOpen = !menuOpen;
        }}
      >
        <MoreHorizontalIcon size={16} strokeWidth={2} aria-hidden="true" />
      </button>
      {#if menuOpen}
        <div class="compact-menu">
          {@render menuContent(compact)}
        </div>
      {/if}
    </div>
  </div>
</div>

<style>
  .diff-toolbar {
    position: relative;
    z-index: 60;
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
    z-index: 1000;
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
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .compact-menu-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 4px;
  }

  .compact-menu-item,
  .compact-menu-action,
  .compact-switch-row {
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
  .compact-menu-action:hover,
  .compact-switch-row:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .compact-menu-item--active {
    background: var(--bg-inset);
    color: var(--text-primary);
    font-weight: 600;
  }

  .compact-switch-row {
    justify-content: space-between;
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
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.1);
  }

  .category-count {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .toggle-switch {
    position: relative;
    width: 36px;
    height: 20px;
    border-radius: 10px;
    background: var(--border-default);
    transition: background 0.2s;
    flex-shrink: 0;
  }

  .toggle-switch--on {
    background: var(--accent-blue);
  }

  .toggle-knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: #ffffff;
    transition: transform 0.2s;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
  }

  .toggle-switch--on .toggle-knob {
    transform: translateX(16px);
  }

  @media (max-width: 760px) {
    .diff-toolbar {
      align-items: flex-start;
      flex-direction: column;
    }

    .toolbar-actions {
      margin-left: 0;
    }
  }
</style>
