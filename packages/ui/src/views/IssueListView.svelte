<script lang="ts">
  import { getSidebar, getStores } from "../context.js";
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import IssueList
    from "../components/sidebar/IssueList.svelte";
  import IssueDetail
    from "../components/detail/IssueDetail.svelte";
  import DetailPaneLayout from "../components/shared/DetailPaneLayout.svelte";
  import { getPaneLayoutStore, type PaneTabSpec } from "../stores/paneLayout.svelte.js";
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
    isSidebarCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    /** Float the expanded sidebar over the list (narrow-container hosts). */
    sidebarOverlay?: boolean;
    autoSyncDetail?: IssueDetailSyncMode;
    hideStaleDetailWhileLoading?: boolean;
    onSidebarResize?: (width: number) => void;
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  let {
    selectedIssue = null,
    isSidebarCollapsed = false,
    hideSidebar = false,
    sidebarWidth = 340,
    sidebarOverlay = false,
    autoSyncDetail = "background",
    hideStaleDetailWhileLoading = false,
    onSidebarResize,
    inlineWorkspace = null,
  }: Props = $props();

  function refreshSelectedDetail(): Promise<void> | undefined {
    if (selectedIssue === null) return undefined;
    const ref = selectedIssue;
    return issues.loadIssueDetail(ref.owner, ref.name, ref.number, {
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

  const paneLayout = getPaneLayoutStore("issues");

  const workspaceClaim = useItemWorkspaceClaim({
    controller: () => inlineWorkspace,
    identity: () => claimIdentity,
    detailMatches: () => issueDetailMatchesRef(issues.getIssueDetail(), selectedIssue ?? null),
    envelopeRef: () => issues.getIssueDetail()?.workspace ?? null,
    refresh: () => void refreshSelectedDetail(),
  });

  // One entry per session the surface's stored tree already holds. `available`
  // never conjures a pane: a session pane exists only because the user promoted
  // it, so a workspace whose sessions were never promoted adds nothing here.
  const sessionTabs = $derived<PaneTabSpec[]>(
    (inlineWorkspace?.promotableSessions() ?? []).map((session) => ({
      key: session.paneKey,
      label: session.label,
      available: paneLayout.hasTab(session.paneKey),
      hideable: true,
    })),
  );

  const paneTabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: true },
    { key: "workspace", label: "Workspace", available: workspaceClaim.ref() !== null, hideable: true },
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
      <DetailPaneLayout
        layout={paneLayout}
        tabs={paneTabs}
        tablistLabel="Issue detail panes"
        leafLabel="Issue detail pane group"
      >
        {#snippet renderPane(tabKey, visible)}
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
    </div>
  {:else}
    <div class="placeholder-content">
      <p class="placeholder-text">Select an issue</p>
      <p class="placeholder-hint">j/k to navigate</p>
    </div>
  {/if}
</CollapsibleSidebar>

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
