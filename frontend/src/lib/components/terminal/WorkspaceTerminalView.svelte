<script lang="ts">
  import { EmptyState, IconButton, Spinner } from "@kenn-io/kit-ui";
  import PlayIcon from "@lucide/svelte/icons/play";
  import { onDestroy, tick, untrack } from "svelte";
  import { navigate } from "../../stores/router.svelte.ts";
  import { isNarrow } from "../../stores/container.svelte.js";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";
  import KataWorkspaceSidebarPane from "./KataWorkspaceSidebarPane.svelte";
  import SessionTerminalSlot from "./SessionTerminalSlot.svelte";
  import Modal from "../shared/Modal.svelte";
  import ConfirmDialog from "../shared/ConfirmDialog.svelte";
  import DialogButton from "../shared/DialogButton.svelte";
  import WorkspaceHome from "./WorkspaceHome.svelte";
  import WorkspaceLauncherOverlay from "./WorkspaceLauncherOverlay.svelte";
  import LaunchMenu from "./LaunchMenu.svelte";
  import TerminalOptionsMenu from "./TerminalOptionsMenu.svelte";
  import TerminalZoomControl from "./TerminalZoomControl.svelte";
  import DockedTerminalPanel from "./DockedTerminalPanel.svelte";
  import WorkflowSplitTree, {
    type WorkflowTabDescriptor,
  } from "./WorkflowSplitTree.svelte";
  import WorkflowPresetMenu from "./WorkflowPresetMenu.svelte";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import type {
    LaunchTarget,
    RuntimeSession,
  } from "@middleman/ui/api/types";
  import {
    getWorkspaceRuntime,
    launchWorkspaceSession,
    renameWorkspaceSession,
    stopWorkspaceSession,
    workspaceSessionWebSocketPath,
    type WorkspaceRuntimeState,
  } from "../../api/workspace-runtime.js";
  import {
    mountedSessions,
    noteSessionMounted,
    noteSessionUnmounted,
    onSessionExited,
    sessionHostKey,
    sessionHostPrefix,
    type MountedSession,
    type SessionHostKey,
  } from "../../stores/session-host.svelte.ts";
  import {
    publishHostedSessions,
    registerWorkspaceControls,
    registerWorkspaceLauncher,
    setWorkspaceControlsBusy,
  } from "../../stores/workspace-host.svelte.ts";
  import {
    beginWorkspaceSwitch,
    cancelWorkspaceSwitch,
    recordWorkspaceSwitchPhase,
  } from "../../instrumentation/workspaceSwitchTiming.js";
  import {
    activateWorkflowTab,
    addTerminalGroup,
    appendWorkflowTabToLeaf,
    activeTerminalGroup,
    clampTerminalHeight,
    closeSessionInTerminalGroups,
    collectSessionKeys,
    createTerminalGroup,
    defaultTerminalLayout,
    findLeafBySession,
    firstLeaf,
    moveWorkflowTabBefore,
    normalizeTerminalLayout,
    normalizeWorkflowTree,
    parseTerminalLayout,
    pruneTree,
    pruneWorkflowTreeToAvailable,
    splitPane,
    splitSessionIntoPane,
    splitWorkflowTabIntoLeaf,
    terminalGroupForSession,
    updateSplitRatio,
    updateTerminalGroupTree,
    updateWorkflowSplitRatio,
    type PaneNode,
    type SessionRegion,
    type SplitDirection,
    type TerminalGroup,
    type TerminalDock,
    type TerminalLayoutState,
    type WorkflowTabKey,
  } from "./terminal-layout";
  import {
    mapWorkflowNodeSessionKeys,
    type WorkflowPreset,
    type WorkflowPresetSession,
  } from "./workflow-presets";
  import {
    clearActiveTerminalDrag,
    readRuntimeSessionDrag,
  } from "./terminal-drag";
  import { shouldRetryFleetDiffWatch } from "./fleet-diff-watch.js";
  import { Button, CollapsibleSidebar,
    getPaneLayoutStore,
    getStores,
    parseSessionPaneKey,
    promoteSessionBesideWorkspace,
    sessionPaneKey,
    sessionPaneKeyMatchesWorkspace,
    SplitResizeHandle,
    WorkspaceRightSidebar,
    type InlineDockMode,
    type PaneSurfaceKey,
    type SplitResizeEvent,
    type WorkspaceItemIdentity, } from "@middleman/ui";
  import { getStackDepth } from "@middleman/ui/stores/keyboard/modal-stack";
  import ChevronsDownIcon from "@lucide/svelte/icons/chevrons-down";
  import ChevronsUpIcon from "@lucide/svelte/icons/chevrons-up";
  import PanelBottomCloseIcon from "@lucide/svelte/icons/panel-bottom-close";
  import {
    AlertIcon,
    RefreshIcon,
  } from "../../icons.ts";
  import { apiErrorMessage, client } from "../../api/runtime.js";
  import { updateSettings } from "../../api/settings.js";
  import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
  import { showFlash } from "@middleman/ui/stores/flash";
  import {
    consumeWorkspaceLaunch,
    pendingWorkspaceLaunchTarget,
  } from "@middleman/ui/stores/workspace-create-pending";
  import { createTerminalZoomController } from "./terminalZoom";

  interface Workspace {
    id: string;
    platform_host: string;
    repo_owner: string;
    repo_name: string;
    repo: {
      provider: string;
      platform_host?: string | undefined;
      owner: string;
      name: string;
      repo_path: string;
    };
    item_type: "pull_request" | "issue" | "kata_task" | "adhoc";
    item_number: number;
    item_key?: string | undefined;
    git_head_ref: string;
    worktree_path: string;
    tmux_session: string;
    status: string;
    enrichment_status: string;
    error_message?: string | null;
    created_at: string;
    mr_title?: string | null;
    mr_state?: string | null;
    mr_is_draft?: boolean | null;
    associated_pr_number?: number | null;
    mr_head_repo_kind?: string | null;
    kata?: KataWorkspaceMetadata | null;
    fleet_host_key?: string;
  }

  interface ClosedRuntimeSession {
    workspaceId: string;
    key: string;
    createdAt: string;
  }

  // hideWorkspaceList / hideRightSidebar let an embedding host
  // render only the terminal/home/empty surface and compose the
  // workspace list and per-item detail sidebar separately via
  // the /workspaces/embed/list and /workspaces/embed/detail
  // routes. Both default to false to preserve the standalone
  // /workspaces and /terminal/{id} layout.
  interface Props {
    workspaceId: string;
    workspaceHostKey?: string | undefined;
    isSidebarCollapsed?: boolean;
    sidebarWidth?: number | undefined;
    onSidebarResize?: ((width: number) => void) | undefined;
    isSidebarToggleEnabled?: boolean;
    onToggleSidebar?: (() => void) | undefined;
    hideWorkspaceList?: boolean;
    hideRightSidebar?: boolean;
    // False while this instance is parked in a hidden host (Plan 2b):
    // every dialog unmounts (its state persists for reopen) and every
    // TerminalPane deactivates. Defaults to true so standalone/embedded
    // usage is unaffected.
    hostVisible?: boolean;
    // Reports a successful delete (normal or forced) of the given
    // workspace ID, regardless of whether this instance was on a
    // terminal route at the time. Lets a hosting shell (e.g. an inline
    // claimant) react even when the delete doesn't trigger navigation.
    onWorkspaceDeleted?: (workspaceId: string, hostKey?: string, identity?: WorkspaceItemIdentity) => void;
    // Set only when this instance is embedded in an inline dock slot
    // (activity/prs/issues), not the Workspaces tab or standalone route.
    // Backs the toolbar's expand/show-details/collapse controls, which
    // replace the inline dock's own removed header bar.
    inlineDock?: { getMode(): InlineDockMode; setMode(mode: InlineDockMode): void } | null;
    // The detail surface this instance is embedded in, if any. Its pane layout is
    // the only record of which of this workspace's sessions have been promoted
    // out of here into a top-level pane, so it is also what tells this view which
    // sessions NOT to render. Unset on the Workspaces tab and the embed routes,
    // which have no detail panes: a session promoted in one surface is still at
    // home in every other place the workspace is shown.
    paneSurface?: PaneSurfaceKey | undefined;
    terminalSettingsReady?: boolean;
  }

  const {
    workspaceId,
    workspaceHostKey = undefined,
    isSidebarCollapsed = false,
    sidebarWidth: externalWorkspaceListWidth = undefined,
    onSidebarResize = undefined,
    isSidebarToggleEnabled = false,
    onToggleSidebar = undefined,
    hideWorkspaceList = false,
    hideRightSidebar = false,
    hostVisible = true,
    onWorkspaceDeleted = undefined,
    inlineDock = null,
    paneSurface = undefined,
    terminalSettingsReady = true,
  }: Props = $props();

  const basePath = (
    window.__BASE_PATH__ ?? "/"
  ).replace(/\/$/, "");
  const { settings: settingsStore } = getStores();
  // Launcher, controls, and pending-write state is keyed by workspace identity
  // rather than held as bare flags: one embedded view serves every selection on its
  // surface, so a switch would otherwise inherit the previous workspace's open
  // overlay and its in-flight writes.
  const viewWorkspaceKey = $derived(`${workspaceId}\u0000${workspaceHostKey ?? ""}`);
  // Each pending write remembers the workspace it started for. An operation's
  // completion can land after the user moved on, and a bare boolean would then
  // report the NEXT workspace's controls busy - disabling them and pinning their
  // popover open with nothing left to clear it.
  let terminalZoomSavingFor = $state<string | null>(null);
  let terminalOptionsSavingFor = $state<string | null>(null);
  const terminalZoomSaving = $derived(terminalZoomSavingFor === viewWorkspaceKey);
  const terminalOptionsSaving = $derived(terminalOptionsSavingFor === viewWorkspaceKey);
  const terminalZoom = createTerminalZoomController({
    store: settingsStore,
    persist: async (terminal) => (await updateSettings({ terminal })).terminal,
    reportError: (error) => {
      const detail = error instanceof Error ? error.message : "Unknown error";
      showFlash(`Couldn't save terminal font size: ${detail}`, {
        tone: "danger",
      });
    },
    onPendingChange: (pending) => {
      // Single-flight per view, so "no longer pending" is absolute: clearing the
      // owner outright is what stops a save that finished after a switch from
      // leaving its originating workspace flagged forever.
      terminalZoomSavingFor = pending ? viewWorkspaceKey : null;
    },
  });
  const terminalFontSize = $derived(
    settingsStore.getTerminalFontSize(),
  );

  let workspace = $state<Workspace | null>(null);
  let runtime = $state.raw<WorkspaceRuntimeState | null>(null);
  let appliedRuntimeState:
    | {
        workspaceId: string;
        hostKey: string | undefined;
        fingerprint: string;
      }
    | null = null;
  let runtimeFetchSeq = 0;
  let runtimeFetchInFlight:
    | Promise<WorkspaceRuntimeState | null>
    | null = null;
  let runtimeFetchInFlightId = "";
  let runtimeFetchInFlightHostKey:
    | string
    | undefined = undefined;
  // The workspace ID that `runtime` was fetched for. Stored
  // alongside the payload so we never render or operate on
  // sessions/targets that belong to a previous workspace
  // (during the in-place transition between workspaces, runtime
  // briefly outlives the workspace it was fetched for).
  let runtimeForId = $state<string>("");
  let runtimeForHostKey = $state<string | undefined>(undefined);
  let loadError = $state<string | null>(null);
  let retryingSetup = $state(false);
  let refreshingWorkspace = $state(false);
  type DeletingWorkspaceTarget = {
    id: string;
    hostKey: string | undefined;
  };
  let deletingWorkspaceTargets = $state<DeletingWorkspaceTarget[]>([]);
  let sidebarRefreshToken = $state(0);
  let diffRefreshToken = $state(0);
  let lastDiffSnapshotVersion = "";
  let forcePromptMessage = $state<string | null>(null);
  let forcePromptForId = $state<string | null>(null);
  // Identity snapshot captured when the 409 arrived, while the loaded
  // envelope still described the delete target: the user can switch
  // workspaces before confirming the force delete, after which the live
  // envelope belongs to someone else.
  let forcePromptIdentity: WorkspaceItemIdentity | undefined;
  let forceDeleting = $state(false);
  let stopPromptSession = $state<RuntimeSession | null>(null);
  let stopSessionStopping = $state(false);
  let renamePrompt = $state<{
    sessionKey: string;
    originalLabel: string;
  } | null>(null);
  let renameInputValue = $state("");
  let renameSaving = $state(false);
  let renameInputEl = $state<HTMLInputElement | null>(null);
  // Bumps on every workspace route change so async callbacks can detect
  // stale, non-destructive responses from a previous visit. Delete targets
  // remain blocked across A -> B -> A navigation, while a successful delete
  // still navigates away if the user returned to the destroyed workspace.
  let workspaceGen = 0;
  onDestroy(() => {
    workspaceGen += 1;
  });
  let runtimeError = $state<string | null>(null);
  let pollTimer = $state<ReturnType<
    typeof setInterval
  > | null>(null);
  let runtimePollTimer = $state<ReturnType<
    typeof setInterval
  > | null>(null);
  let eventSource = $state<EventSource | null>(null);
  let activeTabKey = $state<WorkflowTabKey>("home");
  let mountedSessionKeys = $state<string[]>([]);
  let closedSessions = $state<ClosedRuntimeSession[]>([]);
  let launchingKey = $state<string | null>(null);
  let terminalLayout = $state<TerminalLayoutState>(
    defaultTerminalLayout(),
  );
  let terminalLayoutWorkspaceId = $state("");
  let terminalLaunching = $state(false);
  let workspaceListState = $state<{
    status: "loading" | "retrying" | "loaded";
    total: number;
  }>({ status: "loading", total: 0 });

  const SIDEBAR_TAB_KEY = "middleman-workspace-sidebar-tab";
  const SIDEBAR_OPEN_KEY = "middleman-workspace-sidebar-open";
  const SIDEBAR_WIDTH_KEY = "middleman-workspace-sidebar-width";
  const WORKSPACE_LIST_WIDTH_KEY =
    "middleman-workspace-list-sidebar-width";
  const ACTIVE_WORKSPACE_TAB_KEY_PREFIX =
    "middleman-workspace-active-tab:";
  const TERMINAL_LAYOUT_KEY_PREFIX =
    "middleman-workspace-terminal-layout:";
  const WORKFLOW_PRESETS_KEY = "middleman-workspace-layout-presets";
  const PLAIN_SHELL_TARGET = "plain_shell";
  type EmptyLaunchTargetsState = "idle" | "loading" | "loaded" | "error";
  let emptyLaunchTargets = $state.raw<LaunchTarget[]>([]);
  let emptyLaunchTargetsState = $state<EmptyLaunchTargetsState>("idle");

  let workflowPresets = $state<WorkflowPreset[]>(loadWorkflowPresets());
  let selectedWorkflowPresetId = $state<string | null>(null);
  let applyingWorkflowPresetFor = $state<string | null>(null);
  const applyingWorkflowPreset = $derived(applyingWorkflowPresetFor === viewWorkspaceKey);

  type SidebarTab = "diff" | "pr" | "issue" | "reviews" | "kata_task";

  const MIN_WORKSPACE_LIST_WIDTH = 220;
  const DEFAULT_WORKSPACE_LIST_WIDTH = 260;
  const MAX_WORKSPACE_LIST_WIDTH = 420;

  function clampWorkspaceListWidth(
    value: number,
  ): number {
    return Math.max(
      MIN_WORKSPACE_LIST_WIDTH,
      Math.min(
        MAX_WORKSPACE_LIST_WIDTH,
        Math.round(value),
      ),
    );
  }

  function updateWorkspaceListState(
    state: { status: "loading" | "retrying" | "loaded"; total: number },
  ): void {
    workspaceListState = state;
  }

  async function loadEmptyLaunchTargets(): Promise<void> {
    if (
      emptyLaunchTargetsState === "loaded" ||
      emptyLaunchTargetsState === "loading"
    ) {
      return;
    }
    emptyLaunchTargetsState = "loading";
    try {
      const { data } = await client.GET("/settings");
      emptyLaunchTargets = data?.launch_targets ?? [];
      emptyLaunchTargetsState = "loaded";
    } catch {
      emptyLaunchTargets = [];
      emptyLaunchTargetsState = "error";
    }
  }

  function readLocalStorage(key: string): string | null {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  }

  function writeLocalStorage(key: string, value: string): void {
    try {
      localStorage.setItem(key, value);
    } catch {
      // Best-effort UI preference persistence; keep the in-memory state.
    }
  }

  function loadWorkspaceListWidth(): number {
    const value = parseInt(
      readLocalStorage(WORKSPACE_LIST_WIDTH_KEY) ?? "",
      10,
    );
    return Number.isFinite(value)
      ? clampWorkspaceListWidth(value)
      : DEFAULT_WORKSPACE_LIST_WIDTH;
  }

  function loadSidebarTab(): SidebarTab {
    const v = readLocalStorage(SIDEBAR_TAB_KEY);
    if (v === "diff") return "diff";
    if (v === "pr") return "pr";
    if (v === "issue") return "issue";
    if (v === "reviews") return "reviews";
    return "diff";
  }

  function loadSidebarOpen(): boolean {
    return readLocalStorage(SIDEBAR_OPEN_KEY) === "true";
  }

  const MIN_SIDEBAR_WIDTH = 280;
  const MIN_TERMINAL_WIDTH = 300;
  const DEFAULT_SIDEBAR_WIDTH = 640;
  const RIGHT_SIDEBAR_RESIZE_HANDLE_WIDTH = 4;

  function loadSidebarWidth(): number {
    const v = parseInt(
      readLocalStorage(SIDEBAR_WIDTH_KEY) ?? "",
      10,
    );
    return Number.isFinite(v)
      ? Math.max(MIN_SIDEBAR_WIDTH, v)
      : DEFAULT_SIDEBAR_WIDTH;
  }

  let sidebarTab = $state<SidebarTab>(loadSidebarTab());
  let sidebarOpen = $state(loadSidebarOpen());
  let sidebarWidth = $state(loadSidebarWidth());
  let workspaceListWidth = $state(loadWorkspaceListWidth());
  const currentWorkspaceListWidth = $derived(
    clampWorkspaceListWidth(
      externalWorkspaceListWidth ?? workspaceListWidth,
    ),
  );

  // Runtime is only "live" when both the runtime fetch and the
  // workspace fetch resolve for the current route. Without the
  // workspace.id check, a runtime that lands first for the new
  // workspace can render its sessions/launch targets next to the
  // previous workspace's still-cached header/home data.
  const workspaceLive = $derived(
    workspace?.id === workspaceId && selectedWorkspaceHostKey(workspace) === workspaceHostKey,
  );
  const runtimeLive = $derived(
    runtime !== null && runtimeForId === workspaceId && runtimeForHostKey === workspaceHostKey && workspaceLive,
  );
  const workspaceDetailsReady = $derived(workspaceLive && (runtimeLive || runtimeError !== null));

  function hasAppliedRuntimeFor(
    id: string,
    hostKey: string | undefined,
  ): boolean {
    return (
      appliedRuntimeState?.workspaceId === id &&
      appliedRuntimeState.hostKey === hostKey
    );
  }

  function invalidateRuntimeSnapshot(): void {
    runtimeFetchSeq += 1;
    runtimeFetchInFlight = null;
    runtimeFetchInFlightId = "";
    runtimeFetchInFlightHostKey = undefined;
    appliedRuntimeState = null;
  }
  const runtimeSessions = $derived(
    runtimeLive
      ? (runtime?.sessions ?? []).filter(
          (session) =>
            !closedSessions.some((closed) =>
              sessionGenerationMatches(closed, session),
            ),
        )
      : [],
  );
  const launchTargets = $derived(
    runtimeLive ? (runtime?.launch_targets ?? []) : [],
  );
  const sessionDisplayLabels = $derived.by(() => {
    const labels: Record<string, string> = {};
    for (const session of runtimeSessions) {
      labels[session.key] = session.label;
    }
    return labels;
  });
  // The surface this view is embedded in, whose stored pane tree is the sole
  // record of which sessions have been promoted out of this container.
  const surfaceLayout = $derived(paneSurface ? getPaneLayoutStore(paneSurface) : null);

  function sessionPaneKeyFor(session: RuntimeSession): string {
    return sessionPaneKey(workspaceId, workspaceHostKey, session.key);
  }

  const promotedSessionKeys = $derived(
    new Set(
      surfaceLayout === null
        ? []
        : runtimeSessions
            .filter((session) => surfaceLayout.hasTab(sessionPaneKeyFor(session)))
            .map((session) => session.key),
    ),
  );

  function isPromoted(session: RuntimeSession): boolean {
    return promotedSessionKeys.has(session.key);
  }

  // The two directions of the cross-tree drag: a session tab carries its pane key
  // out, and a promoted pane's key resolves back to the tab it belongs to. Both
  // reject anything that is not a live session of THIS workspace on this host — a
  // session key is unique only within one workspace.
  const workflowPromotion = $derived(
    surfaceLayout === null
      ? undefined
      : {
          paneKeyFor: (tabKey: WorkflowTabKey) => {
            const sessionKey = sessionKeyFromWorkflowTab(tabKey);
            if (sessionKey === null) return null;
            const session = runtimeSessions.find((candidate) => candidate.key === sessionKey);
            return session ? sessionPaneKeyFor(session) : null;
          },
          tabKeyFor: (paneKey: string) => {
            if (!sessionPaneKeyMatchesWorkspace(paneKey, workspaceId, workspaceHostKey)) return null;
            const sessionKey = parseSessionPaneKey(paneKey)?.sessionKey ?? null;
            if (sessionKey === null) return null;
            if (!runtimeSessions.some((candidate) => candidate.key === sessionKey)) return null;
            return workflowTabKeyForSession(sessionKey);
          },
        },
  );

  // Publish what a detail surface may promote. Only this view knows the runtime,
  // the display labels, and each session's generation, and the registry key needs
  // the generation so a relaunched session is not handed the dead one's terminal.
  $effect(() => {
    const sessions = runtimeSessions.map((session) => ({
      paneKey: sessionPaneKeyFor(session),
      label: sessionDisplayLabels[session.key] ?? session.label,
      hostKey: sessionHostKeyFor(session),
      active: session.key === currentSessionKey,
    }));
    const key = { workspaceId, hostKey: workspaceHostKey };
    untrack(() => publishHostedSessions(key, sessions));
  });

  // A detail pane trades the Home tab for the launcher overlay. Keyed off the pane
  // surface rather than the flatten width: unlike the controls, the overlay is a
  // modal and needs no chrome of its own to live in.
  const launcherMode = $derived(paneSurface !== undefined);
  // `auto` records who opened it: an overlay the view raised over an empty pane is
  // its own to take back once there is something to show, while one the user asked
  // for stays until they dismiss it - they may be picking a second session.
  let launcherState = $state<{ workspaceKey: string; auto: boolean } | null>(null);
  const launcherOpen = $derived(launcherState?.workspaceKey === viewWorkspaceKey);
  // Which workspaces the overlay has auto-opened for, so selecting the same item
  // twice does not reopen a launcher the user dismissed, while a different workspace
  // with no session still gets one. A list rather than a single slot: A, then B,
  // then back to A must not reopen A's launcher.
  let launcherAutoOpenedFor = $state<string[]>([]);

  function openLauncher(): void {
    if (!launcherMode) {
      selectWorkspaceTab("home");
      return;
    }
    launcherState = { workspaceKey: viewWorkspaceKey, auto: false };
  }

  /**
   * The view's own fallback when a pane has nothing left to render.
   *
   * Once per workspace, for every automatic path: the empty pane on arrival and the
   * one left behind when the last session goes away are the same situation, and a
   * launcher that came back each time would trap a user who dismissed it - revisit
   * the item, close a session, and it is in the way again. Their own route back is
   * the Launch button in the pane's controls.
   */
  function autoOpenLauncher(): void {
    if (launcherAutoOpenedFor.includes(viewWorkspaceKey)) return;
    launcherAutoOpenedFor = [...launcherAutoOpenedFor, viewWorkspaceKey];
    launcherState = { workspaceKey: viewWorkspaceKey, auto: true };
  }

  function closeLauncher(): void {
    launcherState = null;
  }

  /**
   * Where to go when the tab the user was on disappears - a session stopped, the
   * terminal panel closed, a tab moved to the dock.
   *
   * Home is that place outside a pane. Inside one there is no Home, so it is
   * whatever workflow tab is left, and the launcher when the workspace has nothing
   * left to show: a pane rendering an empty strip is a dead end.
   */
  function selectFallbackTab(): void {
    if (!launcherMode) {
      selectWorkspaceTab("home");
      return;
    }
    const next = workflowTabDescriptors.find((tab) => tab.key !== activeTabKey);
    if (next !== undefined) {
      selectWorkspaceTab(next.key);
      return;
    }
    // The dock counts as something to show: its sessions are not workflow tabs, so
    // a workspace whose only terminal is docked has an empty strip and nothing
    // missing.
    if (runtimeSessions.length === 0) autoOpenLauncher();
  }

  // Reachable from outside the view: the palette command, and a Focus Terminal that
  // finds no session to focus. Only while embedded, since that is the only mode with
  // an overlay to open.
  $effect(() => {
    if (!launcherMode) return;
    untrack(() => registerWorkspaceLauncher(openLauncher));
    return () => untrack(() => registerWorkspaceLauncher(null));
  });

  // A pane whose only tab was Home has nothing to show, and a remembered Home tab
  // names a tab that no longer exists here. Both resolve the same way: show whatever
  // session is there, and open the launcher when there is none.
  $effect(() => {
    if (!launcherMode || !runtimeLive) return;
    const tabs = workflowTabDescriptors;
    const activeMissing = !tabs.some((tab) => tab.key === activeTabKey);
    const workspaceKey = viewWorkspaceKey;
    const anySession = runtimeSessions.length > 0;
    const openState = launcherState;
    const autoOpened = launcherAutoOpenedFor.includes(workspaceKey);
    untrack(() => {
      if (tabs.length > 0 && activeMissing) selectWorkspaceTab(tabs[0]!.key);
      // A docked terminal is not a workflow tab but is very much on screen, so an
      // empty strip alone does not mean the workspace has nothing to show.
      if (tabs.length > 0 || anySession) {
        // Take back what the view opened, and only that: a reconnect, a relaunch,
        // or a first runtime load that lands before its sessions do all report zero
        // sessions for a moment, and an auto-opened launcher left over that gap
        // would then cover the terminal it was standing in for.
        if (openState?.workspaceKey === workspaceKey && openState.auto) closeLauncher();
        return;
      }
      // Once per workspace: reopening a launcher the user dismissed would trap them
      // in it, while a different session-less workspace still gets one.
      if (openState?.workspaceKey === workspaceKey || autoOpened) return;
      autoOpenLauncher();
    });
  });

  // Below the flatten width a detail surface shows one tab strip for every pane and
  // suppresses per-leaf chrome, so there is nowhere to hang the controls button and
  // the toolbar is the only thing left that can carry them.
  const paneFlattened = $derived(surfaceLayout?.paneRender()?.flattened ?? false);
  const controlsInPane = $derived(paneSurface !== undefined && !paneFlattened);

  // Handed to the detail pane's controls popover, which is where the controls live
  // once this view is embedded. Registered with the workspace it acts on, because
  // one embedded view serves every selection on its surface: the snippet is the
  // same object after a switch, so only the key can tell a popover that its
  // subject changed.
  $effect(() => {
    if (!controlsInPane) return;
    const workspaceKey = viewWorkspaceKey;
    // Untracked because the store compares against what is already registered:
    // reading that inside a tracked effect that also writes it is the read-write
    // loop Svelte aborts with effect_update_depth_exceeded.
    untrack(() => registerWorkspaceControls({ snippet: workspaceControls, workspaceKey }));
    return () => untrack(() => registerWorkspaceControls(null));
  });

  // Reported against the workspace the controls act on, and released on the way out:
  // a write started for one workspace only clears its pending flag while that
  // workspace is still current, so an unkeyed flag could stay set after a switch and
  // pin the next workspace's popover open for good.
  $effect(() => {
    const workspaceKey = viewWorkspaceKey;
    const busy = terminalOptionsSaving || terminalZoomSaving || applyingWorkflowPreset;
    untrack(() => setWorkspaceControlsBusy(workspaceKey, controlsInPane && busy));
    return () => untrack(() => setWorkspaceControlsBusy(workspaceKey, false));
  });

  // The session a keyboard promote acts on: whichever one the user is looking at.
  // A workflow tab wins because it fills the pane, and the dock's active tab only
  // counts while the dock is open - a collapsed dock shows no terminal at all.
  const currentSessionKey = $derived(
    sessionKeyFromWorkflowTab(activeTabKey) ??
      (terminalLayout.open ? terminalLayout.activeSessionKey : null),
  );

  // Promotion implies mounted. Demotion hands the session back to the workflow
  // region, whose slot only renders for a mounted session, so a session promoted
  // without ever having been opened here would go dark the instant it came home -
  // its pool entry dropped in the same flush that gave it a tab.
  $effect(() => {
    const promoted = [...promotedSessionKeys];
    untrack(() => {
      for (const sessionKey of promoted) mountSessionTerminal(sessionKey);
    });
  });

  /**
   * The dock's own way out: give this session a pane of its own on the surface
   * hosting the workspace. Undefined when nothing is hosting one, which is what
   * keeps the control off the standalone tab and the embed routes.
   */
  const promoteSessionToPane = $derived(
    surfaceLayout === null
      ? undefined
      : (sessionKey: string) => {
          const session = runtimeSessions.find((candidate) => candidate.key === sessionKey);
          if (!session) return;
          promoteSessionBesideWorkspace(surfaceLayout, sessionPaneKeyFor(session));
        },
  );

  /** Bring a promoted session home before a container edit places it. */
  function demoteWorkflowTab(tabKey: WorkflowTabKey): void {
    const sessionKey = sessionKeyFromWorkflowTab(tabKey);
    if (sessionKey === null) return;
    const session = runtimeSessions.find((candidate) => candidate.key === sessionKey);
    if (!session || !isPromoted(session)) return;
    surfaceLayout?.demoteTab(sessionPaneKeyFor(session));
  }

  const terminalSessions = $derived(
    runtimeSessions.filter(
      (session) => sessionRegion(session) === "terminal" && !isPromoted(session),
    ),
  );

  // Masked, not pruned: the stored trees keep a promoted session's tab and leaf so
  // demotion hands back the placement the user chose, rather than dropping it back
  // wherever normalization would put a session it has never seen.
  const dockTree = $derived(
    promotedSessionKeys.size === 0
      ? terminalLayout.tree
      : pruneTree(
          terminalLayout.tree,
          terminalSessions.map((session) => session.key),
        ),
  );

  function upsertRuntimeSession(session: RuntimeSession): RuntimeSession[] {
    const sessions = [
      ...runtimeSessions.filter((candidate) => candidate.key !== session.key),
      session,
    ];
    if (runtimeLive && runtime) {
      invalidateRuntimeSnapshot();
      runtime = {
        ...runtime,
        sessions: [
          ...runtime.sessions.filter((candidate) => candidate.key !== session.key),
          session,
        ],
      };
    }
    return sessions;
  }
  const currentTerminalGroup = $derived(activeTerminalGroup(terminalLayout));
  const workflowSessions = $derived(
    runtimeSessions.filter(
      (session) => sessionRegion(session) === "workflow" && !isPromoted(session),
    ),
  );
  function workflowSessionStatus(
    session: RuntimeSession,
    label: string,
  ): WorkflowTabDescriptor["status"] {
    if (session.status === "running") {
      return { value: "idle", label: `${label} running` };
    }
    if (session.status === "starting") {
      return { value: "stale", label: `${label} starting` };
    }
    if (session.status === "error") {
      return { value: "unclean", label: `${label} unavailable` };
    }
    return undefined;
  }

  const workflowTabDescriptors = $derived.by<WorkflowTabDescriptor[]>(() => {
    // No Home tab inside a detail pane: the workspace gets one pane there, and
    // spending half its height on a surface only used to start something is the
    // trade this mode exists to undo. The launcher overlay replaces it.
    const tabs: WorkflowTabDescriptor[] = launcherMode
      ? []
      : [
          {
            key: "home",
            label: "Home",
            kind: "home",
          },
        ];
    if (
      terminalLayout.dock === "top" &&
      (terminalLayout.open || terminalSessions.length > 0)
    ) {
      tabs.push({
        key: "terminal",
        label: "Terminal",
        kind: "terminal",
        closable: true,
      });
    }
    for (const session of workflowSessions) {
      const label = sessionDisplayLabels[session.key] ?? session.label;
      tabs.push({
        key: workflowTabKeyForSession(session.key),
        label,
        kind: session.kind === "plain_shell" ? "plain_shell" : "agent",
        status: workflowSessionStatus(session, label),
        renamable: true,
        movableToTerminal: true,
        closable: true,
      });
    }
    return tabs;
  });
  const renderedWorkflowTree = $derived(
    promotedSessionKeys.size === 0 && !launcherMode
      ? terminalLayout.workflowTree
      : pruneWorkflowTreeToAvailable(
          // Embedded too, not just when something is promoted: the stored tree still
          // names Home, and a leaf whose only tab has no descriptor renders a strip
          // with nothing in it.
          terminalLayout.workflowTree,
          workflowTabDescriptors.map((tab) => tab.key),
        ),
  );

  const terminalPanelInStage = $derived(
    terminalLayout.open && terminalLayout.dock === "top",
  );

  // While `workspaceId` has moved on but the previous workspace's
  // data is still on screen (the in-place transition), mutating
  // actions must not run — they would target the new id while the
  // user is looking at the old one. The window is small (≤ a few
  // hundred ms) but observable, so guard every action handler with
  // this and disable the buttons.
  const transitioning = $derived(
    workspaceId !== "" &&
      workspace !== null &&
      (workspace.id !== workspaceId ||
        workspaceHostKey !== selectedWorkspaceHostKey(workspace)),
  );
  const deletingSelectedWorkspace = $derived(
    deletingWorkspaceTargets.some((target) =>
      isDeletingWorkspaceTarget(target, workspaceId, workspaceHostKey),
    ),
  );
  const actionsBlocked = $derived(transitioning || deletingSelectedWorkspace || forceDeleting);
  const inlineDockMode = $derived(inlineDock?.getMode() ?? null);
  const inlineDockExpandBlocked = $derived(getStackDepth() > 0);
  const modalOpen = $derived(
    forcePromptMessage !== null ||
      stopPromptSession !== null ||
      renamePrompt !== null,
  );

  $effect(() => {
    writeLocalStorage(SIDEBAR_TAB_KEY, sidebarTab);
  });
  $effect(() => {
    writeLocalStorage(
      SIDEBAR_OPEN_KEY,
      String(sidebarOpen),
    );
  });
  $effect(() => {
    writeLocalStorage(
      SIDEBAR_WIDTH_KEY,
      String(sidebarWidth),
    );
  });
  $effect(() => {
    if (externalWorkspaceListWidth !== undefined) return;
    writeLocalStorage(
      WORKSPACE_LIST_WIDTH_KEY,
      String(workspaceListWidth),
    );
  });
  $effect(() => {
    if (!workspaceId) return;
    const storageId = workspaceStorageId(workspaceId, workspaceHostKey);
    if (terminalLayoutWorkspaceId !== storageId) return;
    writeLocalStorage(
      terminalLayoutStorageKey(storageId),
      JSON.stringify(terminalLayout),
    );
  });
  $effect(() => {
    writeLocalStorage(WORKFLOW_PRESETS_KEY, JSON.stringify(workflowPresets));
  });

  function handleSidebarToggleClick(tab: SidebarTab): void {
    if (actionsBlocked) return;
    if (sidebarOpen && sidebarTab === tab) {
      sidebarOpen = false;
    } else {
      sidebarTab = tab;
      sidebarOpen = true;
    }
  }

  function openItemSidebar(
    targetId: string,
    tab: SidebarTab,
    targetHostKey?: string,
  ): void {
    // Cross-workspace click: navigate first, then ensure
    // the sidebar is open for the target tab.
    if (
      targetId !== workspaceId ||
      (targetHostKey ?? undefined) !== workspaceHostKey
    ) {
      sidebarTab = tab;
      sidebarOpen = true;
      if (targetHostKey) {
        navigate(
          `/terminal/fleet/${encodeURIComponent(targetHostKey)}/${encodeURIComponent(targetId)}`,
        );
      } else {
        navigate(`/terminal/${encodeURIComponent(targetId)}`);
      }
      return;
    }

    handleSidebarToggleClick(tab);
  }

  function toggleRightSidebar(): void {
    sidebarOpen = !sidebarOpen;
  }

  function handleWorkspaceListResize(width: number): void {
    const clamped = clampWorkspaceListWidth(width);
    if (onSidebarResize) {
      onSidebarResize(clamped);
    } else {
      workspaceListWidth = clamped;
    }
    requestAnimationFrame(() => {
      if (containerEl) {
        clampRightSidebarWidth(containerEl.clientWidth);
      }
    });
  }

  let containerEl = $state<HTMLElement | null>(null);
  let containerWidth = $state(0);

  function maxRightSidebarWidth(
    containerWidth: number,
  ): number {
    return Math.max(
      0,
      containerWidth -
        MIN_TERMINAL_WIDTH -
        RIGHT_SIDEBAR_RESIZE_HANDLE_WIDTH,
    );
  }

  const rightSidebarAriaMax = $derived(
    containerWidth > 0
      ? maxRightSidebarWidth(containerWidth)
      : Math.max(MIN_SIDEBAR_WIDTH, sidebarWidth),
  );
  const rightSidebarAriaMin = $derived(
    Math.min(MIN_SIDEBAR_WIDTH, rightSidebarAriaMax),
  );

  function clampRightSidebarWidth(
    containerWidth: number,
  ): void {
    const maxW = maxRightSidebarWidth(containerWidth);
    if (sidebarWidth > maxW) {
      sidebarWidth = maxW;
    }
  }

  // Keep the terminal usable when the main layout
  // shrinks, including when the left workspace list
  // is resized after the right sidebar is already open.
  // Gated on hostVisible: a parked host sits in a display:none parking
  // node where clientWidth is 0, and clamping against that zero would
  // collapse (and persist) the sidebar width. hostVisible flipping back
  // on reruns the clamp against real geometry.
  $effect(() => {
    if (!hostVisible || hideRightSidebar) return;
    if (!containerEl || !sidebarOpen) return;

    clampRightSidebarWidth(containerEl.clientWidth);
  });

  $effect(() => {
    if (!hostVisible || hideRightSidebar) return;
    if (!sidebarOpen) return;

    function onResize(): void {
      if (containerEl) {
        clampRightSidebarWidth(containerEl.clientWidth);
      }
    }

    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
    };
  });

  let sidebarResizeStartWidth = 0;
  let sidebarResizeMinWidth = MIN_SIDEBAR_WIDTH;
  let sidebarResizeMaxWidth = 9999;

  function handleSidebarResizeStart(): void {
    sidebarResizeStartWidth = sidebarWidth;
    sidebarResizeMaxWidth = containerEl
      ? maxRightSidebarWidth(containerEl.clientWidth)
      : 9999;
    sidebarResizeMinWidth = Math.min(
      MIN_SIDEBAR_WIDTH,
      sidebarResizeMaxWidth,
    );
  }

  function handleSidebarResize(event: SplitResizeEvent): void {
    sidebarWidth = Math.max(
      sidebarResizeMinWidth,
      Math.min(
        sidebarResizeMaxWidth,
        sidebarResizeStartWidth - event.delta,
      ),
    );
  }

  // Window-level shortcut, so it must not stay registered while this view
  // is parked in a hidden host (it would swallow Cmd/Ctrl+] on unrelated
  // pages) or while the right sidebar isn't rendered at all.
  $effect(() => {
    if (!hostVisible || hideRightSidebar) return;
    function onKeydown(e: KeyboardEvent): void {
      if (
        e.key === "]" &&
        (e.metaKey || e.ctrlKey) &&
        !e.defaultPrevented
      ) {
        e.preventDefault();
        toggleRightSidebar();
      }
    }
    window.addEventListener("keydown", onKeydown);
    return () =>
      window.removeEventListener("keydown", onKeydown);
  });

  function displayName(ws: Workspace): string {
    return ws.mr_title ?? ws.git_head_ref;
  }

  function mountSessionTerminal(sessionKey: string): void {
    if (!mountedSessionKeys.includes(sessionKey)) {
      mountedSessionKeys = [...mountedSessionKeys, sessionKey];
    }
  }

  function unmountSessionTerminal(sessionKey: string): void {
    mountedSessionKeys = mountedSessionKeys.filter(
      (key) => key !== sessionKey,
    );
  }

  function sessionHostKeyFor(session: RuntimeSession): SessionHostKey {
    return sessionHostKey(
      workspaceId,
      workspaceHostKey,
      session.key,
      session.created_at,
    );
  }

  // Prefixes this view has ever mounted terminals under, so moving to another
  // workspace can take the previous one's down. Nothing else would: the pool
  // outlives this view, and every parked terminal holds a live websocket, so
  // browsing ten workspaces would otherwise leave ten attachments open.
  const ownedSessionPrefixes = new Set<string>();

  function releaseOwnedSessions(except?: string): void {
    for (const prefix of [...ownedSessionPrefixes]) {
      if (prefix === except) continue;
      for (const session of mountedSessions()) {
        if (session.hostKey.startsWith(prefix)) noteSessionUnmounted(session.hostKey);
      }
      ownedSessionPrefixes.delete(prefix);
    }
  }

  // Sessions the terminal dock puts on screen: the leaves of its tree, and only
  // while the panel is open. A terminal-region session with no leaf has no
  // terminal today and must not gain one just because the pool could park it.
  const dockedSessionKeys = $derived(
    terminalLayout.open ? new Set(collectSessionKeys(dockTree)) : new Set<string>(),
  );

  // Mirror the sessions this workspace puts on screen into the app-level pool,
  // which owns the live terminals. Both regions render pooled slots, so a shell
  // dragged between the dock and the workflow area keeps its tmux attachment
  // and its scrollback instead of being torn down and reattached.
  //
  // Reconciled from state rather than pushed from each mount/unmount call site:
  // a session changes region without either side calling anything, and a missed
  // noteSessionUnmounted would leave a socket attached to nothing.
  $effect(() => {
    const prefix = sessionHostPrefix(workspaceId, workspaceHostKey);
    const desired = new Map<SessionHostKey, MountedSession>();
    for (const session of runtimeSessions) {
      // A promoted session is on screen in a detail pane, which renders its slot
      // but cannot mount it: only this view knows the websocket path, the
      // generation, and whether actions are blocked.
      const onScreen =
        isPromoted(session) ||
        (sessionRegion(session) === "workflow"
          ? mountedSessionKeys.includes(session.key)
          : dockedSessionKeys.has(session.key));
      if (!onScreen) continue;
      const hostKey = sessionHostKeyFor(session);
      desired.set(hostKey, {
        hostKey,
        websocketPath: workspaceSessionWebSocketPath(
          workspaceId,
          session.key,
          workspaceHostKey,
        ),
        status: session.status,
        disabled: actionsBlocked,
      });
    }
    untrack(() => {
      releaseOwnedSessions(prefix);
      if (desired.size > 0) ownedSessionPrefixes.add(prefix);
      for (const session of desired.values()) noteSessionMounted(session);
      // Only this workspace's entries. Another surface's claimed workspace keeps
      // its parked terminals until it stops being claimed.
      for (const session of mountedSessions()) {
        if (!session.hostKey.startsWith(prefix)) continue;
        if (desired.has(session.hostKey)) continue;
        noteSessionUnmounted(session.hostKey);
      }
    });
  });

  // The pool is app-level, so a destroyed view leaves its terminals running
  // forever unless it hands them back.
  $effect(() => () => untrack(() => releaseOwnedSessions()));

  // Pooled terminals report an exit by key; only this view can map that back to
  // a runtime session, and the generation in the key keeps a relaunched session
  // from being mistaken for the dead one.
  $effect(() =>
    onSessionExited((hostKey) => {
      const session = runtimeSessions.find(
        (candidate) => sessionHostKeyFor(candidate) === hostKey,
      );
      if (session) handleSessionExit(session);
    }),
  );

  function sessionGenerationMatches(
    closed: ClosedRuntimeSession,
    session: RuntimeSession,
  ): boolean {
    return (
      closed.workspaceId === session.workspace_id &&
      closed.key === session.key &&
      closed.createdAt === session.created_at
    );
  }

  function markSessionClosed(session: RuntimeSession): void {
    if (
      !closedSessions.some((closed) =>
        sessionGenerationMatches(closed, session),
      )
    ) {
      closedSessions = [
        ...closedSessions,
        {
          workspaceId: session.workspace_id,
          key: session.key,
          createdAt: session.created_at,
        },
      ];
    }
  }

  function clearClosedSession(session: RuntimeSession): void {
    closedSessions = closedSessions.filter(
      (closed) => !sessionGenerationMatches(closed, session),
    );
  }

  function isSessionTerminalMounted(
    sessionKey: string,
  ): boolean {
    return mountedSessionKeys.includes(sessionKey);
  }

  function defaultSessionRegion(session: RuntimeSession): SessionRegion {
    if (session.display_region === "workflow" || session.display_region === "terminal") {
      return session.display_region;
    }
    return session.target_key === PLAIN_SHELL_TARGET ? "terminal" : "workflow";
  }

  function isActiveRuntimeSession(session: RuntimeSession): boolean {
    return session.status === "running" || session.status === "starting";
  }

  function sessionRegion(session: RuntimeSession): SessionRegion {
    return terminalLayout.sessionRegions[session.key] ?? defaultSessionRegion(session);
  }

  function workflowTabKeyForSession(sessionKey: string): WorkflowTabKey {
    return `session:${sessionKey}`;
  }

  function sessionKeyFromWorkflowTab(tabKey: WorkflowTabKey): string | null {
    return tabKey.startsWith("session:") ? tabKey.slice("session:".length) : null;
  }

  function workspaceStorageId(
    id: string,
    hostKey: string | undefined = workspaceHostKey,
  ): string {
    return hostKey ? `fleet:${encodeURIComponent(hostKey)}:${id}` : id;
  }

  function terminalLayoutStorageKey(storageId: string): string {
    return `${TERMINAL_LAYOUT_KEY_PREFIX}${storageId}`;
  }

  function loadTerminalLayout(storageId: string): TerminalLayoutState {
    return parseTerminalLayout(
      readLocalStorage(terminalLayoutStorageKey(storageId)),
    );
  }

  function loadWorkflowPresets(): WorkflowPreset[] {
    const raw = readLocalStorage(WORKFLOW_PRESETS_KEY);
    if (!raw) return [];
    try {
      const parsed = JSON.parse(raw) as unknown;
      if (!Array.isArray(parsed)) return [];
      return parsed.flatMap((item) => {
        const preset = parseWorkflowPreset(item);
        return preset ? [preset] : [];
      });
    } catch {
      return [];
    }
  }

  function parseWorkflowPreset(value: unknown): WorkflowPreset | null {
    if (value === null || typeof value !== "object" || Array.isArray(value)) {
      return null;
    }
    const record = value as Record<string, unknown>;
    if (
      typeof record.id !== "string" ||
      typeof record.name !== "string" ||
      typeof record.createdAt !== "string" ||
      typeof record.updatedAt !== "string" ||
      !Array.isArray(record.sessions)
    ) {
      return null;
    }
    const sessions = record.sessions.flatMap((item) => {
      if (item === null || typeof item !== "object" || Array.isArray(item)) {
        return [];
      }
      const session = item as Record<string, unknown>;
      if (
        typeof session.sourceKey !== "string" ||
        typeof session.targetKey !== "string" ||
        typeof session.label !== "string" ||
        (session.region !== "workflow" && session.region !== "terminal")
      ) {
        return [];
      }
      return [
        {
          sourceKey: session.sourceKey,
          targetKey: session.targetKey,
          label: session.label,
          region: session.region,
        } satisfies WorkflowPresetSession,
      ];
    });
    return {
      id: record.id,
      name: record.name,
      createdAt: record.createdAt,
      updatedAt: record.updatedAt,
      sessions,
      layout: parseTerminalLayout(JSON.stringify(record.layout)),
    };
  }

  function terminalSessionKeysFrom(
    sessions: RuntimeSession[],
    layout: TerminalLayoutState = terminalLayout,
  ): string[] {
    return sessions
      .filter(
        (session) =>
          (layout.sessionRegions[session.key] ?? defaultSessionRegion(session)) ===
          "terminal",
      )
      .map((session) => session.key);
  }

  function workflowTabKeysFrom(
    sessions: RuntimeSession[],
    layout: TerminalLayoutState = terminalLayout,
  ): WorkflowTabKey[] {
    const keys: WorkflowTabKey[] = ["home"];
    if (
      layout.dock === "top" &&
      (layout.open || terminalSessionKeysFrom(sessions, layout).length > 0)
    ) {
      keys.push("terminal");
    }
    for (const session of sessions) {
      const region =
        layout.sessionRegions[session.key] ?? defaultSessionRegion(session);
      if (region === "workflow") {
        keys.push(workflowTabKeyForSession(session.key));
      }
    }
    return keys;
  }

  function normalizeLayoutForSessions(
    sessions: RuntimeSession[],
    base: TerminalLayoutState = terminalLayout,
    activeWorkflowTab: WorkflowTabKey = activeTabKey,
  ): TerminalLayoutState {
    const allKeys = sessions.map((session) => session.key);
    let next = normalizeTerminalLayout(base, allKeys);
    const sessionRegions = { ...next.sessionRegions };
    for (const session of sessions) {
      sessionRegions[session.key] =
        sessionRegions[session.key] ?? defaultSessionRegion(session);
    }
    next = { ...next, sessionRegions };
    const terminalKeys = terminalSessionKeysFrom(sessions, next);
    let terminalGroups = next.terminalGroups.filter((group) =>
      collectTerminalGroupKeys(group).some((key) => terminalKeys.includes(key)),
    );
    const groupedKeys = terminalGroups.flatMap((group) =>
      collectTerminalGroupKeys(group),
    );
    for (const key of terminalKeys) {
      if (!groupedKeys.includes(key)) {
        terminalGroups = [...terminalGroups, createTerminalGroup(key)];
        groupedKeys.push(key);
      }
    }
    const activeGroup =
      (next.activeTerminalGroupID
        ? terminalGroups.find((group) => group.id === next.activeTerminalGroupID)
        : null) ??
      (next.activeSessionKey
        ? terminalGroups.find((group) =>
            collectTerminalGroupKeys(group).includes(next.activeSessionKey!),
          )
        : null) ??
      terminalGroups[0] ??
      null;
    const activeSessionKey =
      activeGroup?.activeSessionKey ??
      firstLeaf(activeGroup?.tree ?? null)?.sessionKey ??
      null;
    const normalized = {
      ...next,
      activeSessionKey,
      tree: activeGroup?.tree ?? null,
      terminalGroups,
      activeTerminalGroupID: activeGroup?.id ?? null,
    };
    const workflowTree = activateWorkflowTab(
      normalizeWorkflowTree(
        normalized.workflowTree,
        workflowTabKeysFrom(sessions, normalized),
      ),
      activeWorkflowTab,
    );
    return {
      ...normalized,
      workflowTree,
    };
  }

  function collectTerminalGroupKeys(group: TerminalGroup): string[] {
    return group.tree ? collectSessionKeys(group.tree) : [];
  }

  function layoutWithTerminalGroups(
    base: TerminalLayoutState,
    groups: TerminalGroup[],
    activeGroupID: string | null,
  ): TerminalLayoutState {
    const activeGroup =
      (activeGroupID
        ? groups.find((group) => group.id === activeGroupID)
        : null) ??
      groups[0] ??
      null;
    return {
      ...base,
      terminalGroups: groups,
      activeTerminalGroupID: activeGroup?.id ?? null,
      activeSessionKey: activeGroup?.activeSessionKey ?? null,
      tree: activeGroup?.tree ?? null,
    };
  }

  function rememberActiveTab(key: WorkflowTabKey): void {
    if (!workspaceId) return;
    writeLocalStorage(
      `${ACTIVE_WORKSPACE_TAB_KEY_PREFIX}${workspaceStorageId(workspaceId)}`,
      key,
    );
  }

  function selectWorkspaceTab(key: WorkflowTabKey): void {
    if (terminalLayout.workflowMode === "grid") {
      terminalLayout = { ...terminalLayout, workflowMode: "tabs" };
    }
    terminalLayout = {
      ...terminalLayout,
      workflowTree: activateWorkflowTab(terminalLayout.workflowTree, key),
    };
    activeTabKey = key;
    rememberActiveTab(key);
  }

  function restoreWorkspaceTabSelection(key: WorkflowTabKey): void {
    activeTabKey = key;
    rememberActiveTab(key);
  }

  function restoreWorkspaceTab(storageId: string): WorkflowTabKey {
    const remembered = readLocalStorage(
      `${ACTIVE_WORKSPACE_TAB_KEY_PREFIX}${storageId}`,
    );
    if (remembered === "diff") return "home";
    if (
      remembered === "home" ||
      remembered === "terminal" ||
      remembered?.startsWith("session:")
    ) {
      return remembered as WorkflowTabKey;
    }
    return "home";
  }

  function defaultSidebarTab(): SidebarTab {
    return "diff";
  }

  function isSidebarTabSupported(
    ws: Workspace,
    tab: SidebarTab,
  ): boolean {
    if (tab === "diff") return true;
    if (tab === "issue") {
      return ws.item_type === "issue";
    }
    if (tab === "kata_task") {
      return ws.item_type === "kata_task";
    }
    if (tab === "reviews") {
      return ws.item_type === "pull_request";
    }
    return getWorkspacePRNumber(ws) !== null;
  }

  function syncSidebarTabForWorkspace(ws: Workspace): void {
    if (!isSidebarTabSupported(ws, sidebarTab)) {
      sidebarTab = defaultSidebarTab();
    }
  }

  function getWorkspacePRNumber(ws: Workspace): number | null {
    if (ws.item_type === "pull_request") return ws.item_number;
    return ws.associated_pr_number ?? null;
  }

  function terminalRoute(id: string): string {
    if (!workspaceHostKey) return `/terminal/${encodeURIComponent(id)}`;
    return `/terminal/fleet/${encodeURIComponent(workspaceHostKey)}/${encodeURIComponent(id)}`;
  }

  function isCurrentWorkspace(id: string, hostKey: string | undefined): boolean {
    return id === workspaceId && hostKey === workspaceHostKey;
  }

  function selectedWorkspaceHostKey(ws: Workspace): string | undefined {
    return ws.fleet_host_key;
  }

  // A 404 on the workspace fetch is authoritative: the workspace no longer
  // exists (deleted by another client). Snapshot the identity from the
  // cached envelope BEFORE dropping it — the deletion callback needs it to
  // tombstone controller-less cached detail — then clear the cached
  // envelope so liveness rendering shows the error state instead of
  // continuing to display the deleted workspace.
  function handleWorkspaceGone(id: string, hostKey: string | undefined): void {
    onWorkspaceDeleted?.(id, hostKey, workspaceIdentitySnapshot(id));
    if (workspace?.id === id) {
      workspace = null;
      stopPolling();
      stopRuntimePolling();
    }
  }

  async function fetchWorkspace(): Promise<void> {
    // Capture the id at call time. With workspaceId changing across
    // navigations, a slow in-flight fetch for the previous id could
    // otherwise resolve after a newer fetch and overwrite the new
    // workspace's data with stale content (causing a perceived flash
    // back to the previous workspace).
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    recordWorkspaceSwitchPhase("workspace-request-start", id, hostKey);
    try {
      if (hostKey) {
        const { data, error, response } = await client.GET(
          "/fleet/hosts/{host_key}/workspaces/{id}",
          {
            params: { path: { host_key: hostKey, id } },
          },
        );
        recordWorkspaceSwitchPhase("workspace-request-end", id, hostKey, {
          status: response.status,
        });
        if (!isCurrentWorkspace(id, hostKey)) return;
        if (!data) {
          if (response.status === 404) handleWorkspaceGone(id, hostKey);
          loadError = apiErrorMessage(
            error,
            `Failed to load workspace (${response.status})`,
          );
          return;
        }
        const nextWorkspace = { ...(data as Workspace), fleet_host_key: hostKey };
        workspace = nextWorkspace;
        syncSidebarTabForWorkspace(nextWorkspace);
        loadError = null;

        if (nextWorkspace.status !== "creating") {
          stopPolling();
        }
        if (nextWorkspace.status === "ready") {
          startRuntimePolling();
          if (!hasAppliedRuntimeFor(id, hostKey)) {
            void fetchRuntime();
          }
        } else {
          stopRuntimePolling();
        }
        return;
      }
      const { data, error, response } = await client.GET(
        "/workspaces/{id}",
        {
          params: { path: { id } },
        },
      );
      recordWorkspaceSwitchPhase("workspace-request-end", id, hostKey, {
        status: response.status,
      });
      if (!isCurrentWorkspace(id, hostKey)) return;
      if (!data) {
        if (response.status === 404) handleWorkspaceGone(id, hostKey);
        loadError = apiErrorMessage(
          error,
          `Failed to load workspace (${response.status})`,
        );
        return;
      }
      workspace = data as Workspace;
      syncSidebarTabForWorkspace(workspace);
      loadError = null;

      if (data.status !== "creating") {
        stopPolling();
      }
      if (data.status === "ready") {
        startRuntimePolling();
        if (!hasAppliedRuntimeFor(id, hostKey)) {
          void fetchRuntime();
        }
      } else {
        stopRuntimePolling();
      }
    } catch (err) {
      recordWorkspaceSwitchPhase("workspace-request-end", id, hostKey, {
        error: true,
      });
      if (!isCurrentWorkspace(id, hostKey)) return;
      loadError =
        err instanceof Error
          ? err.message
          : "Network error";
    }
  }

  interface FetchRuntimeOptions {
    force?: boolean;
  }

  async function fetchRuntime(
    options: FetchRuntimeOptions = {},
  ): Promise<WorkspaceRuntimeState | null> {
    if (!workspaceId) return null;
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    if (
      !options.force &&
      runtimeFetchInFlight &&
      runtimeFetchInFlightId === id &&
      runtimeFetchInFlightHostKey === hostKey
    ) {
      return runtimeFetchInFlight;
    }
    const seq = runtimeFetchSeq + 1;
    runtimeFetchSeq = seq;
    const fetchPromise = (async () => {
      recordWorkspaceSwitchPhase("runtime-request-start", id, hostKey);
      try {
        const data = await getWorkspaceRuntime(id, hostKey);
        recordWorkspaceSwitchPhase("runtime-request-end", id, hostKey, {
          sessions: data.sessions.length,
        });
        if (!isCurrentWorkspace(id, hostKey) || seq !== runtimeFetchSeq) return null;
        const fingerprint = JSON.stringify(data);
        if (
          hasAppliedRuntimeFor(id, hostKey) &&
          appliedRuntimeState?.fingerprint === fingerprint
        ) {
          runtimeError = null;
          return data;
        }
        runtime = data;
        runtimeForId = id;
        runtimeForHostKey = hostKey;
        appliedRuntimeState = { workspaceId: id, hostKey, fingerprint };
        runtimeError = null;
        terminalLayout = normalizeLayoutForSessions(data.sessions);
        if (
          activeTabKey.startsWith("session:") &&
          !data.sessions.some(
            (session) =>
              session.key === activeTabKey.slice("session:".length) &&
              sessionRegion(session) === "workflow",
          )
        ) {
          selectFallbackTab();
        }
        mountedSessionKeys = mountedSessionKeys.filter(
          (key) =>
            data.sessions.some((session) => session.key === key),
        );
        return data;
      } catch (err) {
        recordWorkspaceSwitchPhase("runtime-request-end", id, hostKey, {
          error: true,
        });
        if (!isCurrentWorkspace(id, hostKey) || seq !== runtimeFetchSeq) return null;
        runtimeError =
          err instanceof Error
            ? err.message
            : "Runtime load failed";
        return null;
      } finally {
        if (
          runtimeFetchSeq === seq &&
          runtimeFetchInFlightId === id &&
          runtimeFetchInFlightHostKey === hostKey
        ) {
          runtimeFetchInFlight = null;
          runtimeFetchInFlightId = "";
          runtimeFetchInFlightHostKey = undefined;
        }
      }
    })();
    runtimeFetchInFlight = fetchPromise;
    runtimeFetchInFlightId = id;
    runtimeFetchInFlightHostKey = hostKey;
    return fetchPromise;
  }

  async function handleLaunch(targetKey: string): Promise<void> {
    if (!workspaceId || launchingKey || actionsBlocked) return;
    // Capture id so post-await steps bail if workspace changes mid-launch.
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    launchingKey = targetKey;
    try {
      const session = await launchWorkspaceSession(id, targetKey, {
        hostKey,
        region: "workflow",
      });
      if (!isCurrentWorkspace(id, hostKey)) return;
      const refreshed = await fetchRuntime({ force: true });
      if (!isCurrentWorkspace(id, hostKey)) return;
      clearClosedSession(session);
      moveSessionToWorkflow(session.key);
      mountSessionTerminal(session.key);
      selectWorkspaceTab(workflowTabKeyForSession(session.key));
      // The launch succeeding is not enough to close on: the pane can only render
      // what the refreshed runtime reports, so a failed refresh would drop the user
      // on an empty pane with no explanation. The tab selection above stands either
      // way, so the session appears as soon as a later refresh finds it.
      if (refreshed?.sessions.some((candidate) => candidate.key === session.key) === true) {
        closeLauncher();
      } else {
        showFlash("Session launched, but the workspace could not be reloaded", {
          tone: "danger",
        });
      }
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      showFlash(err instanceof Error ? err.message : "Launch failed", {
        tone: "danger",
      });
    } finally {
      if (isCurrentWorkspace(id, hostKey)) launchingKey = null;
    }
  }

  function openSession(sessionKey: string): void {
    const session = runtimeSessions.find((s) => s.key === sessionKey);
    if (session && sessionRegion(session) === "terminal") {
      selectTerminalSession(sessionKey);
      return;
    }
    mountSessionTerminal(sessionKey);
    selectWorkspaceTab(workflowTabKeyForSession(sessionKey));
  }

  function closeSession(session: RuntimeSession): void {
    if (actionsBlocked) return;
    if (session.status === "running") {
      const triggerEl =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      previouslyFocusedEl = triggerEl;
      stopPromptSession = session;
      return;
    }
    void stopSession(session);
  }

  async function confirmStopSession(): Promise<void> {
    if (stopSessionStopping || stopPromptSession === null) return;
    stopSessionStopping = true;
    const session = stopPromptSession;
    try {
      await stopSession(session);
      if (stopPromptSession?.key === session.key) {
        stopPromptSession = null;
      }
    } finally {
      stopSessionStopping = false;
    }
  }

  function cancelStopSession(): void {
    if (stopSessionStopping) return;
    stopPromptSession = null;
  }

  async function stopSession(session: RuntimeSession): Promise<void> {
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    try {
      await stopWorkspaceSession(id, session.key, hostKey);
      if (!isCurrentWorkspace(id, hostKey)) return;
      await fetchRuntime({ force: true });
      if (!isCurrentWorkspace(id, hostKey)) return;
      unmountSessionTerminal(session.key);
      const terminalGroups = closeSessionInTerminalGroups(
        terminalLayout.terminalGroups,
        session.key,
      );
      terminalLayout = normalizeLayoutForSessions(
        runtimeSessions,
        layoutWithTerminalGroups(
          terminalLayout,
          terminalGroups,
          terminalLayout.activeTerminalGroupID,
        ),
      );
      if (activeTabKey === `session:${session.key}`) {
        selectFallbackTab();
      }
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      showFlash(err instanceof Error ? err.message : "Stop failed", {
        tone: "danger",
      });
    }
  }

  function handleSessionExit(session: RuntimeSession): void {
    if (session.workspace_id !== workspaceId) return;
    markSessionClosed(session);
    unmountSessionTerminal(session.key);
    const terminalGroups = closeSessionInTerminalGroups(
      terminalLayout.terminalGroups,
      session.key,
    );
    terminalLayout = normalizeLayoutForSessions(
      runtimeSessions,
      layoutWithTerminalGroups(
        terminalLayout,
        terminalGroups,
        terminalLayout.activeTerminalGroupID,
      ),
    );
    if (activeTabKey === `session:${session.key}`) {
      selectFallbackTab();
    }
    void fetchRuntime({ force: true });
  }

  async function launchTerminalSession(
    insertIntoTree = true,
  ): Promise<RuntimeSession | null> {
    if (!workspaceId || terminalLaunching || actionsBlocked) return null;
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    terminalLaunching = true;
    try {
      const session = await launchWorkspaceSession(id, PLAIN_SHELL_TARGET, {
        hostKey,
        region: "terminal",
      });
      if (!isCurrentWorkspace(id, hostKey)) return null;
      const sessionsWithLaunch = upsertRuntimeSession(session);
      if (!insertIntoTree) {
        clearClosedSession(session);
        return session;
      }
      const groups = insertIntoTree
        ? addTerminalGroup(terminalLayout.terminalGroups, session.key)
        : terminalLayout.terminalGroups;
      const activeGroupID =
        groups.at(-1)?.id ?? terminalLayout.activeTerminalGroupID;
      terminalLayout = normalizeLayoutForSessions(
        sessionsWithLaunch,
        layoutWithTerminalGroups(
          {
            ...terminalLayout,
            open: true,
            sessionRegions: {
              ...terminalLayout.sessionRegions,
              [session.key]: "terminal",
            },
          },
          groups,
          activeGroupID,
        ),
      );
      await fetchRuntime({ force: true });
      if (!isCurrentWorkspace(id, hostKey)) return null;
      clearClosedSession(session);
      if (terminalLayout.dock === "top") {
        selectWorkspaceTab("terminal");
      }
      return session;
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return null;
      showFlash(
        err instanceof Error ? err.message : "Terminal launch failed",
        { tone: "danger" },
      );
    } finally {
      if (isCurrentWorkspace(id, hostKey)) terminalLaunching = false;
    }
    return null;
  }

  async function toggleTerminalPanel(): Promise<void> {
    if (actionsBlocked) return;
    if (terminalLayout.open) {
      terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
        ...terminalLayout,
        open: false,
      });
      if (activeTabKey === "terminal") {
        selectFallbackTab();
      }
      return;
    }
    terminalLayout = { ...terminalLayout, open: true };
    if (!terminalSessions.some(isActiveRuntimeSession)) {
      await launchTerminalSession();
    } else if (terminalLayout.dock === "top") {
      selectWorkspaceTab("terminal");
    }
  }

  function selectTerminalSession(sessionKey: string): void {
    if (actionsBlocked) return;
    const group = terminalGroupForSession(terminalLayout.terminalGroups, sessionKey);
    const groups = terminalLayout.terminalGroups.map((candidate) =>
      candidate.id === group?.id
        ? { ...candidate, activeSessionKey: sessionKey }
        : candidate,
    );
    terminalLayout = normalizeLayoutForSessions(
      runtimeSessions,
      layoutWithTerminalGroups(
        {
          ...terminalLayout,
          open: true,
        },
        groups,
        group?.id ?? terminalLayout.activeTerminalGroupID,
      ),
    );
    if (terminalLayout.dock === "top") {
      selectWorkspaceTab("terminal");
    }
  }

  function selectTerminalGroup(groupID: string): void {
    if (actionsBlocked) return;
    terminalLayout = normalizeLayoutForSessions(
      runtimeSessions,
      layoutWithTerminalGroups(
        { ...terminalLayout, open: true },
        terminalLayout.terminalGroups,
        groupID,
      ),
    );
    if (terminalLayout.dock === "top") {
      selectWorkspaceTab("terminal");
    }
  }

  function moveSessionToTerminal(sessionKey: string): void {
    if (actionsBlocked) return;
    const session = runtimeSessions.find((s) => s.key === sessionKey);
    if (!session) return;
    const groups = addTerminalGroup(terminalLayout.terminalGroups, sessionKey);
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...layoutWithTerminalGroups(
        {
          ...terminalLayout,
          open: true,
          sessionRegions: {
            ...terminalLayout.sessionRegions,
            [sessionKey]: "terminal",
          },
        },
        groups,
        terminalGroupForSession(groups, sessionKey)?.id ?? null,
      ),
    });
    if (terminalLayout.dock === "top") {
      selectWorkspaceTab("terminal");
    } else if (activeTabKey === `session:${sessionKey}`) {
      selectFallbackTab();
    }
  }

  function moveSessionToWorkflow(sessionKey: string): void {
    if (actionsBlocked) return;
    const terminalGroups = closeSessionInTerminalGroups(
      terminalLayout.terminalGroups,
      sessionKey,
    );
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...layoutWithTerminalGroups(
        {
          ...terminalLayout,
          workflowMode: "tabs",
        },
        terminalGroups,
        terminalLayout.activeTerminalGroupID,
      ),
      sessionRegions: {
        ...terminalLayout.sessionRegions,
        [sessionKey]: "workflow",
      },
    });
    mountSessionTerminal(sessionKey);
    selectWorkspaceTab(workflowTabKeyForSession(sessionKey));
  }

  function layoutWithWorkflowTab(
    tabKey: WorkflowTabKey,
    base: TerminalLayoutState,
  ): TerminalLayoutState {
    if (tabKey === "terminal") {
      return { ...base, open: true, dock: "top" };
    }
    const sessionKey = sessionKeyFromWorkflowTab(tabKey);
    if (sessionKey === null) return base;
    mountSessionTerminal(sessionKey);
    const terminalGroups = closeSessionInTerminalGroups(
      base.terminalGroups,
      sessionKey,
    );
    return {
      ...layoutWithTerminalGroups(
        {
          ...base,
          workflowMode: "tabs",
        },
        terminalGroups,
        base.activeTerminalGroupID,
      ),
      sessionRegions: {
        ...base.sessionRegions,
        [sessionKey]: "workflow",
      },
    };
  }

  function moveWorkflowTabBeforeTarget(
    sourceTabKey: WorkflowTabKey,
    targetTabKey: WorkflowTabKey,
  ): void {
    if (actionsBlocked) return;
    demoteWorkflowTab(sourceTabKey);
    if (sourceTabKey === targetTabKey) return;
    const prepared = normalizeLayoutForSessions(
      runtimeSessions,
      layoutWithWorkflowTab(sourceTabKey, terminalLayout),
    );
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...prepared,
      workflowTree: moveWorkflowTabBefore(
        prepared.workflowTree,
        sourceTabKey,
        targetTabKey,
      ),
    });
    selectWorkspaceTab(sourceTabKey);
  }

  function appendWorkflowTabToGroup(
    sourceTabKey: WorkflowTabKey,
    leafID: string,
  ): void {
    if (actionsBlocked) return;
    demoteWorkflowTab(sourceTabKey);
    const prepared = normalizeLayoutForSessions(
      runtimeSessions,
      layoutWithWorkflowTab(sourceTabKey, terminalLayout),
    );
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...prepared,
      workflowTree: appendWorkflowTabToLeaf(
        prepared.workflowTree,
        sourceTabKey,
        leafID,
      ),
    });
    selectWorkspaceTab(sourceTabKey);
  }

  function splitWorkflowTabIntoGroup(
    sourceTabKey: WorkflowTabKey,
    leafID: string,
    direction: SplitDirection,
    placement: "before" | "after",
  ): void {
    if (actionsBlocked) return;
    demoteWorkflowTab(sourceTabKey);
    const prepared = normalizeLayoutForSessions(
      runtimeSessions,
      layoutWithWorkflowTab(sourceTabKey, terminalLayout),
    );
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...prepared,
      workflowTree: splitWorkflowTabIntoLeaf(
        prepared.workflowTree,
        sourceTabKey,
        leafID,
        direction,
        placement,
      ),
    });
    selectWorkspaceTab(sourceTabKey);
  }

  function closeWorkflowTab(tabKey: WorkflowTabKey): void {
    if (actionsBlocked) return;
    if (tabKey === "terminal") {
      terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
        ...terminalLayout,
        open: false,
      });
      if (activeTabKey === "terminal") {
        selectFallbackTab();
      }
      return;
    }
    const sessionKey = sessionKeyFromWorkflowTab(tabKey);
    if (sessionKey === null) return;
    const session = runtimeSessions.find((s) => s.key === sessionKey);
    if (session) {
      void closeSession(session);
    }
  }

  function moveWorkflowTabToTerminal(tabKey: WorkflowTabKey): void {
    if (actionsBlocked) return;
    const sessionKey = sessionKeyFromWorkflowTab(tabKey);
    if (sessionKey !== null) {
      moveSessionToTerminal(sessionKey);
    }
  }

  function renameWorkflowTab(tabKey: WorkflowTabKey): void {
    if (actionsBlocked) return;
    const sessionKey = sessionKeyFromWorkflowTab(tabKey);
    if (sessionKey === null) return;
    const session = runtimeSessions.find((s) => s.key === sessionKey);
    if (!session) return;
    openRenamePrompt(session);
  }

  function openRenamePrompt(session: RuntimeSession): void {
    const triggerEl =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    previouslyFocusedEl = triggerEl;
    renamePrompt = {
      sessionKey: session.key,
      originalLabel: session.label,
    };
    renameInputValue = session.label;
  }

  async function saveRenamePrompt(): Promise<void> {
    if (renamePrompt === null || renameSaving) return;
    const trimmed = renameInputValue.trim();
    if (!trimmed) return;
    if (trimmed === renamePrompt.originalLabel) {
      cancelRenamePrompt();
      return;
    }

    const id = workspaceId;
    const hostKey = workspaceHostKey;
    const sessionKey = renamePrompt.sessionKey;
    renameSaving = true;
    try {
      const updated = await renameWorkspaceSession(
        id,
        sessionKey,
        trimmed,
        hostKey,
      );
      if (!isCurrentWorkspace(id, hostKey)) return;
      if (runtime) {
        invalidateRuntimeSnapshot();
      }
      runtime = runtime
        ? {
            ...runtime,
            sessions: runtime.sessions.map((session) =>
              session.key === sessionKey ? updated : session,
            ),
          }
        : runtime;
      renamePrompt = null;
      renameInputValue = "";
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      showFlash(err instanceof Error ? err.message : "Rename failed", {
        tone: "danger",
      });
    } finally {
      if (isCurrentWorkspace(id, hostKey)) renameSaving = false;
    }
  }

  function cancelRenamePrompt(): void {
    if (renameSaving) return;
    renamePrompt = null;
    renameInputValue = "";
  }

  function renameSession(session: RuntimeSession): void {
    renameWorkflowTab(workflowTabKeyForSession(session.key));
  }

  function createPresetSnapshot(name: string, id = newPresetID()): WorkflowPreset {
    const now = new Date().toISOString();
    return {
      id,
      name,
      createdAt:
        workflowPresets.find((preset) => preset.id === id)?.createdAt ?? now,
      updatedAt: now,
      sessions: runtimeSessions.map((session) => ({
        sourceKey: session.key,
        targetKey: session.target_key,
        region: sessionRegion(session),
        label: sessionDisplayLabels[session.key] ?? session.label,
      })),
      layout: terminalLayout,
    };
  }

  function saveWorkflowPreset(): void {
    const name = prompt("Preset name", "Review workspace");
    if (name === null) return;
    const trimmed = name.trim();
    if (!trimmed) return;
    const preset = createPresetSnapshot(trimmed);
    workflowPresets = [...workflowPresets, preset];
    selectedWorkflowPresetId = preset.id;
  }

  function updateWorkflowPreset(presetID: string): void {
    const existing = workflowPresets.find((preset) => preset.id === presetID);
    if (!existing) return;
    const preset = createPresetSnapshot(existing.name, existing.id);
    workflowPresets = workflowPresets.map((candidate) =>
      candidate.id === presetID ? preset : candidate,
    );
    selectedWorkflowPresetId = preset.id;
  }

  async function applyWorkflowPreset(presetID: string): Promise<void> {
    if (!workspaceId || applyingWorkflowPreset || actionsBlocked) return;
    const preset = workflowPresets.find((candidate) => candidate.id === presetID);
    if (!preset) return;
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    const presetOwner = viewWorkspaceKey;
    applyingWorkflowPresetFor = presetOwner;
    try {
      const keyMap: Record<string, string> = {};
      for (const spec of preset.sessions) {
        let session = await launchWorkspaceSession(id, spec.targetKey, {
          hostKey,
          region: spec.region,
        });
        if (!isCurrentWorkspace(id, hostKey)) return;
        if (spec.label.trim() && spec.label !== session.label) {
          session = await renameWorkspaceSession(
            id,
            session.key,
            spec.label.trim(),
            hostKey,
          );
          if (!isCurrentWorkspace(id, hostKey)) return;
        }
        keyMap[spec.sourceKey] = session.key;
      }
      const mappedLayout = mapPresetLayout(preset.layout, keyMap);
      const refreshed = await fetchRuntime({ force: true });
      if (!isCurrentWorkspace(id, hostKey) || !refreshed) return;
      const presetActiveTab = firstWorkflowTab(mappedLayout) ?? "home";
      terminalLayout = normalizeLayoutForSessions(
        refreshed.sessions,
        mappedLayout,
        presetActiveTab,
      );
      mountedSessionKeys = refreshed.sessions
        .filter((session) => sessionRegionForLayout(session, terminalLayout) === "workflow")
        .map((session) => session.key);
      selectedWorkflowPresetId = preset.id;
      selectWorkspaceTab(firstWorkflowTab(terminalLayout) ?? "home");
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      showFlash(err instanceof Error ? err.message : "Preset launch failed", {
        tone: "danger",
      });
    } finally {
      // Cleared for the workspace it started on, whatever is selected now: keyed on
      // the current workspace instead, a preset that finished after a switch would
      // leave its own workspace stuck applying for the rest of the session.
      if (applyingWorkflowPresetFor === presetOwner) applyingWorkflowPresetFor = null;
    }
  }

  function deleteWorkflowPreset(presetID: string): void {
    workflowPresets = workflowPresets.filter((preset) => preset.id !== presetID);
    if (selectedWorkflowPresetId === presetID) {
      selectedWorkflowPresetId = null;
    }
  }

  function mapPresetLayout(
    layout: TerminalLayoutState,
    keyMap: Record<string, string>,
  ): TerminalLayoutState {
    const sessionRegions: Record<string, SessionRegion> = {};
    for (const [sourceKey, region] of Object.entries(layout.sessionRegions)) {
      const mappedKey = keyMap[sourceKey];
      if (mappedKey) sessionRegions[mappedKey] = region;
    }
    return {
      ...layout,
      activeSessionKey:
        layout.activeSessionKey ? keyMap[layout.activeSessionKey] ?? null : null,
      tree: mapPaneNodeSessionKeys(layout.tree, keyMap),
      terminalGroups: mapTerminalGroupSessionKeys(layout.terminalGroups, keyMap),
      workflowTree: mapWorkflowNodeSessionKeys(layout.workflowTree, keyMap),
      sessionRegions,
      customSessionLabels: {},
    };
  }

  function mapTerminalGroupSessionKeys(
    groups: TerminalGroup[],
    keyMap: Record<string, string>,
  ): TerminalGroup[] {
    return groups.flatMap((group) => {
      const tree = mapPaneNodeSessionKeys(group.tree, keyMap);
      if (!tree) return [];
      return [
        {
          ...group,
          activeSessionKey: group.activeSessionKey
            ? keyMap[group.activeSessionKey] ?? firstLeaf(tree)?.sessionKey ?? null
            : firstLeaf(tree)?.sessionKey ?? null,
          tree,
        },
      ];
    });
  }

  function mapPaneNodeSessionKeys(
    node: PaneNode | null,
    keyMap: Record<string, string>,
  ): PaneNode | null {
    if (!node) return null;
    if (node.type === "leaf") {
      const mappedKey = keyMap[node.sessionKey];
      return mappedKey ? { ...node, sessionKey: mappedKey } : null;
    }
    const first = mapPaneNodeSessionKeys(node.first, keyMap);
    const second = mapPaneNodeSessionKeys(node.second, keyMap);
    if (!first) return second;
    if (!second) return first;
    return { ...node, first, second };
  }

  function firstWorkflowTab(layout: TerminalLayoutState): WorkflowTabKey | null {
    if (!layout.workflowTree) return null;
    if (layout.workflowTree.type === "leaf") {
      return layout.workflowTree.activeTabKey;
    }
    const leaf = firstWorkflowLeafFrom(layout.workflowTree);
    return leaf?.activeTabKey ?? null;
  }

  function firstWorkflowLeafFrom(
    node: NonNullable<TerminalLayoutState["workflowTree"]>,
  ): Extract<NonNullable<TerminalLayoutState["workflowTree"]>, { type: "leaf" }> | null {
    if (node.type === "leaf") return node;
    return firstWorkflowLeafFrom(node.first) ?? firstWorkflowLeafFrom(node.second);
  }

  function sessionRegionForLayout(
    session: RuntimeSession,
    layout: TerminalLayoutState,
  ): SessionRegion {
    return layout.sessionRegions[session.key] ?? defaultSessionRegion(session);
  }

  function newPresetID(): string {
    if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
      return `preset-${crypto.randomUUID()}`;
    }
    return `preset-${Date.now().toString(36)}-${Math.random()
      .toString(16)
      .slice(2)}`;
  }

  async function splitTerminal(direction: SplitDirection): Promise<void> {
    if (terminalLaunching || actionsBlocked) return;
    const groupBeforeLaunch = currentTerminalGroup;
    const treeBeforeLaunch = groupBeforeLaunch?.tree ?? null;
    const targetLeaf =
      terminalLayout.activeSessionKey !== null
        ? findLeafBySession(treeBeforeLaunch, terminalLayout.activeSessionKey)
        : firstLeaf(treeBeforeLaunch);
    const session = await launchTerminalSession(false);
    if (!session) return;
    const groupID =
      groupBeforeLaunch?.id ?? terminalLayout.activeTerminalGroupID ?? newPaneGroupID();
    const groups =
      groupBeforeLaunch === null
        ? [createTerminalGroup(session.key, groupID)]
        : updateTerminalGroupTree(
            terminalLayout.terminalGroups,
            groupID,
            (group) => ({
              ...group,
              activeSessionKey: session.key,
              tree: splitPane(
                treeBeforeLaunch,
                targetLeaf?.id ?? null,
                session.key,
                direction,
              ),
            }),
          );
    terminalLayout = normalizeLayoutForSessions(
      upsertRuntimeSession(session),
      layoutWithTerminalGroups(
        {
          ...terminalLayout,
          open: true,
          sessionRegions: {
            ...terminalLayout.sessionRegions,
            [session.key]: "terminal",
          },
        },
        groups,
        groupID,
      ),
    );
    if (terminalLayout.dock === "top") {
      selectWorkspaceTab("terminal");
    }
  }

  function splitTerminalSessionIntoPane(
    sessionKey: string,
    targetLeafID: string,
    direction: SplitDirection,
    placement: "before" | "after",
  ): void {
    if (actionsBlocked) return;
    const session = runtimeSessions.find((candidate) => candidate.key === sessionKey);
    const groupID = terminalLayout.activeTerminalGroupID;
    const group = currentTerminalGroup;
    if (!session || !groupID || !group) return;
    const sourceLeaf = findLeafBySession(group.tree, sessionKey);
    if (sourceLeaf?.id === targetLeafID) {
      selectTerminalSession(sessionKey);
      return;
    }
    const groupsWithoutSource = closeSessionInTerminalGroups(
      terminalLayout.terminalGroups,
      sessionKey,
    );
    const targetGroup =
      groupsWithoutSource.find((candidate) => candidate.id === groupID) ?? group;
    const tree = splitSessionIntoPane(
      targetGroup.tree,
      targetLeafID,
      sessionKey,
      direction,
      placement,
    );
    const groups = updateTerminalGroupTree(
      groupsWithoutSource.some((candidate) => candidate.id === groupID)
        ? groupsWithoutSource
        : [...groupsWithoutSource, targetGroup],
      groupID,
      (candidate) => ({
        ...candidate,
        activeSessionKey: sessionKey,
        tree,
      }),
    );
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...layoutWithTerminalGroups(
        {
          ...terminalLayout,
          open: true,
          sessionRegions: {
            ...terminalLayout.sessionRegions,
            [sessionKey]: "terminal",
          },
        },
        groups,
        groupID,
      ),
    });
  }

  function newPaneGroupID(): string {
    if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
      return `terminal-group-${crypto.randomUUID()}`;
    }
    return `terminal-group-${Date.now().toString(36)}-${Math.random()
      .toString(16)
      .slice(2)}`;
  }

  function dockTerminalPanel(dock: TerminalDock): void {
    if (actionsBlocked) return;
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...terminalLayout,
      dock,
      open: true,
    });
    if (dock === "top") {
      selectWorkspaceTab("terminal");
    } else if (activeTabKey === "terminal") {
      selectFallbackTab();
    }
  }

  function resizeTerminalPanel(height: number): void {
    if (actionsBlocked) return;
    terminalLayout = {
      ...terminalLayout,
      height: clampTerminalHeight(height),
    };
  }

  function updateActiveTerminalTree(tree: PaneNode | null): void {
    if (actionsBlocked) return;
    const activeGroupID = terminalLayout.activeTerminalGroupID;
    terminalLayout = {
      ...terminalLayout,
      tree,
      terminalGroups: activeGroupID
        ? updateTerminalGroupTree(
            terminalLayout.terminalGroups,
            activeGroupID,
            (group) => ({ ...group, tree }),
          )
        : terminalLayout.terminalGroups,
    };
  }

  function readDroppedSession(event: DragEvent): string | null {
    return readRuntimeSessionDrag(event, workspaceId);
  }

  function handleWorkflowDragOver(event: DragEvent): void {
    if (actionsBlocked) return;
    if (readDroppedSession(event) === null) return;
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
  }

  function handleWorkflowDrop(event: DragEvent): void {
    if (actionsBlocked) return;
    const sessionKey = readDroppedSession(event);
    if (sessionKey === null) return;
    event.preventDefault();
    moveSessionToWorkflow(sessionKey);
    clearActiveTerminalDrag();
  }

  function startPolling(): void {
    if (pollTimer) return;
    pollTimer = setInterval(() => {
      void fetchWorkspace();
    }, 3000);
  }

  function stopPolling(): void {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function startRuntimePolling(): void {
    if (runtimePollTimer) return;
    runtimePollTimer = setInterval(() => {
      void fetchRuntime();
    }, 3000);
  }

  function stopRuntimePolling(): void {
    if (runtimePollTimer) {
      clearInterval(runtimePollTimer);
      runtimePollTimer = null;
    }
  }

  async function handleRetrySetup(): Promise<void> {
    if (!workspace || retryingSetup || actionsBlocked) return;

    const id = workspaceId;
    const hostKey = workspaceHostKey;
    retryingSetup = true;
    try {
      if (hostKey) {
        const { data, error, response } = await client.POST(
          "/fleet/hosts/{host_key}/workspaces/{id}/retry",
          {
            params: { path: { host_key: hostKey, id } },
          },
        );
        if (!data) {
          showFlash(
            apiErrorMessage(error, `Retry failed (${response.status})`),
            { tone: "danger" },
          );
          return;
        }
        const nextWorkspace = { ...(data as Workspace), fleet_host_key: hostKey };
        if (!isCurrentWorkspace(id, hostKey) || nextWorkspace.id !== id) return;
        workspace = nextWorkspace;
        if (workspace.status === "creating") {
          startPolling();
          await fetchWorkspace();
        }
        return;
      }
      const { data, error, response } = await client.POST(
        "/workspaces/{id}/retry",
        {
          params: { path: { id } },
        },
      );
      if (!data) {
        showFlash(
          apiErrorMessage(error, `Retry failed (${response.status})`),
          { tone: "danger" },
        );
        return;
      }
      if (!isCurrentWorkspace(id, hostKey)) return;
      workspace = data as Workspace;
      if (workspace.status === "creating") {
        startPolling();
        await fetchWorkspace();
      }
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      showFlash(err instanceof Error ? err.message : "Retry failed", {
        tone: "danger",
      });
    } finally {
      if (isCurrentWorkspace(id, hostKey)) retryingSetup = false;
    }
  }

  async function handleRefreshWorkspace(): Promise<void> {
    if (!workspace || refreshingWorkspace || actionsBlocked) return;

    const id = workspace.id;
    const hostKey = workspaceHostKey;
    refreshingWorkspace = true;
    try {
      if (hostKey) {
        const { data, error, response } = await client.POST(
          "/fleet/hosts/{host_key}/workspaces/{id}/refresh",
          {
            params: { path: { host_key: hostKey, id } },
          },
        );
        if (!isCurrentWorkspace(id, hostKey)) return;
        if (!data) {
          showFlash(
            apiErrorMessage(error, `Refresh failed (${response.status})`),
            { tone: "danger" },
          );
          return;
        }
        workspace = { ...(data as Workspace), fleet_host_key: hostKey };
        syncSidebarTabForWorkspace(workspace);
        sidebarRefreshToken += 1;
        if (workspace.status === "ready") {
          void fetchRuntime();
        }
        return;
      }
      const { data, error, response } = await client.POST(
        "/workspaces/{id}/refresh",
        {
          params: { path: { id } },
        },
      );
      if (!isCurrentWorkspace(id, hostKey)) return;
      if (!data) {
        const message = apiErrorMessage(
          error,
          `Refresh failed (${response.status})`,
        );
        showFlash(message, { tone: "danger" });
        return;
      }
      workspace = data as Workspace;
      syncSidebarTabForWorkspace(workspace);
      sidebarRefreshToken += 1;
      if (workspace.status === "ready") {
        void fetchRuntime();
      }
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      const message =
        err instanceof Error
          ? err.message
          : "Refresh failed";
      showFlash(message, { tone: "danger" });
    } finally {
      if (isCurrentWorkspace(id, hostKey)) refreshingWorkspace = false;
    }
  }

  // Provider-aware identity of the loaded envelope, but only while it still
  // describes the given workspace: deletion must tombstone the inline
  // claim/override slot even for workspaces that were never claimed inline,
  // and the store has no identity metadata of its own for those.
  function workspaceIdentitySnapshot(targetId: string): WorkspaceItemIdentity | undefined {
    const ws = workspace;
    if (!ws || ws.id !== targetId) return undefined;
    return {
      provider: ws.repo.provider,
      platformHost: ws.repo.platform_host,
      owner: ws.repo.owner,
      name: ws.repo.name,
      repoPath: ws.repo.repo_path,
      number: ws.item_number,
      // Envelope vocabulary ("pull_request"/"issue"/"kata_task");
      // canonicalItemType maps it for identity comparison.
      itemType: ws.item_type,
    };
  }

  async function handleDelete(
    triggerEl: HTMLElement | null = null,
  ): Promise<void> {
    if (actionsBlocked) return;
    const targetId = workspaceId;
    const targetHostKey = workspaceHostKey;
    const targetGen = workspaceGen;
    const targetIdentity = workspaceIdentitySnapshot(targetId);
    // Capture the trigger synchronously: the click handler runs
    // before `inert` is applied to .terminal-view, so this is the
    // last point we can read the originating focused element. By
    // the time the post-await effect runs, the browser has cleared
    // focus to document.body.
    triggerEl ??=
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    addDeletingWorkspaceTarget(targetId, targetHostKey);
    try {
      const { error, response } = targetHostKey
        ? await client.DELETE("/fleet/hosts/{host_key}/workspaces/{id}", {
            params: { path: { host_key: targetHostKey, id: targetId } },
          })
        : await client.DELETE("/workspaces/{id}", {
            params: { path: { id: targetId } },
          });
      const responseFailed =
        response.status === 409 ||
        (!response.ok && response.status !== 204);
      if (responseFailed && targetGen !== workspaceGen) return;
      // Successful delete: report it before any current-selection guard.
      // The workspace is gone on the server regardless of what the user
      // is looking at now, and inline claimants, tombstones, and route
      // memory must hear about it even after switching workspaces
      // mid-delete. Prompts, flashes, and navigation below stay gated.
      if (!responseFailed) {
        onWorkspaceDeleted?.(targetId, targetHostKey, targetIdentity);
      }
      // Different workspace now: the user has moved on and nothing
      // else about this response applies.
      if (!isCurrentWorkspace(targetId, targetHostKey)) return;
      if (response.status === 409) {
        previouslyFocusedEl = triggerEl;
        forcePromptForId = targetId;
        forcePromptIdentity = targetIdentity;
        forcePromptMessage = apiErrorMessage(
          error,
          "Workspace has uncommitted changes.",
        );
        return;
      }
      if (!response.ok && response.status !== 204) {
        showFlash(
          apiErrorMessage(error, `Delete failed (${response.status})`),
          { tone: "danger" },
        );
        return;
      }
      // Navigate away if the user is still looking at the deleted
      // workspace, even after an A→B→A round trip — otherwise they'd be
      // staring at a workspace that no longer exists.
      if (!isCurrentTerminalRoute(targetId)) return;
      navigate("/workspaces");
    } finally {
      removeDeletingWorkspaceTarget(targetId, targetHostKey);
    }
  }

  function isDeletingWorkspaceTarget(
    target: DeletingWorkspaceTarget,
    id: string,
    hostKey: string | undefined,
  ): boolean {
    return target.id === id && target.hostKey === hostKey;
  }

  function addDeletingWorkspaceTarget(
    id: string,
    hostKey: string | undefined,
  ): void {
    if (
      deletingWorkspaceTargets.some((target) =>
        isDeletingWorkspaceTarget(target, id, hostKey),
      )
    ) {
      return;
    }
    deletingWorkspaceTargets = [...deletingWorkspaceTargets, { id, hostKey }];
  }

  function removeDeletingWorkspaceTarget(
    id: string,
    hostKey: string | undefined,
  ): void {
    deletingWorkspaceTargets = deletingWorkspaceTargets.filter(
      (target) => !isDeletingWorkspaceTarget(target, id, hostKey),
    );
  }

  function isCurrentTerminalRoute(targetId: string): boolean {
    return window.location.pathname.endsWith(terminalRoute(targetId));
  }

  async function confirmForceDelete(): Promise<void> {
    if (forceDeleting) return;
    const targetId = forcePromptForId;
    if (targetId === null) return;
    const targetHostKey = workspaceHostKey;
    const targetGen = workspaceGen;
    // Prefer the snapshot taken at 409 time; the live envelope may belong
    // to a different workspace after an A -> B switch.
    const targetIdentity = forcePromptIdentity ?? workspaceIdentitySnapshot(targetId);
    forceDeleting = true;
    addDeletingWorkspaceTarget(targetId, targetHostKey);
    try {
      const { error, response } = targetHostKey
        ? await client.DELETE("/fleet/hosts/{host_key}/workspaces/{id}", {
            params: {
              path: { host_key: targetHostKey, id: targetId },
              query: { force: true },
            },
          })
        : await client.DELETE("/workspaces/{id}", {
            params: {
              path: { id: targetId },
              query: { force: true },
            },
          });
      const responseFailed =
        !response.ok && response.status !== 204;
      if (responseFailed && targetGen !== workspaceGen) return;
      // Successful force-delete: report it before the current-selection
      // guard — the server destroyed the workspace either way, and
      // inline claimants, tombstones, and route memory must hear about
      // it even after switching workspaces mid-delete.
      if (!responseFailed) {
        onWorkspaceDeleted?.(targetId, targetHostKey, targetIdentity);
      }
      // Once the user has moved to a different workspace we drop the
      // rest of the response on the floor so prompt state stays put and
      // navigate() doesn't pull them away.
      if (!isCurrentWorkspace(targetId, targetHostKey)) return;
      if (!response.ok && response.status !== 204) {
        showFlash(
          apiErrorMessage(error, `Delete failed (${response.status})`),
          { tone: "danger" },
        );
        forcePromptMessage = null;
        forcePromptForId = null;
        forcePromptIdentity = undefined;
        return;
      }
      // Navigate away if the user is still viewing the workspace the
      // server just destroyed, even after an A→B→A round trip.
      forcePromptMessage = null;
      forcePromptForId = null;
      forcePromptIdentity = undefined;
      if (!isCurrentTerminalRoute(targetId)) return;
      navigate("/workspaces");
    } finally {
      removeDeletingWorkspaceTarget(targetId, targetHostKey);
      forceDeleting = false;
    }
  }

  function cancelForceDelete(): void {
    if (forceDeleting) return;
    forcePromptMessage = null;
    forcePromptForId = null;
    forcePromptIdentity = undefined;
  }

  async function watchFleetWorkspaceDiff(
    id: string,
    hostKey: string,
    signal: AbortSignal,
  ): Promise<void> {
    let version = "";
    let retryDelay = 1_000;
    while (!signal.aborted && isCurrentWorkspace(id, hostKey)) {
      try {
        const { data, response } = await client.GET(
          "/fleet/hosts/{host_key}/workspaces/{id}/diff/watch",
          {
            params: {
              path: { host_key: hostKey, id },
              query: version ? { version } : {},
            },
            signal,
          },
        );
        if (signal.aborted || !isCurrentWorkspace(id, hostKey)) return;
        const update = data as
          | { changed?: boolean; version?: string }
          | undefined;
        if (!response.ok) {
          if (!shouldRetryFleetDiffWatch(response.status)) return;
          await waitForFleetDiffWatchRetry(signal, retryDelay);
          retryDelay = Math.min(retryDelay * 2, 30_000);
          continue;
        }
        // A successful but incompatible response will not become valid by
        // retrying forever. Leave request-driven diff loading in place for
        // older fleet members that do not implement the watch contract.
        if (typeof update?.version !== "string" || update.version === "") return;
        retryDelay = 1_000;
        version = update.version;
        if (update.changed && version !== lastDiffSnapshotVersion) {
          lastDiffSnapshotVersion = version;
          diffRefreshToken += 1;
        }
      } catch {
        if (signal.aborted || !isCurrentWorkspace(id, hostKey)) return;
        await waitForFleetDiffWatchRetry(signal, retryDelay);
        retryDelay = Math.min(retryDelay * 2, 30_000);
      }
    }
  }

  function waitForFleetDiffWatchRetry(signal: AbortSignal, delay: number): Promise<void> {
    if (signal.aborted) return Promise.resolve();
    const jitteredDelay = Math.round(delay * (0.8 + Math.random() * 0.4));
    return new Promise((resolve) => {
      const done = () => {
        window.clearTimeout(timeout);
        signal.removeEventListener("abort", done);
        resolve();
      };
      const timeout = window.setTimeout(done, jitteredDelay);
      signal.addEventListener("abort", done, { once: true });
    });
  }

  let previouslyFocusedEl: HTMLElement | null = null;

  $effect(() => {
    if (renamePrompt !== null) {
      void tick().then(() => {
        renameInputEl?.focus();
        renameInputEl?.select();
      });
    } else if (!modalOpen && previouslyFocusedEl !== null) {
      const triggerEl = previouslyFocusedEl;
      previouslyFocusedEl = null;
      void tick().then(() => {
        if (document.contains(triggerEl)) {
          triggerEl.focus();
        }
      });
    }
  });

  $effect(() => {
    if (!workspace) return;
    if (!isSidebarTabSupported(workspace, sidebarTab)) {
      sidebarTab = defaultSidebarTab();
    }
  });

  // React to workspaceId changes (including / from "" on the
  // bare /workspaces route) without remounting the entire view.
  // Removing the {#key} that previously wrapped this component in
  // App.svelte means the lifecycle is now driven entirely by this
  // effect.
  //
  // Keep the previous workspace and runtime available to the workflow
  // stage until their replacements arrive. The right sidebar gates on
  // runtimeLive separately, so it cannot mix those retained values with
  // the newly selected route.
  $effect(() => {
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    if (
      appliedRuntimeState?.workspaceId !== id ||
      appliedRuntimeState.hostKey !== hostKey
    ) {
      appliedRuntimeState = null;
    }
    // Route selection is the zero point for workspace-switch timing:
    // every workspace-switch:* measure is a duration from this call.
    // The token lets the cleanup below cancel exactly this switch when
    // the user leaves the workspace surface entirely, without being
    // able to cancel a newer switch begun elsewhere.
    let switchToken: string | null = null;
    if (id) {
      switchToken = beginWorkspaceSwitch(id, hostKey);
    } else {
      cancelWorkspaceSwitch();
    }
    const storageId = id ? workspaceStorageId(id, hostKey) : "";
    const restoredLayout = id ? loadTerminalLayout(storageId) : defaultTerminalLayout();
    const restoredTab = restoreWorkspaceTab(storageId);
    const restoredActiveTab =
      restoredTab === "terminal" &&
      !(restoredLayout.open && restoredLayout.dock === "top")
        ? "home"
        : restoredTab;
    const layoutForActiveTab =
      restoredActiveTab === "home"
        ? restoredLayout
        : { ...restoredLayout, workflowMode: "tabs" as const };

    // Tab state from the previous workspace can't be valid for a
    // different workspace's runtime, so reset these even though
    // workspace/runtime themselves are kept.
    restoreWorkspaceTabSelection(restoredActiveTab);
    terminalLayout = layoutForActiveTab;
    terminalLayoutWorkspaceId = storageId;
    launchingKey = null;
    terminalLaunching = false;
    closedSessions = [];
    // Retry/refresh requests guard their own state mutations on
    // isCurrentWorkspace, so an in-flight one that resolves after a
    // route change skips its finally cleanup and would otherwise
    // leave these flags stuck true on the next workspace.
    retryingSetup = false;
    refreshingWorkspace = false;
    lastDiffSnapshotVersion = "";

    // Errors/transient flags from the prior workspace should not
    // bleed across — clear them but don't touch workspace/runtime.
    loadError = null;
    runtimeError = null;
    // A 409 force-delete prompt is bound to the workspace that
    // produced it. Dismiss it on any route change so the user
    // can't confirm a destructive action targeting a workspace
    // they're no longer looking at. Bumping the generation token
    // also invalidates any in-flight DELETE callback that captured
    // the previous value.
    forcePromptMessage = null;
    forcePromptForId = null;
    forcePromptIdentity = undefined;
    stopPromptSession = null;
    stopSessionStopping = false;
    renamePrompt = null;
    renameInputValue = "";
    renameSaving = false;
    workspaceGen += 1;
    mountedSessionKeys = restoredActiveTab.startsWith("session:")
      ? [restoredActiveTab.slice("session:".length)]
      : [];

    if (!id) {
      // /workspaces route: drop workspace data so the empty-state
      // message renders rather than continuing to show whatever
      // the previous /terminal/{id} session left behind.
      workspace = null;
      runtime = null;
      runtimeForId = "";
      runtimeForHostKey = undefined;
      stopRuntimePolling();
      return;
    }

    const fleetDiffWatchAbort = hostKey ? new AbortController() : null;
    if (hostKey && fleetDiffWatchAbort) {
      void watchFleetWorkspaceDiff(id, hostKey, fleetDiffWatchAbort.signal);
    }

    const evtUrl = new URL(`${basePath}/api/v1/events`, window.location.origin);
    if (!hostKey) {
      evtUrl.searchParams.set("workspace_id", id);
    }
    const source = new EventSource(`${evtUrl.pathname}${evtUrl.search}`);
    eventSource = source;
    const workspaceRequest = fetchWorkspace();
    void fetchRuntime();

    source.addEventListener(
      "workspace_status",
      (e: MessageEvent) => {
        try {
          const data = JSON.parse(
            e.data as string,
          ) as { id?: string };
          if (!data.id || data.id === id) {
            void fetchWorkspace();
          }
        } catch {
          // Malformed SSE data; ignore.
        }
      },
    );
    source.addEventListener("open", () => {
      void workspaceRequest.then(() => {
        if (
          eventSource === source &&
          isCurrentWorkspace(id, hostKey) &&
          workspace?.id === id &&
          selectedWorkspaceHostKey(workspace) === hostKey &&
          workspace.enrichment_status === "pending"
        ) {
          void fetchWorkspace();
        }
      });
    });
    source.addEventListener(
      "workspace_pr_associated",
      (e: MessageEvent) => {
        try {
          const data = JSON.parse(
            e.data as string,
          ) as { workspace_id?: string };
          if (data.workspace_id === id) {
            void fetchWorkspace();
          }
        } catch {
          // Malformed SSE data; ignore.
        }
      },
    );
    source.addEventListener("reconnect.stale", () => {
      void fetchWorkspace();
      void fetchRuntime();
      diffRefreshToken += 1;
    });
    const refreshPreparedDiff = (e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data as string) as {
          workspace_id?: string;
          version?: string;
        };
        if (
          data.workspace_id === id &&
          typeof data.version === "string" &&
          data.version !== "" &&
          data.version !== lastDiffSnapshotVersion
        ) {
          lastDiffSnapshotVersion = data.version;
          diffRefreshToken += 1;
        }
      } catch {
        // Malformed SSE data; ignore.
      }
    };
    source.addEventListener("workspace_diff_ready", refreshPreparedDiff);
    source.addEventListener("workspace_diff_changed", refreshPreparedDiff);

    void workspaceRequest.then(() => {
      if (workspace?.status === "creating") {
        startPolling();
      } else if (workspace?.status === "ready") {
        startRuntimePolling();
      }
    });

    return () => {
      stopPolling();
      stopRuntimePolling();
      fleetDiffWatchAbort?.abort();
      source.close();
      if (eventSource === source) {
        eventSource = null;
      }
      // Leaving the workspace surface (view unmount) must end the
      // switch so late responses and pane callbacks cannot append
      // phases. On a switch to another workspace this cleanup runs
      // just before the next effect run, cancelling the old switch the
      // new beginWorkspaceSwitch was about to supersede anyway; the
      // token guard only prevents cancelling a switch this run does
      // not own.
      if (switchToken !== null) {
        cancelWorkspaceSwitch(switchToken);
      }
    };
  });

  $effect(() => {
    if (
      workspaceId ||
      workspaceListState.status !== "loaded" ||
      workspaceListState.total !== 0
    ) {
      return;
    }
    void loadEmptyLaunchTargets();
  });

  $effect(() => {
    if (!workspaceId || !runtimeLive || workspace?.status !== "ready") return;
    if (actionsBlocked || launchingKey !== null) return;
    const targetKey = pendingWorkspaceLaunchTarget(workspaceId);
    if (targetKey === null) return;
    if (runtimeSessions.length > 0) {
      consumeWorkspaceLaunch(workspaceId);
      return;
    }
    const target = launchTargets.find(
      (candidate) => candidate.key === targetKey,
    );
    if (!target || target.kind !== "agent" || !target.available) {
      consumeWorkspaceLaunch(workspaceId);
      const reason =
        target?.disabled_reason ?? "is not available in this workspace";
      showFlash(`Agent "${targetKey}" could not launch: ${reason}`, {
        tone: "danger",
      });
      return;
    }
    if (consumeWorkspaceLaunch(workspaceId) === null) return;
    void handleLaunch(targetKey);
  });
</script>

<div
  class="terminal-view"
  inert={modalOpen}
  onkeydowncapture={(event) => {
    if (terminalSettingsReady && !terminalOptionsSaving) {
      terminalZoom.handleKeydown(event);
    }
  }}
>
  {#snippet inlineCollapseControl()}
    <!-- Collapsing the inline dock is pure local UI and must stay
         reachable in every workspace state: the toolbar that carries the
         dock controls only renders once the workspace is ready, so slow
         setup, a fetch failure, or a setup error would otherwise leave
         the dock permanently open short of deleting the workspace or
         navigating away. -->
    {#if inlineDock && inlineDockMode !== null}
      <button class="retry-btn" onclick={() => inlineDock?.setMode("collapsed")}>
        Collapse Terminal
      </button>
    {/if}
  {/snippet}
  {#snippet terminalMainContent()}
    <div class="terminal-main">
      {#if !workspaceId}
        {#if workspaceListState.status === "loaded" && workspaceListState.total === 0}
          <section class="workspace-zero-state" aria-label="Workspaces empty state">
            <div class="workspace-zero-copy">
              <p class="workspace-zero-eyebrow">Workspaces</p>
              <h2>Create a workspace to run agents on a branch</h2>
              <p>
                Workspaces are git worktrees: a PR workspace checks out the PR
                head, while issue-backed and unplanned work start from the
                repository's default branch.
              </p>
              <p>
                From a PR or issue, use the
                <span
                  class="workspace-zero-inline-action"
                  aria-label="Create Workspace example"
                >
                  <Button
                    class="workspace-zero-create-button"
                    disabled
                    tone="info"
                    surface="soft"
                    size="sm"
                    title="Create a PR or issue worktree, then open Workspaces to launch agents, shells, or local review sessions on that branch."
                    label="Create Workspace"
                    shortLabel="Create Workspace"
                  >
                    <PackagePlusIcon
                      size="14"
                      strokeWidth="2.2"
                      aria-hidden="true"
                    />
                  </Button>
                </span>
                button to launch a workspace.
              </p>
              <p>
                For unplanned work, use New workspace in the sidebar to pick a
                tracked repository and branch from its default head.
              </p>
              <p>
                Once it exists, this pane can start agents, local review
                sessions, or a shell inside that worktree.
              </p>
            </div>
            <div class="workspace-zero-example-card" aria-label="Workspace workflow example">
              <div class="workspace-zero-example" aria-label="Launch surface example">
                <span class="workspace-zero-example-label">
                  You can then launch configured agents via the buttons provided
                </span>
                {#if emptyLaunchTargets.length > 0}
                  <WorkspaceHome
                    launchTargets={emptyLaunchTargets}
                    sessions={[]}
                    readonly
                    showHeader={false}
                  />
                {:else if emptyLaunchTargetsState === "error"}
                  <p class="workspace-zero-example-empty">
                    Launch targets could not be loaded.
                  </p>
                {:else if emptyLaunchTargetsState === "loaded"}
                  <p class="workspace-zero-example-empty">
                    Launch targets appear here after agent tools are configured
                    or detected.
                  </p>
                {:else}
                  <p class="workspace-zero-example-empty">
                    Loading launch targets...
                  </p>
                {/if}
              </div>
            </div>
          </section>
        {:else}
          <div class="state-message">
            Select a workspace from the sidebar
          </div>
        {/if}
      {:else if loadError && !workspaceLive}
        <div class="state-message error">
          <span
            class="error-icon-badge"
            role="img"
            aria-label="Workspace load failed"
          >
            <AlertIcon
              class="error-icon"
              size="14"
              strokeWidth="2.4"
              aria-hidden="true"
            />
          </span>
          <span>{loadError}</span>
          <button
            class="retry-btn"
            onclick={() => {
              loadError = null;
              void fetchWorkspace();
            }}
          >
            Retry
          </button>
          {@render inlineCollapseControl()}
        </div>
      {:else if !workspace || !workspaceLive || workspace.status === "creating"}
        <!-- Liveness, not mere presence: during an in-place A→B switch the
             previous workspace stays cached while B loads, and rendering
             its ready toolbar here would show a stale header with action
             guards engaged — an uncollapsible dock if B is slow or fails. -->
        <div class="state-message">
          <Spinner size={18} />
          <span>Setting up workspace...</span>
          {@render inlineCollapseControl()}
        </div>
      {:else if workspace.status === "error"}
        <div class="state-message error">
          <span
            class="error-icon-badge"
            role="img"
            aria-label="Workspace setup failed"
          >
            <AlertIcon
              class="error-icon"
              size="14"
              strokeWidth="2.4"
              aria-hidden="true"
            />
          </span>
          <span>
            {workspace.error_message ??
              "Workspace setup failed"}
          </span>
          <button
            class="retry-btn"
            disabled={actionsBlocked || retryingSetup}
            onclick={() => void handleRetrySetup()}
          >
            Retry
          </button>
          <button
            class="retry-btn danger"
            disabled={actionsBlocked}
            onclick={(event) =>
              void handleDelete(event.currentTarget)}
          >
            Delete
          </button>
          {@render inlineCollapseControl()}
        </div>
      {:else}
        <div class="header-bar">
          <div class="header-start">
            <span class="header-name">
              {displayName(workspace)}
            </span>
            <code class="header-branch">
              {workspace.git_head_ref}
            </code>
          </div>
          <div class="header-end">
            {#if !hideRightSidebar}
              <div class="panel-toggle-group">
                <button
                  class="panel-toggle-btn"
                  class:active={sidebarOpen && sidebarTab === "diff"}
                  disabled={actionsBlocked}
                  onclick={() => handleSidebarToggleClick("diff")}
                >
                  Diff
                </button>
                {#if workspace.item_type === "issue"}
                  <button
                    class="panel-toggle-btn"
                    class:active={sidebarOpen && sidebarTab === "issue"}
                    disabled={actionsBlocked}
                    onclick={() => handleSidebarToggleClick("issue")}
                  >
                    Issue
                  </button>
                {/if}
                {#if workspace.item_type === "kata_task"}
                  <button
                    class="panel-toggle-btn"
                    class:active={sidebarOpen && sidebarTab === "kata_task"}
                    disabled={actionsBlocked}
                    onclick={() => handleSidebarToggleClick("kata_task")}
                  >
                    Kata task
                  </button>
                {/if}
                {#if getWorkspacePRNumber(workspace) !== null}
                  <button
                    class="panel-toggle-btn"
                    class:active={sidebarOpen && sidebarTab === "pr"}
                    disabled={actionsBlocked}
                    onclick={() => handleSidebarToggleClick("pr")}
                  >
                    PR
                  </button>
                {/if}
                {#if workspace.item_type === "pull_request"}
                  <button
                    class="panel-toggle-btn"
                    class:active={sidebarOpen && sidebarTab === "reviews"}
                    disabled={actionsBlocked}
                    onclick={() => handleSidebarToggleClick("reviews")}
                  >
                    Reviews
                  </button>
                {/if}
              </div>
              <IconButton
                class="workspace-refresh-button"
                size="sm"
                disabled={actionsBlocked || refreshingWorkspace}
                ariaLabel="Refresh workspace details"
                onclick={() => void handleRefreshWorkspace()}
              >
                {#if refreshingWorkspace}
                  <Spinner size={14} label="Refreshing workspace" />
                {:else}
                  <RefreshIcon
                    class="header-icon"
                    size="14"
                    strokeWidth="2.2"
                    aria-hidden="true"
                  />
                {/if}
              </IconButton>
            {/if}
            {#if inlineDock && inlineDockMode !== null}
              <!-- Dock mode changes are pure local UI: they must stay
                   available while server-side actions are blocked
                   (deletes in flight), or the dock cannot be collapsed
                   out of the way. Only the modal guard applies, and only
                   to the expand direction. -->
              <button
                class="header-btn"
                disabled={inlineDockMode !== "expanded" && inlineDockExpandBlocked}
                title={
                  inlineDockMode !== "expanded" && inlineDockExpandBlocked
                    ? "Close the open dialog first."
                    : undefined
                }
                onclick={() =>
                  inlineDock?.setMode(inlineDockMode === "expanded" ? "split" : "expanded")}
              >
                {#if inlineDockMode === "expanded"}
                  <ChevronsDownIcon size="14" strokeWidth="2.2" aria-hidden="true" />
                  Show Details
                {:else}
                  <ChevronsUpIcon size="14" strokeWidth="2.2" aria-hidden="true" />
                  Expand Terminal
                {/if}
              </button>
              <button
                class="header-btn"
                onclick={() => inlineDock?.setMode("collapsed")}
              >
                <PanelBottomCloseIcon size="14" strokeWidth="2.2" aria-hidden="true" />
                Collapse Terminal
              </button>
            {/if}
            <button
              class="header-btn danger"
              disabled={actionsBlocked}
              onclick={(event) =>
                void handleDelete(event.currentTarget)}
            >
              Delete
            </button>
          </div>
        </div>
        <div
          class="terminal-and-sidebar"
          bind:this={containerEl}
          bind:clientWidth={containerWidth}
        >
          <div class="terminal-area">
            <div class="workspace-surface">
              {#if !controlsInPane}
                <!-- Kept for the standalone Workspaces tab, whose panes have no tab
                     strip to hold the controls, and for a flattened detail surface,
                     which suppresses per-leaf chrome. Otherwise the pane's own
                     popover renders these, and a bar here would be a second copy of
                     them above the terminal. -->
                <div class="workspace-toolbar">
                  <div class="workspace-toolbar-title">Workflow</div>
                  <div class="workspace-actions">{@render workspaceControls()}</div>
                </div>
              {/if}
              {#if runtimeError}
                <div class="runtime-error">{runtimeError}</div>
              {/if}
              <div
                class="workspace-stage"
                role="region"
                aria-label="Workflow panes"
                ondragover={handleWorkflowDragOver}
                ondrop={handleWorkflowDrop}
              >
                {#if !runtimeLive}
                  <div class="state-message">
                    <Spinner size={18} />
                    <span>Loading workspace runtime...</span>
                  </div>
                {:else}
                  {#if renderedWorkflowTree}
                    <WorkflowSplitTree
                      {workspaceId}
                      dragScope={surfaceLayout?.dragScope}
                      promotion={workflowPromotion}
                      node={renderedWorkflowTree}
                      tabs={workflowTabDescriptors}
                      {activeTabKey}
                      disabled={actionsBlocked}
                      onSelectTab={(tabKey) => {
                        if (tabKey === "terminal") {
                          terminalLayout = { ...terminalLayout, open: true };
                        }
                        const sessionKey = sessionKeyFromWorkflowTab(tabKey);
                        if (sessionKey) mountSessionTerminal(sessionKey);
                        selectWorkspaceTab(tabKey);
                      }}
                      onMoveTabBefore={moveWorkflowTabBeforeTarget}
                      onAppendTabToLeaf={appendWorkflowTabToGroup}
                      onSplitTab={splitWorkflowTabIntoGroup}
                      onMoveTabToTerminal={moveWorkflowTabToTerminal}
                      onCloseTab={closeWorkflowTab}
                      onRenameTab={renameWorkflowTab}
                      onRatioChange={(splitId, ratio) => {
                        terminalLayout = {
                          ...terminalLayout,
                          workflowTree: updateWorkflowSplitRatio(
                            terminalLayout.workflowTree,
                            splitId,
                            ratio,
                          ),
                        };
                      }}
                    >
                      {#snippet renderTab(tabKey, active)}
                        {#if tabKey === "home"}
                          {#if workspace}
                            <WorkspaceHome
                              {workspace}
                              launchTargets={launchTargets}
                              sessions={runtimeSessions}
                              displayLabels={sessionDisplayLabels}
                              {launchingKey}
                              readonly={actionsBlocked}
                              onLaunch={(key) => void handleLaunch(key)}
                              onOpenSession={openSession}
                            />
                          {/if}
                        {:else if tabKey === "terminal" && terminalPanelInStage}
                          <DockedTerminalPanel
                            {workspaceId}
                            {workspaceHostKey}
                            sessions={terminalSessions}
                            displayLabels={sessionDisplayLabels}
                            tree={dockTree}
                            activeSessionKey={terminalLayout.activeSessionKey}
                            open={terminalLayout.open}
                            dock={terminalLayout.dock}
                            height={terminalLayout.height}
                            loading={terminalLaunching}
                            disabled={actionsBlocked}
                            {hostVisible}
                            onToggle={() => void toggleTerminalPanel()}
                            onNewTerminal={() => void launchTerminalSession()}
                            onSplit={(direction) => void splitTerminal(direction)}
                            onSelect={selectTerminalSession}
                            onClose={(session) => void closeSession(session)}
                            onRename={renameSession}
                            onMoveToWorkflow={moveSessionToWorkflow}
                            onPromoteSession={promoteSessionToPane}
                            onDock={dockTerminalPanel}
                            onResize={resizeTerminalPanel}
                            onDropSession={moveSessionToTerminal}
                            onSplitSession={splitTerminalSessionIntoPane}
                            onRatioChange={(splitId, ratio) => {
                              updateActiveTerminalTree(
                                updateSplitRatio(
                                  terminalLayout.tree,
                                  splitId,
                                  ratio,
                                ),
                              );
                            }}
                          />
                        {:else}
                          {@const sessionKey = sessionKeyFromWorkflowTab(tabKey)}
                          {@const session = runtimeSessions.find(
                            (candidate) => candidate.key === sessionKey,
                          )}
                          {#if session && isSessionTerminalMounted(session.key)}
                            <SessionTerminalSlot
                              hostKey={sessionHostKeyFor(session)}
                              visible={active && hostVisible}
                            />
                          {/if}
                        {/if}
                      {/snippet}
                    </WorkflowSplitTree>
                  {/if}
                {/if}
              </div>
              {#if terminalLayout.dock === "bottom"}
                <DockedTerminalPanel
                  {workspaceId}
                  {workspaceHostKey}
                  sessions={terminalSessions}
                  displayLabels={sessionDisplayLabels}
                  tree={dockTree}
                  activeSessionKey={terminalLayout.activeSessionKey}
                  open={terminalLayout.open}
                  dock={terminalLayout.dock}
                  height={terminalLayout.height}
                  loading={terminalLaunching}
                  disabled={actionsBlocked}
                  {hostVisible}
                  onToggle={() => void toggleTerminalPanel()}
                  onNewTerminal={() => void launchTerminalSession()}
                  onSplit={(direction) => void splitTerminal(direction)}
                  onSelect={selectTerminalSession}
                  onClose={(session) => void closeSession(session)}
                  onRename={renameSession}
                  onMoveToWorkflow={moveSessionToWorkflow}
                  onPromoteSession={promoteSessionToPane}
                  onDock={dockTerminalPanel}
                  onResize={resizeTerminalPanel}
                  onDropSession={moveSessionToTerminal}
                  onSplitSession={splitTerminalSessionIntoPane}
                  onRatioChange={(splitId, ratio) => {
                    updateActiveTerminalTree(
                      updateSplitRatio(
                        terminalLayout.tree,
                        splitId,
                        ratio,
                      ),
                    );
                  }}
                />
              {/if}
            </div>
          </div>
          {#if sidebarOpen && !hideRightSidebar}
            <SplitResizeHandle
              class="sidebar-resize-handle"
              ariaLabel="Resize workspace details"
              orientation="horizontal"
              ariaValueMin={rightSidebarAriaMin}
              ariaValueMax={rightSidebarAriaMax}
              ariaValueNow={sidebarWidth}
              onResizeStart={handleSidebarResizeStart}
              onResize={handleSidebarResize}
            />
            <div
              class="right-sidebar"
              style="width: {sidebarWidth}px"
            >
              {#if workspaceDetailsReady && workspace}
                {#snippet kataTaskPanel()}
                  {@const sidebarWorkspace = workspace}
                  {#if sidebarWorkspace?.kata}
                    <KataWorkspaceSidebarPane
                      kata={sidebarWorkspace.kata}
                      disabled={actionsBlocked}
                    />
                  {:else}
                    <EmptyState title="No linked Kata task" />
                  {/if}
                {/snippet}
                <WorkspaceRightSidebar
                  activeTab={sidebarTab}
                  workspaceID={workspace.id}
                  workspaceHostKey={selectedWorkspaceHostKey(workspace)}
                  provider={workspace.repo.provider}
                  platformHost={workspace.repo.platform_host}
                  repoOwner={workspace.repo.owner}
                  repoName={workspace.repo.name}
                  repoPath={workspace.repo.repo_path}
                  ownerItemType={workspace.item_type}
                  ownerItemNumber={workspace.item_number}
                  associatedPRNumber={getWorkspacePRNumber(workspace)}
                  branch={workspace.git_head_ref}
                  roborevBaseUrl={basePath + "/api/roborev"}
                  refreshToken={sidebarRefreshToken}
                  {diffRefreshToken}
                  disabled={actionsBlocked}
                  {kataTaskPanel}
                />
              {:else}
                <div class="state-message">
                  <Spinner size={18} />
                  <span>Loading workspace details...</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    </div>
  {/snippet}

  <CollapsibleSidebar
    isCollapsed={isSidebarCollapsed}
    hideSidebar={hideWorkspaceList}
    sidebarWidth={currentWorkspaceListWidth}
    minSidebarWidth={MIN_WORKSPACE_LIST_WIDTH}
    maxSidebarWidth={MAX_WORKSPACE_LIST_WIDTH}
    onSidebarResize={handleWorkspaceListResize}
    overlay={isNarrow()}
    showCollapsedStrip={isSidebarToggleEnabled}
    onExpand={onToggleSidebar}
    mainOverflow="hidden"
  >
    {#snippet sidebar()}
      <WorkspaceListSidebar
        selectedId={workspaceId}
        selectedHostKey={workspaceHostKey}
        {isSidebarToggleEnabled}
        {hostVisible}
        onCollapseSidebar={onToggleSidebar}
        onOpenItemSidebar={openItemSidebar}
        onWorkspaceListStateChange={updateWorkspaceListState}
        isWorkspaceActionDisabled={(id, hostKey) =>
          deletingWorkspaceTargets.some((target) =>
            isDeletingWorkspaceTarget(target, id, hostKey),
          )}
        onWorkspaceDeletePendingChange={(id, hostKey, pending) => {
          if (pending) {
            addDeletingWorkspaceTarget(id, hostKey);
          } else {
            removeDeletingWorkspaceTarget(id, hostKey);
          }
        }}
        {onWorkspaceDeleted}
      />
    {/snippet}
    {@render terminalMainContent()}
  </CollapsibleSidebar>
</div>

{#if launcherMode && workspace !== null}
  <WorkspaceLauncherOverlay
    open={launcherOpen && hostVisible}
    {workspace}
    launchTargets={launchTargets}
    sessions={runtimeSessions}
    displayLabels={sessionDisplayLabels}
    {launchingKey}
    readonly={actionsBlocked}
    onClose={closeLauncher}
    onLaunch={(key) => void handleLaunch(key)}
    onOpenSession={(sessionKey) => {
      closeLauncher();
      openSession(sessionKey);
    }}
  />
{/if}

{#if renamePrompt !== null && hostVisible}
  <Modal
    open={renamePrompt !== null && hostVisible}
    title="Rename tab"
    width={460}
    frameId="workspace-rename-session"
    onClose={cancelRenamePrompt}
  >
    <form
      id="rename-session-form"
      class="rename-form"
      onsubmit={(event) => {
        event.preventDefault();
        void saveRenamePrompt();
      }}
    >
      <p class="rename-message">
        Choose the label shown in the workflow and terminal panes.
      </p>
      <label class="rename-field">
        <span>Name</span>
        <input
          bind:this={renameInputEl}
          bind:value={renameInputValue}
          autocomplete="off"
          spellcheck="false"
        />
      </label>
    </form>

    {#snippet footer()}
      <DialogButton disabled={renameSaving} onclick={cancelRenamePrompt}>
        Cancel
      </DialogButton>
      <DialogButton
        type="submit"
        form="rename-session-form"
        tone="primary"
        disabled={renameSaving || renameInputValue.trim() === ""}
      >
        {renameSaving ? "Saving..." : "Save"}
      </DialogButton>
    {/snippet}
  </Modal>
{/if}

<ConfirmDialog
  open={stopPromptSession !== null && hostVisible}
  title={stopPromptSession
    ? `Stop ${stopPromptSession.label}?`
    : "Stop session?"}
  message="This will stop the running session and close its pane."
  hint="Any foreground command running inside this session may be interrupted."
  confirmLabel="Stop session"
  pendingLabel="Stopping…"
  busy={stopSessionStopping}
  tone="danger"
  frameId="workspace-stop-session"
  onCancel={cancelStopSession}
  onConfirm={() => void confirmStopSession()}
/>

<ConfirmDialog
  open={forcePromptMessage !== null && hostVisible}
  title="Force delete workspace?"
  message={forcePromptMessage ?? ""}
  hint="Force-deleting discards any uncommitted changes in the worktree. This cannot be undone."
  confirmLabel="Force delete"
  pendingLabel="Deleting…"
  busy={forceDeleting}
  tone="danger"
  frameId="workspace-force-delete"
  onCancel={cancelForceDelete}
  onConfirm={() => void confirmForceDelete()}
/>

<!-- The workspace's own controls, defined here because every one of them is wired
     to this view's state. In a detail pane the pane's popover renders this, so the
     controls follow the workspace without the state leaving the view. -->
{#snippet workspaceControls()}
  <WorkflowPresetMenu
    presets={workflowPresets}
    selectedPresetId={selectedWorkflowPresetId}
    applying={applyingWorkflowPreset}
    onSaveNew={saveWorkflowPreset}
    onUpdate={updateWorkflowPreset}
    onApply={(presetId) => void applyWorkflowPreset(presetId)}
    onDelete={deleteWorkflowPreset}
    disabled={actionsBlocked}
    {hostVisible}
  />
  <TerminalZoomControl
    fontSize={terminalFontSize}
    disabled={actionsBlocked || !terminalSettingsReady || terminalOptionsSaving}
    onDecrease={terminalZoom.decrease}
    onIncrease={terminalZoom.increase}
    onReset={terminalZoom.reset}
  />
  <TerminalOptionsMenu
    disabled={actionsBlocked || !terminalSettingsReady || terminalZoomSaving}
    {hostVisible}
    onSavingChange={(saving) => {
      // Owner-keyed like the other pending writes: single-flight per view, so
      // "done" clears the owner outright rather than only while its workspace is
      // still the one selected.
      terminalOptionsSavingFor = saving ? viewWorkspaceKey : null;
    }}
  />
  {#if launcherMode}
    <!-- One opener rather than the menu: the overlay is the launch surface in a
         pane, and a second copy of the target list inside a popover inside a tab
         strip is the stacking this mode exists to remove. -->
    <Button size="sm" surface="soft" tone="neutral" label="Launch session" onclick={openLauncher}>
      <PlayIcon size="13" strokeWidth="2" aria-hidden="true" />
      Launch
    </Button>
  {:else}
    <LaunchMenu
      launchTargets={launchTargets}
      {launchingKey}
      disabled={actionsBlocked}
      {hostVisible}
      onLaunch={(key) => void handleLaunch(key)}
    />
  {/if}
{/snippet}

<style>
  .terminal-view {
    display: flex;
    width: 100%;
    height: 100%;
    background: var(--bg-primary);
  }

  .terminal-main {
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    min-width: 0;
  }

  .state-message {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-4);
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-lg);
  }

  .state-message.error {
    color: var(--accent-red);
  }

  .workspace-zero-state {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    justify-content: center;
    gap: var(--space-6);
    flex: 1;
    min-width: 0;
    overflow: auto;
    padding: 56px 28px;
    color: var(--text-primary);
    background: var(--bg-primary);
    max-width: 560px;
    margin: 0 auto;
  }

  .workspace-zero-copy {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    min-width: 0;
  }

  .workspace-zero-eyebrow {
    margin: 0;
    color: var(--accent-green);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .workspace-zero-copy h2 {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-xl);
    font-weight: 650;
    line-height: 1.25;
  }

  .workspace-zero-copy p {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.5;
  }

  .workspace-zero-inline-action {
    display: inline-flex;
    vertical-align: middle;
    margin-left: 5px;
    transform: translateY(-1px);
  }

  .workspace-zero-example-card {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
    align-self: flex-start;
    min-width: 280px;
    width: min(620px, 100%);
    padding: 12px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }

  .workspace-zero-example {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.5;
  }

  .workspace-zero-example-label {
    align-self: flex-start;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .workspace-zero-inline-action :global(.workspace-zero-create-button:disabled) {
    cursor: default;
    opacity: 0.72;
  }

  .workspace-zero-example-empty {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.45;
  }

  .workspace-zero-example :global(.workspace-home) {
    width: 100%;
    height: auto;
    padding: 0;
    overflow: visible;
    background: transparent;
  }

  @media (max-width: 760px) {
    .workspace-zero-state {
      padding: 28px 18px;
      max-width: none;
    }

    .workspace-zero-example-card {
      min-width: 0;
      width: 100%;
    }
  }

  .error-icon-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--accent-red);
    color: var(--text-on-accent);
    font-size: var(--font-size-md);
    font-weight: 700;
    flex-shrink: 0;
  }

  :global(.error-icon) {
    display: block;
    width: 14px;
    height: 14px;
    overflow: visible;
  }

  .retry-btn {
    padding: 4px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .retry-btn:hover {
    background: var(--bg-surface-hover);
  }

  .retry-btn:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .retry-btn.danger:hover {
    background: var(--accent-red);
    border-color: var(--accent-red);
    color: var(--text-on-accent);
  }

  .header-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 34px;
    padding: 0 10px;
    background: var(--bg-surface);
    border-bottom: 1px solid var(--border-default);
    border-left: 1px solid var(--border-default);
    gap: var(--space-4);
    flex-shrink: 0;
  }

  .header-start {
    display: flex;
    align-items: center;
    gap: 8px;
    overflow: hidden;
  }

  .header-name {
    font-size: var(--font-size-md);
    font-weight: 600;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    letter-spacing: 0.005em;
  }

  .header-branch {
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    background: var(--bg-inset);
    padding: 1px 6px;
    border-radius: 3px;
    border: 1px solid var(--border-muted);
    white-space: nowrap;
    line-height: 1.5;
  }

  .header-end {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }

  .header-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 22px;
    padding: 0 10px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
    transition: background-color 80ms ease, color 80ms ease,
      border-color 80ms ease;
  }

  :global(.workspace-refresh-button.kit-icon-button) {
    border: 1px solid var(--border-default);
  }

  :global(.header-icon) {
    display: block;
  }

  .header-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: color-mix(in srgb, var(--text-muted) 40%, var(--border-default));
  }

  .header-btn:disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }

  .header-btn.danger:hover:not(:disabled) {
    background: var(--accent-red);
    color: var(--text-on-accent);
    border-color: var(--accent-red);
  }

  .terminal-area {
    flex: 1;
    overflow: hidden;
  }

  .workspace-surface {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    background: var(--bg-primary);
  }

  .workspace-toolbar {
    display: flex;
    align-items: stretch;
    justify-content: space-between;
    gap: var(--space-4);
    height: 30px;
    padding: 0 6px 0 0;
    border-bottom: 1px solid var(--border-default);
    border-left: 1px solid var(--border-default);
    background: var(--bg-inset);
    flex-shrink: 0;
  }

  .workspace-toolbar-title {
    display: inline-flex;
    align-items: center;
    padding: 0 10px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .workspace-actions {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
    padding-left: 6px;
    border-left: 1px solid var(--border-muted);
  }

  .runtime-error {
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-default);
    background: color-mix(in srgb, var(--accent-red) 12%, var(--bg-surface));
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .workspace-stage {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .panel-toggle-group {
    display: inline-flex;
    height: 22px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .panel-toggle-btn {
    display: inline-flex;
    align-items: center;
    padding: 0 10px;
    border: none;
    background: transparent;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 500;
    letter-spacing: 0.01em;
    cursor: pointer;
    font-family: inherit;
    transition: background-color 80ms ease, color 80ms ease;
  }

  .panel-toggle-btn + .panel-toggle-btn {
    border-left: 1px solid var(--border-default);
  }

  .panel-toggle-btn:hover:not(.active):not(:disabled) {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .panel-toggle-btn.active:not(:disabled) {
    background: var(--accent-blue);
    color: var(--text-on-accent);
    font-weight: 600;
  }

  .panel-toggle-btn:disabled {
    cursor: not-allowed;
    color: color-mix(in srgb, var(--text-muted) 75%, var(--bg-surface));
    background: var(--bg-surface);
    opacity: 1;
  }

  .panel-toggle-group .panel-toggle-btn.active:disabled {
    background: color-mix(in srgb, var(--text-muted) 28%, var(--bg-surface)) !important;
    color: color-mix(in srgb, var(--text-muted) 80%, var(--text-primary)) !important;
    box-shadow: inset 0 0 0 1px
      color-mix(in srgb, var(--text-muted) 35%, var(--border-muted));
  }

  .terminal-and-sidebar {
    flex: 1;
    display: flex;
    overflow: hidden;
  }

  .right-sidebar {
    position: relative;
    z-index: 2;
    flex-shrink: 0;
    overflow: hidden;
  }

  .right-sidebar:has(:global(.kit-modal-overlay)) {
    z-index: 80;
  }


  .rename-form {
    display: grid;
    gap: 12px;
    margin: 0;
  }

  .rename-message {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.45;
  }

  .rename-field {
    display: grid;
    gap: 6px;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .rename-field input {
    width: 100%;
    height: 34px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    padding: 0 10px;
    font: inherit;
    font-size: var(--font-size-sm);
    outline: none;
  }

  .rename-field input:focus {
    border-color: var(--accent-blue);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 22%, transparent);
  }

</style>
