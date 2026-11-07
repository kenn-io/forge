<script lang="ts">
  import { onDestroy } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import {
    Button,
    FilterDropdown,
    formatRelativeTime,
    IconButton,
    ScrollBox,
    SegmentedControl,
    SidebarToggle,
    Typeahead,
    type MenuAnchorPoint,
    type SegmentedControlOption,
  } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import LinkIcon from "@lucide/svelte/icons/link";
  import type { Worklist, WorklistItem, WorklistSummary } from "../../api/worklists.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { getSidebar, getStores } from "../../context.js";
  import type { ListItemTarget } from "../../stores/item-actions.js";
  import { registerScopedActions } from "../../stores/keyboard/registry.svelte.js";
  import {
    cycleLabelFilter,
    labelFilterChoices,
    labelFilterSummary,
    matchesLabelFilters,
    type LabelFilters,
  } from "../../utils/label-filter.js";
  import { createRangeSelection, isSelectAllShortcut } from "../../utils/range-selection.svelte.js";
  import { createRepoLabelFormatter, type RepoLabelIdentity } from "../../utils/repo-label.js";
  import { isEmptySearchQuery, parseSearchQuery } from "../../utils/search-query.js";
  import GroupedSidebarSection from "../shared/GroupedSidebarSection.svelte";
  import LabelFilterMenu from "../shared/LabelFilterMenu.svelte";
  import QuerySearchInput from "../shared/QuerySearchInput.svelte";
  import ItemActionsMenu from "../sidebar/ItemActionsMenu.svelte";
  import WorklistRow from "./WorklistRow.svelte";
  import {
    readWorklistOrder,
    readWorklistStateFilter,
    worklistItemClosed,
    worklistItemDone,
    worklistItemKey,
    worklistItemMatchesFilters,
    worklistItemMatchesSearch,
    worklistItemNotFresh,
    worklistItemsByScore,
    worklistItemsInState,
    worklistItemTarget,
    worklistPickerOptions,
    worklistSections,
    writeWorklistOrder,
    writeWorklistStateFilter,
    WORKLIST_FILTER_SECTIONS,
    type WorklistItemFilter,
    type WorklistOrder,
    type WorklistStateFilter,
  } from "./worklist-items.js";
  import { toggleFilterInSections } from "../../utils/pull-attributes.js";

  interface Props {
    worklist: Worklist;
    summaries: readonly WorklistSummary[];
    selectedKey: string | null;
    dismissing?: boolean;
    onOpen: (item: WorklistItem) => void;
    /** Opens the next or previous visible row. */
    onStep: (delta: 1 | -1) => void;
    onPick: (id: number) => void;
    onDismiss: () => void;
    onItemsChanged: () => void;
    onSetHandled: (items: readonly WorklistItem[], handled: boolean) => void;
    onInvolvesMeChange: (active: boolean) => void;
    /** Copies a link that opens this list in another Forge; omitted when links are off. */
    onCopyLink?: (() => void) | undefined;
    /** Receives the keys of the rows on screen, in order, for each list shown. */
    onVisibleOrder?: (worklist: Worklist, keys: readonly string[]) => void;
  }

  let {
    worklist,
    summaries,
    selectedKey,
    dismissing = false,
    onOpen,
    onStep,
    onPick,
    onDismiss,
    onItemsChanged,
    onSetHandled,
    onInvolvesMeChange,
    onCopyLink = undefined,
    onVisibleOrder = undefined,
  }: Props = $props();

  const { toggleSidebar } = getSidebar();
  const { grouping } = getStores();

  let recipeOpen = $state(false);
  let changedOnly = $state(false);
  let order = $state<WorklistOrder>(readWorklistOrder());
  let stateFilter = $state<WorklistStateFilter>(readWorklistStateFilter());
  let search = $state("");
  let itemFilters = $state<WorklistItemFilter[]>([]);
  let labelFilters = $state.raw<LabelFilters>(new Map());
  onDestroy(() => onInvolvesMeChange(false));
  let railElement = $state<HTMLElement | undefined>();

  const collapsedSections = new SvelteSet<string>();

  function itemLabels(item: WorklistItem) {
    return (item.pull ?? item.issue)?.labels;
  }

  const closedCount = $derived(worklist.items.filter(worklistItemClosed).length);
  const searchQuery = $derived(parseSearchQuery(search));
  const searching = $derived(!isEmptySearchQuery(searchQuery));
  const shownItems = $derived(
    worklistItemsInState(worklist.items, stateFilter).filter(
      (item) =>
        (!searching || worklistItemMatchesSearch(item, searchQuery))
        && worklistItemMatchesFilters(item, itemFilters)
        && matchesLabelFilters(itemLabels(item), labelFilters),
    ),
  );
  const changedCount = $derived(shownItems.filter(worklistItemNotFresh).length);
  // Stays on while a search or state filter leaves no changed rows; only a
  // list with nothing changed at all turns it off.
  const changedFilterActive = $derived(changedOnly && worklist.items.some(worklistItemNotFresh));
  const filteredItems = $derived(changedFilterActive ? shownItems.filter(worklistItemNotFresh) : shownItems);
  const sections = $derived(worklistSections(filteredItems));
  const showSectionHeaders = $derived(order === "agent" && sections.some((section) => section.section !== ""));
  const authorLabel = $derived(worklist.created_by.trim() || "an agent");
  // Merged and closed items need nothing more, so they count as done with handled ones.
  const doneCount = $derived(worklist.items.filter(worklistItemDone).length);
  const donePercent = $derived(worklist.items.length === 0 ? 0 : (doneCount / worklist.items.length) * 100);

  function sectionKey(section: string): string {
    return `${worklist.id}\u0000${section}`;
  }

  function sectionCollapsed(section: string): boolean {
    return showSectionHeaders && collapsedSections.has(sectionKey(section));
  }

  function toggleSection(section: string): void {
    const key = sectionKey(section);
    if (collapsedSections.has(key)) collapsedSections.delete(key);
    else collapsedSections.add(key);
  }

  const visibleRows = $derived(
    order === "score"
      ? worklistItemsByScore(filteredItems)
      : sections.filter((section) => !sectionCollapsed(section.section)).flatMap((section) => section.items),
  );

  $effect(() => {
    onVisibleOrder?.(worklist, visibleRows.map(worklistItemKey));
  });

  const orderOptions: SegmentedControlOption[] = [
    { value: "agent", label: "Agent order", title: "The agent's rank, grouped by section" },
    { value: "score", label: "Score", title: "Highest score first" },
  ];

  function setOrder(value: string): void {
    order = value === "score" ? "score" : "agent";
    writeWorklistOrder(order);
  }

  const stateOptions: SegmentedControlOption[] = [
    { value: "open", label: "Open", title: "Items still open" },
    { value: "closed", label: "Closed", title: "Merged, merge-queued, closed, or no longer synced" },
    { value: "all", label: "All", title: "Open and closed items" },
  ];

  function setStateFilter(value: string): void {
    stateFilter = value === "closed" || value === "all" ? value : "open";
    writeWorklistStateFilter(stateFilter);
  }

  const emptyFilterMessage = $derived.by(() => {
    if (searching) return "No items match this search.";
    if (itemFilters.length > 0 || labelFilters.size > 0) return "No items match these filters.";
    if (changedFilterActive) return "No shown items changed since the list was written.";
    return stateFilter === "closed" ? "No items are closed." : "No open items remain. Closed items are hidden.";
  });

  // Every selected filter must match. Review, CI, and Conflicts are pick-one pairs of opposites.
  const filterGroups: { title: string; options: { value: WorklistItemFilter; label: string }[] }[] = [
    {
      title: "Items",
      options: [
        { value: "involves_me", label: "Involves me" },
        { value: "approved", label: "Approved" },
        { value: "has_workspace", label: "Has workspace" },
      ],
    },
    {
      title: "Review",
      options: [
        { value: "draft", label: "Draft" },
        { value: "ready", label: "Ready for review" },
      ],
    },
    {
      title: "CI",
      options: [
        { value: "passed_ci", label: "Passing CI" },
        { value: "failed_ci", label: "Failed CI" },
      ],
    },
    {
      title: "Conflicts",
      options: [
        { value: "merge_conflicts", label: "Merge conflicts" },
        { value: "no_merge_conflicts", label: "No merge conflicts" },
      ],
    },
  ];

  function toggleItemFilter(filter: WorklistItemFilter): void {
    itemFilters = toggleFilterInSections(WORKLIST_FILTER_SECTIONS, itemFilters, filter);
    onInvolvesMeChange(itemFilters.includes("involves_me"));
  }

  const labelChoices = $derived(
    labelFilterChoices(worklist.items.map((item) => ({ labels: itemLabels(item) })), labelFilters),
  );

  const filterSections = $derived([
    ...filterGroups.map((group, index) => ({
      title: group.title,
      items: [
        ...(index === 0
          ? [
              {
                id: "worklist-labels",
                label: "Labels",
                detail: labelFilterSummary(labelFilters),
                active: labelFilters.size > 0,
                submenu: labelFilterMenu,
                onSelect: () => {},
              },
            ]
          : []),
        ...group.options.map((option) => ({
          id: `worklist-${option.value}`,
          label: option.label,
          active: itemFilters.includes(option.value),
          onSelect: () => toggleItemFilter(option.value),
        })),
      ],
    })),
    {
      title: "View",
      items: [
        {
          id: "hide-org-name",
          label: "Hide org name",
          active: grouping.getHideOrgName(),
          onSelect: () => grouping.setHideOrgName(!grouping.getHideOrgName()),
        },
      ],
    },
  ]);
  const filterCount = $derived(itemFilters.length + labelFilters.size + Number(grouping.getHideOrgName()));

  function clearFilters(): void {
    itemFilters = [];
    labelFilters = new Map();
    onInvolvesMeChange(false);
    grouping.setHideOrgName(false);
  }

  function repoIdentity(item: WorklistItem): RepoLabelIdentity {
    return {
      provider: item.repo.provider,
      platformHost: item.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(item.repo),
      owner: item.repo.owner,
      name: item.repo.name,
      repoPath: item.repo.repo_path,
    };
  }

  const repoLabels = $derived(
    createRepoLabelFormatter(worklist.items.map(repoIdentity), { showOrgNames: !grouping.getHideOrgName() }),
  );

  const pickerOptions = $derived(worklistPickerOptions(summaries));

  const selection = createRangeSelection<WorklistItem>({
    rows: () => visibleRows,
    key: worklistItemKey,
    openedKey: () => selectedKey,
  });

  let contextMenu = $state.raw<{
    targets: ListItemTarget[];
    anchor: MenuAnchorPoint;
    returnFocus: HTMLElement | undefined;
  } | null>(null);
  let contextMenuOpen = $state(false);

  function targetsFor(items: readonly WorklistItem[]): ListItemTarget[] {
    return items.flatMap((item) => {
      const target = worklistItemTarget(item);
      return target ? [target] : [];
    });
  }

  const selectedTargets = $derived(targetsFor(selection.selected));
  const selectionAllHandled = $derived(
    selection.selected.length > 0 && selection.selected.every((item) => item.handled_at !== null),
  );

  // Clear at once: waiting for the response could clear a newer selection.
  function markSelection(): void {
    const items = selection.selected;
    if (items.length === 0) return;
    const handled = !selectionAllHandled;
    selection.clear();
    onSetHandled(items, handled);
  }

  function handleRowClick(event: MouseEvent, item: WorklistItem): void {
    if (selection.activate(event, item)) onOpen(item);
  }

  function openContextMenu(event: MouseEvent, item: WorklistItem): void {
    const multi = selection.selected.length > 1 && selection.has(item);
    const targets = targetsFor(multi ? selection.selected : [item]);
    if (targets.length === 0) return;
    event.preventDefault();
    if (!multi) selection.clear();
    contextMenu = {
      targets,
      anchor: { x: event.clientX, y: event.clientY },
      returnFocus: event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined,
    };
    contextMenuOpen = true;
  }

  function openSelectionMenu(event: MouseEvent): void {
    if (selectedTargets.length === 0) return;
    const trigger = event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined;
    const rect = trigger?.getBoundingClientRect();
    contextMenu = {
      targets: selectedTargets,
      anchor: { x: rect?.left ?? event.clientX, y: rect?.bottom ?? event.clientY },
      returnFocus: trigger,
    };
    contextMenuOpen = true;
  }

  function handleSelectionKeys(event: KeyboardEvent): void {
    if (event.key === "Escape" && selection.active) {
      event.preventDefault();
      selection.clear();
    } else if (isSelectAllShortcut(event)) {
      event.preventDefault();
      selection.selectAll();
    }
  }

  function focusInRail(): boolean {
    return railElement !== undefined && railElement.contains(document.activeElement);
  }

  $effect(() => {
    return registerScopedActions("worklist-rail", [
      {
        id: "worklists.next.arrow",
        label: "Next worklist item",
        scope: "view-worklists",
        binding: { key: "ArrowDown" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists" && focusInRail(),
        visible: () => false,
        handler: () => onStep(1),
      },
      {
        id: "worklists.prev.arrow",
        label: "Previous worklist item",
        scope: "view-worklists",
        binding: { key: "ArrowUp" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists" && focusInRail(),
        visible: () => false,
        handler: () => onStep(-1),
      },
    ]);
  });
</script>

{#snippet labelFilterMenu()}
  <LabelFilterMenu
    choices={labelChoices}
    oncycle={(name) => (labelFilters = cycleLabelFilter(labelFilters, name))}
    emptyText="No labels on this worklist"
  />
{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="worklist-rail" bind:this={railElement} onkeydown={handleSelectionKeys}>
  <header class="worklist-rail-header">
    <div class="worklist-title-row">
      <h2 class="worklist-title">
        {#if summaries.length > 1}
          <div class="worklist-picker">
            <Typeahead
              value={String(worklist.id)}
              options={pickerOptions}
              fallbackLabel={worklist.title}
              placeholder="Worklist"
              emptyLabel="No worklists match"
              onselect={(value) => onPick(Number(value))}
            />
          </div>
        {:else}
          {worklist.title}
        {/if}
      </h2>
      {#if onCopyLink}
        <IconButton size="sm" ariaLabel="Copy link" title="Copy a link that opens this list in another Forge" onclick={onCopyLink}>
          <LinkIcon size={14} aria-hidden="true" />
        </IconButton>
      {/if}
      <Button size="sm" label={dismissing ? "Dismissing" : "Dismiss"} disabled={dismissing} onclick={onDismiss} />
      <SidebarToggle state="expanded" label="sidebar" onclick={toggleSidebar} class="kit-sidebar-toggle--push" />
    </div>
    <div class="worklist-search-row">
      <QuerySearchInput
        bind:value={search}
        size="sm"
        block
        placeholder="Filter items..."
        ariaLabel="Filter worklist items"
      />
      <span class="worklist-filter-menu">
        <FilterDropdown
          label="Filters"
          title="Filters"
          active={filterCount > 0}
          badgeCount={filterCount}
          sections={filterSections}
          resetLabel="Clear filters"
          onReset={clearFilters}
          minWidth="190px"
        />
      </span>
    </div>
    <div class="worklist-meta">
      <span>Written by {authorLabel} {formatRelativeTime(worklist.updated_at)}</span>
      {#if changedCount > 0 || changedFilterActive}
        <button
          type="button"
          class="worklist-changed-toggle"
          class:worklist-changed-toggle--on={changedFilterActive}
          aria-pressed={changedFilterActive}
          onclick={() => (changedOnly = !changedOnly)}
        >{changedCount} changed since</button>
      {/if}
      {#if worklist.recipe.trim() !== ""}
        <button
          type="button"
          class="worklist-recipe-toggle"
          aria-expanded={recipeOpen}
          onclick={() => (recipeOpen = !recipeOpen)}
        >
          {#if recipeOpen}<ChevronDownIcon size={12} aria-hidden="true" />{:else}<ChevronRightIcon size={12} aria-hidden="true" />{/if}
          Recipe
        </button>
      {/if}
    </div>
    <div class="worklist-controls">
      <SegmentedControl options={stateOptions} value={stateFilter} onchange={setStateFilter} ariaLabel="Item state">
        {#snippet segment(option)}
          {option.label}{#if option.value === "closed" && closedCount > 0}<span class="worklist-state-count">{closedCount}</span>{/if}
        {/snippet}
      </SegmentedControl>
      <span class="worklist-order">
        <SegmentedControl options={orderOptions} value={order} onchange={setOrder} ariaLabel="Row order" />
      </span>
    </div>
    <div class="worklist-progress">
      <span class="worklist-progress-bar" aria-hidden="true">
        <span style:width={`${donePercent}%`}></span>
      </span>
      <span title="Handled, merged, merge-queued, or closed">{doneCount} of {worklist.items.length} done</span>
    </div>
    {#if recipeOpen}
      <!-- A long recipe scrolls, so it needs keyboard access. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="worklist-recipe" role="region" aria-label="Recipe" tabindex="0">{worklist.recipe}</div>
    {/if}
    {#if selection.active}
      <div class="worklist-selection-bar">
        <span>{selection.selected.length} selected</span>
        <Button
          size="sm"
          label="Actions"
          disabled={selectedTargets.length === 0}
          ariaExpanded={contextMenuOpen}
          onclick={openSelectionMenu}
        />
        <Button size="sm" label={selectionAllHandled ? "Mark unhandled" : "Mark handled"} onclick={markSelection} />
        <Button size="sm" label="Clear" onclick={() => selection.clear()} />
      </div>
    {/if}
  </header>

  <ScrollBox label={`${worklist.title} items`} class="worklist-scroll">
    {#if worklist.items.length === 0}
      <p class="worklist-rail-empty">No items remain on this list.</p>
    {:else if filteredItems.length === 0}
      <p class="worklist-rail-empty">{emptyFilterMessage}</p>
    {:else if showSectionHeaders}
      {#each sections as section (section.section)}
        {@const collapsed = sectionCollapsed(section.section)}
        <GroupedSidebarSection
          label={section.section || "Other"}
          count={section.items.length}
          {collapsed}
          onclick={() => toggleSection(section.section)}
        >
          {@render rows(section.items, section.section || "Other")}
        </GroupedSidebarSection>
      {/each}
    {:else}
      {@render rows(visibleRows, worklist.title)}
    {/if}
  </ScrollBox>
</div>

{#snippet rows(items: readonly WorklistItem[], label: string)}
  <ul class="worklist-rows" aria-label={label}>
    {#each items as item (worklistItemKey(item))}
      {@const key = worklistItemKey(item)}
      <li>
        <WorklistRow
          {item}
          repoLabel={repoLabels.format(repoIdentity(item))}
          opened={key === selectedKey}
          multiSelected={selection.has(item)}
          onclick={(event) => handleRowClick(event, item)}
          oncontextmenu={(event) => openContextMenu(event, item)}
        />
      </li>
    {/each}
  </ul>
{/snippet}

<ItemActionsMenu
  bind:open={contextMenuOpen}
  anchor={contextMenu?.anchor}
  returnFocus={contextMenu?.returnFocus}
  targets={contextMenu?.targets ?? []}
  onChanged={onItemsChanged}
  selectionActive={selection.active}
  onClearSelection={() => selection.clear()}
/>

<style>
  .worklist-search-row {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    min-width: 0;
  }

  .worklist-search-row > :global(:first-child) {
    flex: 1;
    min-width: 0;
  }

  .worklist-filter-menu {
    flex-shrink: 0;
  }

  .worklist-filter-menu :global(.kit-filter-dropdown__btn) {
    position: relative;
    width: 30px;
    justify-content: center;
    gap: 0;
    padding-inline: 0;
  }

  .worklist-filter-menu :global(.kit-filter-dropdown__trigger-label) {
    display: none;
  }

  .worklist-filter-menu :global(.kit-filter-dropdown__badge) {
    position: absolute;
    top: -5px;
    right: -5px;
  }

  .worklist-rail {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    min-width: 0;
    background: var(--bg-surface);
  }

  .worklist-rail-header {
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-4) var(--space-5);
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .worklist-title-row {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    min-width: 0;
  }

  .worklist-title {
    flex: 1;
    min-width: 0;
    margin: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-weight: 600;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Long titles truncate in the list so the "N left" column stays visible. */
  .worklist-picker {
    --typeahead-max-width: none;
    --typeahead-panel-min-width: 360px;
  }

  .worklist-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2) var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .worklist-changed-toggle,
  .worklist-recipe-toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    padding: var(--space-1) var(--space-3);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm, 4px);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    cursor: pointer;
  }

  .worklist-changed-toggle--on {
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }

  .worklist-controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2) var(--space-4);
  }

  .worklist-state-count {
    margin-left: var(--space-2);
    color: var(--text-muted);
  }

  .worklist-order {
    display: inline-flex;
    margin-left: auto;
  }

  /* A long recipe scrolls inside its box so the rows below stay reachable. */
  .worklist-recipe {
    flex-shrink: 0;
    max-height: min(12rem, 30vh);
    overflow-y: auto;
    margin: 0;
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-sm, 4px);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .worklist-progress {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    white-space: nowrap;
  }

  .worklist-progress-bar {
    flex: 1;
    height: 4px;
    overflow: hidden;
    border-radius: 2px;
    background: var(--bg-inset);
  }

  .worklist-progress-bar span {
    display: block;
    height: 100%;
    background: var(--accent-green);
    transition: width 0.25s;
  }

  .worklist-selection-bar {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .worklist-selection-bar span {
    flex: 1;
  }

  .worklist-rail :global(.worklist-scroll) {
    flex: 1;
    min-height: 0;
  }

  .worklist-rail-empty {
    margin: 0;
    padding: var(--space-6) var(--space-5);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .worklist-rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }
</style>
