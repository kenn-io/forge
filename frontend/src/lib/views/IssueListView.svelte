<script lang="ts">
  import type { Snippet } from "svelte";
  import { getSidebar, getStores } from "../context.js";
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import IssueList
    from "../components/sidebar/IssueList.svelte";
  import IssueDetail
    from "../components/detail/IssueDetail.svelte";
  import KataLinksPanel from "../components/kata/KataLinksPanel.svelte";
  import DetailPaneLayout from "../components/shared/DetailPaneLayout.svelte";
  import type { TabbedPanelLeaf } from "../components/shared/tabbed-panel-layout.js";
  import { getPaneLayoutStore, type PaneLayoutStore, type PaneTabSpec } from "../stores/paneLayout.svelte.js";
  import { isSessionPaneKey } from "../stores/session-pane-key.js";
  import type { IssueDetailSyncMode } from "../stores/issues.svelte.js";
  import type { IssueRouteRef } from "../routes.js";
  import { issueDetailMatchesRef } from "../components/detail/detail-match.js";
  import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../workspace-inline.js";
  import { useItemWorkspaceClaim } from "../item-workspace-claim.svelte.js";

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();
  const { issues } = getStores();

  interface Props {
    selectedIssue?: IssueRouteRef | null;
    detailPresentation?: "panes" | "focused";
    isSidebarCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    /** Float the expanded sidebar over the list (narrow-container hosts). */
    sidebarOverlay?: boolean;
    autoSyncDetail?: IssueDetailSyncMode;
    hideStaleDetailWhileLoading?: boolean;
    onSidebarResize?: (width: number) => void;
    inlineWorkspace?: InlineWorkspaceController | null;
    /**
     * The workspace's own controls, rendered in the tab strip of the leaf holding
     * the workspace pane or one of its promoted sessions. Supplied by the app
     * shell: the controls live in `frontend/`, next to the state they act on.
     */
    workspacePaneControls?: Snippet<[boolean]> | undefined;
  }

  let {
    selectedIssue = null,
    detailPresentation = "panes",
    isSidebarCollapsed = false,
    hideSidebar = false,
    sidebarWidth = 340,
    sidebarOverlay = false,
    autoSyncDetail = "background",
    hideStaleDetailWhileLoading = false,
    onSidebarResize,
    inlineWorkspace = null,
    workspacePaneControls = undefined,
  }: Props = $props();

  function refreshSelectedDetail(): void {
    if (selectedIssue === null) return;
    const ref = selectedIssue;
    issues.loadIssueDetail(ref.owner, ref.name, ref.number, {
      sync: false,
      provider: ref.provider,
      platformHost: ref.platformHost,
      repoPath: ref.repoPath,
    });
  }

  const claimIdentity = $derived<WorkspaceItemIdentity | null>(
    selectedIssue
      ? {
          provider: selectedIssue.provider,
          platformHost: selectedIssue.platformHost,
          owner: selectedIssue.owner,
          name: selectedIssue.name,
          repoPath: selectedIssue.repoPath,
          number: selectedIssue.number,
          itemType: "issue",
        }
      : null,
  );

  const paneLayoutStore = getPaneLayoutStore("issues");
  const paneLayout = $derived<PaneLayoutStore | null>(detailPresentation === "panes" ? paneLayoutStore : null);

  const workspaceClaim = useItemWorkspaceClaim({
    controller: () => inlineWorkspace,
    identity: () => claimIdentity,
    detailMatches: () => issueDetailMatchesRef(issues.getIssueDetail(), selectedIssue ?? null),
    envelopeRef: () => issues.getIssueDetail()?.workspace ?? null,
    refresh: refreshSelectedDetail,
  });

  function handlePaneFocus(tabKey: string): void {
    inlineWorkspace?.notePaneFocused(tabKey);
  }

  // One entry per session the surface's stored tree already holds. `available`
  // never conjures a pane: a session pane exists only because the user promoted
  // it, so a workspace whose sessions were never promoted adds nothing here.
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
  mainEmpty={selectedIssue === null}
>
  {#snippet sidebar()}
    <IssueList {sidebarWidth} />
  {/snippet}

  {#if selectedIssue !== null}
    <div class="detail-host">
      {#if detailPresentation === "focused"}
        <IssueDetail
          owner={selectedIssue.owner}
          name={selectedIssue.name}
          number={selectedIssue.number}
          provider={selectedIssue.provider}
          platformHost={selectedIssue.platformHost}
          repoPath={selectedIssue.repoPath}
          autoSync={autoSyncDetail}
          hideStaleWhileLoading={hideStaleDetailWhileLoading}
          {inlineWorkspace}
        />
      {:else if paneLayout !== null}
      <DetailPaneLayout
        layout={paneLayout}
        tabs={paneTabs}
        tablistLabel="Issue detail panes"
        leafLabel="Issue detail pane group"
        paneLeafExtras={workspacePaneControls ? workspaceLeafExtras : undefined}
        onFocusPane={handlePaneFocus}
      >
        {#snippet renderPane(tabKey, visible, _inputActive)}
          {#if tabKey === "conversation"}
            <IssueDetail
              owner={selectedIssue.owner}
              name={selectedIssue.name}
              number={selectedIssue.number}
              provider={selectedIssue.provider}
              platformHost={selectedIssue.platformHost}
              repoPath={selectedIssue.repoPath}
              autoSync={autoSyncDetail}
              hideStaleWhileLoading={hideStaleDetailWhileLoading}
              {inlineWorkspace}
            />
          {:else if tabKey === "kata"}
            <KataLinksPanel
              subject={{
                kind: "issue",
                provider: selectedIssue.provider,
                ...(selectedIssue.platformHost === undefined
                  ? {}
                  : { platformHost: selectedIssue.platformHost }),
                owner: selectedIssue.owner,
                name: selectedIssue.name,
                number: selectedIssue.number,
              }}
              active={visible}
            />
          {:else if tabKey === "workspace" && inlineWorkspace && visible}
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
      <p class="placeholder-text">Select an issue</p>
      <p class="placeholder-hint">j/k to navigate</p>
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
  .detail-pane-workspace-slot {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
    height: 100%;
  }

  .detail-host {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
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
</style>
