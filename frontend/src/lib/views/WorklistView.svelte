<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import { Effect } from "effect";
  import { Button, EmptyState, IconButton, SidebarToggle, Spinner, SplitResizeHandle } from "@kenn-io/kit-ui";
  import type { SplitResizeEvent } from "@kenn-io/kit-ui";
  import ListChecksIcon from "@lucide/svelte/icons/list-checks";
  import XIcon from "@lucide/svelte/icons/x";
  import { ApiProblemError } from "../api/effect-errors.js";
  import { repositoryKeyFromWire } from "../api/repository-key.js";
  import { worklistFailureMessage, type Worklist, type WorklistItem } from "../api/worklists.js";
  import type { AppExecution } from "../app/runtime.js";
  import { getAppRuntime } from "../app/runtime-context.js";
  import IssueDetail from "../components/detail/IssueDetail.svelte";
  import PullDetailPane from "../components/detail/PullDetailPane.svelte";
  import { issueDetailMatchesRef, pullDetailMatchesRef } from "../components/detail/detail-match.js";
  import DetailPaneLayout from "../components/shared/DetailPaneLayout.svelte";
  import type { TabbedPanelLeaf } from "../components/shared/tabbed-panel-layout.js";
  import WorklistContextStrip from "../components/worklists/WorklistContextStrip.svelte";
  import WorklistRail from "../components/worklists/WorklistRail.svelte";
  import {
    worklistItemKey,
    worklistItemMatchesSelection,
    worklistItemSelection,
    worklistSections,
  } from "../components/worklists/worklist-items.js";
  import { getStores } from "../context.js";
  import { useItemWorkspaceClaim } from "../item-workspace-claim.svelte.js";
  import {
    buildWorklistRoute,
    buildWorklistsRoute,
    type PullRequestRouteRef,
    type WorklistDetailTab,
    type WorklistItemSelection,
  } from "../routes.js";
  import { showFlash } from "../stores/flash.svelte.js";
  import { getPaneLayoutStore, type PaneTabSpec } from "../stores/paneLayout.svelte.js";
  import { navigate, replaceUrl } from "../stores/router.svelte.js";
  import { isSessionPaneKey } from "../stores/session-pane-key.js";
  import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../workspace-inline.js";

  interface Props {
    worklistId: number | null;
    selected?: WorklistItemSelection | undefined;
    inlineWorkspace?: InlineWorkspaceController | null;
    workspacePaneControls?: Snippet<[TabbedPanelLeaf]> | undefined;
  }

  let { worklistId, selected = undefined, inlineWorkspace = null, workspacePaneControls = undefined }: Props =
    $props();

  const runtime = getAppRuntime();
  const { worklists, detail: detailStore, issues: issuesStore } = getStores();
  const paneLayout = getPaneLayoutStore("worklists");

  const RAIL_WIDTH_KEY = "kenn-forge-worklist-rail-width";
  const DEFAULT_RAIL_WIDTH = 600;
  const minRailWidth = 320;
  const minDetailWidth = 360;

  function loadRailWidth(): number {
    try {
      const parsed = Number(localStorage.getItem(RAIL_WIDTH_KEY));
      if (Number.isFinite(parsed) && parsed > 0) return parsed;
    } catch {
      // Storage blocked; use the default.
    }
    return DEFAULT_RAIL_WIDTH;
  }

  function persistRailWidth(value: number): void {
    try {
      localStorage.setItem(RAIL_WIDTH_KEY, String(Math.round(value)));
    } catch {
      // Storage blocked; the width just won't survive a reload.
    }
  }

  let requestedRailWidth = $state(loadRailWidth());
  let railCollapsed = $state(false);
  let shellWidth = $state(0);
  let resizeStartWidth = 0;
  const maxRailWidth = $derived(
    shellWidth > 0 ? Math.max(minRailWidth, shellWidth - minDetailWidth) : Number.POSITIVE_INFINITY,
  );
  const railWidth = $derived(Math.max(minRailWidth, Math.min(maxRailWidth, requestedRailWidth)));
  const railAriaMax = $derived(Number.isFinite(maxRailWidth) ? maxRailWidth : Math.max(minRailWidth, railWidth));

  const summaries = $derived(worklists.getSummaries());
  const loadedWorklist = $derived(worklists.getDetail());
  const detailError = $derived(worklists.getDetailError());
  let dismissing = $state(false);
  let undone = $state.raw<{ id: number; title: string } | null>(null);
  let restoring = $state(false);
  let undoTimer: AppExecution<void, never> | null = null;
  // Dismiss and restore finish in the store even after this view is gone; only
  // their presentation and navigation belong to the view.
  let mounted = true;

  // Another tab or an agent can dismiss the open list. A dismissed list stays
  // readable by ID, so treat it as missing unless this view's undo offers it.
  const worklist = $derived(
    loadedWorklist !== null && loadedWorklist.dismissed_at !== null && undone?.id !== loadedWorklist.id
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

  // An agent rewrite can drop the open item. Move to the item that now takes
  // its place in the visible order, else the one before it, else close it.
  function replacementFor(previous: Worklist, next: Worklist, removedKey: string): WorklistItem | null {
    const order =
      visibleOrders.get(previous) ??
      worklistSections(previous.items).flatMap((section) => section.items.map(worklistItemKey));
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

  function handleStackMemberNavigate(ref: PullRequestRouteRef): boolean {
    if (worklistId === null) return false;
    replaceUrl(buildWorklistRoute(worklistId, { ...ref, itemType: "pr", detailTab }));
    return true;
  }


  function showUndo(entry: { id: number; title: string } | null): void {
    undoTimer?.interrupt();
    undoTimer = null;
    if (!mounted) return;
    undone = entry;
    if (entry === null) return;
    undoTimer = runtime.runCommand(
      Effect.sleep("10 seconds").pipe(
        Effect.andThen(
          Effect.sync(() => {
            undone = null;
          }),
        ),
      ),
      { operation: "expire worklist undo", safeContext: {}, onFailure: () => {} },
    );
  }

  $effect(() => () => {
    mounted = false;
    untrack(() => undoTimer?.interrupt());
  });

  function dismiss(): void {
    const list = worklist;
    if (!list || dismissing) return;
    dismissing = true;
    runtime.runCommand(
      worklists.dismissEffect(list.id).pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            if (!mounted) return;
            showUndo({ id: list.id, title: list.title });
            const next = worklists.getSummaries()[0];
            if (next) openList(next.id, "replace");
            else replaceUrl(buildWorklistsRoute());
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            dismissing = false;
          }),
        ),
      ),
      {
        operation: "dismiss worklist",
        safeContext: {},
        onFailure: (failure) => {
          if (!mounted) return;
          showFlash(
            failure instanceof ApiProblemError
              ? worklistFailureMessage(failure, "Could not dismiss the worklist.")
              : "Could not dismiss the worklist.",
            { tone: "danger" },
          );
        },
      },
    );
  }

  // Undo stays on screen until the restore succeeds: a failed restore would
  // otherwise remove the only way back to the list. The expiry timer pauses
  // while the request runs and starts over after a failure.
  function undo(): void {
    const entry = undone;
    if (!entry || restoring) return;
    restoring = true;
    undoTimer?.interrupt();
    undoTimer = null;
    runtime.runCommand(
      worklists.undismissEffect(entry.id).pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            if (!mounted) return;
            showUndo(null);
            openList(entry.id, "replace");
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            restoring = false;
          }),
        ),
      ),
      {
        operation: "restore worklist",
        safeContext: {},
        onFailure: (failure) => {
          if (!mounted) return;
          showUndo(entry);
          showFlash(
            failure instanceof ApiProblemError
              ? worklistFailureMessage(failure, "Could not restore the worklist.")
              : "Could not restore the worklist.",
            { tone: "danger" },
          );
        },
      },
    );
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

  function reloadAfterItemAction(): void {
    runtime.runCommand(worklists.refreshAllEffect(), {
      operation: "refresh worklist after item action",
      safeContext: {},
      onFailure: () => {},
    });
    refreshSelectionDetail();
  }

  const repositoryKey = $derived(selectedItem ? repositoryKeyFromWire(selectedItem.repo) : undefined);
  const prSelection = $derived(
    selected?.itemType === "pr"
      ? {
          owner: selected.owner,
          name: selected.name,
          number: selected.number,
          provider: selected.provider,
          platformHost: selected.platformHost,
          repositoryKey,
          repoPath: selected.repoPath,
        }
      : null,
  );
  const issueSelection = $derived(
    selected?.itemType === "issue"
      ? {
          owner: selected.owner,
          name: selected.name,
          number: selected.number,
          provider: selected.provider,
          platformHost: selected.platformHost,
          repositoryKey,
          repoPath: selected.repoPath,
        }
      : null,
  );
  const isPRSelection = $derived(prSelection !== null);

  const claimIdentity = $derived<WorkspaceItemIdentity | null>(
    selected
      ? {
          provider: selected.provider,
          platformHost: selected.platformHost,
          owner: selected.owner,
          name: selected.name,
          repoPath: selected.repoPath,
          number: selected.number,
          itemType: selected.itemType,
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

  function handleRailResizeStart(): void {
    resizeStartWidth = railWidth;
  }

  function handleRailResize(event: SplitResizeEvent): void {
    requestedRailWidth = Math.max(minRailWidth, Math.min(maxRailWidth, resizeStartWidth + event.delta));
    persistRailWidth(requestedRailWidth);
  }

  const hasDetail = $derived(selected !== undefined && worklist !== null);
</script>

<div class="worklist-shell" bind:clientWidth={shellWidth}>
  {#if undone}
    <div class="worklist-undo-row" role="status">
      <span>Dismissed {undone.title}.</span>
      <Button size="sm" label={restoring ? "Restoring" : "Undo"} disabled={restoring} onclick={undo} />
    </div>
  {/if}

  {#if worklist}
    <div class="worklist-split" class:worklist-split--detail={hasDetail}>
      <section
        class="worklist-rail-pane"
        class:worklist-rail-pane--collapsed={hasDetail && railCollapsed}
        style:--worklist-rail-width={`${railWidth}px`}
      >
        {#if hasDetail && railCollapsed}
          <div class="worklist-collapsed-strip">
            <SidebarToggle
              state="collapsed"
              label="Worklist"
              onclick={() => (railCollapsed = false)}
              class="kit-sidebar-toggle--compact"
            />
          </div>
        {:else}
          <WorklistRail
            {worklist}
            {summaries}
            {selectedKey}
            {dismissing}
            onOpen={openItem}
            onPick={(id) => openList(id, "push")}
            onDismiss={dismiss}
            onItemsChanged={reloadAfterItemAction}
            onSetHandled={setHandled}
            onVisibleOrder={recordVisibleOrder}
          >
            {#snippet headerActions()}
              {#if hasDetail}
                <SidebarToggle
                  state="expanded"
                  label="Worklist"
                  onclick={() => (railCollapsed = true)}
                  class="kit-sidebar-toggle--compact"
                />
              {/if}
            {/snippet}
          </WorklistRail>
        {/if}
      </section>

      {#if hasDetail && !railCollapsed}
        <SplitResizeHandle
          ariaLabel="Resize worklist"
          orientation="horizontal"
          ariaValueMin={minRailWidth}
          ariaValueMax={railAriaMax}
          ariaValueNow={railWidth}
          onResizeStart={handleRailResizeStart}
          onResize={handleRailResize}
        />
      {/if}

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
    </div>
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
      <EmptyState title="No worklists" description="Agents write them with the kenn_forge_put_worklist MCP tool.">
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
    container-type: inline-size;
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

  .worklist-split {
    flex: 1;
    min-height: 0;
    display: flex;
    overflow: hidden;
  }

  .worklist-rail-pane {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .worklist-split--detail .worklist-rail-pane {
    width: var(--worklist-rail-width, 600px);
    flex: 0 0 var(--worklist-rail-width, 600px);
  }

  .worklist-split--detail .worklist-rail-pane--collapsed {
    width: 28px;
    flex-basis: 28px;
    border-right: 1px solid var(--border-default);
  }

  .worklist-collapsed-strip {
    width: 28px;
    flex: 1;
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-top: var(--space-3);
    background: var(--bg-surface);
  }

  .worklist-detail {
    flex: 1;
    min-width: 0;
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

  @container (max-width: 760px) {
    .worklist-split--detail .worklist-rail-pane {
      display: none;
    }

    .worklist-split--detail > :global(.kit-split-resize-handle) {
      display: none;
    }
  }
</style>
