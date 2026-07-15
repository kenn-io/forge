import { KataTaskAPIError, createKataTaskAPI } from "../api/kata/taskClient.js";
import type {
  KataCreateRecurrenceInput,
  KataPatchRecurrenceInput,
  KataProjectSummary,
  KataRecurrence,
  KataRecurrenceResponse,
  KataTaskCreateDraft,
  KataTaskCloseOptions,
  KataTaskAPI,
  KataDuplicateCandidateDisplay,
  KataTaskDetail,
  KataTaskEditPatch,
  KataTaskEvent,
  KataTaskEventStreamMessage,
  KataTaskEventsResponse,
  KataTaskIssuesQuery,
  KataTaskMetadataPatch,
  KataTaskMutationResponse,
  KataTaskMutationTarget,
  KataTaskSearchFilters,
  KataTaskSearchResponse,
  KataTaskSummary,
  KataTaskViewName,
  KataTaskViewResponse,
} from "../api/kata/taskTypes.js";
import { createULID } from "../api/ulid.js";
import { clearInteraction, markInteractionStart, measureInteraction } from "../instrumentation/interactionTiming.js";
import { recordKataGraphDebugEvent, setKataGraphDebugStore } from "./kata-graph-debug.js";

// User Timing interaction name for selecting a task: measures
// "kata:select-issue:detail-visible" (click to detail pane rendered) and
// "kata:select-issue:events-loaded" (click to events section populated).
export const KATA_SELECT_ISSUE_INTERACTION = "kata:select-issue";

export interface KataConnectionState {
  status: "offline" | "connecting" | "online" | "error";
  message?: string | undefined;
}

export interface KataAreaSummary {
  name: string;
  projects: KataProjectSummary[];
}

export interface KataCurrentView {
  name: KataTaskViewName;
  groups: KataTaskViewResponse["groups"];
  fetched_at?: string | undefined;
}

export interface CreateKataWorkspaceStoreOptions {
  api?: KataTaskAPI | undefined;
}

interface KataLoadOptions {
  shouldApply?: (() => boolean) | undefined;
  selectFirst?: boolean | undefined;
}

interface KataBootstrapOptions extends KataLoadOptions {}

interface KataRefreshOptions extends KataLoadOptions {
  refreshSelectedDetail?: boolean | undefined;
  eventDriven?: boolean | undefined;
}

export interface KataEventDeliveryOptions {
  // The workspace owns daemon identity, while the store owns cursor mutation.
  // This guard lets an abandoned daemon generation finish its request without
  // applying its response to the active daemon's shared store.
  shouldApply?: (() => boolean) | undefined;
}

function emptyView(name: KataTaskViewName = "today"): KataCurrentView {
  return { name, groups: [] };
}

export function defaultKataTaskSearchFilters(): KataTaskSearchFilters {
  return {
    scope: { kind: "all" },
    status: "open",
    owner: "",
    label: "",
    query: "",
  };
}

function groupSearchIssues(issues: KataTaskSummary[]): KataTaskViewResponse["groups"] {
  return issues.length > 0 ? [{ id: "search-results", title: "Results", issues }] : [];
}

function issueHierarchyKey(issue: KataTaskSummary): string {
  return `${issue.project_uid}:${issue.short_id}`;
}

function parentHierarchyKey(issue: KataTaskSummary): string | null {
  return issue.parent_short_id ? `${issue.project_uid}:${issue.parent_short_id}` : null;
}

function topLevelIssues(issues: readonly KataTaskSummary[], allIssues: readonly KataTaskSummary[]): KataTaskSummary[] {
  // Mirror the list view: a child is only folded into its parent when that
  // parent is present in the same result set. A search that returns a child
  // without its parent keeps the child as a selectable top-level row.
  const visibleKeys = new Set(allIssues.map(issueHierarchyKey));
  return issues.filter((issue) => {
    const parentKey = parentHierarchyKey(issue);
    return parentKey === null || !visibleKeys.has(parentKey);
  });
}

function selectableViewIssues(groups: KataTaskViewResponse["groups"]): KataTaskSummary[] {
  const allIssues = groups.flatMap((group) => group.issues);
  return groups.flatMap((group) => topLevelIssues(group.issues, allIssues));
}

function projectArea(project: KataProjectSummary): string {
  const area = project.metadata.area?.trim();
  return area && area !== "Unfiled" ? area : "Unfiled";
}

function compareProjectOrder(a: KataProjectSummary, b: KataProjectSummary): number {
  const ao = a.metadata.sidebar_order ?? Number.MAX_SAFE_INTEGER;
  const bo = b.metadata.sidebar_order ?? Number.MAX_SAFE_INTEGER;
  if (ao !== bo) return ao - bo;
  return a.name.localeCompare(b.name);
}

function isTaskInboxProject(project: KataProjectSummary): boolean {
  return project.metadata.role === "inbox";
}

export function deriveKataAreas(projects: KataProjectSummary[]): KataAreaSummary[] {
  const groups = new Map<string, KataProjectSummary[]>();
  for (const project of projects) {
    if (isTaskInboxProject(project)) continue;
    const area = projectArea(project);
    groups.set(area, [...(groups.get(area) ?? []), project]);
  }

  const preferred = ["Personal", "Work", "Unfiled"];
  return [...groups.entries()]
    .sort(([a], [b]) => {
      const ai = preferred.indexOf(a);
      const bi = preferred.indexOf(b);
      if (ai !== -1 || bi !== -1) {
        return (ai === -1 ? Number.MAX_SAFE_INTEGER : ai) - (bi === -1 ? Number.MAX_SAFE_INTEGER : bi);
      }
      return a.localeCompare(b);
    })
    .map(([name, areaProjects]) => ({
      name,
      projects: [...areaProjects].sort(compareProjectOrder),
    }));
}

function hasActiveSearchFilters(filters: KataTaskSearchFilters): boolean {
  if (filters.scope.kind === "project") return true;
  return hasNonViewFilters(filters);
}

function hasNonViewFilters(filters: KataTaskSearchFilters): boolean {
  return (
    filters.status !== "open" ||
    filters.owner.trim() !== "" ||
    filters.label.trim() !== "" ||
    filters.query.trim() !== ""
  );
}

function shouldRefreshViaSearch(filters: KataTaskSearchFilters, currentViewName: KataTaskViewName): boolean {
  if (hasNonViewFilters(filters)) return true;
  return isProjectBacklogScope(filters) && currentViewName === "all";
}

function isProjectBacklogScope(filters: KataTaskSearchFilters): boolean {
  return (
    filters.scope.kind === "project" &&
    filters.status === "open" &&
    filters.owner.trim() === "" &&
    filters.label.trim() === "" &&
    filters.query.trim() === ""
  );
}

function connectionErrorMessage(error: unknown): string {
  if (error instanceof KataTaskAPIError && (error.status === 401 || error.status === 403))
    return "Authentication required";
  return error instanceof Error ? error.message : "Could not load Kata";
}

function scopedIssueQuery(filters: KataTaskSearchFilters): Partial<KataTaskIssuesQuery> {
  return filters.scope.kind === "project" ? { project_uid: filters.scope.project_uid } : {};
}

function shouldApplyLoad(options: KataLoadOptions | undefined): boolean {
  return options?.shouldApply?.() ?? true;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function mergeKataPeerList(
  previous: KataTaskSummary["blocks"],
  next: KataTaskSummary["blocks"],
  hasNext: boolean,
): KataTaskSummary["blocks"] {
  if (hasNext) return next;
  return previous && previous.length > 0 ? [...previous] : next;
}

function hasOwnField<T extends object>(value: T, key: PropertyKey): boolean {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function mergeCachedTaskSummary(previous: KataTaskSummary | undefined, next: KataTaskSummary): KataTaskSummary {
  if (!previous) return next;
  return {
    ...next,
    parent: hasOwnField(next, "parent") ? next.parent : previous.parent,
    parent_short_id: hasOwnField(next, "parent_short_id") ? next.parent_short_id : previous.parent_short_id,
    blocks: mergeKataPeerList(previous.blocks, next.blocks, hasOwnField(next, "blocks")),
    blocked_by: mergeKataPeerList(previous.blocked_by, next.blocked_by, hasOwnField(next, "blocked_by")),
    related: mergeKataPeerList(previous.related, next.related, hasOwnField(next, "related")),
    child_counts: hasOwnField(next, "child_counts") ? next.child_counts : previous.child_counts,
  };
}

function taskPeerListSignature(peers: KataTaskSummary["blocks"]): string {
  return JSON.stringify((peers ?? []).map((peer) => [peer.uid ?? "", peer.short_id]));
}

function taskSummarySignature(task: KataTaskSummary): string {
  return JSON.stringify([
    task.id,
    task.uid,
    task.project_id,
    task.project_uid,
    task.project_name,
    task.short_id,
    task.qualified_id,
    task.title,
    task.body ?? null,
    task.status,
    task.revision,
    task.created_at,
    task.updated_at,
    task.owner ?? null,
    task.author ?? null,
    task.priority ?? null,
    JSON.stringify(task.metadata ?? {}),
    JSON.stringify(task.labels ?? []),
    task.recurrence_id ?? null,
    task.occurrence_key ?? null,
    task.parent ? [task.parent.uid, task.parent.short_id] : null,
    task.parent_short_id ?? null,
    taskPeerListSignature(task.blocks),
    taskPeerListSignature(task.blocked_by),
    taskPeerListSignature(task.related),
    JSON.stringify(task.child_counts ?? null),
    task.closed_reason ?? null,
    task.closed_at ?? null,
    task.deleted_at ?? null,
  ]);
}

export function duplicateCandidatesFromError(error: unknown): KataDuplicateCandidateDisplay[] {
  const envelope = isObject(error) && isObject(error.error) ? error.error : error;
  const details =
    isObject(envelope) && isObject(envelope.details)
      ? envelope.details
      : isObject(error) && isObject(error.details)
        ? error.details
        : undefined;
  const rawCandidates =
    isObject(details) && Array.isArray(details.duplicate_candidates) ? details.duplicate_candidates : [];

  return rawCandidates.flatMap((candidate): KataDuplicateCandidateDisplay[] => {
    if (!isObject(candidate)) return [];
    const source = isObject(candidate.issue) ? candidate.issue : candidate;
    const title = typeof source.title === "string" ? source.title : "";
    const qualifiedID = typeof source.qualified_id === "string" ? source.qualified_id : "";
    if (!title || !qualifiedID) return [];
    return [
      {
        title,
        qualified_id: qualifiedID,
        reason: typeof candidate.reason === "string" ? candidate.reason : undefined,
      },
    ];
  });
}

export interface KataWorkspaceStoreSnapshot {
  connection: KataConnectionState;
  projects: KataProjectSummary[];
  areas: KataAreaSummary[];
  currentView: KataCurrentView;
  selectedIssue: KataTaskDetail | null;
  selectedEvents: KataTaskEvent[];
  selectedRecurrences: KataRecurrence[];
  searchFilters: KataTaskSearchFilters;
  duplicateCandidates: KataDuplicateCandidateDisplay[];
  cachedTasks: KataTaskSummary[];
  eventCursor: number;
  unscopedViewName: KataTaskViewName;
}

export class KataEventCursorSyncError extends Error {
  readonly cursorSyncCause: unknown;

  constructor(cursorSyncCause: unknown) {
    super(cursorSyncCause instanceof Error ? cursorSyncCause.message : "Kata event cursor sync failed.");
    this.name = "KataEventCursorSyncError";
    this.cursorSyncCause = cursorSyncCause;
  }
}

export class KataWorkspaceStore {
  readonly api: KataTaskAPI;
  connection = $state<KataConnectionState>({ status: "offline" });
  projects = $state.raw<KataProjectSummary[]>([]);
  areas = $state.raw<KataAreaSummary[]>([]);
  currentView = $state.raw<KataCurrentView>(emptyView());
  selectedIssue = $state.raw<KataTaskDetail | null>(null);
  selectedEvents = $state.raw<KataTaskEvent[]>([]);
  selectedRecurrences = $state.raw<KataRecurrence[]>([]);
  eventCursor = $state(0);
  daemonId = $state<string | undefined>(undefined);
  searchFilters = $state.raw<KataTaskSearchFilters>(defaultKataTaskSearchFilters());
  duplicateCandidates = $state.raw<KataDuplicateCandidateDisplay[]>([]);
  pendingSelectionUID = $state<string | null>(null);
  pendingMutationCount = $state(0);
  hasPendingMutations = $derived(this.pendingMutationCount > 0);
  private taskCache = $state.raw<Map<string, KataTaskSummary>>(new Map());
  cachedTasks = $derived([...this.taskCache.values()]);

  private viewRequestID = 0;
  private viewAbort: AbortController | null = null;
  private detailRequestID = 0;
  private detailAbort: AbortController | null = null;
  private selectedEventsRead: Promise<void> = Promise.resolve();
  private selectedRecurrencesRead: Promise<void> = Promise.resolve();
  private unscopedViewName: KataTaskViewName = "today";
  private issueETags = new Map<string, string>();
  private metadataQueues = new Map<string, Promise<void>>();
  // Cursor delivery is one ordered lane shared by paginated catch-up and SSE.
  // A task starts only after its predecessor settles, so an event observed by
  // both transports is deduplicated against the cursor before it can refresh
  // the visible workspace a second time.
  private eventDeliveryGeneration = 0;
  private activeEventDeliveryGenerations = new Set<number>();
  private pendingEventDeliveries = new Map<number, Array<() => void>>();

  constructor(options: CreateKataWorkspaceStoreOptions = {}) {
    this.api = options.api ?? createKataTaskAPI();
  }

  private beginViewRequest(): { requestID: number; signal: AbortSignal } {
    this.viewAbort?.abort();
    const controller = new AbortController();
    this.viewAbort = controller;
    return { requestID: ++this.viewRequestID, signal: controller.signal };
  }

  clearDaemonBinding(): void {
    this.api.bindWorkflowDaemon?.();
  }

  bindDaemonForBootstrap(daemonId: string, bindSharedAPI = true): void {
    this.daemonId = daemonId;
    if (bindSharedAPI) this.api.bindWorkflowDaemon?.(daemonId);
  }

  clearDaemonState(viewName: KataTaskViewName = this.currentView.name): void {
    this.eventDeliveryGeneration += 1;
    this.clearSelection();
    this.daemonId = undefined;
    this.clearDaemonBinding();
    this.projects = [];
    this.areas = [];
    this.currentView = emptyView(viewName);
    this.resetSearchFilters();
    this.clearTaskCache();
    this.issueETags.clear();
    this.resetEventCursor();
  }

  async loadProjectCatalog(shouldApply: () => boolean = () => true): Promise<void> {
    const projects = await this.loadProjects();
    if (!shouldApply()) return;
    this.projects = projects.projects;
    this.areas = deriveKataAreas(projects.projects);
  }

  resetToInertWorkspace(): void {
    this.invalidatePendingLoads();
    this.clearTaskCache();
    this.issueETags.clear();
    this.currentView = emptyView("all");
    this.searchFilters = defaultKataTaskSearchFilters();
    this.duplicateCandidates = [];
    this.selectedIssue = null;
    this.selectedEvents = [];
    this.selectedRecurrences = [];
    this.pendingSelectionUID = null;
    this.unscopedViewName = "all";
    this.connection = { status: "offline" };
  }

  captureSnapshot(): KataWorkspaceStoreSnapshot {
    return {
      connection: this.connection,
      projects: this.projects,
      areas: this.areas,
      currentView: this.currentView,
      selectedIssue: this.selectedIssue,
      selectedEvents: this.selectedEvents,
      selectedRecurrences: this.selectedRecurrences,
      searchFilters: this.searchFilters,
      duplicateCandidates: this.duplicateCandidates,
      cachedTasks: this.cachedTasks,
      eventCursor: this.eventCursor,
      unscopedViewName: this.unscopedViewName,
    };
  }

  restoreSnapshot(snapshot: KataWorkspaceStoreSnapshot): void {
    this.resetToInertWorkspace();
    this.connection = snapshot.connection;
    this.projects = snapshot.projects;
    this.areas = snapshot.areas;
    this.currentView = snapshot.currentView;
    this.selectedIssue = snapshot.selectedIssue;
    this.selectedEvents = snapshot.selectedEvents;
    this.selectedRecurrences = snapshot.selectedRecurrences;
    this.searchFilters = snapshot.searchFilters;
    this.duplicateCandidates = snapshot.duplicateCandidates;
    this.cacheTasks(snapshot.cachedTasks);
    this.eventCursor = snapshot.eventCursor;
    this.unscopedViewName = snapshot.unscopedViewName;
  }

  async bootstrap(
    viewName: KataTaskViewName = "today",
    preferredIssueUID?: string | null,
    options: KataBootstrapOptions = {},
  ): Promise<void> {
    if (!shouldApplyLoad(options)) return;
    const { requestID, signal } = this.beginViewRequest();
    this.connection = { status: "connecting" };
    try {
      await this.loadInstance(signal);
      if (!shouldApplyLoad(options)) return;
      const [projects, view] = await Promise.all([
        this.loadProjects(signal),
        this.loadIssues({ view: viewName }, signal),
      ]);
      if (!shouldApplyLoad(options)) return;
      if (requestID !== this.viewRequestID) {
        this.connection = { status: "online" };
        return;
      }
      this.acceptWorkflowResult(view);
      this.clearTaskCache();
      this.projects = projects.projects;
      this.areas = deriveKataAreas(projects.projects);
      this.cacheView(view);
      this.currentView = {
        name: view.view,
        groups: view.groups,
        fetched_at: view.fetched_at,
      };
      this.unscopedViewName = view.view;
      const rawIssues = view.groups.flatMap((group) => group.issues);
      const issues = selectableViewIssues(view.groups);
      const firstIssue = options.selectFirst === false ? undefined : issues[0];
      const nextUID =
        preferredIssueUID && rawIssues.some((issue) => issue.uid === preferredIssueUID)
          ? preferredIssueUID
          : firstIssue?.uid;
      await this.loadSelectedIssue(nextUID ?? preferredIssueUID ?? null, requestID, ++this.detailRequestID, options);
      if (shouldApplyLoad(options)) this.connection = { status: "online" };
    } catch (error) {
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.connection = {
        status: "error",
        message: connectionErrorMessage(error),
      };
      throw error;
    }
  }

  async restoreViewAndFilters(
    viewName: KataTaskViewName,
    filters: KataTaskSearchFilters,
    options: KataLoadOptions = {},
  ): Promise<void> {
    const snapshot = this.captureSnapshot();
    const expectedRequestID = () => this.viewRequestID + 1;
    let rollbackRequestID = expectedRequestID();
    try {
      this.searchFilters = filters;
      this.duplicateCandidates = [];
      await this.openView(viewName, { ...options, selectFirst: false });
      if (hasActiveSearchFilters(filters)) {
        rollbackRequestID = expectedRequestID();
        await this.updateSearchFilters({}, { ...options, selectFirst: false });
      }
    } catch (error) {
      // A restored workspace is accepted only after every required load
      // succeeds. Do not roll back over a newer navigation that superseded it.
      if (this.viewRequestID === rollbackRequestID) {
        this.restoreSnapshot(snapshot);
      }
      throw error;
    }
  }

  async openView(viewName: KataTaskViewName, options: KataLoadOptions = {}): Promise<void> {
    if (!shouldApplyLoad(options)) return;
    const { requestID, signal } = this.beginViewRequest();
    let view: KataTaskViewResponse;
    try {
      view = await this.loadIssues({ view: viewName, ...scopedIssueQuery(this.searchFilters) }, signal);
    } catch (error) {
      if (requestID !== this.viewRequestID) return;
      throw error;
    }
    if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
    this.acceptWorkflowResult(view);
    this.cacheView(view);
    this.currentView = {
      name: view.view,
      groups: view.groups,
      fetched_at: view.fetched_at,
    };
    if (this.searchFilters.scope.kind === "all") {
      this.unscopedViewName = view.view;
    }
    const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(view.groups)[0];
    await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID, options);
  }

  async updateSearchFilters(next: Partial<KataTaskSearchFilters>, options: KataLoadOptions = {}): Promise<void> {
    const previousFilters = this.searchFilters;
    const nextFilters: KataTaskSearchFilters = {
      ...this.searchFilters,
      ...next,
      scope: next.scope ?? this.searchFilters.scope,
    };
    if (options.shouldApply) {
      await this.updateSearchFiltersGuarded(previousFilters, nextFilters, options);
      return;
    }
    if (previousFilters.scope.kind === "all" && nextFilters.scope.kind === "project") {
      this.unscopedViewName = this.currentView.name;
    }
    this.searchFilters = nextFilters;

    if (!hasActiveSearchFilters(this.searchFilters)) {
      this.duplicateCandidates = [];
      await this.openView(this.unscopedViewName, options);
      return;
    }

    const { requestID, signal } = this.beginViewRequest();
    try {
      const results = await this.searchIssues(this.searchFilters, signal);
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.acceptWorkflowResult(results);
      this.duplicateCandidates = [];
      this.cacheTasks(results.issues);
      const groups = groupSearchIssues(results.issues);
      this.currentView = {
        name: isProjectBacklogScope(this.searchFilters) ? "all" : this.currentView.name,
        groups,
        fetched_at: results.fetched_at,
      };
      const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(groups)[0];
      await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID, options);
    } catch (error) {
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.duplicateCandidates = duplicateCandidatesFromError(error);
      if (this.duplicateCandidates.length === 0) throw error;
    }
  }

  private async updateSearchFiltersGuarded(
    previousFilters: KataTaskSearchFilters,
    nextFilters: KataTaskSearchFilters,
    options: KataLoadOptions,
  ): Promise<void> {
    const nextUnscopedViewName =
      previousFilters.scope.kind === "all" && nextFilters.scope.kind === "project"
        ? this.currentView.name
        : this.unscopedViewName;

    if (!hasActiveSearchFilters(nextFilters)) {
      const { requestID, signal } = this.beginViewRequest();
      let view: KataTaskViewResponse;
      try {
        view = await this.loadIssues({ view: nextUnscopedViewName }, signal);
      } catch (error) {
        if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
        throw error;
      }
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.acceptWorkflowResult(view);
      this.searchFilters = nextFilters;
      this.duplicateCandidates = [];
      this.cacheView(view);
      this.currentView = {
        name: view.view,
        groups: view.groups,
        fetched_at: view.fetched_at,
      };
      if (nextFilters.scope.kind === "all") {
        this.unscopedViewName = view.view;
      }
      const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(view.groups)[0];
      await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID, options);
      return;
    }

    const { requestID, signal } = this.beginViewRequest();
    try {
      const results = await this.searchIssues(nextFilters, signal);
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.acceptWorkflowResult(results);
      if (previousFilters.scope.kind === "all" && nextFilters.scope.kind === "project") {
        this.unscopedViewName = this.currentView.name;
      }
      this.searchFilters = nextFilters;
      this.duplicateCandidates = [];
      this.cacheTasks(results.issues);
      const groups = groupSearchIssues(results.issues);
      this.currentView = {
        name: isProjectBacklogScope(nextFilters) ? "all" : this.currentView.name,
        groups,
        fetched_at: results.fetched_at,
      };
      const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(groups)[0];
      await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID, options);
    } catch (error) {
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.searchFilters = nextFilters;
      this.duplicateCandidates = duplicateCandidatesFromError(error);
      if (this.duplicateCandidates.length === 0) throw error;
    }
  }

  async createProject(name: string): Promise<KataProjectSummary> {
    return this.withMutation(async () => {
      const project = await this.api.createProject(name);
      await this.reloadProjects();
      return project;
    });
  }

  async captureIssue(actor: string, draft: KataTaskCreateDraft, idempotencyKey = createULID()): Promise<void> {
    await this.withMutation(async () => {
      const inbox =
        this.projects.find(isTaskInboxProject) ??
        (await this.loadProjects()).projects.find((project) => isTaskInboxProject(project));
      if (!inbox) throw new Error("task inbox project is not available");

      const result = await this.api.createIssue(inbox.id, actor, draft, idempotencyKey);
      this.captureMutationETag(result);
      await this.reloadProjects();

      const { requestID, signal } = this.beginViewRequest();
      this.searchFilters = defaultKataTaskSearchFilters();
      this.duplicateCandidates = [];
      const view = await this.loadIssues({ view: "inbox" }, signal);
      if (requestID !== this.viewRequestID) return;
      this.acceptWorkflowResult(view);
      this.cacheView(view);
      this.currentView = {
        name: "inbox",
        groups: view.groups,
        fetched_at: view.fetched_at,
      };
      this.unscopedViewName = "inbox";
      await this.loadSelectedIssue(result.issue?.uid ?? null, requestID, ++this.detailRequestID);
    });
  }

  async renameProject(projectID: number, name: string): Promise<void> {
    await this.withMutation(async () => {
      await this.api.renameProject(projectID, name);
      await this.reloadProjects();
      await this.refreshCurrentView(this.selectedIssue?.issue.uid);
    });
  }

  async selectIssue(uid: string, options: KataLoadOptions = {}): Promise<boolean> {
    if (!shouldApplyLoad(options)) return false;
    this.pendingSelectionUID = uid;
    const requestID = ++this.detailRequestID;
    this.observeGraphStore("selection-start", { uid, detailRequestID: requestID });
    try {
      return await this.loadSelectedIssue(uid, undefined, requestID, options);
    } catch (error) {
      if (this.detailRequestID === requestID && this.pendingSelectionUID === uid) {
        this.pendingSelectionUID = null;
      }
      throw error;
    }
  }

  clearSelection(): void {
    this.detailRequestID++;
    this.abortPendingDetail();
    this.selectedIssue = null;
    this.selectedEvents = [];
    this.selectedRecurrences = [];
    this.pendingSelectionUID = null;
  }

  async awaitSelectedAuxiliaryReads(): Promise<void> {
    await Promise.all([this.selectedEventsRead, this.selectedRecurrencesRead]);
  }

  async addComment(uid: string, actor: string, body: string): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.addComment(target, actor, body));
  }

  async addLabel(uid: string, actor: string, label: string): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.addLabel(target, actor, label));
  }

  async removeLabel(uid: string, actor: string, label: string): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.removeLabel(target, actor, label));
  }

  async assignOwner(uid: string, actor: string, owner: string): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.assignOwner(target, actor, owner));
  }

  async unassignOwner(uid: string, actor: string): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.unassignOwner(target, actor));
  }

  async setPriority(uid: string, actor: string, priority: number | null): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.setPriority(target, actor, priority));
  }

  async closeIssue(uid: string, actor: string, options: KataTaskCloseOptions = {}): Promise<void> {
    await this.withMutation(async () => {
      await this.mutateIssue(uid, (target) => this.api.closeIssue(target, actor, options));
      await this.reloadProjects();
    });
  }

  async reopenIssue(uid: string, actor: string): Promise<void> {
    await this.withMutation(async () => {
      await this.mutateIssue(uid, (target) => this.api.reopenIssue(target, actor));
      await this.reloadProjects();
    });
  }

  async editIssue(uid: string, actor: string, patch: KataTaskEditPatch): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.editIssue(target, actor, patch));
  }

  async patchMetadata(uid: string, actor: string, patch: KataTaskMetadataPatch): Promise<void> {
    const previous = this.metadataQueues.get(uid) ?? Promise.resolve();
    const next = previous.catch(() => {}).then(() => this.patchMetadataNow(uid, actor, patch));
    this.metadataQueues.set(uid, next);
    try {
      await next;
    } finally {
      if (this.metadataQueues.get(uid) === next) {
        this.metadataQueues.delete(uid);
      }
    }
  }

  async moveIssue(uid: string, actor: string, toProjectUID: string): Promise<void> {
    await this.withMutation(async () => {
      const issue = this.issueForMutation(uid);
      const selectedETag = this.selectedIssue?.issue.uid === uid ? this.selectedIssue.etag : undefined;
      const ifMatch = this.issueETags.get(uid) ?? selectedETag ?? `"rev-${issue.revision}"`;
      await this.mutateIssue(uid, (target) => this.api.moveIssue(target, actor, toProjectUID, ifMatch));
      await this.reloadProjects();
    });
  }

  async createRecurrence(
    projectID: number,
    input: KataCreateRecurrenceInput,
  ): Promise<KataRecurrenceResponse["recurrence"]> {
    return this.withMutation(async () => {
      const response = await this.api.createRecurrence(projectID, input);
      await this.refreshSelectedRecurrences();
      return response.recurrence;
    });
  }

  async patchRecurrence(
    id: number,
    input: KataPatchRecurrenceInput,
    ifMatch: string,
  ): Promise<KataRecurrenceResponse["recurrence"]> {
    return this.withMutation(async () => {
      const target = this.recurrenceTarget(id);
      try {
        const response = await this.api.patchRecurrence(target.projectID, target.uid, input, ifMatch);
        await this.refreshSelectedRecurrences();
        return response.recurrence;
      } catch (error) {
        if (error && typeof error === "object" && (error as { status?: number }).status === 412) {
          const latest = await this.api.showRecurrence(target.projectID, target.uid);
          throw Object.assign(error, { response: latest });
        }
        throw error;
      }
    });
  }

  async deleteRecurrence(id: number, actor: string): Promise<void> {
    await this.withMutation(async () => {
      const target = this.recurrenceTarget(id);
      try {
        await this.api.deleteRecurrence(target.projectID, target.uid, actor, target.ifMatch);
        await this.refreshSelectedRecurrences();
      } catch (error) {
        if (error && typeof error === "object" && (error as { status?: number }).status === 412) {
          await this.refreshSelectedRecurrences();
        }
        throw error;
      }
    });
  }

  resetSearchFilters(): void {
    this.searchFilters = defaultKataTaskSearchFilters();
    this.duplicateCandidates = [];
  }

  rememberTasks(issues: readonly KataTaskSummary[]): void {
    this.cacheTasks(issues);
  }

  invalidatePendingLoads(): void {
    this.viewAbort?.abort();
    this.viewAbort = null;
    this.viewRequestID++;
    this.detailRequestID++;
    this.abortPendingDetail();
    this.pendingSelectionUID = null;
    this.updateGraphDebugStore();
  }

  // Whenever a detail load stops being wanted (newer selection, cleared
  // selection, route invalidation), abort it: a superseded request left
  // running ties up the daemon and its late rejection would surface a
  // stale error for a navigation the user already left.
  private abortPendingDetail(): void {
    if (this.detailAbort) {
      this.observeGraphStore("detail-load-abort", { selectedIssueUID: this.selectedIssue?.issue.uid ?? null });
    }
    this.detailAbort?.abort();
    this.detailAbort = null;
  }

  resetEventCursor(): void {
    this.eventCursor = 0;
  }

  async syncEventCursor(options: KataEventDeliveryOptions = {}): Promise<boolean> {
    return this.enqueueEventDelivery(() => this.syncEventCursorNow(options));
  }

  async applyRemoteEvent(event: KataTaskEvent, options: KataEventDeliveryOptions = {}): Promise<boolean> {
    return this.enqueueEventDelivery(() => this.applyRemoteEventNow(event, options));
  }

  async applyEventStreamMessage(
    message: KataTaskEventStreamMessage,
    options: KataEventDeliveryOptions = {},
  ): Promise<boolean> {
    return this.enqueueEventDelivery(() => this.applyEventStreamMessageNow(message, options));
  }

  private enqueueEventDelivery<T>(operation: () => Promise<T>): Promise<T> {
    const generation = this.eventDeliveryGeneration;
    return new Promise<T>((resolve, reject) => {
      const run = () => {
        void operation()
          .then(resolve, reject)
          .finally(() => {
            const queue = this.pendingEventDeliveries.get(generation);
            const next = queue?.shift();
            if (next) {
              next();
            } else {
              this.pendingEventDeliveries.delete(generation);
              this.activeEventDeliveryGenerations.delete(generation);
            }
          });
      };
      if (this.activeEventDeliveryGenerations.has(generation)) {
        const queue = this.pendingEventDeliveries.get(generation) ?? [];
        queue.push(run);
        this.pendingEventDeliveries.set(generation, queue);
      } else {
        this.activeEventDeliveryGenerations.add(generation);
        run();
      }
    });
  }

  private shouldApplyEventDelivery(options: KataEventDeliveryOptions): boolean {
    return options.shouldApply?.() ?? true;
  }

  private async syncEventCursorNow(options: KataEventDeliveryOptions): Promise<boolean> {
    if (!this.shouldApplyEventDelivery(options)) return false;

    let afterID = this.eventCursor;
    const pendingEvents: KataTaskEvent[] = [];
    let pageError: unknown;

    for (;;) {
      let response: KataTaskEventsResponse;
      try {
        response = await this.loadEvents({ after_id: afterID, limit: 100 });
      } catch (error) {
        pageError = error;
        break;
      }
      if (!this.shouldApplyEventDelivery(options)) return false;

      const nextAfterID = Math.max(afterID, response.next_after_id, ...response.events.map((event) => event.event_id));
      if (response.reset_required) {
        return this.applyEventStreamMessageNow(
          {
            kind: "reset",
            event_id: nextAfterID,
            reset_after_id: response.reset_after_id ?? nextAfterID,
            lastEventID: nextAfterID,
          },
          options,
        );
      }

      pendingEvents.push(...response.events.filter((event) => event.event_id > this.eventCursor));
      const reachedEnd = response.events.length === 0 || nextAfterID === afterID;
      afterID = nextAfterID;
      if (reachedEnd) break;
    }

    let membershipRefreshed = false;
    if (pendingEvents.length > 0) {
      membershipRefreshed = await this.refreshForRemoteEvents(pendingEvents, options);
      if (!this.shouldApplyEventDelivery(options)) return false;
      if (membershipRefreshed) this.eventCursor = Math.max(this.eventCursor, afterID);
    } else if (pageError === undefined) {
      this.eventCursor = Math.max(this.eventCursor, afterID);
    }

    if (pageError !== undefined) {
      if (membershipRefreshed) throw new KataEventCursorSyncError(pageError);
      throw pageError;
    }
    return membershipRefreshed;
  }

  private async applyRemoteEventNow(event: KataTaskEvent, options: KataEventDeliveryOptions): Promise<boolean> {
    if (!this.shouldApplyEventDelivery(options)) return false;
    if (event.event_id <= this.eventCursor) return true;
    const refreshed = await this.refreshForRemoteEvents([event], options);
    if (!this.shouldApplyEventDelivery(options) || !refreshed) return false;
    this.eventCursor = Math.max(this.eventCursor, event.event_id);
    return true;
  }

  private async applyEventStreamMessageNow(
    message: KataTaskEventStreamMessage,
    options: KataEventDeliveryOptions,
  ): Promise<boolean> {
    if (!this.shouldApplyEventDelivery(options)) return false;
    if (message.kind === "reset") {
      if (message.reset_after_id <= this.eventCursor) return true;
      const refreshed = await this.refreshCurrentView(
        this.pendingSelectionUID ?? this.selectedIssue?.issue.uid ?? null,
        { eventDriven: true, shouldApply: options.shouldApply },
      );
      if (refreshed && this.shouldApplyEventDelivery(options)) {
        this.eventCursor = Math.max(this.eventCursor, message.reset_after_id);
      }
      return refreshed && this.shouldApplyEventDelivery(options);
    }
    return this.applyRemoteEventNow(message.event, options);
  }

  private async reloadProjects(shouldApply?: () => boolean): Promise<void> {
    const projects = await this.loadProjects();
    if (shouldApply?.() === false) return;
    this.projects = projects.projects;
    this.areas = deriveKataAreas(projects.projects);
  }

  private async refreshForRemoteEvents(
    events: readonly KataTaskEvent[],
    options: KataEventDeliveryOptions = {},
  ): Promise<boolean> {
    if (events.some((event) => event.type.startsWith("project."))) {
      await this.reloadProjects(options.shouldApply);
      if (!this.shouldApplyEventDelivery(options)) return false;
    }
    const preferredUID = this.pendingSelectionUID ?? this.selectedIssue?.issue.uid ?? null;
    const selectedProjectID = this.selectedIssue?.issue.project_id;
    const refreshSelectedDetail = events.some(
      (event) =>
        event.issue_uid === preferredUID ||
        event.related_issue_uid === preferredUID ||
        (event.type.startsWith("project.") && event.project_id === selectedProjectID),
    );
    const refreshed = await this.refreshCurrentView(preferredUID, {
      refreshSelectedDetail,
      eventDriven: true,
      shouldApply: options.shouldApply,
    });
    if (!this.shouldApplyEventDelivery(options) || !refreshed) return false;
    for (const event of events) this.applyTrivialMetadataEvent(event);
    return true;
  }

  private clearTaskCache(): void {
    this.taskCache = new Map();
  }

  private cacheTasks(issues: readonly KataTaskSummary[]): void {
    if (issues.length === 0) return;
    const next = new Map(this.taskCache);
    let changed = false;
    for (const issue of issues) {
      const previous = next.get(issue.uid);
      const merged = mergeCachedTaskSummary(previous, issue);
      if (!previous || taskSummarySignature(previous) !== taskSummarySignature(merged)) {
        next.set(issue.uid, merged);
        changed = true;
      }
    }
    if (!changed) return;
    this.taskCache = next;
    this.updateGraphDebugStore();
  }

  private cacheView(view: Pick<KataTaskViewResponse, "groups">): void {
    this.cacheTasks(view.groups.flatMap((group) => group.issues));
  }

  private acceptWorkflowResult(result: { daemon_id?: string | undefined }): void {
    const daemonId = result.daemon_id?.trim();
    if (!daemonId) return;
    this.daemonId = daemonId;
  }

  private workflowRequestOptions(signal?: AbortSignal): { daemonId?: string; signal?: AbortSignal } | undefined {
    if (!this.daemonId && !signal) return undefined;
    return {
      ...(this.daemonId ? { daemonId: this.daemonId } : {}),
      ...(signal ? { signal } : {}),
    };
  }

  private loadInstance(signal?: AbortSignal): Promise<Awaited<ReturnType<KataTaskAPI["instance"]>>> {
    const options = this.workflowRequestOptions(signal);
    return options ? this.api.instance(options) : this.api.instance();
  }

  private loadProjects(signal?: AbortSignal): Promise<Awaited<ReturnType<KataTaskAPI["projects"]>>> {
    const options = this.workflowRequestOptions(signal);
    return options ? this.api.projects(options) : this.api.projects();
  }

  private loadEvents(
    query: Parameters<KataTaskAPI["events"]>[0],
    options: Omit<NonNullable<Parameters<KataTaskAPI["events"]>[1]>, "daemonId"> = {},
  ): Promise<KataTaskEventsResponse> {
    const workflowOptions = this.workflowRequestOptions(options.signal);
    return this.api.events(query, { ...options, ...workflowOptions });
  }

  private loadIssues(query: KataTaskIssuesQuery, signal?: AbortSignal): Promise<KataTaskViewResponse> {
    const options = this.workflowRequestOptions(signal);
    return options ? this.api.issues(query, options) : this.api.issues(query);
  }

  private searchIssues(filters: KataTaskSearchFilters, signal?: AbortSignal): Promise<KataTaskSearchResponse> {
    const options = this.workflowRequestOptions(signal);
    return options ? this.api.search(filters, options) : this.api.search(filters);
  }

  private cacheDetail(detail: KataTaskDetail): void {
    this.cacheTasks([detail.issue, ...(detail.children ?? [])]);
  }

  private isIssueRefreshActive(): boolean {
    return this.detailAbort !== null || this.pendingSelectionUID !== null;
  }

  private updateGraphDebugStore(): void {
    setKataGraphDebugStore({
      queueKeys: [],
      graphLoadActive: false,
      issueRefreshActive: this.isIssueRefreshActive(),
      pendingSelectionUID: this.pendingSelectionUID,
      selectedIssueUID: this.selectedIssue?.issue.uid ?? null,
      cachedTaskCount: this.taskCache.size,
    });
  }

  private observeGraphStore(
    kind: Parameters<typeof recordKataGraphDebugEvent>[0],
    detail?: Record<string, unknown>,
  ): void {
    this.updateGraphDebugStore();
    recordKataGraphDebugEvent(kind, detail);
  }

  private applyTrivialMetadataEvent(event: KataTaskEvent): void {
    if (event.type !== "issue.metadata_updated") return;
    if (!event.issue_uid) return;

    const diff = isObject(event.payload) && isObject(event.payload.diff) ? event.payload.diff : undefined;
    if (!diff) return;

    const revisionNew =
      isObject(event.payload) && typeof event.payload.revision_new === "number"
        ? event.payload.revision_new
        : undefined;

    const patchMetadata = (metadata: Record<string, unknown>) => {
      const next = { ...metadata };
      for (const [key, rawChange] of Object.entries(diff)) {
        if (!isObject(rawChange) || !("to" in rawChange)) continue;
        if (rawChange.to === null || rawChange.to === undefined) {
          delete next[key];
        } else {
          next[key] = rawChange.to;
        }
      }
      return next;
    };
    const canApplyToRevision = (revision: number) => revisionNew === undefined || revision <= revisionNew;

    this.currentView = {
      ...this.currentView,
      groups: this.currentView.groups.map((group) => ({
        ...group,
        issues: group.issues.map((issue) =>
          issue.uid === event.issue_uid && canApplyToRevision(issue.revision)
            ? {
                ...issue,
                metadata: patchMetadata(issue.metadata),
                revision: revisionNew ?? issue.revision,
              }
            : issue,
        ),
      })),
    };
    this.cacheView(this.currentView);

    if (this.selectedIssue?.issue.uid !== event.issue_uid) return;
    if (!canApplyToRevision(this.selectedIssue.issue.revision)) return;
    this.selectedIssue = {
      ...this.selectedIssue,
      issue: {
        ...this.selectedIssue.issue,
        metadata: patchMetadata(this.selectedIssue.issue.metadata),
        revision: revisionNew ?? this.selectedIssue.issue.revision,
      },
    };
    this.cacheDetail(this.selectedIssue);
    if (revisionNew !== undefined) {
      this.issueETags.set(event.issue_uid, `"rev-${revisionNew}"`);
    }
  }

  private recurrenceTarget(id: number): { projectID: number; uid: string; ifMatch: string } {
    const recurrence = this.selectedRecurrences.find((item) => item.id === id);
    if (!recurrence) {
      throw new Error(`recurrence not loaded: id=${id}`);
    }
    return {
      projectID: recurrence.project_id,
      uid: recurrence.uid,
      ifMatch: `"rev-${recurrence.revision}"`,
    };
  }

  private async refreshSelectedRecurrences(): Promise<void> {
    if (!this.selectedIssue) return;
    const projectID = this.selectedIssue.issue.project_id;
    const next = await this.recurrencesForProject(projectID);
    if (!this.selectedIssue || this.selectedIssue.issue.project_id !== projectID) return;
    this.selectedRecurrences = next;
  }

  private async patchMetadataNow(uid: string, actor: string, patch: KataTaskMetadataPatch): Promise<void> {
    const issue = this.issueForMutation(uid);
    const selectedETag = this.selectedIssue?.issue.uid === uid ? this.selectedIssue.etag : undefined;
    const ifMatch = this.issueETags.get(uid) ?? selectedETag ?? `"rev-${issue.revision}"`;
    await this.mutateIssue(uid, (target) => this.api.patchIssueMetadata(target, actor, patch, ifMatch));
  }

  private async withMutation<T>(task: () => Promise<T>): Promise<T> {
    this.pendingMutationCount += 1;
    try {
      return await task();
    } finally {
      this.pendingMutationCount -= 1;
    }
  }

  private async mutateIssue(
    uid: string,
    operation: (target: KataTaskMutationTarget) => Promise<KataTaskMutationResponse>,
    options: { preserveSelection?: boolean } = {},
  ): Promise<void> {
    await this.withMutation(async () => {
      const preserveSelection = options.preserveSelection ?? true;
      const target = this.targetForIssue(uid);
      const selectionBeforeMutation = this.detailRequestID;
      const result = await operation(target);
      this.captureMutationETag(result);
      const currentSelectedUID = this.pendingSelectionUID ?? this.selectedIssue?.issue.uid;
      const preferredUID = preserveSelection
        ? selectionBeforeMutation === this.detailRequestID
          ? uid
          : currentSelectedUID
        : undefined;
      await this.refreshCurrentView(preferredUID);
    });
  }

  private targetForIssue(uid: string): KataTaskMutationTarget {
    const issue = this.issueForMutation(uid);
    return { project_id: issue.project_id, ref: issue.uid };
  }

  private issueForMutation(uid: string): KataTaskSummary {
    const selected = this.selectedIssue?.issue.uid === uid ? this.selectedIssue.issue : undefined;
    const listed = this.currentView.groups.flatMap((group) => group.issues).find((issue) => issue.uid === uid);
    const issue = selected ?? listed;
    if (!issue) {
      throw new Error(`issue not loaded: ${uid}`);
    }
    return issue;
  }

  private captureMutationETag(result: unknown): void {
    if (typeof result !== "object" || result === null || !("issue" in result)) return;
    const issue = (result as KataTaskMutationResponse).issue;
    const etag = (result as KataTaskMutationResponse).etag;
    if (issue?.uid) {
      this.cacheTasks([issue]);
    }
    if (issue?.uid && etag) {
      this.issueETags.set(issue.uid, etag);
    }
  }

  private async refreshCurrentView(preferredUID?: string | null, options: KataRefreshOptions = {}): Promise<boolean> {
    if (!shouldApplyLoad(options)) return false;
    const { requestID, signal } = this.beginViewRequest();
    // Selection epoch at refresh start: any selection or clear that lands
    // while the view fetch below is in flight bumps detailRequestID,
    // which makes the preferredUID captured by the caller stale.
    const selectionEpoch = this.detailRequestID;
    let nextView: KataCurrentView;
    let issues: KataTaskSummary[];
    if (shouldRefreshViaSearch(this.searchFilters, this.currentView.name)) {
      let results: KataTaskSearchResponse;
      try {
        results = await this.searchIssues(this.searchFilters, signal);
      } catch (error) {
        if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return false;
        this.duplicateCandidates = duplicateCandidatesFromError(error);
        if (this.duplicateCandidates.length === 0) throw error;
        return false;
      }
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return false;
      this.acceptWorkflowResult(results);
      this.duplicateCandidates = [];
      this.cacheTasks(results.issues);
      const groups = groupSearchIssues(results.issues);
      nextView = {
        name: isProjectBacklogScope(this.searchFilters) ? "all" : this.currentView.name,
        groups,
        fetched_at: results.fetched_at,
      };
      issues = selectableViewIssues(groups);
    } else {
      let view: KataTaskViewResponse;
      try {
        view = await this.loadIssues({ view: this.currentView.name, ...scopedIssueQuery(this.searchFilters) }, signal);
      } catch (error) {
        if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return false;
        this.duplicateCandidates = duplicateCandidatesFromError(error);
        if (this.duplicateCandidates.length === 0) throw error;
        return false;
      }
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return false;
      this.acceptWorkflowResult(view);
      this.duplicateCandidates = [];
      this.cacheView(view);
      nextView = {
        name: view.view,
        groups: view.groups,
        fetched_at: view.fetched_at,
      };
      issues = selectableViewIssues(view.groups);
    }
    this.currentView = nextView;
    if (options.refreshSelectedDetail === false) return true;
    let resolvedUID = preferredUID;
    if (this.detailRequestID !== selectionEpoch) {
      // The selection changed while the view fetch was in flight, so the
      // preferredUID the caller captured is stale. If the newer selection
      // is still loading, leave the pane to it: reloading here would
      // abort that load and silently discard a row the user just clicked.
      // The epoch gate matters — an in-flight load from before this
      // refresh (e.g. an older refresh's own detail reload) must instead
      // be superseded below, or its pre-refresh payload would later
      // overwrite the refreshed detail and ETag state.
      if (this.detailAbort !== null) {
        return true;
      }
      // The newer selection (or clear) already completed; re-resolve so
      // the stale preferredUID cannot revert it.
      resolvedUID = this.pendingSelectionUID ?? this.selectedIssue?.issue.uid ?? null;
    }
    const nextSelectedUID = resolvedUID === undefined ? (issues[0]?.uid ?? null) : resolvedUID;
    try {
      return await this.loadSelectedIssue(nextSelectedUID, requestID, ++this.detailRequestID, options);
    } catch (error) {
      if (!shouldApplyLoad(options)) return false;
      // Event delivery can accept authoritative list membership even if a
      // selected detail cannot be refreshed. Interactive callers must retain
      // the failure so their action is not reported as fully refreshed.
      if (options.eventDriven) return true;
      throw error;
    }
  }

  private async loadSelectedIssue(
    uid: string | null,
    viewRequestID: number | undefined,
    detailRequestID: number,
    options: KataLoadOptions = {},
  ): Promise<boolean> {
    this.abortPendingDetail();

    if (!uid) {
      if (shouldApplyLoad(options) && (viewRequestID === undefined || viewRequestID === this.viewRequestID)) {
        this.selectedIssue = null;
        this.selectedEvents = [];
        this.selectedRecurrences = [];
        this.pendingSelectionUID = null;
        this.updateGraphDebugStore();
        return true;
      }
      return false;
    }

    const abort = new AbortController();
    this.detailAbort = abort;
    this.observeGraphStore("detail-load-start", { uid, detailRequestID });
    const timingToken = String(detailRequestID);
    markInteractionStart(KATA_SELECT_ISSUE_INTERACTION, timingToken);
    // The events read may walk the daemon's whole event log (the daemon has
    // no issue_uid filter), which takes seconds against remote daemons. Start
    // it alongside the detail read so the pane never waits on the walk.
    const requestOptions = this.workflowRequestOptions();
    const eventsPromise = this.loadEvents({ issue_uid: uid, limit: 100 }, { signal: abort.signal });
    eventsPromise.catch(() => {});
    let detail: KataTaskDetail;
    try {
      detail = await this.api.issue(uid, { signal: abort.signal, ...requestOptions });
    } catch (error) {
      if (this.detailAbort === abort) this.detailAbort = null;
      clearInteraction(KATA_SELECT_ISSUE_INTERACTION, timingToken);
      // Aborted means superseded: a newer selection owns the pane now, so
      // this failure must not surface as a user-facing error.
      if (abort.signal.aborted) {
        this.observeGraphStore("detail-load-abort", { uid, detailRequestID });
        return false;
      }
      abort.abort();
      throw error;
    }

    const applyDetail = () => {
      this.cacheDetail(detail);
      this.selectedIssue = detail;
      if (detail.etag) {
        this.issueETags.set(detail.issue.uid, detail.etag);
      } else {
        this.issueETags.set(detail.issue.uid, `"rev-${detail.issue.revision}"`);
      }
      this.selectedEvents = [];
      this.selectedRecurrences = [];
      this.pendingSelectionUID = null;
      this.observeGraphStore("detail-load-complete", { uid, detailRequestID });
      measureInteraction(KATA_SELECT_ISSUE_INTERACTION, "detail-visible", timingToken, { uid });
      this.selectedRecurrencesRead = this.loadSelectedRecurrences(detail.issue.project_id, detailRequestID);
    };

    if (!shouldApplyLoad(options) || (viewRequestID !== undefined && viewRequestID !== this.viewRequestID)) {
      this.observeGraphStore("detail-load-stale", { uid, detailRequestID, viewRequestID });
      clearInteraction(KATA_SELECT_ISSUE_INTERACTION, timingToken);
      return false;
    }
    if (detailRequestID !== this.detailRequestID) {
      this.observeGraphStore("detail-load-stale", { uid, detailRequestID });
      clearInteraction(KATA_SELECT_ISSUE_INTERACTION, timingToken);
      return false;
    }
    // Issue history may require a client-side walk of the daemon's whole
    // event log. The selected detail is already usable, so history fills in
    // as a guarded continuation instead of extending list/view loading.
    applyDetail();
    this.selectedEventsRead = this.finishSelectedEvents(eventsPromise, abort, uid, detailRequestID, timingToken);
    return true;
  }

  // Completes a selection's best-effort event-log read after the
  // detail has already been applied and the selection promise resolved. A
  // failed or superseded read leaves the rendered detail in place with an
  // empty event log instead of failing the selection.
  private async finishSelectedEvents(
    eventsPromise: Promise<KataTaskEventsResponse>,
    abort: AbortController,
    uid: string,
    detailRequestID: number,
    timingToken: string,
  ): Promise<void> {
    let events: KataTaskEventsResponse;
    try {
      events = await eventsPromise;
    } catch {
      clearInteraction(KATA_SELECT_ISSUE_INTERACTION, timingToken);
      if (!abort.signal.aborted) {
        this.observeGraphStore("events-load-error", { uid, detailRequestID });
      }
      return;
    } finally {
      if (this.detailAbort === abort) this.detailAbort = null;
    }
    if (detailRequestID !== this.detailRequestID) {
      clearInteraction(KATA_SELECT_ISSUE_INTERACTION, timingToken);
      return;
    }
    this.selectedEvents = events.events;
    measureInteraction(KATA_SELECT_ISSUE_INTERACTION, "events-loaded", timingToken, {
      uid,
      count: events.events.length,
    });
    clearInteraction(KATA_SELECT_ISSUE_INTERACTION, timingToken);
  }

  private async loadSelectedRecurrences(projectID: number, detailRequestID: number): Promise<void> {
    try {
      const recurrences = await this.recurrencesForProject(projectID);
      if (detailRequestID !== this.detailRequestID) return;
      if (!this.selectedIssue || this.selectedIssue.issue.project_id !== projectID) return;
      this.selectedRecurrences = recurrences;
    } catch {
      if (detailRequestID !== this.detailRequestID) return;
      if (!this.selectedIssue || this.selectedIssue.issue.project_id !== projectID) return;
      this.selectedRecurrences = [];
    }
  }

  private async recurrencesForProject(projectID: number): Promise<KataRecurrence[]> {
    const options = this.workflowRequestOptions();
    const response = options ? await this.api.recurrences(projectID, options) : await this.api.recurrences(projectID);
    return response.recurrences;
  }
}

export function createKataWorkspaceStore(options: CreateKataWorkspaceStoreOptions = {}): KataWorkspaceStore {
  return new KataWorkspaceStore(options);
}
