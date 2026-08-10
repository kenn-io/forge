<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import { Effect } from "effect";
  import { Button } from "@kenn-io/kit-ui";
  import ChevronsUpIcon from "@lucide/svelte/icons/chevrons-up";
  import XIcon from "@lucide/svelte/icons/x";
  import PaneLeafActions from "./PaneLeafActions.svelte";
  import TabbedPanelTree from "./TabbedPanelTree.svelte";
  import {
    collectTabbedPanelLeafIDs,
    findTabbedPanelLeafByID,
    flattenTabbedPanelTree,
    type TabbedPanelDescriptor,
    type TabbedPanelLeaf,
  } from "./tabbed-panel-layout.js";
  import type { PaneLayoutStore, PaneTabSpec } from "../../stores/paneLayout.svelte.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { observeResize } from "../../browser/observers.js";

  interface Props {
    layout: PaneLayoutStore;
    tabs: PaneTabSpec[];
    renderPane: Snippet<[string, boolean, boolean]>;
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
    /**
     * Caller chrome for a leaf, rendered left of the structural controls.
     *
     * The tab strip is the only chrome a pane has, and a promoted session pane
     * needs its workspace's controls there. Suppressed with the structural
     * controls while flattened, where there is one strip for every pane.
     */
    paneLeafExtras?: Snippet<[TabbedPanelLeaf]> | undefined;
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
    paneLeafExtras = undefined,
  }: Props = $props();
  const runtime = getAppRuntime();
  let focusExecution: AppExecution<void, never> | null = null;

  /**
   * The workspace pane draws its own tab strip: one tab per session, plus the dock.
   * A leaf holding it alone therefore stacked two rows to name one thing, and the
   * outer one said only "Workspace" - which the strip below already implies. It is
   * dropped there, and its controls float over the body instead.
   *
   * Flattened is exempt: that mode collapses every pane into ONE strip, so there is
   * no per-leaf row to remove and nothing for a floating cluster to line up with.
   */
  const SOLO_CHROME_TAB_KEYS = ["workspace"] as const;

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
  const measured = $derived(hostWidth > 0);
  const flattened = $derived(measured && hostWidth < flattenBelowPx);
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

  // Leaves that render at all: everything when nothing holds the zoom, and only
  // the zoomed leaf when something does. A zoom covers its siblings entirely, so
  // their panes are as absent from the screen as a hidden one.
  const renderedLeaves = $derived.by(() => {
    const tree = activeTree;
    if (!tree) return [];
    const leaves = collectTabbedPanelLeafIDs(tree)
      .map((id) => findTabbedPanelLeafByID(tree, id))
      .filter((leaf): leaf is TabbedPanelLeaf => leaf !== null);
    if (flattened || zoomedLeafID === null) return leaves;
    return leaves.filter((leaf) => leaf.id === zoomedLeafID);
  });

  const editableTabs = $derived(renderedLeaves.flatMap((leaf) => leaf.tabs));
  // Mirrors the strip's own decision below (soloChromeTabKeys is emptied while
  // flattened), so the report and the rendered chrome cannot disagree.
  const soloChromeTabs = $derived(
    flattened
      ? []
      : renderedLeaves
          .filter((leaf) => leaf.tabs.length === 1 && (SOLO_CHROME_TAB_KEYS as readonly string[]).includes(leaf.tabs[0]!))
          .map((leaf) => leaf.tabs[0]!),
  );
  // One per rendered leaf. A tab sitting behind a sibling tab is a click away,
  // which is enough to be a command target, but it is not on screen.
  const onScreenTabs = $derived(
    renderedLeaves
      .map((leaf) => leaf.activeTabKey)
      .filter((key): key is string => key !== null && key !== undefined),
  );
  const activeInputTabKey = $derived.by(() => {
    if (layout.externalInputActive()) return "";
    const focused = layout.lastFocusedTabKey();
    if (focused !== null && onScreenTabs.includes(focused)) return focused;
    if (routeTabKey !== undefined && onScreenTabs.includes(routeTabKey)) return routeTabKey;
    return onScreenTabs[0] ?? "";
  });

  // Publish the renderer-only facts the command layer needs: that a layout is
  // mounted here at all, which panes it offers, which are on screen, and whether
  // the narrow-width fallback has flattened it (where every structural edit is
  // disabled, so a palette command must not quietly rearrange a tree nobody can
  // see).
  $effect(() => {
    // Null until the host has been measured. Width decides whether structural
    // edits are allowed at all, and an unmeasured host defaulting to "not
    // flattened" exposes those commands for a frame on a narrow layout.
    const report = measured
      ? {
          activeInputTabKey: activeInputTabKey || null,
          editableTabs,
          onScreenTabs,
          flattened,
          soloChromeTabs,
        }
      : null;
    // Untracked because the store compares against the previous report before
    // writing: reading it here would make this effect both a reader and a writer
    // of the same state and it would re-run itself forever.
    untrack(() => layout.notePaneRender(report));
  });

  // Separate and dependency-free so it only runs when this renderer goes away.
  // Clearing the report from the publishing effect's own cleanup would blank it
  // before every republish, and a caller that names a pane from the report — the
  // workspace pane's tab takes its sole session's name — would see that null,
  // change the tab list, and drive the publishing effect around forever.
  $effect(() => () => untrack(() => layout.notePaneRender(null)));

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
  function reclaimFocus(): void {
    focusExecution?.interrupt();
    focusExecution = runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.andThen(Effect.sync(() => {
          const focused = document.activeElement;
          if (focused === null || focused === document.body) host?.focus();
        })),
      ),
      {
        operation: "restore detail pane focus",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  /**
   * Closing a pane unmounts its body. Focus that lived inside it — the terminal,
   * a diff comment box, the close button — would fall to `<body>`, stranding
   * keyboard users and leaving the global single-key shortcuts armed against
   * nothing.
   */
  function closePane(tabKey: string): void {
    layout.setHidden(tabKey, true);
    reclaimFocus();
  }

  // The same stranding happens without a click: a released or deleted workspace
  // makes its pane unavailable and unmounts it out from under the focused
  // terminal.
  let lastAvailableTabs: string[] = untrack(() => availableTabs);
  $effect(() => {
    const now = availableTabs;
    const removed = lastAvailableTabs.some((key) => !now.includes(key));
    lastAvailableTabs = now;
    if (removed) reclaimFocus();
  });

  // Always tracked, whether or not the host wants the callback: which pane the
  // user last worked in is what the pane keyboard commands act on, so a surface
  // that has no route to sync (Issues) still has to record it or its commands
  // target the route's pane instead of the focused one.
  // Deduped against what this reported last, NOT against the layout's stored value:
  // `lastFocusedTabKey` is also written by tab activation, a deep link, and a fresh
  // promotion, so keying off it swallowed the first real focus in a pane the layout
  // already named - and the host, which keeps its own per-workspace record, has no
  // other way to learn about it. Repeat focus in the same pane is still silent,
  // since a surface that rewrites the URL on focus must not do so on every click.
  let lastReportedFocus: string | null = null;

  function focusPane(tabKey: string): void {
    if (lastReportedFocus === tabKey) return;
    lastReportedFocus = tabKey;
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
  //
  // Authority is a transition, not an invariant. This effect also tracks `tabs`,
  // whose identity changes whenever the pane-render report re-derives the list -
  // including as a consequence of a zoom being placed. Re-asserting on every such
  // change silently undid any zoom on a non-route leaf the moment it was made
  // (Expand Terminal, Maximize pane), so it applies only when the route names a
  // different pane than last applied, or names one that just became available.
  let appliedRouteTabKey: string | null = null;
  $effect(() => {
    const key = routeTabKey;
    if (!key || !tabs.some((tab) => tab.key === key && tab.available)) {
      appliedRouteTabKey = null;
      return;
    }
    if (appliedRouteTabKey === key) return;
    appliedRouteTabKey = key;
    lastReportedFocus = key;
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
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.scoped(
          observeResize(el, (entries) => {
            hostWidth = Math.round(entries[0]?.contentRect.width ?? el.getBoundingClientRect().width);
          }).pipe(Effect.andThen(Effect.never)),
        ),
        { operation: "observe detail pane width", safeContext: {}, onFailure: () => {} },
      ),
    );
    return execution.interrupt;
  });
</script>

<!-- A dropped tab this tree does not hold is a promotion: the workspace pane and
     this tree share a drag scope, and the source stays where it is because the
     stored pane tree is the only record that the session moved. Every ordinary
     mutation refuses such a source, so the branch has to be here. -->
<!-- tabindex so the layout can hold focus itself after a pane closes under it. -->
<div class="detail-pane-layout" bind:this={host} tabindex="-1">
  {#if activeTree}
    <div class="detail-pane-tree">
      <TabbedPanelTree
        dragScope={layout.dragScope}
        node={activeTree}
        tabs={descriptors}
        activeTabKey={activeInputTabKey}
        {tablistLabel}
        {leafLabel}
        resizeLabel="Resize detail panes"
        dropTargetsLabel="Detail pane drop targets"
        tabIcon={paneIcon}
        tabActions={hideableTabKeys.length > 0 ? tabActions : undefined}
        {zoomedLeafID}
        onSelectTab={selectTab}
        onFocusPane={focusPane}
        onRatioChange={flattened ? undefined : (splitID, ratio) => layout.setRatio(splitID, ratio)}
        onMoveTabBefore={flattened
          ? undefined
          : (source, target) =>
              layout.hasTab(source)
                ? layout.moveTabBefore(source, target)
                : layout.promoteTab(source, { kind: "before", tabKey: target })}
        onAppendTabToLeaf={flattened
          ? undefined
          : (source, leafID) =>
              layout.hasTab(source)
                ? layout.appendTabToLeaf(source, leafID)
                : layout.promoteTab(source, { kind: "tab", leafID })}
        onSplitTab={flattened
          ? undefined
          : (source, leafID, direction, placement) =>
              layout.hasTab(source)
                ? layout.splitTab(source, leafID, direction, placement)
                : layout.promoteTab(source, { kind: "split", leafID, direction, placement })}
        leafActions={flattened ? undefined : leafActions}
        soloChromeTabKeys={flattened ? [] : SOLO_CHROME_TAB_KEYS}
      >
        {#snippet renderTab(tabKey, visible)}
          {@render renderPane(tabKey, visible, tabKey === activeInputTabKey)}
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
  {@render paneLeafExtras?.(leaf)}
  <PaneLeafActions
    {leaf}
    zoomed={zoomedLeafID === leaf.id}
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
