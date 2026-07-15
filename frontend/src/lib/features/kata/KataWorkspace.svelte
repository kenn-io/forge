<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { IconButton } from "@kenn-io/kit-ui";
  import { showFlash } from "@middleman/ui/stores/flash";
  import LayoutPanelLeftIcon from "@lucide/svelte/icons/layout-panel-left";
  import LayoutPanelTopIcon from "@lucide/svelte/icons/layout-panel-top";
  import PlusIcon from "@lucide/svelte/icons/plus";

  import { fetchKataDaemons, type KataDaemonInfo } from "../../api/kata/daemons.js";
  import { createKataWorkspaceForTask, kataWorkspaceIdentityFromIssue } from "../../api/kata/workspaces.js";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataProjectSummary,
    KataRecurrence,
    KataTaskAPI,
    KataTaskDetail,
    KataTaskEditPatch,
    KataTaskSearchFilters,
    KataTaskSummary,
    KataTaskViewName,
  } from "../../api/kata/taskTypes.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import KataIssueList from "../../components/kata/KataIssueList.svelte";
  import KataResizableSash from "../../components/kata/KataResizableSash.svelte";
  import KataSidebar from "../../components/kata/KataSidebar.svelte";
  import QuickCapture from "../../components/shared/QuickCapture.svelte";
  import type { TypeaheadOption } from "../../components/shared/TypeaheadTrigger.svelte";
  import { computeRemoveMessageLinkPatch, readMessageLinks } from "../../messages/messageLinks.js";
  import type { MessageLinkRef } from "../../messages/types";
  import {
    getActiveKataDaemon,
    getDefaultKataDaemon,
    getKataDaemonRoster,
    getKataDaemonRosterLoaded,
    setActiveKataDaemon,
    setKataDaemonRoster,
  } from "../../stores/active-kata-daemon.svelte.js";
  import { navigate } from "../../stores/router.svelte.js";
  import {
    createKataWorkspaceStore,
    defaultKataTaskSearchFilters,
    KataEventCursorSyncError,
    type KataWorkspaceStoreSnapshot,
  } from "../../stores/kata-workspace.svelte.js";
  import { KataTaskAPIError } from "../../api/kata/taskClient.js";
  import {
    clearKataWorkspaceSelection,
    clearKataWorkspaceState,
    loadKataWorkspaceState,
    saveKataWorkspaceState,
    type KataPersistedWorkspaceState,
  } from "./kataWorkspacePersistence.js";
  import KataDaemonSwitcher from "./KataDaemonSwitcher.svelte";
  import KataReachableGraph from "./KataReachableGraph.svelte";
  import KataRecurrenceDialogs from "./KataRecurrenceDialogs.svelte";
  import KataSearchPanel from "./KataSearchPanel.svelte";
  import { createKataEventStreamController } from "./kataEventStreamController.js";
  import type { KataGraphLayoutDirection } from "./kataReachableGraph.js";

  interface Props {
    api?: KataTaskAPI | undefined;
    selectedIssueUID?: string | null | undefined;
    routeViewName?: KataTaskViewName | null | undefined;
    routeScopeUID?: string | null | undefined;
    requestedDaemonId?: string | null | undefined;
    onSelectedIssueChange?: ((uid: string | null) => void) | undefined;
    onRouteStateChange?: (
      (state: {
        issue?: string | null;
        view?: KataTaskViewName | null;
        scope?: string | null;
        daemon?: string | null;
      }, options?: { replace?: boolean }) => void
    ) | undefined;
    onOpenMessage?: ((messageId: number) => void) | undefined;
  }

  interface KataRouteSnapshot {
    view: KataTaskViewName | null;
    scope: string | null;
    issue: string | null;
  }

  type RestoreSource = "url" | "persisted" | "default";
  type RestorePolicy = "daemon-generation" | "route-authoritative";

  interface RestoreSources {
    view: RestoreSource;
    scope: RestoreSource;
    selection: RestoreSource;
  }

  interface RestorePersistenceDelta {
    clearState?: boolean;
    clearSelection?: boolean;
  }

  interface RestoreResult {
    route: KataRouteSnapshot;
    sources: RestoreSources;
    restoredSelectionUID: string | null;
    startEventStream: boolean;
    persistenceDelta: RestorePersistenceDelta | undefined;
    ancestorReveal: { uid: string; chain: readonly KataTaskSummary[] } | null;
    ancestorRevealError?: string | undefined;
  }

  interface KataRecurrenceDialogController {
    openCreateRecurrence: () => void;
    openEditRecurrence: (recurrence: KataRecurrence) => void;
    openDeleteRecurrence: (recurrence: KataRecurrence) => void;
    closeAll: () => void;
  }

  type SplitOrientation = "vertical" | "horizontal";
  type FailureSurface = "flash" | "daemon" | "view" | "none";
  type ListMode = "tasks" | "reachableGraph";

  function graphLayoutDirectionForSplit(orientation: SplitOrientation): KataGraphLayoutDirection {
    return orientation === "horizontal" ? "LR" : "TB";
  }

  let {
    api = undefined,
    selectedIssueUID = null,
    routeViewName = null,
    routeScopeUID = null,
    requestedDaemonId = null,
    onSelectedIssueChange = undefined,
    onRouteStateChange = undefined,
    onOpenMessage = undefined,
  }: Props = $props();

  let loading = $state(true);
  let viewLoading = $state(false);
  let viewLoadingGeneration = 0;
  let viewWorkCount = $state(0);
  let error = $state<string | null>(null);
  let viewError = $state<string | null>(null);
  let cursorCatchupError = $state<string | null>(null);
  let cursorCatchupRetry = $state<(() => Promise<void>) | null>(null);
  let cursorCatchupRetrying = $state(false);
  let cursorCatchupGeneration = 0;
  let cursorCatchupMounted = true;
  let workspaceMounted = true;
  let lastTaskError: string | null = null;
  let unlinkBusyIds = $state<ReadonlySet<number>>(new Set());
  let daemonInfos = $state.raw<KataDaemonInfo[]>([]);
  let switchingDaemon = $state(false);
  let terminalDaemonFailure = $state(false);
  let terminalRecovery = $state<(() => Promise<void>) | null>(null);
  let terminalRecovering = $state(false);
  let captureOpen = $state(false);
  let listResetGeneration = $state(0);
  let checklistRevealed = $state(false);
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
  let recurrenceDialogs = $state<KataRecurrenceDialogController | null>(null);
  let workspaceActionBusy = $state(false);
  let workspaceOwnershipPending = $state(true);
  let unknownRoutedBootstrapPending = $state(false);
  let listMode = $state<ListMode>("tasks");
  let graphSourceIssue = $state.raw<KataTaskSummary | null>(null);
  const store = createKataWorkspaceStore({ api: untrack(() => api) });
  const actor = "middleman";
  // Route synchronization is level-triggered: the URL is the source of
  // truth and one reconciler effect converges the store to it whenever
  // they differ. The component keeps no memory of what was already
  // synchronized; the only residual route state is:
  // - selectionFromRoute: whether the current selection was applied from
  //   the route. A null issue param means "deselect" only for routed
  //   selections; an auto-selected task (bootstrap, daemon switch) stays
  //   without polluting the URL.
  // - failedRouteSignature: the route whose convergence last failed, so
  //   a missing task or dead scope is not refetched until the URL moves.
  let reconciling = $state(false);
  let selectionFromRoute = false;
  let failedRouteSignature: string | null = null;
  let navigationGeneration = 0;
  // Reactive shadow of navigationGeneration so the issue list can drop
  // a pending keyboard selection the moment any navigation starts —
  // the list only remounts after the new view's data arrives, which is
  // too late for a selection released mid-transition.
  let navigationEpoch = $state(0);
  let restoredSelectionUID = $state<string | null>(null);
  let pendingDirectGraphSelectionUID: string | null = null;
  let restoreRetry = $state<(() => Promise<void>) | null>(null);
  let restoreRetryRouteSignature = $state<string | null>(null);
  let restoreRetrying = $state(false);
  let restoreError = $state<string | null>(null);
  let ancestorRevealRetry = $state<(() => Promise<void>) | null>(null);
  let ancestorRevealRetryRouteSignature = $state<string | null>(null);
  let ancestorRevealError = $state<string | null>(null);
  let provisionalRoutedDaemon = $state<string | null>(null);
  let routedRestoreFallbackDaemon = $state<string | undefined>(undefined);
  let routedRestoreGeneration = 0;
  let routedRestoreRouteSignature: string | null = null;
  let routedFallbackRecovering = $state(false);
  let routedFallbackRequestedDaemon: string | null = null;
  let switchGeneration = 0;
  let switchRouteSignature: string | null = null;
  let switchPreviousCursorCatchupError: string | null = null;
  let switchPreviousCursorCatchupRetry: (() => Promise<void>) | null = null;
  let switchPreviousEventStreamRunning = false;
  let eventStreamRunning = false;
  let revealRequest = $state<{ uid: string; chain: readonly KataTaskSummary[]; generation: number } | null>(null);
  let revealGeneration = 0;
  const layoutStorageKey = "middleman:kata:task-layout/v1";
  const defaultSplitSizes: Record<SplitOrientation, number> = {
    vertical: 420,
    horizontal: 520,
  };
  let splitOrientation = $state<SplitOrientation>("vertical");
  let splitSizes = $state<Record<SplitOrientation, number>>({ ...defaultSplitSizes });
  const activeSplitSize = $derived(splitSizes[splitOrientation]);
  const graphLayoutDirection = $derived(graphLayoutDirectionForSplit(splitOrientation));
  const acceptedKataDaemonId = $derived(
    getActiveKataDaemon() ??
      getDefaultKataDaemon() ??
      daemonInfos.find((daemon) => daemon.default)?.id ??
      daemonInfos[0]?.id,
  );
  const activeKataDaemonId = $derived(store.daemonId ?? acceptedKataDaemonId);
  const requestKataDaemonId = $derived(store.daemonId ?? activeKataDaemonId);
  const workspaceReadOnly = $derived(
    provisionalRoutedDaemon !== null ||
      routedFallbackRecovering ||
      restoreRetry !== null ||
      terminalDaemonFailure,
  );
  const routedDaemonError = $derived(
    requestedDaemonId && daemonInfos.length > 0 && !daemonInfos.some((daemon) => daemon.id === requestedDaemonId)
      ? `Kata daemon ${requestedDaemonId} is not configured.`
      : null,
  );
  const workspaceActionsBlocked = $derived(
    workspaceOwnershipPending ||
      switchingDaemon ||
      workspaceReadOnly ||
      restoreRetrying ||
      terminalRecovering ||
      routedDaemonError !== null ||
      workspaceActionBusy,
  );
  const listStatusFilter = $derived<KataTaskSearchFilters["status"]>(
    store.currentView.name === "logbook" ? "all" : store.searchFilters.status,
  );
  const eventStream = createKataEventStreamController({
    getDaemonId: () => requestKataDaemonId,
    getLastEventID: () => store.eventCursor,
    onOpen: () => {
      store.connection = { status: "online" };
    },
    onMessage: async (message) => {
      await trackViewWork(async () => {
        const scope = captureCursorCatchupScope();
        const selectedUID = store.selectedIssue?.issue.uid ?? null;
        const refreshed = await store.applyEventStreamMessage(message, {
          shouldApply: () => isCursorCatchupScopeCurrent(scope),
        });
        if (refreshed && isCursorCatchupScopeCurrent(scope)) {
          reconcilePersistedSelection(false, selectedUID);
        }
      });
    },
    onReset: () => {
      resetIssueExpansion();
    },
    onError: (message) => {
      store.connection = {
        status: "error",
        message,
      };
    },
  });

  // The workspace target arrives with the combined task-detail payload, so
  // the workspace action renders atomically with the detail pane.
  const workspaceTarget = $derived(
    store.selectedIssue?.workspace_target?.available ? store.selectedIssue.workspace_target : null,
  );
  // A daemon switch is transactional. Catalog data loaded while the target
  // is still provisional must not repaint either daemon's project controls.
  const visibleProjects = $derived(switchingDaemon ? [] : store.projects);
  const visibleAreas = $derived(switchingDaemon ? [] : store.areas);

  const systemViews = [
    { name: "inbox", label: "Inbox" },
    { name: "today", label: "Today" },
    { name: "upcoming", label: "Upcoming" },
    { name: "deadlines", label: "Deadlines" },
    { name: "all", label: "All Open" },
    { name: "logbook", label: "Logbook" },
  ] as const;

  function beginViewLoading(): number {
    const generation = ++viewLoadingGeneration;
    viewWorkCount += 1;
    viewLoading = true;
    return generation;
  }

  function endViewLoading(generation: number): void {
    viewWorkCount = Math.max(0, viewWorkCount - 1);
    if (generation === viewLoadingGeneration) viewLoading = false;
  }

  async function trackViewWork<T>(task: () => Promise<T>): Promise<T> {
    const generation = beginViewLoading();
    try {
      return await task();
    } finally {
      endViewLoading(generation);
    }
  }

  function kataRequestErrorMessage(err: unknown): string {
    return err instanceof Error ? err.message : "Kata request failed.";
  }

  function clearTaskErrors(surface: FailureSurface = "daemon"): void {
    if (surface === "daemon") error = null;
    if (surface === "view") viewError = null;
    lastTaskError = null;
  }

  function surfaceTaskError(message: string, surface: FailureSurface): void {
    lastTaskError = message;
    if (surface === "flash") {
      showFlash(message, { tone: "danger" });
    } else if (surface === "daemon") {
      error = message;
    } else if (surface === "view") {
      viewError = message;
    }
  }

  async function runViewTask(
    task: () => Promise<void | boolean>,
    failureSurface: FailureSurface = "daemon",
    shouldSurfaceFailure: () => boolean = () => true,
  ): Promise<boolean> {
    const loadingGeneration = beginViewLoading();
    clearTaskErrors(failureSurface);
    const expansionSignature = currentExpansionSignature();
    try {
      const ok = (await task()) ?? true;
      if (ok && currentExpansionSignature() !== expansionSignature) {
        resetIssueExpansion();
      }
      return ok;
    } catch (err) {
      if (shouldSurfaceFailure()) {
        surfaceTaskError(kataRequestErrorMessage(err), failureSurface);
      }
      return false;
    } finally {
      endViewLoading(loadingGeneration);
    }
  }

  async function runViewTaskOrThrow(
    task: () => Promise<void>,
    failureSurface: FailureSurface = "daemon",
  ): Promise<void> {
    const loadingGeneration = beginViewLoading();
    clearTaskErrors(failureSurface);
    try {
      await task();
    } catch (err) {
      surfaceTaskError(kataRequestErrorMessage(err), failureSurface);
      throw err;
    } finally {
      endViewLoading(loadingGeneration);
    }
  }

  function projectScopeExists(scopeUID: string, workspaceStore = store): boolean {
    return workspaceStore.projects.some((project) => project.uid === scopeUID);
  }

  function effectiveViewName(view: KataTaskViewName): KataTaskViewName {
    return view === "all" ? "all" : view;
  }

  function canonicalRoute(
    view: KataTaskViewName,
    scopeUID: string | null,
    issueUID: string | null,
    preserveScopedView = false,
  ): KataRouteSnapshot {
    return {
      view: view === "all" || (scopeUID !== null && !preserveScopedView) ? null : view,
      scope: scopeUID,
      issue: issueUID,
    };
  }

  function statusMatches(issue: KataTaskSummary, status: KataTaskSearchFilters["status"]): boolean {
    return status === "all" || issue.status === status;
  }

  function isDefinitiveRestoreFailure(error: unknown): boolean {
    return error instanceof KataTaskAPIError && error.status === 404;
  }

  async function resolveRestoredAncestorChain(
    selected: KataTaskSummary,
    daemonID: string,
    shouldApply: () => boolean,
    workspaceStore = store,
  ): Promise<readonly KataTaskSummary[] | null | undefined> {
    const chain = [selected];
    const visited = new Set([selected.uid]);
    let current = selected;
    for (let depth = 0; current.parent; depth += 1) {
      if (!shouldApply()) return undefined;
      if (depth >= 32 || visited.has(current.parent.uid)) return null;
      visited.add(current.parent.uid);
      let detail: KataTaskDetail;
      try {
        detail = await workspaceStore.api.issue(current.parent.uid, { daemonId: daemonID });
      } catch (error) {
        if (isDefinitiveRestoreFailure(error)) return null;
        throw error;
      }
      if (!shouldApply()) return undefined;
      if (!detail.issue) return null;
      workspaceStore.rememberTasks([detail.issue, ...(detail.children ?? [])]);
      current = detail.issue;
      chain.push(current);
    }
    return chain.reverse();
  }

  async function revealSelectedAncestors(
    selected: KataTaskSummary,
    daemonID: string,
    shouldApply: () => boolean,
    workspaceStore = store,
  ): Promise<void> {
    const retrySignature = fullRouteSignature();
    const isCurrent = (): boolean =>
      shouldApply() &&
      retrySignature === fullRouteSignature() &&
      (workspaceStore.daemonId === undefined || workspaceStore.daemonId === daemonID) &&
      workspaceStore.selectedIssue?.issue.uid === selected.uid;
    try {
      const chain = await resolveRestoredAncestorChain(
        selected,
        daemonID,
        isCurrent,
        workspaceStore,
      );
      if (chain === undefined || !isCurrent()) return;
      ancestorRevealError = null;
      ancestorRevealRetry = null;
      ancestorRevealRetryRouteSignature = null;
      if (chain) revealRequest = { uid: selected.uid, chain, generation: ++revealGeneration };
    } catch (error) {
      if (
        !isCurrent()
      ) {
        return;
      }
      ancestorRevealError = kataRequestErrorMessage(error);
      ancestorRevealRetryRouteSignature = retrySignature;
      ancestorRevealRetry = async () => {
        if (!isCurrent()) return;
        ancestorRevealError = null;
        await revealSelectedAncestors(selected, daemonID, shouldApply, workspaceStore);
      };
    }
  }

  function synchronizeRestoredRoute(route: KataRouteSnapshot, restoredUID: string | null): void {
    selectionFromRoute = route.issue !== null && route.issue === selectedIssueUID;
    restoredSelectionUID = restoredUID;
    // Restoration reconciles the current entry; it must not add a duplicate
    // Kata entry that traps Back/Forward navigation inside the workspace.
    onRouteStateChange?.(route, { replace: true });
  }

  function mergeRestorePersistenceDelta(
    current: RestorePersistenceDelta | undefined,
    next: RestorePersistenceDelta | undefined,
  ): RestorePersistenceDelta | undefined {
    if (!current) return next;
    if (!next) return current;
    const merged: RestorePersistenceDelta = {};
    if (current.clearState || next.clearState) merged.clearState = true;
    if (current.clearSelection || next.clearSelection) merged.clearSelection = true;
    return merged;
  }

  function applyRestorePersistenceDelta(
    daemonID: string,
    delta: RestorePersistenceDelta | undefined,
  ): void {
    if (delta?.clearState) {
      clearKataWorkspaceState(daemonID);
    } else if (delta?.clearSelection) {
      clearKataWorkspaceSelection(daemonID);
    }
  }

  async function restoreKataWorkspaceState(
    daemonID: string,
    route: KataRouteSnapshot,
    synchronizeRoute = true,
    mutatePersistence = true,
    selectDefault = false,
    expectedRouteSignature: string | null = null,
    shouldApply: () => boolean = () => true,
    policy: RestorePolicy = "daemon-generation",
    workspaceStore = store,
  ): Promise<RestoreResult> {
    let synchronizationSignature = expectedRouteSignature;
    const synchronize = (restoredRoute: KataRouteSnapshot, restoredUID: string | null): void => {
      if (
        workspaceMounted &&
        shouldApply() &&
        synchronizeRoute &&
        (synchronizationSignature === null || fullRouteSignature() === synchronizationSignature)
      ) {
        synchronizeRestoredRoute(restoredRoute, restoredUID);
        synchronizationSignature = fullRouteSignatureFor(restoredRoute);
      }
    };
    const persisted = daemonID ? loadKataWorkspaceState(daemonID) : null;
    const inheritRouteFields = policy === "daemon-generation";
    const hasExplicitScope = route.scope !== null;
    const sources: RestoreSources = {
      view: route.view ? "url" : inheritRouteFields && persisted ? "persisted" : "default",
      scope:
        hasExplicitScope
          ? "url"
          : inheritRouteFields && persisted?.filters.scope.kind === "project"
            ? "persisted"
            : "default",
      selection: route.issue ? "url" : inheritRouteFields && persisted?.selectedIssueUID ? "persisted" : "default",
    };
    const defaults = defaultKataTaskSearchFilters();
    let acceptedPersisted: KataPersistedWorkspaceState | null = persisted;
    let persistenceDelta: RestorePersistenceDelta | undefined;
    let ancestorReveal: RestoreResult["ancestorReveal"] = null;
    let ancestorRevealErrorMessage: string | undefined;
    let scopeUID = route.scope;
    let view = route.view ?? (inheritRouteFields ? persisted?.view : undefined) ?? "all";
    let issueUID = route.issue ?? (inheritRouteFields ? persisted?.selectedIssueUID : null) ?? null;
    let filters: KataTaskSearchFilters = persisted
      ? {
          ...persisted.filters,
          scope: inheritRouteFields ? persisted.filters.scope : { kind: "all" },
        }
      : defaults;

    await workspaceStore.loadProjectCatalog(shouldApply);
    if (!shouldApply()) {
      return {
        route,
        sources,
        restoredSelectionUID: null,
        startEventStream: false,
        persistenceDelta,
        ancestorReveal: null,
      };
    }

    if (scopeUID && !projectScopeExists(scopeUID, workspaceStore)) {
      scopeUID = null;
      sources.scope = "default";
    }
    if (acceptedPersisted?.filters.scope.kind === "project") {
      const persistedScope = acceptedPersisted.filters.scope.project_uid;
      if (!projectScopeExists(persistedScope, workspaceStore)) {
        if (mutatePersistence && shouldApply()) clearKataWorkspaceState(daemonID);
        else persistenceDelta = { clearState: true };
        acceptedPersisted = null;
        view = route.view ?? "all";
        issueUID = route.issue;
        filters = defaults;
        sources.view = route.view ? "url" : "default";
        sources.selection = route.issue ? "url" : "default";
      } else if (inheritRouteFields && !hasExplicitScope && !scopeUID) {
        scopeUID = persistedScope;
        sources.scope = "persisted";
      }
    }

    if (
      !scopeUID &&
      !route.scope &&
      !route.view &&
      !route.issue &&
      acceptedPersisted === null &&
      persisted !== null &&
      mutatePersistence &&
      shouldApply()
    ) {
      workspaceStore.resetToInertWorkspace();
      const inertRoute = canonicalRoute("all", null, null);
      synchronize(inertRoute, null);
      return {
        route: inertRoute,
        sources,
        restoredSelectionUID: null,
        startEventStream: false,
        persistenceDelta,
        ancestorReveal: null,
      };
    }

    if (acceptedPersisted === null) {
      if (!shouldApply()) {
        return {
          route,
          sources,
          restoredSelectionUID: null,
          startEventStream: false,
          persistenceDelta,
          ancestorReveal: null,
        };
      }
      if (persisted !== null) {
        if (scopeUID) {
          await workspaceStore.updateSearchFilters(
            { ...defaults, scope: { kind: "project", project_uid: scopeUID } },
            { selectFirst: false, shouldApply },
          );
        } else {
          await workspaceStore.openView(route.view ?? "all", { selectFirst: false, shouldApply });
        }
        const resetRoute = canonicalRoute(
          route.view ?? "all",
          scopeUID,
          route.issue,
          sources.view === "url",
        );
        synchronize(resetRoute, null);
        return { route: resetRoute, sources, restoredSelectionUID: null, startEventStream: true, persistenceDelta, ancestorReveal: null };
      }
      await workspaceStore.bootstrap(route.view ?? "all", null, { selectFirst: selectDefault, shouldApply });
      if (scopeUID && shouldApply()) {
        await workspaceStore.updateSearchFilters(
          { ...defaults, scope: { kind: "project", project_uid: scopeUID } },
          { selectFirst: false, shouldApply },
        );
      }
      const initialRoute = canonicalRoute(
        route.view ?? "all",
        scopeUID,
        route.issue,
        sources.view === "url",
      );
      synchronize(initialRoute, null);
      return { route: initialRoute, sources, restoredSelectionUID: null, startEventStream: true, persistenceDelta, ancestorReveal: null };
    }

    if (scopeUID) {
      filters = { ...filters, scope: { kind: "project", project_uid: scopeUID } };
    } else {
      filters = { ...filters, scope: { kind: "all" } };
    }
    if (route.view) view = route.view;

    if (!shouldApply()) {
      return {
        route,
        sources,
        restoredSelectionUID: null,
        startEventStream: false,
        persistenceDelta,
        ancestorReveal: null,
      };
    }
    workspaceStore.clearSelection();
    await workspaceStore.restoreViewAndFilters(effectiveViewName(view), filters, {
      selectFirst: false,
      shouldApply,
    });
    if (!shouldApply()) {
      return {
        route,
        sources,
        restoredSelectionUID: null,
        startEventStream: false,
        persistenceDelta,
        ancestorReveal: null,
      };
    }

    const canonical = canonicalRoute(view, scopeUID, null, sources.view === "url");
    const rawIssues = workspaceStore.currentView.groups.flatMap((group) => group.issues);
    const routedSelection = sources.selection === "url" ? issueUID : null;
    const persistedSelection = sources.selection === "persisted" ? issueUID : null;
    const effectiveStatus = view === "logbook" ? "all" : filters.status;
    if (persistedSelection && !rawIssues.some((issue) => issue.uid === persistedSelection && statusMatches(issue, effectiveStatus))) {
      if (mutatePersistence && shouldApply()) clearKataWorkspaceSelection(daemonID);
      else persistenceDelta = mergeRestorePersistenceDelta(persistenceDelta, { clearSelection: true });
      if (!shouldApply()) {
        return {
          route,
          sources,
          restoredSelectionUID: null,
          startEventStream: false,
          persistenceDelta,
          ancestorReveal: null,
        };
      }
      workspaceStore.clearSelection();
      synchronize(canonical, null);
      return { route: canonical, sources, restoredSelectionUID: null, startEventStream: true, persistenceDelta, ancestorReveal: null };
    }

    const select = async (): Promise<void> => {
      if (
        !shouldApply() ||
        !issueUID ||
        (workspaceStore.daemonId !== undefined && workspaceStore.daemonId !== daemonID)
      ) {
        return;
      }
      const retrying = restoreRetryRouteSignature !== null;
      if (retrying && restoreRetryRouteSignature !== fullRouteSignature()) return;
      try {
        await workspaceStore.selectIssue(issueUID, { shouldApply });
      } catch (error) {
        if (!shouldApply()) return;
        if (workspaceStore.daemonId !== undefined && workspaceStore.daemonId !== daemonID) return;
        if (retrying && restoreRetryRouteSignature !== fullRouteSignature()) return;
        workspaceStore.clearSelection();
        const failureRoute = canonicalRoute(view, scopeUID, null, sources.view === "url");
        if (isDefinitiveRestoreFailure(error)) {
          if (sources.selection === "persisted") {
            if (mutatePersistence && shouldApply()) clearKataWorkspaceSelection(daemonID);
            else persistenceDelta = mergeRestorePersistenceDelta(persistenceDelta, { clearSelection: true });
          }
          restoreError = null;
          restoreRetry = null;
          restoreRetryRouteSignature = null;
          synchronize(failureRoute, null);
          return;
        }
        restoreError = kataRequestErrorMessage(error);
        synchronize(failureRoute, null);
        const retrySignature = fullRouteSignature();
        restoreRetryRouteSignature = retrySignature;
        restoreRetry = async () => {
          if (retrySignature !== fullRouteSignature()) return;
          restoreError = null;
          await select();
        };
        return;
      }

      if (!shouldApply()) return;
      if (workspaceStore.daemonId !== undefined && workspaceStore.daemonId !== daemonID) return;
      if (retrying && restoreRetryRouteSignature !== fullRouteSignature()) return;
      if (workspaceStore.selectedIssue?.issue.uid !== issueUID) {
        workspaceStore.clearSelection();
        if (sources.selection === "persisted") {
          if (mutatePersistence && shouldApply()) clearKataWorkspaceSelection(daemonID);
          else persistenceDelta = mergeRestorePersistenceDelta(persistenceDelta, { clearSelection: true });
        }
        synchronize(canonicalRoute(view, scopeUID, null, sources.view === "url"), null);
        return;
      }

      const selectedRoute = { ...canonical, issue: issueUID };
      synchronize(selectedRoute, sources.selection === "persisted" ? issueUID : null);
      restoreError = null;
      restoreRetry = null;
      restoreRetryRouteSignature = null;
      if (sources.selection === "persisted") {
        const restoredIssue = workspaceStore.selectedIssue.issue;
        workspaceStore.rememberTasks([restoredIssue, ...(workspaceStore.selectedIssue.children ?? [])]);
        if (workspaceStore !== store) {
          try {
            const chain = await resolveRestoredAncestorChain(
              restoredIssue,
              daemonID,
              shouldApply,
              workspaceStore,
            );
            if (chain && shouldApply()) ancestorReveal = { uid: restoredIssue.uid, chain };
          } catch (error) {
            if (!shouldApply()) return;
            ancestorRevealErrorMessage = kataRequestErrorMessage(error);
          }
        }
      }
    };

    if (!shouldApply()) {
      return {
        route,
        sources,
        restoredSelectionUID: null,
        startEventStream: false,
        persistenceDelta,
        ancestorReveal: null,
      };
    }
    restoreError = null;
    restoreRetry = null;
    restoreRetryRouteSignature = null;
    if (!routedSelection) await select();
    if (!shouldApply()) {
      return {
        route,
        sources,
        restoredSelectionUID: null,
        startEventStream: false,
        persistenceDelta,
        ancestorReveal: null,
      };
    }
    if (!issueUID || routedSelection) synchronize({ ...canonical, issue: routedSelection }, null);
    return {
      route: workspaceStore.selectedIssue
        ? { ...canonical, issue: workspaceStore.selectedIssue.issue.uid }
        : { ...canonical, issue: routedSelection },
      sources,
      restoredSelectionUID,
      startEventStream: true,
      persistenceDelta,
      ancestorReveal,
      ancestorRevealError: ancestorRevealErrorMessage,
    };
  }

  onMount(() => {
    let cancelled = false;
    loadLayoutPrefs();

    void (async () => {
      let routedDaemonId: string | null = null;
      let previousExplicitDaemon: string | undefined;
      try {
        const daemons = await fetchKataDaemons();
        if (cancelled) return;
        daemonInfos = daemons;
        setKataDaemonRoster(
          daemons.map((daemon) => daemon.id),
          daemons.find((daemon) => daemon.default)?.id,
        );
        const unknownRoutedDaemon = requestedDaemonId !== null && !daemons.some((daemon) => daemon.id === requestedDaemonId);
        if (unknownRoutedDaemon) {
          store.resetToInertWorkspace();
          store.clearDaemonBinding();
          stopEventStream();
          unknownRoutedBootstrapPending = true;
          workspaceOwnershipPending = true;
          return;
        }
        routedDaemonId = requestedDaemonId;
        if (routedDaemonId) {
          previousExplicitDaemon = getActiveKataDaemon();
          routedRestoreFallbackDaemon = previousExplicitDaemon ?? getDefaultKataDaemon();
          provisionalRoutedDaemon = routedDaemonId;
          store.bindDaemonForBootstrap(routedDaemonId);
        }
        const daemonID = routedDaemonId ?? activeKataDaemonId ?? "home";
        if (!routedDaemonId) store.api.bindWorkflowDaemon?.(daemonID);
        const finalizeRestoration = async (
          restored: RestoreResult,
          attemptedRoute: KataRouteSnapshot,
          attemptedSignature: string,
          catchUpCursor: boolean,
          restoreGeneration: number,
        ): Promise<void> => {
          if (cancelled || (routedDaemonId !== null && !routedRestoreIsCurrent(restoreGeneration, attemptedSignature))) return;
          let persistenceDelta = restored.persistenceDelta;
          selectionFromRoute =
            attemptedRoute.issue !== null && store.selectedIssue?.issue.uid === attemptedRoute.issue;
          if (
            attemptedRoute.issue !== null &&
            currentRouteSnapshot().issue === attemptedRoute.issue &&
            store.selectedIssue?.issue.uid !== attemptedRoute.issue
          ) {
            let selectionError: unknown;
            const ok = await runViewTask(
              async () => {
                try {
                  return await store.selectIssue(attemptedRoute.issue!, {
                    shouldApply: () =>
                      routedDaemonId === null ||
                      routedRestoreIsCurrent(restoreGeneration, attemptedSignature),
                  });
                } catch (error) {
                  selectionError = error;
                  throw error;
                }
              },
              "none",
            );
            if (cancelled || (routedDaemonId !== null && !routedRestoreIsCurrent(restoreGeneration, attemptedSignature))) return;
            if (ok) {
              selectionFromRoute = true;
              if (store.selectedIssue) {
                const restoredIssue = store.selectedIssue.issue;
                store.rememberTasks([restoredIssue, ...(store.selectedIssue.children ?? [])]);
              }
            } else if (
              isDefinitiveRestoreFailure(selectionError) &&
              currentRouteSnapshot().issue === attemptedRoute.issue
            ) {
              store.clearSelection();
              selectionFromRoute = false;
            } else if (selectionError !== undefined) {
              throw selectionError;
            }
          }
          if (cancelled || (routedDaemonId !== null && !routedRestoreIsCurrent(restoreGeneration, attemptedSignature))) return;
          if (restored.startEventStream && catchUpCursor) {
            const cursorDelta = await syncEventCursorAndReconcileSelection(
              captureCursorCatchupScope(),
              routedDaemonId === null,
              routedDaemonId === null,
            );
            persistenceDelta = mergeRestorePersistenceDelta(persistenceDelta, cursorDelta);
          }
          if (cancelled || (routedDaemonId !== null && !routedRestoreIsCurrent(restoreGeneration, attemptedSignature))) return;
          if (routedDaemonId) {
            setActiveKataDaemon(routedDaemonId);
            workspaceOwnershipPending = false;
            provisionalRoutedDaemon = null;
            routedRestoreFallbackDaemon = undefined;
            routedRestoreRouteSignature = null;
            applyRestorePersistenceDelta(routedDaemonId, persistenceDelta);
            const route =
              fullRouteSignature() === attemptedSignature
                ? {
                    ...restored.route,
                    issue: store.selectedIssue?.issue.uid ?? null,
                  }
                : currentRouteSnapshot();
            onRouteStateChange?.({ ...route, daemon: null }, { replace: true });
            persistActiveWorkspaceState();
            if (store.selectedIssue) {
              const restoredIssue = store.selectedIssue.issue;
              void revealSelectedAncestors(restoredIssue, routedDaemonId, () =>
                store.daemonId === routedDaemonId &&
                fullRouteSignature() === fullRouteSignatureFor(route),
              );
            }
          } else if (restored.startEventStream && catchUpCursor) {
            workspaceOwnershipPending = false;
          } else if (routedDaemonId === null && !restored.startEventStream) {
            workspaceOwnershipPending = false;
          }
          if (restored.startEventStream && catchUpCursor) {
            startEventStream();
            if (store.selectedIssue) {
              const restoredIssue = store.selectedIssue.issue;
              void revealSelectedAncestors(restoredIssue, daemonID, () =>
                (store.daemonId === undefined || store.daemonId === daemonID) &&
                store.selectedIssue?.issue.uid === restoredIssue.uid,
              );
            }
          }
          error = null;
          if (restoreRetryRouteSignature === null) {
            restoreError = null;
            restoreRetry = null;
          }
        };
        const restoreInitialWorkspace = async (catchUpCursor: boolean): Promise<RestoreResult> =>
          withRouteEmission(async () => {
            const attemptedRoute = currentRouteSnapshot();
            const attemptedSignature = currentFullRouteSignature();
            const restoreGeneration = ++routedRestoreGeneration;
            if (routedDaemonId !== null) routedRestoreRouteSignature = attemptedSignature;
            const restored = await restoreKataWorkspaceState(
              daemonID,
              attemptedRoute,
              routedDaemonId === null,
              routedDaemonId === null,
              false,
              attemptedSignature,
              () => routedDaemonId === null || routedRestoreIsCurrent(restoreGeneration, attemptedSignature),
            );
            await finalizeRestoration(
              restored,
              attemptedRoute,
              attemptedSignature,
              catchUpCursor,
              restoreGeneration,
            );
            return restored;
          });
        const routedAttemptStillOwnsRoute = (): boolean =>
          routedDaemonId !== null &&
          provisionalRoutedDaemon === routedDaemonId &&
          requestedDaemonId === routedDaemonId;
        const abandonRoutedRestore = (): void => {
          if (provisionalRoutedDaemon === null) return;
          const fallbackDaemon = routedRestoreFallbackDaemon ?? acceptedKataDaemonId;
          routedRestoreGeneration += 1;
          routedRestoreRouteSignature = null;
          provisionalRoutedDaemon = null;
          routedRestoreFallbackDaemon = undefined;
          restoreError = null;
          restoreRetry = null;
          restoreRetryRouteSignature = null;
          if (fallbackDaemon) void recoverRoutedFallbackDaemon(fallbackDaemon);
        };
        const installRestoreRetry = (): void => {
          restoreRetryRouteSignature = currentFullRouteSignature();
          restoreRetry = async () => {
            if (restoreRetrying) return;
            restoreRetrying = true;
            restoreError = null;
            try {
              await restoreInitialWorkspace(true);
              if (provisionalRoutedDaemon === null) {
                restoreRetry = null;
                restoreRetryRouteSignature = null;
                return;
              }
              if (
                routedDaemonId !== null &&
                !routedAttemptStillOwnsRoute()
              ) {
                abandonRoutedRestore();
                return;
              }
              restoreRetry = null;
              restoreRetryRouteSignature = null;
            } catch (retryError) {
              if (cancelled) return;
              if (
                routedDaemonId !== null &&
                !routedAttemptStillOwnsRoute()
              ) {
                abandonRoutedRestore();
                return;
              }
              if (routedDaemonId) {
                setActiveKataDaemon(previousExplicitDaemon, false);
                restoreCursorCatchupError(null, null);
              }
              restoreError = kataRequestErrorMessage(retryError);
              installRestoreRetry();
            } finally {
              restoreRetrying = false;
            }
          };
        };
        let restored: RestoreResult;
        try {
          restored = await restoreInitialWorkspace(routedDaemonId !== null);
        } catch (err) {
          if (routedDaemonId !== null && !routedAttemptStillOwnsRoute()) {
            abandonRoutedRestore();
            return;
          }
          if (store.connection.status === "error" && daemonInfos.length > 0) {
            error = store.connection.message ?? "Connection failed";
          } else {
            restoreError = kataRequestErrorMessage(err);
          }
          installRestoreRetry();
          return;
        }
        if (routedDaemonId === null && !restored.startEventStream) workspaceOwnershipPending = false;
        if (restored.startEventStream && routedDaemonId === null) {
          try {
            const cursorDelta = await syncEventCursorAndReconcileSelection();
            applyRestorePersistenceDelta(daemonID, cursorDelta);
            if (!cancelled) {
              startEventStream();
              workspaceOwnershipPending = false;
              if (store.selectedIssue) {
                const restoredIssue = store.selectedIssue.issue;
                void revealSelectedAncestors(restoredIssue, daemonID, () =>
                  (store.daemonId === undefined || store.daemonId === daemonID) &&
                  store.selectedIssue?.issue.uid === restoredIssue.uid,
                );
              }
            }
          } catch (cursorError) {
            if (cancelled) return;
            cursorCatchupError = kataRequestErrorMessage(cursorError);
            cursorCatchupRetry = () => retryEventCursorCatchup(true);
            workspaceOwnershipPending = true;
            terminalDaemonFailure = false;
            terminalRecovery = null;
            error = null;
            stopEventStream();
          }
        }
      } catch (err) {
        if (!cancelled) {
          if (routedDaemonId) {
            store.clearDaemonState();
            setActiveKataDaemon(previousExplicitDaemon);
          }
          terminalDaemonFailure = true;
          error =
            store.connection.status === "error" && store.connection.message
              ? store.connection.message
              : err instanceof Error
                ? err.message
                : "Kata request failed.";
        }
      } finally {
        if (!cancelled) loading = false;
      }
    })();

    return () => {
      cancelled = true;
      workspaceMounted = false;
      switchGeneration += 1;
      switchRouteSignature = null;
      cursorCatchupMounted = false;
      invalidateCursorCatchup();
      stopEventStream();
    };
  });

  function scopeUIDFromFilters(filters: KataTaskSearchFilters): string | null {
    return filters.scope.kind === "project" ? filters.scope.project_uid : null;
  }

  function beginNavigation(): number {
    revealRequest = null;
    captureOpen = false;
    navigationGeneration += 1;
    navigationEpoch = navigationGeneration;
    return navigationGeneration;
  }

  function isCurrentNavigation(generation: number): boolean {
    return generation === navigationGeneration;
  }

  function currentRouteSnapshot(): KataRouteSnapshot {
    return {
      view: routeViewName ?? null,
      scope: routeScopeUID ?? null,
      issue: selectedIssueUID ?? null,
    };
  }

  function currentFullRouteSignature(): string {
    const route = currentRouteSnapshot();
    return `${route.view ?? ""}\u0000${route.scope ?? ""}\u0000${route.issue ?? ""}\u0000${requestedDaemonId ?? ""}`;
  }

  function routedRestoreIsCurrent(generation: number, signature: string): boolean {
    return (
      workspaceMounted &&
      generation === routedRestoreGeneration &&
      signature === routedRestoreRouteSignature &&
      signature === currentFullRouteSignature()
    );
  }

  async function recoverRoutedFallbackDaemon(daemonID: string): Promise<void> {
    routedFallbackRequestedDaemon = daemonID;
    if (routedFallbackRecovering) return;
    routedFallbackRecovering = true;
    terminalDaemonFailure = false;
    terminalRecovery = null;
    stopEventStream();
    try {
      while (workspaceMounted && routedFallbackRequestedDaemon !== null) {
        const recoveryDaemonID = routedFallbackRequestedDaemon;
        routedFallbackRequestedDaemon = null;
        const recoveryGeneration = ++routedRestoreGeneration;
        const recoverySignature = currentFullRouteSignature();
        const recoveryRoute = currentRouteSnapshot();
        routedRestoreRouteSignature = recoverySignature;
        const isCurrent = (): boolean =>
          workspaceMounted &&
          recoveryGeneration === routedRestoreGeneration &&
          recoverySignature === currentFullRouteSignature();
        try {
          if (!isCurrent()) continue;
          store.clearDaemonState();
          store.bindDaemonForBootstrap(recoveryDaemonID);
          const restored = await restoreKataWorkspaceState(
            recoveryDaemonID,
            recoveryRoute,
            false,
            true,
            false,
            null,
            isCurrent,
            "route-authoritative",
          );
          if (!isCurrent()) continue;
          await store.api.instance();
          if (!isCurrent()) continue;
          const cursorDelta = await syncEventCursorAndReconcileSelection(
            { daemonID: recoveryDaemonID, generation: cursorCatchupGeneration },
            true,
            false,
          );
          if (!isCurrent()) continue;
          applyRestorePersistenceDelta(
            recoveryDaemonID,
            mergeRestorePersistenceDelta(restored.persistenceDelta, cursorDelta),
          );
          setActiveKataDaemon(recoveryDaemonID);
          terminalDaemonFailure = false;
          terminalRecovery = null;
          workspaceOwnershipPending = false;
          error = null;
          if (restored.startEventStream) startEventStream();
          routedRestoreRouteSignature = null;
          break;
        } catch (recoveryError) {
          if (!isCurrent()) continue;
          error = kataRequestErrorMessage(recoveryError);
          terminalDaemonFailure = true;
          stopEventStream();
          terminalRecovery = async () => {
            if (terminalRecovering) return;
            terminalRecovering = true;
            terminalDaemonFailure = false;
            try {
              routedFallbackRequestedDaemon = recoveryDaemonID;
              await recoverRoutedFallbackDaemon(recoveryDaemonID);
            } finally {
              terminalRecovering = false;
            }
          };
          break;
        }
      }
    } finally {
      routedFallbackRecovering = false;
      if (workspaceMounted && routedFallbackRequestedDaemon !== null && !terminalDaemonFailure) {
        void recoverRoutedFallbackDaemon(routedFallbackRequestedDaemon);
      }
    }
  }

  function routeSignature(route: KataRouteSnapshot): string {
    return `${route.view ?? ""}\u0000${route.scope ?? ""}\u0000${route.issue ?? ""}`;
  }

  function fullRouteSignatureFor(route: KataRouteSnapshot): string {
    return `${routeSignature(route)}\u0000${requestedDaemonId ?? ""}`;
  }

  function fullRouteSignature(): string {
    return fullRouteSignatureFor(currentRouteSnapshot());
  }

  function actualIssueUID(): string | null {
    return store.pendingSelectionUID ?? store.selectedIssue?.issue.uid ?? null;
  }

  // Interactions that load the store and then emit the matching URL
  // update hold this count so the reconciler never treats their
  // store-ahead-of-URL window as drift to converge away.
  let routeEmissionWork = $state(0);

  async function withRouteEmission<T>(task: () => Promise<T>): Promise<T> {
    routeEmissionWork += 1;
    try {
      return await task();
    } finally {
      routeEmissionWork -= 1;
    }
  }

  function reconcilerBusy(): boolean {
    return (
      loading ||
      switchingDaemon ||
      routedFallbackRecovering ||
      terminalDaemonFailure ||
      restoreRetry !== null ||
      restoreRetrying ||
      routeEmissionWork > 0
    );
  }

  type RouteMismatch = "daemon" | "viewScope" | "select" | "clear" | null;

  // Read-only view work is intentionally absent: a daemon switch advances
  // the navigation generation and invalidates pending loads, so stale read
  // completions cannot repaint the new daemon. Only ownership setup, another
  // switch transaction, or non-supersedable writes hold the exclusive lock.
  function daemonSwitchLocked(): boolean {
    return (
      loading ||
      switchingDaemon ||
      provisionalRoutedDaemon !== null ||
      routedFallbackRecovering ||
      restoreRetry !== null ||
      terminalDaemonFailure ||
      store.hasPendingMutations ||
      workspaceActionBusy
    );
  }

  // The reconciler starts route list loads without awaiting so a newer
  // route can supersede and abort them. This records which route's list
  // load is in flight so re-evaluations do not start a duplicate.
  let viewScopeLoadSignature = $state<string | null>(null);

  function startViewScopeLoad(signature: string): void {
    beginNavigation();
    closeReachableGraph();
    resetDetailDrafts();
    store.invalidatePendingLoads();
    if ((selectedIssueUID ?? null) === null) {
      store.clearSelection();
      selectionFromRoute = false;
    }
    viewScopeLoadSignature = signature;
    const viewName = routeViewName ?? null;
    const scopeUID = routeScopeUID ?? null;
    void runViewTask(() => loadRouteViewScope(viewName, scopeUID), "view").then((ok) => {
      if (viewScopeLoadSignature === signature) {
        viewScopeLoadSignature = null;
      }
      if (!ok && fullRouteSignature() === signature) {
        failedRouteSignature = signature;
      }
    });
  }

  function routeMismatch(): RouteMismatch {
    if (requestedDaemonId !== null && requestedDaemonId !== activeKataDaemonId) {
      // An unknown routed daemon surfaces routedDaemonError instead of
      // looping; a roster change re-evaluates through daemonInfos.
      return daemonInfos.some((daemon) => daemon.id === requestedDaemonId) ? "daemon" : null;
    }
    const desiredScope = routeScopeUID ?? null;
    const desiredView = routeViewName ?? null;
    if (desiredScope !== scopeUIDFromFilters(store.searchFilters)) return "viewScope";
    // A null routed view constrains nothing while scoped (the backlog
    // and filtered variants both live under a bare scope route); without
    // a scope it means the default "all" view.
    if (desiredView !== null && desiredView !== store.currentView.name) return "viewScope";
    if (desiredView === null && desiredScope === null && store.currentView.name !== "all") return "viewScope";
    const desiredIssue = selectedIssueUID ?? null;
    const actualIssue = actualIssueUID();
    if (desiredIssue === actualIssue) return null;
    if (desiredIssue) return "select";
    return selectionFromRoute ? "clear" : null;
  }

  // Shared by the reconciler and bootstrap: load the routed view/scope
  // combination. Route-driven list loads never auto-select; the issue
  // convergence step owns the routed selection.
  async function loadRouteViewScope(viewName: KataTaskViewName | null, scopeUID: string | null): Promise<void> {
    store.resetSearchFilters();
    if (scopeUID) {
      await store.updateSearchFilters(
        { scope: { kind: "project", project_uid: scopeUID } },
        { selectFirst: false },
      );
      if (viewName) {
        await store.openView(viewName, { selectFirst: false });
      }
      return;
    }
    await store.openView(viewName ?? "all", { selectFirst: false });
  }

  async function reconcileRoute(): Promise<void> {
    reconciling = true;
    try {
      for (;;) {
        if (reconcilerBusy()) return;
        const signature = fullRouteSignature();
        if (failedRouteSignature !== null && failedRouteSignature !== signature) {
          // The route moved off a failed target; drop the stale failure
          // surface so the new destination starts clean.
          clearTaskErrors("view");
          failedRouteSignature = null;
        }
        if (failedRouteSignature === signature) return;
        const mismatch = routeMismatch();
        if (mismatch === null) {
          if ((selectedIssueUID ?? null) !== null) selectionFromRoute = true;
          return;
        }
        if (mismatch === "daemon") {
          if (daemonSwitchLocked()) return;
          await switchKataDaemon(requestedDaemonId!);
          if (requestedDaemonId !== null && requestedDaemonId !== activeKataDaemonId) {
            // The switch refused or failed; the effect re-fires when its
            // gates clear or the route moves.
            return;
          }
          continue;
        }
        if (mismatch === "viewScope") {
          if (viewScopeLoadSignature !== signature) {
            startViewScopeLoad(signature);
          }
          // The effect re-fires when the load lands (or fails) and the
          // loop resumes from the then-current route.
          return;
        }
        if (mismatch === "select") {
          // The current route's list must settle before its detail selection
          // starts. A superseded non-abortable load is harmless: the store's
          // request guard drops its late result.
          if (viewScopeLoadSignature === signature) return;
          beginNavigation();
          resetDetailDrafts();
          const uid = selectedIssueUID!;
          let selectionError: unknown;
          const ok = await runViewTask(
            async () => {
              try {
                return await store.selectIssue(uid);
              } catch (error) {
                selectionError = error;
                throw error;
              }
            },
            "view",
          );
          if (!ok) {
            if (pendingDirectGraphSelectionUID === uid) pendingDirectGraphSelectionUID = null;
            // A competing refresh can supersede this detail read without an
            // error. Leave the route retryable; the refresh completion will
            // re-run the reconciler. Only an actual request failure makes the
            // current route a terminal failed target.
            if (lastTaskError === null) return;
            // The routed task cannot be shown; keeping the previous
            // detail under the new URL would lie about what is open.
            if (fullRouteSignature() === signature) {
              store.clearSelection();
              selectionFromRoute = false;
              if (isDefinitiveRestoreFailure(selectionError)) {
                onRouteStateChange?.({ issue: null }, { replace: true });
              } else {
                failedRouteSignature = signature;
              }
              return;
            }
          } else {
            selectionFromRoute = true;
            if (store.selectedIssue) {
              const selected = store.selectedIssue.issue;
              store.rememberTasks([selected, ...(store.selectedIssue.children ?? [])]);
              void revealSelectedAncestors(selected, activeKataDaemonId ?? "home", () =>
                fullRouteSignature() === signature && store.selectedIssue?.issue.uid === uid,
              );
            }
            if (pendingDirectGraphSelectionUID === uid) {
              pendingDirectGraphSelectionUID = null;
              persistActiveWorkspaceState();
            }
          }
          continue;
        }
        // mismatch === "clear"
        beginNavigation();
        clearTaskErrors("view");
        resetDetailDrafts();
        store.clearSelection();
        selectionFromRoute = false;
        persistActiveWorkspaceState();
      }
    } finally {
      reconciling = false;
    }
  }

  // Route changes preempt in-flight loads: the abort unblocks a stalled
  // detail read immediately and the store's request guards drop any late
  // list results. Interaction echoes also land here, harmlessly — their
  // loads have already applied by the time they emit.
  $effect.pre(() => {
    void requestedDaemonId;
    void routeViewName;
    void routeScopeUID;
    void selectedIssueUID;
    untrack(() => {
      if (unknownRoutedBootstrapPending && requestedDaemonId === null) {
        unknownRoutedBootstrapPending = false;
        workspaceOwnershipPending = true;
        if (acceptedKataDaemonId) void recoverRoutedFallbackDaemon(acceptedKataDaemonId);
      }
      if (pendingDirectGraphSelectionUID !== null && pendingDirectGraphSelectionUID !== (selectedIssueUID ?? null)) {
        pendingDirectGraphSelectionUID = null;
      }
      if (loading && provisionalRoutedDaemon !== null && routedRestoreRouteSignature !== currentFullRouteSignature()) {
        const fallbackDaemon = routedRestoreFallbackDaemon ?? acceptedKataDaemonId;
        routedRestoreGeneration += 1;
        routedRestoreRouteSignature = null;
        provisionalRoutedDaemon = null;
        routedRestoreFallbackDaemon = undefined;
        restoreError = null;
        restoreRetry = null;
        restoreRetryRouteSignature = null;
        if (fallbackDaemon) void recoverRoutedFallbackDaemon(fallbackDaemon);
      }
      if (routedFallbackRecovering && routedRestoreRouteSignature !== currentFullRouteSignature()) {
        routedRestoreGeneration += 1;
        routedRestoreRouteSignature = null;
        if (acceptedKataDaemonId) routedFallbackRequestedDaemon = acceptedKataDaemonId;
      }
      if (switchingDaemon && switchRouteSignature !== currentFullRouteSignature()) {
        switchGeneration += 1;
        switchingDaemon = false;
        switchRouteSignature = null;
        store.bindDaemonForBootstrap(acceptedKataDaemonId ?? "home");
        restoreCursorCatchupError(switchPreviousCursorCatchupError, switchPreviousCursorCatchupRetry);
        if (switchPreviousEventStreamRunning) startEventStream();
      }
      if (loading || switchingDaemon) return;
      // Echoes of already-applied interactions (the store converged
      // before emitting) must not abort their own follow-up reads,
      // e.g. the selected task's slow event-log walk.
      if (routeMismatch() === null) return;
      beginNavigation();
      store.invalidatePendingLoads();
    });
  });

  $effect(() => {
    // Every input the reconciler compares is read here so the effect
    // re-fires when any of them changes, including changes that land
    // while a previous reconcile pass is still draining.
    void requestedDaemonId;
    void routeViewName;
    void routeScopeUID;
    void selectedIssueUID;
    void store.daemonId;
    void activeKataDaemonId;
    void store.currentView.name;
    void store.searchFilters;
    void store.pendingSelectionUID;
    void store.selectedIssue;
    void daemonInfos;
    if (ancestorRevealRetryRouteSignature !== null && ancestorRevealRetryRouteSignature !== fullRouteSignature()) {
      ancestorRevealError = null;
      ancestorRevealRetry = null;
      ancestorRevealRetryRouteSignature = null;
    }
    if (restoreRetryRouteSignature !== null && restoreRetryRouteSignature !== currentFullRouteSignature()) {
      restoreError = null;
      restoreRetry = null;
      restoreRetryRouteSignature = null;
      if (provisionalRoutedDaemon !== null) {
        const fallbackDaemon = routedRestoreFallbackDaemon ?? acceptedKataDaemonId;
        routedRestoreGeneration += 1;
        routedRestoreRouteSignature = null;
        provisionalRoutedDaemon = null;
        routedRestoreFallbackDaemon = undefined;
        if (fallbackDaemon) void untrack(() => recoverRoutedFallbackDaemon(fallbackDaemon));
      }
    }
    void viewWorkCount;
    void store.hasPendingMutations;
    void workspaceActionBusy;
    if (reconciling || reconcilerBusy()) return;
    if (failedRouteSignature !== null && failedRouteSignature !== fullRouteSignature()) {
      // The route moved off a failed target; drop the stale failure
      // surface even when the new route is already converged.
      clearTaskErrors("view");
      failedRouteSignature = null;
    }
    if (failedRouteSignature === fullRouteSignature()) return;
    const mismatch = routeMismatch();
    if (mismatch === null) {
      if ((selectedIssueUID ?? null) !== null && actualIssueUID() === (selectedIssueUID ?? null)) {
        selectionFromRoute = true;
      }
      return;
    }
    if (mismatch === "daemon" && daemonSwitchLocked()) return;
    // A project route load applies its scope before its optional view load
    // finishes. That intermediate state can leave only the issue selection
    // mismatched, but selecting now would be aborted when the remaining view
    // load clears selection. Only the current route's load must settle; stale
    // loads are rejected by the store when they eventually return.
    const signature = fullRouteSignature();
    if (viewScopeLoadSignature === signature && mismatch !== "viewScope") return;
    if (mismatch === "viewScope" && viewScopeLoadSignature === signature) return;
    void untrack(() => reconcileRoute());
  });

  // After an interaction load shifts the selection away from a routed
  // issue, the URL must follow the store: a stale issue param would make
  // the reconciler revert the shift on its next pass.
  function emitRouteSelectionSync(): void {
    const actual = actualIssueUID();
    if ((selectedIssueUID ?? null) !== null && (selectedIssueUID ?? null) !== actual) {
      onRouteStateChange?.({ issue: actual }, { replace: true });
    }
  }

  function selectedProjectName(): string | null {
    const scope = store.searchFilters.scope;
    if (scope.kind !== "project") return null;
    return store.projects.find((project) => project.uid === scope.project_uid)?.name ?? null;
  }

  function projectNameForIssue(issue: KataTaskSummary): string | null {
    const project = store.projects.find((candidate) => candidate.uid === issue.project_uid);
    return project?.name ?? issue.project_name ?? null;
  }

  function ownerOptions(): TypeaheadOption[] {
    const seen = new Set<string>();
    return [store.selectedIssue?.issue.owner, ...visibleIssues().map((issue) => issue.owner)]
      .filter((owner): owner is string => typeof owner === "string" && owner.trim() !== "")
      .filter((owner) => {
        const key = owner.toLowerCase();
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
      .map((owner) => ({ value: owner, label: owner }));
  }

  function listTitle(): string {
    return selectedProjectName() ?? systemViews.find((view) => view.name === store.currentView.name)?.label ?? "Kata";
  }

  function activeDaemonStatusLabel(): string | undefined {
    if (routedDaemonError) return routedDaemonError;
    if (error) return error;
    if (
      store.daemonId &&
      getKataDaemonRosterLoaded() &&
      !getKataDaemonRoster().includes(store.daemonId)
    ) {
      return "Daemon is no longer configured";
    }
    if (store.connection.status !== "error") return undefined;
    return store.connection.message ?? "Connection failed";
  }

  function resetIssueExpansion(): void {
    listResetGeneration += 1;
  }

  function currentExpansionSignature(): string {
    const issueParts = store.currentView.groups.flatMap((group) =>
      group.issues.map(
        (issue) =>
          `${group.id}:${issue.uid}:${issue.revision}:${issue.parent_short_id ?? ""}:${issue.child_counts?.open ?? 0}:${issue.child_counts?.total ?? 0}`,
      ),
    );
    return [activeKataDaemonId ?? "", store.currentView.name, store.currentView.fetched_at ?? "", ...issueParts].join("|");
  }

  function visibleIssues(): KataTaskSummary[] {
    return store.currentView.groups.flatMap((group) => group.issues);
  }

  function persistActiveWorkspaceState(): void {
    const daemonID = activeKataDaemonId;
    if (!daemonID || switchingDaemon || restoreRetry !== null || provisionalRoutedDaemon !== null || terminalDaemonFailure) return;
    saveKataWorkspaceState(daemonID, {
      view: store.currentView.name,
      filters: store.searchFilters,
      selectedIssueUID: store.selectedIssue?.issue.uid ?? null,
    });
  }

  function selectedIssueHasRawResultMembership(selectedUID: string | null | undefined = store.selectedIssue?.issue.uid): boolean {
    if (!selectedUID) return true;
    return store.currentView.groups
      .flatMap((group) => group.issues)
      .some((issue) => issue.uid === selectedUID && statusMatches(issue, listStatusFilter));
  }

  function canonicalizeClearedSelection(): void {
    const route = canonicalRoute(
      store.currentView.name,
      scopeUIDFromFilters(store.searchFilters),
      null,
      routeViewName !== null,
    );
    selectionFromRoute = false;
    onRouteStateChange?.(route, { replace: true });
    if (!onRouteStateChange) onSelectedIssueChange?.(null);
  }

  function reconcileSelectionMembership(
    daemonID: string,
    afterAcceptedMutation: boolean,
    selectedUID?: string | null,
    emitRoute = true,
    mutatePersistence = true,
  ): RestorePersistenceDelta | undefined {
    const capturedUID = selectedUID === undefined
      ? (store.pendingSelectionUID ?? store.selectedIssue?.issue.uid ?? null)
      : selectedUID;
    const currentUID = store.pendingSelectionUID ?? store.selectedIssue?.issue.uid ?? null;
    // A completed new selection wins a stale refresh, but a missing current
    // selection means the authoritative refresh itself removed the capture.
    if (!capturedUID || (currentUID !== null && currentUID !== capturedUID)) return;
    if (!selectedIssueHasRawResultMembership(capturedUID)) {
      resetDetailDrafts();
      store.clearSelection();
      if (emitRoute) canonicalizeClearedSelection();
      if (afterAcceptedMutation) {
        persistActiveWorkspaceState();
      } else if (mutatePersistence) {
        clearKataWorkspaceSelection(daemonID);
      } else {
        return { clearSelection: true };
      }
      return;
    }

    if (afterAcceptedMutation) persistActiveWorkspaceState();
    return undefined;
  }

  function reconcilePersistedSelection(afterAcceptedMutation: boolean, selectedUID?: string | null): void {
    const daemonID = activeKataDaemonId;
    if (!daemonID || switchingDaemon) return;
    reconcileSelectionMembership(daemonID, afterAcceptedMutation, selectedUID);
  }

  function selectedIssueMatchesStatusFilter(status: KataTaskSearchFilters["status"]): boolean {
    const selected = store.selectedIssue?.issue;
    return !selected || status === "all" || selected.status === status;
  }

  function loadLayoutPrefs(): void {
    if (typeof window === "undefined") return;
    try {
      const raw = window.localStorage.getItem(layoutStorageKey);
      if (!raw) return;
      const parsed = JSON.parse(raw) as Partial<{
        orientation: SplitOrientation;
        sizes: Partial<Record<SplitOrientation, number>>;
      }>;
      if (parsed.orientation === "vertical" || parsed.orientation === "horizontal") {
        splitOrientation = parsed.orientation;
      }
      const sizes = parsed.sizes ?? {};
      const next: Record<SplitOrientation, number> = { ...defaultSplitSizes };
      for (const key of ["vertical", "horizontal"] as const) {
        const value = sizes[key];
        if (typeof value === "number" && Number.isFinite(value) && value > 0) {
          next[key] = value;
        }
      }
      splitSizes = next;
    } catch {
      // Corrupt or unavailable browser preferences should not block the workspace.
    }
  }

  function saveLayoutPrefs(): void {
    if (typeof window === "undefined") return;
    try {
      window.localStorage.setItem(
        layoutStorageKey,
        JSON.stringify({ orientation: splitOrientation, sizes: splitSizes }),
      );
    } catch {
      // Best-effort browser preference.
    }
  }

  function toggleSplitOrientation(): void {
    splitOrientation = splitOrientation === "vertical" ? "horizontal" : "vertical";
    saveLayoutPrefs();
  }

  function handleSashResize(size: number): void {
    splitSizes = { ...splitSizes, [splitOrientation]: size };
    saveLayoutPrefs();
  }

  function stopEventStream(resetReconnect = true): void {
    eventStreamRunning = false;
    eventStream.stop(resetReconnect);
  }

  function startEventStream(reconnecting = false): void {
    eventStreamRunning = true;
    eventStream.start(reconnecting);
  }

  interface CursorCatchupScope {
    daemonID: string | undefined;
    generation: number;
  }

  function captureCursorCatchupScope(): CursorCatchupScope {
    return { daemonID: activeKataDaemonId, generation: cursorCatchupGeneration };
  }

  function isCursorCatchupScopeCurrent(scope: CursorCatchupScope): boolean {
    return cursorCatchupMounted && scope.generation === cursorCatchupGeneration && scope.daemonID === activeKataDaemonId;
  }

  function invalidateCursorCatchup(): void {
    cursorCatchupGeneration += 1;
    cursorCatchupRetrying = false;
    cursorCatchupError = null;
    cursorCatchupRetry = null;
  }

  function restoreCursorCatchupError(error: string | null, retry: (() => Promise<void>) | null): void {
    cursorCatchupError = error;
    cursorCatchupRetry = retry;
    cursorCatchupRetrying = false;
  }

  async function retryEventCursorCatchup(startStreamAfterSuccess = false): Promise<void> {
    if (cursorCatchupRetrying) return;
    const scope = captureCursorCatchupScope();
    cursorCatchupRetrying = true;
    try {
      await syncEventCursorAndReconcileSelection(scope);
      if (isCursorCatchupScopeCurrent(scope)) {
        terminalDaemonFailure = false;
        terminalRecovery = null;
        error = null;
        if (startStreamAfterSuccess) {
          workspaceOwnershipPending = false;
          startEventStream();
        }
      }
    } catch (error) {
      if (isCursorCatchupScopeCurrent(scope)) {
        cursorCatchupError = kataRequestErrorMessage(error);
        cursorCatchupRetry = () => retryEventCursorCatchup(startStreamAfterSuccess);
      }
    } finally {
      if (isCursorCatchupScopeCurrent(scope)) cursorCatchupRetrying = false;
    }
  }

  async function syncEventCursorAndReconcileSelection(
    scope = captureCursorCatchupScope(),
    mutatePersistence = true,
    emitRoute = true,
  ): Promise<RestorePersistenceDelta | undefined> {
    const selectedUID = store.selectedIssue?.issue.uid ?? null;
    try {
      const membershipRefreshed = await store.syncEventCursor({ shouldApply: () => isCursorCatchupScopeCurrent(scope) });
      if (!isCursorCatchupScopeCurrent(scope)) return;
      const persistenceDelta = membershipRefreshed
        ? reconcileSelectionMembership(scope.daemonID ?? "", false, selectedUID, emitRoute, mutatePersistence)
        : undefined;
      cursorCatchupError = null;
      cursorCatchupRetry = null;
      return persistenceDelta;
    } catch (error) {
      if (!isCursorCatchupScopeCurrent(scope)) return;
      if (error instanceof KataEventCursorSyncError) {
        const persistenceDelta = reconcileSelectionMembership(
          scope.daemonID ?? "",
          false,
          selectedUID,
          emitRoute,
          mutatePersistence,
        );
        cursorCatchupError = kataRequestErrorMessage(error.cursorSyncCause);
        cursorCatchupRetry = retryEventCursorCatchup;
        return persistenceDelta;
      }
      throw error;
    }
  }

  async function updateSearchFilters(filters: Partial<KataTaskSearchFilters>): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      const memory = captureWorkspaceMemory();
      const nextStatus = filters.status ?? store.searchFilters.status;
      closeReachableGraph();
      resetDetailDrafts();
      // A pending detail load is abandoned by the filter reload, so drop
      // it before awaiting.
      store.invalidatePendingLoads();
      if (!selectedIssueMatchesStatusFilter(nextStatus)) {
        store.clearSelection();
      }
      const ok = await runViewTask(() => store.updateSearchFilters(filters), "view");
      if (!ok) {
        if (isCurrentNavigation(generation)) restoreWorkspaceMemory(memory);
        return;
      }
      if (!isCurrentNavigation(generation)) return;
      const nextScopeUID = scopeUIDFromFilters(store.searchFilters);
      if (nextScopeUID !== (routeScopeUID ?? null)) {
        const nextViewName = nextScopeUID
          ? null
          : routeViewName ?? (store.currentView.name === "all" ? null : store.currentView.name);
        onRouteStateChange?.({
          view: nextViewName,
          scope: nextScopeUID,
          issue: store.selectedIssue?.issue.uid ?? null,
        });
      } else {
        emitRouteSelectionSync();
      }
      persistActiveWorkspaceState();
    });
  }

  async function openRoutedSystemView(viewName: KataTaskViewName, direct = false): Promise<void> {
    const filters = store.searchFilters;
    const viewAlreadyLoaded =
      store.currentView.fetched_at !== undefined &&
      store.currentView.name === viewName &&
      filters.scope.kind === "all" &&
      filters.status === "open" &&
      filters.owner.trim() === "" &&
      filters.label.trim() === "" &&
      filters.query.trim() === "";
    const routeAlreadyCanonical =
      ((routeViewName ?? null) === viewName || (viewName === "all" && routeViewName === null)) &&
      (routeScopeUID ?? null) === null &&
      (selectedIssueUID ?? null) === null;
    if (
      viewAlreadyLoaded &&
      routeAlreadyCanonical &&
      actualIssueUID() === null &&
      listMode === "tasks" &&
      viewScopeLoadSignature === null
    ) {
      return;
    }
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      const memory = captureWorkspaceMemory();
      closeReachableGraph();
      resetDetailDrafts();
      store.resetSearchFilters();
      // Clear (and thereby abort) the abandoned selection before awaiting
      // the new view: while that fetch is in flight, a still-running
      // detail load could fail and surface a stale error for a selection
      // this navigation has already discarded.
      store.clearSelection();
      selectionFromRoute = false;
      if (viewAlreadyLoaded) {
        // Re-selecting the loaded view still preempts a superseded routed
        // list load. Its response cannot be allowed to repaint after this
        // interaction has declared the current view authoritative.
        if (viewScopeLoadSignature !== null) {
          store.invalidatePendingLoads();
          viewScopeLoadSignature = null;
        }
      } else {
        const ok = await runViewTask(() => store.openView(viewName, { selectFirst: false }), "view");
        if (!ok) {
          if (isCurrentNavigation(generation)) restoreWorkspaceMemory(memory);
          return;
        }
        if (!isCurrentNavigation(generation)) return;
      }
      if (!routeAlreadyCanonical) {
        onRouteStateChange?.({
          view: viewName,
          scope: null,
          issue: null,
        });
      }
      if (direct) persistActiveWorkspaceState();
    });
  }

  async function openRoutedProjectScope(projectUID: string, direct = false): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      const memory = captureWorkspaceMemory();
      closeReachableGraph();
      resetDetailDrafts();
      // Scope changes keep a completed selection but abandon an in-flight
      // one (the scoped reload re-selects from selectedIssue, which a
      // pending load hasn't populated). Invalidate up front so the doomed
      // detail request can't fail into a stale workspace error while the
      // scoped list is still loading.
      store.invalidatePendingLoads();
      const ok = await runViewTask(
        () => store.updateSearchFilters({ scope: { kind: "project", project_uid: projectUID } }),
        "view",
      );
      if (!ok) {
        if (isCurrentNavigation(generation)) restoreWorkspaceMemory(memory);
        return;
      }
      if (!isCurrentNavigation(generation)) return;
      onRouteStateChange?.({
        view: null,
        scope: projectUID,
        issue: store.selectedIssue?.issue.uid ?? null,
      });
      if (direct) persistActiveWorkspaceState();
    });
  }

  function scheduleProjectScope(projectUID: string): void {
    void openRoutedProjectScope(projectUID, true);
  }

  async function createKataProject(name: string): Promise<KataProjectSummary> {
    if (workspaceActionsBlocked) throw new Error("Kata workspace is not writable.");
    let created: KataProjectSummary | undefined;
    await runViewTaskOrThrow(async () => {
      created = await store.createProject(name);
    });
    return created!;
  }

  async function submitQuickCapture(title: string): Promise<void> {
    if (workspaceActionsBlocked) return;
    await withRouteEmission(async () => {
      await runViewTaskOrThrow(async () => {
        closeReachableGraph();
        resetDetailDrafts();
        await store.captureIssue(actor, { title });
      });
      // Capture navigates the workspace to the inbox and selects the
      // created task; the URL follows both.
      onRouteStateChange?.({
        view: store.currentView.name,
        scope: scopeUIDFromFilters(store.searchFilters),
        issue: actualIssueUID(),
      });
      persistActiveWorkspaceState();
    });
  }

  interface KataWorkspaceMemory {
    store: KataWorkspaceStoreSnapshot;
    selectionFromRoute: boolean;
    restoredSelectionUID: string | null;
  }

  function captureWorkspaceMemory(): KataWorkspaceMemory {
    return {
      store: store.captureSnapshot(),
      selectionFromRoute,
      restoredSelectionUID,
    };
  }

  function restoreWorkspaceMemory(memory: KataWorkspaceMemory): void {
    store.restoreSnapshot(memory.store);
    selectionFromRoute = memory.selectionFromRoute;
    restoredSelectionUID = memory.restoredSelectionUID;
  }

  function acceptWorkspaceStore(candidate: ReturnType<typeof createKataWorkspaceStore>): void {
    store.restoreSnapshot(candidate.captureSnapshot());
  }

  function switchOwnsTransaction(generation: number, routeSignature: string): boolean {
    return (
      workspaceMounted &&
      switchingDaemon &&
      generation === switchGeneration &&
      routeSignature === switchRouteSignature
    );
  }

  async function switchKataDaemon(id: string): Promise<void> {
    if (daemonSwitchLocked()) return;
    const previousDaemonID = store.daemonId ?? activeKataDaemonId;
    if (id === previousDaemonID) {
      if (requestedDaemonId === id) {
        onRouteStateChange?.({ ...currentRouteSnapshot(), daemon: null }, { replace: true });
      }
      return;
    }

    const previousView = store.currentView.name;
    const previousWorkspace = captureWorkspaceMemory();
    const ownedRouteSignature = fullRouteSignature();
    let committedRouteSignature: string | null = null;
    const switchTransactionCanApply = (): boolean =>
      switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature) ||
      (committedRouteSignature !== null &&
        workspaceMounted &&
        !switchingDaemon &&
        store.daemonId === id &&
        fullRouteSignature() === committedRouteSignature);
    const routeMatchesOwnedSwitch = (): boolean => fullRouteSignature() === ownedRouteSignature;
    const routedSwitchRoute = requestedDaemonId === id ? currentRouteSnapshot() : null;
    const previousCursorCatchupError = cursorCatchupError;
    const previousCursorCatchupRetry = cursorCatchupRetry;
    switchPreviousCursorCatchupError = previousCursorCatchupError;
    switchPreviousCursorCatchupRetry = previousCursorCatchupRetry;
    switchPreviousEventStreamRunning = eventStreamRunning;
    invalidateCursorCatchup();
    switchingDaemon = true;
    terminalDaemonFailure = false;
    terminalRecovery = null;
    const ownedSwitchGeneration = ++switchGeneration;
    switchRouteSignature = ownedRouteSignature;
    beginNavigation();
    closeReachableGraph();
    resetDetailDrafts();
    resetIssueExpansion();
    store.invalidatePendingLoads();
    stopEventStream();

    const recoverPreviousWorkspace = async (
      shouldApply: () => boolean = () => switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature),
    ): Promise<boolean> => {
      if (!previousDaemonID) throw new Error("Previous Kata daemon is unavailable.");
      if (!shouldApply()) return false;
      store.clearDaemonState(previousView);
      store.bindDaemonForBootstrap(previousDaemonID);
      setActiveKataDaemon(previousDaemonID, false);
      restoreWorkspaceMemory(previousWorkspace);
      store.bindDaemonForBootstrap(previousDaemonID);
      await store.api.instance();
      if (!shouldApply()) return false;
      const rollbackSelectedUID = store.selectedIssue?.issue.uid ?? null;
      let rollbackMembershipRefreshed = false;
      try {
        rollbackMembershipRefreshed = await store.syncEventCursor({ shouldApply });
        if (!shouldApply()) return false;
        restoreCursorCatchupError(previousCursorCatchupError, previousCursorCatchupRetry);
      } catch (catchupError) {
        if (!shouldApply()) return false;
        if (!(catchupError instanceof KataEventCursorSyncError)) throw catchupError;
        rollbackMembershipRefreshed = true;
        cursorCatchupError = kataRequestErrorMessage(catchupError.cursorSyncCause);
        cursorCatchupRetry = retryEventCursorCatchup;
      }
      if (!shouldApply()) return false;
      if (rollbackMembershipRefreshed) {
        reconcileSelectionMembership(previousDaemonID, false, rollbackSelectedUID, false);
      }
      const rollbackIssueUID = store.selectedIssue?.issue.uid ?? null;
      const rollbackRoute = canonicalRoute(
        store.currentView.name,
        scopeUIDFromFilters(store.searchFilters),
        rollbackIssueUID,
      );
      selectionFromRoute = rollbackIssueUID !== null && previousWorkspace.selectionFromRoute;
      restoredSelectionUID = previousWorkspace.restoredSelectionUID;
      if (requestedDaemonId === id) {
        onRouteStateChange?.({ ...rollbackRoute, daemon: null }, { replace: true });
      } else if (fullRouteSignature() === ownedRouteSignature) {
        if (onRouteStateChange) {
          onRouteStateChange(rollbackRoute, { replace: true });
        } else {
          onSelectedIssueChange?.(rollbackIssueUID);
        }
      }
      terminalDaemonFailure = false;
      terminalRecovery = null;
      error = null;
      startEventStream();
      return true;
    };

    try {
      const candidate = createKataWorkspaceStore({ api: store.api });
      candidate.bindDaemonForBootstrap(id, false);
      await candidate.api.instance({ daemonId: id });
      const restored = await restoreKataWorkspaceState(
        id,
        routedSwitchRoute ?? { view: null, scope: null, issue: null },
        false,
        false,
        routedSwitchRoute === null,
        null,
        switchTransactionCanApply,
        "daemon-generation",
        candidate,
      );
      if (!switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature) || !restored.startEventStream) {
        throw new Error("Kata daemon workspace could not be restored.");
      }
      if (restoreRetry !== null) {
        throw new Error(restoreError ?? "Kata daemon workspace could not be restored.");
      }
      if (routedSwitchRoute?.issue && candidate.selectedIssue?.issue.uid !== routedSwitchRoute.issue) {
        try {
          await candidate.selectIssue(routedSwitchRoute.issue, {
            shouldApply: () => switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature),
          });
        } catch (selectionError) {
          if (!isDefinitiveRestoreFailure(selectionError)) throw selectionError;
          candidate.clearSelection();
          restored.route = { ...restored.route, issue: null };
        }
      }
      await candidate.awaitSelectedAuxiliaryReads();
      const targetSelectedUID = candidate.selectedIssue?.issue.uid ?? null;
      let targetMembershipRefreshed = false;
      try {
        targetMembershipRefreshed = await candidate.syncEventCursor({
          shouldApply: () => switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature),
        });
        cursorCatchupError = null;
        cursorCatchupRetry = null;
      } catch (catchupError) {
        if (!(catchupError instanceof KataEventCursorSyncError)) throw catchupError;
        if (!switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature)) {
          throw new Error("Kata daemon switch was superseded.");
        }
        targetMembershipRefreshed = true;
        cursorCatchupError = kataRequestErrorMessage(catchupError.cursorSyncCause);
        cursorCatchupRetry = retryEventCursorCatchup;
      }
      if (!switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature)) {
        throw new Error("Kata daemon switch was superseded.");
      }
      if (!routeMatchesOwnedSwitch()) throw new Error("Kata daemon switch was superseded.");
      acceptWorkspaceStore(candidate);
      store.bindDaemonForBootstrap(id);
      const cursorPersistenceDelta = targetMembershipRefreshed
        ? reconcileSelectionMembership(id, false, targetSelectedUID, false, false)
        : undefined;

      setActiveKataDaemon(id);
      clearTaskErrors("view");
      applyRestorePersistenceDelta(
        id,
        mergeRestorePersistenceDelta(restored.persistenceDelta, cursorPersistenceDelta),
      );
      const acceptedIssueUID =
        store.selectedIssue?.issue.uid ??
        (restored.sources.selection === "url" ? restored.route.issue : null);
      const acceptedRoute = { ...restored.route, issue: acceptedIssueUID };
      selectionFromRoute = acceptedIssueUID !== null && acceptedIssueUID === selectedIssueUID;
      restoredSelectionUID = acceptedIssueUID === restored.restoredSelectionUID ? restored.restoredSelectionUID : null;
      if (requestedDaemonId === id) {
        onRouteStateChange?.({ ...acceptedRoute, daemon: null }, { replace: true });
      } else if (fullRouteSignature() === ownedRouteSignature) {
        if (onRouteStateChange) {
          onRouteStateChange(acceptedRoute);
        } else {
          onSelectedIssueChange?.(acceptedIssueUID);
        }
      }
      committedRouteSignature = fullRouteSignature();
      if (restored.ancestorReveal) {
        revealRequest = { ...restored.ancestorReveal, generation: ++revealGeneration };
      } else if (restored.ancestorRevealError && store.selectedIssue) {
        ancestorRevealError = restored.ancestorRevealError;
        const restoredIssue = store.selectedIssue.issue;
        ancestorRevealRetryRouteSignature = committedRouteSignature;
        ancestorRevealRetry = async () => {
          if (!switchTransactionCanApply() || store.selectedIssue?.issue.uid !== restoredIssue.uid) return;
          ancestorRevealError = null;
          await revealSelectedAncestors(restoredIssue, id, switchTransactionCanApply);
        };
      }
      persistActiveWorkspaceState();
      startEventStream();
    } catch (targetError) {
      restoreError = null;
      restoreRetry = null;
      restoreRetryRouteSignature = null;
      ancestorRevealError = null;
      ancestorRevealRetry = null;
      ancestorRevealRetryRouteSignature = null;
      const targetMessage = kataRequestErrorMessage(targetError);
      try {
        if (!workspaceMounted) return;
        if (!switchOwnsTransaction(ownedSwitchGeneration, ownedRouteSignature)) return;
        const recovered = await recoverPreviousWorkspace();
        if (recovered && workspaceMounted) showFlash(targetMessage, { tone: "danger" });
      } catch (rollbackError) {
        terminalDaemonFailure = true;
        setActiveKataDaemon(previousDaemonID, false);
        restoreWorkspaceMemory(previousWorkspace);
        if (previousDaemonID) store.bindDaemonForBootstrap(previousDaemonID);
        stopEventStream();
        error = `${targetMessage} ${kataRequestErrorMessage(rollbackError)}`;
        terminalRecovery = async () => {
          if (terminalRecovering) return;
          const recoveryRouteSignature = fullRouteSignature();
          terminalRecovering = true;
          try {
            await recoverPreviousWorkspace(
              () => workspaceMounted && terminalRecovering && fullRouteSignature() === recoveryRouteSignature,
            );
          } catch (recoveryError) {
            terminalDaemonFailure = true;
            error = `${targetMessage} ${kataRequestErrorMessage(recoveryError)}`;
          } finally {
            terminalRecovering = false;
          }
        };
      }
    } finally {
      if (ownedSwitchGeneration === switchGeneration) {
        switchingDaemon = false;
        switchRouteSignature = null;
        if (workspaceMounted && routeMismatch() !== null) {
          beginNavigation();
          store.invalidatePendingLoads();
        }
      }
    }
  }

  function resetDetailDrafts(): void {
    checklistRevealed = false;
    recurrenceDialogs?.closeAll();
  }

  async function selectIssue(uid: string, notify = true, direct = notify): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      resetDetailDrafts();
      const ok = await runViewTask(() => store.selectIssue(uid), "view");
      if (!ok || !isCurrentNavigation(generation)) return;
      if (notify) onSelectedIssueChange?.(uid);
      if (direct) persistActiveWorkspaceState();
    });
  }

  function selectReachableGraphIssue(uid: string): void {
    if (onSelectedIssueChange && selectedIssueUID !== uid) {
      // Route the node first; the echo lands synchronously and the
      // reconciler applies the selection (and owns its failure surface).
      pendingDirectGraphSelectionUID = uid;
      onSelectedIssueChange(uid);
      return;
    }
    void selectIssue(uid, false, true);
  }

  function openReachableGraph(issue: KataTaskSummary): void {
    store.rememberTasks([issue]);
    graphSourceIssue = issue;
    listMode = "reachableGraph";
  }

  function rememberGraphTasks(tasks: readonly KataTaskSummary[]): void {
    store.rememberTasks(tasks);
  }

  function closeReachableGraph(): void {
    listMode = "tasks";
    graphSourceIssue = null;
  }

  async function moveSelectedIssue(toProjectUID: string): Promise<boolean> {
    const selected = store.selectedIssue?.issue;
    if (workspaceActionsBlocked || !selected || !toProjectUID || pendingMoveIssueUIDs.has(selected.uid)) return false;
    const sourceIssueUID = selected.uid;
    const generation = navigationGeneration;
    pendingMoveIssueUIDs = new Set(pendingMoveIssueUIDs).add(sourceIssueUID);
    try {
      const ok = await runViewTask(
        () => store.moveIssue(sourceIssueUID, actor, toProjectUID),
        "flash",
        () => isCurrentNavigation(generation),
      );
      if (ok && isCurrentNavigation(generation)) reconcilePersistedSelection(true);
      return ok;
    } finally {
      const nextPendingMoves = new Set(pendingMoveIssueUIDs);
      nextPendingMoves.delete(sourceIssueUID);
      pendingMoveIssueUIDs = nextPendingMoves;
    }
  }

  async function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => store.patchMetadata(uid, actor, patch), "flash");
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function addSelectedComment(uid: string, body: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    return runViewTask(() => store.addComment(uid, actor, body), "flash");
  }

  async function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => store.editIssue(uid, actor, patch), "flash");
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function assignSelectedOwner(uid: string, owner: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => store.assignOwner(uid, actor, owner), "flash");
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function unassignSelectedOwner(uid: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => store.unassignOwner(uid, actor), "flash");
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function setSelectedPriority(uid: string, priority: number | null): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => store.setPriority(uid, actor, priority), "flash");
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function addSelectedLabel(uid: string, label: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => store.addLabel(uid, actor, label), "flash");
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function removeSelectedLabel(uid: string, label: string): Promise<void> {
    if (workspaceActionsBlocked) return;
    const ok = await runViewTask(() => store.removeLabel(uid, actor, label), "flash");
    if (ok) reconcilePersistedSelection(true);
  }

  function selectedMessageLinks(): MessageLinkRef[] {
    return store.selectedIssue ? readMessageLinks(store.selectedIssue.issue.metadata) : [];
  }

  function openWorkspace(id: string): void {
    navigate(`/terminal/${encodeURIComponent(id)}`);
  }

  async function createWorkspaceForSelectedIssue(): Promise<void> {
    const selected = store.selectedIssue?.issue;
    if (workspaceActionsBlocked || !selected || workspaceActionBusy) return;
    workspaceActionBusy = true;
    try {
      const created = await createKataWorkspaceForTask(
        kataWorkspaceIdentityFromIssue(
          selected,
          store.daemonId ?? activeKataDaemonId ?? null,
          projectNameForIssue(selected),
        ),
      );
      openWorkspace(created.id);
    } catch (err) {
      showFlash(kataRequestErrorMessage(err), { tone: "danger" });
    } finally {
      workspaceActionBusy = false;
    }
  }

  function selectedWorkspaceAction():
    | { label: string; busy?: boolean; disabled?: boolean; onClick: () => void | Promise<void> }
    | undefined {
    if (!workspaceTarget?.available) return undefined;
    if (workspaceTarget.existing_workspace) {
      const id = workspaceTarget.existing_workspace.id;
      return {
        label: "Open workspace",
        onClick: () => openWorkspace(id),
      };
    }
    return {
      label: "Create workspace",
      busy: workspaceActionBusy,
      disabled: workspaceActionsBlocked,
      onClick: createWorkspaceForSelectedIssue,
    };
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  async function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<void> {
    if (workspaceActionsBlocked) return;
    await runViewTaskOrThrow(async () => {
      await store.createRecurrence(projectID, input);
    }, "none");
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    if (workspaceActionsBlocked) return;
    await runViewTaskOrThrow(async () => {
      await store.patchRecurrence(id, input, etag);
    }, "none");
  }

  async function deleteRecurrence(recurrence: KataRecurrence): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    return runViewTask(() => store.deleteRecurrence(recurrence.id, actor), "flash");
  }

  async function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Promise<boolean> {
    const selected = store.selectedIssue;
    if (workspaceActionsBlocked || !selected) return false;
    const ok = await runViewTask(
      () =>
        store.closeIssue(selected.issue.uid, actor, {
          reason,
          message,
        }),
      "flash",
    );
    if (ok) reconcilePersistedSelection(true);
    return ok;
  }

  async function reopenSelectedIssue(): Promise<void> {
    const selected = store.selectedIssue;
    if (workspaceActionsBlocked || !selected) return;
    const ok = await runViewTask(() => store.reopenIssue(selected.issue.uid, actor), "flash");
    if (ok) reconcilePersistedSelection(true);
  }

  async function deleteSelectedIssue(): Promise<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from issue detail.");
  }

  async function unlinkMessageLink(link: MessageLinkRef): Promise<void> {
    if (workspaceActionsBlocked || unlinkBusyIds.size > 0) return;
    const selected = store.selectedIssue;
    if (!selected) return;
    const uid = selected.issue.uid;
    const links = selectedMessageLinks();
    const patch = computeRemoveMessageLinkPatch(links, link.message_id);
    if (patch === null) return;
    const metadataPatch: Record<string, unknown> = { mail_links: patch.mail_links };

    unlinkBusyIds = new Set(links.map((item) => item.message_id));
    try {
      const ok = await runViewTask(() => store.patchMetadata(uid, actor, metadataPatch), "none");
      if (!ok) {
        showFlash(lastTaskError || "Could not unlink message.", { tone: "danger" });
      }
    } finally {
      unlinkBusyIds = new Set();
    }
  }
</script>

<section class="kata-feature" aria-labelledby="kata-title" inert={switchingDaemon} aria-busy={switchingDaemon || restoreRetrying}>
  <header class="kata-header">
    <div class="kata-header-title">
      <h1 id="kata-title">Kata</h1>
      {#if daemonInfos.length > 0}
        <KataDaemonSwitcher
          daemons={daemonInfos}
          activeId={provisionalRoutedDaemon === null ? activeKataDaemonId : acceptedKataDaemonId}
          activeStatusLabel={activeDaemonStatusLabel()}
          activeStatusTone={activeDaemonStatusLabel() ? "error" : undefined}
          disabled={daemonSwitchLocked()}
          onSelect={(id) => {
            void switchKataDaemon(id);
          }}
        />
      {:else if activeDaemonStatusLabel()}
        <span class="daemon-fallback-status" role="alert">{activeDaemonStatusLabel()}</span>
      {/if}
    </div>
    <div class="kata-header-actions">
      <IconButton
        ariaLabel={splitOrientation === "vertical" ? "Switch to side-by-side layout" : "Switch to stacked layout"}
        title={splitOrientation === "vertical"
          ? "Side-by-side (list left, detail right)"
          : "Stacked (list top, detail bottom)"}
        onclick={toggleSplitOrientation}
      >
        {#if splitOrientation === "vertical"}
          <LayoutPanelLeftIcon size={15} strokeWidth={1.8} aria-hidden="true" />
        {:else}
          <LayoutPanelTopIcon size={15} strokeWidth={1.8} aria-hidden="true" />
        {/if}
      </IconButton>
      <button
        type="button"
        class="accent-button header-action"
        disabled={workspaceActionsBlocked}
        onclick={() => { if (!workspaceActionsBlocked) captureOpen = true; }}
      >
        <PlusIcon size={13} strokeWidth={1.9} aria-hidden="true" />
        <span>New task</span>
      </button>
    </div>
  </header>

  {#if cursorCatchupError}
    <p class="kata-request-error" role="alert">
      {cursorCatchupError}
      {#if cursorCatchupRetry}
        <button
          type="button"
          disabled={cursorCatchupRetrying}
          aria-busy={cursorCatchupRetrying}
          onclick={() => { void cursorCatchupRetry?.(); }}
        >{cursorCatchupRetrying ? "Retrying…" : "Retry"}</button>
      {/if}
    </p>
  {/if}
  {#if restoreError}
    <p class="kata-request-error" role="alert">
      {restoreError}
      {#if restoreRetry}
        <button
          type="button"
          onclick={() => {
            const retry = restoreRetry;
            if (retry) void retry();
          }}
        >Retry</button>
      {/if}
    </p>
  {/if}
  {#if ancestorRevealError}
    <p class="kata-request-error" role="alert">
      {ancestorRevealError}
      {#if ancestorRevealRetry}
        <button
          type="button"
          onclick={() => {
            const retry = ancestorRevealRetry;
            if (retry) void retry();
          }}
        >Retry</button>
      {/if}
    </p>
  {/if}
  {#if terminalDaemonFailure && terminalRecovery}
    <p class="kata-request-error" role="alert">
      The retained Kata workspace is read-only until its daemon reconnects.
      <button
        type="button"
        disabled={terminalRecovering}
        aria-busy={terminalRecovering}
        onclick={() => { void terminalRecovery?.(); }}
      >{terminalRecovering ? "Retrying…" : "Retry"}</button>
    </p>
  {/if}

  <div class="kata-layout" inert={workspaceOwnershipPending || workspaceReadOnly} aria-busy={restoreRetrying || terminalRecovering}>
    <KataSidebar
      areas={visibleAreas}
      projects={visibleProjects}
      currentView={store.currentView}
      searchFilters={store.searchFilters}
      onOpenView={(name) => {
        void openRoutedSystemView(name, true);
      }}
      onOpenProject={(projectUID) => {
        scheduleProjectScope(projectUID);
      }}
      onCreateProject={createKataProject}
    />

    <main class="kata-main" aria-label="Kata tasks">
      {#if viewError}
        <p class="kata-view-error" role="alert">{viewError}</p>
      {/if}
      <KataResizableSash
        orientation={splitOrientation}
        primarySize={activeSplitSize}
        minPrimary={splitOrientation === "vertical" ? 220 : 320}
        minSecondary={splitOrientation === "vertical" ? 220 : 360}
        ariaLabel="Resize Kata panes"
        onResize={handleSashResize}
        primary={listPane}
        secondary={detailPane}
      />
    </main>
  </div>
</section>

{#snippet listPane()}
  <div class="list-column kata-list">
    {#if listMode === "reachableGraph" && graphSourceIssue}
      <KataReachableGraph
        api={store.api}
        sourceIssue={graphSourceIssue}
        selectedUID={store.pendingSelectionUID ?? store.selectedIssue?.issue.uid ?? null}
        layoutDirection={graphLayoutDirection}
        onBack={closeReachableGraph}
        onSelectIssue={(uid) => {
          selectReachableGraphIssue(uid);
        }}
        onGraphTasksLoaded={rememberGraphTasks}
      />
    {:else}
      <KataSearchPanel
        filters={store.searchFilters}
        projects={visibleProjects}
        duplicateCandidates={store.duplicateCandidates}
        onChange={updateSearchFilters}
      />
      {#key `${activeKataDaemonId ?? ""}:${listResetGeneration}`}
        <KataIssueList
          currentView={store.currentView}
          scopeLabel={listTitle()}
          scopedProjectName={selectedProjectName()}
          selectedIssueUID={store.pendingSelectionUID ?? store.selectedIssue?.issue.uid ?? null}
          loading={viewLoading}
          statusFilter={listStatusFilter}
          resetGeneration={listResetGeneration}
          navigationGeneration={navigationEpoch}
          {revealRequest}
          api={store.api}
          onSelect={(issue) => {
            void selectIssue(issue.uid);
          }}
          onOpenGraph={openReachableGraph}
          onRememberTasks={(issues) => {
            store.rememberTasks(issues);
          }}
        />
      {/key}
    {/if}
  </div>
{/snippet}

{#snippet detailPane()}
  {#if store.pendingSelectionUID && store.selectedIssue?.issue.uid !== store.pendingSelectionUID}
    <section class="kata-detail-empty" aria-label="Task detail">
      <p class="empty detail-empty">Loading task</p>
    </section>
  {:else if store.selectedIssue}
    <KataIssueDetail
      issue={store.selectedIssue}
      events={store.selectedEvents}
      currentView={store.currentView}
      api={store.api}
      activeDaemonId={activeKataDaemonId}
      projects={store.projects}
      ownerOptions={ownerOptions()}
      messageLinks={selectedMessageLinks()}
      unlinkBusyIds={unlinkBusyIds}
      selectedRecurrences={store.selectedRecurrences}
      {checklistRevealed}
      movePending={pendingMoveIssueUIDs.has(store.selectedIssue.issue.uid)}
      onMoveIssue={moveSelectedIssue}
      onPatchMetadata={patchSelectedMetadata}
      onAddComment={addSelectedComment}
      onEditIssue={editSelectedIssue}
      onAssignOwner={assignSelectedOwner}
      onUnassignOwner={unassignSelectedOwner}
      onSetPriority={setSelectedPriority}
      onAddLabel={addSelectedLabel}
      onRemoveLabel={removeSelectedLabel}
      onOpenMessage={onOpenMessage
        ? (link) => {
          onOpenMessage?.(link.message_id);
        }
        : undefined}
      onUnlinkMessage={unlinkMessageLink}
      onRevealChecklist={revealChecklist}
      onCreateRecurrence={() => recurrenceDialogs?.openCreateRecurrence()}
      onEditRecurrence={(recurrence) => recurrenceDialogs?.openEditRecurrence(recurrence)}
      onDeleteRecurrence={(recurrence) => recurrenceDialogs?.openDeleteRecurrence(recurrence)}
      onCloseIssue={closeSelectedIssue}
      onReopenIssue={reopenSelectedIssue}
      onDeleteIssue={deleteSelectedIssue}
      onSelectIssue={(uid) => {
        void selectIssue(uid);
      }}
      onOpenGraph={openReachableGraph}
      workspaceAction={selectedWorkspaceAction()}
    />
  {:else}
    <section class="kata-detail-empty" aria-label="Task detail">
      <p class="empty detail-empty">Select a task</p>
    </section>
  {/if}
{/snippet}

<QuickCapture
  open={captureOpen}
  disabled={workspaceActionsBlocked}
  onClose={() => { captureOpen = false; }}
  onSubmit={submitQuickCapture}
/>

<KataRecurrenceDialogs
  bind:this={recurrenceDialogs}
  selectedIssue={store.selectedIssue}
  {actor}
  onCreate={createRecurrence}
  onPatch={patchRecurrence}
  onDelete={deleteRecurrence}
/>

<style>
  .kata-feature {
    min-height: 100%;
    background: var(--bg-app);
    color: var(--text-primary);
    display: flex;
    flex-direction: column;
  }

  .kata-view-error {
    flex: 0 0 auto;
    margin: var(--space-4) var(--space-5) 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .kata-header {
    min-height: 56px;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border-default);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
  }

  .kata-header-title {
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--space-4);
    flex: 1 1 auto;
  }

  .kata-header h1 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 650;
    line-height: 1.2;
  }

  .daemon-fallback-status {
    min-width: 0;
    max-width: min(420px, 48vw);
    color: var(--accent-red);
    font-size: var(--font-size-sm);
    line-height: 1.3;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .kata-header-actions {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    flex: 0 0 auto;
  }

  .header-action {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    white-space: nowrap;
  }

  .kata-layout {
    min-height: 0;
    flex: 1;
    display: grid;
    grid-template-columns: 240px minmax(0, 1fr);
  }

  .kata-main {
    min-width: 0;
    min-height: 0;
    display: flex;
    overflow: hidden;
  }

  .list-column {
    min-width: 0;
    min-height: 0;
    display: flex;
    flex: 1 1 auto;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .kata-detail-empty {
    flex: 1 1 auto;
    min-width: 0;
    min-height: 0;
    overflow: auto;
    background: var(--bg-primary);
    padding: 18px 22px;
  }

  @media (max-width: 900px) {
    .kata-layout {
      grid-template-columns: 1fr;
      grid-template-rows: auto minmax(0, 1fr);
    }
  }
</style>
