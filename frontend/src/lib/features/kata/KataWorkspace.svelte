<script lang="ts">
  import { onMount, untrack } from "svelte";
  import LayoutPanelLeftIcon from "@lucide/svelte/icons/layout-panel-left";
  import LayoutPanelTopIcon from "@lucide/svelte/icons/layout-panel-top";
  import PlusIcon from "@lucide/svelte/icons/plus";

  import { fetchKataDaemons, type KataDaemonInfo } from "../../api/kata/daemons.js";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataProjectSummary,
    KataRecurrence,
    KataTaskAPI,
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
    setActiveKataDaemon,
    setKataDaemonRoster,
  } from "../../stores/active-kata-daemon.svelte.js";
  import { createKataWorkspaceStore } from "../../stores/kata-workspace.svelte.js";
  import KataDaemonSwitcher from "./KataDaemonSwitcher.svelte";
  import KataRecurrenceDialogs from "./KataRecurrenceDialogs.svelte";
  import KataSearchPanel from "./KataSearchPanel.svelte";
  import { createKataEventStreamController } from "./kataEventStreamController.js";

  interface Props {
    api?: KataTaskAPI | undefined;
    selectedIssueUID?: string | null | undefined;
    routeViewName?: KataTaskViewName | null | undefined;
    routeScopeUID?: string | null | undefined;
    onSelectedIssueChange?: ((uid: string | null) => void) | undefined;
    onRouteStateChange?: (
      (state: { issue?: string | null; view?: KataTaskViewName | null; scope?: string | null }) => void
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
  type KataConnectionTone = "offline" | "connecting" | "online" | "error";

  let {
    api = undefined,
    selectedIssueUID = null,
    routeViewName = null,
    routeScopeUID = null,
    onSelectedIssueChange = undefined,
    onRouteStateChange = undefined,
    onOpenMessage = undefined,
  }: Props = $props();

  let loading = $state(true);
  let viewLoading = $state(false);
  let viewLoadingGeneration = 0;
  let error = $state<string | null>(null);
  let unlinkBusyIds = $state<ReadonlySet<number>>(new Set());
  let unlinkError = $state<string | null>(null);
  let daemonInfos = $state.raw<KataDaemonInfo[]>([]);
  let switchingDaemon = $state(false);
  let captureOpen = $state(false);
  let listResetGeneration = $state(0);
  let checklistRevealed = $state(false);
  let recurrenceDialogs = $state<KataRecurrenceDialogController | null>(null);
  const store = createKataWorkspaceStore({ api: untrack(() => api) });
  const actor = "middleman";
  let syncedRouteIssueUID = $state<string | null>(null);
  let syncedRouteViewName: KataTaskViewName | null = null;
  let syncedRouteScopeUID: string | null = null;
  let navigationGeneration = 0;
  // Reactive shadow of navigationGeneration so the issue list can drop
  // a pending keyboard selection the moment any navigation starts —
  // the list only remounts after the new view's data arrives, which is
  // too late for a selection released mid-transition.
  let navigationEpoch = $state(0);
  let observedRouteSignature = "";
  const layoutStorageKey = "middleman:kata:task-layout/v1";
  const defaultSplitSizes: Record<SplitOrientation, number> = {
    vertical: 420,
    horizontal: 520,
  };
  let splitOrientation = $state<SplitOrientation>("vertical");
  let splitSizes = $state<Record<SplitOrientation, number>>({ ...defaultSplitSizes });
  const activeSplitSize = $derived(splitSizes[splitOrientation]);
  const activeKataDaemonId = $derived(
    getActiveKataDaemon() ??
      getDefaultKataDaemon() ??
      daemonInfos.find((daemon) => daemon.default)?.id ??
      daemonInfos[0]?.id,
  );
  const eventStream = createKataEventStreamController({
    getDaemonId: () => activeKataDaemonId,
    getLastEventID: () => store.eventCursor,
    onOpen: () => {
      store.connection = { status: "online" };
    },
    onMessage: async (message) => {
      await store.applyEventStreamMessage(message);
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
    viewLoading = true;
    return generation;
  }

  function endViewLoading(generation: number): void {
    if (generation !== viewLoadingGeneration) return;
    viewLoading = false;
  }

  async function runViewTask(task: () => Promise<void | boolean>): Promise<boolean> {
    const loadingGeneration = beginViewLoading();
    error = null;
    const expansionSignature = currentExpansionSignature();
    try {
      const ok = (await task()) ?? true;
      if (ok && currentExpansionSignature() !== expansionSignature) {
        resetIssueExpansion();
      }
      return ok;
    } catch (err) {
      error = err instanceof Error ? err.message : "Kata request failed.";
      return false;
    } finally {
      endViewLoading(loadingGeneration);
    }
  }

  async function runViewTaskOrThrow(task: () => Promise<void>): Promise<void> {
    const loadingGeneration = beginViewLoading();
    error = null;
    try {
      await task();
    } catch (err) {
      error = err instanceof Error ? err.message : "Kata request failed.";
      throw err;
    } finally {
      endViewLoading(loadingGeneration);
    }
  }

  onMount(() => {
    let cancelled = false;
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
        const bootstrapRoute = currentRouteSnapshot();
        observedRouteSignature = routeSignature(bootstrapRoute);
        const bootstrapViewName = bootstrapRoute.view ?? "all";
        const bootstrapIssueUID = bootstrapRoute.issue;
        await store.bootstrap(bootstrapViewName, bootstrapIssueUID, { selectFirst: bootstrapIssueUID !== null });
        if (bootstrapRoute.view || bootstrapRoute.scope) {
          await applyRouteViewScope(bootstrapRoute.view, bootstrapRoute.scope, bootstrapRoute.issue);
        }
        await store.syncEventCursor();
        syncedRouteIssueUID =
          bootstrapRoute.issue && store.selectedIssue?.issue.uid === bootstrapRoute.issue ? bootstrapRoute.issue : null;
        syncedRouteViewName = bootstrapRoute.view;
        syncedRouteScopeUID = bootstrapRoute.scope;
        if (!cancelled) {
          startEventStream();
        }
      } catch (err) {
        if (!cancelled) {
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

  async function applyRouteViewScope(
    viewName: KataTaskViewName | null,
    scopeUID: string | null,
    issueUID: string | null,
  ): Promise<void> {
    const generation = beginNavigation();
    store.invalidatePendingLoads();
    resetDetailDrafts();
    store.resetSearchFilters();
    const shouldApplyRouteLoad = () =>
      isCurrentNavigation(generation) &&
      (routeViewName ?? null) === viewName &&
      (routeScopeUID ?? null) === scopeUID;
    const routeLoadOptions = { shouldApply: shouldApplyRouteLoad, selectFirst: issueUID !== null };
    if (scopeUID) {
      await store.updateSearchFilters(
        { scope: { kind: "project", project_uid: scopeUID } },
        routeLoadOptions,
      );
      if (viewName) {
        await store.openView(viewName, routeLoadOptions);
      }
    } else {
      if (viewName) {
        await store.openView(viewName, routeLoadOptions);
      } else if (store.currentView.name !== "all") {
        await store.openView("all", routeLoadOptions);
      }
    }
    if (!isCurrentNavigation(generation)) return;
    if (issueUID) {
      await store.selectIssue(issueUID);
    } else {
      store.clearSelection();
    }
    if (!isCurrentNavigation(generation)) return;
    syncedRouteViewName = viewName;
    syncedRouteScopeUID = scopeUID;
    syncedRouteIssueUID = issueUID && store.selectedIssue?.issue.uid === issueUID ? issueUID : null;
  }

  $effect(() => {
    const uid = selectedIssueUID ?? null;
    if (loading) return;
    if ((routeViewName ?? null) !== syncedRouteViewName || (routeScopeUID ?? null) !== syncedRouteScopeUID) return;
    if (!uid) {
      if (syncedRouteIssueUID === null) return;
      resetDetailDrafts();
      store.clearSelection();
      syncedRouteIssueUID = null;
      return;
    }
    if (uid === syncedRouteIssueUID) return;
    if (store.selectedIssue?.issue.uid === uid) {
      syncedRouteIssueUID = uid;
      return;
    }
    void selectIssue(uid, false);
  });

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

  $effect.pre(() => {
    if (loading) return;
    const snapshot = currentRouteSnapshot();
    const signature = routeSignature(snapshot);
    if (signature === observedRouteSignature) return;
    observedRouteSignature = signature;
    beginNavigation();
    store.invalidatePendingLoads();
  });

  $effect(() => {
    const viewName = routeViewName ?? null;
    const scopeUID = routeScopeUID ?? null;
    const issueUID = selectedIssueUID ?? null;
    if (loading) return;
    if (viewName === syncedRouteViewName && scopeUID === syncedRouteScopeUID) return;
    void untrack(() => runViewTask(() => applyRouteViewScope(viewName, scopeUID, issueUID)));
  });

  function selectedProjectName(): string | null {
    const scope = store.searchFilters.scope;
    if (scope.kind !== "project") return null;
    return store.projects.find((project) => project.uid === scope.project_uid)?.name ?? null;
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

  function activeDaemonLabel(): string {
    return activeKataDaemonId ?? "Kata daemon";
  }

  function connectionLabel(): string {
    if (error) return error;
    if (store.connection.status === "error") return store.connection.message ?? "Connection failed";
    if (loading || store.connection.status === "connecting") return `Connecting - ${activeDaemonLabel()}`;
    return "Connected";
  }

  function connectionTitle(): string {
    if (error) return error;
    if (store.connection.status === "error") return store.connection.message ?? "Connection failed";
    if (store.connection.status === "online") return `Connected - ${activeDaemonLabel()}`;
    if (loading || store.connection.status === "connecting") return `Connecting - ${activeDaemonLabel()}`;
    return activeDaemonLabel();
  }

  function showVisibleConnectionStatus(): boolean {
    const tone = connectionTone();
    return tone === "connecting" || tone === "error";
  }

  function connectionTone(): KataConnectionTone {
    if (error) return "error";
    if (loading && store.connection.status !== "error") return "connecting";
    return store.connection.status;
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
    const generation = beginNavigation();
    resetDetailDrafts();
    // Same rationale as openRoutedProjectScope: a pending detail load is
    // abandoned by the filter reload, so drop it before awaiting.
    store.invalidatePendingLoads();
    await runViewTask(() => store.updateSearchFilters(filters));
    if (!isCurrentNavigation(generation)) return;
    const nextScopeUID = scopeUIDFromFilters(store.searchFilters);
    if (nextScopeUID !== syncedRouteScopeUID) {
      const nextViewName = nextScopeUID ? null : store.currentView.name === "all" ? null : store.currentView.name;
      syncedRouteViewName = nextViewName;
      syncedRouteScopeUID = nextScopeUID;
      onRouteStateChange?.({
        view: nextViewName,
        scope: nextScopeUID,
        issue: store.selectedIssue?.issue.uid ?? null,
      });
    }
  }

  async function openRoutedSystemView(viewName: KataTaskViewName): Promise<void> {
    const generation = beginNavigation();
    resetDetailDrafts();
    store.resetSearchFilters();
    // Clear (and thereby abort) the abandoned selection before awaiting
    // the new view: while that fetch is in flight, a still-running
    // detail load could fail and surface a stale error for a selection
    // this navigation has already discarded.
    store.clearSelection();
    await runViewTask(() => store.openView(viewName, { selectFirst: false }));
    if (!isCurrentNavigation(generation)) return;
    syncedRouteViewName = viewName;
    syncedRouteScopeUID = null;
    syncedRouteIssueUID = null;
    onRouteStateChange?.({
      view: viewName,
      scope: null,
      issue: null,
    });
  }

  async function openRoutedProjectScope(projectUID: string): Promise<void> {
    const generation = beginNavigation();
    resetDetailDrafts();
    // Scope changes keep a completed selection but abandon an in-flight
    // one (the scoped reload re-selects from selectedIssue, which a
    // pending load hasn't populated). Invalidate up front so the doomed
    // detail request can't fail into a stale workspace error while the
    // scoped list is still loading.
    store.invalidatePendingLoads();
    const ok = await runViewTask(() => store.updateSearchFilters({ scope: { kind: "project", project_uid: projectUID } }));
    if (!ok || !isCurrentNavigation(generation)) return;
    syncedRouteViewName = null;
    syncedRouteScopeUID = projectUID;
    onRouteStateChange?.({
      view: null,
      scope: projectUID,
      issue: store.selectedIssue?.issue.uid ?? null,
    });
  }

  function scheduleProjectScope(projectUID: string): void {
    void openRoutedProjectScope(projectUID);
  }

  async function createKataProject(name: string): Promise<KataProjectSummary> {
    return store.createProject(name);
  }

  async function renameKataProject(id: number, name: string): Promise<void> {
    await store.renameProject(id, name);
  }

  async function submitQuickCapture(title: string): Promise<void> {
    await runViewTaskOrThrow(async () => {
      resetDetailDrafts();
      await store.captureIssue(actor, { title });
    });
  }

  async function switchKataDaemon(id: string): Promise<void> {
    if (switchingDaemon) return;
    const previousExplicitDaemon = getActiveKataDaemon();
    if (id === activeKataDaemonId) return;

    const generation = beginNavigation();
    const previousView = store.currentView.name;
    const previousFilters = store.searchFilters;
    const previousIssueUID = store.selectedIssue?.issue.uid ?? null;
    switchingDaemon = true;
    resetDetailDrafts();
    resetIssueExpansion();
    // The switch abandons any in-flight detail load (only a completed
    // selection is captured for restore above), so drop it before the
    // daemon reload: its failure mid-switch would otherwise surface a
    // stale error from the previous daemon.
    store.invalidatePendingLoads();
    setActiveKataDaemon(id);
    stopEventStream();
    try {
      store.resetEventCursor();
      const ok = await runViewTask(async () => {
        await store.bootstrap(previousView);
        store.resetSearchFilters();
      });
      if (!ok) {
        setActiveKataDaemon(previousExplicitDaemon);
        const restored = await runViewTask(async () => {
          store.resetEventCursor();
          await store.bootstrap(previousView, previousIssueUID);
          await store.updateSearchFilters(previousFilters);
          if (store.currentView.name !== previousView) {
            await store.openView(previousView);
          }
          if (previousIssueUID && store.selectedIssue?.issue.uid !== previousIssueUID) {
            await store.selectIssue(previousIssueUID);
          }
        });
        if (restored) {
          await store.syncEventCursor();
        }
        startEventStream();
        return;
      }

      await store.syncEventCursor();
      if (!isCurrentNavigation(generation)) {
        startEventStream();
        return;
      }
      const nextUID = store.selectedIssue?.issue.uid ?? null;
      syncedRouteIssueUID = selectedIssueUID ?? null;
      onSelectedIssueChange?.(nextUID);
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
    const generation = beginNavigation();
    resetDetailDrafts();
    const ok = await runViewTask(() => store.selectIssue(uid));
    if (!ok || !isCurrentNavigation(generation)) return;
    if (!notify || selectedIssueUID === uid) {
      syncedRouteIssueUID = uid;
    }
    if (notify) onSelectedIssueChange?.(uid);
  }

  async function moveSelectedIssue(toProjectUID: string | null): Promise<void> {
    const selected = store.selectedIssue?.issue;
    if (!selected || !toProjectUID) return;
    await runViewTask(() => store.moveIssue(selected.uid, actor, toProjectUID));
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

  function revealChecklist(): void {
    checklistRevealed = true;
  }

  async function createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<void> {
    await runViewTaskOrThrow(async () => {
      await store.createRecurrence(projectID, input);
    });
  }

  async function patchRecurrence(id: number, input: KataPatchRecurrenceInput, etag: string): Promise<void> {
    await runViewTaskOrThrow(async () => {
      await store.patchRecurrence(id, input, etag);
    });
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
      const ok = await runViewTask(() => store.patchMetadata(uid, actor, metadataPatch));
      if (!ok) {
        unlinkError = error || "Could not unlink message.";
      }
    } finally {
      unlinkBusyIds = new Set();
    }
  }
</script>

<section class="kata-feature" aria-labelledby="kata-title">
  <header class="kata-header">
    <div class="kata-header-title">
      <h1 id="kata-title">Kata</h1>
      {#if daemonInfos.length > 1}
        <KataDaemonSwitcher
          daemons={daemonInfos}
          activeId={activeKataDaemonId}
          onSelect={(id) => {
            void switchKataDaemon(id);
          }}
        />
      {/if}
      {#if showVisibleConnectionStatus()}
        <span
          class="daemon-status"
          role="status"
          title={connectionTitle()}
          aria-label={`Connection: ${connectionTone()}`}
        >
          <span class={`daemon-status-dot daemon-status-dot--${connectionTone()}`} aria-hidden="true"></span>
          <span class="daemon-status-label">{connectionLabel()}</span>
        </span>
      {:else}
        <span class="daemon-status-sr" role="status" aria-label={`Connection: ${connectionTone()}`}>
          {connectionLabel()}
        </span>
      {/if}
    </div>
    <div class="kata-header-actions">
      <button
        type="button"
        class="icon-button"
        aria-label={splitOrientation === "vertical" ? "Switch to side-by-side layout" : "Switch to stacked layout"}
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
      </button>
      <button type="button" class="accent-button header-action" onclick={() => { captureOpen = true; }}>
        <PlusIcon size={13} strokeWidth={1.9} aria-hidden="true" />
        <span>New task</span>
      </button>
    </div>
  </header>

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
      onRenameProject={renameKataProject}
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
        resetGeneration={listResetGeneration}
        navigationGeneration={navigationEpoch}
        api={store.api}
        onSelect={(issue) => {
          void selectIssue(issue.uid);
        }}
      />
    {/key}
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
    gap: 10px;
    flex: 1 1 auto;
  }

  .kata-header h1 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 650;
    line-height: 1.2;
  }

  .kata-header-actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: 0 0 auto;
  }

  .header-action {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    white-space: nowrap;
  }

  .icon-button {
    width: 28px;
    height: 28px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    cursor: pointer;
  }

  .icon-button:hover,
  .icon-button:focus-visible {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    outline: none;
  }

  .daemon-status {
    min-width: 0;
    max-width: min(42vw, 460px);
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .daemon-status-dot {
    width: 8px;
    height: 8px;
    border-radius: var(--radius-pill);
    background: var(--text-faint);
    flex: 0 0 auto;
  }

  .daemon-status-dot--online {
    background: var(--accent-green);
  }

  .daemon-status-dot--connecting {
    background: var(--accent-amber);
  }

  .daemon-status-dot--error {
    background: var(--accent-red);
  }

  .daemon-status-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .daemon-status-dot--error + .daemon-status-label {
    color: var(--accent-red);
  }

  .daemon-status-sr {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
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
