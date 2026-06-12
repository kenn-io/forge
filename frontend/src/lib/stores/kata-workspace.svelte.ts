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

interface KataBootstrapOptions {
  selectFirst?: boolean | undefined;
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
  searchFilters = $state.raw<KataTaskSearchFilters>(defaultKataTaskSearchFilters());
  duplicateCandidates = $state.raw<KataDuplicateCandidateDisplay[]>([]);
  pendingSelectionUID = $state<string | null>(null);

  private viewRequestID = 0;
  private detailRequestID = 0;
  private detailAbort: AbortController | null = null;
  private unscopedViewName: KataTaskViewName = "today";
  private issueETags = new Map<string, string>();
  private metadataQueues = new Map<string, Promise<void>>();

  constructor(options: CreateKataWorkspaceStoreOptions = {}) {
    this.api = options.api ?? createKataTaskAPI();
  }

  async bootstrap(
    viewName: KataTaskViewName = "today",
    preferredIssueUID?: string | null,
    options: KataBootstrapOptions = {},
  ): Promise<void> {
    const requestID = ++this.viewRequestID;
    this.connection = { status: "connecting" };
    try {
      await this.api.instance();
      const [projects, view] = await Promise.all([this.api.projects(), this.api.issues({ view: viewName })]);
      if (requestID !== this.viewRequestID) {
        this.connection = { status: "online" };
        return;
      }
      this.projects = projects.projects;
      this.areas = deriveKataAreas(projects.projects);
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
      await this.loadSelectedIssue(nextUID ?? preferredIssueUID ?? null, requestID, ++this.detailRequestID);
      this.connection = { status: "online" };
    } catch (error) {
      if (requestID !== this.viewRequestID) return;
      this.connection = {
        status: "error",
        message: connectionErrorMessage(error),
      };
      throw error;
    }
  }

  async openView(viewName: KataTaskViewName, options: KataLoadOptions = {}): Promise<void> {
    if (!shouldApplyLoad(options)) return;
    const requestID = ++this.viewRequestID;
    const view = await this.api.issues({ view: viewName, ...scopedIssueQuery(this.searchFilters) });
    if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
    this.currentView = {
      name: view.view,
      groups: view.groups,
      fetched_at: view.fetched_at,
    };
    if (this.searchFilters.scope.kind === "all") {
      this.unscopedViewName = view.view;
    }
    const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(view.groups)[0];
    await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID);
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

    const requestID = ++this.viewRequestID;
    try {
      const results = await this.api.search(this.searchFilters);
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.duplicateCandidates = [];
      const groups = groupSearchIssues(results.issues);
      this.currentView = {
        name: isProjectBacklogScope(this.searchFilters) ? "all" : this.currentView.name,
        groups,
        fetched_at: results.fetched_at,
      };
      const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(groups)[0];
      await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID);
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
      const requestID = ++this.viewRequestID;
      const view = await this.api.issues({ view: nextUnscopedViewName });
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.searchFilters = nextFilters;
      this.duplicateCandidates = [];
      this.currentView = {
        name: view.view,
        groups: view.groups,
        fetched_at: view.fetched_at,
      };
      if (nextFilters.scope.kind === "all") {
        this.unscopedViewName = view.view;
      }
      const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(view.groups)[0];
      await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID);
      return;
    }

    const requestID = ++this.viewRequestID;
    try {
      const results = await this.api.search(nextFilters);
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      if (previousFilters.scope.kind === "all" && nextFilters.scope.kind === "project") {
        this.unscopedViewName = this.currentView.name;
      }
      this.searchFilters = nextFilters;
      this.duplicateCandidates = [];
      const groups = groupSearchIssues(results.issues);
      this.currentView = {
        name: isProjectBacklogScope(nextFilters) ? "all" : this.currentView.name,
        groups,
        fetched_at: results.fetched_at,
      };
      const firstIssue = options.selectFirst === false ? undefined : selectableViewIssues(groups)[0];
      await this.loadSelectedIssue(firstIssue?.uid ?? null, requestID, ++this.detailRequestID);
    } catch (error) {
      if (requestID !== this.viewRequestID || !shouldApplyLoad(options)) return;
      this.searchFilters = nextFilters;
      this.duplicateCandidates = duplicateCandidatesFromError(error);
      if (this.duplicateCandidates.length === 0) throw error;
    }
  }

  async createProject(name: string): Promise<KataProjectSummary> {
    const project = await this.api.createProject(name);
    await this.reloadProjects();
    return project;
  }

  async captureIssue(actor: string, draft: KataTaskCreateDraft, idempotencyKey = createULID()): Promise<void> {
    const inbox =
      this.projects.find(isTaskInboxProject) ??
      (await this.api.projects()).projects.find((project) => isTaskInboxProject(project));
    if (!inbox) throw new Error("task inbox project is not available");

    const result = await this.api.createIssue(inbox.id, actor, draft, idempotencyKey);
    this.captureMutationETag(result);
    await this.reloadProjects();

    const requestID = ++this.viewRequestID;
    this.searchFilters = defaultKataTaskSearchFilters();
    this.duplicateCandidates = [];
    const view = await this.api.issues({ view: "inbox" });
    if (requestID !== this.viewRequestID) return;
    this.currentView = {
      name: "inbox",
      groups: view.groups,
      fetched_at: view.fetched_at,
    };
    this.unscopedViewName = "inbox";
    await this.loadSelectedIssue(result.issue?.uid ?? null, requestID, ++this.detailRequestID);
  }

  async renameProject(projectID: number, name: string): Promise<void> {
    await this.api.renameProject(projectID, name);
    await this.reloadProjects();
    await this.refreshCurrentView(this.selectedIssue?.issue.uid);
  }

  async selectIssue(uid: string): Promise<boolean> {
    this.pendingSelectionUID = uid;
    const requestID = ++this.detailRequestID;
    try {
      return await this.loadSelectedIssue(uid, undefined, requestID);
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
    await this.mutateIssue(uid, (target) => this.api.closeIssue(target, actor, options));
    await this.reloadProjects();
  }

  async reopenIssue(uid: string, actor: string): Promise<void> {
    await this.mutateIssue(uid, (target) => this.api.reopenIssue(target, actor));
    await this.reloadProjects();
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
    const issue = this.issueForMutation(uid);
    const selectedETag = this.selectedIssue?.issue.uid === uid ? this.selectedIssue.etag : undefined;
    const ifMatch = this.issueETags.get(uid) ?? selectedETag ?? `"rev-${issue.revision}"`;
    await this.mutateIssue(uid, (target) => this.api.moveIssue(target, actor, toProjectUID, ifMatch));
    await this.reloadProjects();
  }

  async createRecurrence(
    projectID: number,
    input: KataCreateRecurrenceInput,
  ): Promise<KataRecurrenceResponse["recurrence"]> {
    const response = await this.api.createRecurrence(projectID, input);
    await this.refreshSelectedRecurrences();
    return response.recurrence;
  }

  async patchRecurrence(
    id: number,
    input: KataPatchRecurrenceInput,
    ifMatch: string,
  ): Promise<KataRecurrenceResponse["recurrence"]> {
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
  }

  async deleteRecurrence(id: number, actor: string): Promise<void> {
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
  }

  resetSearchFilters(): void {
    this.searchFilters = defaultKataTaskSearchFilters();
    this.duplicateCandidates = [];
  }

  invalidatePendingLoads(): void {
    this.viewRequestID++;
    this.detailRequestID++;
    this.abortPendingDetail();
    this.pendingSelectionUID = null;
  }

  // Whenever a detail load stops being wanted (newer selection, cleared
  // selection, route invalidation), abort it: a superseded request left
  // running ties up the daemon and its late rejection would surface a
  // stale error for a navigation the user already left.
  private abortPendingDetail(): void {
    this.detailAbort?.abort();
    this.detailAbort = null;
  }

  resetEventCursor(): void {
    this.eventCursor = 0;
  }

  async syncEventCursor(): Promise<void> {
    let afterID = this.eventCursor;
    for (;;) {
      const response = await this.api.events({ after_id: afterID, limit: 100 });
      const nextAfterID = Math.max(afterID, response.next_after_id, ...response.events.map((event) => event.event_id));
      if (response.reset_required) {
        await this.applyEventStreamMessage({
          kind: "reset",
          event_id: nextAfterID,
          reset_after_id: response.reset_after_id ?? nextAfterID,
          lastEventID: nextAfterID,
        });
      } else {
        for (const event of response.events) {
          const refreshed = await this.applyRemoteEvent(event);
          if (!refreshed) return;
        }
        this.eventCursor = Math.max(this.eventCursor, nextAfterID);
      }
      if (response.events.length === 0 || nextAfterID === afterID) return;
      afterID = nextAfterID;
    }
  }

  async applyRemoteEvent(event: KataTaskEvent): Promise<boolean> {
    if (event.event_id <= this.eventCursor) return true;
    if (event.type.startsWith("project.")) {
      await this.reloadProjects();
    }
    const preferredUID = this.pendingSelectionUID ?? this.selectedIssue?.issue.uid ?? null;
    const refreshed = await this.refreshCurrentView(preferredUID);
    if (!refreshed) return false;
    this.applyTrivialMetadataEvent(event);
    this.eventCursor = event.event_id;
    return true;
  }

  async applyEventStreamMessage(message: KataTaskEventStreamMessage): Promise<void> {
    if (message.kind === "reset") {
      const refreshed = await this.refreshCurrentView(
        this.pendingSelectionUID ?? this.selectedIssue?.issue.uid ?? null,
      );
      if (refreshed) {
        this.eventCursor = Math.max(this.eventCursor, message.reset_after_id);
      }
      return;
    }
    await this.applyRemoteEvent(message.event);
  }

  private async reloadProjects(): Promise<void> {
    const projects = await this.api.projects();
    this.projects = projects.projects;
    this.areas = deriveKataAreas(projects.projects);
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

  private async mutateIssue(
    uid: string,
    operation: (target: KataTaskMutationTarget) => Promise<KataTaskMutationResponse>,
    options: { preserveSelection?: boolean } = {},
  ): Promise<void> {
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
    if (issue?.uid && etag) {
      this.issueETags.set(issue.uid, etag);
    }
  }

  private async refreshCurrentView(preferredUID?: string | null): Promise<boolean> {
    const requestID = ++this.viewRequestID;
    // Selection epoch at refresh start: any selection or clear that lands
    // while the view fetch below is in flight bumps detailRequestID,
    // which makes the preferredUID captured by the caller stale.
    const selectionEpoch = this.detailRequestID;
    let nextView: KataCurrentView;
    let issues: KataTaskSummary[];
    if (shouldRefreshViaSearch(this.searchFilters, this.currentView.name)) {
      let results: KataTaskSearchResponse;
      try {
        results = await this.api.search(this.searchFilters);
      } catch (error) {
        if (requestID !== this.viewRequestID) return false;
        this.duplicateCandidates = duplicateCandidatesFromError(error);
        if (this.duplicateCandidates.length === 0) throw error;
        return false;
      }
      if (requestID !== this.viewRequestID) return false;
      this.duplicateCandidates = [];
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
        view = await this.api.issues({ view: this.currentView.name, ...scopedIssueQuery(this.searchFilters) });
      } catch (error) {
        if (requestID !== this.viewRequestID) return false;
        this.duplicateCandidates = duplicateCandidatesFromError(error);
        if (this.duplicateCandidates.length === 0) throw error;
        return false;
      }
      if (requestID !== this.viewRequestID) return false;
      this.duplicateCandidates = [];
      nextView = {
        name: view.view,
        groups: view.groups,
        fetched_at: view.fetched_at,
      };
      issues = selectableViewIssues(view.groups);
    }
    this.currentView = nextView;
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
    return await this.loadSelectedIssue(nextSelectedUID, requestID, ++this.detailRequestID);
  }

  private async loadSelectedIssue(
    uid: string | null,
    viewRequestID: number | undefined,
    detailRequestID: number,
  ): Promise<boolean> {
    this.abortPendingDetail();

    if (!uid) {
      if (viewRequestID === undefined || viewRequestID === this.viewRequestID) {
        this.selectedIssue = null;
        this.selectedEvents = [];
        this.selectedRecurrences = [];
        this.pendingSelectionUID = null;
        return true;
      }
      return false;
    }

    const abort = new AbortController();
    this.detailAbort = abort;
    let detail: KataTaskDetail;
    let events: KataTaskEventsResponse;
    try {
      [detail, events] = await Promise.all([
        this.api.issue(uid, { signal: abort.signal }),
        this.api.events({ issue_uid: uid, limit: 100 }, { signal: abort.signal }),
      ]);
    } catch (error) {
      // Aborted means superseded: a newer selection owns the pane now, so
      // this failure must not surface as a user-facing error.
      if (abort.signal.aborted) return false;
      throw error;
    } finally {
      if (this.detailAbort === abort) this.detailAbort = null;
    }
    if (viewRequestID !== undefined && viewRequestID !== this.viewRequestID) return false;
    if (detailRequestID !== this.detailRequestID) return false;
    this.selectedIssue = detail;
    if (detail.etag) {
      this.issueETags.set(detail.issue.uid, detail.etag);
    } else {
      this.issueETags.set(detail.issue.uid, `"rev-${detail.issue.revision}"`);
    }
    this.selectedEvents = events.events;
    this.selectedRecurrences = [];
    this.pendingSelectionUID = null;
    void this.loadSelectedRecurrences(detail.issue.project_id, detailRequestID);
    return true;
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
    const response = await this.api.recurrences(projectID);
    return response.recurrences;
  }
}

export function createKataWorkspaceStore(options: CreateKataWorkspaceStoreOptions = {}): KataWorkspaceStore {
  return new KataWorkspaceStore(options);
}
