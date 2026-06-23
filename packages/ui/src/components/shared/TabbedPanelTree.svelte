<script lang="ts">
  import type { Snippet } from "svelte";
  import Self from "./TabbedPanelTree.svelte";
  import {
    clearActiveTabbedPanelDrag,
    readTabbedPanelTabDrag,
    startTabbedPanelTabDrag,
  } from "./tabbed-panel-drag.js";
  import type {
    TabbedPanelDescriptor,
    TabbedPanelDirection,
    TabbedPanelNode,
    TabbedPanelSplitEdge,
  } from "./tabbed-panel-layout.js";
  import {
    clampTabbedPanelRatio,
    tabbedPanelSplitEdgeFromPoint,
    tabbedPanelSplitPlacementForEdge,
  } from "./tabbed-panel-layout.js";

  interface Props {
    dragScope: string;
    node: TabbedPanelNode;
    tabs: TabbedPanelDescriptor[];
    activeTabKey: string;
    renderTab: Snippet<[string, boolean]>;
    tabIcon?: Snippet<[TabbedPanelDescriptor]> | undefined;
    tabActions?: Snippet<[TabbedPanelDescriptor]> | undefined;
    scrollPanels?: boolean;
    disabled?: boolean;
    tablistLabel?: string;
    leafLabel?: string;
    resizeLabel?: string;
    dropTargetsLabel?: string;
    onSelectTab?: ((tabKey: string) => void) | undefined;
    onMoveTabBefore?: ((sourceTabKey: string, targetTabKey: string) => void) | undefined;
    onAppendTabToLeaf?: ((sourceTabKey: string, leafID: string) => void) | undefined;
    onSplitTab?:
      | ((
          sourceTabKey: string,
          leafID: string,
          direction: TabbedPanelDirection,
          placement: "before" | "after",
        ) => void)
      | undefined;
    onTabDoubleClick?: ((tabKey: string) => void) | undefined;
    onRatioChange?: ((splitID: string, ratio: number) => void) | undefined;
    onStartTabDrag?: ((event: DragEvent, tab: TabbedPanelDescriptor) => void) | undefined;
    onReadDraggedTab?: ((event: DragEvent) => string | null) | undefined;
    onClearDrag?: (() => void) | undefined;
  }

  const {
    dragScope,
    node,
    tabs,
    activeTabKey,
    renderTab,
    tabIcon = undefined,
    tabActions = undefined,
    scrollPanels = false,
    disabled = false,
    tablistLabel = "Panel group tabs",
    leafLabel = "Panel group",
    resizeLabel = "Resize panel split",
    dropTargetsLabel = "Panel group drop targets",
    onSelectTab,
    onMoveTabBefore,
    onAppendTabToLeaf,
    onSplitTab,
    onTabDoubleClick,
    onRatioChange,
    onStartTabDrag = undefined,
    onReadDraggedTab = undefined,
    onClearDrag = undefined,
  }: Props = $props();

  let splitEl = $state<HTMLDivElement | null>(null);
  let dropTargetsVisible = $state(false);
  let activeSplitEdge = $state<TabbedPanelSplitEdge | null>(null);
  let draggedTabKey = $state<string | null>(null);
  let draggedTabWidth = $state(112);
  let tabSortPreview = $state<{
    targetTabKey: string;
    placement: "before" | "after";
  } | null>(null);

  function tabForKey(tabKey: string): TabbedPanelDescriptor | null {
    return tabs.find((tab) => tab.key === tabKey) ?? null;
  }

  function startTabDrag(event: DragEvent, tab: TabbedPanelDescriptor): void {
    if (disabled) return;
    if (!tabDragEnabled()) return;
    if (onStartTabDrag) {
      onStartTabDrag(event, tab);
    } else {
      startTabbedPanelTabDrag(event, { scope: dragScope, tabKey: tab.key }, "Middleman panel tab");
    }
    draggedTabKey = tab.key;
    const sourceEl =
      event.currentTarget instanceof HTMLElement
        ? (event.currentTarget.closest(".tabbed-panel-tab") ?? event.currentTarget)
        : null;
    draggedTabWidth = sourceEl ? Math.round(sourceEl.getBoundingClientRect().width) : 112;
    setTabDragImage(event, tab, draggedTabWidth);
  }

  function readDraggedTab(event: DragEvent): string | null {
    if (disabled) return null;
    return onReadDraggedTab ? onReadDraggedTab(event) : readTabbedPanelTabDrag(event, dragScope);
  }

  function splitEdgeFromEvent(event: DragEvent): TabbedPanelSplitEdge | null {
    const target = event.currentTarget;
    if (!(target instanceof HTMLElement)) return null;
    const rect = target.getBoundingClientRect();
    return tabbedPanelSplitEdgeFromPoint(rect, event.clientX, event.clientY);
  }

  function tabSortPlacementFromEvent(event: DragEvent): "before" | "after" {
    const target = event.currentTarget;
    if (!(target instanceof HTMLElement)) return "before";
    const rect = target.getBoundingClientRect();
    return event.clientX < rect.left + rect.width / 2 ? "before" : "after";
  }

  function handleTabDragOver(event: DragEvent, targetTabKey: string): void {
    if (disabled) return;
    if (!canSortTabs()) return;
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
    adoptDraggedTab(sourceTabKey);
    event.preventDefault();
    event.stopPropagation();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    if (sourceTabKey === targetTabKey) {
      tabSortPreview = null;
      return;
    }
    tabSortPreview = {
      targetTabKey,
      placement: tabSortPlacementFromEvent(event),
    };
  }

  function handleTabStripDragOver(event: DragEvent): void {
    if (disabled) return;
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
    const target = event.target;
    const overTab = target instanceof HTMLElement && target.closest(".tabbed-panel-tab");
    if (overTab && !canSortTabs()) return;
    if (!overTab && !onAppendTabToLeaf) return;
    adoptDraggedTab(sourceTabKey);
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    if (overTab) {
      return;
    }
    const tablist = event.currentTarget;
    tabSortPreview =
      canSortTabs() && tablist instanceof HTMLElement
        ? sortPreviewFromPoint(tablist, sourceTabKey, event.clientX)
        : null;
  }

  function handleSplitDragOver(event: DragEvent): void {
    if (disabled) return;
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
    const edge = splitEdgeFromEvent(event);
    if ((edge === null && !onAppendTabToLeaf) || (edge !== null && !onSplitTab)) {
      return;
    }
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    dropTargetsVisible = true;
    activeSplitEdge = onSplitTab ? edge : null;
  }

  function hideDropTargets(): void {
    dropTargetsVisible = false;
    activeSplitEdge = null;
  }

  function clearTabSortPreview(): void {
    tabSortPreview = null;
  }

  function adoptDraggedTab(sourceTabKey: string): void {
    if (draggedTabKey !== sourceTabKey) {
      draggedTabKey = sourceTabKey;
    }
  }

  function clearExternalTabDragState(): void {
    if (draggedTabKey !== null && node.type === "leaf" && !node.tabs.includes(draggedTabKey)) {
      clearTabDragState();
      return;
    }
    clearTabSortPreview();
  }

  function clearTabDragState(): void {
    draggedTabKey = null;
    draggedTabWidth = 112;
    clearTabSortPreview();
  }

  function finishTabDrag(): void {
    hideDropTargets();
    clearTabDragState();
    if (onClearDrag) {
      onClearDrag();
    } else {
      clearActiveTabbedPanelDrag();
    }
  }

  function handleDragLeave(event: DragEvent): void {
    const current = event.currentTarget;
    const next = event.relatedTarget;
    if (current instanceof HTMLElement && next instanceof Node && current.contains(next)) {
      return;
    }
    hideDropTargets();
  }

  function handleTabStripDragLeave(event: DragEvent): void {
    const current = event.currentTarget;
    const next = event.relatedTarget;
    if (current instanceof HTMLElement && next instanceof Node && current.contains(next)) {
      return;
    }
    clearExternalTabDragState();
  }

  function sortPreviewFromPoint(
    tablist: HTMLElement,
    sourceTabKey: string,
    clientX: number,
  ): {
    targetTabKey: string;
    placement: "before" | "after";
  } | null {
    if (node.type !== "leaf") return null;
    let lastTargetKey: string | null = null;
    const tabEls = Array.from(tablist.querySelectorAll<HTMLElement>("[data-tabbed-panel-tab-key]"));
    for (const tabEl of tabEls) {
      const tabKey = tabEl.dataset.tabbedPanelTabKey;
      if (!tabKey || !node.tabs.includes(tabKey) || tabKey === sourceTabKey) {
        continue;
      }
      const rect = tabEl.getBoundingClientRect();
      if (clientX < rect.left + rect.width / 2) {
        return { targetTabKey: tabKey, placement: "before" };
      }
      lastTargetKey = tabKey;
    }
    return lastTargetKey ? { targetTabKey: lastTargetKey, placement: "after" } : null;
  }

  function moveTabToSortPlacement(
    sourceTabKey: string,
    targetTabKey: string,
    placement: "before" | "after",
  ): void {
    if (!canSortTabs()) return;
    if (sourceTabKey === targetTabKey) return;
    if (node.type !== "leaf") return;
    if (placement === "before") {
      onMoveTabBefore?.(sourceTabKey, targetTabKey);
      return;
    }
    const targetIndex = node.tabs.indexOf(targetTabKey);
    if (targetIndex < 0) return;
    const nextTabKey = node.tabs[targetIndex + 1];
    if (!nextTabKey) {
      onAppendTabToLeaf?.(sourceTabKey, node.id);
      return;
    }
    if (nextTabKey === sourceTabKey) return;
    onMoveTabBefore?.(sourceTabKey, nextTabKey);
  }

  function dropOnTab(event: DragEvent, targetTabKey: string): void {
    if (disabled) return;
    if (!canSortTabs()) return;
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null || sourceTabKey === targetTabKey) return;
    event.preventDefault();
    event.stopPropagation();
    const placement =
      tabSortPreview?.targetTabKey === targetTabKey
        ? tabSortPreview.placement
        : tabSortPlacementFromEvent(event);
    moveTabToSortPlacement(sourceTabKey, targetTabKey, placement);
    finishTabDrag();
  }

  function dropIntoLeaf(event: DragEvent, leafID: string): void {
    if (disabled) return;
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
    if (tabSortPreview && canSortTabs()) {
      event.preventDefault();
      moveTabToSortPlacement(sourceTabKey, tabSortPreview.targetTabKey, tabSortPreview.placement);
    } else {
      if (!onAppendTabToLeaf) return;
      event.preventDefault();
      onAppendTabToLeaf?.(sourceTabKey, leafID);
    }
    finishTabDrag();
  }

  function dropSplit(event: DragEvent, leafID: string): void {
    if (disabled) return;
    const sourceTabKey = readDraggedTab(event);
    const edge = splitEdgeFromEvent(event);
    if (sourceTabKey === null) return;
    if ((edge === null && !onAppendTabToLeaf) || (edge !== null && !onSplitTab)) {
      finishTabDrag();
      return;
    }
    event.preventDefault();
    if (edge === null) {
      onAppendTabToLeaf?.(sourceTabKey, leafID);
      finishTabDrag();
      return;
    }
    const { direction, placement } = tabbedPanelSplitPlacementForEdge(edge);
    onSplitTab?.(sourceTabKey, leafID, direction, placement);
    finishTabDrag();
  }

  function setTabDragImage(event: DragEvent, tab: TabbedPanelDescriptor, width: number): void {
    if (!event.dataTransfer) return;
    const ghost = document.createElement("div");
    ghost.textContent = tab.label;
    const ghostWidth = Math.max(90, Math.min(220, width));
    Object.assign(ghost.style, {
      position: "fixed",
      top: "-1000px",
      left: "-1000px",
      zIndex: "9999",
      width: `${ghostWidth}px`,
      height: "30px",
      display: "flex",
      alignItems: "center",
      padding: "0 10px",
      border:
        "var(--chrome-border-width) solid color-mix(in srgb, var(--accent-blue) 72%, transparent)",
      borderRadius: "4px",
      background: "var(--bg-surface)",
      color: "var(--text-primary)",
      boxShadow: "0 12px 32px rgb(0 0 0 / 38%)",
      fontFamily: "inherit",
      fontSize: "var(--font-size-sm)",
      fontWeight: "650",
      pointerEvents: "none",
    });
    document.body.appendChild(ghost);
    event.dataTransfer.setDragImage(ghost, ghostWidth / 2, 15);
    requestAnimationFrame(() => ghost.remove());
  }

  function showTabPlaceholder(targetTabKey: string, placement: "before" | "after"): boolean {
    return (
      draggedTabKey !== null &&
      draggedTabKey !== targetTabKey &&
      tabSortPreview?.targetTabKey === targetTabKey &&
      tabSortPreview.placement === placement
    );
  }

  function tabPlaceholderStyle(): string {
    const width = Math.max(72, Math.min(240, draggedTabWidth));
    return `--dragged-tab-width: ${width}px;`;
  }

  function statusClass(tab: TabbedPanelDescriptor): string {
    return tab.statusTone ?? tab.status ?? "default";
  }

  function startResize(event: PointerEvent): void {
    if (disabled) return;
    if (node.type !== "split" || !splitEl) return;
    event.preventDefault();
    const rect = splitEl.getBoundingClientRect();
    const direction = node.direction;
    const splitID = node.id;
    const pointerID = event.pointerId;
    (event.currentTarget as HTMLElement).setPointerCapture(pointerID);

    function onPointerMove(moveEvent: PointerEvent): void {
      const ratio =
        direction === "horizontal"
          ? (moveEvent.clientX - rect.left) / Math.max(1, rect.width)
          : (moveEvent.clientY - rect.top) / Math.max(1, rect.height);
      onRatioChange?.(splitID, clampTabbedPanelRatio(ratio));
    }

    function onPointerUp(upEvent: PointerEvent): void {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
      try {
        (event.currentTarget as HTMLElement).releasePointerCapture(upEvent.pointerId);
      } catch {
        // Pointer capture may already be gone after a browser-cancelled drag.
      }
    }

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp, { once: true });
  }

  function tabDragEnabled(): boolean {
    return !disabled && Boolean(onStartTabDrag || onMoveTabBefore || onAppendTabToLeaf || onSplitTab);
  }

  function canSortTabs(): boolean {
    return !disabled && Boolean(onMoveTabBefore);
  }
</script>

{#if node.type === "leaf"}
  <section class="tabbed-panel-leaf" aria-label={leafLabel}>
    <div
      class={["tabbed-panel-tabs", { "drag-sorting": draggedTabKey !== null }]}
      role="tablist"
      tabindex="-1"
      aria-label={tablistLabel}
      ondragover={handleTabStripDragOver}
      ondragleave={handleTabStripDragLeave}
      ondrop={(event) => dropIntoLeaf(event, node.id)}
    >
      {#each node.tabs as tabKey (tabKey)}
        {@const tab = tabForKey(tabKey)}
        {#if tab}
          {#if showTabPlaceholder(tab.key, "before")}
            <div
              class="tab-drop-placeholder tabbed-panel-tab-drop-placeholder before"
              style={tabPlaceholderStyle()}
              data-testid="tabbed-panel-tab-drop-placeholder"
              aria-hidden="true"
            ></div>
          {/if}
          <div
            class={[
              "tabbed-panel-tab",
              {
                active: node.activeTabKey === tab.key,
                dragging: draggedTabKey === tab.key,
                "sort-target":
                  tabSortPreview?.targetTabKey === tab.key && draggedTabKey !== tab.key,
              },
            ]}
            role="presentation"
            data-tabbed-panel-tab-key={tab.key}
            ondragover={(event) => handleTabDragOver(event, tab.key)}
            ondrop={(event) => dropOnTab(event, tab.key)}
          >
            <button
              class="tabbed-panel-tab-button"
              draggable={tabDragEnabled()}
              disabled={disabled}
              ondragstart={(event) => startTabDrag(event, tab)}
              ondragend={finishTabDrag}
              ondblclick={() => onTabDoubleClick?.(tab.key)}
              aria-selected={activeTabKey === tab.key}
              role="tab"
              onclick={() => onSelectTab?.(tab.key)}
            >
              {#if tabIcon}
                <span class="tabbed-panel-tab-icon" aria-hidden="true">
                  {@render tabIcon(tab)}
                </span>
              {/if}
              <span class="tabbed-panel-tab-label">{tab.label}</span>
              {#if tab.status}
                <span
                  class={["tabbed-panel-status-dot", statusClass(tab)]}
                  title={tab.status}
                ></span>
              {/if}
            </button>
            {#if tabActions}
              {@render tabActions(tab)}
            {/if}
          </div>
          {#if showTabPlaceholder(tab.key, "after")}
            <div
              class="tab-drop-placeholder tabbed-panel-tab-drop-placeholder after"
              style={tabPlaceholderStyle()}
              data-testid="tabbed-panel-tab-drop-placeholder"
              aria-hidden="true"
            ></div>
          {/if}
        {/if}
      {/each}
    </div>
    <div
      class={["tabbed-panel-body", { "show-drop-targets": dropTargetsVisible }]}
      role="group"
      aria-label={dropTargetsLabel}
      ondragover={handleSplitDragOver}
      ondragleave={handleDragLeave}
      ondrop={(event) => dropSplit(event, node.id)}
    >
      {#each node.tabs as tabKey (tabKey)}
        <div
          class={[
            "tabbed-panel-tab-panel",
            {
              active: node.activeTabKey === tabKey,
              scrollable: scrollPanels,
            },
          ]}
        >
          {@render renderTab(tabKey, node.activeTabKey === tabKey)}
        </div>
      {/each}
      <div
        class={[
          "tabbed-panel-split-preview",
          activeSplitEdge,
          { active: dropTargetsVisible && activeSplitEdge !== null },
        ]}
        aria-hidden="true"
      ></div>
    </div>
  </section>
{:else}
  <div
    bind:this={splitEl}
    class={["tabbed-panel-split", node.direction]}
    style={`--first-ratio: ${node.ratio}; --second-ratio: ${1 - node.ratio};`}
  >
    <div class="tabbed-panel-split-child first">
      <Self
        {dragScope}
        node={node.first}
        {tabs}
        {activeTabKey}
        {renderTab}
        {tabIcon}
        {tabActions}
        {scrollPanels}
        {disabled}
        {tablistLabel}
        {leafLabel}
        {resizeLabel}
        {dropTargetsLabel}
        {onSelectTab}
        {onMoveTabBefore}
        {onAppendTabToLeaf}
        {onSplitTab}
        {onTabDoubleClick}
        {onRatioChange}
        {onStartTabDrag}
        {onReadDraggedTab}
        {onClearDrag}
      />
    </div>
    <button
      class="tabbed-panel-split-divider"
      aria-label={resizeLabel}
      disabled={disabled}
      onpointerdown={startResize}
    ></button>
    <div class="tabbed-panel-split-child second">
      <Self
        {dragScope}
        node={node.second}
        {tabs}
        {activeTabKey}
        {renderTab}
        {tabIcon}
        {tabActions}
        {scrollPanels}
        {disabled}
        {tablistLabel}
        {leafLabel}
        {resizeLabel}
        {dropTargetsLabel}
        {onSelectTab}
        {onMoveTabBefore}
        {onAppendTabToLeaf}
        {onSplitTab}
        {onTabDoubleClick}
        {onRatioChange}
        {onStartTabDrag}
        {onReadDraggedTab}
        {onClearDrag}
      />
    </div>
  </div>
{/if}

<style>
  .tabbed-panel-split,
  .tabbed-panel-leaf {
    min-width: 0;
    min-height: 0;
    height: 100%;
  }

  .tabbed-panel-split {
    display: flex;
    overflow: hidden;
  }

  .tabbed-panel-split.horizontal {
    flex-direction: row;
  }

  .tabbed-panel-split.vertical {
    flex-direction: column;
  }

  .tabbed-panel-split-child {
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .tabbed-panel-split-child.first {
    flex: var(--first-ratio) 1 0;
  }

  .tabbed-panel-split-child.second {
    flex: var(--second-ratio) 1 0;
  }

  .tabbed-panel-split-divider {
    flex: 0 0 var(--chrome-pane-divider-width);
    appearance: none;
    border: 0;
    padding: 0;
    background: var(--border-muted);
    cursor: col-resize;
    flex-shrink: 0;
  }

  .tabbed-panel-split.vertical > .tabbed-panel-split-divider {
    cursor: row-resize;
  }

  .tabbed-panel-split-divider:hover,
  .tabbed-panel-split-divider:focus-visible {
    background: var(--accent-blue);
    outline: none;
  }

  .tabbed-panel-split-divider:disabled {
    cursor: default;
    opacity: 0.62;
  }

  .tabbed-panel-split-divider:disabled:hover,
  .tabbed-panel-split-divider:disabled:focus-visible {
    background: var(--border-muted);
  }

  :global(.tabbed-panel-split.horizontal
      > .tabbed-panel-split-child.first
      .tabbed-panel-leaf) {
    border-right: 0;
  }

  :global(.tabbed-panel-split.horizontal
      > .tabbed-panel-split-child.second
      .tabbed-panel-leaf) {
    border-left: 0;
  }

  :global(.tabbed-panel-split.vertical
      > .tabbed-panel-split-child.first
      .tabbed-panel-leaf) {
    border-bottom: 0;
  }

  :global(.tabbed-panel-split.vertical
      > .tabbed-panel-split-child.second
      .tabbed-panel-leaf) {
    border-top: 0;
  }

  .tabbed-panel-leaf {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: var(--chrome-border-width) solid var(--border-default);
    border-top: 0;
    background: var(--bg-surface);
  }

  .tabbed-panel-tabs {
    position: relative;
    display: flex;
    align-items: stretch;
    min-height: 30px;
    background: var(--bg-inset);
    overflow-x: auto;
    scrollbar-width: none;
  }

  .tabbed-panel-tabs::after {
    content: "";
    position: absolute;
    inset: auto 0 0 0;
    z-index: 1;
    height: var(--chrome-border-width);
    background: var(--border-muted);
    pointer-events: none;
  }

  .tabbed-panel-tabs.drag-sorting {
    cursor: grabbing;
  }

  .tabbed-panel-tabs::-webkit-scrollbar {
    width: 0;
    height: 0;
  }

  .tabbed-panel-tab {
    position: relative;
    z-index: 0;
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    min-width: 0;
    max-width: 220px;
    border-right: var(--chrome-border-width) solid var(--border-muted);
    color: var(--text-muted);
    transition:
      transform 150ms cubic-bezier(0.16, 1, 0.3, 1),
      opacity 120ms ease,
      background-color 120ms ease,
    color 120ms ease;
  }

  .tabbed-panel-tab.active {
    z-index: 2;
    background: var(--bg-surface);
    color: var(--text-primary);
    margin-bottom: calc(-1 * var(--chrome-border-width));
  }

  .tabbed-panel-tab.active::before {
    content: "";
    position: absolute;
    inset: 0 0 auto 0;
    height: var(--chrome-active-accent-width);
    background: var(--accent-blue);
    pointer-events: none;
  }

  .tabbed-panel-tab.dragging {
    opacity: 0.34;
    transform: translateY(-4px) scale(0.96);
    background: color-mix(in srgb, var(--accent-blue) 10%, var(--bg-surface));
    box-shadow: 0 8px 22px rgb(0 0 0 / 18%);
  }

  .tabbed-panel-tab.sort-target:not(.dragging) {
    color: var(--text-primary);
    background: color-mix(in srgb, var(--accent-blue) 9%, transparent);
  }

  .tabbed-panel-tab-button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    height: 100%;
    padding: 0 8px;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: grab;
  }

  .tabbed-panel-tab-button:disabled {
    cursor: default;
    color: inherit;
  }

  .tabbed-panel-tabs.drag-sorting .tabbed-panel-tab-button {
    cursor: grabbing;
  }

  .tabbed-panel-tab-button:hover:not(:disabled),
  .tabbed-panel-tab-button:focus-visible:not(:disabled) {
    color: var(--text-primary);
    outline: none;
  }

  .tabbed-panel-tab-icon {
    display: inline-flex;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .tabbed-panel-tab.active .tabbed-panel-tab-icon {
    color: var(--accent-blue);
  }

  .tabbed-panel-tab-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tabbed-panel-status-dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--text-muted);
    flex-shrink: 0;
  }

  .tabbed-panel-status-dot.running {
    background: var(--accent-green);
  }

  .tabbed-panel-status-dot.starting {
    background: var(--accent-amber);
  }

  .tabbed-panel-status-dot.success {
    background: var(--accent-green);
  }

  .tabbed-panel-status-dot.warning {
    background: var(--accent-amber);
  }

  .tabbed-panel-status-dot.danger {
    background: var(--accent-red);
  }

  .tabbed-panel-tab :global(.tabbed-panel-tab-tool) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 22px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    opacity: 0;
  }

  .tabbed-panel-tab:hover :global(.tabbed-panel-tab-tool),
  .tabbed-panel-tab :global(.tabbed-panel-tab-tool:focus-visible) {
    opacity: 1;
  }

  .tabbed-panel-tab :global(.tabbed-panel-tab-tool:hover),
  .tabbed-panel-tab :global(.tabbed-panel-tab-tool:focus-visible) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    outline: none;
  }

  .tab-drop-placeholder {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 var(--dragged-tab-width, 112px);
    width: var(--dragged-tab-width, 112px);
    min-width: 72px;
    height: 30px;
    border-right: var(--chrome-border-width) solid var(--border-muted);
    background: color-mix(in srgb, var(--accent-blue) 7%, transparent);
    animation: tab-placeholder-in 140ms cubic-bezier(0.16, 1, 0.3, 1);
    pointer-events: none;
  }

  .tab-drop-placeholder::before {
    content: "";
    position: absolute;
    inset: 4px 5px;
    border: var(--chrome-border-width) dashed
      color-mix(in srgb, var(--accent-blue) 62%, transparent);
    border-radius: 4px;
    background: color-mix(in srgb, var(--accent-blue) 13%, transparent);
    box-shadow: inset 0 0 0 var(--chrome-border-width)
      color-mix(in srgb, var(--accent-blue) 10%, transparent);
  }

  .tab-drop-placeholder::after {
    content: "";
    position: absolute;
    top: 4px;
    bottom: 4px;
    width: var(--chrome-active-accent-width);
    border-radius: 999px;
    background: var(--accent-blue);
    box-shadow: 0 0 0 var(--chrome-border-width)
      color-mix(in srgb, var(--accent-blue) 24%, transparent);
  }

  .tab-drop-placeholder.before::after {
    left: var(--chrome-pane-divider-width);
  }

  .tab-drop-placeholder.after::after {
    right: var(--chrome-pane-divider-width);
  }

  .tabbed-panel-body {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .tabbed-panel-tab-panel {
    position: absolute;
    inset: 0;
    visibility: hidden;
    overflow: hidden;
  }

  .tabbed-panel-tab-panel.scrollable {
    overflow-x: hidden;
    overflow-y: auto;
    scrollbar-gutter: stable;
  }

  .tabbed-panel-tab-panel.active {
    visibility: visible;
  }

  .tabbed-panel-split-preview {
    position: absolute;
    z-index: 4;
    inset: 0;
    border: var(--chrome-border-width) solid
      color-mix(in srgb, var(--accent-blue) 44%, transparent);
    opacity: 0;
    pointer-events: none;
    background: color-mix(in srgb, var(--accent-blue) 14%, transparent);
    -webkit-backdrop-filter: blur(3px) saturate(1.05);
    backdrop-filter: blur(3px) saturate(1.05);
    box-shadow: inset 0 0 0 var(--chrome-border-width)
      color-mix(in srgb, var(--accent-blue) 18%, transparent);
    transition:
      opacity 90ms ease,
      inset 90ms ease;
  }

  .tabbed-panel-body.show-drop-targets .tabbed-panel-split-preview.active {
    opacity: 1;
  }

  .tabbed-panel-split-preview.top {
    top: 0;
    right: 0;
    bottom: 50%;
    left: 0;
    border-width: 0 0 var(--chrome-active-accent-width);
    border-bottom-color: var(--accent-blue);
  }

  .tabbed-panel-split-preview.right {
    top: 0;
    right: 0;
    bottom: 0;
    left: 50%;
    border-width: 0 0 0 var(--chrome-active-accent-width);
    border-left-color: var(--accent-blue);
  }

  .tabbed-panel-split-preview.bottom {
    top: 50%;
    right: 0;
    bottom: 0;
    left: 0;
    border-width: var(--chrome-active-accent-width) 0 0;
    border-top-color: var(--accent-blue);
  }

  .tabbed-panel-split-preview.left {
    top: 0;
    right: 50%;
    bottom: 0;
    left: 0;
    border-width: 0 var(--chrome-active-accent-width) 0 0;
    border-right-color: var(--accent-blue);
  }

  @keyframes tab-placeholder-in {
    from {
      flex-basis: 0;
      width: 0;
      opacity: 0;
      transform: scaleX(0.82);
    }
    to {
      flex-basis: var(--dragged-tab-width, 112px);
      width: var(--dragged-tab-width, 112px);
      opacity: 1;
      transform: scaleX(1);
    }
  }
</style>
