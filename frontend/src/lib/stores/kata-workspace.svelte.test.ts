import { describe, expect, test, vi } from "vite-plus/test";

import { KataTaskAPIError } from "../api/kata/taskClient.js";
import type {
  KataCreateRecurrenceInput,
  KataInstanceResponse,
  KataTaskCreateDraft,
  KataProjectSummary,
  KataRecurrence,
  KataRecurrenceResponse,
  KataPatchRecurrenceInput,
  KataTaskAPI,
  KataTaskDetail,
  KataTaskEvent,
  KataTaskEventsQuery,
  KataTaskEventsResponse,
  KataTaskIssuesQuery,
  KataTaskSearchFilters,
  KataTaskSearchResponse,
  KataTaskSummary,
  KataTaskViewResponse,
} from "../api/kata/taskTypes.js";
import { buildKataTaskView } from "../api/kata/taskViewBuilder.js";
import { createKataWorkspaceStore, deriveKataAreas, duplicateCandidatesFromError } from "./kata-workspace.svelte.js";

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
  open_count = 0,
): KataProjectSummary {
  return {
    id: Number(uid.replace(/\D/g, "")) || 1,
    uid,
    name,
    metadata,
    open_count,
  };
}

const projects = [
  project("project-inbox", "Inbox", { area: "Unfiled", role: "inbox" }, 1),
  project("project-finances", "Finances", { area: "Personal", sidebar_order: 10 }, 3),
  project("project-health", "Health", { area: "Personal", sidebar_order: 20 }, 2),
  project("project-kata", "Kata", { area: "Work", sidebar_order: 10 }, 2),
];

function issue(
  uid: string,
  title: string,
  project_uid: string,
  metadata: KataTaskSummary["metadata"] = {},
  status: KataTaskSummary["status"] = "open",
): KataTaskSummary {
  const p = projects.find((candidate) => candidate.uid === project_uid) ?? projects[1]!;
  return {
    id: Number(uid.replace(/\D/g, "")) || 1,
    uid,
    project_id: p.id,
    short_id: uid.replace(/^issue-/, ""),
    qualified_id: `${p.name}#${uid.replace(/^issue-/, "")}`,
    title,
    status,
    project_uid,
    project_name: p.name,
    metadata,
    revision: 1,
    author: "fixture-user",
    labels: project_uid === "project-health" ? ["health"] : ["work"],
    owner: project_uid === "project-kata" ? "agent:planner" : "fixture-user",
    created_at: "2026-05-01T12:00:00.000Z",
    updated_at: fetchedAt,
    ...(status === "closed" ? { closed_at: "2026-05-15T12:00:00.000Z" } : {}),
  };
}

const issues = [
  issue("issue-pay-rent", "Pay rent", "project-finances", {
    scheduled_on: "2026-05-15",
    deadline_on: "2026-05-01",
  }),
  issue("issue-call-dentist", "Call dentist", "project-health", { scheduled_on: "2026-05-15" }),
  issue("issue-renew-passport", "Renew passport", "project-inbox", { scheduled_on: "2026-05-22" }),
  issue("issue-email-susan", "Email Susan re: Q3", "project-kata", { scheduled_on: "2026-05-15" }),
  issue("issue-read-design-notes", "Read design notes", "project-kata"),
  issue("issue-paid-rent", "Paid rent", "project-finances", {}, "closed"),
];

const events: KataTaskEvent[] = [
  {
    event_id: 1,
    event_uid: "event-pay-rent",
    origin_instance_uid: "instance-1",
    type: "issue.created",
    project_id: projects[1]!.id,
    project_uid: "project-finances",
    project_name: "Finances",
    issue_uid: "issue-pay-rent",
    issue_short_id: "pay-rent",
    actor: "fixture-user",
    created_at: fetchedAt,
  },
  {
    event_id: 2,
    event_uid: "event-dentist",
    origin_instance_uid: "instance-1",
    type: "issue.created",
    project_id: projects[2]!.id,
    project_uid: "project-health",
    project_name: "Health",
    issue_uid: "issue-call-dentist",
    issue_short_id: "call-dentist",
    actor: "fixture-user",
    created_at: fetchedAt,
  },
];

function detailFor(uid: string): KataTaskDetail {
  const found = issues.find((candidate) => candidate.uid === uid) ?? issues[0]!;
  return {
    issue: { ...found, body: `${found.title} body` },
    comments: [],
    labels: (found.labels ?? []).map((label) => ({
      issue_id: found.id,
      label,
      author: "fixture-user",
      created_at: fetchedAt,
    })),
    links: [],
    children: [],
    etag: `"rev-${found.revision}"`,
  };
}

type FakeKataTaskAPI = KataTaskAPI & {
  addComment(target: { project_id: number; ref: string }, actor: string, body: string): Promise<{ changed: boolean }>;
  addLabel(target: { project_id: number; ref: string }, actor: string, label: string): Promise<{ changed: boolean }>;
  removeLabel(target: { project_id: number; ref: string }, actor: string, label: string): Promise<{ changed: boolean }>;
  assignOwner(target: { project_id: number; ref: string }, actor: string, owner: string): Promise<{ changed: boolean }>;
  unassignOwner(target: { project_id: number; ref: string }, actor: string): Promise<{ changed: boolean }>;
  setPriority(
    target: { project_id: number; ref: string },
    actor: string,
    priority: number | null,
  ): Promise<{ changed: boolean }>;
  closeIssue(
    target: { project_id: number; ref: string },
    actor: string,
    options?: { reason?: string; message?: string; source?: string },
  ): Promise<{ changed: boolean }>;
  reopenIssue(target: { project_id: number; ref: string }, actor: string): Promise<{ changed: boolean }>;
  editIssue(
    target: { project_id: number; ref: string },
    actor: string,
    patch: { title?: string; body?: string },
  ): Promise<{ changed: boolean }>;
  patchIssueMetadata(
    target: { project_id: number; ref: string },
    actor: string,
    patch: Record<string, unknown>,
    ifMatch: string,
  ): Promise<{ changed: boolean; issue?: KataTaskSummary; etag?: string }>;
  moveIssue(
    target: { project_id: number; ref: string },
    actor: string,
    toProjectUID: string,
    ifMatch: string,
  ): Promise<{ changed: boolean; issue?: KataTaskSummary; etag?: string; new_short_id: string }>;
  mocks: {
    instance: ReturnType<typeof vi.fn>;
    projects: ReturnType<typeof vi.fn>;
    createProject: ReturnType<typeof vi.fn>;
    renameProject: ReturnType<typeof vi.fn>;
    patchProjectMetadata: ReturnType<typeof vi.fn>;
    createIssue: ReturnType<typeof vi.fn>;
    issues: ReturnType<typeof vi.fn>;
    search: ReturnType<typeof vi.fn>;
    issue: ReturnType<typeof vi.fn>;
    events: ReturnType<typeof vi.fn>;
    addComment: ReturnType<typeof vi.fn>;
    addLabel: ReturnType<typeof vi.fn>;
    removeLabel: ReturnType<typeof vi.fn>;
    assignOwner: ReturnType<typeof vi.fn>;
    unassignOwner: ReturnType<typeof vi.fn>;
    setPriority: ReturnType<typeof vi.fn>;
    closeIssue: ReturnType<typeof vi.fn>;
    reopenIssue: ReturnType<typeof vi.fn>;
    editIssue: ReturnType<typeof vi.fn>;
    patchIssueMetadata: ReturnType<typeof vi.fn>;
    moveIssue: ReturnType<typeof vi.fn>;
    recurrences: ReturnType<typeof vi.fn>;
    createRecurrence: ReturnType<typeof vi.fn>;
    showRecurrence: ReturnType<typeof vi.fn>;
    patchRecurrence: ReturnType<typeof vi.fn>;
    deleteRecurrence: ReturnType<typeof vi.fn>;
  };
};

function createFakeKataTaskAPI(): FakeKataTaskAPI {
  const instance = vi.fn(
    async (): Promise<KataInstanceResponse> => ({
      instance_uid: "instance-1",
      version: "dev",
      schema_version: 1,
    }),
  );
  const projectsMock = vi.fn(async () => ({ projects, fetched_at: fetchedAt }));
  const createProject = vi.fn(
    async (name: string): Promise<KataProjectSummary> => ({
      id: 90,
      uid: `project-${name.toLowerCase()}`,
      name,
      metadata: {},
      open_count: 0,
    }),
  );
  const patchProjectMetadata = vi.fn(async (projectID: number, _actor: string, patch: Record<string, unknown>) => {
    const base = projects.find((item) => item.id === projectID) ?? projects[0]!;
    return {
      changed: true,
      project: { ...base, metadata: { ...base.metadata, ...patch }, revision: (base.revision ?? 1) + 1 },
      etag: `"rev-${(base.revision ?? 1) + 1}"`,
    };
  });
  const renameProject = vi.fn(
    async (_projectID: number, name: string): Promise<KataProjectSummary> => ({
      id: 1,
      uid: `project-${name.toLowerCase()}`,
      name,
      metadata: {},
      open_count: 0,
    }),
  );
  const createIssue = vi.fn(
    async (
      projectID: number,
      actor: string,
      draft: KataTaskCreateDraft,
    ): Promise<{ changed: boolean; issue: KataTaskSummary; etag: string }> => ({
      changed: true,
      issue: {
        ...issue("issue-capture", draft.title, "project-inbox", draft.metadata ?? {}),
        project_id: projectID,
        author: actor,
        body: draft.body,
        labels: draft.labels,
      },
      etag: '"rev-1"',
    }),
  );
  const issuesMock = vi.fn(async (query: KataTaskIssuesQuery): Promise<KataTaskViewResponse> => {
    let rows = query.view === "logbook" ? issues.filter((item) => item.status === "closed") : issues;
    rows = rows.filter((item) => (query.project_uid ? item.project_uid === query.project_uid : true));
    if (query.area) {
      const allowed = new Set(
        projects
          .filter((item) => item.metadata.area?.toLowerCase() === query.area?.toLowerCase())
          .map((item) => item.uid),
      );
      rows = rows.filter((item) => allowed.has(item.project_uid));
    }
    return buildKataTaskView({
      view: query.view,
      issues: rows,
      projects,
      today: "2026-05-15",
      fetched_at: fetchedAt,
    });
  });
  const search = vi.fn(async (filters: KataTaskSearchFilters): Promise<KataTaskSearchResponse> => {
    const query = filters.query.trim().toLowerCase();
    const owner = filters.owner.trim().toLowerCase();
    const label = filters.label.trim().toLowerCase();
    const rows = issues.filter((item) => {
      if (filters.scope.kind === "project" && item.project_uid !== filters.scope.project_uid) return false;
      if (filters.status !== "all" && item.status !== filters.status) return false;
      if (owner && item.owner?.toLowerCase() !== owner) return false;
      if (label && !(item.labels ?? []).some((candidate) => candidate.toLowerCase() === label)) return false;
      if (
        query &&
        ![item.title, item.qualified_id, item.body].filter(Boolean).join(" ").toLowerCase().includes(query)
      ) {
        return false;
      }
      return true;
    });
    return { filters, issues: rows, fetched_at: fetchedAt };
  });
  const issueMock = vi.fn(async (uid: string) => detailFor(uid));
  const eventsMock = vi.fn(async (query: KataTaskEventsQuery = {}): Promise<KataTaskEventsResponse> => {
    const rows = events
      .filter((event) => event.event_id > (query.after_id ?? 0))
      .filter((event) => (query.issue_uid ? event.issue_uid === query.issue_uid : true))
      .slice(0, query.limit ?? 100);
    return {
      reset_required: false,
      events: rows,
      next_after_id: rows.at(-1)?.event_id ?? query.after_id ?? 0,
    };
  });
  const addComment = vi.fn(async () => ({ changed: true }));
  const addLabel = vi.fn(async () => ({ changed: true }));
  const removeLabel = vi.fn(async () => ({ changed: true }));
  const assignOwner = vi.fn(async () => ({ changed: true }));
  const unassignOwner = vi.fn(async () => ({ changed: true }));
  const setPriority = vi.fn(async () => ({ changed: true }));
  const closeIssue = vi.fn(async () => ({ changed: true }));
  const reopenIssue = vi.fn(async () => ({ changed: true }));
  const editIssue = vi.fn(async () => ({ changed: true }));
  const patchIssueMetadata = vi.fn(
    async (
      target: { project_id: number; ref: string },
      _actor: string,
      patch: Record<string, unknown>,
    ): Promise<{ changed: boolean; issue?: KataTaskSummary; etag?: string }> => {
      const base = issues.find((item) => item.uid === target.ref) ?? issues[0]!;
      return {
        changed: true,
        issue: { ...base, metadata: { ...base.metadata, ...patch }, revision: base.revision + 1 },
        etag: `"rev-${base.revision + 1}"`,
      };
    },
  );
  const moveIssue = vi.fn(
    async (
      target: { project_id: number; ref: string },
      _actor: string,
      toProjectUID: string,
    ): Promise<{ changed: boolean; issue?: KataTaskSummary; etag?: string; new_short_id: string }> => {
      const base = issues.find((item) => item.uid === target.ref) ?? issues[0]!;
      const nextProject = projects.find((item) => item.uid === toProjectUID) ?? projects[3]!;
      return {
        changed: true,
        issue: {
          ...base,
          project_id: nextProject.id,
          project_uid: nextProject.uid,
          project_name: nextProject.name,
          revision: base.revision + 1,
        },
        etag: `"rev-${base.revision + 1}"`,
        new_short_id: `${nextProject.name}#moved`,
      };
    },
  );
  const recurrence = {
    id: 1,
    uid: "recurrence-1",
    project_id: projects[1]!.id,
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
  };
  const recurrences = vi.fn(async () => ({ recurrences: [], fetched_at: fetchedAt }));
  const createRecurrence = vi.fn(async () => ({ recurrence }));
  const showRecurrence = vi.fn(async () => ({ recurrence, etag: '"rev-1"' }));
  const patchRecurrence = vi.fn(async () => ({
    recurrence: { ...recurrence, revision: 2 },
    changed: true,
    etag: '"rev-2"',
  }));
  const deleteRecurrence = vi.fn(async () => undefined);
  return {
    instance,
    projects: projectsMock,
    createProject,
    renameProject,
    patchProjectMetadata,
    createIssue,
    issues: issuesMock,
    search,
    issue: issueMock,
    events: eventsMock,
    addComment,
    addLabel,
    removeLabel,
    assignOwner,
    unassignOwner,
    setPriority,
    closeIssue,
    reopenIssue,
    editIssue,
    patchIssueMetadata,
    moveIssue,
    recurrences,
    createRecurrence,
    showRecurrence,
    patchRecurrence,
    deleteRecurrence,
    mocks: {
      instance,
      projects: projectsMock,
      createProject,
      renameProject,
      patchProjectMetadata,
      createIssue,
      issues: issuesMock,
      search,
      issue: issueMock,
      events: eventsMock,
      addComment,
      addLabel,
      removeLabel,
      assignOwner,
      unassignOwner,
      setPriority,
      closeIssue,
      reopenIssue,
      editIssue,
      patchIssueMetadata,
      moveIssue,
      recurrences,
      createRecurrence,
      showRecurrence,
      patchRecurrence,
      deleteRecurrence,
    },
  };
}

function recurrence(overrides: Partial<KataRecurrence> = {}): KataRecurrence {
  return {
    id: 1,
    uid: "recurrence-1",
    project_id: projects[1]!.id,
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

describe("kata workspace store", () => {
  test("bootstraps sidebar, Today view, and selected detail from injected API", async () => {
    const store = createKataWorkspaceStore({ api: createFakeKataTaskAPI() });

    await store.bootstrap();

    expect(store.connection.status).toBe("online");
    expect(store.projects.map((item) => item.name)).toEqual(["Inbox", "Finances", "Health", "Kata"]);
    expect(store.areas.map((area) => area.name)).toEqual(["Personal", "Work"]);
    expect(store.currentView.name).toBe("today");
    expect(store.currentView.groups[0]?.issues[0]?.title).toBe("Pay rent");
    expect(store.selectedIssue?.issue.uid).toBe("issue-pay-rent");
    expect(store.selectedEvents.map((event) => event.type)).toEqual(["issue.created"]);
  });

  test("bootstraps selection from visible top-level task rows", async () => {
    const parent = {
      ...issues[0]!,
      uid: "issue-parent",
      short_id: "parent",
      qualified_id: "Finances#parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    };
    const child = {
      ...issues[1]!,
      uid: "issue-child",
      short_id: "child",
      qualified_id: "Finances#child",
      project_id: parent.project_id,
      project_uid: parent.project_uid,
      project_name: parent.project_name,
      title: "A child task",
      parent_short_id: parent.short_id,
    };
    const api = createFakeKataTaskAPI();
    api.mocks.issues.mockResolvedValueOnce({
      view: "today",
      groups: [{ id: "today", title: "Today", issues: [child, parent] }],
      fetched_at: fetchedAt,
    });
    api.mocks.issue.mockImplementation(async (uid: string) => ({
      ...detailFor(uid),
      issue: { ...(uid === parent.uid ? parent : child), body: `${uid} body` },
    }));
    const store = createKataWorkspaceStore({ api });

    await store.bootstrap();

    expect(store.selectedIssue?.issue.uid).toBe(parent.uid);
  });

  test("does not let an already stale guarded view load cancel the active view", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    const initialSelectionUID = store.selectedIssue?.issue.uid;
    const issueLoadCount = api.mocks.issues.mock.calls.length;
    const detailLoadCount = api.mocks.issue.mock.calls.length;

    await store.openView("inbox", { shouldApply: () => false });

    expect(api.mocks.issues).toHaveBeenCalledTimes(issueLoadCount);
    expect(api.mocks.issue).toHaveBeenCalledTimes(detailLoadCount);
    expect(store.currentView.name).toBe("today");
    expect(store.selectedIssue?.issue.uid).toBe(initialSelectionUID);
  });

  test("derives area groups without surfacing task inbox projects", () => {
    expect(deriveKataAreas(projects)).toEqual([
      { name: "Personal", projects: [projects[1], projects[2]] },
      { name: "Work", projects: [projects[3]] },
    ]);
  });

  test("captures new tasks into the existing task inbox project and selects the created task", async () => {
    const api = createFakeKataTaskAPI();
    const captured = {
      ...issue("issue-capture", "Capture from notes", "project-inbox", { scheduled_on: "2026-05-20" }),
      body: "Markdown note",
      labels: ["notes"],
    };
    api.mocks.createIssue.mockResolvedValueOnce({
      changed: true,
      issue: captured,
      etag: '"rev-1"',
    });
    api.mocks.issues.mockImplementation(async (query: KataTaskIssuesQuery) => {
      const base = await createFakeKataTaskAPI().issues(query);
      if (query.view !== "inbox") return base;
      return {
        ...base,
        groups: [{ id: "inbox", title: "Inbox", issues: [captured] }],
      };
    });
    api.mocks.issue.mockImplementation(async (uid: string) => ({
      issue: { ...(uid === captured.uid ? captured : issues[0]!), body: `${uid} body` },
      comments: [],
      labels: [],
      links: [],
      children: [],
      etag: '"rev-1"',
    }));
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("inbox");

    await store.captureIssue(
      "middleman",
      {
        title: "Capture from notes",
        body: "Markdown note",
        labels: ["notes"],
        metadata: { scheduled_on: "2026-05-20" },
      },
      "01MIDDLEMANCAPTURE00000002",
    );

    expect(api.mocks.createIssue).toHaveBeenCalledWith(
      projects[0]!.id,
      "middleman",
      expect.objectContaining({ title: "Capture from notes" }),
      "01MIDDLEMANCAPTURE00000002",
    );
    expect(store.currentView.name).toBe("inbox");
    expect(store.currentView.groups.flatMap((group) => group.issues).map((item) => item.title)).toContain(
      "Capture from notes",
    );
    expect(store.selectedIssue?.issue.uid).toBe("issue-capture");
  });

  test("capturing a task clears project filters before showing the inbox", async () => {
    const api = createFakeKataTaskAPI();
    const captured = issue("issue-capture", "Capture from filtered view", "project-inbox");
    api.mocks.createIssue.mockResolvedValueOnce({
      changed: true,
      issue: captured,
      etag: '"rev-1"',
    });
    api.mocks.issues.mockImplementation(async (query: KataTaskIssuesQuery) => {
      if (query.view === "inbox") {
        return {
          view: "inbox",
          groups: [{ id: "inbox", title: "Inbox", issues: [captured] }],
          fetched_at: fetchedAt,
        };
      }
      return buildKataTaskView({ view: query.view, issues, projects, fetched_at: fetchedAt });
    });
    api.mocks.issue.mockImplementation(async (uid: string) => ({
      issue: { ...(uid === captured.uid ? captured : issues[0]!), body: `${uid} body` },
      comments: [],
      labels: [],
      links: [],
      children: [],
      etag: '"rev-1"',
    }));
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("today");
    await store.updateSearchFilters({
      scope: { kind: "project", project_uid: "project-finances" },
      query: "rent",
      owner: "fixture-user",
      label: "work",
    });

    await store.captureIssue("middleman", { title: captured.title }, "01MIDDLEMANCAPTURE00000003");

    expect(store.currentView.name).toBe("inbox");
    expect(store.searchFilters).toEqual({
      scope: { kind: "all" },
      status: "open",
      owner: "",
      label: "",
      query: "",
    });
    expect(api.mocks.issues).toHaveBeenLastCalledWith({ view: "inbox" });
  });

  test("normalizes duplicate candidates from tolerant task error details", () => {
    const candidates = duplicateCandidatesFromError({
      error: {
        code: "duplicate_issue",
        message: "possible duplicate",
        details: {
          duplicate_candidates: [
            { title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" },
            { issue: { title: "Call dentist", qualified_id: "Health#dent" }, reason: "semantic match" },
          ],
        },
      },
    });

    expect(candidates).toEqual([
      { title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" },
      { title: "Call dentist", qualified_id: "Health#dent", reason: "semantic match" },
    ]);
  });

  test("search duplicate responses surface candidates without replacing the current view", async () => {
    const api = createFakeKataTaskAPI();
    api.mocks.search.mockRejectedValueOnce(
      Object.assign(new Error("possible duplicate"), {
        details: {
          duplicate_candidates: [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }],
        },
      }),
    );
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("today");
    const previousGroups = store.currentView.groups;

    await store.updateSearchFilters({ query: "duplicate" });

    expect(store.duplicateCandidates).toEqual([
      { title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" },
    ]);
    expect(store.currentView.groups).toBe(previousGroups);
  });

  test("successful searches clear duplicate candidates", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("today");
    store.duplicateCandidates = [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }];

    await store.updateSearchFilters({ query: "rent" });

    expect(store.duplicateCandidates).toEqual([]);
    expect(store.currentView.groups.flatMap((group) => group.issues).map((item) => item.uid)).toContain(
      "issue-pay-rent",
    );
  });

  test("surfaces auth failures with a stable connection message", async () => {
    const api = createFakeKataTaskAPI();
    api.mocks.instance.mockRejectedValueOnce(
      new KataTaskAPIError({
        status: 401,
        code: "auth_required",
        message: "missing bearer token",
        headers: new Headers(),
      }),
    );
    const store = createKataWorkspaceStore({ api });

    await expect(store.bootstrap()).rejects.toBeInstanceOf(KataTaskAPIError);
    expect(store.connection).toEqual({
      status: "error",
      message: "Authentication required",
    });
  });

  test("selects a different issue detail without reloading the whole view", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    api.mocks.issues.mockClear();

    await store.selectIssue("issue-call-dentist");

    expect(api.mocks.issues).not.toHaveBeenCalled();
    expect(store.currentView.name).toBe("today");
    expect(store.selectedIssue?.issue.title).toBe("Call dentist");
    expect(store.selectedEvents.map((event) => event.issue_uid)).toEqual(["issue-call-dentist"]);
  });

  test("loading an issue loads the full recurrence list for that issue project", async () => {
    const recurrences = [
      recurrence({ id: 1 }),
      recurrence({ id: 2, uid: "recurrence-2", template_title: "Other" }),
      recurrence({ id: 3, uid: "recurrence-3", template_title: "Third" }),
    ];
    const api = createFakeKataTaskAPI();
    api.mocks.recurrences.mockResolvedValue({ recurrences, fetched_at: "t1" });
    const store = createKataWorkspaceStore({ api });

    await store.selectIssue("issue-pay-rent");

    expect(api.mocks.recurrences).toHaveBeenCalledWith(projects[1]!.id);
    await vi.waitFor(() => {
      expect(store.selectedRecurrences.map((item) => item.id)).toEqual([1, 2, 3]);
    });
  });

  test("slow recurrence loading does not delay issue detail selection", async () => {
    const api = createFakeKataTaskAPI();
    const slowRecurrences = deferred<Awaited<ReturnType<KataTaskAPI["recurrences"]>>>();
    api.mocks.recurrences.mockReturnValue(slowRecurrences.promise);
    const store = createKataWorkspaceStore({ api });

    const selected = await store.selectIssue("issue-pay-rent");

    expect(selected).toBe(true);
    expect(store.selectedIssue?.issue.uid).toBe("issue-pay-rent");
    expect(store.pendingSelectionUID).toBeNull();
    expect(store.selectedRecurrences).toEqual([]);

    slowRecurrences.resolve({ recurrences: [recurrence({ id: 9 })], fetched_at: "t1" });
    await vi.waitFor(() => {
      expect(store.selectedRecurrences.map((item) => item.id)).toEqual([9]);
    });
  });

  test("failed recurrence loading does not block issue selection", async () => {
    const loaded = [recurrence({ id: 1, template_title: "Loaded recurrence" })];
    const api = createFakeKataTaskAPI();
    api.mocks.recurrences
      .mockResolvedValueOnce({ recurrences: loaded, fetched_at: "t1" })
      .mockRejectedValueOnce(new Error("recurrence fetch failed"));
    const store = createKataWorkspaceStore({ api });

    await store.selectIssue("issue-pay-rent");
    await vi.waitFor(() => {
      expect(store.selectedRecurrences).toEqual(loaded);
    });
    await expect(store.selectIssue("issue-call-dentist")).resolves.toBe(true);

    expect(store.selectedIssue?.issue.uid).toBe("issue-call-dentist");
    expect(store.selectedEvents.map((event) => event.issue_uid)).toEqual(["issue-call-dentist"]);
    expect(store.selectedRecurrences).toEqual([]);
    expect(store.pendingSelectionUID).toBeNull();
  });

  test("opens a different view and selects its first issue", async () => {
    const store = createKataWorkspaceStore({ api: createFakeKataTaskAPI() });
    await store.bootstrap();

    await store.openView("inbox");

    expect(store.currentView.name).toBe("inbox");
    expect(store.currentView.groups[0]?.title).toBe("Inbox");
    expect(store.selectedIssue?.issue.title).toBe("Renew passport");
  });

  test("project scope shows the project backlog instead of filtering the current system view", async () => {
    const store = createKataWorkspaceStore({ api: createFakeKataTaskAPI() });
    await store.bootstrap();
    await store.openView("inbox");

    await store.updateSearchFilters({ scope: { kind: "project", project_uid: "project-kata" } });

    expect(store.searchFilters.scope).toEqual({ kind: "project", project_uid: "project-kata" });
    expect(store.currentView.name).toBe("all");
    expect(store.currentView.groups.map((group) => group.title)).toEqual(["Results"]);
    expect(store.currentView.groups.flatMap((group) => group.issues).map((item) => item.title)).toEqual([
      "Email Susan re: Q3",
      "Read design notes",
    ]);
  });

  test("search-backed views select the first visible parent row", async () => {
    const parent = {
      ...issue("issue-parent", "Parent task", "project-kata"),
      short_id: "parent",
      qualified_id: "Kata#parent",
      child_counts: { open: 1, total: 1 },
    };
    const child = {
      ...issue("issue-child", "Child task", "project-kata"),
      short_id: "child",
      qualified_id: "Kata#child",
      parent_short_id: "parent",
    };
    const api = createFakeKataTaskAPI();
    api.mocks.search.mockResolvedValueOnce({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "parent" },
      issues: [child, parent],
      fetched_at: fetchedAt,
    });
    api.mocks.issue.mockImplementation(async (uid: string) => ({
      ...detailFor(uid),
      issue: { ...(uid === child.uid ? child : parent), body: `${uid} body` },
    }));
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();

    await store.updateSearchFilters({ query: "parent" });

    expect(store.currentView.groups.flatMap((group) => group.issues).map((item) => item.uid)).toEqual([
      "issue-child",
      "issue-parent",
    ]);
    expect(store.selectedIssue?.issue.uid).toBe("issue-parent");
  });

  test("system view navigation keeps the selected project scope", async () => {
    const store = createKataWorkspaceStore({ api: createFakeKataTaskAPI() });
    await store.bootstrap();
    await store.updateSearchFilters({ scope: { kind: "project", project_uid: "project-health" } });

    await store.openView("upcoming");

    expect(store.currentView.name).toBe("upcoming");
    expect(store.currentView.groups).toEqual([]);
    expect(store.selectedIssue).toBeNull();
  });

  test("project-scoped system view refreshes through issues instead of search", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    await store.updateSearchFilters({ scope: { kind: "project", project_uid: "project-kata" } });
    await store.openView("upcoming");
    api.mocks.search.mockClear();
    api.mocks.issues.mockClear();

    await store.applyRemoteEvent({
      ...events[0]!,
      event_id: 50,
      issue_uid: "issue-email-susan",
      project_uid: "project-kata",
      project_name: "Kata",
      project_id: projects[3]!.id,
    });

    expect(api.mocks.search).not.toHaveBeenCalled();
    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "upcoming", project_uid: "project-kata" });
    expect(store.currentView.name).toBe("upcoming");
  });

  test("mutation refresh keeps project-scoped system view and preserves selection", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    await store.updateSearchFilters({ scope: { kind: "project", project_uid: "project-kata" } });
    await store.openView("upcoming");
    await store.selectIssue("issue-read-design-notes");
    api.mocks.search.mockClear();
    api.mocks.issues.mockClear();

    await store.addLabel("issue-read-design-notes", "fixture-user", "followup");

    expect(api.mocks.addLabel).toHaveBeenCalledWith(
      { project_id: projects[3]!.id, ref: "issue-read-design-notes" },
      "fixture-user",
      "followup",
    );
    expect(api.mocks.search).not.toHaveBeenCalled();
    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "upcoming", project_uid: "project-kata" });
    expect(store.currentView.name).toBe("upcoming");
    expect(store.selectedIssue?.issue.uid).toBe("issue-read-design-notes");
  });

  test("metadata patches use selected detail ETag and refresh the selected detail", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();

    await store.patchMetadata("issue-pay-rent", "fixture-user", { scheduled_on: "2026-05-20" });

    expect(api.mocks.patchIssueMetadata).toHaveBeenCalledWith(
      { project_id: projects[1]!.id, ref: "issue-pay-rent" },
      "fixture-user",
      { scheduled_on: "2026-05-20" },
      '"rev-1"',
    );
    expect(store.selectedIssue?.issue.uid).toBe("issue-pay-rent");
    expect(api.mocks.issue).toHaveBeenCalledWith("issue-pay-rent");
  });

  test("serializes metadata patches so rapid edits use the refreshed ETag", async () => {
    const api = createFakeKataTaskAPI();
    let currentIssue = { ...issues[0]!, metadata: { ...issues[0]!.metadata } };
    api.mocks.patchIssueMetadata.mockImplementation(
      async (
        _target: { project_id: number; ref: string },
        _actor: string,
        patch: Record<string, unknown>,
      ): Promise<{ changed: boolean; issue?: KataTaskSummary; etag?: string }> => {
        currentIssue = {
          ...currentIssue,
          metadata: { ...currentIssue.metadata, ...patch },
          revision: currentIssue.revision + 1,
        };
        return { changed: true, issue: currentIssue, etag: `"rev-${currentIssue.revision}"` };
      },
    );
    api.mocks.issue.mockImplementation(async (uid: string) => {
      if (uid !== currentIssue.uid) return detailFor(uid);
      return { ...detailFor(uid), issue: currentIssue, etag: `"rev-${currentIssue.revision}"` };
    });
    api.mocks.issues.mockImplementation(async (query: KataTaskIssuesQuery) =>
      buildKataTaskView({
        view: query.view,
        issues: [currentIssue, ...issues.slice(1)],
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      }),
    );
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();

    await Promise.all([
      store.patchMetadata("issue-pay-rent", "fixture-user", { scheduled_on: "2026-05-20" }),
      store.patchMetadata("issue-pay-rent", "fixture-user", { deadline_on: "2026-05-21" }),
    ]);

    expect(api.mocks.patchIssueMetadata.mock.calls.map((call) => call[3])).toEqual(['"rev-1"', '"rev-2"']);
    expect(store.selectedIssue?.issue.metadata).toMatchObject({
      scheduled_on: "2026-05-20",
      deadline_on: "2026-05-21",
    });
    expect(store.selectedIssue?.issue.revision).toBe(3);
  });

  test("moveIssue uses the selected ETag and refreshes projects", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    api.mocks.projects.mockClear();

    await store.moveIssue("issue-pay-rent", "fixture-user", "project-kata");

    expect(api.mocks.moveIssue).toHaveBeenCalledWith(
      { project_id: projects[1]!.id, ref: "issue-pay-rent" },
      "fixture-user",
      "project-kata",
      '"rev-1"',
    );
    expect(api.mocks.projects).toHaveBeenCalled();
    expect(store.selectedIssue?.issue.uid).toBe("issue-pay-rent");
  });

  test("createRecurrence calls the API and refetches the selected project recurrence list", async () => {
    const initial = [recurrence({ id: 1, template_title: "Existing" })];
    const created = recurrence({ id: 2, uid: "recurrence-2", template_title: "Brand new" });
    const api = createFakeKataTaskAPI();
    api.mocks.recurrences
      .mockResolvedValueOnce({ recurrences: initial, fetched_at: "t1" })
      .mockResolvedValueOnce({ recurrences: [...initial, created], fetched_at: "t2" });
    api.mocks.createRecurrence.mockResolvedValue({ recurrence: created } satisfies KataRecurrenceResponse);
    const store = createKataWorkspaceStore({ api });
    await store.selectIssue("issue-pay-rent");

    const input: KataCreateRecurrenceInput = {
      actor: "fixture-user",
      rrule: "FREQ=DAILY;INTERVAL=1",
      dtstart: "2026-05-20",
      timezone: "UTC",
      template: { title: "Brand new" },
    };
    const result = await store.createRecurrence(projects[1]!.id, input);

    expect(api.mocks.createRecurrence).toHaveBeenCalledWith(projects[1]!.id, input);
    expect(api.mocks.recurrences).toHaveBeenCalledTimes(2);
    expect(result).toEqual(created);
    expect(store.selectedRecurrences.map((item) => item.id)).toEqual([1, 2]);
  });

  test("patchRecurrence refetches on success and attaches latest response to conflict errors", async () => {
    const before = recurrence({ id: 1, template_title: "T1" });
    const after = { ...before, template_title: "T2", revision: 2 };
    const api = createFakeKataTaskAPI();
    api.mocks.recurrences
      .mockResolvedValueOnce({ recurrences: [before], fetched_at: "t1" })
      .mockResolvedValueOnce({ recurrences: [after], fetched_at: "t2" });
    api.mocks.patchRecurrence.mockResolvedValue({ recurrence: after } satisfies KataRecurrenceResponse);
    const store = createKataWorkspaceStore({ api });
    await store.selectIssue("issue-pay-rent");

    const patch: KataPatchRecurrenceInput = { actor: "fixture-user", template: { title: "T2" } };
    const result = await store.patchRecurrence(1, patch, '"rev-1"');

    expect(api.mocks.patchRecurrence).toHaveBeenCalledWith(projects[1]!.id, "recurrence-1", patch, '"rev-1"');
    expect(result).toEqual(after);
    expect(store.selectedRecurrences[0]!.template_title).toBe("T2");

    const conflict = Object.assign(new Error("conflict"), { status: 412 });
    api.mocks.patchRecurrence.mockRejectedValueOnce(conflict);
    api.mocks.showRecurrence.mockResolvedValueOnce({ recurrence: after, etag: '"rev-2"' });

    await expect(
      store.patchRecurrence(1, { actor: "fixture-user", template: { title: "T3" } }, '"rev-1"'),
    ).rejects.toMatchObject({
      status: 412,
      response: { recurrence: after, etag: '"rev-2"' },
    });
  });

  test("deleteRecurrence refetches after the delete request resolves", async () => {
    const rec = recurrence({ id: 1 });
    const api = createFakeKataTaskAPI();
    api.mocks.recurrences
      .mockResolvedValueOnce({ recurrences: [rec], fetched_at: "t1" })
      .mockResolvedValueOnce({ recurrences: [], fetched_at: "t2" });
    let resolveDelete!: () => void;
    api.mocks.deleteRecurrence.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveDelete = resolve;
        }),
    );
    const store = createKataWorkspaceStore({ api });
    await store.selectIssue("issue-pay-rent");
    const initialCalls = api.mocks.recurrences.mock.calls.length;

    const deletePromise = store.deleteRecurrence(1, "fixture-user");
    await Promise.resolve();
    expect(api.mocks.recurrences.mock.calls.length).toBe(initialCalls);

    resolveDelete();
    await deletePromise;
    expect(api.mocks.deleteRecurrence).toHaveBeenCalledWith(projects[1]!.id, "recurrence-1", "fixture-user", '"rev-1"');
    expect(api.mocks.recurrences.mock.calls.length).toBe(initialCalls + 1);
    expect(store.selectedRecurrences).toEqual([]);
  });

  test("ignores stale search responses when filters change quickly", async () => {
    const api = createFakeKataTaskAPI();
    const oldSearch = deferred<KataTaskSearchResponse>();
    const newSearch = deferred<KataTaskSearchResponse>();
    api.mocks.search.mockImplementation((filters: KataTaskSearchFilters) => {
      if (filters.query === "old") return oldSearch.promise;
      if (filters.query === "new") return newSearch.promise;
      return Promise.resolve({ filters, issues: [], fetched_at: fetchedAt });
    });
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("inbox");
    await store.selectIssue("issue-pay-rent");

    const first = store.updateSearchFilters({ query: "old" });
    const second = store.updateSearchFilters({ query: "new" });
    newSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "new" },
      issues: [issues[1]!],
      fetched_at: "new",
    });
    await second;

    oldSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "old" },
      issues: [issues[0]!],
      fetched_at: "old",
    });
    await first;

    expect(store.searchFilters.query).toBe("new");
    expect(store.currentView.groups.flatMap((group) => group.issues).map((item) => item.title)).toEqual([
      "Call dentist",
    ]);
    expect(store.selectedIssue?.issue.uid).toBe("issue-call-dentist");
  });

  test("ignores stale bootstrap view data after newer navigation", async () => {
    const api = createFakeKataTaskAPI();
    const slowToday = deferred<KataTaskViewResponse>();
    let slowBootstrap = true;
    api.mocks.issues.mockImplementation((query: KataTaskIssuesQuery) => {
      if (slowBootstrap && query.view === "today") return slowToday.promise;
      return createFakeKataTaskAPI().issues(query);
    });
    const store = createKataWorkspaceStore({ api });

    const bootstrap = store.bootstrap("today");
    await Promise.resolve();
    slowBootstrap = false;
    await store.openView("inbox");
    expect(store.currentView.name).toBe("inbox");

    slowToday.resolve(await createFakeKataTaskAPI().issues({ view: "today" }));
    await bootstrap;

    expect(store.connection.status).toBe("online");
    expect(store.currentView.name).toBe("inbox");
    expect(store.selectedIssue?.issue.uid).toBe("issue-renew-passport");
  });

  test("ignores stale manual issue selections after a newer view load", async () => {
    const api = createFakeKataTaskAPI();
    const slowIssue = deferred<KataTaskDetail>();
    let slowPayRent = false;
    api.mocks.issue.mockImplementation((uid: string) => {
      if (slowPayRent && uid === "issue-pay-rent") return slowIssue.promise;
      return Promise.resolve(detailFor(uid));
    });
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();

    slowPayRent = true;
    const selectPayRent = store.selectIssue("issue-pay-rent");
    await store.openView("inbox");
    expect(store.selectedIssue?.issue.uid).toBe("issue-renew-passport");

    slowIssue.resolve(detailFor("issue-pay-rent"));
    const applied = await selectPayRent;

    expect(applied).toBe(false);
    expect(store.selectedIssue?.issue.uid).toBe("issue-renew-passport");
    expect(store.selectedEvents.map((event) => event.issue_uid)).toEqual([]);
  });

  test("delayed addComment refresh preserves a newer manual selection", async () => {
    const api = createFakeKataTaskAPI();
    const slowComment = deferred<{ changed: boolean }>();
    api.mocks.addComment.mockReturnValueOnce(slowComment.promise);
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();

    const mutation = store.addComment("issue-pay-rent", "fixture-user", "Payment is scheduled.");
    await Promise.resolve();
    await store.selectIssue("issue-call-dentist");

    slowComment.resolve({ changed: true });
    await mutation;

    expect(api.mocks.addComment).toHaveBeenCalledWith(
      { project_id: projects[1]!.id, ref: "issue-pay-rent" },
      "fixture-user",
      "Payment is scheduled.",
    );
    expect(store.selectedIssue?.issue.uid).toBe("issue-call-dentist");
    expect(store.selectedEvents.map((event) => event.issue_uid)).toEqual(["issue-call-dentist"]);
  });

  test("delayed mutation detail load does not overwrite a newer manual selection", async () => {
    const api = createFakeKataTaskAPI();
    const slowPayRentDetail = deferred<KataTaskDetail>();
    let holdNextPayRentDetail = false;
    api.mocks.issue.mockImplementation((uid: string) => {
      if (holdNextPayRentDetail && uid === "issue-pay-rent") {
        holdNextPayRentDetail = false;
        return slowPayRentDetail.promise;
      }
      return Promise.resolve(detailFor(uid));
    });
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    api.mocks.issue.mockClear();

    holdNextPayRentDetail = true;
    const mutation = store.addComment("issue-pay-rent", "fixture-user", "Payment is scheduled.");
    await Promise.resolve();
    await Promise.resolve();
    expect(api.mocks.issue).toHaveBeenCalledWith("issue-pay-rent");

    await store.selectIssue("issue-call-dentist");
    expect(store.selectedIssue?.issue.uid).toBe("issue-call-dentist");

    slowPayRentDetail.resolve(detailFor("issue-pay-rent"));
    await mutation;

    expect(store.selectedIssue?.issue.uid).toBe("issue-call-dentist");
    expect(store.selectedEvents.map((event) => event.issue_uid)).toEqual(["issue-call-dentist"]);
  });

  test("syncs event cursor through paged event reads", async () => {
    const api = createFakeKataTaskAPI();
    api.mocks.events.mockImplementation(async (query: KataTaskEventsQuery = {}) => {
      const after = query.after_id ?? 0;
      const rows = [
        { ...events[0]!, event_id: 1 },
        { ...events[1]!, event_id: 2 },
      ]
        .filter((event) => event.event_id > after)
        .slice(0, 1);
      return {
        reset_required: false,
        events: rows,
        next_after_id: rows.at(-1)?.event_id ?? after,
      };
    });
    const store = createKataWorkspaceStore({ api });

    await store.syncEventCursor();

    expect(store.eventCursor).toBe(2);
    const cursorReads = api.mocks.events.mock.calls.filter(([query]) => query?.after_id !== undefined);
    expect(cursorReads.map(([query]) => query?.after_id)).toEqual([0, 1, 2]);
  });

  test("syncing the event cursor applies observed events before advancing", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("inbox");
    api.mocks.issues.mockClear();

    await store.syncEventCursor();

    expect(store.eventCursor).toBe(2);
    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "inbox" });
    expect(store.selectedIssue?.issue.uid).toBe("issue-renew-passport");
  });

  test("syncing the event cursor stops after an unapplied event in a multi-event page", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("all");
    await store.updateSearchFilters({ query: "rent" });
    api.mocks.events.mockResolvedValueOnce({
      reset_required: false,
      events: [
        { ...events[0]!, event_id: 100 },
        { ...events[1]!, event_id: 101 },
      ],
      next_after_id: 101,
    });
    api.mocks.search.mockRejectedValueOnce(
      Object.assign(new Error("possible duplicate"), {
        details: {
          duplicate_candidates: [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }],
        },
      }),
    );

    await store.syncEventCursor();

    expect(store.eventCursor).toBe(0);
    expect(api.mocks.search).toHaveBeenCalledTimes(2);
    const cursorReads = api.mocks.events.mock.calls.filter(([query]) => query?.after_id !== undefined);
    expect(cursorReads.map(([query]) => query?.after_id)).toEqual([0]);
  });

  test("resetEventCursor lets lower-id events from a new daemon apply", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    await store.syncEventCursor();
    const staleCursor = store.eventCursor;
    expect(staleCursor).toBeGreaterThan(1);

    store.resetEventCursor();
    expect(store.eventCursor).toBe(0);

    api.mocks.issues.mockClear();
    const lowID = 1;
    expect(lowID).toBeLessThan(staleCursor);
    await store.applyRemoteEvent({
      ...events[0]!,
      event_id: lowID,
    });

    expect(store.eventCursor).toBe(lowID);
    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "today" });
  });

  test("remote events do not advance the cursor when refreshing the view fails", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    api.mocks.issues.mockRejectedValueOnce(new Error("network down"));

    await expect(
      store.applyRemoteEvent({
        ...events[0]!,
        event_id: 88,
      }),
    ).rejects.toThrow("network down");

    expect(store.eventCursor).toBe(0);
  });

  test("remote thin events invalidate the active view and selected detail", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    await store.selectIssue("issue-pay-rent");
    api.mocks.issues.mockClear();
    api.mocks.issue.mockClear();
    api.mocks.issue.mockResolvedValueOnce({
      ...detailFor("issue-pay-rent"),
      comments: [
        {
          id: 1,
          issue_id: issues[0]!.id,
          body: "Remote note.",
          author: "agent:bookkeeper",
          created_at: fetchedAt,
        },
      ],
    });

    await store.applyRemoteEvent({
      event_id: 77,
      event_uid: "event-remote-note",
      origin_instance_uid: "instance-remote",
      type: "comment.created",
      project_id: projects[1]!.id,
      project_uid: "project-finances",
      project_name: "Finances",
      issue_uid: "issue-pay-rent",
      issue_short_id: "pay-rent",
      actor: "agent:bookkeeper",
      created_at: fetchedAt,
    });

    expect(store.eventCursor).toBe(77);
    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "today" });
    expect(api.mocks.issue).toHaveBeenCalledWith("issue-pay-rent");
    expect(store.selectedIssue?.comments.map((comment) => comment.body)).toContain("Remote note.");
  });

  test("metadata update events patch the visible issue and still refetch the view", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    api.mocks.issues.mockClear();

    await store.applyRemoteEvent({
      event_id: 77,
      event_uid: "event-metadata-update",
      origin_instance_uid: "instance-remote",
      type: "issue.metadata_updated",
      project_id: projects[1]!.id,
      project_uid: "project-finances",
      project_name: "Finances",
      issue_uid: "issue-pay-rent",
      issue_short_id: "pay-rent",
      actor: "agent:bookkeeper",
      payload: {
        revision_new: 3,
        diff: {
          scheduled_on: { from: "2026-05-15", to: "2026-05-20" },
          someday: { from: false, to: true },
        },
      },
      created_at: fetchedAt,
    });

    expect(store.eventCursor).toBe(77);
    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "today" });
    expect(store.selectedIssue?.issue.metadata).toMatchObject({
      scheduled_on: "2026-05-20",
      someday: true,
    });
  });

  test("older metadata update events do not overwrite newer refreshed issue state", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    const newerDetail = detailFor("issue-pay-rent");
    const newerIssue = {
      ...issues[0]!,
      metadata: {
        ...issues[0]!.metadata,
        scheduled_on: "2026-05-25",
        someday: false,
      },
      revision: 5,
    };
    api.mocks.issues.mockResolvedValueOnce(
      buildKataTaskView({
        view: "today",
        issues: [newerIssue],
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      }),
    );
    api.mocks.issue.mockResolvedValueOnce({
      ...newerDetail,
      issue: {
        ...newerDetail.issue,
        metadata: newerIssue.metadata,
        revision: newerIssue.revision,
      },
      etag: '"rev-5"',
    });

    await store.applyRemoteEvent({
      event_id: 77,
      event_uid: "event-stale-metadata-update",
      origin_instance_uid: "instance-remote",
      type: "issue.metadata_updated",
      project_id: projects[1]!.id,
      project_uid: "project-finances",
      project_name: "Finances",
      issue_uid: "issue-pay-rent",
      issue_short_id: "pay-rent",
      actor: "agent:bookkeeper",
      payload: {
        revision_new: 3,
        diff: {
          scheduled_on: { from: "2026-05-15", to: "2026-05-20" },
          someday: { from: false, to: true },
        },
      },
      created_at: fetchedAt,
    });
    const visibleIssue = store.currentView.groups
      .flatMap((group) => group.issues)
      .find((item) => item.uid === newerIssue.uid);

    expect(store.eventCursor).toBe(77);
    expect(visibleIssue?.metadata).toMatchObject({
      scheduled_on: "2026-05-25",
      someday: false,
    });
    expect(visibleIssue?.revision).toBe(5);
    expect(store.selectedIssue?.issue.metadata).toMatchObject({
      scheduled_on: "2026-05-25",
      someday: false,
    });
    expect(store.selectedIssue?.issue.revision).toBe(5);

    await store.patchMetadata("issue-pay-rent", "fixture-user", { deadline_on: "2026-05-30" });

    expect(api.mocks.patchIssueMetadata).toHaveBeenCalledWith(
      { project_id: projects[1]!.id, ref: "issue-pay-rent" },
      "fixture-user",
      { deadline_on: "2026-05-30" },
      '"rev-5"',
    );
  });

  test("syncing the event cursor refreshes reset-required responses before advancing", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("inbox");
    api.mocks.issues.mockClear();
    api.mocks.events.mockResolvedValueOnce({
      reset_required: true,
      reset_after_id: 25,
      events: [],
      next_after_id: 25,
    });

    await store.syncEventCursor();

    expect(api.mocks.issues).toHaveBeenCalledWith({ view: "inbox" });
    expect(store.eventCursor).toBe(25);
  });

  test("sync reset stream messages reload the current view and advance the cursor", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("inbox");

    await store.applyEventStreamMessage({
      kind: "reset",
      event_id: 100,
      reset_after_id: 100,
      lastEventID: 100,
    });

    expect(store.eventCursor).toBe(100);
    expect(api.mocks.issues).toHaveBeenLastCalledWith({ view: "inbox" });
    expect(store.currentView.name).toBe("inbox");
    expect(store.connection.status).toBe("online");
  });

  test("sync reset stream messages preserve active filters and selected issue", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("all");
    await store.updateSearchFilters({
      scope: { kind: "project", project_uid: "project-kata" },
      owner: "agent:planner",
      query: "design",
    });
    await store.selectIssue("issue-read-design-notes");
    api.mocks.search.mockClear();

    await store.applyEventStreamMessage({
      kind: "reset",
      event_id: 100,
      reset_after_id: 100,
      lastEventID: 100,
    });

    expect(store.eventCursor).toBe(100);
    expect(store.searchFilters).toMatchObject({
      scope: { kind: "project", project_uid: "project-kata" },
      owner: "agent:planner",
      query: "design",
    });
    expect(api.mocks.search).toHaveBeenLastCalledWith(store.searchFilters);
    expect(store.selectedIssue?.issue.uid).toBe("issue-read-design-notes");
  });

  test("sync reset stream messages surface duplicate candidates without replacing filtered results", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("all");
    await store.updateSearchFilters({ query: "rent" });
    const previousGroups = store.currentView.groups;
    api.mocks.search.mockRejectedValueOnce(
      Object.assign(new Error("possible duplicate"), {
        details: {
          duplicate_candidates: [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }],
        },
      }),
    );

    await store.applyEventStreamMessage({
      kind: "reset",
      event_id: 100,
      reset_after_id: 100,
      lastEventID: 100,
    });

    expect(store.eventCursor).toBe(0);
    expect(store.duplicateCandidates).toEqual([
      { title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" },
    ]);
    expect(store.currentView.groups).toBe(previousGroups);
  });

  test("remote events do not advance the cursor when filtered refresh surfaces duplicate candidates", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("all");
    await store.updateSearchFilters({ query: "rent" });
    const previousGroups = store.currentView.groups;
    api.mocks.search.mockRejectedValueOnce(
      Object.assign(new Error("possible duplicate"), {
        details: {
          duplicate_candidates: [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }],
        },
      }),
    );

    await store.applyRemoteEvent({ ...events[0]!, event_id: 100 });

    expect(store.eventCursor).toBe(0);
    expect(store.duplicateCandidates).toEqual([
      { title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" },
    ]);
    expect(store.currentView.groups).toBe(previousGroups);
  });

  test("sync reset stream messages clear stale duplicate candidates after a successful filtered refresh", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("all");
    await store.updateSearchFilters({ query: "rent" });
    store.duplicateCandidates = [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }];

    await store.applyEventStreamMessage({
      kind: "reset",
      event_id: 100,
      reset_after_id: 100,
      lastEventID: 100,
    });

    expect(store.eventCursor).toBe(100);
    expect(store.duplicateCandidates).toEqual([]);
    expect(store.currentView.groups.flatMap((group) => group.issues).map((item) => item.uid)).toContain(
      "issue-pay-rent",
    );
  });

  test("sync reset stream messages do not advance the cursor after a superseded reload", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap("today");

    const slow = deferred<KataTaskViewResponse>();
    api.mocks.issues.mockImplementationOnce(async () => slow.promise);
    const reset = store.applyEventStreamMessage({
      kind: "reset",
      event_id: 100,
      reset_after_id: 100,
      lastEventID: 100,
    });
    await vi.waitFor(() => {
      expect(api.mocks.issues).toHaveBeenCalledWith({ view: "today" });
    });

    await store.openView("inbox");
    slow.resolve(
      buildKataTaskView({
        view: "today",
        issues,
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      }),
    );
    await reset;

    expect(store.currentView.name).toBe("inbox");
    expect(store.eventCursor).toBe(0);
  });

  test("sync reset stream messages do not advance the cursor when reload fails", async () => {
    const api = createFakeKataTaskAPI();
    const store = createKataWorkspaceStore({ api });
    await store.bootstrap();
    api.mocks.issues.mockRejectedValueOnce(new Error("daemon offline"));

    await expect(
      store.applyEventStreamMessage({
        kind: "reset",
        event_id: 100,
        reset_after_id: 100,
        lastEventID: 100,
      }),
    ).rejects.toThrow("daemon offline");

    expect(store.eventCursor).toBe(0);
  });
});
