<script lang="ts">
  import { onDestroy, onMount, untrack } from "svelte";
  import {
    Button,
    EmptyState,
    FlashBanner,
    Spinner,
  } from "@kenn-io/kit-ui";
  import { Provider, WorkspaceRightSidebar } from "@middleman/ui";
  import type { StoreInstances } from "@middleman/ui";

  import { client } from "../../api/runtime.js";
  import { getSettings } from "../../api/settings.js";
  import {
    getBasePath,
    getPage,
    getRoute,
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
  import SessionTerminalPool from "./SessionTerminalPool.svelte";
  import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";
  import WorkspaceEmbedEmptyState from "./WorkspaceEmbedEmptyState.svelte";
  import WorkspaceFirstRunPanel from "./WorkspaceFirstRunPanel.svelte";
  import WorkspaceProjectCard from "./WorkspaceProjectCard.svelte";
  import {
    beginTerminalSettingsHydration,
    hydrateTerminalSettings,
  } from "@middleman/ui/stores/terminal-settings-persistence";
  import { showFlash } from "@middleman/ui/stores/flash";

  let stores = $state<StoreInstances | undefined>();
  let terminalSettingsReady = $state(false);
  let terminalSettingsError = $state<string | null>(null);
  let settingsLoadSequence = 0;
  let settingsLoadController: AbortController | undefined;
  const terminalSettingsTimeoutMs = 8_000;

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

  $effect(() => {
    const activeStores = stores;
    if (!activeStores) return;

    void loadTerminalSettings(activeStores);
    return () => {
      settingsLoadSequence += 1;
      settingsLoadController?.abort();
      settingsLoadController = undefined;
    };
  });

  async function getSettingsWithTimeout(controller: AbortController) {
    let timeout: ReturnType<typeof setTimeout> | undefined;
    try {
      return await Promise.race([
        getSettings({ signal: controller.signal }),
        new Promise<never>((_, reject) => {
          timeout = setTimeout(() => {
            const error = new Error("Timed out loading terminal settings");
            controller.abort(error);
            reject(error);
          }, terminalSettingsTimeoutMs);
        }),
      ]);
    } finally {
      if (timeout !== undefined) clearTimeout(timeout);
    }
  }

  async function loadTerminalSettings(activeStores: StoreInstances): Promise<void> {
    settingsLoadController?.abort();
    const controller = new AbortController();
    settingsLoadController = controller;
    const sequence = ++settingsLoadSequence;
    const terminalHydration = untrack(() =>
      beginTerminalSettingsHydration(activeStores.settings)
    );
    terminalSettingsReady = false;
    terminalSettingsError = null;
    try {
      const settings = await getSettingsWithTimeout(controller);
      if (sequence !== settingsLoadSequence) return;
      hydrateTerminalSettings(terminalHydration, settings.terminal);
      terminalSettingsReady = true;
    } catch (error) {
      if (sequence !== settingsLoadSequence) return;
      const detail = error instanceof Error ? error.message : "Unknown error";
      terminalSettingsError = detail;
      showFlash(`Couldn't load terminal settings: ${detail}`, {
        tone: "danger",
      });
    } finally {
      if (settingsLoadController === controller) {
        settingsLoadController = undefined;
      }
    }
  }

  function retryTerminalSettings(): void {
    const activeStores = stores;
    if (!activeStores) return;
    void loadTerminalSettings(activeStores);
  }
</script>

<Provider
  {client}
  roborevBaseUrl="/api/roborev"
  onError={(msg) => showFlash(msg, { tone: "danger" })}
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
  <!-- Embed routes have no app header, so the shared flash banner pins to the
       top of the pane. Without this mount, showFlash from embed surfaces
       (provider errors, workspace actions) lands in the shared store with no
       banner to render it. -->
  <FlashBanner top="0" />
  <main class="embed-layout">
    {#if r.page === "embed-workspace-list"}
      <WorkspaceListSidebar selectedId="" />
    {:else if r.page === "embed-workspace-terminal"}
      {#if terminalSettingsReady}
        <!-- The view renders portal slots, not terminals. In the full app shell
             WorkspaceHost mounts the pool; this branch replaces that shell
             entirely, so without its own pool every session pane is blank.
             Safe to mount here precisely because the two branches are
             exclusive — two pools would render one session twice. -->
        <SessionTerminalPool />
        <WorkspaceTerminalView
          workspaceId={r.workspaceId}
          hideWorkspaceList={true}
          hideRightSidebar={true}
          {terminalSettingsReady}
        />
      {:else if terminalSettingsError}
        <EmptyState
          title="Couldn't load terminal settings"
          description={terminalSettingsError}
        >
          <Button
            label="Retry"
            ariaLabel="Retry terminal settings"
            onclick={retryTerminalSettings}
          />
        </EmptyState>
      {:else}
        <EmptyState title="Loading terminal settings">
          {#snippet icon()}
            <Spinner size={18} label="Loading terminal settings" />
          {/snippet}
        </EmptyState>
      {/if}
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
