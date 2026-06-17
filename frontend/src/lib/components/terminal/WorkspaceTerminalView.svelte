<script lang="ts">
  import { tick, untrack } from "svelte";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import { navigate } from "../../stores/router.svelte.ts";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";
  import TerminalPane from "./TerminalPane.svelte";
  import WorkspaceHome from "./WorkspaceHome.svelte";
  import LaunchMenu from "./LaunchMenu.svelte";
  import TerminalOptionsMenu from "./TerminalOptionsMenu.svelte";
  import DockedTerminalPanel from "./DockedTerminalPanel.svelte";
  import WorkflowSplitTree, {
    type WorkflowTabDescriptor,
  } from "./WorkflowSplitTree.svelte";
  import WorkflowPresetMenu from "./WorkflowPresetMenu.svelte";
  import type { RuntimeSession } from "@middleman/ui/api/types";
  import {
    getWorkspaceRuntime,
    launchWorkspaceSession,
    renameWorkspaceSession,
    stopWorkspaceSession,
    workspaceSessionWebSocketPath,
    type WorkspaceRuntimeState,
  } from "../../api/workspace-runtime.js";
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
  import {
    CollapsibleResizableSidebar,
    SplitResizeHandle,
    WorkspaceRightSidebar,
    type SplitResizeEvent,
  } from "@middleman/ui";
  import {
    AlertIcon,
    RefreshIcon,
    SpinnerIcon,
  } from "../../icons.ts";
  import { apiErrorMessage, client } from "../../api/runtime.js";
  import { showFlash } from "../../stores/flash.svelte.js";

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
    item_type: "pull_request" | "issue";
    item_number: number;
    git_head_ref: string;
    worktree_path: string;
    tmux_session: string;
    status: string;
    error_message?: string | null;
    created_at: string;
    mr_title?: string | null;
    mr_state?: string | null;
    mr_is_draft?: boolean | null;
    associated_pr_number?: number | null;
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
    sidebarWidth?: number;
    onSidebarResize?: (width: number) => void;
    isSidebarToggleEnabled?: boolean;
    onToggleSidebar?: () => void;
    hideWorkspaceList?: boolean;
    hideRightSidebar?: boolean;
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
  }: Props = $props();

  const basePath = (
    window.__BASE_PATH__ ?? "/"
  ).replace(/\/$/, "");

  let workspace = $state<Workspace | null>(null);
  let runtime = $state.raw<WorkspaceRuntimeState | null>(null);
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
  let actionError = $state<string | null>(null);
  let retryingSetup = $state(false);
  let refreshingWorkspace = $state(false);
  let sidebarRefreshToken = $state(0);
  let forcePromptMessage = $state<string | null>(null);
  let forcePromptForId = $state<string | null>(null);
  let forceDeleting = $state(false);
  let cancelForceBtnEl = $state<HTMLButtonElement | null>(null);
  let stopPromptSession = $state<RuntimeSession | null>(null);
  let stopSessionStopping = $state(false);
  let cancelStopBtnEl = $state<HTMLButtonElement | null>(null);
  let renamePrompt = $state<{
    sessionKey: string;
    originalLabel: string;
  } | null>(null);
  let renameInputValue = $state("");
  let renameSaving = $state(false);
  let renameInputEl = $state<HTMLInputElement | null>(null);
  // Bumps on every workspace route change. Async delete callbacks
  // capture this at request time and bail out if it has moved on,
  // covering the case where the user leaves and returns to the same
  // workspace before an in-flight response settles — an id check
  // alone would let a stale 409 reopen the prompt.
  let workspaceGen = 0;
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

  let workflowPresets = $state<WorkflowPreset[]>(loadWorkflowPresets());
  let selectedWorkflowPresetId = $state<string | null>(null);
  let applyingWorkflowPreset = $state(false);

  type SidebarTab = "diff" | "pr" | "issue" | "reviews";

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
  const runtimeLive = $derived(
    runtime !== null &&
      runtimeForId === workspaceId &&
      runtimeForHostKey === workspaceHostKey &&
      workspace?.id === workspaceId &&
      selectedWorkspaceHostKey(workspace) === workspaceHostKey,
  );
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
  const terminalSessions = $derived(
    runtimeSessions.filter(
      (session) => sessionRegion(session) === "terminal",
    ),
  );
  function upsertRuntimeSession(session: RuntimeSession): RuntimeSession[] {
    const sessions = [
      ...runtimeSessions.filter((candidate) => candidate.key !== session.key),
      session,
    ];
    if (runtimeLive && runtime) {
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
      (session) => sessionRegion(session) === "workflow",
    ),
  );
  const workflowTabDescriptors = $derived.by<WorkflowTabDescriptor[]>(() => {
    const tabs: WorkflowTabDescriptor[] = [
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
      tabs.push({
        key: workflowTabKeyForSession(session.key),
        label: sessionDisplayLabels[session.key] ?? session.label,
        kind: session.kind === "plain_shell" ? "plain_shell" : "agent",
        status: session.status,
        renamable: true,
        movableToTerminal: true,
        closable: true,
      });
    }
    return tabs;
  });
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
  const actionsBlocked = $derived(transitioning);
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

  function handleSegmentClick(tab: SidebarTab): void {
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

    handleSegmentClick(tab);
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
  $effect(() => {
    if (!containerEl || !sidebarOpen) return;

    clampRightSidebarWidth(containerEl.clientWidth);
  });

  $effect(() => {
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
        sidebarResizeStartWidth - event.deltaX,
      ),
    );
  }

  $effect(() => {
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

  async function fetchWorkspace(): Promise<void> {
    // Capture the id at call time. With workspaceId changing across
    // navigations, a slow in-flight fetch for the previous id could
    // otherwise resolve after a newer fetch and overwrite the new
    // workspace's data with stale content (causing a perceived flash
    // back to the previous workspace).
    const id = workspaceId;
    const hostKey = workspaceHostKey;
    try {
      if (hostKey) {
        const { data, error, response } = await client.GET(
          "/fleet/hosts/{host_key}/workspaces/{id}",
          {
            params: { path: { host_key: hostKey, id } },
          },
        );
        if (!isCurrentWorkspace(id, hostKey)) return;
        if (!data) {
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
        actionError = null;

        if (nextWorkspace.status !== "creating") {
          stopPolling();
        }
        if (nextWorkspace.status === "ready") {
          startRuntimePolling();
          void fetchRuntime();
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
      if (!isCurrentWorkspace(id, hostKey)) return;
      if (!data) {
        loadError = apiErrorMessage(
          error,
          `Failed to load workspace (${response.status})`,
        );
        return;
      }
      workspace = data as Workspace;
      syncSidebarTabForWorkspace(workspace);
      loadError = null;
      actionError = null;

      if (data.status !== "creating") {
        stopPolling();
      }
      if (data.status === "ready") {
        startRuntimePolling();
        void fetchRuntime();
      } else {
        stopRuntimePolling();
      }
    } catch (err) {
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
      try {
        const data = await getWorkspaceRuntime(id, hostKey);
        if (!isCurrentWorkspace(id, hostKey) || seq !== runtimeFetchSeq) return null;
        runtime = data;
        runtimeForId = id;
        runtimeForHostKey = hostKey;
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
          selectWorkspaceTab("home");
        }
        mountedSessionKeys = mountedSessionKeys.filter(
          (key) =>
            data.sessions.some((session) => session.key === key),
        );
        return data;
      } catch (err) {
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
    runtimeError = null;
    try {
      const session = await launchWorkspaceSession(
        id,
        targetKey,
        hostKey,
        "workflow",
      );
      if (!isCurrentWorkspace(id, hostKey)) return;
      await fetchRuntime({ force: true });
      if (!isCurrentWorkspace(id, hostKey)) return;
      clearClosedSession(session);
      moveSessionToWorkflow(session.key);
      mountSessionTerminal(session.key);
      selectWorkspaceTab(workflowTabKeyForSession(session.key));
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      runtimeError =
        err instanceof Error ? err.message : "Launch failed";
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
        selectWorkspaceTab("home");
      }
    } catch (err) {
      if (!isCurrentWorkspace(id, hostKey)) return;
      runtimeError =
        err instanceof Error ? err.message : "Stop failed";
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
      selectWorkspaceTab("home");
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
    runtimeError = null;
    try {
      const session = await launchWorkspaceSession(
        id,
        PLAIN_SHELL_TARGET,
        hostKey,
        "terminal",
      );
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
      runtimeError =
        err instanceof Error
          ? err.message
          : "Terminal launch failed";
    } finally {
      if (isCurrentWorkspace(id, hostKey)) terminalLaunching = false;
    }
    return null;
  }

  async function toggleTerminalPanel(): Promise<void> {
    if (terminalLayout.open) {
      terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
        ...terminalLayout,
        open: false,
      });
      if (activeTabKey === "terminal") {
        selectWorkspaceTab("home");
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
      selectWorkspaceTab("home");
    }
  }

  function moveSessionToWorkflow(sessionKey: string): void {
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
    if (tabKey === "terminal") {
      terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
        ...terminalLayout,
        open: false,
      });
      if (activeTabKey === "terminal") {
        selectWorkspaceTab("home");
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
    const sessionKey = sessionKeyFromWorkflowTab(tabKey);
    if (sessionKey !== null) {
      moveSessionToTerminal(sessionKey);
    }
  }

  function renameWorkflowTab(tabKey: WorkflowTabKey): void {
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
    runtimeError = null;
    try {
      const updated = await renameWorkspaceSession(
        id,
        sessionKey,
        trimmed,
        hostKey,
      );
      if (!isCurrentWorkspace(id, hostKey)) return;
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
      runtimeError =
        err instanceof Error ? err.message : "Rename failed";
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
    applyingWorkflowPreset = true;
    runtimeError = null;
    try {
      const keyMap: Record<string, string> = {};
      for (const spec of preset.sessions) {
        let session = await launchWorkspaceSession(
          id,
          spec.targetKey,
          hostKey,
          spec.region,
        );
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
      runtimeError =
        err instanceof Error ? err.message : "Preset launch failed";
    } finally {
      if (isCurrentWorkspace(id, hostKey)) applyingWorkflowPreset = false;
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
    terminalLayout = normalizeLayoutForSessions(runtimeSessions, {
      ...terminalLayout,
      dock,
      open: true,
    });
    if (dock === "top") {
      selectWorkspaceTab("terminal");
    } else if (activeTabKey === "terminal") {
      selectWorkspaceTab("home");
    }
  }

  function resizeTerminalPanel(height: number): void {
    terminalLayout = {
      ...terminalLayout,
      height: clampTerminalHeight(height),
    };
  }

  function updateActiveTerminalTree(tree: PaneNode | null): void {
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
    if (readDroppedSession(event) === null) return;
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
  }

  function handleWorkflowDrop(event: DragEvent): void {
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
    actionError = null;
    try {
      if (hostKey) {
        const { data, error, response } = await client.POST(
          "/fleet/hosts/{host_key}/workspaces/{id}/retry",
          {
            params: { path: { host_key: hostKey, id } },
          },
        );
        if (!data) {
          actionError = apiErrorMessage(
            error,
            `Retry failed (${response.status})`,
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
        actionError = apiErrorMessage(
          error,
          `Retry failed (${response.status})`,
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
      actionError =
        err instanceof Error
          ? err.message
          : "Retry failed";
    } finally {
      if (isCurrentWorkspace(id, hostKey)) retryingSetup = false;
    }
  }

  async function handleRefreshWorkspace(): Promise<void> {
    if (!workspace || refreshingWorkspace || actionsBlocked) return;

    const id = workspace.id;
    const hostKey = workspaceHostKey;
    refreshingWorkspace = true;
    actionError = null;
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
          actionError = apiErrorMessage(
            error,
            `Refresh failed (${response.status})`,
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
        actionError = message;
        showFlash(message);
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
      actionError = message;
      showFlash(message);
    } finally {
      if (isCurrentWorkspace(id, hostKey)) refreshingWorkspace = false;
    }
  }

  async function handleDelete(
    triggerEl: HTMLElement | null = null,
  ): Promise<void> {
    if (actionsBlocked) return;
    actionError = null;
    const targetId = workspaceId;
    const targetHostKey = workspaceHostKey;
    const targetGen = workspaceGen;
    // Capture the trigger synchronously: the click handler runs
    // before `inert` is applied to .terminal-view, so this is the
    // last point we can read the originating focused element. By
    // the time the post-await effect runs, the browser has cleared
    // focus to document.body.
    triggerEl ??=
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const { error, response } = targetHostKey
      ? await client.DELETE("/fleet/hosts/{host_key}/workspaces/{id}", {
          params: { path: { host_key: targetHostKey, id: targetId } },
        })
      : await client.DELETE("/workspaces/{id}", {
          params: { path: { id: targetId } },
        });
    // Different workspace now: the user has moved on and nothing
    // about this response applies.
    if (!isCurrentWorkspace(targetId, targetHostKey)) return;
    if (response.status === 409) {
      // A 409 that lands after the user briefly left and returned
      // to the same workspace would feel like an unrequested
      // prompt; suppress it on a generation mismatch and let the
      // user retry if they want.
      if (targetGen !== workspaceGen) return;
      previouslyFocusedEl = triggerEl;
      forcePromptForId = targetId;
      forcePromptMessage = apiErrorMessage(
        error,
        "Workspace has uncommitted changes.",
      );
      return;
    }
    if (!response.ok && response.status !== 204) {
      if (targetGen !== workspaceGen) return;
      actionError = apiErrorMessage(
        error,
        `Delete failed (${response.status})`,
      );
      return;
    }
    // Successful delete: the server destroyed this workspace and
    // the user is still looking at it. Navigate away even after
    // an A→B→A round trip — otherwise they'd be staring at a
    // workspace that no longer exists.
    if (!isCurrentTerminalRoute(targetId)) return;
    navigate("/workspaces");
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
    forceDeleting = true;
    actionError = null;
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
      // The force-delete on the server is destructive and runs to
      // completion either way; once the user has moved to a
      // different workspace we just drop the response on the
      // floor so navigate() doesn't pull them away.
      if (!isCurrentWorkspace(targetId, targetHostKey)) return;
      if (!response.ok && response.status !== 204) {
        if (targetGen !== workspaceGen) return;
        actionError = apiErrorMessage(
          error,
          `Delete failed (${response.status})`,
        );
        forcePromptMessage = null;
        forcePromptForId = null;
        return;
      }
      // Successful force-delete on the workspace the user is
      // viewing — navigate away even after an A→B→A round trip
      // so we don't leave them on a workspace the server just
      // destroyed.
      forcePromptMessage = null;
      forcePromptForId = null;
      if (!isCurrentTerminalRoute(targetId)) return;
      navigate("/workspaces");
    } finally {
      forceDeleting = false;
    }
  }

  function cancelForceDelete(): void {
    if (forceDeleting) return;
    forcePromptMessage = null;
    forcePromptForId = null;
  }

  let previouslyFocusedEl: HTMLElement | null = null;

  function cancelActiveModal(): void {
    if (renamePrompt !== null) {
      cancelRenamePrompt();
      return;
    }
    if (stopPromptSession !== null) {
      cancelStopSession();
      return;
    }
    cancelForceDelete();
  }

  function handleConfirmPromptKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      event.preventDefault();
      cancelActiveModal();
      return;
    }
    if (event.key !== "Tab") return;
    const container = event.currentTarget;
    if (!(container instanceof HTMLElement)) return;
    const dialog = container.querySelector("[role='dialog']");
    if (!(dialog instanceof HTMLElement)) return;
    const focusable = Array.from(
      dialog.querySelectorAll<HTMLElement>(
        "button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex='-1'])",
      ),
    );
    if (focusable.length === 0) return;
    const currentIndex = focusable.findIndex(
      (el) => el === document.activeElement,
    );
    const nextIndex = event.shiftKey
      ? currentIndex <= 0
        ? focusable.length - 1
        : currentIndex - 1
      : currentIndex < 0 ||
          currentIndex >= focusable.length - 1
        ? 0
        : currentIndex + 1;
    event.preventDefault();
    focusable[nextIndex]?.focus();
  }

  $effect(() => {
    if (renamePrompt !== null) {
      void tick().then(() => {
        renameInputEl?.focus();
        renameInputEl?.select();
      });
    } else if (stopPromptSession !== null) {
      void tick().then(() => cancelStopBtnEl?.focus());
    } else if (forcePromptMessage !== null) {
      void tick().then(() => cancelForceBtnEl?.focus());
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
    if (forcePromptMessage === null) return;
    return untrack(() =>
      pushModalFrame("workspace-force-delete", []),
    );
  });

  $effect(() => {
    if (stopPromptSession === null) return;
    return untrack(() =>
      pushModalFrame("workspace-stop-session", []),
    );
  });

  $effect(() => {
    if (renamePrompt === null) return;
    return untrack(() =>
      pushModalFrame("workspace-rename-session", []),
    );
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
  // Critically, this effect must NOT null out `workspace` or
  // `runtime` between switches: the right sidebar and stage area
  // both gate on those values being non-null, so clearing them
  // would unmount the right sidebar and replace the stage with the
  // "Setting up workspace…" spinner — the flash the user is trying
  // to avoid. Instead we let the previous workspace's data stay on
  // screen until the new fetchWorkspace() resolves and overwrites
  // it in place.
  $effect(() => {
    const id = workspaceId;
    const storageId = id ? workspaceStorageId(id, workspaceHostKey) : "";
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

    // Errors/transient flags from the prior workspace should not
    // bleed across — clear them but don't touch workspace/runtime.
    loadError = null;
    actionError = null;
    runtimeError = null;
    // A 409 force-delete prompt is bound to the workspace that
    // produced it. Dismiss it on any route change so the user
    // can't confirm a destructive action targeting a workspace
    // they're no longer looking at. Bumping the generation token
    // also invalidates any in-flight DELETE callback that captured
    // the previous value.
    forcePromptMessage = null;
    forcePromptForId = null;
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

    const evtUrl = `${basePath}/api/v1/events`;
    const source = new EventSource(evtUrl);
    eventSource = source;

    source.addEventListener(
      "workspace_status",
      (e: MessageEvent) => {
        try {
          const data = JSON.parse(
            e.data as string,
          ) as { id?: string };
          if (data.id === id) {
            void fetchWorkspace();
          }
        } catch {
          // Malformed SSE data; ignore.
        }
      },
    );
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
    });

    void fetchWorkspace().then(() => {
      if (workspace?.status === "creating") {
        startPolling();
      } else if (workspace?.status === "ready") {
        startRuntimePolling();
      }
    });

    return () => {
      stopPolling();
      stopRuntimePolling();
      source.close();
      if (eventSource === source) {
        eventSource = null;
      }
    };
  });
</script>

<div class="terminal-view" inert={modalOpen}>
  {#snippet terminalMainContent()}
    <div class="terminal-main">
      {#if !workspaceId}
        <div class="state-message">
          Select a workspace from the sidebar
        </div>
      {:else if loadError && !workspace}
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
        </div>
      {:else if !workspace || workspace.status === "creating"}
        <div class="state-message">
          <SpinnerIcon
            class="spinner"
            size="18"
            strokeWidth="2"
            aria-hidden="true"
          />
          <span>Setting up workspace...</span>
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
            disabled={retryingSetup}
            onclick={() => void handleRetrySetup()}
          >
            Retry
          </button>
          <button
            class="retry-btn danger"
            onclick={(event) =>
              void handleDelete(event.currentTarget)}
          >
            Delete
          </button>
          {#if actionError}
            <span class="action-error">{actionError}</span>
          {/if}
        </div>
      {:else}
        <div class="header-bar">
          <div class="header-left">
            <span class="header-name">
              {displayName(workspace)}
            </span>
            <code class="header-branch">
              {workspace.git_head_ref}
            </code>
          </div>
          <div class="header-right">
            {#if !hideRightSidebar}
              <div class="seg-control">
                <button
                  class="seg-btn"
                  class:active={sidebarOpen && sidebarTab === "diff"}
                  onclick={() => handleSegmentClick("diff")}
                >
                  Diff
                </button>
                {#if workspace.item_type === "issue"}
                  <button
                    class="seg-btn"
                    class:active={sidebarOpen && sidebarTab === "issue"}
                    onclick={() => handleSegmentClick("issue")}
                  >
                    Issue
                  </button>
                {/if}
                {#if getWorkspacePRNumber(workspace) !== null}
                  <button
                    class="seg-btn"
                    class:active={sidebarOpen && sidebarTab === "pr"}
                    onclick={() => handleSegmentClick("pr")}
                  >
                    PR
                  </button>
                {/if}
                {#if workspace.item_type === "pull_request"}
                  <button
                    class="seg-btn"
                    class:active={sidebarOpen && sidebarTab === "reviews"}
                    onclick={() => handleSegmentClick("reviews")}
                  >
                    Reviews
                  </button>
                {/if}
              </div>
              <button
                class="header-btn header-icon-btn"
                disabled={actionsBlocked || refreshingWorkspace}
                title="Refresh workspace details"
                aria-label="Refresh workspace details"
                onclick={() => void handleRefreshWorkspace()}
              >
                {#if refreshingWorkspace}
                  <SpinnerIcon
                    class="header-icon spinning"
                    size="14"
                    strokeWidth="2.2"
                    aria-hidden="true"
                  />
                {:else}
                  <RefreshIcon
                    class="header-icon"
                    size="14"
                    strokeWidth="2.2"
                    aria-hidden="true"
                  />
                {/if}
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
        <div class="terminal-and-sidebar" bind:this={containerEl}>
          <div class="terminal-area">
            <div class="workspace-surface">
              <div class="workspace-toolbar">
                <div class="workspace-toolbar-title">Workflow</div>
                <div class="workspace-actions">
                  <WorkflowPresetMenu
                    presets={workflowPresets}
                    selectedPresetId={selectedWorkflowPresetId}
                    applying={applyingWorkflowPreset}
                    onSaveNew={saveWorkflowPreset}
                    onUpdate={updateWorkflowPreset}
                    onApply={(presetId) => void applyWorkflowPreset(presetId)}
                    onDelete={deleteWorkflowPreset}
                  />
                  <TerminalOptionsMenu />
                  <LaunchMenu
                    launchTargets={launchTargets}
                    {launchingKey}
                    onLaunch={(key) => void handleLaunch(key)}
                  />
                </div>
              </div>
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
                    <SpinnerIcon
                      class="spinner"
                      size="18"
                      strokeWidth="2"
                      aria-hidden="true"
                    />
                    <span>Loading workspace runtime...</span>
                  </div>
                {:else}
                  {#if terminalLayout.workflowTree}
                    <WorkflowSplitTree
                      {workspaceId}
                      node={terminalLayout.workflowTree}
                      tabs={workflowTabDescriptors}
                      {activeTabKey}
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
                            tree={terminalLayout.tree}
                            activeSessionKey={terminalLayout.activeSessionKey}
                            open={terminalLayout.open}
                            dock={terminalLayout.dock}
                            height={terminalLayout.height}
                            loading={terminalLaunching}
                            onToggle={() => void toggleTerminalPanel()}
                            onNewTerminal={() => void launchTerminalSession()}
                            onSplit={(direction) => void splitTerminal(direction)}
                            onSelect={selectTerminalSession}
                            onClose={(session) => void closeSession(session)}
                            onRename={renameSession}
                            onMoveToWorkflow={moveSessionToWorkflow}
                            onDock={dockTerminalPanel}
                            onResize={resizeTerminalPanel}
                            onDropSession={moveSessionToTerminal}
                            onExit={handleSessionExit}
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
                            {#key session.key}
                              <TerminalPane
                                websocketPath={workspaceSessionWebSocketPath(
                                  workspaceId,
                                  session.key,
                                  workspaceHostKey,
                                )}
                                reconnectOnExit={false}
                                {active}
                                onExit={() => handleSessionExit(session)}
                                initialStatus={session.status}
                              />
                            {/key}
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
                  tree={terminalLayout.tree}
                  activeSessionKey={terminalLayout.activeSessionKey}
                  open={terminalLayout.open}
                  dock={terminalLayout.dock}
                  height={terminalLayout.height}
                  loading={terminalLaunching}
                  onToggle={() => void toggleTerminalPanel()}
                  onNewTerminal={() => void launchTerminalSession()}
                  onSplit={(direction) => void splitTerminal(direction)}
                  onSelect={selectTerminalSession}
                  onClose={(session) => void closeSession(session)}
                  onRename={renameSession}
                  onMoveToWorkflow={moveSessionToWorkflow}
                  onDock={dockTerminalPanel}
                  onResize={resizeTerminalPanel}
                  onDropSession={moveSessionToTerminal}
                  onExit={handleSessionExit}
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
          {#if sidebarOpen && workspace && !hideRightSidebar}
            <SplitResizeHandle
              class="sidebar-resize-handle"
              ariaLabel="Resize workspace details"
              onResizeStart={handleSidebarResizeStart}
              onResize={handleSidebarResize}
            />
            <div
              class="right-sidebar"
              style="width: {sidebarWidth}px"
            >
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
              />
            </div>
          {/if}
        </div>
      {/if}
    </div>
  {/snippet}

  {#if hideWorkspaceList}
    {@render terminalMainContent()}
  {:else}
    <CollapsibleResizableSidebar
      isCollapsed={isSidebarCollapsed}
      sidebarWidth={currentWorkspaceListWidth}
      minSidebarWidth={MIN_WORKSPACE_LIST_WIDTH}
      maxSidebarWidth={MAX_WORKSPACE_LIST_WIDTH}
      onSidebarResize={handleWorkspaceListResize}
      showCollapsedStrip={isSidebarToggleEnabled}
      onExpand={onToggleSidebar}
      mainOverflow="hidden"
    >
      {#snippet sidebar()}
        <WorkspaceListSidebar
          selectedId={workspaceId}
          selectedHostKey={workspaceHostKey}
          {isSidebarToggleEnabled}
          onCollapseSidebar={onToggleSidebar}
          onOpenItemSidebar={openItemSidebar}
        />
      {/snippet}
      {@render terminalMainContent()}
    </CollapsibleResizableSidebar>
  {/if}
</div>

{#if renamePrompt !== null}
  <div
    class="force-delete-backdrop"
    role="presentation"
    onkeydown={handleConfirmPromptKeydown}
  >
    <div
      class="force-delete-dialog rename-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="rename-session-title"
      aria-describedby="rename-session-message"
    >
      <form
        class="rename-form"
        onsubmit={(event) => {
          event.preventDefault();
          void saveRenamePrompt();
        }}
      >
        <div class="force-delete-header">
          <h2 id="rename-session-title">Rename tab</h2>
        </div>
        <p id="rename-session-message" class="force-delete-message">
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
        <div class="force-delete-actions">
          <button
            type="button"
            class="force-delete-cancel"
            disabled={renameSaving}
            onclick={cancelRenamePrompt}
          >
            Cancel
          </button>
          <button
            type="submit"
            class="rename-confirm"
            disabled={renameSaving || renameInputValue.trim() === ""}
          >
            {renameSaving ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

{#if stopPromptSession !== null}
  <div
    class="force-delete-backdrop"
    role="presentation"
    onkeydown={handleConfirmPromptKeydown}
  >
    <div
      class="force-delete-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="stop-session-title"
      aria-describedby="stop-session-message"
    >
      <div class="force-delete-header">
        <AlertIcon
          class="force-delete-icon"
          size="20"
          strokeWidth="2"
          aria-hidden="true"
        />
        <h2 id="stop-session-title">Stop {stopPromptSession.label}?</h2>
      </div>
      <p id="stop-session-message" class="force-delete-message">
        This will stop the running session and close its pane.
      </p>
      <p class="force-delete-hint">
        Any foreground command running inside this session may be interrupted.
      </p>
      <div class="force-delete-actions">
        <button
          type="button"
          class="force-delete-cancel"
          disabled={stopSessionStopping}
          bind:this={cancelStopBtnEl}
          onclick={cancelStopSession}
        >
          Cancel
        </button>
        <button
          type="button"
          class="force-delete-confirm"
          disabled={stopSessionStopping}
          onclick={() => void confirmStopSession()}
        >
          {stopSessionStopping ? "Stopping…" : "Stop session"}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if forcePromptMessage !== null}
  <div
    class="force-delete-backdrop"
    role="presentation"
    onkeydown={handleConfirmPromptKeydown}
  >
    <div
      class="force-delete-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="force-delete-title"
      aria-describedby="force-delete-message"
    >
      <div class="force-delete-header">
        <AlertIcon
          class="force-delete-icon"
          size="20"
          strokeWidth="2"
          aria-hidden="true"
        />
        <h2 id="force-delete-title">Force delete workspace?</h2>
      </div>
      <p id="force-delete-message" class="force-delete-message">
        {forcePromptMessage}
      </p>
      <p class="force-delete-hint">
        Force-deleting discards any uncommitted changes in the
        worktree. This cannot be undone.
      </p>
      <div class="force-delete-actions">
        <button
          type="button"
          class="force-delete-cancel"
          disabled={forceDeleting}
          bind:this={cancelForceBtnEl}
          onclick={cancelForceDelete}
        >
          Cancel
        </button>
        <button
          type="button"
          class="force-delete-confirm"
          disabled={forceDeleting}
          onclick={() => void confirmForceDelete()}
        >
          {forceDeleting ? "Deleting…" : "Force delete"}
        </button>
      </div>
    </div>
  </div>
{/if}

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
    gap: 10px;
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-lg);
  }

  .state-message.error {
    color: var(--accent-red);
  }

  .error-icon-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--accent-red);
    color: #fff;
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
    color: #fff;
  }

  .action-error {
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  :global(.spinner) {
    animation: spin 0.8s linear infinite;
    color: var(--text-muted);
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
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
    gap: 10px;
    flex-shrink: 0;
  }

  .header-left {
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

  .header-right {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }

  .header-btn {
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

  .header-icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    padding: 0;
  }

  :global(.header-icon) {
    display: block;
  }

  :global(.header-icon.spinning) {
    animation: spin 0.8s linear infinite;
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
    color: #fff;
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
    gap: 10px;
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

  .seg-control {
    display: inline-flex;
    height: 22px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .seg-btn {
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

  .seg-btn + .seg-btn {
    border-left: 1px solid var(--border-default);
  }

  .seg-btn:hover:not(.active) {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .seg-btn.active {
    background: var(--accent-blue);
    color: #fff;
    font-weight: 600;
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

  .right-sidebar:has(:global(.modal-overlay)) {
    z-index: 80;
  }

  .force-delete-backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    background: color-mix(in srgb, black 50%, transparent);
    backdrop-filter: blur(2px);
    animation: force-delete-fade 120ms ease-out;
  }

  .force-delete-dialog {
    width: min(420px, 100%);
    background: var(--bg-surface);
    color: var(--text-primary);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    box-shadow: 0 24px 80px rgb(0 0 0 / 35%);
    padding: 20px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    animation: force-delete-pop 160ms cubic-bezier(0.16, 1, 0.3, 1);
  }

  .rename-dialog {
    width: min(460px, 100%);
  }

  .rename-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    margin: 0;
  }

  .force-delete-header {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  :global(.force-delete-icon) {
    color: var(--accent-red);
    flex-shrink: 0;
  }

  .force-delete-header h2 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 600;
    color: var(--text-primary);
  }

  .force-delete-message {
    margin: 0;
    font-size: var(--font-size-md);
    color: var(--text-secondary);
    line-height: 1.5;
  }

  .force-delete-hint {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    line-height: 1.5;
  }

  .rename-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-secondary);
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

  .force-delete-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }

  .force-delete-cancel,
  .force-delete-confirm,
  .rename-confirm {
    height: 30px;
    padding: 0 14px;
    font-size: var(--font-size-sm);
    font-weight: 500;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: background-color 80ms ease, color 80ms ease,
      border-color 80ms ease;
  }

  .force-delete-cancel {
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    color: var(--text-secondary);
  }

  .force-delete-cancel:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .force-delete-confirm {
    background: var(--accent-red);
    border: 1px solid var(--accent-red);
    color: #fff;
    font-weight: 600;
  }

  .rename-confirm {
    background: var(--accent-blue);
    border: 1px solid var(--accent-blue);
    color: #fff;
    font-weight: 600;
  }

  .force-delete-confirm:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-red) 88%, black);
    border-color: color-mix(in srgb, var(--accent-red) 88%, black);
  }

  .rename-confirm:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-blue) 88%, black);
    border-color: color-mix(in srgb, var(--accent-blue) 88%, black);
  }

  .force-delete-cancel:disabled,
  .force-delete-confirm:disabled,
  .rename-confirm:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  @keyframes force-delete-fade {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  @keyframes force-delete-pop {
    from {
      opacity: 0;
      transform: scale(0.96) translateY(4px);
    }
    to {
      opacity: 1;
      transform: scale(1) translateY(0);
    }
  }
</style>
