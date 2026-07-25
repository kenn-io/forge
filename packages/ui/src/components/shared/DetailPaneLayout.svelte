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
    /**
     * Below this container width the tree flattens to a single tab strip.
     *
     * Set to twice the narrowest useful pane, not to a "wide desktop" width: the
     * default arrangement stacks the workspace BELOW the detail, which needs
     * height rather than width, and a high threshold silently removed that whole
     * layout at ordinary window sizes. Side-by-side splits above this width stay
     * the user's explicit choice and are already ratio-clamped.
     */
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
    flattenBelowPx = 720,
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
    // A tree has no single active tab, so the surface's last-focused tab breaks the
    // tie. Focus wins over the route rather than the other way around: the
    // deep-link effect below notes focus whenever the route names a pane, so
    // last-focused is never staler than the route, and preferring the route would
    // make revealing a non-route pane (Focus Terminal) impossible here — the flat
    // strip would snap straight back to the route's pane.
    return flattenTabbedPanelTree(tree, layout.lastFocusedTabKey() ?? routeTabKey ?? undefined);
  });

  // Publish the renderer-only facts the command layer needs: that a layout is
  // mounted here at all, which panes it offers, and whether the narrow-width
  // fallback has flattened it (where every structural edit is disabled, so a
  // palette command must not quietly rearrange a tree nobody can see).
  $effect(() => {
    const report = { availableTabs, flattened };
    // Untracked because the store compares against the previous report before
    // writing: reading it here would make this effect both a reader and a writer
    // of the same state and it would re-run itself forever.
    untrack(() => layout.notePaneRender(report));
    return () => untrack(() => layout.notePaneRender(null));
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
   * Reclaim focus for the layout after something focusable inside it went away.
   *
   * Checked AFTER the DOM update rather than before: whether focus fell to
   * `<body>` is the whole question, and asking beforehand means deciding which
   * elements are about to be removed. That guess was wrong for the close button
   * itself — it lives in the tab header, not the pane body — so keyboard-closing
   * a pane declined restoration and stranded focus. Focus already elsewhere is
   * never stolen, so a background close leaves the user's control alone.
   */
  async function reclaimFocus(): Promise<void> {
    await tick();
    const focused = document.activeElement;
    if (focused === null || focused === document.body) host?.focus();
  }

  /**
   * Closing a pane unmounts its body. Focus that lived inside it — the terminal,
   * a diff comment box, the close button — would fall to `<body>`, stranding
   * keyboard users and leaving the global single-key shortcuts armed against
   * nothing.
   */
  function closePane(tabKey: string): void {
    layout.setHidden(tabKey, true);
    void reclaimFocus();
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
