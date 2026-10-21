<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import { Effect } from "effect";
  import { Button, CollapsibleSidebar, EmptyState, IconButton, Spinner } from "@kenn-io/kit-ui";
  import ListChecksIcon from "@lucide/svelte/icons/list-checks";
  import XIcon from "@lucide/svelte/icons/x";
  import { ApiProblemError } from "../api/effect-errors.js";
  import { worklistFailureMessage, type Worklist, type WorklistItem } from "../api/worklists.js";
  import { getAppRuntime } from "../app/runtime-context.js";
  import IssueDetail from "../components/detail/IssueDetail.svelte";
  import PullDetailPane from "../components/detail/PullDetailPane.svelte";
  import { issueDetailMatchesRef, pullDetailMatchesRef } from "../components/detail/detail-match.js";
  import DetailPaneLayout from "../components/shared/DetailPaneLayout.svelte";
  import type { TabbedPanelLeaf } from "../components/shared/tabbed-panel-layout.js";
  import WorklistContextStrip from "../components/worklists/WorklistContextStrip.svelte";
  import WorklistRail from "../components/worklists/WorklistRail.svelte";
  import { WorklistDismissal } from "../components/worklists/worklist-dismissal.svelte.js";
  import {
    readWorklistOrder,
    worklistItemKey,
    worklistItemMatchesSelection,
    worklistItemSelection,
    worklistItemsByScore,
    worklistSections,
  } from "../components/worklists/worklist-items.js";
  import { getSidebar, getStores } from "../context.js";
  import { useItemWorkspaceClaim } from "../item-workspace-claim.svelte.js";
  import {
    buildWorklistRoute,
    buildWorklistsRoute,
    type PullRequestRouteRef,
    type WorklistDetailTab,
    type WorklistItemSelection,
  } from "../routes.js";
  import { showFlash } from "../stores/flash.svelte.js";
  import { registerScopedActions } from "../stores/keyboard/registry.svelte.js";
  import { getPaneLayoutStore, type PaneTabSpec } from "../stores/paneLayout.svelte.js";
  import { getRoute, navigate, replaceUrl } from "../stores/router.svelte.js";
  import { isSessionPaneKey } from "../stores/session-pane-key.js";
  import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../workspace-inline.js";

  interface Props {
    worklistId: number | null;
    selected?: WorklistItemSelection | undefined;
    isSidebarCollapsed?: boolean;
    sidebarWidth?: number;
    sidebarOverlay?: boolean;
    onSidebarResize?: (width: number) => void;
    inlineWorkspace?: InlineWorkspaceController | null;
    workspacePaneControls?: Snippet<[TabbedPanelLeaf]> | undefined;
  }

  let {
    worklistId,
    selected = undefined,
    isSidebarCollapsed = false,
    sidebarWidth = 340,
    sidebarOverlay = false,
    onSidebarResize,
    inlineWorkspace = null,
    workspacePaneControls = undefined,
  }: Props = $props();

  const runtime = getAppRuntime();
  const { worklists, detail: detailStore, issues: issuesStore } = getStores();
  const { toggleSidebar } = getSidebar();
  const paneLayout = getPaneLayoutStore("worklists");

  const summaries = $derived(worklists.getSummaries());
  const loadedWorklist = $derived(worklists.getDetail());
  const detailError = $derived(worklists.getDetailError());

  const dismissal = new WorklistDismissal({
    runtime,
    worklists,
    shownListId: () => worklistId,
    onDismissed: () => {
      const next = worklists.getSummaries()[0];
      if (next) openList(next.id, "replace");
      else replaceUrl(buildWorklistsRoute());
    },
    onRestored: (id) => openList(id, "replace"),
  });
  $effect(() => () => dismissal.dispose());

  // Another tab or an agent can dismiss the open list. A dismissed list stays
  // readable by ID, so treat it as missing unless this view is dismissing it
  // or offering its undo.
  const worklist = $derived(
    loadedWorklist !== null && loadedWorklist.dismissed_at !== null && !dismissal.keepsShowing(loadedWorklist.id)
      ? null
      : loadedWorklist,
  );
  const routedListMissing = $derived(
    worklistId === null ||
      (worklists.getOpenId() === worklistId &&
        (detailError?.kind === "notFound" || (loadedWorklist !== null && worklist === null))),
  );
  const selectedItem = $derived(worklist?.items.find((item) => worklistItemMatchesSelection(item, selected)) ?? null);
  const selectedKey = $derived(selectedItem ? worklistItemKey(selectedItem) : null);
  const detailTab = $derived<WorklistDetailTab>(selected?.detailTab ?? "conversation");

  let selectFirstItemOnArrival: number | null = untrack(() => (selected ? null : worklistId));

  function openList(id: number, mode: "push" | "replace"): void {
    selectFirstItemOnArrival = id;
    const route = buildWorklistRoute(id);
    if (mode === "push") navigate(route);
    else replaceUrl(route);
  }

  $effect(() => {
    untrack(() =>
      runtime.runCommand(worklists.refreshSummariesEffect(), {
        operation: "refresh worklists on open",
        safeContext: {},
        onFailure: () => {},
      }),
    );
    return () =>
      untrack(() =>
        runtime.runCommand(worklists.openEffect(null), {
          operation: "close worklist",
          safeContext: {},
          onFailure: () => {},
        }),
      );
  });

  $effect(() => {
    const id = worklistId;
    untrack(() =>
      runtime.runCommand(worklists.openEffect(id), {
        operation: "open worklist",
        safeContext: { routed: id !== null },
        onFailure: () => {},
      }),
    );
  });

  $effect(() => {
    if (!routedListMissing || !worklists.summariesLoaded()) return;
    const newest = summaries.find((summary) => summary.id !== worklistId);
    if (newest) untrack(() => openList(newest.id, "replace"));
  });

  $effect(() => {
    if (!worklist || worklist.id !== selectFirstItemOnArrival) return;
    selectFirstItemOnArrival = null;
    if (selected) return;
    const first = [...worklist.items].sort((a, b) => a.position - b.position)[0];
    if (first) untrack(() => replaceUrl(buildWorklistRoute(worklist.id, worklistItemSelection(first))));
  });

  // The rail reports its visible order for each list it shows, keyed by the
  // list object, so the order before an agent rewrite is still known after it.
  const visibleOrders = new WeakMap<Worklist, readonly string[]>();
  let previousWorklist: Worklist | null = null;

  function recordVisibleOrder(list: Worklist, keys: readonly string[]): void {
    visibleOrders.set(list, keys);
  }

  // A collapsed rail is unmounted and reopens with its filter and sections
  // reset, so its keys follow the stored row order until it is back.
  function visibleKeys(list: Worklist): readonly string[] {
    const reported = isSidebarCollapsed ? undefined : visibleOrders.get(list);
    if (reported) return reported;
    const rows =
      readWorklistOrder() === "score"
        ? worklistItemsByScore(list.items)
        : worklistSections(list.items).flatMap((section) => section.items);
    return rows.map(worklistItemKey);
  }

  // An agent rewrite can drop the open item. Move to the item that now takes
  // its place in the visible order, else the one before it, else close it.
  function replacementFor(previous: Worklist, next: Worklist, removedKey: string): WorklistItem | null {
    const order = visibleKeys(previous);
    const surviving = new Map(next.items.map((item) => [worklistItemKey(item), item]));
    const at = order.indexOf(removedKey);
    const after = order.slice(at + 1).find((key) => surviving.has(key));
    const before = order
      .slice(0, Math.max(at, 0))
      .reverse()
      .find((key) => surviving.has(key));
    const key = after ?? before;
    return key === undefined ? null : (surviving.get(key) ?? null);
  }

  $effect(() => {
    const list = worklist;
    const selection = selected;
    const previous = previousWorklist;
    previousWorklist = list;
    if (!list || !previous || previous === list || previous.id !== list.id || !selection) return;
    if (list.items.some((item) => worklistItemMatchesSelection(item, selection))) return;
    const removed = previous.items.find((item) => worklistItemMatchesSelection(item, selection));
    if (!removed) return;
    const next = replacementFor(previous, list, worklistItemKey(removed));
    untrack(() =>
      replaceUrl(
        next ? buildWorklistRoute(list.id, worklistItemSelection(next, detailTab)) : buildWorklistRoute(list.id),
      ),
    );
  });

  function openItem(item: WorklistItem): void {
    if (!worklist) return;
    replaceUrl(buildWorklistRoute(worklist.id, worklistItemSelection(item, detailTab)));
  }

  function closeItem(): void {
    if (worklistId !== null) replaceUrl(buildWorklistRoute(worklistId));
  }

  function handleDetailTabChange(tab: WorklistDetailTab): void {
    if (!selected || worklistId === null || tab === detailTab) return;
    replaceUrl(buildWorklistRoute(worklistId, { ...selected, detailTab: tab }));
  }

  // Stack members share the open item's repository, so they keep its identity.
  function handleStackMemberNavigate(ref: PullRequestRouteRef): boolean {
    if (worklistId === null) return false;
    const repositoryKey = ref.repositoryKey ?? selected?.repositoryKey;
    replaceUrl(buildWorklistRoute(worklistId, { ...ref, repositoryKey, itemType: "pr", detailTab }));
    return true;
  }

  function setHandled(items: readonly WorklistItem[], handled: boolean, onApplied?: () => void): void {
    const list = worklist;
    if (!list || items.length === 0) return;
    const fallback = handled ? "Could not mark the item handled." : "Could not mark the item unhandled.";
    runtime.runCommand(
      worklists.setHandledEffect(list.id, items, handled).pipe(Effect.tap(() => Effect.sync(() => onApplied?.()))),
      {
        operation: "set worklist items handled",
        safeContext: { handled, count: items.length },
        onFailure: (failure) =>
          showFlash(failure instanceof ApiProblemError ? worklistFailureMessage(failure, fallback) : fallback, {
            tone: "danger",
          }),
      },
    );
  }

  function step(delta: 1 | -1, keys: readonly string[]): void {
    const list = worklist;
    if (!list || keys.length === 0) return;
    const current = selectedKey === null ? -1 : keys.indexOf(selectedKey);
    const next = current === -1 ? (delta === 1 ? 0 : keys.length - 1) : current + delta;
    const key = keys[Math.max(0, Math.min(keys.length - 1, next))];
    const item = list.items.find((candidate) => worklistItemKey(candidate) === key);
    if (!item) return;
    openItem(item);
    queueMicrotask(() => {
      document.querySelector(`[data-worklist-key="${CSS.escape(worklistItemKey(item))}"]`)?.scrollIntoView({
        block: "nearest",
      });
    });
  }

  function stepVisible(delta: 1 | -1): void {
    if (worklist) step(delta, visibleKeys(worklist));
  }

  function markCurrentAndAdvance(): void {
    const list = worklist;
    const current = selectedItem;
    if (!list || !current || current.handled_at !== null) {
      stepVisible(1);
      return;
    }
    // Move on only once the mark lands, so a failed mark keeps its item open,
    // and only while the same list and item are still open here.
    const keys = visibleKeys(list);
    setHandled([current], true, () => {
      const route = getRoute();
      const stillOpen =
        route.page === "worklists" && route.worklistId === list.id && worklistItemMatchesSelection(current, route.selected);
      if (stillOpen) step(1, keys);
    });
  }

  // Registered here rather than in the rail so they keep working while the
  // rail is collapsed, as the pull request list's do.
  $effect(() => {
    return registerScopedActions("worklist-view", [
      {
        id: "worklists.next",
        label: "Next worklist item",
        scope: "view-worklists",
        binding: { key: "j" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists",
        handler: () => stepVisible(1),
      },
      {
        id: "worklists.prev",
        label: "Previous worklist item",
        scope: "view-worklists",
        binding: { key: "k" },
        priority: 0,
        when: (ctx) => ctx.page === "worklists",
        handler: () => stepVisible(-1),
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
    ]);
  });

  function reloadAfterItemAction(): void {
    runtime.runCommand(worklists.refreshAllEffect(), {
      operation: "refresh worklist after item action",
      safeContext: {},
      onFailure: () => {},
    });
    refreshSelectionDetail();
  }

  // The URL route can be stale after a rename; the matched item carries the
  // repository's current route.
  const target = $derived(selectedItem ? worklistItemSelection(selectedItem) : selected);
  const prSelection = $derived(
    target?.itemType === "pr"
      ? {
          owner: target.owner,
          name: target.name,
          number: target.number,
          provider: target.provider,
          platformHost: target.platformHost,
          repositoryKey: target.repositoryKey,
          repoPath: target.repoPath,
        }
      : null,
  );
  const issueSelection = $derived(
    target?.itemType === "issue"
      ? {
          owner: target.owner,
          name: target.name,
          number: target.number,
          provider: target.provider,
          platformHost: target.platformHost,
          repositoryKey: target.repositoryKey,
          repoPath: target.repoPath,
        }
      : null,
  );
  const isPRSelection = $derived(prSelection !== null);

  const claimIdentity = $derived<WorkspaceItemIdentity | null>(
    target
      ? {
          provider: target.provider,
          platformHost: target.platformHost,
          owner: target.owner,
          name: target.name,
          repoPath: target.repoPath,
          number: target.number,
          itemType: target.itemType,
        }
      : null,
  );

  const selectedPullDetail = $derived.by(() => {
    const detail = detailStore.getDetail();
    return pullDetailMatchesRef(detail, prSelection) ? detail : null;
  });

  function loadedDetailMatchesSelection(): boolean {
    if (prSelection) return pullDetailMatchesRef(detailStore.getDetail(), prSelection);
    if (issueSelection) return issueDetailMatchesRef(issuesStore.getIssueDetail(), issueSelection);
    return false;
  }

  function selectionEnvelopeRef() {
    if (prSelection) return detailStore.getDetail()?.workspace ?? null;
    if (issueSelection) return issuesStore.getIssueDetail()?.workspace ?? null;
    return null;
  }

  function refreshSelectionDetail(): void {
    const ref = prSelection ?? issueSelection;
    if (!ref) return;
    const options = {
      sync: false,
      provider: ref.provider,
      platformHost: ref.platformHost,
      repositoryKey: ref.repositoryKey,
      repoPath: ref.repoPath,
    };
    if (prSelection) detailStore.loadDetail(ref.owner, ref.name, ref.number, options);
    else issuesStore.loadIssueDetail(ref.owner, ref.name, ref.number, options);
  }

  const workspaceClaim = useItemWorkspaceClaim({
    controller: () => inlineWorkspace,
    identity: () => claimIdentity,
    detailMatches: loadedDetailMatchesSelection,
    envelopeRef: selectionEnvelopeRef,
    refresh: refreshSelectionDetail,
  });

  const sessionTabs = $derived<PaneTabSpec[]>(
    (inlineWorkspace?.promotableSessions() ?? []).map((session) => ({
      key: session.paneKey,
      label: session.label,
      available: paneLayout.hasTab(session.paneKey),
      hideable: true,
    })),
  );

  const paneTabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: selected !== undefined },
    { key: "files", label: "Files changed", available: isPRSelection },
    {
      key: "workspace",
      label: inlineWorkspace?.workspacePaneLabel() ?? "Workspace",
      available:
        workspaceClaim.ref() !== null &&
        inlineWorkspace?.workspacePaneEmpty() !== true &&
        inlineWorkspace?.workspacePaneRowOnly() !== true,
      hideable: true,
    },
    ...sessionTabs,
  ]);

  const routePanesSplitApart = $derived.by(() => {
    const render = paneLayout.paneRender();
    if (render === null || render.flattened) return false;
    return render.onScreenTabs.includes("conversation") && render.onScreenTabs.includes("files");
  });

  function handlePaneSelect(tabKey: string): void {
    if (tabKey === "conversation" || tabKey === "files") handleDetailTabChange(tabKey);
  }

  function handlePaneFocus(tabKey: string): void {
    inlineWorkspace?.notePaneFocused(tabKey);
    if (tabKey !== "conversation" && tabKey !== "files") return;
    if (!isPRSelection || tabKey === detailTab || !routePanesSplitApart) return;
    handleDetailTabChange(tabKey);
  }

  const hasDetail = $derived(selected !== undefined && worklist !== null);
</script>

<div class="worklist-shell">
  {#if dismissal.state.kind === "undo" || dismissal.state.kind === "restoring"}
    {@const restoring = dismissal.state.kind === "restoring"}
    <div class="worklist-undo-row" role="status">
      <span>Dismissed {dismissal.state.title}.</span>
      <Button size="sm" label={restoring ? "Restoring" : "Undo"} disabled={restoring} onclick={() => dismissal.undo()} />
    </div>
  {/if}

  {#if worklist}
    <CollapsibleSidebar
      isCollapsed={isSidebarCollapsed}
      {sidebarWidth}
      {onSidebarResize}
      overlay={sidebarOverlay}
      showCollapsedStrip={true}
      onExpand={toggleSidebar}
      mainEmpty={!hasDetail}
    >
      {#snippet sidebar()}
        <WorklistRail
          {worklist}
          {summaries}
          {selectedKey}
          dismissing={dismissal.state.kind === "dismissing"}
          onOpen={openItem}
          onStep={stepVisible}
          onPick={(id) => openList(id, "push")}
          onDismiss={() => dismissal.dismiss(worklist)}
          onItemsChanged={reloadAfterItemAction}
          onSetHandled={setHandled}
          onVisibleOrder={recordVisibleOrder}
        />
      {/snippet}

      {#if hasDetail && selected}
        <section class="worklist-detail">
          {#if selectedItem}
            <WorklistContextStrip
              item={selectedItem}
              createdBy={worklist.created_by}
              onToggleHandled={() => setHandled([selectedItem], selectedItem.handled_at === null)}
              onClose={closeItem}
            />
          {:else}
            <div class="worklist-detail-header">
              <span>{selected.owner}/{selected.name}#{selected.number}</span>
              <IconButton size="sm" ariaLabel="Close worklist item" onclick={closeItem}>
                <XIcon size={14} aria-hidden="true" />
              </IconButton>
            </div>
          {/if}

          <DetailPaneLayout
            layout={paneLayout}
            tabs={paneTabs}
            tablistLabel="Worklist detail panes"
            leafLabel="Worklist detail pane group"
            routeTabKey={isPRSelection ? detailTab : undefined}
            onSelectTab={handlePaneSelect}
            onFocusPane={handlePaneFocus}
            paneLeafExtras={workspacePaneControls ? workspaceLeafExtras : undefined}
          >
            {#snippet renderPane(tabKey, visible, inputActive)}
              {#if tabKey === "workspace" && inlineWorkspace && visible}
                <div class="detail-pane-workspace-slot" {@attach inlineWorkspace.slotAttachment}></div>
              {:else if isSessionPaneKey(tabKey)}
                {@const sessionPane = inlineWorkspace?.sessionPane() ?? null}
                {#if sessionPane}
                  {@render sessionPane({ paneKey: tabKey, visible })}
                {/if}
              {:else if prSelection}
                <PullDetailPane
                  {tabKey}
                  {visible}
                  keyboardActive={inputActive}
                  pr={prSelection}
                  detail={selectedPullDetail}
                  autoSync="background"
                  hideStaleWhileLoading={true}
                  workflowApprovalSync={false}
                  onStackMemberNavigate={handleStackMemberNavigate}
                  onDetailTabChange={handleDetailTabChange}
                  {inlineWorkspace}
                />
              {:else if tabKey === "conversation" && issueSelection}
                <IssueDetail
                  owner={issueSelection.owner}
                  name={issueSelection.name}
                  number={issueSelection.number}
                  provider={issueSelection.provider}
                  platformHost={issueSelection.platformHost}
                  repositoryKey={issueSelection.repositoryKey}
                  repoPath={issueSelection.repoPath}
                  autoSync="background"
                  hideStaleWhileLoading={true}
                  {inlineWorkspace}
                />
              {/if}
            {/snippet}
          </DetailPaneLayout>
          {@render inlineWorkspace?.dockRow()?.()}
        </section>
      {/if}
    </CollapsibleSidebar>
  {:else if detailError?.kind === "failed"}
    <div class="worklist-status" role="alert">
      <span>{detailError.message}</span>
      <Button
        size="sm"
        label="Retry"
        onclick={() =>
          runtime.runCommand(worklists.refreshOpenDetailEffect(), {
            operation: "retry worklist",
            safeContext: {},
            onFailure: () => {},
          })}
      />
    </div>
  {:else if routedListMissing && worklists.getSummariesError() !== null}
    <div class="worklist-status" role="alert">
      <span>{worklists.getSummariesError()}</span>
      <Button
        size="sm"
        label="Retry"
        onclick={() =>
          runtime.runCommand(worklists.refreshSummariesEffect(), {
            operation: "retry worklists",
            safeContext: {},
            onFailure: () => {},
          })}
      />
    </div>
  {:else if routedListMissing && worklists.summariesLoaded() && summaries.length === 0}
    <div class="worklist-empty">
      <EmptyState title="No worklists" description="Ask an agent connected to Forge to make one, for example in the Kenn chat sidebar.">
        {#snippet icon()}<ListChecksIcon size={24} aria-hidden="true" />{/snippet}
      </EmptyState>
    </div>
  {:else}
    <div class="worklist-status" role="status"><Spinner size={16} />Loading worklist</div>
  {/if}
</div>

{#snippet workspaceLeafExtras(leaf: TabbedPanelLeaf)}
  {#if leaf.tabs.some((tabKey) => tabKey === "workspace" || isSessionPaneKey(tabKey))}
    {@render workspacePaneControls?.(leaf)}
  {/if}
{/snippet}

<style>
  .worklist-shell {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .worklist-undo-row {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-5);
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .worklist-detail {
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .worklist-detail-header {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-5);
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .detail-pane-workspace-slot {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
    height: 100%;
  }

  .worklist-status {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-4);
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .worklist-empty {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
  }
</style>
