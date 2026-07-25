<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import { Button } from "@kenn-io/kit-ui";
  import ChevronsUpIcon from "@lucide/svelte/icons/chevrons-up";
  import XIcon from "@lucide/svelte/icons/x";
  import PaneLeafActions from "./PaneLeafActions.svelte";
  import TabbedPanelTree from "./TabbedPanelTree.svelte";
  import {
    flattenTabbedPanelTree,
    type TabbedPanelDescriptor,
    type TabbedPanelLeaf,
  } from "./tabbed-panel-layout.js";
  import type { PaneLayoutStore, PaneTabSpec } from "../../stores/paneLayout.svelte.js";

  interface Props {
    layout: PaneLayoutStore;
    tabs: PaneTabSpec[];
    renderPane: Snippet<[string, boolean]>;
    paneIcon?: Snippet<[TabbedPanelDescriptor]> | undefined;
    tablistLabel?: string;
    leafLabel?: string;
    /** Below this container width the tree flattens to a single strip. */
    flattenBelowPx?: number;
    /** Route-bound tab, when the surface has one. */
    routeTabKey?: string | undefined;
    /** A tab was clicked. Surfaces route this through navigate(). */
    onSelectTab?: ((tabKey: string) => void) | undefined;
    /**
     * Focus moved into a different pane's body. Surfaces with a route-bound tab
     * use this to follow the user between panes that are visible at once, where
     * clicking a tab is not what moved them.
     */
    onFocusPane?: ((tabKey: string) => void) | undefined;
  }

  const {
    layout,
    tabs,
    renderPane,
    paneIcon = undefined,
    tablistLabel = "Detail panes",
    leafLabel = "Detail pane group",
    flattenBelowPx = 1280,
    routeTabKey = undefined,
    onSelectTab = undefined,
    onFocusPane = undefined,
  }: Props = $props();

  let host = $state<HTMLElement | null>(null);
  let hostWidth = $state(0);

  const availableTabs = $derived(tabs.filter((tab) => tab.available).map((tab) => tab.key));
  const hideableTabKeys = $derived(tabs.filter((tab) => tab.hideable === true).map((tab) => tab.key));
  const descriptors = $derived<TabbedPanelDescriptor[]>(
    tabs.filter((tab) => tab.available).map((tab) => ({ key: tab.key, label: tab.label })),
  );
  const renderTree = $derived(layout.renderTree(availableTabs));
  const zoomedLeafID = $derived(layout.effectiveZoomedLeafID(availableTabs));

  // Measured rather than media-queried: these surfaces are embedded at several
  // widths (focus presentation, activity drawer) inside the same viewport.
  const flattened = $derived(hostWidth > 0 && hostWidth < flattenBelowPx);

  const activeTree = $derived.by(() => {
    const tree = renderTree;
    if (!tree) return null;
    if (!flattened) return tree;
    // A tree has no single active tab, so the surface's last-focused tab breaks
    // the tie; the route-bound tab wins when there is one.
    return flattenTabbedPanelTree(tree, routeTabKey ?? layout.lastFocusedTabKey() ?? undefined);
  });

  const hiddenButAvailable = $derived(
    tabs.filter((tab) => tab.available && layout.hiddenTabKeys().includes(tab.key)),
  );

  function selectTab(tabKey: string): void {
    layout.activateTab(tabKey);
    layout.noteFocused(tabKey);
    onSelectTab?.(tabKey);
  }

  /**
   * Focus must never be stolen from a control the user moved to themselves, so
   * reclaim only when it already fell to `<body>` or still sits inside the
   * subtree that is going away.
   */
  function shouldReclaimFocus(closing: Element | null): boolean {
    const focused = document.activeElement;
    if (focused === null || focused === document.body) return true;
    return closing?.contains(focused) ?? false;
  }

  async function reclaimFocus(): Promise<void> {
    await tick();
    if (shouldReclaimFocus(null)) host?.focus();
  }

  /**
   * Closing a pane unmounts its body. Focus that lived inside it — the terminal,
   * a diff comment box — would fall to `<body>`, stranding keyboard users and
   * leaving the global single-key shortcuts armed against nothing.
   */
  function closePane(tabKey: string): void {
    const closing = host?.querySelector(`[data-pane-key="${tabKey}"]`) ?? null;
    const reclaim = shouldReclaimFocus(closing);
    layout.setHidden(tabKey, true);
    if (reclaim) void reclaimFocus();
  }

  // The same stranding happens without a click: a released or deleted workspace
  // makes its pane unavailable and unmounts it out from under the focused
  // terminal.
  let lastAvailableTabs: string[] = untrack(() => availableTabs);
  $effect(() => {
    const now = availableTabs;
    const removed = lastAvailableTabs.some((key) => !now.includes(key));
    lastAvailableTabs = now;
    if (removed) void reclaimFocus();
  });

  function focusPane(tabKey: string): void {
    if (layout.lastFocusedTabKey() === tabKey) return;
    layout.noteFocused(tabKey);
    onFocusPane?.(tabKey);
  }

  // A zoom must not survive the disappearance of what was zoomed. The store
  // cannot see availability, so masking it at render time is not enough: a
  // workspace pane zoomed, released, then reclaimed would come back maximized
  // over the conversation the user was reading. Drop the stored value instead.
  $effect(() => {
    if (layout.zoomedLeafID() === null) return;
    if (layout.effectiveZoomedLeafID(availableTabs) !== null) return;
    untrack(() => layout.clearZoom());
  });

  // A deep link is authoritative over stored layout state: it must activate the
  // pane it names, and drop a zoom held by any other leaf, or the URL and the
  // screen disagree with no way for the user to tell why.
  $effect(() => {
    const key = routeTabKey;
    if (!key || !tabs.some((tab) => tab.key === key && tab.available)) return;
    untrack(() => {
      layout.activateTab(key);
      layout.noteFocused(key);
      const zoomed = layout.zoomedLeafID();
      if (zoomed !== null && zoomed !== layout.leafIDForTab(key)) layout.clearZoom();
    });
  });

  $effect(() => {
    const el = host;
    if (!el) {
      hostWidth = 0;
      return;
    }
    hostWidth = Math.round(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      hostWidth = Math.round(entries[0]?.contentRect.width ?? el.getBoundingClientRect().width);
    });
    observer.observe(el);
    return () => observer.disconnect();
  });
</script>

<!-- tabindex so the layout can hold focus itself after a pane closes under it. -->
<div class="detail-pane-layout" bind:this={host} tabindex="-1">
  {#if activeTree}
    <div class="detail-pane-tree">
      <TabbedPanelTree
        dragScope={layout.dragScope}
        node={activeTree}
        tabs={descriptors}
        activeTabKey={routeTabKey ?? layout.lastFocusedTabKey() ?? ""}
        {tablistLabel}
        {leafLabel}
        resizeLabel="Resize detail panes"
        dropTargetsLabel="Detail pane drop targets"
        tabIcon={paneIcon}
        tabActions={hideableTabKeys.length > 0 ? tabActions : undefined}
        {zoomedLeafID}
        onSelectTab={selectTab}
        onFocusPane={onFocusPane ? focusPane : undefined}
        onRatioChange={flattened ? undefined : (splitID, ratio) => layout.setRatio(splitID, ratio)}
        onMoveTabBefore={flattened ? undefined : (source, target) => layout.moveTabBefore(source, target)}
        onAppendTabToLeaf={flattened ? undefined : (source, leafID) => layout.appendTabToLeaf(source, leafID)}
        onSplitTab={flattened
          ? undefined
          : (source, leafID, direction, placement) => layout.splitTab(source, leafID, direction, placement)}
        leafActions={flattened ? undefined : leafActions}
      >
        {#snippet renderTab(tabKey, visible)}
          {@render renderPane(tabKey, visible)}
        {/snippet}
      </TabbedPanelTree>
    </div>
  {/if}

  {#each hiddenButAvailable as tab (tab.key)}
    <!-- Hiding a pane can empty its leaf, which then stops rendering, so the way
         back cannot live inside the tree. -->
    <div class="detail-pane-reopen">
      <span class="detail-pane-reopen-label">{tab.label}</span>
      <Button
        size="sm"
        surface="soft"
        tone="neutral"
        label={`Show ${tab.label}`}
        onclick={() => layout.setHidden(tab.key, false)}
      >
        <ChevronsUpIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      </Button>
    </div>
  {/each}
</div>

{#snippet tabActions(tab: TabbedPanelDescriptor)}
  {#if hideableTabKeys.includes(tab.key)}
    <!-- The only way INTO the hidden state. Deleting the workspace dock removes
         its close button, and the reopen strip alone would be a one-way door. -->
    <button
      class="tabbed-panel-tab-tool"
      type="button"
      title={`Hide ${tab.label}`}
      aria-label={`Hide ${tab.label}`}
      data-testid={`pane-hide-${tab.key}`}
      onclick={() => closePane(tab.key)}
    >
      <XIcon size="11" strokeWidth="2.3" aria-hidden="true" />
    </button>
  {/if}
{/snippet}

{#snippet leafActions(leaf: TabbedPanelLeaf)}
  <PaneLeafActions
    {leaf}
    zoomed={zoomedLeafID === leaf.id}
    onSplit={(tabKey, leafID, direction, placement) =>
      layout.splitTab(tabKey, leafID, direction, placement)}
    onToggleZoom={(leafID) => layout.toggleZoom(leafID)}
  />
{/snippet}

<style>
  .detail-pane-layout {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .detail-pane-tree {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .detail-pane-tree > :global(*) {
    flex: 1;
    min-width: 0;
    min-height: 0;
  }

  .detail-pane-reopen {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    flex-shrink: 0;
    padding: 6px 12px;
    border-top: var(--chrome-border-width) solid var(--border-muted);
    background: var(--bg-inset);
  }

  .detail-pane-reopen-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
  }
</style>
