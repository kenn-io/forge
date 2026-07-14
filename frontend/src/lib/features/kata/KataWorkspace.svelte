<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { IconButton } from "@kenn-io/kit-ui";
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
  import { createKataWorkspaceStore } from "../../stores/kata-workspace.svelte.js";
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

  interface KataRecurrenceDialogController {
    openCreateRecurrence: () => void;
    openEditRecurrence: (recurrence: KataRecurrence) => void;
    openDeleteRecurrence: (recurrence: KataRecurrence) => void;
    closeAll: () => void;
  }

  type SplitOrientation = "vertical" | "horizontal";
  type FailureSurface = "request" | "daemon" | "none";
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
  let requestError = $state<string | null>(null);
  let lastTaskError: string | null = null;
  let unlinkBusyIds = $state<ReadonlySet<number>>(new Set());
  let unlinkError = $state<string | null>(null);
  let daemonInfos = $state.raw<KataDaemonInfo[]>([]);
  let switchingDaemon = $state(false);
  let terminalDaemonFailure = $state(false);
  let captureOpen = $state(false);
  let listResetGeneration = $state(0);
  let checklistRevealed = $state(false);
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
  let recurrenceDialogs = $state<KataRecurrenceDialogController | null>(null);
  let workspaceActionBusy = $state(false);
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
  const layoutStorageKey = "middleman:kata:task-layout/v1";
  const defaultSplitSizes: Record<SplitOrientation, number> = {
    vertical: 420,
    horizontal: 520,
  };
  let splitOrientation = $state<SplitOrientation>("vertical");
  let splitSizes = $state<Record<SplitOrientation, number>>({ ...defaultSplitSizes });
  const activeSplitSize = $derived(splitSizes[splitOrientation]);
  const graphLayoutDirection = $derived(graphLayoutDirectionForSplit(splitOrientation));
  const activeKataDaemonId = $derived(
    store.daemonId ??
      getActiveKataDaemon() ??
      getDefaultKataDaemon() ??
      daemonInfos.find((daemon) => daemon.default)?.id ??
      daemonInfos[0]?.id,
  );
  const routedDaemonError = $derived(
    requestedDaemonId && daemonInfos.length > 0 && !daemonInfos.some((daemon) => daemon.id === requestedDaemonId)
      ? `Kata daemon ${requestedDaemonId} is not configured.`
      : null,
  );
  const listStatusFilter = $derived<KataTaskSearchFilters["status"]>(
    store.currentView.name === "logbook" ? "all" : store.searchFilters.status,
  );
  const eventStream = createKataEventStreamController({
    getDaemonId: () => store.daemonId ?? activeKataDaemonId,
    getLastEventID: () => store.eventCursor,
    onOpen: () => {
      store.connection = { status: "online" };
    },
    onMessage: async (message) => {
      await trackViewWork(() => store.applyEventStreamMessage(message));
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

  function clearTaskErrors(): void {
    error = null;
    requestError = null;
    lastTaskError = null;
  }

  function surfaceTaskError(message: string, surface: FailureSurface): void {
    lastTaskError = message;
    if (surface === "request") {
      requestError = message;
    } else if (surface === "daemon") {
      error = message;
    }
  }

  async function runViewTask(
    task: () => Promise<void | boolean>,
    failureSurface: FailureSurface = "request",
    shouldSurfaceFailure: () => boolean = () => true,
  ): Promise<boolean> {
    const loadingGeneration = beginViewLoading();
    clearTaskErrors();
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
    failureSurface: FailureSurface = "request",
  ): Promise<void> {
    const loadingGeneration = beginViewLoading();
    clearTaskErrors();
    try {
      await task();
    } catch (err) {
      surfaceTaskError(kataRequestErrorMessage(err), failureSurface);
      throw err;
    } finally {
      endViewLoading(loadingGeneration);
    }
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
        const bootstrapRoute = currentRouteSnapshot();
        const bootstrapViewName = bootstrapRoute.view ?? "all";
        const bootstrapIssueUID = bootstrapRoute.issue;
        const hasDaemonRoute = requestedDaemonId !== null;
        routedDaemonId = daemons.some((daemon) => daemon.id === requestedDaemonId) ? requestedDaemonId : null;
        if (routedDaemonId) {
          previousExplicitDaemon = getActiveKataDaemon();
          store.bindDaemonForBootstrap(routedDaemonId);
        }
        await store.bootstrap(
          bootstrapViewName,
          hasDaemonRoute ? null : bootstrapIssueUID,
          { selectFirst: !hasDaemonRoute && bootstrapIssueUID !== null },
        );
        if (bootstrapRoute.scope) {
          await loadRouteViewScope(bootstrapRoute.view, bootstrapRoute.scope);
        }
        await store.syncEventCursor();
        if (routedDaemonId) {
          setActiveKataDaemon(routedDaemonId);
        }
        const routedIssueUID = bootstrapIssueUID && store.selectedIssue?.issue.uid !== bootstrapIssueUID
          ? bootstrapIssueUID
          : null;
        if (routedIssueUID) {
          const selected = await runViewTask(() => store.selectIssue(routedIssueUID));
          if (!selected) {
            failedRouteSignature = fullRouteSignature();
          }
        }
        selectionFromRoute = bootstrapIssueUID !== null && store.selectedIssue?.issue.uid === bootstrapIssueUID;
        if (routedDaemonId && store.daemonId === routedDaemonId) {
          onRouteStateChange?.({
            view: bootstrapRoute.view,
            scope: bootstrapRoute.scope,
            issue: bootstrapRoute.issue,
            daemon: null,
          }, { replace: true });
        }
        if (!cancelled) {
          startEventStream();
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
        if (!cancelled) {
          loading = false;
        }
      }
    })();

    return () => {
      cancelled = true;
      stopEventStream();
    };
  });

  function scopeUIDFromFilters(filters: KataTaskSearchFilters): string | null {
    return filters.scope.kind === "project" ? filters.scope.project_uid : null;
  }

  function beginNavigation(): number {
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

  function routeSignature(route: KataRouteSnapshot): string {
    return `${route.view ?? ""}\u0000${route.scope ?? ""}\u0000${route.issue ?? ""}`;
  }

  function fullRouteSignature(): string {
    return `${routeSignature(currentRouteSnapshot())} ${requestedDaemonId ?? ""}`;
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
    return loading || switchingDaemon || terminalDaemonFailure || routeEmissionWork > 0;
  }

  type RouteMismatch = "daemon" | "viewScope" | "select" | "clear" | null;

  // A daemon switch is transactional and refuses while other work is in
  // flight. The reconciler must check the same gates before attempting
  // it: an attempt that would refuse must be a no-op evaluation, not a
  // reconcile pass, or the effect re-fires itself in a busy loop.
  function daemonSwitchGated(): boolean {
    return viewWorkCount > 0 || store.hasPendingMutations || workspaceActionBusy;
  }

  // Route list loads are not abortable, so the reconciler starts them
  // without awaiting: a stalled fetch must not block converging to a
  // newer route (the store's request guards drop the late result). This
  // records which route's list load is in flight so re-evaluations do
  // not start a duplicate.
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
    void runViewTask(() => loadRouteViewScope(viewName, scopeUID)).then((ok) => {
      if (viewScopeLoadSignature === signature) {
        viewScopeLoadSignature = null;
      }
      if (!ok && fullRouteSignature() === signature) {
        failedRouteSignature = signature;
      }
    });
  }

  function routeMismatch(): RouteMismatch {
    if (requestedDaemonId !== null && requestedDaemonId !== store.daemonId) {
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
          clearTaskErrors();
          failedRouteSignature = null;
        }
        if (failedRouteSignature === signature) return;
        const mismatch = routeMismatch();
        if (mismatch === null) {
          if ((selectedIssueUID ?? null) !== null) selectionFromRoute = true;
          return;
        }
        if (mismatch === "daemon") {
          if (daemonSwitchGated()) return;
          await switchKataDaemon(requestedDaemonId!);
          if (requestedDaemonId !== null && requestedDaemonId !== store.daemonId) {
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
          beginNavigation();
          resetDetailDrafts();
          const uid = selectedIssueUID!;
          const ok = await runViewTask(() => store.selectIssue(uid));
          if (!ok) {
            // The routed task cannot be shown; keeping the previous
            // detail under the new URL would lie about what is open.
            if (fullRouteSignature() === signature) {
              failedRouteSignature = signature;
              store.clearSelection();
              selectionFromRoute = false;
              return;
            }
          } else {
            selectionFromRoute = true;
          }
          continue;
        }
        // mismatch === "clear"
        beginNavigation();
        clearTaskErrors();
        resetDetailDrafts();
        store.clearSelection();
        selectionFromRoute = false;
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
      // The daemon-switch transaction owns its loads; queued route
      // changes converge through the reconciler once it settles.
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
    void store.currentView.name;
    void store.searchFilters;
    void store.pendingSelectionUID;
    void store.selectedIssue;
    void daemonInfos;
    void viewWorkCount;
    void store.hasPendingMutations;
    void workspaceActionBusy;
    if (reconciling || reconcilerBusy()) return;
    if (failedRouteSignature !== null && failedRouteSignature !== fullRouteSignature()) {
      // The route moved off a failed target; drop the stale failure
      // surface even when the new route is already converged.
      clearTaskErrors();
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
    if (mismatch === "daemon" && daemonSwitchGated()) return;
    // A project route load applies its scope before its optional view load
    // finishes. That intermediate state can leave only the issue selection
    // mismatched, but selecting now would be aborted when the remaining view
    // load clears selection. Wait for the complete route load to settle.
    if (viewScopeLoadSignature !== null && mismatch !== "viewScope") return;
    if (mismatch === "viewScope" && viewScopeLoadSignature === fullRouteSignature()) return;
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
    eventStream.stop(resetReconnect);
  }

  function startEventStream(reconnecting = false): void {
    eventStream.start(reconnecting);
  }

  async function updateSearchFilters(filters: Partial<KataTaskSearchFilters>): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      const nextStatus = filters.status ?? store.searchFilters.status;
      closeReachableGraph();
      resetDetailDrafts();
      // A pending detail load is abandoned by the filter reload, so drop
      // it before awaiting.
      store.invalidatePendingLoads();
      if (!selectedIssueMatchesStatusFilter(nextStatus)) {
        store.clearSelection();
      }
      await runViewTask(() => store.updateSearchFilters(filters));
      if (!isCurrentNavigation(generation)) return;
      const nextScopeUID = scopeUIDFromFilters(store.searchFilters);
      if (nextScopeUID !== (routeScopeUID ?? null)) {
        const nextViewName = nextScopeUID ? null : store.currentView.name === "all" ? null : store.currentView.name;
        onRouteStateChange?.({
          view: nextViewName,
          scope: nextScopeUID,
          issue: store.selectedIssue?.issue.uid ?? null,
        });
        return;
      }
      emitRouteSelectionSync();
    });
  }

  async function openRoutedSystemView(viewName: KataTaskViewName): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      closeReachableGraph();
      resetDetailDrafts();
      store.resetSearchFilters();
      // Clear (and thereby abort) the abandoned selection before awaiting
      // the new view: while that fetch is in flight, a still-running
      // detail load could fail and surface a stale error for a selection
      // this navigation has already discarded.
      store.clearSelection();
      selectionFromRoute = false;
      await runViewTask(() => store.openView(viewName, { selectFirst: false }));
      if (!isCurrentNavigation(generation)) return;
      onRouteStateChange?.({
        view: viewName,
        scope: null,
        issue: null,
      });
    });
  }

  async function openRoutedProjectScope(projectUID: string): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      closeReachableGraph();
      resetDetailDrafts();
      // Scope changes keep a completed selection but abandon an in-flight
      // one (the scoped reload re-selects from selectedIssue, which a
      // pending load hasn't populated). Invalidate up front so the doomed
      // detail request can't fail into a stale workspace error while the
      // scoped list is still loading.
      store.invalidatePendingLoads();
      const ok = await runViewTask(() =>
        store.updateSearchFilters({ scope: { kind: "project", project_uid: projectUID } }),
      );
      if (!ok || !isCurrentNavigation(generation)) return;
      onRouteStateChange?.({
        view: null,
        scope: projectUID,
        issue: store.selectedIssue?.issue.uid ?? null,
      });
    });
  }

  function scheduleProjectScope(projectUID: string): void {
    void openRoutedProjectScope(projectUID);
  }

  async function createKataProject(name: string): Promise<KataProjectSummary> {
    let created: KataProjectSummary | undefined;
    await runViewTaskOrThrow(async () => {
      created = await store.createProject(name);
    });
    return created!;
  }

  async function submitQuickCapture(title: string): Promise<void> {
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
    });
  }

  async function switchKataDaemon(id: string): Promise<void> {
    if (loading || switchingDaemon || viewWorkCount > 0 || store.hasPendingMutations || workspaceActionBusy) return;
    const previousExplicitDaemon = getActiveKataDaemon();
    const previousDaemon = store.daemonId ?? activeKataDaemonId;
    if (id === store.daemonId) return;

    const generation = beginNavigation();
    const previousView = store.currentView.name;
    const previousFilters = store.searchFilters;
    const previousIssueUID = store.selectedIssue?.issue.uid ?? null;
    const routeAtSwitchStart = routeSignature(currentRouteSnapshot());
    switchingDaemon = true;
    terminalDaemonFailure = false;
    closeReachableGraph();
    resetDetailDrafts();
    resetIssueExpansion();
    // The switch abandons any in-flight detail load (only a completed
    // selection is captured for restore above), so drop it before the
    // daemon reload: its failure mid-switch would otherwise surface a
    // stale error from the previous daemon.
    store.invalidatePendingLoads();
    store.clearDaemonState(previousView);
    setActiveKataDaemon(id);
    stopEventStream();
    try {
      const ok = await runViewTask(async () => {
        await store.bootstrap(previousView, null, { selectFirst: requestedDaemonId !== id });
        store.resetSearchFilters();
        await store.syncEventCursor();
      }, "daemon");
      if (!ok) {
        store.clearDaemonState(previousView);
        setActiveKataDaemon(previousDaemon);
        const restored = await runViewTask(async () => {
          await store.bootstrap(previousView, previousIssueUID);
          await store.updateSearchFilters(previousFilters);
          if (store.currentView.name !== previousView) {
            await store.openView(previousView);
          }
          if (previousIssueUID && store.selectedIssue?.issue.uid !== previousIssueUID) {
            await store.selectIssue(previousIssueUID);
          }
          await store.syncEventCursor();
        }, "daemon");
        if (restored) {
          setActiveKataDaemon(previousExplicitDaemon);
          if (requestedDaemonId === id) {
            onRouteStateChange?.({
              view: previousView,
              scope: scopeUIDFromFilters(previousFilters),
              issue: previousIssueUID,
              daemon: null,
            }, { replace: true });
          }
          startEventStream();
        } else {
          store.clearDaemonState(previousView);
          terminalDaemonFailure = true;
        }
        return;
      }

      if (!isCurrentNavigation(generation)) {
        startEventStream();
        return;
      }
      const nextUID = store.selectedIssue?.issue.uid ?? null;
      if (requestedDaemonId === id) {
        // Consume the transient daemon param; the current route (which
        // may have changed while the switch was in flight) stays put and
        // the reconciler converges the fresh workspace to it.
        onRouteStateChange?.({
          view: routeViewName,
          scope: routeScopeUID,
          issue: selectedIssueUID,
          daemon: null,
        }, { replace: true });
      } else if (routeSignature(currentRouteSnapshot()) === routeAtSwitchStart) {
        onSelectedIssueChange?.(nextUID);
      }
      startEventStream();
    } finally {
      switchingDaemon = false;
    }
  }

  function resetDetailDrafts(): void {
    checklistRevealed = false;
    recurrenceDialogs?.closeAll();
  }

  async function selectIssue(uid: string, notify = true): Promise<void> {
    await withRouteEmission(async () => {
      const generation = beginNavigation();
      resetDetailDrafts();
      const ok = await runViewTask(() => store.selectIssue(uid));
      if (!ok || !isCurrentNavigation(generation)) return;
      if (notify) onSelectedIssueChange?.(uid);
    });
  }

  function selectReachableGraphIssue(uid: string): void {
    if (onSelectedIssueChange && selectedIssueUID !== uid) {
      // Route the node first; the echo lands synchronously and the
      // reconciler applies the selection (and owns its failure surface).
      onSelectedIssueChange(uid);
      return;
    }
    void selectIssue(uid, false);
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
    if (!selected || pendingMoveIssueUIDs.has(selected.uid)) return false;
    const sourceIssueUID = selected.uid;
    const generation = navigationGeneration;
    pendingMoveIssueUIDs = new Set(pendingMoveIssueUIDs).add(sourceIssueUID);
    try {
      return await runViewTask(
        () => store.moveIssue(sourceIssueUID, actor, toProjectUID),
        "request",
        () => isCurrentNavigation(generation),
      );
    } finally {
      const nextPendingMoves = new Set(pendingMoveIssueUIDs);
      nextPendingMoves.delete(sourceIssueUID);
      pendingMoveIssueUIDs = nextPendingMoves;
    }
  }

  async function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Promise<boolean> {
    return runViewTask(() => store.patchMetadata(uid, actor, patch));
  }

  async function addSelectedComment(uid: string, body: string): Promise<boolean> {
    return runViewTask(() => store.addComment(uid, actor, body));
  }

  async function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Promise<boolean> {
    return runViewTask(() => store.editIssue(uid, actor, patch));
  }

  async function assignSelectedOwner(uid: string, owner: string): Promise<boolean> {
    return runViewTask(() => store.assignOwner(uid, actor, owner));
  }

  async function unassignSelectedOwner(uid: string): Promise<boolean> {
    return runViewTask(() => store.unassignOwner(uid, actor));
  }

  async function setSelectedPriority(uid: string, priority: number | null): Promise<boolean> {
    return runViewTask(() => store.setPriority(uid, actor, priority));
  }

  async function addSelectedLabel(uid: string, label: string): Promise<boolean> {
    return runViewTask(() => store.addLabel(uid, actor, label));
  }

  async function removeSelectedLabel(uid: string, label: string): Promise<void> {
    await runViewTask(() => store.removeLabel(uid, actor, label));
  }

  function selectedMessageLinks(): MessageLinkRef[] {
    return store.selectedIssue ? readMessageLinks(store.selectedIssue.issue.metadata) : [];
  }

  function openWorkspace(id: string): void {
    navigate(`/terminal/${encodeURIComponent(id)}`);
  }

  async function createWorkspaceForSelectedIssue(): Promise<void> {
    const selected = store.selectedIssue?.issue;
    if (!selected || workspaceActionBusy) return;
    workspaceActionBusy = true;
    requestError = null;
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
      requestError = kataRequestErrorMessage(err);
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
      disabled: workspaceActionBusy,
      onClick: createWorkspaceForSelectedIssue,
    };
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  async function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<void> {
    await runViewTaskOrThrow(async () => {
      await store.createRecurrence(projectID, input);
    }, "none");
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    await runViewTaskOrThrow(async () => {
      await store.patchRecurrence(id, input, etag);
    }, "none");
  }

  async function deleteRecurrence(recurrence: KataRecurrence): Promise<boolean> {
    return runViewTask(() => store.deleteRecurrence(recurrence.id, actor));
  }

  async function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Promise<boolean> {
    const selected = store.selectedIssue;
    if (!selected) return false;
    return runViewTask(() =>
      store.closeIssue(selected.issue.uid, actor, {
        reason,
        message,
      }),
    );
  }

  async function reopenSelectedIssue(): Promise<void> {
    const selected = store.selectedIssue;
    if (!selected) return;
    await runViewTask(() => store.reopenIssue(selected.issue.uid, actor));
  }

  async function deleteSelectedIssue(): Promise<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from issue detail.");
  }

  async function unlinkMessageLink(link: MessageLinkRef): Promise<void> {
    if (unlinkBusyIds.size > 0) return;
    const selected = store.selectedIssue;
    if (!selected) return;
    const uid = selected.issue.uid;
    const links = selectedMessageLinks();
    const patch = computeRemoveMessageLinkPatch(links, link.message_id);
    if (patch === null) return;
    const metadataPatch: Record<string, unknown> = { mail_links: patch.mail_links };

    unlinkBusyIds = new Set(links.map((item) => item.message_id));
    unlinkError = null;
    try {
      const ok = await runViewTask(() => store.patchMetadata(uid, actor, metadataPatch), "none");
      if (!ok) {
        unlinkError = lastTaskError || "Could not unlink message.";
      }
    } finally {
      unlinkBusyIds = new Set();
    }
  }
</script>

<section class="kata-feature" aria-labelledby="kata-title" inert={switchingDaemon} aria-busy={switchingDaemon}>
  <header class="kata-header">
    <div class="kata-header-title">
      <h1 id="kata-title">Kata</h1>
      {#if daemonInfos.length > 0}
        <KataDaemonSwitcher
          daemons={daemonInfos}
          activeId={activeKataDaemonId}
          activeStatusLabel={activeDaemonStatusLabel()}
          activeStatusTone={activeDaemonStatusLabel() ? "error" : undefined}
          disabled={loading || switchingDaemon || viewWorkCount > 0 || store.hasPendingMutations || workspaceActionBusy}
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
      <button type="button" class="accent-button header-action" onclick={() => { captureOpen = true; }}>
        <PlusIcon size={13} strokeWidth={1.9} aria-hidden="true" />
        <span>New task</span>
      </button>
    </div>
  </header>

  {#if requestError}
    <p class="kata-request-error" role="alert">{requestError}</p>
  {/if}

  <div class="kata-layout">
    <KataSidebar
      areas={store.areas}
      projects={store.projects}
      currentView={store.currentView}
      searchFilters={store.searchFilters}
      onOpenView={(name) => {
        void openRoutedSystemView(name);
      }}
      onOpenProject={(projectUID) => {
        scheduleProjectScope(projectUID);
      }}
      onCreateProject={createKataProject}
    />

    <main class="kata-main" aria-label="Kata tasks">
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
        projects={store.projects}
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
      {unlinkError}
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

  .kata-request-error {
    margin: 10px 20px 0;
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--accent-red) 42%, var(--border-default));
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-red) 9%, var(--bg-primary));
    color: var(--accent-red);
    font-size: var(--font-size-sm);
    line-height: 1.35;
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
