import { getActiveKataDaemon, getDefaultKataDaemon } from "../../stores/active-kata-daemon.svelte.js";
import { fetchKataDaemons, KATA_DAEMON_HEADER, kataProxyPath, kataTaskDetailPath, withKataDaemon } from "./daemons.js";
import {
  normalizeKataEvents,
  normalizeKataInstance,
  normalizeKataProject,
  normalizeKataProjectList,
  normalizeKataReachableGraph,
  normalizeKataRecurrenceResponse,
  normalizeKataRecurrences,
  normalizeKataTaskDetail,
  normalizeKataTaskList,
  normalizeKataTaskSummary,
} from "./taskNormalizers.js";
import type {
  KataCreateRecurrenceInput,
  KataProjectMetadataPatch,
  KataProjectMutationResponse,
  KataProjectSummary,
  KataReachableGraphQuery,
  KataReachableGraphResponse,
  KataTaskAPI,
  KataTaskCloseOptions,
  KataTaskDetail,
  KataTaskEditPatch,
  KataTaskEventsQuery,
  KataTaskEventsResponse,
  KataTaskIssuesQuery,
  KataTaskMetadataPatch,
  KataTaskMoveResponse,
  KataTaskMutationResponse,
  KataTaskMutationTarget,
  KataTaskSearchFilters,
  KataTaskSearchResponse,
  KataTaskSummary,
  KataWorkspaceTarget,
} from "./taskTypes.js";
import { buildKataTaskView } from "./taskViewBuilder.js";
import { localDateString } from "../dates.js";

export interface CreateKataTaskAPIOptions {
  fetchImpl?: typeof fetch | undefined;
  getDaemonId?: (() => string | undefined) | undefined;
  getDefaultDaemonId?: (() => string | undefined) | undefined;
}

interface RequestResult<T> {
  body: T;
  headers: Headers;
}

// The explicit `| undefined` unions are required by
// exactOptionalPropertyTypes: call sites pass values that may be
// undefined (e.g. daemonHeaders() or an optional caller signal).
interface KataRequestInit {
  method?: string | undefined;
  body?: unknown;
  headers?: Record<string, string> | undefined;
  signal?: AbortSignal | undefined;
  // The path is already app-rooted (a middleman API route) instead of a
  // daemon path to send through the passthrough proxy.
  appRoute?: boolean | undefined;
}

interface ErrorEnvelope {
  code: string;
  message: string;
  details?: unknown;
}

const KATA_TASK_API_PREFIX = "/api" + "/v1";

// Page size for issue-scoped event-log walks. Both TCP and unix-socket
// daemons accept limit=1000 on /events; larger pages keep the walk to a
// handful of round trips even on multi-thousand-event logs.
const KATA_EVENTS_SCAN_PAGE_LIMIT = 1000;
const responseHeaders = new WeakMap<KataTaskAPI, Headers>();

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stableEqual(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false;
    return left.every((item, index) => stableEqual(item, right[index]));
  }
  if (isObject(left) || isObject(right)) {
    if (!isObject(left) || !isObject(right)) return false;
    const leftKeys = Object.keys(left).sort();
    const rightKeys = Object.keys(right).sort();
    if (!stableEqual(leftKeys, rightKeys)) return false;
    return leftKeys.every((key) => stableEqual(left[key], right[key]));
  }
  return false;
}

function issueMetadataIncludes(issue: KataTaskSummary, patch: KataTaskMetadataPatch): boolean {
  return Object.entries(patch).every(([key, value]) => stableEqual(issue.metadata[key], value));
}

function parseErrorEnvelope(body: unknown, status: number): ErrorEnvelope {
  const source = isObject(body) && isObject(body.error) ? body.error : body;
  if (isObject(source)) {
    const code = typeof source.code === "string" ? source.code : `http_${status}`;
    const message =
      typeof source.message === "string"
        ? source.message
        : typeof source.detail === "string"
          ? source.detail
          : typeof source.title === "string"
            ? source.title
            : `HTTP ${status}`;
    return { code, message, details: source.details };
  }
  return { code: `http_${status}`, message: `HTTP ${status}` };
}

function taskPath(path: string): string {
  return `${KATA_TASK_API_PREFIX}${path}`;
}

function withProjectIdentity(issue: KataTaskSummary, project?: KataProjectSummary): KataTaskSummary {
  if (!project) return issue;
  return {
    ...issue,
    project_id: issue.project_id || project.id,
    project_uid: issue.project_uid || project.uid,
    project_name: issue.project_name || project.name,
    qualified_id: issue.qualified_id || `${project.name}#${issue.short_id}`,
  };
}

function normalizeSearchResults(raw: unknown, project?: KataProjectSummary): KataTaskSummary[] {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  if (!isObject(source)) return [];
  if (Array.isArray(source.results)) {
    return normalizeKataTaskList({
      issues: source.results.filter((hit): hit is Record<string, unknown> => isObject(hit)).map((hit) => hit.issue),
    })
      .groups.flatMap((group) => group.issues)
      .map((issue) => withProjectIdentity(issue, project));
  }
  return normalizeKataTaskList(source)
    .groups.flatMap((group) => group.issues)
    .map((issue) => withProjectIdentity(issue, project));
}

function issueMatchesScope(
  issue: KataTaskSummary,
  query: Pick<KataTaskIssuesQuery, "project_uid" | "area">,
  projects: Map<string, KataProjectSummary>,
): boolean {
  if (query.project_uid && issue.project_uid !== query.project_uid) return false;
  if (query.area) {
    const area = projects.get(issue.project_uid)?.metadata.area;
    if (typeof area !== "string" || area.toLowerCase() !== query.area.toLowerCase()) return false;
  }
  return true;
}

function eventMatchesQuery(event: KataTaskEventsResponse["events"][number], query: KataTaskEventsQuery): boolean {
  if (query.project_id !== undefined && event.project_id !== query.project_id) return false;
  if (query.issue_uid !== undefined && event.issue_uid !== query.issue_uid) return false;
  return true;
}

function normalizeMutationResponse(raw: unknown, headers?: Headers): KataTaskMutationResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  const body = isObject(source) ? source : {};
  const out: KataTaskMutationResponse = {
    changed: body.changed === true,
    etag: headers?.get("etag") ?? undefined,
    comment: isObject(body.comment) ? (body.comment as unknown as KataTaskMutationResponse["comment"]) : undefined,
    label: isObject(body.label) ? (body.label as unknown as KataTaskMutationResponse["label"]) : undefined,
    event: isObject(body.event) ? (body.event as unknown as KataTaskMutationResponse["event"]) : undefined,
  };
  if (isObject(body.issue)) {
    out.issue = normalizeKataTaskSummary(body.issue);
  }
  return out;
}

function normalizeProjectMutationResponse(raw: unknown, headers?: Headers): KataProjectMutationResponse {
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  const body = isObject(source) ? source : {};
  return {
    changed: body.changed === true,
    etag: headers?.get("etag") ?? undefined,
    project: isObject(body.project) ? normalizeKataProject(body.project) : undefined,
    event: isObject(body.event) ? (body.event as unknown as KataProjectMutationResponse["event"]) : undefined,
  };
}

function normalizeMoveResponse(raw: unknown, headers?: Headers): KataTaskMoveResponse {
  const out = normalizeMutationResponse(raw, headers);
  const source = isObject(raw) && isObject(raw.body) ? raw.body : raw;
  const body = isObject(source) ? source : {};
  return {
    ...out,
    new_short_id: typeof body.new_short_id === "string" ? body.new_short_id : "",
  };
}

function issueSearchText(issue: KataTaskSummary): string {
  return [issue.title, issue.body, issue.qualified_id, issue.project_name, issue.owner, issue.labels?.join(" ")]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

function filterSearchIssues(
  issues: KataTaskSummary[],
  filters: KataTaskSearchFilters,
  options: { applyQuery?: boolean } = {},
): KataTaskSummary[] {
  const query = filters.query.trim().toLowerCase();
  const owner = filters.owner.trim().toLowerCase();
  const label = filters.label.trim().toLowerCase();
  const applyQuery = options.applyQuery ?? true;
  return issues.filter((issue) => {
    if (filters.scope.kind === "project" && issue.project_uid !== filters.scope.project_uid) return false;
    if (filters.status !== "all" && issue.status !== filters.status) return false;
    if (owner && issue.owner?.toLowerCase() !== owner) return false;
    if (label && !(issue.labels ?? []).some((item) => item.toLowerCase() === label)) return false;
    if (applyQuery && query && !issueSearchText(issue).includes(query)) return false;
    return true;
  });
}

export class KataTaskAPIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: unknown;
  readonly headers: Headers;

  constructor(input: { status: number; code: string; message: string; details?: unknown; headers: Headers }) {
    super(input.message);
    this.name = "KataTaskAPIError";
    this.status = input.status;
    this.code = input.code;
    this.details = input.details;
    this.headers = input.headers;
  }
}

export class KataTaskRevisionConflictError extends KataTaskAPIError {
  constructor(input: { status: number; code: string; message: string; details?: unknown; headers: Headers }) {
    super(input);
    this.name = "KataTaskRevisionConflictError";
  }
}

export function getLastKataTaskResponseHeaders(api: KataTaskAPI): Headers | undefined {
  return responseHeaders.get(api);
}

export function createKataTaskAPI(options: CreateKataTaskAPIOptions = {}): KataTaskAPI {
  const getDaemonId = options.getDaemonId ?? getActiveKataDaemon;
  const getDefaultDaemonId = options.getDefaultDaemonId ?? getDefaultKataDaemon;
  const baseFetchImpl = options.fetchImpl ?? fetch;
  let resolvedDefaultDaemonId: string | undefined;
  let workflowDaemonId: string | undefined;
  const getEffectiveDaemonId = () =>
    workflowDaemonId ?? getDaemonId() ?? getDefaultDaemonId() ?? resolvedDefaultDaemonId;
  const fetchImpl = withKataDaemon(baseFetchImpl, getEffectiveDaemonId);
  let api: KataTaskAPI;

  function daemonHeaders(daemonId?: string): Record<string, string> | undefined {
    return daemonId ? { [KATA_DAEMON_HEADER]: daemonId } : undefined;
  }

  function pinnedDaemonHeaders(daemonId?: string): Record<string, string> {
    return { [KATA_DAEMON_HEADER]: daemonId ?? "" };
  }

  async function resolveOperationDaemonId(explicitDaemonId?: string, preferWorkflow = false): Promise<string> {
    const selected =
      explicitDaemonId?.trim() ||
      (preferWorkflow ? workflowDaemonId?.trim() : undefined) ||
      getDaemonId()?.trim() ||
      getDefaultDaemonId()?.trim() ||
      resolvedDefaultDaemonId?.trim();
    if (selected) return selected;

    const daemons = await fetchKataDaemons(baseFetchImpl);
    const fallback = daemons.find((daemon) => daemon.default) ?? daemons[0];
    const fallbackID = fallback?.id.trim();
    if (fallbackID) {
      resolvedDefaultDaemonId = fallbackID;
      return fallbackID;
    }

    throw new KataTaskAPIError({
      status: 503,
      code: "service_unavailable",
      message: "no Kata daemon is available",
      headers: new Headers(),
    });
  }

  async function request<T>(path: string, init: KataRequestInit = {}): Promise<RequestResult<T>> {
    const headers = new Headers(init.headers);
    const requestInit: RequestInit = {
      method: init.method ?? "GET",
      headers,
    };
    if (init.signal) {
      requestInit.signal = init.signal;
    }
    if (init.body !== undefined) {
      headers.set("Content-Type", "application/json");
      requestInit.body = JSON.stringify(init.body);
    }

    const response = await fetchImpl(init.appRoute ? path : kataProxyPath(path), requestInit);
    responseHeaders.set(api, response.headers);

    const text = await response.text();
    let body: unknown = {};
    if (text.trim() !== "") {
      try {
        body = JSON.parse(text);
      } catch {
        body = text;
      }
    }

    if (!response.ok) {
      const envelope = parseErrorEnvelope(body, response.status);
      const input = {
        status: response.status,
        code: envelope.code,
        message: envelope.message,
        details: envelope.details,
        headers: response.headers,
      };
      if (envelope.code === "revision_conflict") {
        throw new KataTaskRevisionConflictError(input);
      }
      throw new KataTaskAPIError(input);
    }

    return { body: body as T, headers: response.headers };
  }

  function issuePath(target: KataTaskMutationTarget): string {
    return taskPath(`/projects/${target.project_id}/issues/${encodeURIComponent(target.ref)}`);
  }

  async function mutate(
    path: string,
    body: unknown,
    method = "POST",
    headers?: Record<string, string>,
  ): Promise<KataTaskMutationResponse> {
    const result = await request<unknown>(path, { method, body, headers });
    return normalizeMutationResponse(result.body, result.headers);
  }

  function patchMetadata(
    path: string,
    actor: string,
    patch: KataTaskMetadataPatch,
    ifMatch: string,
    idempotencyKey?: string,
    daemonId?: string,
    pinned = false,
  ): Promise<KataTaskMutationResponse> {
    return mutate(path, { actor, patch }, "PUT", {
      "If-Match": ifMatch,
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
      ...(pinned ? pinnedDaemonHeaders(daemonId) : daemonHeaders(daemonId)),
    });
  }

  async function fetchProjects(daemonId?: string, pinned = false) {
    const result = await request<unknown>(taskPath("/projects?include=stats"), {
      headers: pinned ? pinnedDaemonHeaders(daemonId) : daemonHeaders(daemonId),
    });
    return normalizeKataProjectList(result.body);
  }

  async function fetchIssuesByStatus(
    status: "open" | "closed",
    daemonId?: string,
    project?: KataProjectSummary,
    pinned = false,
  ): Promise<KataTaskSummary[]> {
    const params = new URLSearchParams();
    params.set("status", status);
    if (status === "closed") params.set("limit", "500");
    const basePath = project ? `/projects/${project.id}/issues` : "/issues";
    const path = taskPath(`${basePath}?${params.toString()}`);
    const result = await request<unknown>(path, {
      headers: pinned ? pinnedDaemonHeaders(daemonId) : daemonHeaders(daemonId),
    });
    return normalizeKataTaskList(result.body)
      .groups.flatMap((group) => group.issues)
      .map((issue) => withProjectIdentity(issue, project));
  }

  async function fetchIssue(
    uid: string,
    daemonId?: string,
    pinned = false,
    signal?: AbortSignal,
  ): Promise<KataTaskDetail> {
    // Middleman's combined read: the daemon detail plus the resolved
    // workspace target in one round trip, so the detail pane and its
    // workspace action render together.
    const result = await request<{ detail?: unknown; etag?: string; workspace_target?: KataWorkspaceTarget }>(
      kataTaskDetailPath(uid),
      {
        headers: pinned ? pinnedDaemonHeaders(daemonId) : daemonHeaders(daemonId),
        signal,
        appRoute: true,
      },
    );
    const body = isObject(result.body) ? result.body : {};
    const detail = normalizeKataTaskDetail(body.detail);
    const etag = typeof body.etag === "string" && body.etag !== "" ? body.etag : undefined;
    const target = isObject(body.workspace_target) ? (body.workspace_target as KataWorkspaceTarget) : undefined;
    return { ...detail, etag, workspace_target: target };
  }

  async function postRecurrence(path: string, input: KataCreateRecurrenceInput) {
    const result = await request<unknown>(path, { method: "POST", body: input });
    return normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined);
  }

  async function searchAllProjects(
    filters: KataTaskSearchFilters,
    daemonId?: string,
    pinned = false,
  ): Promise<KataTaskSummary[]> {
    if (filters.status === "all") {
      const [open, closed] = await Promise.all([
        fetchIssuesByStatus("open", daemonId, undefined, pinned),
        fetchIssuesByStatus("closed", daemonId, undefined, pinned),
      ]);
      return filterSearchIssues([...open, ...closed], filters);
    }
    return filterSearchIssues(await fetchIssuesByStatus(filters.status, daemonId, undefined, pinned), filters);
  }

  async function searchProjectIssueList(
    filters: KataTaskSearchFilters & { scope: { kind: "project"; project_uid: string } },
    project: KataProjectSummary,
    daemonId?: string,
    pinned = false,
  ): Promise<KataTaskSummary[]> {
    if (filters.status === "all") {
      const [open, closed] = await Promise.all([
        fetchIssuesByStatus("open", daemonId, project, pinned),
        fetchIssuesByStatus("closed", daemonId, project, pinned),
      ]);
      return filterSearchIssues([...open, ...closed], filters);
    }
    return filterSearchIssues(await fetchIssuesByStatus(filters.status, daemonId, project, pinned), filters);
  }

  async function hydrateProjectSearchRows(
    issues: KataTaskSummary[],
    filters: KataTaskSearchFilters & { scope: { kind: "project"; project_uid: string } },
    project: KataProjectSummary,
    daemonId?: string,
    pinned = false,
  ): Promise<KataTaskSummary[]> {
    if (filters.label.trim() === "" || issues.length === 0) return issues;
    const rows = await searchProjectIssueList(
      {
        ...filters,
        query: "",
        label: "",
      },
      project,
      daemonId,
      pinned,
    );
    const byUID = new Map(rows.map((issue) => [issue.uid, issue]));
    return issues.map((issue) => ({
      ...issue,
      labels: byUID.get(issue.uid)?.labels ?? issue.labels,
    }));
  }

  async function searchProject(
    filters: KataTaskSearchFilters & { scope: { kind: "project"; project_uid: string } },
    daemonId?: string,
    pinned = false,
  ) {
    const projects = await fetchProjects(daemonId, pinned);
    const project = projects.projects.find((item) => item.uid === filters.scope.project_uid);
    if (!project) {
      return filterSearchIssues([], filters);
    }
    if (filters.query.trim() === "") {
      return searchProjectIssueList(filters, project, daemonId, pinned);
    }
    const params = new URLSearchParams();
    params.set("q", filters.query);
    const result = await request<unknown>(taskPath(`/projects/${project.id}/search?${params.toString()}`), {
      headers: pinned ? pinnedDaemonHeaders(daemonId) : daemonHeaders(daemonId),
    });
    const issues = await hydrateProjectSearchRows(
      normalizeSearchResults(result.body, project),
      filters,
      project,
      daemonId,
      pinned,
    );
    return filterSearchIssues(issues, filters, { applyQuery: false });
  }

  api = {
    bindWorkflowDaemon(daemonId) {
      workflowDaemonId = daemonId?.trim() || undefined;
    },

    async instance() {
      const result = await request<unknown>(taskPath("/instance"));
      return normalizeKataInstance(result.body);
    },

    projects() {
      return fetchProjects();
    },

    async createProject(name) {
      const result = await request<unknown>(taskPath("/projects"), {
        method: "POST",
        body: { name },
      });
      const source = isObject(result.body) && isObject(result.body.body) ? result.body.body : result.body;
      const project = isObject(source) && isObject(source.project) ? normalizeKataProject(source.project) : undefined;
      if (!project) {
        throw new KataTaskAPIError({
          status: 500,
          code: "invalid_project_response",
          message: "project create response did not include a project",
          headers: result.headers,
        });
      }
      return project;
    },

    async renameProject(projectID, name) {
      const result = await request<unknown>(taskPath(`/projects/${projectID}`), {
        method: "PATCH",
        body: { name },
      });
      const source = isObject(result.body) && isObject(result.body.body) ? result.body.body : result.body;
      const project = isObject(source) && isObject(source.project) ? normalizeKataProject(source.project) : undefined;
      if (!project) {
        throw new KataTaskAPIError({
          status: 500,
          code: "invalid_project_response",
          message: "project rename response did not include a project",
          headers: result.headers,
        });
      }
      return project;
    },

    async patchProjectMetadata(projectID: number, actor: string, patch: KataProjectMetadataPatch, ifMatch: string) {
      const result = await request<unknown>(taskPath(`/projects/${projectID}/metadata`), {
        method: "POST",
        body: { actor, patch },
        headers: { "If-Match": ifMatch },
      });
      return normalizeProjectMutationResponse(result.body, result.headers);
    },

    async createIssue(projectID, actor, draft, idempotencyKey) {
      const daemonId = await resolveOperationDaemonId(undefined, true);
      const { metadata, ...createDraft } = draft;
      const result = await request<unknown>(taskPath(`/projects/${projectID}/issues`), {
        method: "POST",
        body: { actor, ...createDraft },
        headers: {
          ...pinnedDaemonHeaders(daemonId),
          ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
        },
      });
      const created = normalizeMutationResponse(result.body, result.headers);
      if (!metadata || Object.keys(metadata).length === 0) return created;
      if (!created.issue) {
        throw new KataTaskAPIError({
          status: 500,
          code: "invalid_issue_response",
          message: "issue create response did not include an issue",
          headers: result.headers,
        });
      }
      const issueUID = created.issue.uid;
      try {
        return await patchMetadata(
          taskPath(`/projects/${projectID}/issues/${encodeURIComponent(issueUID)}/metadata`),
          actor,
          metadata,
          created.etag ?? `"rev-${created.issue.revision}"`,
          idempotencyKey ? `${idempotencyKey}:metadata` : undefined,
          daemonId,
          true,
        );
      } catch (error) {
        if (!(error instanceof KataTaskRevisionConflictError)) throw error;
        const current = await fetchIssue(issueUID, daemonId, true);
        if (!issueMetadataIncludes(current.issue, metadata)) throw error;
        return {
          changed: false,
          etag: current.etag,
          issue: current.issue,
        };
      }
    },

    async issues(query, opts) {
      const daemonId = await resolveOperationDaemonId(opts?.daemonId);
      const status = query.view === "logbook" ? "closed" : "open";
      const genericIssuesPromise =
        query.project_uid === undefined ? fetchIssuesByStatus(status, daemonId, undefined, true) : undefined;
      const projectsPromise = fetchProjects(daemonId, true);
      const issuesPromise =
        genericIssuesPromise ??
        projectsPromise.then((projects) => {
          const project = projects.projects.find((item) => item.uid === query.project_uid);
          return project ? fetchIssuesByStatus(status, daemonId, project, true) : [];
        });
      const [issues, projects] = await Promise.all([issuesPromise, projectsPromise]);
      const projectMap = new Map(projects.projects.map((project) => [project.uid, project]));
      const scopedIssues = issues.filter((issue) => issueMatchesScope(issue, query, projectMap));
      const view = buildKataTaskView({
        view: query.view,
        issues: scopedIssues,
        projects: projects.projects,
        today: localDateString(),
        fetched_at: new Date().toISOString(),
      });
      return { ...view, daemon_id: daemonId };
    },

    async search(filters, opts): Promise<KataTaskSearchResponse> {
      const daemonId = await resolveOperationDaemonId(opts?.daemonId);
      const issues =
        filters.scope.kind === "project"
          ? await searchProject(
              filters as KataTaskSearchFilters & { scope: { kind: "project"; project_uid: string } },
              daemonId,
              true,
            )
          : await searchAllProjects(filters, daemonId, true);
      const response = {
        filters,
        issues,
        fetched_at: new Date().toISOString(),
        daemon_id: daemonId,
      };
      return response;
    },

    async issue(uid, opts) {
      return fetchIssue(uid, opts?.daemonId, opts?.pinned, opts?.signal);
    },

    async reachableGraph(
      projectID: number,
      ref: string,
      query: KataReachableGraphQuery = {},
      opts,
    ): Promise<KataReachableGraphResponse> {
      const params = new URLSearchParams();
      params.set("depth", query.depth ?? "full");
      if (query.hide_done) params.set("hide_done", "true");
      const result = await request<unknown>(
        taskPath(`/projects/${projectID}/issues/${encodeURIComponent(ref)}/graph?${params.toString()}`),
        {
          headers: daemonHeaders(opts?.daemonId),
          signal: opts?.signal,
        },
      );
      return normalizeKataReachableGraph(result.body);
    },

    async events(query = {}, opts) {
      const daemonId = await resolveOperationDaemonId(undefined, true);

      async function fetchPage(afterID: number | undefined, pageLimit: number): Promise<KataTaskEventsResponse> {
        const params = new URLSearchParams();
        if (query.project_id !== undefined) params.set("project_id", String(query.project_id));
        if (afterID !== undefined) params.set("after_id", String(afterID));
        params.set("limit", String(pageLimit));
        const suffix = params.toString() ? `?${params.toString()}` : "";
        const result = await request<unknown>(taskPath(`/events${suffix}`), {
          headers: pinnedDaemonHeaders(daemonId),
          signal: opts?.signal,
        });
        return normalizeKataEvents(result.body);
      }

      if (query.issue_uid !== undefined && query.limit !== undefined) {
        let afterID = query.after_id;
        let lastResponse: KataTaskEventsResponse | undefined;
        const events: KataTaskEventsResponse["events"] = [];
        // The daemon has no server-side issue_uid filter, so this walks the
        // log and filters client-side. Page far beyond the requested limit:
        // paging at query.limit means one round trip per few matches, which
        // takes seconds against remote daemons.
        const pageLimit = Math.max(query.limit, KATA_EVENTS_SCAN_PAGE_LIMIT);

        for (;;) {
          const response = await fetchPage(afterID, pageLimit);
          const filtered = response.events.filter((event) => eventMatchesQuery(event, query));
          events.push(...filtered);
          lastResponse = response;

          const cursor = Math.max(
            afterID ?? 0,
            response.next_after_id,
            ...response.events.map((event) => event.event_id),
          );
          if (events.length >= query.limit || response.events.length === 0 || cursor === (afterID ?? 0)) {
            break;
          }
          afterID = cursor;
        }

        const response = lastResponse ?? {
          reset_required: false,
          events: [],
          next_after_id: query.after_id ?? 0,
        };
        return {
          ...response,
          events: events.slice(0, query.limit),
        };
      }

      const params = new URLSearchParams();
      if (query.project_id !== undefined) params.set("project_id", String(query.project_id));
      if (query.after_id !== undefined) params.set("after_id", String(query.after_id));
      if (query.limit !== undefined && query.issue_uid === undefined) params.set("limit", String(query.limit));
      const suffix = params.toString() ? `?${params.toString()}` : "";
      const result = await request<unknown>(taskPath(`/events${suffix}`), {
        headers: pinnedDaemonHeaders(daemonId),
        signal: opts?.signal,
      });
      const events = normalizeKataEvents(result.body);
      return {
        ...events,
        events: events.events.filter((event) => eventMatchesQuery(event, query)),
      };
    },

    addComment(target, actor, body) {
      return mutate(`${issuePath(target)}/comments`, { actor, body });
    },

    addLabel(target, actor, label) {
      return mutate(`${issuePath(target)}/labels`, { actor, label });
    },

    removeLabel(target, actor, label) {
      const path = `${issuePath(target)}/labels/${encodeURIComponent(label)}?actor=${encodeURIComponent(actor)}`;
      return mutate(path, undefined, "DELETE");
    },

    assignOwner(target, actor, owner) {
      return mutate(`${issuePath(target)}/actions/assign`, { actor, owner });
    },

    unassignOwner(target, actor) {
      return mutate(`${issuePath(target)}/actions/unassign`, { actor });
    },

    setPriority(target, actor, priority) {
      return mutate(`${issuePath(target)}/actions/priority`, { actor, priority });
    },

    closeIssue(target, actor, options: KataTaskCloseOptions = {}) {
      return mutate(`${issuePath(target)}/actions/close`, { actor, ...options });
    },

    reopenIssue(target, actor) {
      return mutate(`${issuePath(target)}/actions/reopen`, { actor });
    },

    editIssue(target, actor, patch: KataTaskEditPatch) {
      return mutate(issuePath(target), { actor, ...patch }, "PATCH");
    },

    patchIssueMetadata(target, actor, patch, ifMatch) {
      return patchMetadata(`${issuePath(target)}/metadata`, actor, patch, ifMatch);
    },

    async moveIssue(target, actor, toProjectUID, ifMatch) {
      const result = await request<unknown>(`${issuePath(target)}/actions/move`, {
        method: "POST",
        body: { actor, to_project_uid: toProjectUID },
        headers: { "If-Match": ifMatch },
      });
      return normalizeMoveResponse(result.body, result.headers);
    },

    async recurrences(projectID) {
      const result = await request<unknown>(taskPath(`/projects/${projectID}/recurrences`));
      return normalizeKataRecurrences(result.body);
    },

    createRecurrence(projectID, input) {
      return postRecurrence(taskPath(`/projects/${projectID}/recurrences`), input);
    },

    async showRecurrence(projectID, recurrenceUID) {
      const result = await request<unknown>(
        taskPath(`/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}`),
      );
      return normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined);
    },

    async patchRecurrence(projectID, recurrenceUID, patch, ifMatch) {
      const result = await request<unknown>(
        taskPath(`/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}`),
        {
          method: "PATCH",
          body: patch,
          headers: { "If-Match": ifMatch },
        },
      );
      return normalizeKataRecurrenceResponse(result.body, result.headers.get("etag") ?? undefined);
    },

    async deleteRecurrence(projectID, recurrenceUID, actor, ifMatch) {
      await request<unknown>(
        taskPath(
          `/projects/${projectID}/recurrences/${encodeURIComponent(recurrenceUID)}?actor=${encodeURIComponent(actor)}`,
        ),
        {
          method: "DELETE",
          headers: ifMatch ? { "If-Match": ifMatch } : undefined,
        },
      );
    },
  };

  return api;
}
