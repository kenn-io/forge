<script lang="ts">
  import { Cause, Effect, Exit, Option } from "effect";
  import { onDestroy, onMount, untrack } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { getStores } from "../../context.js";
  import type { GeneratedApi } from "../../api/generated-api.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution, AppServices } from "../../app/runtime.js";
  import { IconButton, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    createdWorkspaceRef,
    isWorkspaceCreatePending,
  } from "../../stores/workspace-create-pending.svelte.js";
  import type { WorkspaceItemIdentity } from "../../workspace-inline.js";
  import LayoutPanelLeftIcon from "@lucide/svelte/icons/layout-panel-left";
  import LayoutPanelTopIcon from "@lucide/svelte/icons/layout-panel-top";
  import PlusIcon from "@lucide/svelte/icons/plus";

  import { fetchKataDaemons, type KataDaemonInfo } from "../../api/kata/daemons.js";
  import {
    createKataTaskAPI,
    KataMutationOutcomeUnknownError,
    KataMutationPartiallyAppliedError,
  } from "../../api/kata/taskClient.js";
  import {
    KataSnapshotAPIError,
    fetchKataWorkspaceSnapshot,
    searchKataTaskReferences,
    type KataSnapshotIntent,
    type KataTaskReferenceSearch,
  } from "../../api/kata/snapshot.js";
  import type { KataIssueNavigationTarget } from "../../api/kata/navigation.js";
  import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
  import { kataWorkspaceIdentityFromIssue } from "../../api/kata/workspaces.js";
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
    KataTaskEffect,
    KataTaskMutationResponse,
    KataTaskMutationTarget,
    KataTaskSearchFilters,
    KataTaskSummary,
    KataTaskViewName,
  } from "../../api/kata/taskTypes.js";
  import KataIssueDetail from "../../components/kata/KataIssueDetail.svelte";
  import KataIssueList, { type KataIssueRevealRequest } from "../../components/kata/KataIssueList.svelte";
  import KataResizableSash from "../../components/kata/KataResizableSash.svelte";
  import KataSidebar from "../../components/kata/KataSidebar.svelte";
  import QuickCapture from "../../components/shared/QuickCapture.svelte";
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
    KataMutationError,
    KataWorkflow,
    type KataCustomMutationUncertainty,
    type KataMutationFenceState,
  } from "./kata-workflow.js";
  import { KataWorkspaceCreationWorkflow } from "./kata-workspace-creation-workflow.js";
  import {
    commentMutationEvidence,
    editMutationEvidence,
    issueCreateMutationEvidence,
    kataMutationIdentity,
    labelMutationEvidence,
    metadataMutationEvidence,
    moveMutationEvidence,
    ownerMutationEvidence,
    priorityMutationEvidence,
    projectCreateMutationEvidence,
    reconcileRecurrenceMutation,
    recurrenceCreateMatches,
    recurrencePatchMatches,
    statusMutationEvidence,
    type KataMutationEvidence,
  } from "./kata-mutation-evidence.js";
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
    type KataWorkspaceAuthorityRequest,
  } from "./kataWorkspaceAuthority.js";
  import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";
  import {
    createKataWorkspaceAuthorityController,
    createKataWorkspaceAuthorityOwner,
  } from "./kataWorkspaceAuthorityController.svelte.js";
  import type { KataGraphLayoutDirection } from "./kataReachableGraph.js";

  const { settings } = getStores();
  const appRuntime = getAppRuntime();
  const mutationSurfaceOwner = createKataWorkspaceAuthorityOwner();

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
    reconcileRecurrences: (recurrences: readonly KataRecurrence[]) => void;
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

  interface PendingRecoveredSelection {
    uid: string;
    notify: boolean;
    direct: boolean;
    replaceRoute: boolean;
  }

  type SplitOrientation = "vertical" | "horizontal";
  type FailureSurface = "flash" | "daemon" | "view" | "none";
  type ListMode = "tasks" | "reachableGraph";

  function graphLayoutDirectionForSplit(orientation: SplitOrientation): KataGraphLayoutDirection {
    return orientation === "horizontal" ? "LR" : "TB";
  }

  let {
    api = undefined,
    searchReferences = undefined,
    selectedIssueUID = null,
    routeViewName = null,
    routeScopeUID = null,
    requestedDaemonId = null,
    onSelectedIssueChange = undefined,
    onRouteStateChange = undefined,
  }: Props = $props();

  let loading = $state(true);
  let viewLoading = $state(false);
  let viewLoadingGeneration = 0;
  let viewWorkCount = $state(0);
  let error = $state<string | null>(null);
  let viewError = $state<string | null>(null);
  let lastTaskError: string | null = null;
  let daemonInfos = $state.raw<KataDaemonInfo[]>([]);
  let daemonRosterLoaded = $state(false);
  let switchingDaemon = $state(false);
  let captureOpen = $state(false);
  let listResetGeneration = $state(0);
  let checklistRevealed = $state(false);
  let pendingMoveIssueUIDs = $state.raw<ReadonlySet<string>>(new Set());
  let recurrenceDialogs = $state<KataRecurrenceDialogController | null>(null);
  let selectedRecurrences = $state.raw<KataRecurrence[]>([]);
  let navigationExecution: AppExecution<void, never> | null = null;
  let pendingMutationCount = $state(0);
  let mutationRefreshPending = $state(false);
  let mutationAcknowledged = $state(false);
  let mutationDraftResetGeneration = $state(0);
  let mutationRecurrenceRefreshRequired = false;
  let recurrenceConflictRecoveryPending = $state(false);
  let mutationRefreshError = $state<string | null>(null);
  let mutationOutcomeUnknown = $state(false);
  let mutationPartialOutcome = $state(false);
  let mutationRefreshRequest: KataWorkspaceAuthorityRequest | null = null;
  let mutationRefreshGeneration = 0;
  let authorityRetrying = $state(false);
  let workspaceSurfaceMounted = true;
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
  let sidebarCatalog = $state.raw<{ daemonID: string; projects: KataProjectSummary[] } | null>(null);
  let pendingCreatedProjectScope: PendingCreatedProjectScope | null = null;
  let supersededRouteSelectionUID: string | null = null;
  let rowNavigationSelection: PendingRecoveredSelection | null = null;
  let replaceNextSelectionScopeUID: string | null = null;
  let pendingRecoveredSelection: PendingRecoveredSelection | null = null;
  const actor = "kenn-forge";
  let navigationGeneration = 0;
  // Reactive shadow of navigationGeneration so the issue list can drop
  // a pending keyboard selection the moment any navigation starts —
  // the list only remounts after the new view's data arrives, which is
  // too late for a selection released mid-transition.
  let navigationEpoch = $state(0);
  let revealRequest = $state<KataIssueRevealRequest | null>(null);
  let revealGeneration = 0;
  const layoutStorageKey = "kenn-forge:kata:task-layout/v1";
  const defaultSplitSizes: Record<SplitOrientation, number> = {
    vertical: 420,
    horizontal: 520,
  };
  let splitOrientation = $state<SplitOrientation>("vertical");
  let splitSizes = $state<Record<SplitOrientation, number>>({ ...defaultSplitSizes });
  const activeSplitSize = $derived(splitSizes[splitOrientation]);
  const graphLayoutDirection = $derived(graphLayoutDirectionForSplit(splitOrientation));
  const routedDaemonError = $derived(
    requestedDaemonId && daemonRosterLoaded && !daemonInfos.some((daemon) => daemon.id === requestedDaemonId)
      ? `Kata daemon ${requestedDaemonId} is not configured.`
      : null,
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
    owner: createKataWorkspaceAuthorityOwner(),
    resetIssueExpansion,
    onSnapshotAccepted: (snapshot) => Effect.gen(function* () {
      sidebarCatalog = {
        daemonID: snapshot.daemon_id,
        projects: structuredClone(snapshot.projects) as KataProjectSummary[],
      };
      const requestedIssueUID = selectedIssueUID?.trim() ?? "";
      const recoveredSelection = pendingRecoveredSelection?.uid === snapshot.selected_issue_uid
        ? pendingRecoveredSelection
        : null;
      // A selection-less snapshot accepted mid row-navigation is one leg of a
      // navigation transaction (for example moved-link-peer recovery); do not
      // rewrite the current history entry while its outcome is pending.
      const rowSelectionAccepted =
        rowNavigationSelection !== null &&
        (rowNavigationSelection.uid === snapshot.selected_issue_uid || !snapshot.selected_issue_uid);
      if (
        requestedIssueUID &&
        requestedIssueUID !== supersededRouteSelectionUID &&
        !rowSelectionAccepted &&
        !recoveredSelection &&
        snapshot.selected_issue_uid !== requestedIssueUID
      ) {
        onRouteStateChange?.({ issue: null }, { replace: true });
      }
      if (recoveredSelection) {
        pendingRecoveredSelection = null;
        if (recoveredSelection.notify) {
          if (recoveredSelection.replaceRoute && onRouteStateChange) {
            onRouteStateChange({ issue: recoveredSelection.uid }, { replace: true });
          } else {
            onSelectedIssueChange?.(recoveredSelection.uid);
          }
        }
        if (recoveredSelection.direct) persistActiveWorkspaceState();
      }
      const awaitRecurrenceRefresh =
        mutationRefreshPending &&
        mutationRecurrenceRefreshRequired &&
        (mutationAcknowledged || recurrenceConflictRecoveryPending);
      if (!awaitRecurrenceRefresh && !mutationOutcomeUnknown && !mutationPartialOutcome) {
        finishAcceptedMutationRefresh();
      }
      authorityRetrying = false;
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
      yield* loadSelectedRecurrences(snapshot, awaitRecurrenceRefresh);
    }),
    onStreamOpen: () => {
      connection = { status: "online" };
    },
    onStreamError: (message) => {
      connection = { status: "error", message };
    },
  });
  onDestroy(() => {
    workspaceSurfaceMounted = false;
    navigationExecution?.interrupt();
    appRuntime.runCommand(authorityController.dispose(), {
      operation: "dispose Kata workspace authority",
      safeContext: {},
      onFailure: () => {},
    });
  });

  const runtimeTaskReferenceSearch: KataTaskReferenceSearch = (query, options = {}) => {
    return searchKataTaskReferences(query, {
      ...options,
      daemon_id: options.daemon_id ?? activeKataDaemonId,
    });
  };

  function stopAuthority(): void {
    appRuntime.runCommand(authorityController.stop(), {
      operation: "stop Kata workspace authority",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function runNavigation<A>(
    command: Effect.Effect<A, never, AppServices>,
    operation: string,
    safeContext: Readonly<Record<string, string | number | boolean>> = {},
  ): void {
    navigationExecution?.interrupt();
    navigationExecution = appRuntime.runCommand(command.pipe(Effect.asVoid), {
      operation,
      safeContext,
      onFailure: () => {},
    });
  }
  const authorityStore = authorityController.authorityStore;
  const workspaceActionsBlocked = $derived(
    workspaceOwnershipPending ||
      switchingDaemon ||
      pendingMutationCount > 0 ||
      mutationRefreshPending,
  );
  const mutationActionsBlocked = $derived(
    workspaceActionsBlocked || authorityStore.state.phase !== "accepted",
  );
  const detailAuthorityBlocked = $derived(
    (mutationRefreshPending && (mutationAcknowledged || recurrenceConflictRecoveryPending)) ||
      authorityStore.state.phase !== "accepted",
  );
  const authorityRecoveryMessage = $derived(
    switchingDaemon || routedDaemonError
      ? null
      : mutationRefreshError
      ? mutationPartialOutcome
        ? mutationRefreshError
        : mutationOutcomeUnknown
        ? `Kata could not confirm whether the last change was applied: ${mutationRefreshError}`
        : recurrenceConflictRecoveryPending
        ? `The recurrence changed, but its current revision could not be loaded: ${mutationRefreshError}`
        : `Change saved, but Kata snapshot refresh failed: ${mutationRefreshError}`
      : authorityStore.state.phase === "degraded" || authorityStore.state.phase === "abandoned"
        ? authorityStore.state.error
        : workspaceOwnershipPending
          ? error
          : null,
  );
  const acceptedSnapshot = $derived(authorityStore.snapshot);
  const acceptedKataDaemonId = $derived(
    getActiveKataDaemon() ??
      getDefaultKataDaemon() ??
      daemonInfos.find((daemon) => daemon.default)?.id ??
      daemonInfos[0]?.id,
  );
  const activeKataDaemonId = $derived(acceptedSnapshot?.daemon_id ?? bootstrapDaemonId ?? acceptedKataDaemonId);
  let activeMutationFenceKey = $state<string | null>(null);

  function observeMutationFence(state: KataMutationFenceState): Effect.Effect<void> {
    return Effect.sync(() => {
      activeMutationFenceKey = state.kind === "resolved" ? null : state.identity.key;
      if (state.kind === "unknown") {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationOutcomeUnknown = true;
        mutationPartialOutcome = false;
        mutationRefreshError = state.message;
        mutationRefreshRequest = null;
        return;
      }
      if (state.kind === "partial") {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationOutcomeUnknown = false;
        mutationPartialOutcome = true;
        mutationRefreshError = state.message;
        mutationRefreshRequest = null;
        return;
      }
      if (state.kind === "reconciling") {
        mutationRefreshPending = true;
        mutationAcknowledged = false;
        mutationOutcomeUnknown = true;
        mutationPartialOutcome = false;
        mutationRefreshError = null;
        return;
      }
      if (state.resolution === "applied") mutationDraftResetGeneration += 1;
      mutationRefreshPending = false;
      mutationAcknowledged = false;
      mutationOutcomeUnknown = false;
      mutationPartialOutcome = false;
      mutationRefreshError = null;
      mutationRefreshRequest = null;
      mutationRefreshGeneration += 1;
    });
  }

  $effect(() => {
    const daemonId = activeKataDaemonId;
    if (!daemonId) return;
    const execution = appRuntime.runCommand(
      Effect.scoped(
        Effect.gen(function* () {
          const workflow = yield* KataWorkflow;
          yield* workflow.claimMutations(daemonId, mutationSurfaceOwner, observeMutationFence);
          return yield* Effect.never;
        }),
      ),
      {
        operation: "claim Kata mutation recovery",
        safeContext: { owner: mutationSurfaceOwner },
        onFailure: () => {},
      },
    );
    return execution.interrupt;
  });
  const acceptedProjects = $derived(
    acceptedSnapshot ? structuredClone(acceptedSnapshot.projects) as KataProjectSummary[] : [],
  );
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
      ? acceptedSnapshot.enrichment_errors.graph?.message ??
        (acceptedGraph ? null : "Reachable task graph is unavailable.")
      : null,
  );
  const acceptedReadyIssueUIDs = $derived(acceptedSnapshot?.member_issue_uid_set ?? new Set<string>());

  // The workspace target arrives with the combined task-detail payload, so
  // the workspace action renders atomically with the detail pane.
  const workspaceTarget = $derived(
    acceptedSelectedIssue?.workspace_target?.available ? acceptedSelectedIssue.workspace_target : null,
  );
  const workspaceItemIdentity = $derived.by<WorkspaceItemIdentity | null>(() => {
    const selected = acceptedSelectedIssue?.issue;
    const daemonID = acceptedSnapshot?.daemon_id;
    if (!selected || !daemonID) return null;
    return {
      provider: "kata",
      platformHost: daemonID,
      owner: selected.project_uid,
      name: selected.uid,
      repoPath: selected.project_uid,
      number: 0,
      itemType: "kata_task",
    };
  });
  const workspaceActionBusy = $derived(
    workspaceItemIdentity !== null && isWorkspaceCreatePending(workspaceItemIdentity),
  );
  const effectiveWorkspaceRef = $derived(
    workspaceTarget?.existing_workspace ??
      (workspaceItemIdentity === null ? null : createdWorkspaceRef(workspaceItemIdentity)),
  );
  // A daemon switch is transactional. Catalog data loaded while the target
  // is still provisional must not repaint either daemon's project controls.
  const visibleProjects = $derived.by(() => {
    if (switchingDaemon || routedDaemonError || authorityStore.state.phase === "abandoned") return [];
    const requestedDaemonID = authorityStore.state.intent?.daemon_id ?? activeKataDaemonId;
    if (!sidebarCatalog || (requestedDaemonID && requestedDaemonID !== sidebarCatalog.daemonID)) return [];
    return sidebarCatalog.projects;
  });
  const visibleAreas = $derived(deriveKataAreas([...visibleProjects]));

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

  function trackViewWork<A, E, R>(task: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> {
    return Effect.suspend(() => {
      const generation = beginViewLoading();
      return task.pipe(Effect.ensuring(Effect.sync(() => endViewLoading(generation))));
    });
  }

  function kataRequestErrorMessage(err: unknown): string {
    return err instanceof Error ? err.message : "Kata request failed.";
  }

  function kataRequestCauseMessage(cause: Cause.Cause<unknown>): string {
    const failure = Cause.findErrorOption(cause);
    return Option.isSome(failure) ? kataRequestErrorMessage(failure.value) : Cause.pretty(cause);
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

  function runViewTask<A, E, R>(
    task: Effect.Effect<A, E, R>,
    failureSurface: FailureSurface = "daemon",
    shouldSurfaceFailure: () => boolean = () => true,
  ): Effect.Effect<boolean, never, R> {
    return Effect.suspend(() => {
      const loadingGeneration = beginViewLoading();
      clearTaskErrors(failureSurface);
      const expansionSignature = currentExpansionSignature();
      return task.pipe(
        Effect.map((result) => (typeof result === "boolean" ? result : true)),
        Effect.tap((ok) =>
          Effect.sync(() => {
            if (ok && currentExpansionSignature() !== expansionSignature) resetIssueExpansion();
          }),
        ),
        Effect.catch((failure) =>
          Effect.sync(() => {
            if (shouldSurfaceFailure()) surfaceTaskError(kataRequestErrorMessage(failure), failureSurface);
            return false;
          }),
        ),
        Effect.ensuring(Effect.sync(() => endViewLoading(loadingGeneration))),
      );
    });
  }

  function runViewTaskOrThrow<E, R>(
    task: Effect.Effect<void, E, R>,
    failureSurface: FailureSurface = "daemon",
  ): Effect.Effect<void, E, R> {
    return Effect.suspend(() => {
      const loadingGeneration = beginViewLoading();
      clearTaskErrors(failureSurface);
      return task.pipe(
        Effect.tapError((failure) =>
          Effect.sync(() => surfaceTaskError(kataRequestErrorMessage(failure), failureSurface)),
        ),
        Effect.ensuring(Effect.sync(() => endViewLoading(loadingGeneration))),
      );
    });
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
  ): Effect.Effect<boolean, unknown, AppServices> | null {
    if (authorityIdentity(authorityStore.state.intent) === authorityIdentity(request.intent)) {
      authorityStore.updatePresentation(request.presentation);
      return null;
    }
    return authorityController.load(request);
  }

  onMount(() => {
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

    const bootstrap = appRuntime.runCommand(
      Effect.gen(function* () {
        const daemons = yield* fetchKataDaemons();
        daemonInfos = daemons;
        daemonRosterLoaded = true;
        setKataDaemonRoster(
          daemons.map((daemon) => daemon.id),
          daemons.find((daemon) => daemon.default)?.id,
        );
        const requestedDaemon = requestedDaemonId?.trim() || undefined;
        if (requestedDaemon && !daemons.some((daemon) => daemon.id === requestedDaemon)) {
          const message = `Kata daemon ${requestedDaemon} is not configured.`;
          stopAuthority();
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
        const restoredFilters =
          persisted?.view === restoredView ? persisted.filters : defaultKataTaskSearchFilters(restoredView);
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
        yield* authorityController.load(request);
      }).pipe(
        Effect.catch((loadError) =>
          Effect.sync(() => {
            error = kataRequestErrorMessage(loadError);
          }),
        ),
        Effect.onExit((exit) =>
          Exit.isFailure(exit) && Cause.hasInterruptsOnly(exit.cause)
            ? Effect.void
            : Effect.sync(() => {
                appliedRouteSignature = bootstrapRouteSignature;
                appliedViewScopeSignature = bootstrapViewScopeSignature;
                appliedAuthorityRouteSignature = bootstrapAuthorityRouteSignature;
                appliedGraphSourceUID = bootstrapGraphSourceUID;
                routeReconcilerReady = true;
                loading = false;
              }),
        ),
      ),
      {
        operation: "bootstrap Kata workspace",
        safeContext: {},
        onFailure: () => {},
      },
    );

    return () => {
      bootstrap.interrupt();
      stopAuthority();
    };
  });

  function beginNavigation(): number {
    pendingRecoveredSelection = null;
    rowNavigationSelection = null;
    supersededRouteSelectionUID = null;
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
    return loading || switchingDaemon || workspaceActionsBlocked;
  }

  function finishAcceptedMutationRefresh(): void {
    if (mutationRefreshPending && mutationAcknowledged) mutationDraftResetGeneration += 1;
    mutationRefreshPending = false;
    mutationAcknowledged = false;
    mutationRefreshError = null;
    mutationOutcomeUnknown = false;
    mutationPartialOutcome = false;
    mutationRefreshRequest = null;
    mutationRecurrenceRefreshRequired = false;
    recurrenceConflictRecoveryPending = false;
    mutationRefreshGeneration += 1;
  }

  function loadSelectedRecurrences(
    snapshot: KataWorkspaceSnapshotProjection,
    requiredForMutation = false,
  ): Effect.Effect<boolean, never, GeneratedApi> {
    return Effect.gen(function* () {
      selectedRecurrences = [];
      const detail = snapshot.selected_detail;
      if (!detail) {
        if (requiredForMutation) finishAcceptedMutationRefresh();
        return true;
      }
      return yield* taskAPI.recurrences(detail.issue.project_id, { daemonId: snapshot.daemon_id }).pipe(
        Effect.map((response) => {
          const current = authorityStore.snapshot;
          if (
            current?.daemon_id !== snapshot.daemon_id ||
            current.selected_issue_uid !== detail.issue.uid ||
            current.selected_detail?.issue.project_id !== detail.issue.project_id
          ) {
            return false;
          }
          selectedRecurrences = response.recurrences;
          recurrenceDialogs?.reconcileRecurrences(response.recurrences);
          if (requiredForMutation) finishAcceptedMutationRefresh();
          return true;
        }),
        Effect.catch((recurrenceError) =>
          Effect.sync(() => {
            selectedRecurrences = [];
            if (!requiredForMutation) return false;
            mutationRefreshPending = true;
            mutationRefreshError = kataRequestErrorMessage(recurrenceError);
            showFlash(
              recurrenceConflictRecoveryPending
                ? "The current recurrence revision could not refresh. Retry before making more changes."
                : "Change saved, but Kata recurrences could not refresh. Retry before making more changes.",
              { tone: "warning" },
            );
            return false;
          }),
        ),
      );
    });
  }

  function beginRecurrenceConflictRecovery(): void {
    recurrenceDialogs?.closeAll();
    mutationRefreshGeneration += 1;
    mutationRefreshPending = true;
    mutationAcknowledged = false;
    mutationRecurrenceRefreshRequired = true;
    recurrenceConflictRecoveryPending = true;
    mutationRefreshError = "Could not refresh Kata recurrences.";
    mutationOutcomeUnknown = false;
    mutationRefreshRequest = acceptedAuthorityRequest();
  }

  function acceptedDaemonIDForMutation(): string {
    const daemonID = acceptedSnapshot?.daemon_id;
    if (!daemonID) throw new Error("No accepted Kata snapshot daemon is available.");
    return daemonID;
  }

  function acceptedMutationOptions(): KataPinnedDaemonOptions {
    return { daemonId: acceptedDaemonIDForMutation() };
  }

  function hasHTTPStatus(error: unknown, status: number): boolean {
    return typeof error === "object" && error !== null && "status" in error && error.status === status;
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

  function runAuthorityMutation<T>(
    task: KataTaskEffect<T>,
    options:
      | { readonly evidence: KataMutationEvidence; readonly refreshRecurrences?: boolean }
      | { readonly uncertainty: KataCustomMutationUncertainty; readonly refreshRecurrences?: boolean },
  ): Effect.Effect<T, unknown, AppServices> {
    return Effect.suspend(() => {
      const capturedRequest = acceptedAuthorityRequest();
      const baseline = acceptedSnapshot;
      if (!baseline) return Effect.fail(new Error("No accepted Kata snapshot is available."));
      const uncertainty = "uncertainty" in options
        ? options.uncertainty
        : {
            identity: options.evidence.identity(baseline.daemon_id),
            baseline,
            readFresh: fetchKataWorkspaceSnapshot(
              {
                daemon_id: baseline.daemon_id,
                scope: "global",
                authority: "all",
                ...(options.evidence.selectedIssueUID === undefined
                  ? {}
                  : { selected_issue_uid: options.evidence.selectedIssueUID }),
              },
              { fresh: true },
            ),
            isApplied: options.evidence.isApplied,
          };
      let replacementRequest: KataWorkspaceAuthorityRequest | null = capturedRequest;
      let replacementGeneration = ++mutationRefreshGeneration;
      mutationRefreshPending = true;
      mutationAcknowledged = false;
      mutationRecurrenceRefreshRequired = options.refreshRecurrences === true;
      mutationRefreshError = null;
      mutationOutcomeUnknown = false;
      mutationRefreshRequest = capturedRequest;
      const program = Effect.gen(function* () {
        const workflow = yield* KataWorkflow;
        const result = yield* workflow.mutateAndRevalidate(
          baseline.daemon_id,
          Effect.sync(() => (pendingMutationCount += 1)).pipe(
            Effect.andThen(
              task.pipe(
                Effect.mapError(
                  (cause) => new KataMutationError({ message: kataRequestErrorMessage(cause), cause }),
                ),
              ),
            ),
            Effect.ensuring(Effect.sync(() => (pendingMutationCount -= 1))),
          ),
          Effect.suspend(() =>
            replacementRequest
              ? authorityController.load(replacementRequest).pipe(
                  Effect.mapError(
                    (cause) => new KataMutationError({ message: kataRequestErrorMessage(cause), cause }),
                  ),
                )
              : Effect.succeed(true),
          ).pipe(
            Effect.onExit((exit) =>
              Effect.sync(() => {
                if (
                  replacementGeneration !== mutationRefreshGeneration ||
                  (Exit.isSuccess(exit) && exit.value)
                ) {
                  return;
                }
                mutationRefreshPending = true;
                mutationRefreshError = Exit.isFailure(exit)
                  ? kataRequestCauseMessage(exit.cause)
                  : "Kata snapshot replacement was not accepted.";
                mutationRefreshRequest = currentMutationReplacementRequest(capturedRequest) ?? replacementRequest;
                showFlash("Change saved, but Kata could not refresh. Retry the snapshot before making more changes.", {
                  tone: "warning",
                });
              }),
            ),
          ),
          () =>
            Effect.sync(() => {
              replacementRequest = currentMutationReplacementRequest(capturedRequest);
              transferLoadingRowSelectionToReplacement(replacementRequest);
              replacementGeneration = ++mutationRefreshGeneration;
              mutationRefreshPending = replacementRequest !== null;
              mutationAcknowledged = replacementRequest !== null;
              mutationRecurrenceRefreshRequired =
                replacementRequest !== null && options.refreshRecurrences === true;
              mutationRefreshError = null;
              mutationOutcomeUnknown = false;
              mutationPartialOutcome = false;
              mutationRefreshRequest = replacementRequest;
            }),
          uncertainty,
        );
        return result.acknowledgement;
      });
      return program.pipe(
        Effect.catch((failure) => {
          const mutationError = failure instanceof KataMutationError && failure.cause instanceof Error
            ? failure.cause
            : failure;
          return Effect.sync(() => {
            mutationRefreshGeneration += 1;
            if (mutationError instanceof KataMutationOutcomeUnknownError) {
              mutationRefreshPending = true;
              mutationAcknowledged = false;
              mutationRecurrenceRefreshRequired = false;
              mutationOutcomeUnknown = true;
              mutationPartialOutcome = false;
              mutationRefreshError = mutationError.message;
              mutationRefreshRequest = currentMutationReplacementRequest(capturedRequest) ?? capturedRequest;
              return;
            }
            if (mutationError instanceof KataMutationPartiallyAppliedError) {
              mutationRefreshPending = true;
              mutationAcknowledged = false;
              mutationRecurrenceRefreshRequired = false;
              mutationOutcomeUnknown = false;
              mutationPartialOutcome = true;
              mutationRefreshError = mutationError.message;
              mutationRefreshRequest = null;
              return;
            }
            mutationRefreshPending = false;
            mutationAcknowledged = false;
            mutationRecurrenceRefreshRequired = false;
            mutationOutcomeUnknown = false;
            mutationPartialOutcome = false;
            mutationRefreshError = null;
            mutationRefreshRequest = null;
          }).pipe(Effect.andThen(Effect.fail(mutationError)));
        }),
      );
    });
  }

  function currentMutationReplacementRequest(
    capturedRequest: KataWorkspaceAuthorityRequest,
  ): KataWorkspaceAuthorityRequest | null {
    const state = authorityStore.state;
    if (state.phase === "loading" || state.phase === "accepted" || state.phase === "degraded") {
      return {
        intent: structuredClone(state.intent),
        presentation: { ...authorityStore.presentation },
      };
    }
    return state.phase === "idle" ? capturedRequest : null;
  }

  function transferLoadingRowSelectionToReplacement(request: KataWorkspaceAuthorityRequest | null): void {
    const selection = rowNavigationSelection;
    if (
      authorityStore.state.phase !== "loading" ||
      !selection ||
      request?.intent.selected_issue_uid !== selection.uid
    ) return;
    pendingRecoveredSelection = selection;
    rowNavigationSelection = null;
  }

  function acceptedAuthorityRequest(): KataWorkspaceAuthorityRequest {
    const state = authorityStore.state;
    if (state.phase !== "accepted") throw new Error("No accepted Kata snapshot intent is available.");
    return {
      intent: structuredClone(state.intent),
      presentation: { ...authorityStore.presentation },
    };
  }

  function nominalAuthorityRequest(): KataWorkspaceAuthorityRequest {
    return kataWorkspaceAuthorityRequest({
      daemonID: requestedDaemonId?.trim() || bootstrapDaemonId || acceptedKataDaemonId,
      view: currentViewName,
      filters: searchFilters,
      selectedIssueUID: selectedIssueUID?.trim() || acceptedSelectedIssue?.issue.uid,
      graphSourceUID: graphAuthoritySourceUID,
    });
  }

  function recoveryAuthorityRequest(): KataWorkspaceAuthorityRequest {
    const state = authorityStore.state;
    if (state.phase === "degraded") {
      return {
        intent: structuredClone(state.intent),
        presentation: { ...authorityStore.presentation },
      };
    }
    if (mutationRefreshRequest) return mutationRefreshRequest;
    return nominalAuthorityRequest();
  }

  function retryAuthoritySnapshot(): void {
    if (authorityRetrying) return;
    authorityRetrying = true;
    mutationRefreshError = null;
    error = null;
    const uncertainMutationKey = activeMutationFenceKey;
    const partialMutationKey = mutationPartialOutcome ? uncertainMutationKey : null;
    appRuntime.runCommand(
      Effect.gen(function* () {
        if (mutationOutcomeUnknown && uncertainMutationKey !== null) {
            const workflow = yield* KataWorkflow;
            const resolution = yield* workflow.reconcileMutation(uncertainMutationKey);
            if (resolution === "ambiguous") return;
        }
        const request = recoveryAuthorityRequest();
        transferLoadingRowSelectionToReplacement(request);
        if (!acceptedSnapshot) workspaceOwnershipPending = true;
        const accepted = yield* authorityController.load(request);
        if (!accepted) {
          return yield* Effect.fail(
            new Error(authorityStore.state.error ?? "Kata snapshot replacement was not accepted."),
          );
        }
        if (partialMutationKey !== null) {
            const workflow = yield* KataWorkflow;
            yield* workflow.acknowledgeMutation(partialMutationKey);
        }
      }).pipe(
        Effect.catch((retryError) =>
          Effect.sync(() => {
            const message = kataRequestErrorMessage(retryError);
            if (mutationRefreshPending) mutationRefreshError = message;
            else error = message;
            workspaceOwnershipPending = !authorityStore.snapshot;
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            authorityRetrying = false;
          }),
        ),
      ),
      {
        operation: "retry Kata workspace authority",
        safeContext: { daemonId: activeKataDaemonId ?? "" },
        onFailure: () => {},
      },
    );
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
    if (requestedDaemon && daemonRosterLoaded && !daemonInfos.some((daemon) => daemon.id === requestedDaemon)) {
      const message = `Kata daemon ${requestedDaemon} is not configured.`;
      routeReconcileGeneration += 1;
      stopAuthority();
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
          ? { ...defaultKataTaskSearchFilters(nextRouteView ?? "all"), scope: nextScope }
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
    runNavigation(
      load.pipe(
        Effect.catch((loadError) =>
          Effect.sync(() => {
            if (generation !== routeReconcileGeneration) return;
            error = kataRequestErrorMessage(loadError);
            workspaceOwnershipPending = false;
          }),
        ),
        Effect.ensuring(Effect.sync(() => endViewLoading(loadingGeneration))),
      ),
      "reconcile Kata workspace route",
      { daemonId: requestedDaemon ?? "" },
    );
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
    if (!revealRequest && acceptedSnapshot) {
      const revealChain = selectedMemberRevealChain(acceptedSnapshot);
      if (revealChain) {
        const selectedUID = revealChain.at(-1)!.uid;
        const restoreFocus =
          typeof document !== "undefined" &&
          document.activeElement instanceof HTMLElement &&
          document.activeElement.dataset.uid === selectedUID;
        revealRequest = {
          uid: selectedUID,
          chain: revealChain,
          generation: ++revealGeneration,
          ...(restoreFocus ? { restoreFocus: true } : {}),
        };
      }
    }
    listResetGeneration += 1;
  }

  function currentExpansionSignature(): string {
    const issueParts = acceptedCurrentView.groups.flatMap((group) =>
      group.issues.map(
        (issue) =>
          `${group.id}:${issue.uid}:${issue.revision}:${issue.parent_short_id ?? ""}:${issue.child_counts?.open ?? 0}:${issue.child_counts?.total ?? 0}`,
      ),
    );
    return [activeKataDaemonId ?? "", acceptedCurrentView.name, ...issueParts].join("|");
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

  function updateSearchFilters(filters: Partial<KataTaskSearchFilters>): Effect.Effect<boolean, never, AppServices> {
    return Effect.gen(function* () {
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
      const accepted = load ? yield* runViewTask(load, "view") : true;
      if (scopeChanged) {
        const scopeUID = searchFilters.scope.kind === "project" ? searchFilters.scope.project_uid : null;
        onRouteStateChange?.(
          canonicalRoute(acceptedCurrentView.name, scopeUID, acceptedSelectedIssue?.issue.uid ?? null, true),
        );
      }
      persistActiveWorkspaceState();
      return accepted;
    });
  }

  function openRoutedSystemView(viewName: KataTaskViewName, direct = false): Effect.Effect<boolean, never, AppServices> {
    return Effect.gen(function* () {
      replaceNextSelectionScopeUID = null;
      const generation = beginNavigation();
      closeReachableGraph(false);
      resetDetailDrafts();
      currentViewName = viewName;
      searchFilters = defaultKataTaskSearchFilters(viewName);
      const accepted = yield* runViewTask(
        authorityController.load(kataWorkspaceAuthorityRequest({
          daemonID: activeKataDaemonId,
          view: viewName,
          filters: searchFilters,
          selectedIssueUID: acceptedSelectedIssue?.issue.uid ?? selectedIssueUID,
        })),
        "view",
        () => isCurrentNavigation(generation),
      );
      if (!accepted || !isCurrentNavigation(generation)) return false;
      const route = canonicalRoute(viewName, null, acceptedSelectedIssue?.issue.uid ?? null);
      onRouteStateChange?.(route);
      if (direct) persistActiveWorkspaceState();
      return true;
    });
  }

  function openRoutedProjectScope(projectUID: string, direct = false): Effect.Effect<boolean, never, AppServices> {
    return Effect.gen(function* () {
      replaceNextSelectionScopeUID = null;
      const generation = beginNavigation();
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
      supersededRouteSelectionUID = previousSelectedIssueUID;
      const accepted = yield* runViewTask(
        authorityController.load(kataWorkspaceAuthorityRequest({
          daemonID: activeKataDaemonId,
          view: "all",
          filters: searchFilters,
          selectedIssueUID: selectedIssueUIDForScope,
        })),
        "view",
        () => isCurrentNavigation(generation),
      );
      if (!isCurrentNavigation(generation)) return false;
      supersededRouteSelectionUID = null;
      if (!accepted) return false;
      if (previousSelectedIssueUID && acceptedSelectedIssue?.issue.uid !== previousSelectedIssueUID) {
        replaceNextSelectionScopeUID = projectUID;
      }
      onRouteStateChange?.({ view: null, scope: projectUID, issue: acceptedSelectedIssue?.issue.uid ?? null });
      if (direct) persistActiveWorkspaceState();
      return true;
    });
  }

  function createKataProject(name: string): Effect.Effect<KataTaskMutationResponse, unknown, AppServices> {
    return Effect.suspend(() => {
      if (mutationActionsBlocked) return Effect.fail(new Error("Kata workspace is not writable."));
      const snapshot = acceptedSnapshot;
      if (!snapshot) return Effect.fail(new Error("No accepted Kata snapshot is available."));
      const pending: PendingCreatedProjectScope = {
        daemonID: snapshot.daemon_id,
        name: name.trim(),
        existingUIDs: new Set(snapshot.projects.map((project) => project.uid)),
      };
      pendingCreatedProjectScope = pending;
      return runAuthorityMutation(
        taskAPI.createProject(name, acceptedMutationOptions()),
        { evidence: projectCreateMutationEvidence(name, pending.existingUIDs) },
      ).pipe(
        Effect.tap((acknowledgement) =>
          Effect.sync(() => {
            if (!acknowledgement.changed && pendingCreatedProjectScope === pending) pendingCreatedProjectScope = null;
          }),
        ),
        Effect.tapError(() =>
          Effect.sync(() => {
            if (pendingCreatedProjectScope === pending) pendingCreatedProjectScope = null;
          }),
        ),
      );
    });
  }

  function submitQuickCapture(title: string): Effect.Effect<void, unknown, AppServices> {
    return Effect.suspend(() => {
      if (mutationActionsBlocked) return Effect.void;
      const inbox = acceptedProjects.find((project) => project.metadata.role === "inbox");
      if (!inbox) return Effect.fail(new Error("task inbox project is not available"));
      const draft = { title };
      const baselineUIDs = new Set(acceptedSnapshot?.issues.map((issue) => issue.uid) ?? []);
      return runViewTaskOrThrow(
        runAuthorityMutation(
          taskAPI.createIssue(inbox.id, actor, draft, acceptedMutationOptions()),
          { evidence: issueCreateMutationEvidence(inbox.uid, draft, baselineUIDs) },
        ).pipe(Effect.asVoid),
      );
    });
  }

  function switchKataDaemon(id: string): Effect.Effect<void, never, AppServices> {
    return Effect.suspend(() => {
      if ((id === activeKataDaemonId && acceptedSnapshot) || switchingDaemon) return Effect.void;
      const sourceViewName = currentViewName;
      const sourceFilters = structuredClone(searchFilters);
      const sourceListMode = listMode;
      const sourceGraphSourceIssue = graphSourceIssue;
      const sourceGraphAuthoritySourceUID = graphAuthoritySourceUID;
      const sourceAppliedGraphSourceUID = appliedGraphSourceUID;
      const persisted = loadKataWorkspaceState(id);
      let targetViewName = persisted?.view ?? "all";
      let targetFilters = persisted?.filters ?? defaultKataTaskSearchFilters(targetViewName);
      let targetSelectedIssueUID = persisted?.selectedIssueUID ?? acceptedSelectedIssue?.issue.uid ?? null;
      const generation = beginNavigation();
      replaceNextSelectionScopeUID = null;
      supersededRouteSelectionUID = selectedIssueUID?.trim() || acceptedSelectedIssue?.issue.uid || null;
      closeReachableGraph(false);
      resetDetailDrafts();
      currentViewName = targetViewName;
      searchFilters = targetFilters;
      switchingDaemon = true;
      workspaceOwnershipPending = true;
      viewError = null;
      let switched = false;
      const program = Effect.gen(function* () {
        const initialLoad = authorityController.load(kataWorkspaceAuthorityRequest({
          daemonID: id,
          view: targetViewName,
          filters: targetFilters,
          selectedIssueUID: targetSelectedIssueUID,
        }));
        const accepted = yield* initialLoad.pipe(
          Effect.catch((switchError) => {
            if (
              !(switchError instanceof KataSnapshotAPIError) ||
              switchError.code !== "projectNotFound" ||
              persisted?.filters.scope.kind !== "project"
            ) return Effect.fail(switchError);
            targetViewName = "all";
            targetFilters = defaultKataTaskSearchFilters();
            targetSelectedIssueUID = null;
            currentViewName = targetViewName;
            searchFilters = targetFilters;
            return authorityController.load(kataWorkspaceAuthorityRequest({
              daemonID: id,
              view: targetViewName,
              filters: targetFilters,
            }));
          }),
        );
        if (!accepted || !isCurrentNavigation(generation)) return;
        supersededRouteSelectionUID = null;
        const targetScopeUID = targetFilters.scope.kind === "project" ? targetFilters.scope.project_uid : null;
        onRouteStateChange?.(
          {
            ...canonicalRoute(
              targetViewName,
              targetScopeUID,
              acceptedSelectedIssue?.issue.uid ?? null,
              true,
            ),
            daemon: null,
          },
          { replace: true },
        );
        switched = true;
      });
      return program.pipe(
        Effect.catch((switchError) =>
          Effect.sync(() => {
            error = kataRequestErrorMessage(switchError);
            authorityStore.abandon(error);
            showFlash(error, { tone: "danger" });
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            if (!switched && isCurrentNavigation(generation)) {
              currentViewName = sourceViewName;
              searchFilters = sourceFilters;
              listMode = sourceListMode;
              graphSourceIssue = sourceGraphSourceIssue;
              graphAuthoritySourceUID = sourceGraphAuthoritySourceUID;
              appliedGraphSourceUID = sourceAppliedGraphSourceUID;
              supersededRouteSelectionUID = null;
            }
            if (acceptedSnapshot) workspaceOwnershipPending = false;
            switchingDaemon = false;
            if (switched) persistActiveWorkspaceState();
          }),
        ),
      );
    });
  }

  function resetDetailDrafts(): void {
    checklistRevealed = false;
    recurrenceDialogs?.closeAll();
  }

  function selectIssue(uid: string, notify = true, direct = notify): Effect.Effect<boolean, never, AppServices> {
    return Effect.gen(function* () {
      const replaceRoute =
        searchFilters.scope.kind === "project" && replaceNextSelectionScopeUID === searchFilters.scope.project_uid;
      replaceNextSelectionScopeUID = null;
      const generation = beginNavigation();
      const rowSelection = { uid, notify, direct, replaceRoute };
      rowNavigationSelection = rowSelection;
      resetDetailDrafts();
      const ok = yield* runViewTask(
        authorityController.load(kataWorkspaceAuthorityRequest({
          daemonID: activeKataDaemonId,
          view: acceptedCurrentView.name,
          filters: searchFilters,
          selectedIssueUID: uid,
          graphSourceUID: graphSourceIssue?.uid,
        })),
        "view",
      );
      if (rowNavigationSelection === rowSelection) rowNavigationSelection = null;
      if (!ok || !isCurrentNavigation(generation)) {
        if (
          !ok &&
          isCurrentNavigation(generation) &&
          authorityStore.state.phase === "degraded" &&
          authorityStore.state.intent.selected_issue_uid === uid
        ) {
          pendingRecoveredSelection = rowSelection;
        }
        return false;
      }
      if (notify) {
        if (replaceRoute && onRouteStateChange) onRouteStateChange({ issue: uid }, { replace: true });
        else onSelectedIssueChange?.(uid);
      }
      if (direct) persistActiveWorkspaceState();
      return true;
    });
  }

  function selectReachableGraphIssue(uid: string): void {
    appRuntime.runCommand(selectIssue(uid, true, true), {
      operation: "select Kata graph task",
      safeContext: { issueUid: uid },
      onFailure: () => {},
    });
  }

  // Resolves a link target's current identity through an isolated all-status
  // read pinned to the active daemon, for peers whose catalog identity moved
  // (closed/reopened or changed project) between enrichment and the click.
  function resolveCurrentLinkTarget(uid: string): Effect.Effect<KataIssueNavigationTarget | null, never, AppServices> {
    return Effect.gen(function* () {
      const store = createKataAuthorityStore();
      const daemonID = activeKataDaemonId;
      const intent: KataSnapshotIntent = {
        ...(daemonID ? { daemon_id: daemonID } : {}),
        scope: "global",
        authority: "all",
        selected_issue_uid: uid,
      };
      const accepted = yield* Effect.gen(function* () {
          const workflow = yield* KataWorkflow;
          return yield* workflow.latestSnapshot(
            "kata-link-resolution",
            store,
            intent,
            fetchKataWorkspaceSnapshot(intent),
          );
        });
      const detail = store.snapshot?.selected_detail;
      if (!accepted || store.snapshot?.selected_issue_uid !== uid || !detail) return null;
      return { uid, status: detail.issue.status, project_uid: detail.issue.project_uid };
    }).pipe(Effect.catch(() => Effect.succeed(null)));
  }

  function selectLinkedIssue(
    target: KataIssueNavigationTarget,
    allowRetry = true,
  ): Effect.Effect<boolean, never, AppServices> {
    return Effect.gen(function* () {
    // Members of the current authority select in place. Off-authority peers
    // (closed or cross-project links) carry their full identity, so route to
    // the authority that contains them instead of requesting a non-member
    // selection the server would refuse.
    if (acceptedSnapshot?.member_issue_uid_set.has(target.uid)) {
      return yield* selectIssue(target.uid);
    }
    replaceNextSelectionScopeUID = null;
    const generation = beginNavigation();
    closeReachableGraph(false);
    resetDetailDrafts();
    const rowSelection = { uid: target.uid, notify: false, direct: false, replaceRoute: false };
    rowNavigationSelection = rowSelection;
    const viewName: KataTaskViewName = target.status === "closed" ? "logbook" : "all";
    currentViewName = viewName;
    searchFilters = {
      ...defaultKataTaskSearchFilters(viewName),
      scope: { kind: "project", project_uid: target.project_uid },
    };
    const accepted = yield* runViewTask(
      authorityController.load(kataWorkspaceAuthorityRequest({
        daemonID: activeKataDaemonId,
        view: viewName,
        filters: searchFilters,
        selectedIssueUID: target.uid,
      })),
      "view",
    );
    if (rowNavigationSelection === rowSelection) rowNavigationSelection = null;
    if (!accepted || !isCurrentNavigation(generation)) return false;
    if (authorityStore.snapshot?.selected_issue_uid !== target.uid) {
      // The peer's identity moved after enrichment, so the requested
      // authority no longer contains it. Re-resolve its current identity and
      // retry once with fresh status/project — unless the user has navigated
      // elsewhere while the lookup was pending. On definitive failure, push
      // the selection-less authority route so the URL matches the view while
      // the source task stays one history entry back.
      const failRecovery = (): void => {
        onRouteStateChange?.(canonicalRoute(viewName, target.project_uid, null, true));
        persistActiveWorkspaceState();
      };
      if (!allowRetry) {
        failRecovery();
        return false;
      }
      const current = yield* resolveCurrentLinkTarget(target.uid);
      if (!isCurrentNavigation(generation)) return false;
      if (!current || (current.status === target.status && current.project_uid === target.project_uid)) {
        failRecovery();
        return false;
      }
      return yield* selectLinkedIssue(current, false);
    }
    onRouteStateChange?.(canonicalRoute(viewName, target.project_uid, target.uid, true));
    persistActiveWorkspaceState();
    return true;
    });
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

  function retryAcceptedEnrichment(): Effect.Effect<boolean, never, AppServices> {
    return runViewTask(authorityController.retry(), "view");
  }

  function moveSelectedIssue(toProjectUID: string): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() => {
      const selected = acceptedSelectedIssue?.issue;
      if (mutationActionsBlocked || !selected || !toProjectUID || pendingMoveIssueUIDs.has(selected.uid)) {
        return Effect.succeed(false);
      }
      const sourceIssueUID = selected.uid;
      const generation = navigationGeneration;
      pendingMoveIssueUIDs = new SvelteSet(pendingMoveIssueUIDs).add(sourceIssueUID);
      return runViewTask(
        runAuthorityMutation(
          taskAPI.moveIssue(
            selectedMutationTarget(sourceIssueUID),
            actor,
            toProjectUID,
            selectedMutationETag(sourceIssueUID),
            acceptedMutationOptions(),
          ),
          { evidence: moveMutationEvidence(sourceIssueUID, toProjectUID) },
        ),
        "flash",
        () => isCurrentNavigation(generation),
      ).pipe(
        Effect.ensuring(
          Effect.sync(() => {
            const nextPendingMoves = new SvelteSet(pendingMoveIssueUIDs);
            nextPendingMoves.delete(sourceIssueUID);
            pendingMoveIssueUIDs = nextPendingMoves;
          }),
        ),
      );
    });
  }

  function patchSelectedMetadata(uid: string, patch: Record<string, unknown>): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.patchIssueMetadata(
                selectedMutationTarget(uid),
                actor,
                patch,
                selectedMutationETag(uid),
                { daemonId: acceptedDaemonIDForMutation() },
              ),
              { evidence: metadataMutationEvidence(uid, patch) },
            ),
            "flash",
          ),
    );
  }

  function addSelectedComment(uid: string, body: string): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() => {
      if (mutationActionsBlocked) return Effect.succeed(false);
      const priorMatches = acceptedSelectedIssue?.comments.filter(
        (comment) => comment.author === actor && comment.body === body,
      ).length ?? 0;
      return runViewTask(
        runAuthorityMutation(
          taskAPI.addComment(selectedMutationTarget(uid), actor, body, acceptedMutationOptions()),
          { evidence: commentMutationEvidence(uid, actor, body, priorMatches) },
        ),
        "flash",
      );
    });
  }

  function editSelectedIssue(uid: string, patch: KataTaskEditPatch): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.editIssue(selectedMutationTarget(uid), actor, patch, acceptedMutationOptions()),
              { evidence: editMutationEvidence(uid, patch) },
            ),
            "flash",
          ),
    );
  }

  function assignSelectedOwner(uid: string, owner: string): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.assignOwner(selectedMutationTarget(uid), actor, owner, acceptedMutationOptions()),
              { evidence: ownerMutationEvidence(uid, owner) },
            ),
            "flash",
          ),
    );
  }

  function unassignSelectedOwner(uid: string): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.unassignOwner(selectedMutationTarget(uid), actor, acceptedMutationOptions()),
              { evidence: ownerMutationEvidence(uid, undefined) },
            ),
            "flash",
          ),
    );
  }

  function setSelectedPriority(uid: string, priority: number | null): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.setPriority(selectedMutationTarget(uid), actor, priority, acceptedMutationOptions()),
              { evidence: priorityMutationEvidence(uid, priority) },
            ),
            "flash",
          ),
    );
  }

  function addSelectedLabel(uid: string, label: string): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.addLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions()),
              { evidence: labelMutationEvidence(uid, label, true) },
            ),
            "flash",
          ),
    );
  }

  function removeSelectedLabel(uid: string, label: string): Effect.Effect<boolean, never, AppServices> {
    return Effect.suspend(() =>
      mutationActionsBlocked
        ? Effect.succeed(false)
        : runViewTask(
            runAuthorityMutation(
              taskAPI.removeLabel(selectedMutationTarget(uid), actor, label, acceptedMutationOptions()),
              { evidence: labelMutationEvidence(uid, label, false) },
            ),
            "flash",
          ),
    );
  }

  function openWorkspace(id: string): void {
    navigate(`/terminal/${encodeURIComponent(id)}`);
  }

  function createWorkspaceForSelectedIssue(
    launchTargetKey?: string,
  ): Effect.Effect<void, never, AppServices> {
    return Effect.suspend(() => {
      const selected = acceptedSelectedIssue?.issue;
      const itemIdentity = workspaceItemIdentity;
      if (mutationActionsBlocked || !selected || itemIdentity === null || workspaceActionBusy) return Effect.void;
      const daemonID = acceptedSnapshot?.daemon_id ?? "";
      const issueUID = selected.uid;
      return KataWorkspaceCreationWorkflow.pipe(
        Effect.flatMap((workflow) =>
          workflow.submit({
            purpose: kataWorkspaceIdentityFromIssue(selected, daemonID, projectNameForIssue(selected)),
            itemIdentity,
            ...(launchTargetKey ? { launchTargetKey } : {}),
            presentation: {
              isCurrent: () => workspaceSurfaceMounted && acceptedSelectedIssue?.issue.uid === issueUID,
              navigate: (workspaceID) => Effect.sync(() => openWorkspace(workspaceID)),
            },
          }),
        ),
      );
    });
  }

  function selectedWorkspaceAction() {
    if (!workspaceTarget?.available) return undefined;
    if (effectiveWorkspaceRef) {
      const id = effectiveWorkspaceRef.id;
      return {
        label: "Open workspace",
        onClick: () => openWorkspace(id),
      };
    }
    return {
      label: "Create workspace",
      busy: workspaceActionBusy,
      disabled: mutationActionsBlocked,
      launchTargets: settings.getLaunchTargets(),
      onCreate: createWorkspaceForSelectedIssue,
    };
  }

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  function recurrenceUncertainty(
    projectID: number,
    family: string,
    operation: string,
    target: string,
    baseline: readonly KataRecurrence[],
    isApplied: (recurrences: readonly KataRecurrence[]) => boolean,
  ): KataCustomMutationUncertainty {
    const daemonId = acceptedDaemonIDForMutation();
    return {
      identity: kataMutationIdentity(daemonId, family, operation, target),
      reconcile: reconcileRecurrenceMutation(
        baseline,
        Effect.suspend(() => taskAPI.recurrences(projectID, { daemonId })),
        isApplied,
      ),
    };
  }

  function createRecurrence(
    projectID: number,
    input: KataCreateRecurrenceInput,
  ): Effect.Effect<void, unknown, AppServices> {
    if (mutationActionsBlocked) return Effect.void;
    const baseline = selectedRecurrences;
    const baselineUIDs = new SvelteSet(baseline.map((recurrence) => recurrence.uid));
    return runViewTaskOrThrow(
      runAuthorityMutation(
        taskAPI.createRecurrence(projectID, input, acceptedMutationOptions()),
        {
          refreshRecurrences: true,
          uncertainty: recurrenceUncertainty(
            projectID,
            "recurrence-create",
            "create Kata recurrence",
            `${projectID}:${input.rrule}:${input.dtstart}:${input.timezone}:${input.template.title}`,
            baseline,
            (recurrences) =>
              recurrences.filter(
                (recurrence) => !baselineUIDs.has(recurrence.uid) && recurrenceCreateMatches(recurrence, input),
              ).length === 1,
          ),
        },
      ).pipe(Effect.asVoid),
      "none",
    );
  }

  function patchRecurrence(
    id: number,
    input: KataPatchRecurrenceInput,
    etag: string,
  ): Effect.Effect<void, unknown, AppServices> {
    if (mutationActionsBlocked) return Effect.void;
    return runViewTaskOrThrow(Effect.suspend(() => {
      const recurrence = selectedRecurrences.find((item) => item.id === id);
      if (!recurrence) return Effect.fail(new Error(`recurrence not loaded: id=${id}`));
      return runAuthorityMutation(
        taskAPI.patchRecurrence(
          recurrence.project_id,
          recurrence.uid,
          input,
          etag,
          acceptedMutationOptions(),
        ),
        {
          refreshRecurrences: true,
          uncertainty: recurrenceUncertainty(
            recurrence.project_id,
            "recurrence-patch",
            "edit Kata recurrence",
            recurrence.uid,
            selectedRecurrences,
            (recurrences) => {
              const fresh = recurrences.find((candidate) => candidate.uid === recurrence.uid);
              return fresh !== undefined && fresh.revision > recurrence.revision && recurrencePatchMatches(fresh, input);
            },
          ),
        },
      ).pipe(Effect.asVoid);
    }), "none");
  }

  function deleteRecurrence(recurrence: KataRecurrence): Effect.Effect<boolean, never, AppServices> {
    if (mutationActionsBlocked) return Effect.succeed(false);
    return runViewTask(
      runAuthorityMutation(
          taskAPI.deleteRecurrence(
            recurrence.project_id,
            recurrence.uid,
            actor,
            acceptedMutationOptions(),
            `"rev-${recurrence.revision}"`,
          ),
          {
            refreshRecurrences: true,
            uncertainty: recurrenceUncertainty(
              recurrence.project_id,
              "recurrence-delete",
              "delete Kata recurrence",
              recurrence.uid,
              selectedRecurrences,
              (recurrences) => recurrences.every((candidate) => candidate.uid !== recurrence.uid),
            ),
          },
        ).pipe(
          Effect.catch((error) => {
        // A revision conflict means another client changed this recurrence;
        // reload the list and wait for the winning reload to reconcile the
        // open dialog. If reconciliation fails, fence that stale dialog until
        // a retry loads the current revision.
            if (!hasHTTPStatus(error, 412) || !acceptedSnapshot) return Effect.fail(error);
            return loadSelectedRecurrences(acceptedSnapshot).pipe(
              Effect.tap((refreshed) =>
                Effect.sync(() => {
                  if (!refreshed) beginRecurrenceConflictRecovery();
                }),
              ),
              Effect.andThen(Effect.fail(error)),
            );
          }),
        ),
      "flash",
    );
  }

  function closeSelectedIssue(
    reason: "done" | "wontfix" | "duplicate" | "superseded",
    message: string,
  ): Effect.Effect<boolean, never, AppServices> {
    const selected = acceptedSelectedIssue;
    if (mutationActionsBlocked || !selected) return Effect.succeed(false);
    return runViewTask(
        runAuthorityMutation(
          taskAPI.closeIssue(
            selectedMutationTarget(selected.issue.uid),
            actor,
            { reason, message },
            acceptedMutationOptions(),
          ),
          { evidence: statusMutationEvidence(selected.issue.uid, "closed", reason) },
        ),
      "flash",
    );
  }

  function reopenSelectedIssue(): Effect.Effect<boolean, never, AppServices> {
    const selected = acceptedSelectedIssue;
    if (mutationActionsBlocked || !selected) return Effect.succeed(false);
    return runViewTask(
      runAuthorityMutation(
        taskAPI.reopenIssue(selectedMutationTarget(selected.issue.uid), actor, acceptedMutationOptions()),
        { evidence: statusMutationEvidence(selected.issue.uid, "open") },
      ),
      "flash",
    );
  }

  function deleteSelectedIssue(): Effect.Effect<boolean, never, AppServices> {
    return closeSelectedIssue("wontfix", "Deleted from issue detail.");
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
            runNavigation(switchKataDaemon(id), "switch Kata daemon", { daemonId: id });
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
        disabled={mutationActionsBlocked}
        onclick={() => { if (!mutationActionsBlocked) captureOpen = true; }}
      >
        <PlusIcon size={13} strokeWidth={1.9} aria-hidden="true" />
        <span>New task</span>
      </button>
    </div>
  </header>

  {#if mutationRefreshPending && (mutationAcknowledged || recurrenceConflictRecoveryPending) && !mutationRefreshError}
    <section class="kata-authority-recovery" role="status">
      <span>
        {recurrenceConflictRecoveryPending
          ? "Refreshing the current recurrence revision…"
          : "Change saved. Refreshing Kata snapshot…"}
      </span>
    </section>
  {:else if authorityRecoveryMessage}
    <section class="kata-authority-recovery" role="alert">
      <span>{authorityRecoveryMessage}</span>
      <button type="button" disabled={authorityRetrying} onclick={retryAuthoritySnapshot}>
        {authorityRetrying ? "Retrying…" : mutationPartialOutcome ? "Acknowledge partial change" : "Retry Kata snapshot"}
      </button>
    </section>
  {/if}

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
      onOpenView={(name) => openRoutedSystemView(name, true)}
      onOpenProject={(projectUID) => openRoutedProjectScope(projectUID, true)}
      onCreateProject={createKataProject}
    />

    <main class="kata-main" aria-label="Kata tasks">
      {#if viewError && !authorityRecoveryMessage}
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
            <button
              type="button"
              onclick={() => runNavigation(retryAcceptedEnrichment(), "retry Kata graph enrichment")}
            >Retry graph</button>
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
      {#key activeKataDaemonId ?? ""}
        <KataIssueList
          currentView={acceptedCurrentView}
          issueCatalog={acceptedIssueCatalog}
          scopeLabel={listTitle()}
          scopedProjectName={selectedProjectName()}
          selectedIssueUID={acceptedSelectedIssue?.issue.uid ?? null}
          loading={viewLoading}
          statusFilter={searchFilters.status}
          readyIssueUIDs={acceptedReadyIssueUIDs}
          resetGeneration={listResetGeneration}
          navigationGeneration={navigationEpoch}
          {revealRequest}
          onSelect={(issue) => {
            runNavigation(selectIssue(issue.uid), "select Kata task", { issueUid: issue.uid });
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
      searchReferences={searchReferences ?? runtimeTaskReferenceSearch}
      activeDaemonId={activeKataDaemonId}
      {linkFilters}
      onLinkFiltersChange={(next) => {
        linkFilters = next;
      }}
      projects={acceptedProjects}
      ownerOptions={ownerOptions()}
      {selectedRecurrences}
      {checklistRevealed}
      actionsDisabled={mutationActionsBlocked}
      authorityBlocked={detailAuthorityBlocked}
      draftResetGeneration={mutationDraftResetGeneration}
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
      onRevealChecklist={revealChecklist}
      onCreateRecurrence={() => recurrenceDialogs?.openCreateRecurrence()}
      onEditRecurrence={(recurrence) => recurrenceDialogs?.openEditRecurrence(recurrence)}
      onDeleteRecurrence={(recurrence) => recurrenceDialogs?.openDeleteRecurrence(recurrence)}
      onCloseIssue={closeSelectedIssue}
      onReopenIssue={reopenSelectedIssue}
      onDeleteIssue={deleteSelectedIssue}
      onSelectIssue={(target) =>
        runNavigation(selectLinkedIssue(target), "open linked Kata task", { issueUid: target.uid })}
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
  disabled={mutationActionsBlocked}
  onClose={() => { captureOpen = false; }}
  onSubmit={submitQuickCapture}
/>

<KataRecurrenceDialogs
  bind:this={recurrenceDialogs}
  selectedIssue={acceptedSelectedIssue}
  {actor}
  disabled={mutationActionsBlocked}
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

  .kata-authority-recovery {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    border-bottom: 1px solid var(--border-default);
    background: color-mix(in srgb, var(--accent-amber) 10%, var(--bg-primary));
    padding: var(--space-3) var(--space-5);
    color: var(--text-primary);
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
