import { vi } from "vite-plus/test";
import { Effect } from "effect";

import {
  KATA_DAEMON_HEADER,
  type KataAuthority,
  type KataAuthorityScope,
  type KataWorkspaceSnapshotResponse,
} from "../../../api/kata/snapshot.js";
import type {
  KataProjectSummary,
  KataReachableGraphResponse,
  KataRecurrence,
  KataTaskAPI,
  KataTaskDetail,
  KataTaskEvent,
  KataTaskSummary,
} from "../../../api/kata/taskTypes.js";
import {
  getActiveKataDaemon,
  getDefaultKataDaemon,
  setActiveKataDaemon,
  setKataDaemonRoster,
} from "../../../stores/active-kata-daemon.svelte.js";

export class TestResizeObserver implements ResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

type SnapshotProject = NonNullable<KataWorkspaceSnapshotResponse["projects"]>[number];
type SnapshotIssue = NonNullable<KataWorkspaceSnapshotResponse["issues"]>[number];
type SnapshotEnrichment = KataWorkspaceSnapshotResponse["enrichment"];
type SnapshotGraph = NonNullable<SnapshotEnrichment["graph"]>;
type SnapshotHistoryEvent = NonNullable<SnapshotEnrichment["selected_history"]>[number];

interface KataWorkspaceSnapshotFixtureRequest {
  daemonID: string;
  scope: KataAuthorityScope;
  projectUID?: string | undefined;
  authority: KataAuthority;
  selectedIssueUID?: string | undefined;
  graphSourceUID?: string | undefined;
}

type KataWorkspaceSnapshotFixtureOverride = (
  request: KataWorkspaceSnapshotFixtureRequest,
  snapshot: KataWorkspaceSnapshotResponse,
) => KataWorkspaceSnapshotResponse | Promise<KataWorkspaceSnapshotResponse>;

interface KataWorkspaceSnapshotFixtureSource {
  rows(daemonID: string): readonly KataTaskSummary[];
  projects(daemonID: string): readonly KataProjectSummary[];
  detail(uid: string, daemonID: string): KataTaskDetail | undefined;
  events(uid: string, daemonID: string): readonly KataTaskEvent[];
  eventCursor(daemonID: string): number;
  graph(uid: string, daemonID: string): KataReachableGraphResponse | undefined;
  override?: KataWorkspaceSnapshotFixtureOverride | undefined;
}

let activeSnapshotFixture: KataWorkspaceSnapshotFixtureSource | null = null;
let snapshotFetchInstalled = false;
let snapshotGeneration = 0;
const defaultFetch = globalThis.fetch.bind(globalThis);

function requestURL(input: RequestInfo | URL): URL {
  if (input instanceof Request) return new URL(input.url);
  return input instanceof URL ? input : new URL(String(input), window.location.origin);
}

function requestHeaders(input: RequestInfo | URL, init?: RequestInit): Headers {
  return new Headers(input instanceof Request ? input.headers : init?.headers);
}

function snapshotProject(project: KataProjectSummary): SnapshotProject {
  return {
    id: project.id,
    uid: project.uid,
    name: project.name,
    metadata: project.metadata,
    revision: project.revision ?? 1,
    created_at: project.created_at ?? fetchedAt,
    open_count: project.open_count,
    closed_count: 0,
    ...(project.deleted_at ? { deleted_at: project.deleted_at } : {}),
  };
}

function snapshotIssue(issue: KataTaskSummary): SnapshotIssue {
  return { ...issue } as SnapshotIssue;
}

function matchesAuthority(issue: KataTaskSummary, authority: KataAuthority): boolean {
  if (authority === "all") return true;
  if (authority === "closed") return issue.status === "closed";
  return issue.status === "open";
}

async function snapshotResponse(
  source: KataWorkspaceSnapshotFixtureSource,
  request: KataWorkspaceSnapshotFixtureRequest,
): Promise<KataWorkspaceSnapshotResponse> {
  const daemonRows = source.rows(request.daemonID);
  const memberRows = daemonRows.filter(
    (item) =>
      matchesAuthority(item, request.authority) &&
      (request.scope !== "project" || item.project_uid === request.projectUID),
  );
  const selectedDetail = request.selectedIssueUID
    ? source.detail(request.selectedIssueUID, request.daemonID)
    : undefined;
  const selectedHistory = request.selectedIssueUID ? source.events(request.selectedIssueUID, request.daemonID) : [];
  const graph = request.graphSourceUID ? source.graph(request.graphSourceUID, request.daemonID) : undefined;
  const generation = ++snapshotGeneration;
  const snapshot: KataWorkspaceSnapshotResponse = {
    server_instance_id: "test-server",
    daemon_id: request.daemonID,
    intent: {
      scope: request.scope,
      ...(request.scope === "project" ? { project_uid: request.projectUID! } : {}),
      authority: request.authority,
    },
    generation,
    invalidation_epoch: generation,
    event_cursor: source.eventCursor(request.daemonID),
    fetched_at: fetchedAt,
    projects: source.projects(request.daemonID).map(snapshotProject),
    member_issue_uids: memberRows.map((item) => item.uid),
    issues: memberRows.map(snapshotIssue),
    enrichment: {
      ...(request.selectedIssueUID && selectedDetail
        ? {
            selected_issue_uid: request.selectedIssueUID,
            selected_detail: {
              detail: selectedDetail,
              ...(selectedDetail.etag ? { etag: selectedDetail.etag } : {}),
              workspace_target: selectedDetail.workspace_target ?? { available: false },
            },
            selected_history: selectedHistory.map((event) => ({ ...event }) as SnapshotHistoryEvent),
          }
        : {}),
      ...(graph
        ? {
            graph: graph as SnapshotGraph,
            graph_fetched_at: graph.fetched_at,
          }
        : {}),
    },
    ...(request.graphSourceUID ? { graph_source_uid: request.graphSourceUID } : {}),
  };
  return source.override ? source.override(request, snapshot) : snapshot;
}

function installKataWorkspaceSnapshotFixture(source: KataWorkspaceSnapshotFixtureSource): void {
  activeSnapshotFixture = source;
  if (snapshotFetchInstalled) return;

  const currentFetch = globalThis.fetch;
  const delegate =
    (vi.isMockFunction(currentFetch) ? vi.mocked(currentFetch).getMockImplementation() : undefined) ??
    (vi.isMockFunction(currentFetch) ? defaultFetch : currentFetch.bind(globalThis));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = requestURL(input);
    if (url.pathname !== "/api/v1/kata/tasks/snapshot") {
      return delegate(input, init);
    }
    const fixture = activeSnapshotFixture;
    if (!fixture) return new Response("No Kata workspace snapshot fixture is installed.", { status: 500 });

    const scope = url.searchParams.get("scope");
    const authority = url.searchParams.get("authority");
    const projectUID = url.searchParams.get("project_uid") ?? undefined;
    if (
      (scope !== "global" && scope !== "project") ||
      (scope === "project" && !projectUID) ||
      !["open", "ready", "closed", "all"].includes(authority ?? "")
    ) {
      return new Response("Invalid Kata workspace snapshot request.", { status: 400 });
    }
    const daemonID =
      requestHeaders(input, init).get(KATA_DAEMON_HEADER) ?? getActiveKataDaemon() ?? getDefaultKataDaemon() ?? "home";
    return Response.json(
      await snapshotResponse(fixture, {
        daemonID,
        scope,
        ...(projectUID ? { projectUID } : {}),
        authority: authority as KataAuthority,
        selectedIssueUID: url.searchParams.get("selected_issue_uid") ?? undefined,
        graphSourceUID: url.searchParams.get("graph_source_uid") ?? undefined,
      }),
    );
  });
  snapshotFetchInstalled = true;
}

export function resetKataWorkspaceTestState(): void {
  activeSnapshotFixture = null;
  snapshotFetchInstalled = false;
  snapshotGeneration = 0;
  if (!("ResizeObserver" in globalThis)) {
    Object.defineProperty(globalThis, "ResizeObserver", {
      configurable: true,
      value: TestResizeObserver,
    });
  }
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
  localStorage.clear();
  setActiveKataDaemon(undefined);
  setKataDaemonRoster([], undefined);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const fetchedAt = "2026-05-15T16:00:00.000Z";

function project(
  uid: string,
  name: string,
  metadata: KataProjectSummary["metadata"] = {},
  openCount = 0,
): KataProjectSummary {
  return {
    id: Number(uid.replace(/\D/g, "")) || 1,
    uid,
    name,
    metadata,
    open_count: openCount,
  };
}

const projects = [
  project("project-inbox", "Inbox", { area: "Unfiled", role: "inbox" }, 1),
  project("project-finances", "Finances", { area: "Personal", sidebar_order: 10 }, 1),
  project("project-kata", "Kata", { area: "Work", sidebar_order: 10 }, 1),
];

function issue(
  uid: string,
  title: string,
  projectUID: string,
  metadata: KataTaskSummary["metadata"] = {},
  labels = ["work"],
): KataTaskSummary {
  const p = projects.find((candidate) => candidate.uid === projectUID) ?? projects[1]!;
  return {
    id: Number(uid.replace(/\D/g, "")) || 1,
    uid,
    project_id: p.id,
    short_id: uid.replace(/^issue-/, ""),
    qualified_id: `${p.name}#${uid.replace(/^issue-/, "")}`,
    title,
    status: "open",
    project_uid: projectUID,
    project_name: p.name,
    metadata,
    revision: 1,
    owner: "fixture-user",
    author: "fixture-user",
    labels,
    created_at: "2026-05-01T12:00:00.000Z",
    updated_at: fetchedAt,
  };
}

const initialIssues = [
  issue(
    "issue-pay-rent",
    "Pay rent",
    "project-finances",
    {
      scheduled_on: "2026-05-15",
      deadline_on: "2026-05-01",
    },
    ["home"],
  ),
  issue("issue-email-susan", "Email Susan re: Q3", "project-kata", { scheduled_on: "2026-05-15" }),
];

function recurrence(overrides: Partial<KataRecurrence> = {}): KataRecurrence {
  return {
    id: 1,
    uid: "recurrence-1",
    project_id: 1,
    rrule: "FREQ=DAILY",
    dtstart: "2026-05-15",
    timezone: "UTC",
    template_title: "Recurring task",
    template_body: "",
    template_labels: [],
    template_metadata: {},
    author: "fixture-user",
    revision: 1,
    created_at: fetchedAt,
    updated_at: fetchedAt,
    ...overrides,
  };
}

function makeComment(id: number, issueID: number, body: string) {
  return {
    id,
    issue_id: issueID,
    author: "fixture-user",
    body,
    created_at: fetchedAt,
  };
}

function detail(
  uid: string,
  rows = initialIssues,
  commentsByUID = new Map<string, ReturnType<typeof makeComment>[]>(),
): KataTaskDetail {
  const found = rows.find((candidate) => candidate.uid === uid) ?? rows[0]!;
  const labels = (found.labels ?? []).map((label) => ({
    issue_id: found.id,
    label,
    author: "fixture-user",
    created_at: fetchedAt,
  }));
  return {
    issue: { ...found, body: `${found.title} body` },
    comments: commentsByUID.get(found.uid) ?? [],
    labels,
    links: [],
    children: [],
  };
}

function reachableGraph(
  sourceUID: string,
  rows: readonly KataTaskSummary[],
  depth: KataReachableGraphResponse["depth"] = "full",
  hideDone = false,
): KataReachableGraphResponse {
  const visibleRows = rows.filter((item) => item.uid === sourceUID || !(hideDone && item.closed_reason === "done"));
  const visible = new Set(visibleRows.map((item) => item.uid));
  return {
    source_uid: sourceUID,
    depth,
    hide_done: hideDone,
    nodes: visibleRows.map((item) => ({ ...item, labels: [...(item.labels ?? [])] })),
    edges: visibleRows
      .flatMap((item) => [
        ...(item.parent?.uid
          ? [{ from_uid: item.parent.uid, to_uid: item.uid, kind: "parent" as const, layout: true }]
          : []),
        ...(item.blocks ?? []).map((peer) => ({
          from_uid: item.uid,
          to_uid: peer.uid,
          kind: "blocks" as const,
          layout: true,
        })),
        ...(item.blocked_by ?? []).map((peer) => ({
          from_uid: peer.uid,
          to_uid: item.uid,
          kind: "blocks" as const,
          layout: true,
        })),
        ...(item.related ?? []).map((peer) => ({
          from_uid: item.uid,
          to_uid: peer.uid,
          kind: "related" as const,
          layout: true,
        })),
      ])
      .filter((edge) => visible.has(edge.from_uid) && visible.has(edge.to_uid)),
    unresolved_refs: [],
    fetched_at: fetchedAt,
  };
}

function createDaemonWorkspaceAPI(
  rowsByDaemon: Record<string, KataTaskSummary[]>,
  projectsByDaemon: Record<string, KataProjectSummary[]> = {},
  options: {
    eventsByDaemon?: Record<string, KataTaskEvent[]> | undefined;
    snapshot?: KataWorkspaceSnapshotFixtureOverride | undefined;
  } = {},
): KataTaskAPI {
  const api: KataTaskAPI = {
    createProject: vi.fn(() => Effect.succeed({ changed: true })),
    createIssue: vi.fn(() => Effect.succeed({ changed: true })),
    addComment: vi.fn(() => Effect.succeed({ changed: true })),
    addLabel: vi.fn(() => Effect.succeed({ changed: true })),
    removeLabel: vi.fn(() => Effect.succeed({ changed: true })),
    assignOwner: vi.fn(() => Effect.succeed({ changed: true })),
    unassignOwner: vi.fn(() => Effect.succeed({ changed: true })),
    setPriority: vi.fn(() => Effect.succeed({ changed: true })),
    closeIssue: vi.fn(() => Effect.succeed({ changed: true })),
    reopenIssue: vi.fn(() => Effect.succeed({ changed: true })),
    editIssue: vi.fn(() => Effect.succeed({ changed: true })),
    patchIssueMetadata: vi.fn(() => Effect.succeed({ changed: true })),
    moveIssue: vi.fn(() => Effect.succeed({ changed: true })),
    recurrences: vi.fn(() => Effect.succeed({ recurrences: [], fetched_at: fetchedAt })),
    createRecurrence: vi.fn(() => Effect.succeed({ recurrence: recurrence() })),
    showRecurrence: vi.fn(() => Effect.succeed({ recurrence: recurrence(), etag: '"rev-1"' })),
    patchRecurrence: vi.fn(() => Effect.succeed({ changed: true, recurrence: recurrence(), etag: '"rev-2"' })),
    deleteRecurrence: vi.fn(() => Effect.void),
  };
  installKataWorkspaceSnapshotFixture({
    rows: (requestedDaemonID) => rowsByDaemon[requestedDaemonID] ?? [],
    projects: (requestedDaemonID) => projectsByDaemon[requestedDaemonID] ?? projects,
    detail: (uid, requestedDaemonID) => {
      const daemonRows = rowsByDaemon[requestedDaemonID] ?? [];
      return daemonRows.some((item) => item.uid === uid) ? detail(uid, daemonRows) : undefined;
    },
    events: (uid, requestedDaemonID) =>
      (options.eventsByDaemon?.[requestedDaemonID] ?? []).filter((event) => event.issue_uid === uid),
    eventCursor: (requestedDaemonID) =>
      Math.max(0, ...(options.eventsByDaemon?.[requestedDaemonID] ?? []).map((event) => event.event_id)),
    graph: (uid, requestedDaemonID) => {
      const daemonRows = rowsByDaemon[requestedDaemonID] ?? [];
      return daemonRows.some((item) => item.uid === uid) ? reachableGraph(uid, daemonRows) : undefined;
    },
    override: options.snapshot,
  });
  return api;
}

function createWorkspaceAPI(
  initialRows = initialIssues,
  options: {
    recurrences?: KataRecurrence[] | undefined;
    events?: KataTaskEvent[] | undefined;
    snapshot?: KataWorkspaceSnapshotFixtureOverride | undefined;
  } = {},
): {
  api: KataTaskAPI;
  addComment: ReturnType<typeof vi.fn>;
  addLabel: ReturnType<typeof vi.fn>;
  removeLabel: ReturnType<typeof vi.fn>;
  assignOwner: ReturnType<typeof vi.fn>;
  unassignOwner: ReturnType<typeof vi.fn>;
  setPriority: ReturnType<typeof vi.fn>;
  patchIssueMetadata: ReturnType<typeof vi.fn>;
  moveIssue: ReturnType<typeof vi.fn>;
  recurrences: ReturnType<typeof vi.fn>;
  createRecurrence: ReturnType<typeof vi.fn>;
  patchRecurrence: ReturnType<typeof vi.fn>;
  deleteRecurrence: ReturnType<typeof vi.fn>;
  createIssue: ReturnType<typeof vi.fn>;
} {
  let rows: KataTaskSummary[] = initialRows.map((item) => ({ ...item, labels: [...(item.labels ?? [])] }));
  const commentsByUID = new Map<string, ReturnType<typeof makeComment>[]>([
    ["issue-pay-rent", [makeComment(1, rows[0]!.id, "Verify amount against the lease.")]],
  ]);
  const addComment = vi.fn((target: { ref: string }, _actor: string, body: string) =>
    Effect.sync(() => {
      const found = rows.find((item) => item.uid === target.ref) ?? rows[0]!;
      const next = [makeComment(Date.now(), found.id, body), ...(commentsByUID.get(found.uid) ?? [])];
      commentsByUID.set(found.uid, next);
      return { changed: true };
    }),
  );
  const addLabel = vi.fn((target: { ref: string }, _actor: string, label: string) =>
    Effect.sync(() => {
      rows = rows.map((item) =>
        item.uid === target.ref ? { ...item, labels: [...new Set([...(item.labels ?? []), label])] } : item,
      );
      return { changed: true };
    }),
  );
  const removeLabel = vi.fn((target: { ref: string }, _actor: string, label: string) =>
    Effect.sync(() => {
      rows = rows.map((item) =>
        item.uid === target.ref
          ? { ...item, labels: (item.labels ?? []).filter((candidate) => candidate !== label) }
          : item,
      );
      return { changed: true };
    }),
  );
  const assignOwner = vi.fn((target: { ref: string }, _actor: string, owner: string) =>
    Effect.sync(() => {
      rows = rows.map((item) => (item.uid === target.ref ? { ...item, owner } : item));
      return { changed: true };
    }),
  );
  const unassignOwner = vi.fn((target: { ref: string }) =>
    Effect.sync(() => {
      rows = rows.map((item) => (item.uid === target.ref ? { ...item, owner: undefined } : item));
      return { changed: true };
    }),
  );
  const setPriority = vi.fn((target: { ref: string }, _actor: string, priority: number | null) =>
    Effect.sync(() => {
      rows = rows.map((item) => (item.uid === target.ref ? { ...item, priority: priority ?? undefined } : item));
      return { changed: true };
    }),
  );
  const patchIssueMetadata = vi.fn((target: { ref: string }, _actor: string, patch: Record<string, unknown>) =>
    Effect.sync(() => {
      rows = rows.map((item) =>
        item.uid === target.ref
          ? { ...item, metadata: { ...item.metadata, ...patch }, revision: item.revision + 1 }
          : item,
      );
      return { changed: true };
    }),
  );
  const moveIssue = vi.fn(() => Effect.succeed({ changed: true }));
  const recurrences = vi.fn(() => Effect.succeed({ recurrences: options.recurrences ?? [], fetched_at: fetchedAt }));
  const createRecurrence = vi.fn(() => Effect.succeed({ recurrence: recurrence() }));
  const patchRecurrence = vi.fn(() =>
    Effect.succeed({ changed: true, recurrence: recurrence({ revision: 2 }), etag: '"rev-2"' }),
  );
  const deleteRecurrence = vi.fn(() => Effect.void);
  const createIssue = vi.fn((projectID: number, actor: string, draft: { title: string }) =>
    Effect.sync(() => {
      const created: KataTaskSummary = {
        ...issue("issue-capture", draft.title, "project-inbox"),
        project_id: projectID,
        author: actor,
      };
      rows = [created, ...rows];
      return { changed: true };
    }),
  );
  const api: KataTaskAPI = {
    createProject: vi.fn(() => Effect.succeed({ changed: true })),
    createIssue,
    addComment,
    addLabel,
    removeLabel,
    assignOwner,
    unassignOwner,
    setPriority,
    closeIssue: vi.fn(() => Effect.succeed({ changed: true })),
    reopenIssue: vi.fn(() => Effect.succeed({ changed: true })),
    editIssue: vi.fn(() => Effect.succeed({ changed: true })),
    patchIssueMetadata,
    moveIssue,
    recurrences,
    createRecurrence,
    showRecurrence: vi.fn(() => Effect.succeed({ recurrence: recurrence(), etag: '"rev-1"' })),
    patchRecurrence,
    deleteRecurrence,
  };
  installKataWorkspaceSnapshotFixture({
    rows: () => rows,
    projects: () => projects,
    detail: (uid) => (rows.some((item) => item.uid === uid) ? detail(uid, rows, commentsByUID) : undefined),
    events: (uid) => (options.events ?? []).filter((event) => event.issue_uid === uid),
    eventCursor: () => Math.max(0, ...(options.events ?? []).map((event) => event.event_id)),
    graph: (uid) => (rows.some((item) => item.uid === uid) ? reachableGraph(uid, rows) : undefined),
    override: options.snapshot,
  });
  return {
    api,
    addComment,
    addLabel,
    removeLabel,
    assignOwner,
    unassignOwner,
    setPriority,
    patchIssueMetadata,
    moveIssue,
    recurrences,
    createRecurrence,
    patchRecurrence,
    deleteRecurrence,
    createIssue,
  };
}

export {
  createDaemonWorkspaceAPI,
  createWorkspaceAPI,
  deferred,
  detail,
  fetchedAt,
  initialIssues,
  issue,
  projects,
  recurrence,
};
