<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { IconButton, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { showFlash } from "@middleman/ui/stores/flash";
  import LayoutPanelLeftIcon from "@lucide/svelte/icons/layout-panel-left";
  import LayoutPanelTopIcon from "@lucide/svelte/icons/layout-panel-top";
  import PlusIcon from "@lucide/svelte/icons/plus";

  import { fetchKataDaemons, type KataDaemonInfo } from "../../api/kata/daemons.js";
  import { createKataTaskAPI } from "../../api/kata/taskClient.js";
  import {
    searchKataTaskReferences,
    type KataSnapshotIntent,
    type KataTaskReferenceSearch,
  } from "../../api/kata/snapshot.js";
  import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
  import { createKataWorkspaceForTask, kataWorkspaceIdentityFromIssue } from "../../api/kata/workspaces.js";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataPinnedDaemonOptions,
    KataProjectSummary,
    KataReachableGraphResponse,
    KataRecurrence,
    KataTaskAPI,
    KataTaskDetail,
    KataTaskEditPatch,
    KataTaskEvent,
    KataTaskMutationResponse,
    KataTaskMutationTarget,
    KataTaskSearchFilters,
    KataTaskSummary,
    KataTaskViewName,
  } from "../../api/kata/taskTypes.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import KataIssueList from "../../components/kata/KataIssueList.svelte";
  import KataResizableSash from "../../components/kata/KataResizableSash.svelte";
  import KataSidebar from "../../components/kata/KataSidebar.svelte";
  import QuickCapture from "../../components/shared/QuickCapture.svelte";
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
  import { loadKataWorkspaceState, saveKataWorkspaceState } from "./kataWorkspacePersistence.js";
  import KataDaemonSwitcher from "./KataDaemonSwitcher.svelte";
  import KataReachableGraph from "./KataReachableGraph.svelte";
  import KataRecurrenceDialogs from "./KataRecurrenceDialogs.svelte";
  import KataSearchPanel from "./KataSearchPanel.svelte";
  import {
    applyKataLinkStatusScope,
    createKataLinkFilters,
    type KataLinkFilters,
  } from "./kataLinkFilters.js";
  import {
    defaultKataTaskSearchFilters,
    deriveKataAreas,
    kataWorkspaceAuthorityRequest,
    projectKataWorkspaceView,
  } from "./kataWorkspaceAuthority.js";
  import { createKataWorkspaceAuthorityController } from "./kataWorkspaceAuthorityController.svelte.js";
  import type { KataGraphLayoutDirection } from "./kataReachableGraph.js";

  interface Props {
    api?: KataTaskAPI | undefined;
    searchReferences?: KataTaskReferenceSearch | undefined;
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

  interface KataConnectionState {
    status: "offline" | "online" | "error";
    message?: string | undefined;
  }

  interface PendingCreatedProjectScope {
    daemonID: string;
    name: string;
    existingUIDs: ReadonlySet<string>;
  }

  type SplitOrientation = "vertical" | "horizontal";
  type FailureSurface = "flash" | "daemon" | "view" | "none";
  type ListMode = "tasks" | "reachableGraph";

  function graphLayoutDirectionForSplit(orientation: SplitOrientation): KataGraphLayoutDirection {
    return orientation === "horizontal" ? "LR" : "TB";
  }

  let {
    api = undefined,
    searchReferences = searchKataTaskReferences,
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
  let lastTaskError: string | null = null;
  let unlinkBusyIds = $state<ReadonlySet<number>>(new Set());
  let daemonInfos = $state.raw<KataDaemonInfo[]>([]);
  let switchingDaemon = $state(false);
  let captureOpen = $state(false);
  let listResetGeneration = $state(0);
  let checklistRevealed = $state(false);
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
  let recurrenceDialogs = $state<KataRecurrenceDialogController | null>(null);
  let selectedRecurrences = $state.raw<KataRecurrence[]>([]);
  let recurrenceLoadGeneration = 0;
  let pendingMutationCount = $state(0);
  let workspaceActionBusy = $state(false);
  let workspaceOwnershipPending = $state(true);
  let listMode = $state<ListMode>("tasks");
  let graphSourceIssue = $state.raw<KataTaskSummary | null>(null);
  let linkFilters = $state<KataLinkFilters>(createKataLinkFilters("open"));
  let lastLinkFilterScope: {
    daemonID: string | undefined;
    status: KataTaskSearchFilters["status"];
  } = { daemonID: undefined, status: "open" };
  let graphAuthoritySourceUID = $state<string | null>(null);
  let currentViewName = $state<KataTaskViewName>("all");
  let searchFilters = $state.raw<KataTaskSearchFilters>(defaultKataTaskSearchFilters());
  let routeReconcilerReady = $state(false);
  let appliedRouteSignature = "";
  let appliedViewScopeSignature = "";
  let appliedAuthorityRouteSignature = "";
  let appliedGraphSourceUID = "";
  let routeReconcileGeneration = 0;
  const taskAPI = untrack(() => api) ?? createKataTaskAPI();
  let connection = $state.raw<KataConnectionState>({ status: "offline" });
  let bootstrapDaemonId = $state<string | undefined>(undefined);
  let lastAcceptedSnapshot: KataWorkspaceSnapshotProjection | null = null;
  let pendingCreatedProjectScope: PendingCreatedProjectScope | null = null;
  let projectNavigationSelectionUID: string | null = null;
  let replaceNextSelectionScopeUID: string | null = null;
  const actor = "middleman";
  let navigationGeneration = 0;
  // Reactive shadow of navigationGeneration so the issue list can drop
  // a pending keyboard selection the moment any navigation starts —
  // the list only remounts after the new view's data arrives, which is
  // too late for a selection released mid-transition.
  let navigationEpoch = $state(0);
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
  const routedDaemonError = $derived(
    requestedDaemonId && daemonInfos.length > 0 && !daemonInfos.some((daemon) => daemon.id === requestedDaemonId)
      ? `Kata daemon ${requestedDaemonId} is not configured.`
      : null,
  );
  const workspaceActionsBlocked = $derived(
    workspaceOwnershipPending || switchingDaemon || pendingMutationCount > 0 || workspaceActionBusy,
  );
  const listStatusFilter = $derived<KataTaskSearchFilters["status"]>(
    currentViewName === "logbook" && searchFilters.status === "open"
      ? "all"
      : searchFilters.status,
  );
  $effect(() => {
    const daemonID = activeKataDaemonId;
    const status = listStatusFilter;
    if (daemonID !== lastLinkFilterScope.daemonID) {
      linkFilters = createKataLinkFilters(status);
    } else if (status !== lastLinkFilterScope.status) {
      linkFilters = applyKataLinkStatusScope(untrack(() => linkFilters), status);
    }
    lastLinkFilterScope = { daemonID, status };
  });
  const authorityController = createKataWorkspaceAuthorityController({
    resetIssueExpansion,
    onSnapshotAccepted: (snapshot) => {
      const previousSnapshot = lastAcceptedSnapshot;
      lastAcceptedSnapshot = snapshot;
      const previousSelectedIssueUID = previousSnapshot?.selected_issue_uid;
      if (
        previousSelectedIssueUID &&
        previousSelectedIssueUID !== projectNavigationSelectionUID &&
        previousSelectedIssueUID === selectedIssueUID?.trim() &&
        previousSnapshot.member_issue_uid_set.has(previousSelectedIssueUID) &&
        !snapshot.member_issue_uid_set.has(previousSelectedIssueUID) &&
        snapshot.selected_issue_uid !== previousSelectedIssueUID
      ) {
        onRouteStateChange?.({ issue: null }, { replace: true });
      }
      const pendingProject = pendingCreatedProjectScope;
      if (pendingProject) {
        if (snapshot.daemon_id !== pendingProject.daemonID) {
          pendingCreatedProjectScope = null;
        } else {
          const createdProject = snapshot.projects.find(
            (project) => !pendingProject.existingUIDs.has(project.uid) && project.name.trim() === pendingProject.name,
          );
          if (createdProject) {
            pendingCreatedProjectScope = null;
            onRouteStateChange?.({ view: null, scope: createdProject.uid, issue: null });
          }
        }
      }
      const selectedRowIsFocused =
        typeof document !== "undefined" &&
        document.activeElement instanceof HTMLElement &&
        document.activeElement.dataset.uid === snapshot.selected_issue_uid;
      const revealChain = selectedRowIsFocused ? null : selectedMemberRevealChain(snapshot);
      revealRequest = revealChain
        ? {
            uid: revealChain.at(-1)!.uid,
            chain: revealChain,
            generation: ++revealGeneration,
          }
        : null;
      connection = { status: "online" };
      bootstrapDaemonId = snapshot.daemon_id;
      setActiveKataDaemon(snapshot.daemon_id);
      workspaceOwnershipPending = false;
      error = null;
      viewError = null;
      persistActiveWorkspaceState();
      scheduleSelectedRecurrenceLoad(snapshot);
    },
    onStreamOpen: () => {
      connection = { status: "online" };
    },
    onStreamError: (message) => {
      connection = { status: "error", message };
    },
  });
  const authorityStore = authorityController.authorityStore;
  const acceptedSnapshot = $derived(authorityStore.snapshot);
  const acceptedKataDaemonId = $derived(
    getActiveKataDaemon() ??
      getDefaultKataDaemon() ??
      daemonInfos.find((daemon) => daemon.default)?.id ??
      daemonInfos[0]?.id,
  );
  const activeKataDaemonId = $derived(acceptedSnapshot?.daemon_id ?? bootstrapDaemonId ?? acceptedKataDaemonId);
  const acceptedProjects = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.projects) as KataProjectSummary[] : [],
  );
  const acceptedAreas = $derived(deriveKataAreas([...acceptedProjects]));
  const acceptedCurrentView = $derived.by(() => {
    if (!acceptedSnapshot) return { name: currentViewName, groups: [] };
    const projected = projectKataWorkspaceView({
          view: currentViewName,
          filters: searchFilters,
          snapshot: acceptedSnapshot,
          issues: authorityStore.projection.issues,
        });
    return { name: projected.view, groups: projected.groups, fetched_at: projected.fetched_at };
  });
  const acceptedSelectedIssue = $derived(
    acceptedSnapshot?.selected_detail
      ? structuredClone(acceptedSnapshot.selected_detail) as KataTaskDetail
      : null,
  );
  const acceptedSelectedEvents = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.selected_history) as KataTaskEvent[] : [],
  );
  const acceptedIssueCatalog = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.issues) as KataTaskSummary[] : [],
  );
  const acceptedGraph = $derived(
    acceptedSnapshot?.graph
      ? structuredClone(acceptedSnapshot.graph) as KataReachableGraphResponse
      : null,
  );
  const acceptedGraphSourceIssue = $derived.by(() => {
    if (!graphSourceIssue) return null;
    const issue = acceptedIssueCatalog.find((candidate) => candidate.uid === graphSourceIssue?.uid) ??
      acceptedGraph?.nodes.find((candidate) => candidate.uid === graphSourceIssue?.uid) ??
      graphSourceIssue;
    return structuredClone(issue) as KataTaskSummary;
  });
  const selectedDetailEnrichmentError = $derived(
    acceptedSnapshot?.selected_issue_uid
      ? acceptedSnapshot.enrichment_errors.detail?.message ?? null
      : null,
  );
  const selectedHistoryEnrichmentError = $derived(
    acceptedSnapshot?.selected_issue_uid && acceptedSelectedIssue
      ? acceptedSnapshot.enrichment_errors.history?.message ?? null
      : null,
  );
  const graphEnrichmentError = $derived(
    acceptedSnapshot?.graph_source_uid && acceptedSnapshot.graph_source_uid === acceptedGraphSourceIssue?.uid
      ? acceptedSnapshot.enrichment_errors.graph?.message ?? null
      : null,
  );
  const acceptedReadyIssueUIDs = $derived(acceptedSnapshot?.member_issue_uid_set ?? new Set<string>());

  // The workspace target arrives with the combined task-detail payload, so
  // the workspace action renders atomically with the detail pane.
  const workspaceTarget = $derived(
    acceptedSelectedIssue?.workspace_target?.available ? acceptedSelectedIssue.workspace_target : null,
  );
  // A daemon switch is transactional. Catalog data loaded while the target
  // is still provisional must not repaint either daemon's project controls.
  const visibleProjects = $derived(switchingDaemon ? [] : acceptedProjects);
  const visibleAreas = $derived(switchingDaemon ? [] : acceptedAreas);

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
    task: () => Promise<unknown>,
    failureSurface: FailureSurface = "daemon",
    shouldSurfaceFailure: () => boolean = () => true,
  ): Promise<boolean> {
    const loadingGeneration = beginViewLoading();
    clearTaskErrors(failureSurface);
    const expansionSignature = currentExpansionSignature();
    try {
      const result = await task();
      const ok = typeof result === "boolean" ? result : true;
      if (ok && currentExpansionSignature() !== expansionSignature) {
        resetIssueExpansion();
      }
      return ok;
    } catch (err) {
      if (shouldSurfaceFailure()) {
        const message = kataRequestErrorMessage(err);
        surfaceTaskError(message, failureSurface);
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

  function authorityIdentity(intent: KataSnapshotIntent | null): string {
    if (!intent) return "";
    return JSON.stringify([
      intent.daemon_id ?? "",
      intent.scope,
      intent.project_uid ?? "",
      intent.authority,
      intent.selected_issue_uid ?? "",
      intent.graph_source_uid ?? "",
    ]);
  }

  function selectedMemberRevealChain(snapshot: KataWorkspaceSnapshotProjection): KataTaskSummary[] | null {
    const selectedUID = snapshot.selected_issue_uid;
    if (!selectedUID || !snapshot.member_issue_uid_set.has(selectedUID)) return null;
    const issuesByUID = new Map(snapshot.issues.map((issue) => [issue.uid, issue]));
    const selected = issuesByUID.get(selectedUID);
    if (!selected?.parent?.uid) return null;

    const chain: KataTaskSummary[] = [];
    const visited = new Set<string>();
    let current = selected;
    while (true) {
      if (visited.has(current.uid)) return null;
      visited.add(current.uid);
      chain.unshift(structuredClone(current) as KataTaskSummary);
      const parentUID = current.parent?.uid;
      if (!parentUID) break;
      const parent = issuesByUID.get(parentUID);
      if (!parent) return null;
      current = parent;
    }
    return chain.length > 1 ? chain : null;
  }

  function updateAuthorityPresentationOrLoad(
    request: ReturnType<typeof kataWorkspaceAuthorityRequest>,
  ): Promise<boolean> | null {
    if (authorityIdentity(authorityStore.state.intent) === authorityIdentity(request.intent)) {
      authorityStore.updatePresentation(request.presentation);
      return null;
    }
    return authorityController.load(request);
  }

  onMount(() => {
    let cancelled = false;
    const bootstrapRouteSignature = JSON.stringify([
      requestedDaemonId?.trim() ?? "",
      routeViewName ?? "",
      routeScopeUID?.trim() ?? "",
      selectedIssueUID?.trim() ?? "",
    ]);
    const bootstrapViewScopeSignature = JSON.stringify([
      routeViewName ?? "",
      routeScopeUID?.trim() ?? "",
    ]);
    const bootstrapAuthorityRouteSignature = JSON.stringify([
      requestedDaemonId?.trim() ?? "",
      routeViewName ?? "",
      routeScopeUID?.trim() ?? "",
    ]);
    const bootstrapGraphSourceUID = graphAuthoritySourceUID ?? "";
    loadLayoutPrefs();

    void (async () => {
      try {
        const daemons = await fetchKataDaemons();
        if (cancelled) return;
        daemonInfos = daemons;
        setKataDaemonRoster(
          daemons.map((daemon) => daemon.id),
          daemons.find((daemon) => daemon.default)?.id,
        );
        const requestedDaemon = requestedDaemonId?.trim() || undefined;
        if (requestedDaemon && !daemons.some((daemon) => daemon.id === requestedDaemon)) {
          const message = `Kata daemon ${requestedDaemon} is not configured.`;
          authorityController.stop();
          authorityStore.abandon(message);
          workspaceOwnershipPending = true;
          connection = { status: "error", message };
          error = message;
          return;
        }
        const daemonID =
          requestedDaemon ??
          getActiveKataDaemon() ??
          daemons.find((daemon) => daemon.default)?.id ??
          daemons[0]?.id ??
          "home";
        const persisted = loadKataWorkspaceState(daemonID);
        const restoredView = routeViewName ?? persisted?.view ?? "all";
        const restoredFilters = persisted?.filters ?? defaultKataTaskSearchFilters();
        currentViewName = restoredView;
        searchFilters = routeScopeUID
          ? { ...restoredFilters, scope: { kind: "project", project_uid: routeScopeUID } }
          : restoredFilters;
        bootstrapDaemonId = daemonID;
        const request = kataWorkspaceAuthorityRequest({
          daemonID,
          view: restoredView,
          filters: searchFilters,
          selectedIssueUID: selectedIssueUID ?? persisted?.selectedIssueUID ?? null,
          graphSourceUID: graphSourceIssue?.uid,
        });
        await authorityController.load(request);
      } catch (loadError) {
        if (!cancelled) error = kataRequestErrorMessage(loadError);
      } finally {
        if (!cancelled) {
          appliedRouteSignature = bootstrapRouteSignature;
          appliedViewScopeSignature = bootstrapViewScopeSignature;
          appliedAuthorityRouteSignature = bootstrapAuthorityRouteSignature;
          appliedGraphSourceUID = bootstrapGraphSourceUID;
          routeReconcilerReady = true;
          loading = false;
        }
      }
    })();

    return () => {
      cancelled = true;
      authorityController.stop();
    };
  });

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

  function daemonSwitchLocked(): boolean {
    return loading || switchingDaemon || workspaceOwnershipPending || pendingMutationCount > 0 || workspaceActionBusy;
  }

  function scheduleSelectedRecurrenceLoad(snapshot: KataWorkspaceSnapshotProjection): void {
    const generation = ++recurrenceLoadGeneration;
    selectedRecurrences = [];
    const detail = snapshot.selected_detail;
    if (!detail) return;
    void taskAPI
      .recurrences(detail.issue.project_id, { daemonId: snapshot.daemon_id })
      .then((response) => {
        if (generation !== recurrenceLoadGeneration) return;
        const current = authorityStore.snapshot;
        if (
          current?.daemon_id !== snapshot.daemon_id ||
          current.selected_issue_uid !== detail.issue.uid ||
          current.selected_detail?.issue.project_id !== detail.issue.project_id
        ) {
          return;
        }
        selectedRecurrences = response.recurrences;
      })
      .catch(() => {
        if (generation === recurrenceLoadGeneration) selectedRecurrences = [];
      });
  }

  function acceptedDaemonIDForMutation(): string {
    const daemonID = acceptedSnapshot?.daemon_id;
    if (!daemonID) throw new Error("No accepted Kata snapshot daemon is available.");
    return daemonID;
  }

  function acceptedMutationOptions(): KataPinnedDaemonOptions {
    return { daemonId: acceptedDaemonIDForMutation() };
  }

  function selectedMutationTarget(uid: string): KataTaskMutationTarget {
    if (!acceptedSelectedIssue || acceptedSelectedIssue.issue.uid !== uid) {
      throw new Error(`issue not selected: ${uid}`);
    }
    return { project_id: acceptedSelectedIssue.issue.project_id, ref: uid };
  }

  function selectedMutationETag(uid: string): string {
    if (!acceptedSelectedIssue || acceptedSelectedIssue.issue.uid !== uid) {
      throw new Error(`issue not selected: ${uid}`);
    }
    if (!acceptedSelectedIssue.etag) throw new Error(`selected snapshot is missing an ETag for ${uid}`);
    return acceptedSelectedIssue.etag;
  }

  async function runAuthorityMutation<T>(task: () => Promise<T>): Promise<T> {
    pendingMutationCount += 1;
    try {
      return await task();
    } finally {
      pendingMutationCount -= 1;
    }
  }

  $effect(() => {
    const nextRequestedDaemon = requestedDaemonId?.trim() ?? "";
    const nextRouteView = routeViewName;
    const nextRouteScope = routeScopeUID?.trim() ?? "";
    const nextSelectedIssue = selectedIssueUID?.trim() ?? "";
    const nextGraphSourceUID = graphAuthoritySourceUID ?? "";
    const nextRouteSignature = JSON.stringify([
      nextRequestedDaemon,
      nextRouteView ?? "",
      nextRouteScope,
      nextSelectedIssue,
    ]);
    const nextViewScopeSignature = JSON.stringify([nextRouteView ?? "", nextRouteScope]);
    const nextAuthorityRouteSignature = JSON.stringify([
      nextRequestedDaemon,
      nextRouteView ?? "",
      nextRouteScope,
    ]);
    if (!routeReconcilerReady) return;

    if (
      replaceNextSelectionScopeUID &&
      (nextRouteScope !== replaceNextSelectionScopeUID || nextSelectedIssue !== "")
    ) {
      replaceNextSelectionScopeUID = null;
    }

    const routeChanged = nextRouteSignature !== appliedRouteSignature;
    const graphChanged = nextGraphSourceUID !== appliedGraphSourceUID;
    if (!routeChanged && !graphChanged) return;
    const viewOrScopeChanged = nextViewScopeSignature !== appliedViewScopeSignature;
    const authorityRouteChanged = nextAuthorityRouteSignature !== appliedAuthorityRouteSignature;
    appliedRouteSignature = nextRouteSignature;
    appliedViewScopeSignature = nextViewScopeSignature;
    appliedAuthorityRouteSignature = nextAuthorityRouteSignature;
    appliedGraphSourceUID = authorityRouteChanged ? "" : nextGraphSourceUID;
    const requestedDaemon = nextRequestedDaemon || undefined;
    if (requestedDaemon && daemonInfos.length > 0 && !daemonInfos.some((daemon) => daemon.id === requestedDaemon)) {
      const message = `Kata daemon ${requestedDaemon} is not configured.`;
      routeReconcileGeneration += 1;
      authorityController.stop();
      authorityStore.abandon(message);
      workspaceOwnershipPending = true;
      connection = { status: "error", message };
      error = message;
      return;
    }

    const generation = ++routeReconcileGeneration;
    if (routeChanged) {
      beginNavigation();
      resetDetailDrafts();
      if (authorityRouteChanged) {
        listMode = "tasks";
        graphSourceIssue = null;
        graphAuthoritySourceUID = null;
        currentViewName = nextRouteView ?? "all";
        const nextScope: KataTaskSearchFilters["scope"] = nextRouteScope
          ? { kind: "project", project_uid: nextRouteScope }
          : { kind: "all" };
        searchFilters = viewOrScopeChanged
          ? { ...defaultKataTaskSearchFilters(), scope: nextScope }
          : { ...searchFilters, scope: nextScope };
      }
    }

    const request = kataWorkspaceAuthorityRequest({
      daemonID: requestedDaemon ?? activeKataDaemonId,
      view: currentViewName,
      filters: searchFilters,
      selectedIssueUID: routeChanged
        ? nextSelectedIssue
        : acceptedSelectedIssue?.issue.uid ?? nextSelectedIssue,
      graphSourceUID: authorityRouteChanged ? undefined : nextGraphSourceUID,
    });
    const load = untrack(() => updateAuthorityPresentationOrLoad(request));
    if (!load) {
      persistActiveWorkspaceState();
      return;
    }

    workspaceOwnershipPending = true;
    const loadingGeneration = beginViewLoading();
    void load
      .catch((loadError: unknown) => {
        if (generation !== routeReconcileGeneration) return;
        error = kataRequestErrorMessage(loadError);
        workspaceOwnershipPending = false;
      })
      .finally(() => {
        endViewLoading(loadingGeneration);
      });
  });
  function selectedProjectName(): string | null {
    const scope = searchFilters.scope;
    if (scope.kind !== "project") return null;
    return acceptedProjects.find((project) => project.uid === scope.project_uid)?.name ?? null;
  }

  function projectNameForIssue(issue: KataTaskSummary): string | null {
    const project = acceptedProjects.find((candidate) => candidate.uid === issue.project_uid);
    return project?.name ?? issue.project_name ?? null;
  }

  function ownerOptions(): TypeaheadOption[] {
    const seen = new SvelteSet<string>();
    return [acceptedSelectedIssue?.issue.owner, ...visibleIssues().map((issue) => issue.owner)]
      .filter((owner): owner is string => typeof owner === "string" && owner.trim() !== "")
      .filter((owner) => {
        const key = owner.toLowerCase();
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
      .map((owner) => ({ name: owner, label: owner }));
  }

  function listTitle(): string {
    return selectedProjectName() ?? systemViews.find((view) => view.name === acceptedCurrentView.name)?.label ?? "Kata";
  }

  function activeDaemonStatusLabel(): string | undefined {
    if (routedDaemonError) return routedDaemonError;
    if (error) return error;
    if (
      activeKataDaemonId &&
      getKataDaemonRosterLoaded() &&
      !getKataDaemonRoster().includes(activeKataDaemonId)
    ) {
      return "Daemon is no longer configured";
    }
    if (connection.status !== "error") return undefined;
    return connection.message ?? "Connection failed";
  }

  function resetIssueExpansion(): void {
    listResetGeneration += 1;
  }

  function currentExpansionSignature(): string {
    const issueParts = acceptedCurrentView.groups.flatMap((group) =>
      group.issues.map(
        (issue) =>
          `${group.id}:${issue.uid}:${issue.revision}:${issue.parent_short_id ?? ""}:${issue.child_counts?.open ?? 0}:${issue.child_counts?.total ?? 0}`,
      ),
    );
    return [activeKataDaemonId ?? "", acceptedCurrentView.name, acceptedCurrentView.fetched_at ?? "", ...issueParts].join("|");
  }

  function visibleIssues(): KataTaskSummary[] {
    return acceptedCurrentView.groups.flatMap((group) => group.issues);
  }

  function persistActiveWorkspaceState(): void {
    const daemonID = activeKataDaemonId;
    if (!daemonID || switchingDaemon || workspaceOwnershipPending) return;
    saveKataWorkspaceState(daemonID, {
      view: acceptedCurrentView.name,
      filters: searchFilters,
      selectedIssueUID: acceptedSelectedIssue?.issue.uid ?? null,
    });
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

  async function updateSearchFilters(filters: Partial<KataTaskSearchFilters>): Promise<void> {
    replaceNextSelectionScopeUID = null;
    const previousScope = searchFilters.scope;
    const nextScope = filters.scope ?? searchFilters.scope;
    const scopeChanged =
      previousScope.kind !== nextScope.kind ||
      (previousScope.kind === "project" &&
        nextScope.kind === "project" &&
        previousScope.project_uid !== nextScope.project_uid);
    searchFilters = { ...searchFilters, ...filters, scope: nextScope };
    const request = kataWorkspaceAuthorityRequest({
      daemonID: activeKataDaemonId,
      view: acceptedCurrentView.name,
      filters: searchFilters,
      selectedIssueUID: acceptedSelectedIssue?.issue.uid ?? selectedIssueUID,
      graphSourceUID: graphSourceIssue?.uid,
    });
    const load = updateAuthorityPresentationOrLoad(request);
    if (load) {
      await runViewTask(async () => {
        await load;
      }, "view");
    }
    if (scopeChanged) {
      const scopeUID = searchFilters.scope.kind === "project" ? searchFilters.scope.project_uid : null;
      onRouteStateChange?.(
        canonicalRoute(acceptedCurrentView.name, scopeUID, acceptedSelectedIssue?.issue.uid ?? null, true),
      );
    }
    persistActiveWorkspaceState();
  }

  async function openRoutedSystemView(viewName: KataTaskViewName, direct = false): Promise<void> {
    replaceNextSelectionScopeUID = null;
    beginNavigation();
    closeReachableGraph(false);
    resetDetailDrafts();
    currentViewName = viewName;
    searchFilters = defaultKataTaskSearchFilters();
    await runViewTask(async () => {
      await authorityController.load(kataWorkspaceAuthorityRequest({
        daemonID: activeKataDaemonId,
        view: viewName,
        filters: searchFilters,
        selectedIssueUID: acceptedSelectedIssue?.issue.uid ?? selectedIssueUID,
      }));
    }, "view");
    const route = canonicalRoute(viewName, null, acceptedSelectedIssue?.issue.uid ?? null);
    onRouteStateChange?.(route);
    if (direct) persistActiveWorkspaceState();
  }

  async function openRoutedProjectScope(projectUID: string, direct = false): Promise<void> {
    replaceNextSelectionScopeUID = null;
    beginNavigation();
    closeReachableGraph(false);
    resetDetailDrafts();
    const previousSelectedIssue = acceptedSelectedIssue?.issue;
    const previousSelectedIssueUID = previousSelectedIssue?.uid ?? selectedIssueUID?.trim() ?? null;
    const selectedIssueUIDForScope = previousSelectedIssue?.project_uid === projectUID
      ? previousSelectedIssue.uid
      : undefined;
    currentViewName = "all";
    searchFilters = {
      ...defaultKataTaskSearchFilters(),
      scope: { kind: "project", project_uid: projectUID },
    };
    projectNavigationSelectionUID = previousSelectedIssueUID;
    const accepted = await runViewTask(async () => {
      await authorityController.load(kataWorkspaceAuthorityRequest({
        daemonID: activeKataDaemonId,
        view: "all",
        filters: searchFilters,
        selectedIssueUID: selectedIssueUIDForScope,
      }));
    }, "view");
    projectNavigationSelectionUID = null;
    if (!accepted) return;
    if (previousSelectedIssueUID && acceptedSelectedIssue?.issue.uid !== previousSelectedIssueUID) {
      replaceNextSelectionScopeUID = projectUID;
    }
    onRouteStateChange?.({ view: null, scope: projectUID, issue: acceptedSelectedIssue?.issue.uid ?? null });
    if (direct) persistActiveWorkspaceState();
  }

  function scheduleProjectScope(projectUID: string): void {
    void openRoutedProjectScope(projectUID, true);
  }

  async function createKataProject(name: string): Promise<KataTaskMutationResponse> {
    if (workspaceActionsBlocked) throw new Error("Kata workspace is not writable.");
    const snapshot = acceptedSnapshot;
    if (!snapshot) throw new Error("No accepted Kata snapshot is available.");
    const pending: PendingCreatedProjectScope = {
      daemonID: snapshot.daemon_id,
      name: name.trim(),
      existingUIDs: new Set(snapshot.projects.map((project) => project.uid)),
    };
    pendingCreatedProjectScope = pending;
    try {
      const acknowledgement = await runAuthorityMutation(() => taskAPI.createProject(name, acceptedMutationOptions()));
      if (!acknowledgement.changed && pendingCreatedProjectScope === pending) pendingCreatedProjectScope = null;
      return acknowledgement;
    } catch (error) {
      if (pendingCreatedProjectScope === pending) pendingCreatedProjectScope = null;
      throw error;
    }
  }

  async function submitQuickCapture(title: string): Promise<void> {
    if (workspaceActionsBlocked) return;
    const inbox = acceptedProjects.find((project) => project.metadata.role === "inbox");
    if (!inbox) throw new Error("task inbox project is not available");
    await runViewTaskOrThrow(async () => {
      await runAuthorityMutation(() => taskAPI.createIssue(
        inbox.id,
        actor,
        { title },
        acceptedMutationOptions(),
      ));
    });
  }

  async function switchKataDaemon(id: string): Promise<void> {
    if (id === activeKataDaemonId || switchingDaemon) return;
    replaceNextSelectionScopeUID = null;
    switchingDaemon = true;
    viewError = null;
    try {
      const accepted = await authorityController.load(kataWorkspaceAuthorityRequest({
        daemonID: id,
        view: acceptedCurrentView.name,
        filters: searchFilters,
        selectedIssueUID: acceptedSelectedIssue?.issue.uid ?? selectedIssueUID,
        graphSourceUID: graphSourceIssue?.uid,
      }));
      if (!accepted) return;
      onRouteStateChange?.({ daemon: null }, { replace: true });
    } catch (switchError) {
      showFlash(kataRequestErrorMessage(switchError), { tone: "danger" });
    } finally {
      switchingDaemon = false;
    }
  }

  function resetDetailDrafts(): void {
    checklistRevealed = false;
    recurrenceDialogs?.closeAll();
  }

  async function selectIssue(uid: string, notify = true, direct = notify): Promise<void> {
    const replaceRoute =
      searchFilters.scope.kind === "project" && replaceNextSelectionScopeUID === searchFilters.scope.project_uid;
    replaceNextSelectionScopeUID = null;
    const generation = beginNavigation();
    resetDetailDrafts();
    const ok = await runViewTask(async () =>
      authorityController.load(kataWorkspaceAuthorityRequest({
        daemonID: activeKataDaemonId,
        view: acceptedCurrentView.name,
        filters: searchFilters,
        selectedIssueUID: uid,
        graphSourceUID: graphSourceIssue?.uid,
      })), "view");
    if (!ok || !isCurrentNavigation(generation)) return;
    if (notify) {
      if (replaceRoute && onRouteStateChange) onRouteStateChange({ issue: uid }, { replace: true });
      else onSelectedIssueChange?.(uid);
    }
    if (direct) persistActiveWorkspaceState();
  }

  function selectReachableGraphIssue(uid: string): void {
    void selectIssue(uid, true, true);
  }

  function openReachableGraph(issue: KataTaskSummary): void {
    graphSourceIssue = issue;
    graphAuthoritySourceUID = issue.uid;
    listMode = "reachableGraph";
  }

  function closeReachableGraph(reload = true): void {
    listMode = "tasks";
    graphSourceIssue = null;
    graphAuthoritySourceUID = null;
    if (!reload) appliedGraphSourceUID = "";
  }

  async function retryAcceptedEnrichment(): Promise<void> {
    await runViewTask(() => authorityController.retry(), "view");
  }

  async function moveSelectedIssue(toProjectUID: string): Promise<boolean> {
    const selected = acceptedSelectedIssue?.issue;
    if (workspaceActionsBlocked || !selected || !toProjectUID || pendingMoveIssueUIDs.has(selected.uid)) return false;
    const sourceIssueUID = selected.uid;
    const generation = navigationGeneration;
    pendingMoveIssueUIDs = new SvelteSet(pendingMoveIssueUIDs).add(sourceIssueUID);
    try {
      const ok = await runViewTask(
        () => runAuthorityMutation(() => taskAPI.moveIssue(
          selectedMutationTarget(sourceIssueUID),
          actor,
          toProjectUID,
          selectedMutationETag(sourceIssueUID),
          acceptedMutationOptions(),
        )),
        "flash",
        () => isCurrentNavigation(generation),
      );
      return ok;
    } finally {
      const nextPendingMoves = new SvelteSet(pendingMoveIssueUIDs);
      nextPendingMoves.delete(sourceIssueUID);
      pendingMoveIssueUIDs = nextPendingMoves;
    }
  }

  async function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => runAuthorityMutation(() => taskAPI.patchIssueMetadata(
      selectedMutationTarget(uid),
      actor,
      patch,
      selectedMutationETag(uid),
      { daemonId: acceptedDaemonIDForMutation() },
    )), "flash");
    return ok;
  }

  async function addSelectedComment(uid: string, body: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    return runViewTask(() => runAuthorityMutation(() =>
      taskAPI.addComment(selectedMutationTarget(uid), actor, body, acceptedMutationOptions())
    ), "flash");
  }

  async function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.editIssue(selectedMutationTarget(uid), actor, patch, acceptedMutationOptions())
    ), "flash");
    return ok;
  }

  async function assignSelectedOwner(uid: string, owner: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.assignOwner(selectedMutationTarget(uid), actor, owner, acceptedMutationOptions())
    ), "flash");
    return ok;
  }

  async function unassignSelectedOwner(uid: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.unassignOwner(selectedMutationTarget(uid), actor, acceptedMutationOptions())
    ), "flash");
    return ok;
  }

  async function setSelectedPriority(uid: string, priority: number | null): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.setPriority(selectedMutationTarget(uid), actor, priority, acceptedMutationOptions())
    ), "flash");
    return ok;
  }

  async function addSelectedLabel(uid: string, label: string): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    const ok = await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.addLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions())
    ), "flash");
    return ok;
  }

  async function removeSelectedLabel(uid: string, label: string): Promise<void> {
    if (workspaceActionsBlocked) return;
    await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.removeLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions())
    ), "flash");
  }

  function selectedMessageLinks(): MessageLinkRef[] {
    return acceptedSelectedIssue ? readMessageLinks(acceptedSelectedIssue.issue.metadata) : [];
  }

  function openWorkspace(id: string): void {
    navigate(`/terminal/${encodeURIComponent(id)}`);
  }

  async function createWorkspaceForSelectedIssue(): Promise<void> {
    const selected = acceptedSelectedIssue?.issue;
    if (workspaceActionsBlocked || !selected || workspaceActionBusy) return;
    workspaceActionBusy = true;
    try {
      const created = await createKataWorkspaceForTask(
        kataWorkspaceIdentityFromIssue(
          selected,
          acceptedSnapshot?.daemon_id ?? null,
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
      await runAuthorityMutation(() => taskAPI.createRecurrence(projectID, input, acceptedMutationOptions()));
    }, "none");
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    if (workspaceActionsBlocked) return;
    await runViewTaskOrThrow(async () => {
      const recurrence = selectedRecurrences.find((item) => item.id === id);
      if (!recurrence) throw new Error(`recurrence not loaded: id=${id}`);
      await runAuthorityMutation(() => taskAPI.patchRecurrence(
        recurrence.project_id,
        recurrence.uid,
        input,
        etag,
        acceptedMutationOptions(),
      ));
    }, "none");
  }

  async function deleteRecurrence(recurrence: KataRecurrence): Promise<boolean> {
    if (workspaceActionsBlocked) return false;
    return runViewTask(() => runAuthorityMutation(() =>
      taskAPI.deleteRecurrence(recurrence.project_id, recurrence.uid, actor, acceptedMutationOptions())
    ), "flash");
  }

  async function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Promise<boolean> {
    const selected = acceptedSelectedIssue;
    if (workspaceActionsBlocked || !selected) return false;
    const ok = await runViewTask(
      () =>
        runAuthorityMutation(() => taskAPI.closeIssue(
          selectedMutationTarget(selected.issue.uid),
          actor,
          { reason, message },
          acceptedMutationOptions(),
        )),
      "flash",
    );
    return ok;
  }

  async function reopenSelectedIssue(): Promise<void> {
    const selected = acceptedSelectedIssue;
    if (workspaceActionsBlocked || !selected) return;
    await runViewTask(() => runAuthorityMutation(() =>
      taskAPI.reopenIssue(selectedMutationTarget(selected.issue.uid), actor, acceptedMutationOptions())
    ), "flash");
  }

  async function deleteSelectedIssue(): Promise<boolean> {
    return closeSelectedIssue("wontfix", "Deleted from issue detail.");
  }

  async function unlinkMessageLink(link: MessageLinkRef): Promise<void> {
    if (workspaceActionsBlocked || unlinkBusyIds.size > 0) return;
    const selected = acceptedSelectedIssue;
    if (!selected) return;
    const uid = selected.issue.uid;
    const links = selectedMessageLinks();
    const patch = computeRemoveMessageLinkPatch(links, link.message_id);
    if (patch === null) return;
    const metadataPatch: Record<string, unknown> = { mail_links: patch.mail_links };

    unlinkBusyIds = new Set(links.map((item) => item.message_id));
    try {
      const ok = await runViewTask(() => runAuthorityMutation(() => taskAPI.patchIssueMetadata(
        selectedMutationTarget(uid),
        actor,
        metadataPatch,
        selectedMutationETag(uid),
        { daemonId: acceptedDaemonIDForMutation() },
      )), "none");
      if (!ok) {
        showFlash(lastTaskError || "Could not unlink message.", { tone: "danger" });
      }
    } finally {
      unlinkBusyIds = new Set();
    }
  }
</script>

<section class="kata-feature" aria-labelledby="kata-title" inert={switchingDaemon} aria-busy={loading || switchingDaemon}>
  <header class="kata-header">
    <div class="kata-header-title">
      <h1 id="kata-title">Kata</h1>
      {#if daemonInfos.length > 0}
        <KataDaemonSwitcher
          daemons={daemonInfos}
          activeId={activeKataDaemonId}
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

  <div
    class="kata-layout"
    inert={workspaceOwnershipPending}
    aria-busy={loading || switchingDaemon || workspaceOwnershipPending}
  >
    <KataSidebar
      areas={visibleAreas}
      projects={visibleProjects}
      currentView={acceptedCurrentView}
      searchFilters={searchFilters}
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
        <p class="kata-view-error" role="alert">
          {viewError}
        </p>
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
    {#if listMode === "reachableGraph" && acceptedGraphSourceIssue}
      {#if acceptedGraph && acceptedSnapshot?.graph_source_uid === acceptedGraphSourceIssue.uid}
        <KataReachableGraph
          graph={acceptedGraph}
          sourceIssue={acceptedGraphSourceIssue}
          selectedUID={acceptedSelectedIssue?.issue.uid ?? null}
          layoutDirection={graphLayoutDirection}
          onBack={closeReachableGraph}
          onSelectIssue={(uid) => {
            selectReachableGraphIssue(uid);
          }}
        />
      {:else if graphEnrichmentError}
        <section class="kata-enrichment-state" aria-label="Reachable task graph">
          <p class="kata-enrichment-error" role="alert">{graphEnrichmentError}</p>
          <div class="kata-enrichment-actions">
            <button type="button" onclick={() => closeReachableGraph()}>Back to task list</button>
            <button type="button" onclick={() => void retryAcceptedEnrichment()}>Retry graph</button>
          </div>
        </section>
      {:else}
        <section
          class="kata-graph-loading"
          aria-label="Reachable task graph"
        >Loading graph...</section>
      {/if}
    {:else}
      <KataSearchPanel
        filters={searchFilters}
        projects={visibleProjects}
        onChange={updateSearchFilters}
      />
      {#key `${activeKataDaemonId ?? ""}:${listResetGeneration}`}
        <KataIssueList
          currentView={acceptedCurrentView}
          issueCatalog={acceptedIssueCatalog}
          scopeLabel={listTitle()}
          scopedProjectName={selectedProjectName()}
          selectedIssueUID={acceptedSelectedIssue?.issue.uid ?? null}
          loading={viewLoading}
          statusFilter={listStatusFilter}
          readyIssueUIDs={acceptedReadyIssueUIDs}
          resetGeneration={listResetGeneration}
          navigationGeneration={navigationEpoch}
          {revealRequest}
          onSelect={(issue) => {
            void selectIssue(issue.uid);
          }}
          onOpenGraph={openReachableGraph}
        />
      {/key}
    {/if}
  </div>
{/snippet}

{#snippet detailPane()}
  {#if acceptedSelectedIssue}
    {#if selectedHistoryEnrichmentError}
      <p class="kata-enrichment-warning" role="alert">{selectedHistoryEnrichmentError}</p>
    {/if}
    <KataIssueDetail
      issue={acceptedSelectedIssue}
      events={acceptedSelectedEvents}
      issueCatalog={acceptedIssueCatalog}
      {searchReferences}
      activeDaemonId={activeKataDaemonId}
      {linkFilters}
      onLinkFiltersChange={(next) => {
        linkFilters = next;
      }}
      projects={acceptedProjects}
      ownerOptions={ownerOptions()}
      messageLinks={selectedMessageLinks()}
      unlinkBusyIds={unlinkBusyIds}
      {selectedRecurrences}
      {checklistRevealed}
      movePending={pendingMoveIssueUIDs.has(acceptedSelectedIssue.issue.uid)}
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
  {:else if selectedDetailEnrichmentError}
    <section class="kata-detail-empty" aria-label="Task detail">
      <p class="kata-enrichment-error" role="alert">{selectedDetailEnrichmentError}</p>
    </section>
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
  selectedIssue={acceptedSelectedIssue}
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

  .kata-enrichment-state {
    display: grid;
    place-content: center;
    gap: var(--space-4);
    flex: 1 1 auto;
    min-height: 0;
    padding: var(--space-6);
    text-align: center;
  }

  .kata-enrichment-error,
  .kata-enrichment-warning {
    margin: 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .kata-enrichment-warning {
    padding: var(--space-3) var(--space-5);
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-primary);
  }

  .kata-enrichment-actions {
    display: flex;
    justify-content: center;
    gap: var(--space-3);
  }

  @media (max-width: 900px) {
    .kata-layout {
      grid-template-columns: 1fr;
      grid-template-rows: auto minmax(0, 1fr);
    }
  }
</style>
