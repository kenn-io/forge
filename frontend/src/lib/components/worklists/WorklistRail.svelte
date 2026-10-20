<script lang="ts">
  import type { Snippet } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { Button, formatRelativeTime, ScrollBox, SelectDropdown, type MenuAnchorPoint } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import type { Worklist, WorklistItem, WorklistSummary } from "../../api/worklists.js";
  import type { ListItemTarget } from "../../stores/item-actions.js";
  import { registerScopedActions } from "../../stores/keyboard/registry.svelte.js";
  import { createRangeSelection } from "../../utils/range-selection.svelte.js";
  import GroupedSidebarSection from "../shared/GroupedSidebarSection.svelte";
  import ItemActionsMenu from "../sidebar/ItemActionsMenu.svelte";
  import WorklistRow from "./WorklistRow.svelte";
  import { worklistItemKey, worklistItemNotFresh, worklistItemTarget, worklistSections } from "./worklist-items.js";

  interface Props {
    worklist: Worklist;
    summaries: readonly WorklistSummary[];
    selectedKey: string | null;
    dismissing?: boolean;
    onOpen: (item: WorklistItem) => void;
    onPick: (id: number) => void;
    onDismiss: () => void;
    onItemsChanged: () => void;
    /** Marks or unmarks items; onApplied runs once the server applied the change. */
    onSetHandled: (items: readonly WorklistItem[], handled: boolean, onApplied?: () => void) => void;
    headerActions?: Snippet;
  }

  let {
    worklist,
    summaries,
    selectedKey,
    dismissing = false,
    onOpen,
    onPick,
    onDismiss,
    onItemsChanged,
    onSetHandled,
    headerActions = undefined,
  }: Props = $props();

  let recipeOpen = $state(false);
  let changedOnly = $state(false);
  let railElement = $state<HTMLElement | undefined>();

  const collapsedSections = new SvelteSet<string>();

  const changedCount = $derived(worklist.items.filter(worklistItemNotFresh).length);
  const changedFilterActive = $derived(changedOnly && changedCount > 0);
  const filteredItems = $derived(changedFilterActive ? worklist.items.filter(worklistItemNotFresh) : worklist.items);
  const sections = $derived(worklistSections(filteredItems));
  const showSectionHeaders = $derived(sections.some((section) => section.section !== ""));
  const authorLabel = $derived(worklist.created_by.trim() || "an agent");
  const handledCount = $derived(worklist.items.filter((item) => item.handled_at !== null).length);
  const handledPercent = $derived(worklist.items.length === 0 ? 0 : (handledCount / worklist.items.length) * 100);

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
    sections.filter((section) => !sectionCollapsed(section.section)).flatMap((section) => section.items),
  );

  const pickerOptions = $derived(summaries.map((summary) => ({ value: String(summary.id), label: summary.title })));

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

  function markSelection(): void {
    const items = selection.selected;
    if (items.length === 0) return;
    onSetHandled(items, !selectionAllHandled, () => selection.clear());
  }

  function markCurrentAndAdvance(): void {
    const current =
      selectedKey === null ? undefined : worklist.items.find((item) => worklistItemKey(item) === selectedKey);
    if (current && current.handled_at === null) onSetHandled([current], true);
    step(1);
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

  function clearSelectionOnEscape(event: KeyboardEvent): void {
    if (event.key === "Escape" && selection.active) {
      event.preventDefault();
      selection.clear();
    }
  }

  function step(delta: 1 | -1): void {
    const rows = visibleRows;
    if (rows.length === 0) return;
    const current = selectedKey === null ? -1 : rows.findIndex((row) => worklistItemKey(row) === selectedKey);
    const next = current === -1 ? (delta === 1 ? 0 : rows.length - 1) : current + delta;
    const row = rows[Math.max(0, Math.min(rows.length - 1, next))];
    if (!row) return;
    selection.clear();
    onOpen(row);
    queueMicrotask(() => {
      railElement
        ?.querySelector<HTMLElement>(`[data-worklist-key="${CSS.escape(worklistItemKey(row))}"]`)
        ?.scrollIntoView({ block: "nearest" });
    });
  }

  function focusInRail(): boolean {
    return railElement !== undefined && railElement.contains(document.activeElement);
  }

  $effect(() => {
    return registerScopedActions("worklist-rail", [
      {
        id: "worklists.next",
        label: "Next worklist item",
        scope: "view-worklists",
        binding: { key: "j" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists",
        handler: () => step(1),
      },
      {
        id: "worklists.prev",
        label: "Previous worklist item",
        scope: "view-worklists",
        binding: { key: "k" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists",
        handler: () => step(-1),
      },
      {
        id: "worklists.handled-next",
        label: "Mark handled and go to next",
        scope: "view-worklists",
        binding: { key: "d" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists",
        handler: markCurrentAndAdvance,
      },
      {
        id: "worklists.next.arrow",
        label: "Next worklist item",
        scope: "view-worklists",
        binding: { key: "ArrowDown" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists" && focusInRail(),
        visible: () => false,
        handler: () => step(1),
      },
      {
        id: "worklists.prev.arrow",
        label: "Previous worklist item",
        scope: "view-worklists",
        binding: { key: "ArrowUp" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists" && focusInRail(),
        visible: () => false,
        handler: () => step(-1),
      },
    ]);
  });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="worklist-rail" bind:this={railElement} onkeydown={clearSelectionOnEscape}>
  <header class="worklist-rail-header">
    <div class="worklist-title-row">
      <h2 class="worklist-title">
        {#if summaries.length > 1}
          <SelectDropdown
            value={String(worklist.id)}
            options={pickerOptions}
            title="Worklist"
            onchange={(value) => onPick(Number(value))}
          />
        {:else}
          {worklist.title}
        {/if}
      </h2>
      <Button size="sm" label={dismissing ? "Dismissing" : "Dismiss"} disabled={dismissing} onclick={onDismiss} />
      {@render headerActions?.()}
    </div>
    <div class="worklist-meta">
      <span>Written by {authorLabel} {formatRelativeTime(worklist.updated_at)}</span>
      {#if changedCount > 0}
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
    <div class="worklist-progress">
      <span class="worklist-progress-bar" aria-hidden="true">
        <span style:width={`${handledPercent}%`}></span>
      </span>
      <span>{handledCount} of {worklist.items.length} handled</span>
    </div>
    {#if recipeOpen}
      <p class="worklist-recipe">{worklist.recipe}</p>
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
    {#if showSectionHeaders}
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

  .worklist-recipe {
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

  .worklist-rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }
</style>
