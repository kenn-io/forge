<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { Provider, WorkspaceRightSidebar } from "@middleman/ui";
  import type { StoreInstances } from "@middleman/ui";

  import { client } from "../../api/runtime.js";
  import {
    getBasePath,
    getPage,
    getRoute,
    getView,
    navigate,
  } from "../../stores/router.svelte.ts";
  import {
    cleanupTheme,
    initTheme,
    reapplyTheme,
  } from "../../stores/theme.svelte.js";
  import {
    emitWorkspaceCommand,
    getActiveWorktreeKey,
    getIssueActions,
    getPullRequestActions,
    getUIConfig,
    initWorkspaceBridge,
    invokeAction,
  } from "../../stores/embed-config.svelte.js";
  import { getGlobalRepo } from "../../stores/filter.svelte.js";
  import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";
  import WorkspaceEmbedEmptyState from "./WorkspaceEmbedEmptyState.svelte";
  import WorkspaceFirstRunPanel from "./WorkspaceFirstRunPanel.svelte";
  import WorkspaceProjectCard from "./WorkspaceProjectCard.svelte";
  import { showFlash } from "../../stores/flash.svelte.js";

  let stores = $state<StoreInstances | undefined>();

  onMount(() => {
    initTheme();
    initWorkspaceBridge();
    return () => {
      cleanupTheme();
    };
  });

  onDestroy(() => {
    stores?.events.disconnect();
  });

  $effect(() => {
    reapplyTheme();
  });
</script>

<Provider
  {client}
  roborevBaseUrl="/api/roborev"
  onError={showFlash}
  onNavigate={(e) =>
    navigate(typeof e === "string" ? e : e.path)}
  onWorkspaceCommand={emitWorkspaceCommand}
  actions={{
    pull: getPullRequestActions().map((a) => ({
      id: a.id,
      label: a.label,
      handler: (ctx) => invokeAction(a, {
        surface: ctx.surface,
        owner: ctx.owner,
        name: ctx.name,
        number: ctx.number,
        ...ctx.meta != null && { meta: ctx.meta },
      }),
    })),
    issue: getIssueActions().map((a) => ({
      id: a.id,
      label: a.label,
      handler: (ctx) => invokeAction(a, {
        surface: ctx.surface,
        owner: ctx.owner,
        name: ctx.name,
        number: ctx.number,
        ...ctx.meta != null && { meta: ctx.meta },
      }),
    })),
  }}
  config={{
    hideStar: getUIConfig().hideStar,
    basePath: getBasePath(),
  }}
  hostState={{
    getGlobalRepo,
    getGroupByRepo: () => stores?.grouping.getGroupByRepo() ?? true,
    getView,
    getActiveWorktreeKey,
  }}
  {getPage}
  sidebar={{
    isEmbedded: () => true,
    isSidebarToggleEnabled: () => false,
    toggleSidebar: () => {},
  }}
  bind:stores
>
  {@const r = getRoute()}
  <main class="embed-layout">
    {#if r.page === "embed-workspace-list"}
      <WorkspaceListSidebar selectedId="" />
    {:else if r.page === "embed-workspace-terminal"}
      <WorkspaceTerminalView
        workspaceId={r.workspaceId}
        hideWorkspaceList={true}
        hideRightSidebar={true}
      />
    {:else if r.page === "embed-workspace-detail"}
      <WorkspaceRightSidebar
        activeTab={r.tab ??
          (r.itemType === "issue" ? "issue" : "pr")}
        workspaceID=""
        provider={r.provider}
        platformHost={r.platformHost}
        repoOwner={r.owner}
        repoName={r.name}
        repoPath={r.repoPath}
        ownerItemType={r.itemType === "issue" ? "issue" : "pull_request"}
        ownerItemNumber={r.number}
        associatedPRNumber={r.itemType === "pr" ? r.number : null}
        branch={r.branch ?? ""}
        roborevBaseUrl={getBasePath().replace(/\/$/, "") +
          "/api/roborev"}
      />
    {:else if r.page === "embed-workspace-empty"}
      <WorkspaceEmbedEmptyState reason={r.reason} />
    {:else if r.page === "embed-workspace-first-run"}
      <WorkspaceFirstRunPanel />
    {:else if r.page === "embed-workspace-project"}
      <WorkspaceProjectCard
        projectId={r.projectId}
        hostKey={r.hostKey}
      />
    {/if}
  </main>
</Provider>

<style>
  .embed-layout {
    flex: 1;
    overflow: hidden;
    background: var(--bg-primary);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
</style>
