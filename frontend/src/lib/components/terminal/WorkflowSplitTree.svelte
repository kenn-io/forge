<script lang="ts">
  import type { Snippet } from "svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import MoveIcon from "@lucide/svelte/icons/move";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import HouseIcon from "@lucide/svelte/icons/house";
  import Self from "./WorkflowSplitTree.svelte";
  import type {
    SplitEdge,
    SplitDirection,
    WorkflowNode,
    WorkflowTabKey,
  } from "./terminal-layout";
  import {
    clampRatio,
    splitEdgeFromPoint,
    splitPlacementForEdge,
  } from "./terminal-layout";
  import {
    clearActiveTerminalDrag,
    readWorkflowTabDrag,
    startWorkflowTabDrag,
  } from "./terminal-drag";

  export interface WorkflowTabDescriptor {
    key: WorkflowTabKey;
    label: string;
    kind: "home" | "shell" | "terminal" | "agent" | "plain_shell";
    status?: string | undefined;
    renamable?: boolean | undefined;
    movableToTerminal?: boolean | undefined;
    closable?: boolean | undefined;
  }

  interface Props {
    workspaceId: string;
    node: WorkflowNode;
    tabs: WorkflowTabDescriptor[];
    activeTabKey: WorkflowTabKey;
    renderTab: Snippet<[WorkflowTabKey, boolean]>;
    onSelectTab?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onMoveTabBefore?:
      | ((sourceTabKey: WorkflowTabKey, targetTabKey: WorkflowTabKey) => void)
      | undefined;
    onAppendTabToLeaf?:
      | ((sourceTabKey: WorkflowTabKey, leafID: string) => void)
      | undefined;
    onSplitTab?:
      | ((
          sourceTabKey: WorkflowTabKey,
          leafID: string,
          direction: SplitDirection,
          placement: "before" | "after",
        ) => void)
      | undefined;
    onMoveTabToTerminal?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onCloseTab?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onRenameTab?: ((tabKey: WorkflowTabKey) => void) | undefined;
    onRatioChange?: ((splitID: string, ratio: number) => void) | undefined;
  }

  const {
    workspaceId,
    node,
    tabs,
    activeTabKey,
    renderTab,
    onSelectTab,
    onMoveTabBefore,
    onAppendTabToLeaf,
    onSplitTab,
    onMoveTabToTerminal,
    onCloseTab,
    onRenameTab,
    onRatioChange,
  }: Props = $props();

  let splitEl = $state<HTMLDivElement | null>(null);
  let dropTargetsVisible = $state(false);
  let activeSplitEdge = $state<SplitEdge | null>(null);
  let draggedTabKey = $state<WorkflowTabKey | null>(null);
  let draggedTabWidth = $state(112);
  let tabSortPreview = $state<{
    targetTabKey: WorkflowTabKey;
    placement: "before" | "after";
  } | null>(null);

  function tabForKey(tabKey: WorkflowTabKey): WorkflowTabDescriptor | null {
    return tabs.find((tab) => tab.key === tabKey) ?? null;
  }

  function startTabDrag(
    event: DragEvent,
    tab: WorkflowTabDescriptor,
  ): void {
    startWorkflowTabDrag(event, { workspaceId, tabKey: tab.key });
    draggedTabKey = tab.key;
    const sourceEl =
      event.currentTarget instanceof HTMLElement
        ? (event.currentTarget.closest(".group-tab") ?? event.currentTarget)
        : null;
    draggedTabWidth = sourceEl
      ? Math.round(sourceEl.getBoundingClientRect().width)
      : 112;
    setTabDragImage(event, tab, draggedTabWidth);
  }

  function readDraggedTab(event: DragEvent): WorkflowTabKey | null {
    return readWorkflowTabDrag(event, workspaceId);
  }

  function splitEdgeFromEvent(event: DragEvent): SplitEdge | null {
    const target = event.currentTarget;
    if (!(target instanceof HTMLElement)) return null;
    const rect = target.getBoundingClientRect();
    return splitEdgeFromPoint(rect, event.clientX, event.clientY);
  }

  function handleDragOver(event: DragEvent): void {
    if (readDraggedTab(event) === null) return;
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
  }

  function tabSortPlacementFromEvent(
    event: DragEvent,
  ): "before" | "after" {
    const target = event.currentTarget;
    if (!(target instanceof HTMLElement)) return "before";
    const rect = target.getBoundingClientRect();
    return event.clientX < rect.left + rect.width / 2 ? "before" : "after";
  }

  function handleTabDragOver(
    event: DragEvent,
    targetTabKey: WorkflowTabKey,
  ): void {
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
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
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    const target = event.target;
    if (
      target instanceof HTMLElement &&
      target.closest(".group-tab")
    ) {
      return;
    }
    const tablist = event.currentTarget;
    tabSortPreview =
      tablist instanceof HTMLElement
        ? sortPreviewFromPoint(tablist, sourceTabKey, event.clientX)
        : null;
  }

  function handleSplitDragOver(event: DragEvent): void {
    handleDragOver(event);
    if (event.defaultPrevented) {
      dropTargetsVisible = true;
      activeSplitEdge = splitEdgeFromEvent(event);
    }
  }

  function hideDropTargets(): void {
    dropTargetsVisible = false;
    activeSplitEdge = null;
  }

  function clearTabSortPreview(): void {
    tabSortPreview = null;
  }

  function clearTabDragState(): void {
    draggedTabKey = null;
    draggedTabWidth = 112;
    clearTabSortPreview();
  }

  function finishTabDrag(): void {
    hideDropTargets();
    clearTabDragState();
    clearActiveTerminalDrag();
  }

  function handleDragLeave(event: DragEvent): void {
    const current = event.currentTarget;
    const next = event.relatedTarget;
    if (
      current instanceof HTMLElement &&
      next instanceof Node &&
      current.contains(next)
    ) {
      return;
    }
    hideDropTargets();
  }

  function handleTabStripDragLeave(event: DragEvent): void {
    const current = event.currentTarget;
    const next = event.relatedTarget;
    if (
      current instanceof HTMLElement &&
      next instanceof Node &&
      current.contains(next)
    ) {
      return;
    }
    clearTabSortPreview();
  }

  function sortPreviewFromPoint(
    tablist: HTMLElement,
    sourceTabKey: WorkflowTabKey,
    clientX: number,
  ):
    | {
        targetTabKey: WorkflowTabKey;
        placement: "before" | "after";
      }
    | null {
    if (node.type !== "leaf") return null;
    let lastTargetKey: WorkflowTabKey | null = null;
    const tabEls = Array.from(
      tablist.querySelectorAll<HTMLElement>("[data-workflow-tab-key]"),
    );
    for (const tabEl of tabEls) {
      const tabKey = tabEl.dataset.workflowTabKey as WorkflowTabKey | undefined;
      if (!tabKey || !node.tabs.includes(tabKey) || tabKey === sourceTabKey) {
        continue;
      }
      const rect = tabEl.getBoundingClientRect();
      if (clientX < rect.left + rect.width / 2) {
        return { targetTabKey: tabKey, placement: "before" };
      }
      lastTargetKey = tabKey;
    }
    return lastTargetKey
      ? { targetTabKey: lastTargetKey, placement: "after" }
      : null;
  }

  function moveTabToSortPlacement(
    sourceTabKey: WorkflowTabKey,
    targetTabKey: WorkflowTabKey,
    placement: "before" | "after",
  ): void {
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

  function dropOnTab(event: DragEvent, targetTabKey: WorkflowTabKey): void {
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
    const sourceTabKey = readDraggedTab(event);
    if (sourceTabKey === null) return;
    event.preventDefault();
    if (tabSortPreview) {
      moveTabToSortPlacement(
        sourceTabKey,
        tabSortPreview.targetTabKey,
        tabSortPreview.placement,
      );
    } else {
      onAppendTabToLeaf?.(sourceTabKey, leafID);
    }
    finishTabDrag();
  }

  function dropSplit(event: DragEvent, leafID: string): void {
    const sourceTabKey = readDraggedTab(event);
    const edge = splitEdgeFromEvent(event);
    if (sourceTabKey === null) return;
    event.preventDefault();
    if (edge === null) {
      onAppendTabToLeaf?.(sourceTabKey, leafID);
      finishTabDrag();
      return;
    }
    const { direction, placement } = splitPlacementForEdge(edge);
    onSplitTab?.(sourceTabKey, leafID, direction, placement);
    finishTabDrag();
  }

  function setTabDragImage(
    event: DragEvent,
    tab: WorkflowTabDescriptor,
    width: number,
  ): void {
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
      border: "1px solid color-mix(in srgb, var(--accent-blue) 72%, transparent)",
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

  function showTabPlaceholder(
    targetTabKey: WorkflowTabKey,
    placement: "before" | "after",
  ): boolean {
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

  function statusClass(status: string | undefined): string {
    if (status === "running") return "running";
    if (status === "starting") return "starting";
    return "exited";
  }

  function startResize(event: PointerEvent): void {
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
      onRatioChange?.(splitID, clampRatio(ratio));
    }

    function onPointerUp(upEvent: PointerEvent): void {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
      try {
        (event.currentTarget as HTMLElement).releasePointerCapture(
          upEvent.pointerId,
        );
      } catch {
        // Pointer capture may already be gone after a browser-cancelled drag.
      }
    }

    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp, { once: true });
  }
</script>

{#if node.type === "leaf"}
  <section class="workflow-leaf" aria-label="Workflow group">
    <div
      class={["group-tabs", { "drag-sorting": draggedTabKey !== null }]}
      role="tablist"
      tabindex="-1"
      aria-label="Workflow group tabs"
      ondragover={handleTabStripDragOver}
      ondragleave={handleTabStripDragLeave}
      ondrop={(event) => dropIntoLeaf(event, node.id)}
    >
      {#each node.tabs as tabKey (tabKey)}
        {@const tab = tabForKey(tabKey)}
        {#if tab}
          {#if showTabPlaceholder(tab.key, "before")}
            <div
              class="tab-drop-placeholder before"
              style={tabPlaceholderStyle()}
              data-testid="workflow-tab-drop-placeholder"
              aria-hidden="true"
            ></div>
          {/if}
          <div
            class={[
              "group-tab",
              {
                active: node.activeTabKey === tab.key,
                dragging: draggedTabKey === tab.key,
                "sort-target":
                  tabSortPreview?.targetTabKey === tab.key &&
                  draggedTabKey !== tab.key,
              },
            ]}
            role="presentation"
            data-workflow-tab-key={tab.key}
            ondragover={(event) => handleTabDragOver(event, tab.key)}
            ondrop={(event) => dropOnTab(event, tab.key)}
          >
            <button
              class="group-tab-button"
              draggable="true"
              ondragstart={(event) => startTabDrag(event, tab)}
              ondragend={finishTabDrag}
              ondblclick={() => onRenameTab?.(tab.key)}
              aria-selected={activeTabKey === tab.key}
              role="tab"
              onclick={() => onSelectTab?.(tab.key)}
            >
              <span class="tab-icon" aria-hidden="true">
                {#if tab.kind === "home"}
                  <HouseIcon size="13" strokeWidth="2" />
                {:else if tab.kind === "plain_shell" || tab.kind === "terminal" || tab.kind === "shell"}
                  <TerminalIcon size="13" strokeWidth="2" />
                {:else}
                  <SparklesIcon size="13" strokeWidth="2" />
                {/if}
              </span>
              <span class="tab-label">{tab.label}</span>
              {#if tab.status}
                <span
                  class={["status-dot", statusClass(tab.status)]}
                  title={tab.status}
                ></span>
              {/if}
            </button>
            {#if tab.renamable}
              <button
                class="tab-tool"
                title="Rename"
                aria-label={`Rename ${tab.label}`}
                onclick={() => onRenameTab?.(tab.key)}
              >
                <PencilIcon size="11" strokeWidth="2.2" aria-hidden="true" />
              </button>
            {/if}
            {#if tab.movableToTerminal}
              <button
                class="tab-tool"
                title="Move to terminal"
                aria-label={`Move ${tab.label} to terminal`}
                onclick={() => onMoveTabToTerminal?.(tab.key)}
              >
                <MoveIcon size="11" strokeWidth="2.2" aria-hidden="true" />
              </button>
            {/if}
            {#if tab.closable}
              <button
                class="tab-tool"
                title="Close"
                aria-label={`Close ${tab.label}`}
                onclick={() => onCloseTab?.(tab.key)}
              >
                <XIcon size="11" strokeWidth="2.3" aria-hidden="true" />
              </button>
            {/if}
          </div>
          {#if showTabPlaceholder(tab.key, "after")}
            <div
              class="tab-drop-placeholder after"
              style={tabPlaceholderStyle()}
              data-testid="workflow-tab-drop-placeholder"
              aria-hidden="true"
            ></div>
          {/if}
        {/if}
      {/each}
    </div>
    <div
      class={["group-body", { "show-drop-targets": dropTargetsVisible }]}
      role="group"
      aria-label="Workflow group drop targets"
      ondragover={handleSplitDragOver}
      ondragleave={handleDragLeave}
      ondrop={(event) => dropSplit(event, node.id)}
    >
      {#each node.tabs as tabKey (tabKey)}
        <div
          class={[
            "group-tab-panel",
            { active: node.activeTabKey === tabKey },
          ]}
        >
          {@render renderTab(tabKey, node.activeTabKey === tabKey)}
        </div>
      {/each}
      <div
        class={[
          "split-preview",
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
    class={["workflow-split", node.direction]}
    style={`--first-ratio: ${node.ratio}; --second-ratio: ${1 - node.ratio};`}
  >
    <div class="split-child first">
      <Self
        {workspaceId}
        node={node.first}
        {tabs}
        {activeTabKey}
        {renderTab}
        {onSelectTab}
        {onMoveTabBefore}
        {onAppendTabToLeaf}
        {onSplitTab}
        {onMoveTabToTerminal}
        {onCloseTab}
        {onRenameTab}
        {onRatioChange}
      />
    </div>
    <button
      class="split-divider"
      aria-label="Resize workflow split"
      onpointerdown={startResize}
    ></button>
    <div class="split-child second">
      <Self
        {workspaceId}
        node={node.second}
        {tabs}
        {activeTabKey}
        {renderTab}
        {onSelectTab}
        {onMoveTabBefore}
        {onAppendTabToLeaf}
        {onSplitTab}
        {onMoveTabToTerminal}
        {onCloseTab}
        {onRenameTab}
        {onRatioChange}
      />
    </div>
  </div>
{/if}

<style>
  .workflow-split,
  .workflow-leaf {
    min-width: 0;
    min-height: 0;
    height: 100%;
  }

  .workflow-split {
    display: flex;
    overflow: hidden;
  }

  .workflow-split.horizontal {
    flex-direction: row;
  }

  .workflow-split.vertical {
    flex-direction: column;
  }

  .split-child {
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .split-child.first {
    flex: var(--first-ratio) 1 0;
  }

  .split-child.second {
    flex: var(--second-ratio) 1 0;
  }

  .split-divider {
    flex: 0 0 5px;
    border: 0;
    background: var(--bg-primary);
    cursor: col-resize;
    position: relative;
  }

  .workflow-split.vertical > .split-divider {
    cursor: row-resize;
  }

  .split-divider::before {
    content: "";
    position: absolute;
    inset: 0 2px;
    background: var(--border-default);
  }

  .workflow-split.vertical > .split-divider::before {
    inset: 2px 0;
  }

  .split-divider:hover::before,
  .split-divider:focus-visible::before {
    background: var(--accent-blue);
  }

  .split-divider:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
  }

  .workflow-leaf {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .group-tabs {
    display: flex;
    align-items: stretch;
    min-height: 30px;
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-inset);
    overflow-x: auto;
    scrollbar-width: none;
  }

  .group-tabs.drag-sorting {
    cursor: grabbing;
  }

  .group-tabs::-webkit-scrollbar {
    width: 0;
    height: 0;
  }

  .group-tab {
    position: relative;
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    min-width: 0;
    max-width: 220px;
    border-right: 1px solid var(--border-muted);
    color: var(--text-muted);
    transition:
      transform 150ms cubic-bezier(0.16, 1, 0.3, 1),
      opacity 120ms ease,
      background-color 120ms ease,
      color 120ms ease;
  }

  .group-tab.active {
    background: var(--bg-surface);
    color: var(--text-primary);
    margin-bottom: -1px;
    border-bottom: 1px solid var(--bg-surface);
  }

  .group-tab.active::before {
    content: "";
    position: absolute;
    inset: 0 0 auto 0;
    height: 2px;
    background: var(--accent-blue);
    pointer-events: none;
  }

  .group-tab.dragging {
    opacity: 0.34;
    transform: translateY(-4px) scale(0.96);
    background: color-mix(in srgb, var(--accent-blue) 10%, var(--bg-surface));
    box-shadow: 0 8px 22px rgb(0 0 0 / 18%);
  }

  .group-tab.sort-target:not(.dragging) {
    color: var(--text-primary);
    background: color-mix(in srgb, var(--accent-blue) 9%, transparent);
  }

  .group-tab-button {
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

  .group-tabs.drag-sorting .group-tab-button {
    cursor: grabbing;
  }

  .group-tab-button:hover,
  .group-tab-button:focus-visible {
    color: var(--text-primary);
    outline: none;
  }

  .tab-icon {
    display: inline-flex;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .group-tab.active .tab-icon {
    color: var(--accent-blue);
  }

  .tab-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .status-dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--text-muted);
    flex-shrink: 0;
  }

  .status-dot.running {
    background: var(--accent-green);
  }

  .status-dot.starting {
    background: var(--accent-amber);
  }

  .tab-tool {
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

  .group-tab:hover .tab-tool,
  .tab-tool:focus-visible {
    opacity: 1;
  }

  .tab-tool:hover,
  .tab-tool:focus-visible {
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
    border-right: 1px solid var(--border-muted);
    background: color-mix(in srgb, var(--accent-blue) 7%, transparent);
    animation: tab-placeholder-in 140ms cubic-bezier(0.16, 1, 0.3, 1);
    pointer-events: none;
  }

  .tab-drop-placeholder::before {
    content: "";
    position: absolute;
    inset: 4px 5px;
    border: 1px dashed color-mix(in srgb, var(--accent-blue) 62%, transparent);
    border-radius: 4px;
    background: color-mix(in srgb, var(--accent-blue) 13%, transparent);
    box-shadow: inset 0 0 0 1px
      color-mix(in srgb, var(--accent-blue) 10%, transparent);
  }

  .tab-drop-placeholder::after {
    content: "";
    position: absolute;
    top: 4px;
    bottom: 4px;
    width: 2px;
    border-radius: 999px;
    background: var(--accent-blue);
    box-shadow: 0 0 0 1px
      color-mix(in srgb, var(--accent-blue) 24%, transparent);
  }

  .tab-drop-placeholder.before::after {
    left: 3px;
  }

  .tab-drop-placeholder.after::after {
    right: 3px;
  }

  .group-body {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .group-tab-panel {
    position: absolute;
    inset: 0;
    visibility: hidden;
  }

  .group-tab-panel.active {
    visibility: visible;
  }

  .split-preview {
    position: absolute;
    z-index: 4;
    inset: 0;
    border: 1px solid color-mix(in srgb, var(--accent-blue) 44%, transparent);
    opacity: 0;
    pointer-events: none;
    background: color-mix(in srgb, var(--accent-blue) 14%, transparent);
    -webkit-backdrop-filter: blur(3px) saturate(1.05);
    backdrop-filter: blur(3px) saturate(1.05);
    box-shadow: inset 0 0 0 1px
      color-mix(in srgb, var(--accent-blue) 18%, transparent);
    transition:
      opacity 90ms ease,
      inset 90ms ease;
  }

  .group-body.show-drop-targets .split-preview.active {
    opacity: 1;
  }

  .split-preview.top {
    top: 0;
    right: 0;
    bottom: 50%;
    left: 0;
    border-width: 0 0 2px;
    border-bottom-color: var(--accent-blue);
  }

  .split-preview.right {
    top: 0;
    right: 0;
    bottom: 0;
    left: 50%;
    border-width: 0 0 0 2px;
    border-left-color: var(--accent-blue);
  }

  .split-preview.bottom {
    top: 50%;
    right: 0;
    bottom: 0;
    left: 0;
    border-width: 2px 0 0;
    border-top-color: var(--accent-blue);
  }

  .split-preview.left {
    top: 0;
    right: 50%;
    bottom: 0;
    left: 0;
    border-width: 0 2px 0 0;
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
