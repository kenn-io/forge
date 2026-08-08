<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { Effect } from "effect";
  import {
    Button,
    EmptyState,
    FlashBanner,
    Spinner,
  } from "@kenn-io/kit-ui";
  import WorkspaceRightSidebar from "../workspace/WorkspaceRightSidebar.svelte";
  import type { StoreInstances } from "../../types.js";

  import { getStores } from "../../context.js";
  import {
    StartupWorkflow,
    startupErrorMessage,
    type StartupError,
  } from "../../app/startup-workflow.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    getBasePath,
    getRoute,
  } from "../../stores/router.svelte.ts";
  import {
    cleanupTheme,
    initTheme,
    reapplyTheme,
  } from "../../stores/theme.svelte.js";
  import {
    initWorkspaceBridge,
  } from "../../stores/embed-config.svelte.js";
  import SessionTerminalPool from "./SessionTerminalPool.svelte";
  import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";
  import WorkspaceEmbedEmptyState from "./WorkspaceEmbedEmptyState.svelte";
  import WorkspaceFirstRunPanel from "./WorkspaceFirstRunPanel.svelte";
  import WorkspaceProjectCard from "./WorkspaceProjectCard.svelte";
  import {
    beginTerminalSettingsHydration,
    hydrateTerminalSettings,
  } from "../../stores/terminal-settings-persistence.js";
  import { showFlash } from "../../stores/flash.svelte.js";

  const stores: StoreInstances = getStores();
  let terminalSettingsReady = $state(false);
  let terminalSettingsError = $state<string | null>(null);
  const runtime = getAppRuntime();
  let interruptSettingsLoad = () => {};
  const r = $derived(getRoute());

  onMount(() => {
    initTheme();
    initWorkspaceBridge();
    return () => {
      cleanupTheme();
    };
  });

  $effect(() => {
    reapplyTheme();
  });

  $effect(() => {
    untrack(() => loadTerminalSettings(stores));
    return () => {
      interruptSettingsLoad();
      interruptSettingsLoad = () => {};
    };
  });

  function loadTerminalSettings(activeStores: StoreInstances, refresh = false): void {
    interruptSettingsLoad();
    const terminalHydration = untrack(() =>
      beginTerminalSettingsHydration(activeStores.settings)
    );
    terminalSettingsReady = false;
    terminalSettingsError = null;
    const program = Effect.gen(function* () {
      const startup = yield* StartupWorkflow;
      if (refresh) yield* startup.invalidate;
      const settings = yield* startup.start;
      yield* Effect.sync(() => {
        hydrateTerminalSettings(terminalHydration, settings.terminal);
        terminalSettingsReady = true;
      });
    });
    const execution = runtime.runCommand(program, {
      operation: "load embedded terminal settings",
      safeContext: {},
      onFailure: (failure: StartupError) => {
        const detail = startupErrorMessage(failure);
        terminalSettingsError = detail;
        showFlash(`Couldn't load terminal settings: ${detail}`, {
          tone: "danger",
        });
      },
    });
    interruptSettingsLoad = execution.interrupt;
  }

  function retryTerminalSettings(): void {
    loadTerminalSettings(stores, true);
  }
</script>

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
