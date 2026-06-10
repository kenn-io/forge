import { vi } from "vite-plus/test";

import type {
  KataInstanceResponse,
  KataProjectSummary,
  KataRecurrence,
  KataTaskAPI,
  KataTaskEvent,
  KataTaskEventsResponse,
  KataTaskIssuesQuery,
  KataTaskSearchFilters,
  KataTaskSearchResponse,
  KataTaskSummary,
} from "../../api/kata/taskTypes.js";
import { buildKataTaskView } from "../../api/kata/taskViewBuilder.js";
import type { MessageLinkRef } from "../../messages/types";
import {
  getActiveKataDaemon,
  getDefaultKataDaemon,
  setActiveKataDaemon,
  setKataDaemonRoster,
} from "../../stores/active-kata-daemon.svelte.js";

export class TestResizeObserver implements ResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

export function resetKataWorkspaceTestState(): void {
  if (!("ResizeObserver" in globalThis)) {
    Object.defineProperty(globalThis, "ResizeObserver", {
      configurable: true,
      value: TestResizeObserver,
    });
  }
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

function messageLink(overrides: Partial<MessageLinkRef> = {}): MessageLinkRef {
  return {
    message_id: 1001,
    conversation_id: 1001,
    subject: "Project sync",
    from: "alice@example.com",
    sent_at: "2026-05-15T09:00:00Z",
    added_at: "2026-05-18T00:00:00Z",
    ...overrides,
  };
}

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
) {
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

function createDaemonWorkspaceAPI(rowsByDaemon: Record<string, KataTaskSummary[]>): KataTaskAPI {
  function rows(): KataTaskSummary[] {
    return rowsByDaemon[getActiveKataDaemon() ?? getDefaultKataDaemon() ?? "home"] ?? [];
  }

  return {
    instance: vi.fn(
      async (): Promise<KataInstanceResponse> => ({
        instance_uid: "instance-1",
        version: "dev",
        schema_version: 1,
      }),
    ),
    projects: vi.fn(async () => ({ projects, fetched_at: fetchedAt })),
    createProject: vi.fn(async (name: string) => ({
      id: 90,
      uid: `project-${name.toLowerCase()}`,
      name,
      metadata: {},
      open_count: 0,
    })),
    renameProject: vi.fn(async (_projectID: number, name: string) => ({
      id: 1,
      uid: `project-${name.toLowerCase()}`,
      name,
      metadata: {},
      open_count: 0,
    })),
    patchProjectMetadata: vi.fn(async (projectID: number, _actor: string, patch: Record<string, unknown>) => {
      const base = projects.find((item) => item.id === projectID) ?? projects[0]!;
      return {
        changed: true,
        project: { ...base, metadata: { ...base.metadata, ...patch } },
        etag: '"rev-2"',
      };
    }),
    createIssue: vi.fn(async (projectID: number, actor: string, draft: { title: string }) => ({
      changed: true,
      issue: {
        ...issue("issue-capture", draft.title, "project-inbox"),
        project_id: projectID,
        author: actor,
      },
      etag: '"rev-1"',
    })),
    issues: vi.fn(async (query: KataTaskIssuesQuery) =>
      buildKataTaskView({
        view: query.view,
        issues: rows().filter((item) => (query.project_uid ? item.project_uid === query.project_uid : true)),
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      }),
    ),
    search: vi.fn(
      async (filters: KataTaskSearchFilters): Promise<KataTaskSearchResponse> => ({
        filters,
        issues: rows().filter((item) =>
          filters.scope.kind === "project" ? item.project_uid === filters.scope.project_uid : true,
        ),
        fetched_at: fetchedAt,
      }),
    ),
    issue: vi.fn(async (uid: string) => detail(uid, rows())),
    events: vi.fn(
      async (): Promise<KataTaskEventsResponse> => ({
        reset_required: false,
        events: [],
        next_after_id: 0,
      }),
    ),
    addComment: vi.fn(async () => ({ changed: true })),
    addLabel: vi.fn(async () => ({ changed: true })),
    removeLabel: vi.fn(async () => ({ changed: true })),
    assignOwner: vi.fn(async () => ({ changed: true })),
    unassignOwner: vi.fn(async () => ({ changed: true })),
    setPriority: vi.fn(async () => ({ changed: true })),
    closeIssue: vi.fn(async () => ({ changed: true })),
    reopenIssue: vi.fn(async () => ({ changed: true })),
    editIssue: vi.fn(async () => ({ changed: true })),
    patchIssueMetadata: vi.fn(async () => ({ changed: true, etag: '"rev-2"' })),
    moveIssue: vi.fn(async () => ({ changed: true, new_short_id: "moved" })),
    recurrences: vi.fn(async () => ({ recurrences: [], fetched_at: fetchedAt })),
    createRecurrence: vi.fn(async () => ({ recurrence: recurrence() })),
    showRecurrence: vi.fn(async () => ({ recurrence: recurrence(), etag: '"rev-1"' })),
    patchRecurrence: vi.fn(async () => ({ changed: true, recurrence: recurrence(), etag: '"rev-2"' })),
    deleteRecurrence: vi.fn(async () => undefined),
  };
}

function createWorkspaceAPI(
  initialRows = initialIssues,
  options: { recurrences?: KataRecurrence[] | undefined; events?: KataTaskEvent[] | undefined } = {},
): {
  api: KataTaskAPI;
  search: ReturnType<typeof vi.fn>;
  addComment: ReturnType<typeof vi.fn>;
  addLabel: ReturnType<typeof vi.fn>;
  removeLabel: ReturnType<typeof vi.fn>;
  assignOwner: ReturnType<typeof vi.fn>;
  unassignOwner: ReturnType<typeof vi.fn>;
  setPriority: ReturnType<typeof vi.fn>;
  patchIssueMetadata: ReturnType<typeof vi.fn>;
  moveIssue: ReturnType<typeof vi.fn>;
  createRecurrence: ReturnType<typeof vi.fn>;
  patchRecurrence: ReturnType<typeof vi.fn>;
  createIssue: ReturnType<typeof vi.fn>;
} {
  let rows: KataTaskSummary[] = initialRows.map((item) => ({ ...item, labels: [...(item.labels ?? [])] }));
  const commentsByUID = new Map<string, ReturnType<typeof makeComment>[]>([
    ["issue-pay-rent", [makeComment(1, rows[0]!.id, "Verify amount against the lease.")]],
  ]);
  const search = vi.fn(
    async (filters: KataTaskSearchFilters): Promise<KataTaskSearchResponse> => ({
      filters,
      issues: rows.filter((item) =>
        filters.scope.kind === "project" ? item.project_uid === filters.scope.project_uid : true,
      ),
      fetched_at: fetchedAt,
    }),
  );
  const addComment = vi.fn(async (target: { ref: string }, _actor: string, body: string) => {
    const found = rows.find((item) => item.uid === target.ref) ?? rows[0]!;
    const next = [makeComment(Date.now(), found.id, body), ...(commentsByUID.get(found.uid) ?? [])];
    commentsByUID.set(found.uid, next);
    return { changed: true, issue: found };
  });
  const addLabel = vi.fn(async (target: { ref: string }, _actor: string, label: string) => {
    rows = rows.map((item) =>
      item.uid === target.ref ? { ...item, labels: [...new Set([...(item.labels ?? []), label])] } : item,
    );
    return { changed: true, issue: rows.find((item) => item.uid === target.ref) };
  });
  const removeLabel = vi.fn(async (target: { ref: string }, _actor: string, label: string) => {
    rows = rows.map((item) =>
      item.uid === target.ref
        ? { ...item, labels: (item.labels ?? []).filter((candidate) => candidate !== label) }
        : item,
    );
    return { changed: true, issue: rows.find((item) => item.uid === target.ref) };
  });
  const assignOwner = vi.fn(async (target: { ref: string }, _actor: string, owner: string) => {
    rows = rows.map((item) => (item.uid === target.ref ? { ...item, owner } : item));
    return { changed: true, issue: rows.find((item) => item.uid === target.ref) };
  });
  const unassignOwner = vi.fn(async (target: { ref: string }) => {
    rows = rows.map((item) => (item.uid === target.ref ? { ...item, owner: undefined } : item));
    return { changed: true, issue: rows.find((item) => item.uid === target.ref) };
  });
  const setPriority = vi.fn(async (target: { ref: string }, _actor: string, priority: number | null) => {
    rows = rows.map((item) => (item.uid === target.ref ? { ...item, priority: priority ?? undefined } : item));
    return { changed: true, issue: rows.find((item) => item.uid === target.ref) };
  });
  const patchIssueMetadata = vi.fn(async (target: { ref: string }, _actor: string, patch: Record<string, unknown>) => {
    rows = rows.map((item) =>
      item.uid === target.ref
        ? { ...item, metadata: { ...item.metadata, ...patch }, revision: item.revision + 1 }
        : item,
    );
    return { changed: true, issue: rows.find((item) => item.uid === target.ref), etag: '"rev-2"' };
  });
  const moveIssue = vi.fn(async () => ({ changed: true, new_short_id: "moved" }));
  const createRecurrence = vi.fn(async () => ({
    recurrence: recurrence(),
  }));
  const patchRecurrence = vi.fn(async () => ({
    changed: true,
    recurrence: recurrence({ revision: 2 }),
    etag: '"rev-2"',
  }));
  const createIssue = vi.fn(async (projectID: number, actor: string, draft: { title: string }) => {
    const created: KataTaskSummary = {
      ...issue("issue-capture", draft.title, "project-inbox"),
      project_id: projectID,
      author: actor,
    };
    rows = [created, ...rows];
    return { changed: true, issue: created, etag: '"rev-1"' };
  });
  return {
    api: {
      instance: vi.fn(
        async (): Promise<KataInstanceResponse> => ({
          instance_uid: "instance-1",
          version: "dev",
          schema_version: 1,
        }),
      ),
      projects: vi.fn(async () => ({ projects, fetched_at: fetchedAt })),
      createProject: vi.fn(async (name: string) => ({
        id: 90,
        uid: `project-${name.toLowerCase()}`,
        name,
        metadata: {},
        open_count: 0,
      })),
      renameProject: vi.fn(async (_projectID: number, name: string) => ({
        id: 1,
        uid: `project-${name.toLowerCase()}`,
        name,
        metadata: {},
        open_count: 0,
      })),
      patchProjectMetadata: vi.fn(async (projectID: number, _actor: string, patch: Record<string, unknown>) => {
        const base = projects.find((item) => item.id === projectID) ?? projects[0]!;
        return {
          changed: true,
          project: { ...base, metadata: { ...base.metadata, ...patch } },
          etag: '"rev-2"',
        };
      }),
      createIssue,
      issues: vi.fn(async (query: KataTaskIssuesQuery) =>
        buildKataTaskView({
          view: query.view,
          issues: rows.filter((item) => (query.project_uid ? item.project_uid === query.project_uid : true)),
          projects,
          today: "2026-05-15",
          fetched_at: fetchedAt,
        }),
      ),
      search,
      issue: vi.fn(async (uid: string) => detail(uid, rows, commentsByUID)),
      events: vi.fn(
        async (): Promise<KataTaskEventsResponse> => ({
          reset_required: false,
          events: options.events ?? [],
          next_after_id: 0,
        }),
      ),
      addComment,
      addLabel,
      removeLabel,
      assignOwner,
      unassignOwner,
      setPriority,
      closeIssue: vi.fn(async () => ({ changed: true })),
      reopenIssue: vi.fn(async () => ({ changed: true })),
      editIssue: vi.fn(async () => ({ changed: true })),
      patchIssueMetadata,
      moveIssue,
      recurrences: vi.fn(async () => ({ recurrences: options.recurrences ?? [], fetched_at: fetchedAt })),
      createRecurrence,
      showRecurrence: vi.fn(async () => ({
        recurrence: recurrence(),
        etag: '"rev-1"',
      })),
      patchRecurrence,
      deleteRecurrence: vi.fn(async () => undefined),
    },
    search,
    addComment,
    addLabel,
    removeLabel,
    assignOwner,
    unassignOwner,
    setPriority,
    patchIssueMetadata,
    moveIssue,
    createRecurrence,
    patchRecurrence,
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
  messageLink,
  projects,
  recurrence,
};
