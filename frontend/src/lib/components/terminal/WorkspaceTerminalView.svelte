<script lang="ts">
  import { tick, untrack } from "svelte";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import { navigate } from "../../stores/router.svelte.ts";
  import WorkspaceListSidebar from "./WorkspaceListSidebar.svelte";
  import TerminalPane from "./TerminalPane.svelte";
  import WorkspaceHome from "./WorkspaceHome.svelte";
  import WorkspaceTabs from "./WorkspaceTabs.svelte";
  import LaunchMenu from "./LaunchMenu.svelte";
  import TerminalOptionsMenu from "./TerminalOptionsMenu.svelte";
  import ShellDrawer from "./ShellDrawer.svelte";
  import type { RuntimeSession } from "@middleman/ui/api/types";
  import {
    ensureWorkspaceShell,
    getWorkspaceRuntime,
    launchWorkspaceSession,
    stopWorkspaceSession,
    workspaceSessionWebSocketPath,
    workspaceTmuxWebSocketPath,
    type WorkspaceRuntimeState,
  } from "../../api/workspace-runtime.js";
  import {
    CollapsibleResizableSidebar,
    SplitResizeHandle,
    WorkspaceRightSidebar,
    type SplitResizeEvent,
  } from "@middleman/ui";
  import { AlertIcon, SpinnerIcon } from "../../icons.ts";
  import { apiErrorMessage, client } from "../../api/runtime.js";

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
  }

  interface ClosedShellSession {
    workspaceId: string;
    key: string;
    createdAt: string;
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
  // The workspace ID that `runtime` was fetched for. Stored
  // alongside the payload so we never render or operate on
  // sessions/targets that belong to a previous workspace
  // (during the in-place transition between workspaces, runtime
  // briefly outlives the workspace it was fetched for).
  let runtimeForId = $state<string>("");
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let retryingSetup = $state(false);
  let forcePromptMessage = $state<string | null>(null);
  let forcePromptForId = $state<string | null>(null);
  let forceDeleting = $state(false);
  let cancelForceBtnEl = $state<HTMLButtonElement | null>(null);
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
  let eventSource = $state<EventSource | null>(null);
  let activeTabKey = $state("home");
  let tmuxTabOpen = $state(false);
  let tmuxTerminalMounted = $state(false);
  let mountedSessionKeys = $state<string[]>([]);
  let closedSessions = $state<ClosedRuntimeSession[]>([]);
  let closedShellSession = $state<ClosedShellSession | null>(null);
  let launchingKey = $state<string | null>(null);
  let shellOpen = $state(false);
  let shellLoading = $state(false);

  const SIDEBAR_TAB_KEY = "middleman-workspace-sidebar-tab";
  const SIDEBAR_OPEN_KEY = "middleman-workspace-sidebar-open";
  const SIDEBAR_WIDTH_KEY = "middleman-workspace-sidebar-width";
  const WORKSPACE_LIST_WIDTH_KEY =
    "middleman-workspace-list-sidebar-width";
  const ACTIVE_WORKSPACE_TAB_KEY_PREFIX =
    "middleman-workspace-active-tab:";

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

  function loadWorkspaceListWidth(): number {
    const value = parseInt(
      localStorage.getItem(WORKSPACE_LIST_WIDTH_KEY) ?? "",
      10,
    );
    return Number.isFinite(value)
      ? clampWorkspaceListWidth(value)
      : DEFAULT_WORKSPACE_LIST_WIDTH;
  }

  function loadSidebarTab(): SidebarTab {
    const v = localStorage.getItem(SIDEBAR_TAB_KEY);
    if (v === "diff") return "diff";
    if (v === "pr") return "pr";
    if (v === "issue") return "issue";
    if (v === "reviews") return "reviews";
    return "diff";
  }

  function loadSidebarOpen(): boolean {
    return localStorage.getItem(SIDEBAR_OPEN_KEY) === "true";
  }

  const MIN_SIDEBAR_WIDTH = 280;
  const MIN_TERMINAL_WIDTH = 300;
  const DEFAULT_SIDEBAR_WIDTH = 640;
  const RIGHT_SIDEBAR_RESIZE_HANDLE_WIDTH = 4;

  function loadSidebarWidth(): number {
    const v = parseInt(
      localStorage.getItem(SIDEBAR_WIDTH_KEY) ?? "",
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
      workspace?.id === workspaceId,
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
  const shellSession = $derived(
    runtimeLive ? (runtime?.shell_session ?? null) : null,
  );
  const shellSessionLocallyClosed = $derived(
    shellSession !== null &&
      closedShellSession?.workspaceId === shellSession.workspace_id &&
      closedShellSession?.key === shellSession.key &&
      closedShellSession?.createdAt === shellSession.created_at,
  );
  const shellSessionActive = $derived(
    !shellSessionLocallyClosed &&
      (shellSession?.status === "running" ||
        shellSession?.status === "starting"),
  );
  const activeSession = $derived.by(() => {
    if (!activeTabKey.startsWith("session:")) return null;
    const key = activeTabKey.slice("session:".length);
    return runtimeSessions.find((session) => session.key === key) ?? null;
  });

  // While `workspaceId` has moved on but the previous workspace's
  // data is still on screen (the in-place transition), mutating
  // actions must not run — they would target the new id while the
  // user is looking at the old one. The window is small (≤ a few
  // hundred ms) but observable, so guard every action handler with
  // this and disable the buttons.
  const transitioning = $derived(
    workspaceId !== "" &&
      workspace !== null &&
      workspace.id !== workspaceId,
  );
  const actionsBlocked = $derived(transitioning);

  $effect(() => {
    localStorage.setItem(SIDEBAR_TAB_KEY, sidebarTab);
  });
  $effect(() => {
    localStorage.setItem(
      SIDEBAR_OPEN_KEY,
      String(sidebarOpen),
    );
  });
  $effect(() => {
    localStorage.setItem(
      SIDEBAR_WIDTH_KEY,
      String(sidebarWidth),
    );
  });
  $effect(() => {
    if (externalWorkspaceListWidth !== undefined) return;
    localStorage.setItem(
      WORKSPACE_LIST_WIDTH_KEY,
      String(workspaceListWidth),
    );
  });

  function handleSegmentClick(tab: SidebarTab): void {
    if (sidebarOpen && sidebarTab === tab) {
      sidebarOpen = false;
    } else {
      sidebarTab = tab;
      sidebarOpen = true;
    }
  }

  function openItemSidebar(targetId: string, tab: SidebarTab): void {
    // Cross-workspace click: navigate first, then ensure
    // the sidebar is open for the target tab.
    if (targetId !== workspaceId) {
      sidebarTab = tab;
      sidebarOpen = true;
      navigate(`/terminal/${targetId}`);
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

  function markShellClosed(
    id: string,
    shellKey: string,
    createdAt: string,
  ): void {
    closedShellSession = { workspaceId: id, key: shellKey, createdAt };
  }

  function clearClosedShellIfReplaced(
    id: string,
    shell: RuntimeSession | null | undefined,
  ): void {
    if (
      closedShellSession?.workspaceId === id &&
      (closedShellSession.key !== shell?.key ||
        closedShellSession.createdAt !== shell?.created_at)
    ) {
      closedShellSession = null;
    }
  }

  function isSessionTerminalMounted(
    sessionKey: string,
  ): boolean {
    return mountedSessionKeys.includes(sessionKey);
  }

  function rememberActiveTab(key: string): void {
    if (!workspaceId) return;
    localStorage.setItem(
      `${ACTIVE_WORKSPACE_TAB_KEY_PREFIX}${workspaceId}`,
      key,
    );
  }

  function selectWorkspaceTab(key: string): void {
    activeTabKey = key;
    rememberActiveTab(key);
  }

  function restoreWorkspaceTab(id: string): string {
    const remembered = localStorage.getItem(
      `${ACTIVE_WORKSPACE_TAB_KEY_PREFIX}${id}`,
    );
    if (remembered === "diff") return "home";
    return remembered ?? "home";
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

  async function fetchWorkspace(): Promise<void> {
    // Capture the id at call time. With workspaceId changing across
    // navigations, a slow in-flight fetch for the previous id could
    // otherwise resolve after a newer fetch and overwrite the new
    // workspace's data with stale content (causing a perceived flash
    // back to the previous workspace).
    const id = workspaceId;
    try {
      const { data, error, response } = await client.GET(
        "/workspaces/{id}",
        {
          params: { path: { id } },
        },
      );
      if (id !== workspaceId) return;
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
        void fetchRuntime();
      }
    } catch (err) {
      if (id !== workspaceId) return;
      loadError =
        err instanceof Error
          ? err.message
          : "Network error";
    }
  }

  async function fetchRuntime(): Promise<void> {
    if (!workspaceId) return;
    const id = workspaceId;
    const seq = runtimeFetchSeq + 1;
    runtimeFetchSeq = seq;
    try {
      const data = await getWorkspaceRuntime(id);
      if (id !== workspaceId || seq !== runtimeFetchSeq) return;
      runtime = data;
      runtimeForId = id;
      runtimeError = null;
      clearClosedShellIfReplaced(id, data.shell_session);
      if (
        activeTabKey.startsWith("session:") &&
        !activeSession
      ) {
        selectWorkspaceTab("home");
      }
      mountedSessionKeys = mountedSessionKeys.filter(
        (key) =>
          data.sessions.some((session) => session.key === key),
      );
    } catch (err) {
      if (id !== workspaceId || seq !== runtimeFetchSeq) return;
      runtimeError =
        err instanceof Error
          ? err.message
          : "Runtime load failed";
    }
  }

  async function handleLaunch(targetKey: string): Promise<void> {
    if (!workspaceId || launchingKey || actionsBlocked) return;
    const target = launchTargets.find((t) => t.key === targetKey);
    if (target?.kind === "tmux") {
      tmuxTabOpen = true;
      tmuxTerminalMounted = true;
      selectWorkspaceTab("tmux");
      return;
    }

    // Capture id so post-await steps bail if workspace changes mid-launch.
    const id = workspaceId;
    launchingKey = targetKey;
    runtimeError = null;
    try {
      const session = await launchWorkspaceSession(
        id,
        targetKey,
      );
      if (id !== workspaceId) return;
      await fetchRuntime();
      if (id !== workspaceId) return;
      clearClosedSession(session);
      mountSessionTerminal(session.key);
      selectWorkspaceTab(`session:${session.key}`);
    } catch (err) {
      if (id !== workspaceId) return;
      runtimeError =
        err instanceof Error ? err.message : "Launch failed";
    } finally {
      if (id === workspaceId) launchingKey = null;
    }
  }

  function openSession(sessionKey: string): void {
    mountSessionTerminal(sessionKey);
    selectWorkspaceTab(`session:${sessionKey}`);
  }

  async function closeSession(session: RuntimeSession): Promise<void> {
    if (actionsBlocked) return;
    if (
      session.status === "running" &&
      !confirm(`Stop ${session.label}?`)
    ) {
      return;
    }
    const id = workspaceId;
    try {
      await stopWorkspaceSession(id, session.key);
      if (id !== workspaceId) return;
      await fetchRuntime();
      if (id !== workspaceId) return;
      unmountSessionTerminal(session.key);
      if (activeTabKey === `session:${session.key}`) {
        selectWorkspaceTab("home");
      }
    } catch (err) {
      if (id !== workspaceId) return;
      runtimeError =
        err instanceof Error ? err.message : "Stop failed";
    }
  }

  function handleSessionExit(session: RuntimeSession): void {
    if (session.workspace_id !== workspaceId) return;
    markSessionClosed(session);
    unmountSessionTerminal(session.key);
    if (activeTabKey === `session:${session.key}`) {
      selectWorkspaceTab("home");
    }
    void fetchRuntime();
  }

  function handleShellExit(
    id: string,
    shellKey: string,
    createdAt: string,
  ): void {
    if (id !== workspaceId) return;
    markShellClosed(id, shellKey, createdAt);
    shellOpen = false;
    shellLoading = false;
    void fetchRuntime();
  }

  async function toggleShell(): Promise<void> {
    if (shellOpen) {
      shellOpen = false;
      return;
    }
    if (actionsBlocked) return;
    shellOpen = true;
    if (shellLoading) return;

    // Always call ensureWorkspaceShell on open. It is idempotent
    // server-side (returns the existing session if running, starts
    // a fresh one if exited), so trusting the locally-cached
    // shellSessionActive flag would mount a TerminalPane against a
    // shell the server has already torn down — yielding a 404
    // attach + reconnect loop.
    const id = workspaceId;
    shellLoading = true;
    runtimeError = null;
    try {
      await ensureWorkspaceShell(id);
      if (id !== workspaceId) return;
      await fetchRuntime();
    } catch (err) {
      if (id !== workspaceId) return;
      runtimeError =
        err instanceof Error
          ? err.message
          : "Shell launch failed";
    } finally {
      if (id === workspaceId) shellLoading = false;
    }
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

  async function handleRetrySetup(): Promise<void> {
    if (!workspace || retryingSetup || actionsBlocked) return;

    retryingSetup = true;
    actionError = null;
    try {
      const { data, error, response } = await client.POST(
        "/workspaces/{id}/retry",
        {
          params: { path: { id: workspaceId } },
        },
      );
      if (!data) {
        actionError = apiErrorMessage(
          error,
          `Retry failed (${response.status})`,
        );
        return;
      }
      workspace = data as Workspace;
      if (workspace.status === "creating") {
        startPolling();
        await fetchWorkspace();
      }
    } catch (err) {
      actionError =
        err instanceof Error
          ? err.message
          : "Retry failed";
    } finally {
      retryingSetup = false;
    }
  }

  async function handleDelete(): Promise<void> {
    if (actionsBlocked) return;
    actionError = null;
    const targetId = workspaceId;
    const targetGen = workspaceGen;
    // Capture the trigger synchronously: the click handler runs
    // before `inert` is applied to .terminal-view, so this is the
    // last point we can read the originating focused element. By
    // the time the post-await effect runs, the browser has cleared
    // focus to document.body.
    const triggerEl =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const { error, response } = await client.DELETE(
      "/workspaces/{id}",
      {
        params: { path: { id: targetId } },
      },
    );
    // Different workspace now: the user has moved on and nothing
    // about this response applies.
    if (targetId !== workspaceId) return;
    if (response.status === 409) {
      // A 409 that lands after the user briefly left and returned
      // to the same workspace would feel like an unrequested
      // prompt; suppress it on a generation mismatch and let the
      // user retry if they want.
      if (targetGen !== workspaceGen) return;
      previouslyFocusedEl = triggerEl;
      forcePromptForId = targetId;
      forcePromptMessage =
        error?.detail ??
        "Workspace has uncommitted changes.";
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
    navigate("/workspaces");
  }

  async function confirmForceDelete(): Promise<void> {
    if (forceDeleting) return;
    const targetId = forcePromptForId;
    if (targetId === null) return;
    const targetGen = workspaceGen;
    forceDeleting = true;
    actionError = null;
    try {
      const { error, response } = await client.DELETE(
        "/workspaces/{id}",
        {
          params: {
            path: { id: targetId },
            query: { force: true },
          },
        },
      );
      // The force-delete on the server is destructive and runs to
      // completion either way; once the user has moved to a
      // different workspace we just drop the response on the
      // floor so navigate() doesn't pull them away.
      if (targetId !== workspaceId) return;
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

  function handleForcePromptKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      event.preventDefault();
      cancelForceDelete();
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
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  $effect(() => {
    if (forcePromptMessage !== null) {
      void tick().then(() => cancelForceBtnEl?.focus());
    } else if (previouslyFocusedEl !== null) {
      previouslyFocusedEl.focus();
      previouslyFocusedEl = null;
    }
  });

  $effect(() => {
    if (forcePromptMessage === null) return;
    return untrack(() =>
      pushModalFrame("workspace-force-delete", []),
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
    const restoredTab = restoreWorkspaceTab(id);

    // Tab state from the previous workspace can't be valid for a
    // different workspace's runtime, so reset these even though
    // workspace/runtime themselves are kept. shellOpen must reset
    // too: the ShellDrawer's TerminalPane only opens its WebSocket
    // on mount, so leaving the drawer open across a workspace
    // change would route keystrokes to the previous workspace's
    // shell while the user looks at the new workspace.
    activeTabKey = restoredTab;
    tmuxTabOpen = restoredTab === "tmux";
    shellOpen = false;
    launchingKey = null;
    shellLoading = false;
    closedSessions = [];

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
    workspaceGen += 1;
    tmuxTerminalMounted = restoredTab === "tmux";
    mountedSessionKeys = restoredTab.startsWith("session:")
      ? [restoredTab.slice("session:".length)]
      : [];

    if (!id) {
      // /workspaces route: drop workspace data so the empty-state
      // message renders rather than continuing to show whatever
      // the previous /terminal/{id} session left behind.
      workspace = null;
      runtime = null;
      runtimeForId = "";
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
      }
    });

    return () => {
      stopPolling();
      source.close();
      if (eventSource === source) {
        eventSource = null;
      }
    };
  });
</script>

<div class="terminal-view" inert={forcePromptMessage !== null}>
  {#snippet terminalMainContent()}
    <div class="terminal-main">
      {#if !workspaceId}
        <div class="state-message">
          Select a workspace from the sidebar
        </div>
      {:else if loadError && !workspace}
        <div class="state-message error">
          <AlertIcon
            class="error-icon"
            size="16"
            strokeWidth="2"
            aria-label="Workspace load failed"
          />
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
          <AlertIcon
            class="error-icon"
            size="16"
            strokeWidth="2"
            aria-label="Workspace setup failed"
          />
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
            onclick={() => void handleDelete()}
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
            {/if}
            <button
              class="header-btn danger"
              disabled={actionsBlocked}
              onclick={() => void handleDelete()}
            >
              Delete
            </button>
          </div>
        </div>
        <div class="terminal-and-sidebar" bind:this={containerEl}>
          <div class="terminal-area">
            <div class="workspace-surface">
              <div class="workspace-toolbar">
                <WorkspaceTabs
                  activeKey={activeTabKey}
                  sessions={runtimeSessions}
                  tmuxOpen={tmuxTabOpen}
                  onSelectHome={() => {
                    selectWorkspaceTab("home");
                  }}
                  onSelectTmux={() => {
                    tmuxTerminalMounted = true;
                    selectWorkspaceTab("tmux");
                  }}
                  onSelectSession={openSession}
                  onCloseTmux={() => {
                    tmuxTabOpen = false;
                    tmuxTerminalMounted = false;
                    if (activeTabKey === "tmux") {
                      selectWorkspaceTab("home");
                    }
                  }}
                  onCloseSession={(key) => {
                    const session = runtimeSessions.find(
                      (s) => s.key === key,
                    );
                    if (session) void closeSession(session);
                  }}
                />
                <div class="workspace-actions">
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
              <div class="workspace-stage">
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
                  <div
                    class="stage-pane"
                    class:active={activeTabKey === "home"}
                  >
                    <WorkspaceHome
                      {workspace}
                      launchTargets={launchTargets}
                      sessions={runtimeSessions}
                      {launchingKey}
                      onLaunch={(key) => void handleLaunch(key)}
                      onOpenSession={openSession}
                    />
                  </div>
                  {#if tmuxTabOpen}
                    <div
                      class="stage-pane"
                      class:active={activeTabKey === "tmux"}
                    >
                      {#if tmuxTerminalMounted}
                        <TerminalPane
                          websocketPath={workspaceTmuxWebSocketPath(
                            workspaceId,
                          )}
                          reconnectOnExit={true}
                          active={activeTabKey === "tmux"}
                        />
                      {/if}
                    </div>
                  {/if}
                  {#each runtimeSessions as session (session.key)}
                    <div
                      class="stage-pane"
                      class:active={activeTabKey === `session:${session.key}`}
                    >
                      {#if isSessionTerminalMounted(session.key)}
                        <TerminalPane
                          websocketPath={workspaceSessionWebSocketPath(
                            workspaceId,
                            session.key,
                          )}
                          reconnectOnExit={false}
                          active={activeTabKey === `session:${session.key}`}
                          onExit={() => handleSessionExit(session)}
                          initialStatus={session.status}
                        />
                      {/if}
                    </div>
                  {/each}
                {/if}
              </div>
              <ShellDrawer
                {workspaceId}
                open={shellOpen}
                loading={shellLoading}
                shellSession={shellSessionActive ? shellSession : null}
                onToggle={() => void toggleShell()}
                onExit={(id, shellKey, createdAt) =>
                  handleShellExit(id, shellKey, createdAt)}
              />
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
          {isSidebarToggleEnabled}
          onCollapseSidebar={onToggleSidebar}
          onOpenItemSidebar={openItemSidebar}
        />
      {/snippet}
      {@render terminalMainContent()}
    </CollapsibleResizableSidebar>
  {/if}
</div>

{#if forcePromptMessage !== null}
  <div
    class="force-delete-backdrop"
    role="presentation"
    onkeydown={handleForcePromptKeydown}
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

  :global(.error-icon) {
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

  .header-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: color-mix(in srgb, var(--text-muted) 40%, var(--border-default));
  }

  .header-btn.danger:hover {
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
    background: var(--bg-inset);
    flex-shrink: 0;
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
  }

  /* Tabs stay mounted across switches so terminal scrollback and the
   * WebSocket survive — non-active panes are layered below and
   * hidden via visibility so layout/sizing is preserved. */
  .stage-pane {
    position: absolute;
    inset: 0;
    visibility: hidden;
  }

  .stage-pane.active {
    visibility: visible;
    z-index: 1;
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

  .force-delete-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }

  .force-delete-cancel,
  .force-delete-confirm {
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

  .force-delete-confirm:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-red) 88%, black);
    border-color: color-mix(in srgb, var(--accent-red) 88%, black);
  }

  .force-delete-cancel:disabled,
  .force-delete-confirm:disabled {
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
