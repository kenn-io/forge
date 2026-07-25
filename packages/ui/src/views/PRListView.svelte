<script module lang="ts">
  type DetailTab = "conversation" | "files";

  const filesScrollPositions: Record<string, number> = Object.create(null) as Record<
    string,
    number
  >;
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import { getNavigate, getSidebar, getStores } from "../context.js";
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import PullList from "../components/sidebar/PullList.svelte";
  import PullDetail from "../components/detail/PullDetail.svelte";
  import DiffFilesLayout from "../components/diff/DiffFilesLayout.svelte";
  import DetailPaneLayout from "../components/shared/DetailPaneLayout.svelte";
  import {
    createTabbedPanelLeaf,
    splitTabbedPanelTabIntoLeaf,
  } from "../components/shared/tabbed-panel-layout.js";
  import { getPaneLayoutStore, type PaneTabSpec } from "../stores/paneLayout.svelte.js";
  import type { ProviderCapabilities, PullDetail as PullDetailResponse } from "../api/types.js";
  import type { DetailSyncMode } from "../stores/detail.svelte.js";
  import { reviewThreadsFromEvents } from "../components/diff/review-thread-context.js";
  import {
    buildFocusPullRequestRoute,
    buildPullRequestFilesRoute,
    buildPullRequestRoute,
    type PullRequestRouteRef,
  } from "../routes.js";
  import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
  import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../workspace-inline.js";
  import { useItemWorkspaceClaim } from "../item-workspace-claim.svelte.js";

  type StackMemberNavigate = (ref: PullRequestRouteRef) => boolean | void;

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();
  const navigate = getNavigate();
  const { detail: detailStore } = getStores();
  const PR_PANE_TABS = ["conversation", "files", "workspace"];

  /** Conversation and files share a leaf, with the workspace below: the layout
   * the PR detail had before panes became rearrangeable. */
  function defaultPRPaneTree() {
    const base = createTabbedPanelLeaf(["conversation", "files"], "conversation");
    return (
      splitTabbedPanelTabIntoLeaf(
        createTabbedPanelLeaf(PR_PANE_TABS, "conversation", base.id),
        "workspace",
        base.id,
        "vertical",
        "after",
      ) ?? base
    );
  }

  const paneLayout = getPaneLayoutStore("prs", PR_PANE_TABS, defaultPRPaneTree());

  const defaultProviderCapabilities: ProviderCapabilities = {
    read_repositories: true,
    read_merge_requests: true,
    read_issues: true,
    read_comments: true,
    read_releases: true,
    read_labels: true,
    read_markdown_images: false,
    read_authenticated_user: false,
    read_ci: true,
    comment_mutation: true,
    thread_reply: false,
    thread_resolve: false,
    label_mutation: true,
    assignee_mutation: false,
    reviewer_mutation: false,
    state_mutation: true,
    merge_mutation: true,
    review_mutation: true,
    workflow_approval: true,
    ready_for_review: true,
    draft_mutation: true,
    issue_mutation: true,
    review_draft_mutation: false,
    review_thread_resolution: false,
    review_suggestion_application: false,
    read_review_threads: false,
    native_multiline_ranges: false,
    mutation_head_binding: false,
    supported_review_actions: [],
  };

  interface Props {
    selectedPR?: PullRequestRouteRef | null;
    detailTab?: DetailTab;
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
    onDetailTabChange?: (tab: DetailTab) => void;
    onStackMemberNavigate?: StackMemberNavigate;
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  let {
    selectedPR = null,
    detailTab = "conversation",
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
    inlineWorkspace = null,
  }: Props = $props();

  function filesScrollKey(): string | null {
    if (selectedPR === null) return null;
    return [
      selectedPR.provider,
      selectedPR.platformHost ?? "",
      selectedPR.repoPath,
      selectedPR.number,
    ].join("\0");
  }

  function filesScrollTop(): number {
    const key = filesScrollKey();
    return key ? (filesScrollPositions[key] ?? 0) : 0;
  }

  function rememberFilesScroll(scrollTop: number): void {
    const key = filesScrollKey();
    if (!key) return;
    filesScrollPositions[key] = scrollTop;
  }

  function detailTabRoute(tab: DetailTab, ref: PullRequestRouteRef): string {
    return tab === "files" ? buildPullRequestFilesRoute(ref) : buildPullRequestRoute(ref);
  }

  function selectDetailTab(tab: DetailTab): void {
    if (onDetailTabChange) {
      onDetailTabChange(tab);
      return;
    }
    if (selectedPR === null) return;
    navigate(detailTabRoute(tab, selectedPR));
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
    if (!isRouteBoundPane(tabKey) || tabKey === detailTab) return;
    // Only meaningful once the two route-bound panes sit in different leaves and
    // are therefore visible at once. While they share a leaf, switching between
    // them is a tab click, and that path already owns the route.
    if (paneLayout.leafIDForTab("conversation") === paneLayout.leafIDForTab("files")) return;
    if (onDetailTabChange) {
      onDetailTabChange(tabKey);
      return;
    }
    if (selectedPR === null) return;
    // Replace, not push: walking between two panes on screen at the same time
    // must not fill the Back stack.
    navigate(detailTabRoute(tabKey, selectedPR), { replace: true });
  }

  function handleStackMemberNavigate(ref: PullRequestRouteRef): boolean | void {
    if (onStackMemberNavigate) return onStackMemberNavigate(ref);
    if (routeFamily !== "focus") return undefined;
    navigate(buildFocusPullRequestRoute(ref));
    return true;
  }

  function detailMatchesSelected(
    detail: PullDetailResponse | null,
    ref: PullRequestRouteRef | null,
  ): boolean {
    return (
      !!detail &&
      !!ref &&
      detail.repo_owner === ref.owner &&
      detail.repo_name === ref.name &&
      detail.merge_request.Number === ref.number &&
      canonicalProvider(detail.repo?.provider ?? "") === canonicalProvider(ref.provider) &&
      resolvedPlatformHost(ref.provider, detail.repo?.platform_host) ===
        resolvedPlatformHost(ref.provider, ref.platformHost) &&
      detail.repo?.repo_path === ref.repoPath
    );
  }

  const selectedDetail = $derived.by(() => {
    const detail = detailStore.getDetail();
    return detailMatchesSelected(detail, selectedPR) ? detail : null;
  });

  function refreshSelectedDetail(): Promise<void> | undefined {
    if (selectedPR === null) return undefined;
    const ref = selectedPR;
    return detailStore.loadDetail(ref.owner, ref.name, ref.number, {
      sync: false,
      provider: ref.provider,
      platformHost: ref.platformHost,
      repoPath: ref.repoPath,
    });
  }

  $effect(() => {
    // The diff pane can be on screen alongside the conversation, so a matching
    // detail is needed whenever the files pane renders, not only on its route.
    if (selectedPR === null || (!filesPaneVisible && detailTab !== "files")) return;
    const ref = selectedPR;
    untrack(() => {
      if (detailMatchesSelected(detailStore.getDetail(), ref)) return;
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

  const workspaceClaimed = $derived(
    inlineWorkspace !== null &&
      claimIdentity !== null &&
      inlineWorkspace.isClaimedFor(claimIdentity),
  );

  const paneTabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: true },
    { key: "files", label: "Files changed", available: true },
    { key: "workspace", label: "Workspace", available: workspaceClaimed, hideable: true },
  ]);

  /** True when files renders alongside the conversation rather than behind it. */
  const filesPaneVisible = $derived(
    paneLayout.leafIDForTab("conversation") !== paneLayout.leafIDForTab("files"),
  );

  useItemWorkspaceClaim({
    controller: () => inlineWorkspace,
    identity: () => claimIdentity,
    detailMatches: () => detailMatchesSelected(detailStore.getDetail(), selectedPR ?? null),
    envelopeRef: () => detailStore.getDetail()?.workspace ?? null,
    refresh: () => void refreshSelectedDetail(),
  });

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
      <DetailPaneLayout
        layout={paneLayout}
        tabs={paneTabs}
        tablistLabel="Pull request detail panes"
        leafLabel="Pull request detail pane group"
        routeTabKey={detailTab}
        onSelectTab={handlePaneSelect}
        onFocusPane={handlePaneFocus}
      >
        {#snippet renderPane(tabKey, visible)}
          {#if tabKey === "conversation"}
            <PullDetail
              owner={selectedPR.owner}
              name={selectedPR.name}
              number={selectedPR.number}
              provider={selectedPR.provider}
              platformHost={selectedPR.platformHost}
              repoPath={selectedPR.repoPath}
              autoSync={autoSyncDetail}
              {workflowApprovalSync}
              hideTabs={true}
              hideStaleWhileLoading={hideStaleDetailWhileLoading}
              onStackMemberNavigate={handleStackMemberNavigate}
              {inlineWorkspace}
            />
          {:else if tabKey === "files" && visible}
            <!-- Mounted only while on screen: every leaf renders all of its tabs, so
                 an unconditional diff pane would fetch a diff for every PR the user
                 merely looks at. Keyed on the PR because DiffFilesLayout keeps
                 per-file state that must not leak across pull requests; the
                 remembered scroll offset is restored through initialScrollTop so
                 neither the remount nor a pane switch loses the reader's place. -->
            {#key `${selectedPR.provider}/${selectedPR.platformHost ?? ""}/${selectedPR.repoPath}/${selectedPR.number}`}
              <DiffFilesLayout
                owner={selectedPR.owner}
                name={selectedPR.name}
                number={selectedPR.number}
                provider={selectedPR.provider}
                platformHost={selectedPR.platformHost}
                repoPath={selectedPR.repoPath}
                diffHeadSHA={selectedDetail?.diff_head_sha}
                capabilities={selectedDetail?.repo?.capabilities ?? defaultProviderCapabilities}
                operations={selectedDetail?.repo?.operations}
                reviewThreads={reviewThreadsFromEvents(selectedDetail?.events)}
                initialScrollTop={filesScrollTop()}
                onScrollTopChange={rememberFilesScroll}
              />
            {/key}
          {:else if tabKey === "workspace" && inlineWorkspace && visible}
            <!-- Portal target for the single live terminal subtree, which the
                 frontend host reparents in here. Mounted only while visible: a slot
                 that lingered behind another tab or a zoom would stay the registered
                 host and strand the terminal off screen. Unmounting parks it. -->
            <div class="detail-pane-workspace-slot" {@attach inlineWorkspace.slotAttachment}></div>
          {/if}
        {/snippet}
      </DetailPaneLayout>
    </div>
  {:else}
    <div class="placeholder-content">
      <p class="placeholder-text">Select a PR</p>
      <p class="placeholder-hint">j/k to navigate &middot; 1/2 to switch views</p>
    </div>
  {/if}
</CollapsibleSidebar>

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
