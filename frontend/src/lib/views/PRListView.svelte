<script module lang="ts">
  type DetailTab = "conversation" | "files";
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import { untrack } from "svelte";
  import { getNavigate, getSidebar, getStores } from "../context.js";
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import PullList from "../components/sidebar/PullList.svelte";
  import PullDetailPane from "../components/detail/PullDetailPane.svelte";
  import KataLinksPanel from "../components/kata/KataLinksPanel.svelte";
  import { pullDetailMatchesRef } from "../components/detail/detail-match.js";
  import DetailPaneLayout from "../components/shared/DetailPaneLayout.svelte";
  import type { TabbedPanelLeaf } from "../components/shared/tabbed-panel-layout.js";
  import { getPaneLayoutStore, type PaneLayoutStore, type PaneTabSpec } from "../stores/paneLayout.svelte.js";
  import { isSessionPaneKey } from "../stores/session-pane-key.js";
  import { getStackDepth } from "../stores/keyboard/modal-stack.svelte.js";
  import type { DetailSyncMode } from "../stores/detail.svelte.js";
  import {
    buildFocusPullRequestRoute,
    buildPullRequestFilesRoute,
    buildPullRequestRoute,
    type PullRequestRouteRef,
  } from "../routes.js";
  import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../workspace-inline.js";
  import { useItemWorkspaceClaim } from "../item-workspace-claim.svelte.js";

  type StackMemberNavigate = (ref: PullRequestRouteRef) => boolean | void;

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();
  const navigate = getNavigate();
  const { detail: detailStore } = getStores();
  interface Props {
    selectedPR?: PullRequestRouteRef | null;
    detailTab?: DetailTab;
    detailPresentation?: "panes" | "focused";
    isSidebarCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    /** Float the expanded sidebar over the list (narrow-container hosts). */
    sidebarOverlay?: boolean;
    autoSyncDetail?: DetailSyncMode;
    hideStaleDetailWhileLoading?: boolean;
    workflowApprovalSync?: boolean;
    routeFamily?: "canonical" | "focus";
    onSidebarResize?: (width: number) => void;
    onDetailTabChange?: (tab: DetailTab, options?: { replace?: boolean }) => void;
    onStackMemberNavigate?: StackMemberNavigate;
    onOpenWorkspace?: ((workspaceId: string) => void) | undefined;
    onViewWorkspaces?: (() => void) | undefined;
    inlineWorkspace?: InlineWorkspaceController | null;
    /**
     * The workspace's own controls, rendered in the tab strip of the leaf holding
     * the workspace pane or one of its promoted sessions. Supplied by the app
     * shell: the controls live in `frontend/`, next to the state they act on.
     */
    workspacePaneControls?: Snippet<[boolean]> | undefined;
  }

  let {
    selectedPR = null,
    detailTab = "conversation",
    detailPresentation = "panes",
    isSidebarCollapsed = false,
    hideSidebar = false,
    sidebarWidth = 340,
    sidebarOverlay = false,
    autoSyncDetail = "background",
    hideStaleDetailWhileLoading = false,
    workflowApprovalSync = true,
    routeFamily = "canonical",
    onSidebarResize,
    onDetailTabChange,
    onStackMemberNavigate,
    onOpenWorkspace,
    onViewWorkspaces,
    inlineWorkspace = null,
    workspacePaneControls = undefined,
  }: Props = $props();
  const paneLayoutStore = getPaneLayoutStore("prs");
  const paneLayout = $derived<PaneLayoutStore | null>(detailPresentation === "panes" ? paneLayoutStore : null);

  function detailTabRoute(tab: DetailTab, ref: PullRequestRouteRef): string {
    return tab === "files" ? buildPullRequestFilesRoute(ref) : buildPullRequestRoute(ref);
  }

  /**
   * True when conversation and files sit in different leaves, and are therefore
   * both on screen.
   *
   * This, not which control the user touched, decides history semantics. A pane
   * split into its own leaf still renders a clickable tab header, so keying off
   * "click pushes, focus replaces" would give the same move between two visible
   * panes different Back-stack behavior depending on where the pointer landed.
   */
  const routePanesSplitApart = $derived.by(() => {
    if (paneLayout === null) return false;
    // Straight from the renderer, never inferred from the stored tree. Flattened
    // shows one strip whatever the tree says; a zoom covers every other leaf;
    // and a pane tabbed behind a sibling is not on screen either. All three
    // stored-as-split arrangements show one pane, so a tab change is a
    // navigation again.
    const render = paneLayout.paneRender();
    if (render === null || render.flattened) return false;
    return render.onScreenTabs.includes("conversation") && render.onScreenTabs.includes("files");
  });

  function selectDetailTab(tab: DetailTab): void {
    // Replace while both panes are on screen: moving between them is not a
    // navigation the user would want to walk back through one step at a time.
    const replace = routePanesSplitApart;
    if (onDetailTabChange) {
      onDetailTabChange(tab, { replace });
      return;
    }
    if (selectedPR === null) return;
    navigate(detailTabRoute(tab, selectedPR), { replace });
  }

  function isRouteBoundPane(tabKey: string): tabKey is DetailTab {
    return tabKey === "conversation" || tabKey === "files";
  }

  function handlePaneSelect(tabKey: string): void {
    // The workspace pane has no route of its own; only the two route-bound panes
    // may move the URL.
    if (isRouteBoundPane(tabKey)) selectDetailTab(tabKey);
  }

  function handlePaneFocus(tabKey: string): void {
    // Every pane, before the route-bound filter below: the host decides which keys
    // name a workspace, and it needs the container and the promoted session panes
    // that this branch would otherwise drop.
    inlineWorkspace?.notePaneFocused(tabKey);
    if (!isRouteBoundPane(tabKey) || tabKey === detailTab) return;
    // Focus only owns the route where there is no tab to click, i.e. where both
    // panes are already visible.
    if (!routePanesSplitApart) return;
    selectDetailTab(tabKey);
  }

  function diffKeyboardActive(tabKey: string, inputActive: boolean): boolean {
    if (inputActive) return true;
    if (tabKey !== "files" || detailTab !== "files") return false;
    if (getStackDepth() > 0) return false;
    if (paneLayout === null) return true;
    const render = paneLayout.paneRender();
    return render !== null && render.activeInputTabKey === null && !paneLayout.externalInputActive();
  }

  function handleStackMemberNavigate(ref: PullRequestRouteRef): boolean | void {
    if (onStackMemberNavigate) return onStackMemberNavigate(ref);
    if (routeFamily !== "focus") return undefined;
    navigate(buildFocusPullRequestRoute(ref));
    return true;
  }

  const selectedDetail = $derived.by(() => {
    const detail = detailStore.getDetail();
    return pullDetailMatchesRef(detail, selectedPR) ? detail : null;
  });

  function refreshSelectedDetail(): void {
    if (selectedPR === null) return;
    const ref = selectedPR;
    detailStore.loadDetail(ref.owner, ref.name, ref.number, {
      sync: false,
      provider: ref.provider,
      platformHost: ref.platformHost,
      repoPath: ref.repoPath,
    });
  }

  $effect(() => {
    // The diff pane can be on screen alongside the conversation, so a matching
    // detail is needed whenever the panes are split apart, not only on the files
    // route. Deliberately keyed on the arrangement rather than on true rendered
    // visibility: prefetching a detail the user is one pane switch away from is
    // cheap, and threading visibility out of the layout host is not.
    if (selectedPR === null || (!routePanesSplitApart && detailTab !== "files")) return;
    const ref = selectedPR;
    untrack(() => {
      if (pullDetailMatchesRef(detailStore.getDetail(), ref)) return;
      void refreshSelectedDetail();
    });
  });

  const claimIdentity = $derived<WorkspaceItemIdentity | null>(
    selectedPR
      ? {
          provider: selectedPR.provider,
          platformHost: selectedPR.platformHost,
          owner: selectedPR.owner,
          name: selectedPR.name,
          repoPath: selectedPR.repoPath,
          number: selectedPR.number,
          itemType: "pull",
        }
      : null,
  );

  const workspaceClaim = useItemWorkspaceClaim({
    controller: () => inlineWorkspace,
    identity: () => claimIdentity,
    detailMatches: () => pullDetailMatchesRef(detailStore.getDetail(), selectedPR ?? null),
    envelopeRef: () => detailStore.getDetail()?.workspace ?? null,
    refresh: () => void refreshSelectedDetail(),
  });

  // One entry per session the surface's stored tree already holds. `available`
  // never conjures a pane: a session pane exists only because the user promoted
  // it, so a workspace whose sessions the user never promoted adds nothing here.
  const sessionTabs = $derived<PaneTabSpec[]>(
    (inlineWorkspace?.promotableSessions() ?? []).map((session) => ({
      key: session.paneKey,
      label: session.label,
      available: paneLayout?.hasTab(session.paneKey) ?? false,
      hideable: true,
    })),
  );
  const paneTabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: true },
    { key: "files", label: "Files changed", available: true },
    { key: "kata", label: "Kata", available: true, hideable: true },
    {
      key: "workspace",
      label: inlineWorkspace?.workspacePaneLabel() ?? "Workspace",
      // Retire an empty workflow container behind the surface-hosted dock. A
      // promoted session then fills the branch beside it without a blank stage.
      available:
        workspaceClaim.ref() !== null &&
        inlineWorkspace?.workspacePaneEmpty() !== true &&
        inlineWorkspace?.workspacePaneRowOnly() !== true,
      hideable: true,
    },
    ...sessionTabs,
  ]);

</script>

<CollapsibleSidebar
  isCollapsed={isSidebarCollapsed}
  {hideSidebar}
  {sidebarWidth}
  {onSidebarResize}
  overlay={sidebarOverlay}
  showCollapsedStrip={isSidebarToggleEnabled()}
  onExpand={toggleSidebar}
  mainEmpty={selectedPR === null}
>
  {#snippet sidebar()}
    <PullList getDetailTab={() => detailTab} showSelectedDiffSidebar={false} {sidebarWidth} />
  {/snippet}

  {#if selectedPR !== null}
    <div class="detail-host">
      {#if detailPresentation === "focused"}
        <PullDetailPane
          tabKey={detailTab}
          visible={true}
          keyboardActive={diffKeyboardActive(detailTab, false)}
          pr={selectedPR}
          detail={selectedDetail}
          autoSync={autoSyncDetail}
          hideStaleWhileLoading={hideStaleDetailWhileLoading}
          {workflowApprovalSync}
          onStackMemberNavigate={handleStackMemberNavigate}
          onDetailTabChange={selectDetailTab}
          {onOpenWorkspace}
          {onViewWorkspaces}
          {inlineWorkspace}
        />
      {:else if paneLayout !== null}
      <DetailPaneLayout
        layout={paneLayout}
        tabs={paneTabs}
        tablistLabel="Pull request detail panes"
        leafLabel="Pull request detail pane group"
        routeTabKey={detailTab}
        onSelectTab={handlePaneSelect}
        onFocusPane={handlePaneFocus}
        paneLeafExtras={workspacePaneControls ? workspaceLeafExtras : undefined}
      >
        {#snippet renderPane(tabKey, visible, inputActive)}
          {#if tabKey === "workspace" && inlineWorkspace && visible}
            <!-- Portal target for the single live terminal subtree, which the
                 frontend host reparents in here. Mounted only while visible: a slot
                 that lingered behind another tab or a zoom would stay the registered
                 host and strand the terminal off screen. Unmounting parks it. -->
            <div class="detail-pane-workspace-slot" {@attach inlineWorkspace.slotAttachment}></div>
          {:else if isSessionPaneKey(tabKey)}
            {@const sessionPane = inlineWorkspace?.sessionPane() ?? null}
            {#if sessionPane}
              <!-- The frontend supplies the body: it owns the session registry, and
                   the visibility argument travels with it so a pane tabbed behind a
                   sibling leaves its terminal inert rather than off screen and live. -->
              {@render sessionPane({ paneKey: tabKey, visible })}
            {/if}
          {:else if tabKey === "kata"}
            <KataLinksPanel
              subject={{
                kind: "pull_request",
                provider: selectedPR.provider,
                ...(selectedPR.platformHost === undefined
                  ? {}
                  : { platformHost: selectedPR.platformHost }),
                owner: selectedPR.owner,
                name: selectedPR.name,
                number: selectedPR.number,
              }}
              active={visible}
            />
          {:else}
            <PullDetailPane
              {tabKey}
              {visible}
              keyboardActive={diffKeyboardActive(tabKey, inputActive)}
              pr={selectedPR}
              detail={selectedDetail}
              autoSync={autoSyncDetail}
              hideStaleWhileLoading={hideStaleDetailWhileLoading}
              {workflowApprovalSync}
              onStackMemberNavigate={handleStackMemberNavigate}
              {onOpenWorkspace}
              {onViewWorkspaces}
              {inlineWorkspace}
            />
          {/if}
        {/snippet}
      </DetailPaneLayout>
      {/if}
      <!-- The terminal dock, anchored at this surface's bottom edge while the
           container pane has retired because it is empty or row-only. The dock
           normally lives inside that pane, and must remain reachable outside it. -->
      {#if detailPresentation === "panes"}{@render inlineWorkspace?.dockRow()?.()}{/if}
    </div>
  {:else}
    <div class="placeholder-content">
      <p class="placeholder-text">Select a PR</p>
      <p class="placeholder-hint">j/k to navigate &middot; 1/2 to switch views</p>
    </div>
  {/if}
</CollapsibleSidebar>

<!-- Only the leaf actually holding the workspace or one of its promoted sessions:
     the controls act on that workspace, so offering them from a leaf of unrelated
     panes would be a control with no subject. -->
{#snippet workspaceLeafExtras(leaf: TabbedPanelLeaf)}
  {#if leaf.tabs.some((tabKey) => tabKey === "workspace" || isSessionPaneKey(tabKey))}
    {@render workspacePaneControls?.(leaf.tabs.includes("workspace"))}
  {/if}
{/snippet}

<style>
  .detail-host {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .detail-pane-workspace-slot {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
    height: 100%;
  }

  .placeholder-content {
    text-align: center;
  }

  .placeholder-text {
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .placeholder-hint {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    margin-top: 8px;
    opacity: 0.7;
  }

  .detail-host :global(.pull-detail-content) {
    max-width: 800px;
  }
</style>
