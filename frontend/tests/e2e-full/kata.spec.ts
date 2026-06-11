import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { once } from "node:events";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { expect, type Locator, type Page, test } from "@playwright/test";
import {
  startIsolatedE2EServer as startDefaultIsolatedE2EServer,
  startIsolatedE2EServerWithOptions,
} from "./support/e2eServer";
import { createDocsFixture } from "./support/docsFixture";

async function startIsolatedE2EServer() {
  return startIsolatedE2EServerWithOptions({ visibleImportedModes: true });
}

type BackendState = {
  commentsByUID: Map<string, CommentRow[]>;
  duplicateIssueListResponses: DuplicateIssueListResponse[];
  duplicateProjectSearches: DuplicateProjectSearchResponse[];
  events: EventRow[];
  issues: IssueSummary[];
  links: LinkRow[];
  nextCommentID: number;
  nextRecurrenceID: number;
  recurrences: RecurrenceRow[];
  projects: ProjectRow[];
  seenIfMatches: string[];
  seenStreamLastEventIDs: Array<string | undefined>;
  seenPaths: string[];
  streams: Set<ServerResponse>;
  failNextAssignOwner?: string | undefined;
  failNextMetadataMessage?: string | undefined;
  eventsBarrier?: Promise<void> | undefined;
  issuesBarrier?: Promise<void> | undefined;
  searchBarriers: Map<string, Promise<void>>;
  onEventsRequest?: ((state: BackendState, url: URL) => void) | undefined;
};

type MsgvaultBackendState = {
  authorized: boolean;
};

type BackendHandle = {
  state: BackendState;
  url: string;
  close: () => Promise<void>;
};

type MsgvaultBackendHandle = {
  state: MsgvaultBackendState;
  url: string;
  close: () => Promise<void>;
};

type ProjectRow = {
  id: number;
  uid: string;
  name: string;
  metadata: Record<string, unknown>;
  open_count: number;
};

type DuplicateProjectSearchResponse = {
  projectID: number;
  query: string;
  candidates: Array<{ title: string; qualified_id: string; reason?: string }>;
};

type DuplicateIssueListResponse = {
  candidates: Array<{ title: string; qualified_id: string; reason?: string }>;
};

const now = "2026-05-15T10:00:00Z";
const today = localDateString();
const middlemanCSRFHeader = { "X-Middleman-Csrf": "1" };

const projects: ProjectRow[] = [
  {
    id: 1,
    uid: "project-finance",
    name: "Finances",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  },
  {
    id: 2,
    uid: "project-kata",
    name: "Kata",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 1,
  },
];

const inboxProject: ProjectRow = {
  id: 99,
  uid: "project-inbox",
  name: "Inbox",
  metadata: { area: "Personal", role: "inbox", sidebar_order: 0 },
  open_count: 0,
};

const issues = [
  issueSummary({
    id: 11,
    uid: "issue-rent",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "FIN-1",
    qualified_id: "Finances#FIN-1",
    title: "Pay rent",
    body: "Send June rent from checking.\n\nDue to landlord on the first.",
    owner: "Wes",
    priority: 0,
    labels: ["home"],
    metadata: {
      scheduled_on: today,
      checklist: [{ id: "rent-zelle", text: "Send Zelle", done: false }],
    },
  }),
  issueSummary({
    id: 22,
    uid: "issue-q3",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-7",
    qualified_id: "Kata#kat-7",
    title: "Email Susan re: Q3",
    body: "Confirm the Q3 project review agenda.",
    owner: "Susan",
    labels: ["work"],
  }),
];

type IssueSummary = ReturnType<typeof issueSummary>;
type CommentRow = ReturnType<typeof commentRow>;
type EventRow = ReturnType<typeof eventRow>;
type LinkRow = ReturnType<typeof linkRow>;
type RecurrenceRow = ReturnType<typeof recurrenceRow>;
type KataBackendOptions = {
  events?: EventRow[] | undefined;
  projects?: ProjectRow[] | undefined;
  issues?: IssueSummary[] | undefined;
  links?: LinkRow[] | undefined;
  recurrences?: RecurrenceRow[] | undefined;
  duplicateProjectSearches?: DuplicateProjectSearchResponse[] | undefined;
  eventsBarrier?: Promise<void> | undefined;
  issuesBarrier?: Promise<void> | undefined;
  searchBarriers?: Map<string, Promise<void>> | undefined;
  onEventsRequest?: ((state: BackendState, url: URL) => void) | undefined;
};

function issueSummary(input: {
  id: number;
  uid: string;
  project_id: number;
  project_uid: string;
  project_name: string;
  short_id: string;
  qualified_id: string;
  title: string;
  body: string;
  status?: "open" | "closed" | undefined;
  closed_at?: string | undefined;
  owner?: string | undefined;
  priority?: number | undefined;
  labels: string[];
  parent_short_id?: string | undefined;
  child_counts?: { open: number; total: number } | undefined;
  metadata?: Record<string, unknown> | undefined;
}) {
  return {
    ...input,
    status: input.status ?? "open",
    closed_at: input.closed_at,
    metadata: input.metadata ?? {},
    revision: 1,
    author: "e2e",
    created_at: now,
    updated_at: now,
  };
}

function commentRow(input: { id: number; issue_id: number; author: string; body: string; created_at?: string }) {
  return {
    id: input.id,
    issue_id: input.issue_id,
    author: input.author,
    body: input.body,
    created_at: input.created_at ?? now,
  };
}

function eventRow(input: {
  event_id: number;
  event_uid: string;
  type: string;
  project_id: number;
  project_uid: string;
  project_name: string;
  actor?: string | undefined;
  issue?: Pick<IssueSummary, "id" | "uid" | "short_id"> | undefined;
  payload?: Record<string, unknown> | undefined;
  created_at?: string | undefined;
}) {
  return {
    event_id: input.event_id,
    event_uid: input.event_uid,
    origin_instance_uid: "kata-e2e",
    type: input.type,
    project_id: input.project_id,
    project_uid: input.project_uid,
    project_name: input.project_name,
    issue_id: input.issue?.id,
    issue_uid: input.issue?.uid,
    issue_short_id: input.issue?.short_id,
    actor: input.actor ?? "e2e",
    payload: input.payload,
    created_at: input.created_at ?? now,
  };
}

function linkRow(input: {
  id: number;
  project_id: number;
  from: Pick<IssueSummary, "uid" | "short_id">;
  to: Pick<IssueSummary, "uid" | "short_id">;
  type: "parent" | "blocks" | "related";
  author?: string | undefined;
}) {
  return {
    id: input.id,
    project_id: input.project_id,
    from: { uid: input.from.uid, short_id: input.from.short_id },
    to: { uid: input.to.uid, short_id: input.to.short_id },
    type: input.type,
    author: input.author ?? "e2e",
    created_at: now,
  };
}

function recurrenceRow(input: {
  id: number;
  uid: string;
  project_id: number;
  rrule: string;
  dtstart: string;
  timezone: string;
  template_title: string;
  template_body?: string | undefined;
  template_labels?: string[] | undefined;
  template_metadata?: Record<string, unknown> | undefined;
  author?: string | undefined;
  revision?: number | undefined;
  deleted_at?: string | undefined;
}) {
  return {
    id: input.id,
    uid: input.uid,
    project_id: input.project_id,
    rrule: input.rrule,
    dtstart: input.dtstart,
    timezone: input.timezone,
    template_title: input.template_title,
    template_body: input.template_body ?? "",
    template_labels: input.template_labels ?? [],
    template_metadata: input.template_metadata ?? {},
    author: input.author ?? "e2e",
    revision: input.revision ?? 1,
    created_at: now,
    updated_at: now,
    deleted_at: input.deleted_at,
  };
}

function localDateString(date = new Date()): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function previousLocalDateString(date = new Date()): string {
  const previous = new Date(date);
  previous.setDate(previous.getDate() - 1);
  return localDateString(previous);
}

async function openDocsEditor(page: Page, baseURL: string, url: string): Promise<Locator> {
  await page.goto(`${baseURL}${url}`);
  const editButton = page.getByRole("button", { name: "Edit", exact: true });
  await expect(editButton).toBeEnabled();
  await editButton.click();
  const editor = page.locator(".cm-editor .cm-content");
  await expect(editor).toBeVisible();
  await editor.click();
  return editor;
}

async function clearEditor(page: Page, editor: Locator): Promise<void> {
  await editor.focus();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.press("Delete");
}

function autocompleteTooltip(page: Page): Locator {
  return page.locator(".cm-tooltip-autocomplete").locator("visible=true");
}

function appHeaderTab(page: Page, name: string): Locator {
  return page.locator(".tab-group").getByRole("button", { name, exact: true });
}

async function startKataBackend(options: KataBackendOptions = {}): Promise<BackendHandle> {
  const rows = (options.issues ?? issues).map((issue) => ({
    ...issue,
    labels: [...issue.labels],
    metadata: { ...issue.metadata },
  }));
  const state: BackendState = {
    commentsByUID: new Map([
      ["issue-rent", [commentRow({ id: 1, issue_id: 11, author: "e2e", body: "Verify amount against the lease." })]],
    ]),
    duplicateIssueListResponses: [],
    duplicateProjectSearches: [...(options.duplicateProjectSearches ?? [])],
    events: [...(options.events ?? [])],
    issues: rows,
    links: [...(options.links ?? [])],
    nextCommentID: 2,
    nextRecurrenceID: 1,
    recurrences: [...(options.recurrences ?? [])],
    projects: options.projects ?? projects,
    seenIfMatches: [],
    seenStreamLastEventIDs: [],
    seenPaths: [],
    streams: new Set(),
    eventsBarrier: options.eventsBarrier,
    issuesBarrier: options.issuesBarrier,
    searchBarriers: options.searchBarriers ?? new Map(),
    onEventsRequest: options.onEventsRequest,
  };
  const server = createServer((req, res) => {
    void handleKataRequest(state, req, res);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const addr = server.address() as AddressInfo;
  return {
    state,
    url: `http://127.0.0.1:${addr.port}`,
    close: async () => {
      for (const stream of state.streams) {
        stream.end();
      }
      await closeServer(server);
    },
  };
}

async function startMsgvaultBackend(): Promise<MsgvaultBackendHandle> {
  const state: MsgvaultBackendState = {
    authorized: false,
  };
  const server = createServer((req, res) => {
    handleMsgvaultRequest(state, req, res);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const addr = server.address() as AddressInfo;
  return {
    state,
    url: `http://127.0.0.1:${addr.port}`,
    close: () => closeServer(server),
  };
}

async function handleKataRequest(state: BackendState, req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  state.seenPaths.push(`${req.method ?? "GET"} ${url.pathname}${url.search}`);

  const recurrencesRoute = /^\/api\/v1\/projects\/(\d+)\/recurrences$/.exec(url.pathname);
  if (recurrencesRoute) {
    const projectID = Number(recurrencesRoute[1]);
    if (req.method === "POST") {
      await handleCreateRecurrence(state, req, res, projectID);
      return;
    }
    writeJSON(res, 200, {
      recurrences: state.recurrences.filter(
        (recurrence) => recurrence.project_id === projectID && recurrence.deleted_at === undefined,
      ),
      fetched_at: now,
    });
    return;
  }

  const recurrenceRoute = /^\/api\/v1\/projects\/(\d+)\/recurrences\/([^/]+)$/.exec(url.pathname);
  if (recurrenceRoute) {
    await handleRecurrenceDetail(state, req, res, {
      projectID: Number(recurrenceRoute[1]),
      uid: decodeURIComponent(recurrenceRoute[2] ?? ""),
    });
    return;
  }

  const projectRoute = /^\/api\/v1\/projects\/(\d+)$/.exec(url.pathname);
  if (projectRoute) {
    await handleProjectRename(state, req, res, Number(projectRoute[1]));
    return;
  }

  const issueCreateRoute = /^\/api\/v1\/projects\/(\d+)\/issues$/.exec(url.pathname);
  if (issueCreateRoute) {
    await handleIssueCreate(state, req, res, Number(issueCreateRoute[1]));
    return;
  }

  const issueEditRoute = /^\/api\/v1\/projects\/(\d+)\/issues\/([^/]+)$/.exec(url.pathname);
  if (issueEditRoute) {
    await handleIssueEdit(state, req, res, {
      projectID: Number(issueEditRoute[1]),
      ref: decodeURIComponent(issueEditRoute[2] ?? ""),
    });
    return;
  }

  const projectSearchRoute = /^\/api\/v1\/projects\/(\d+)\/search$/.exec(url.pathname);
  if (projectSearchRoute) {
    const projectID = Number(projectSearchRoute[1]);
    const q = url.searchParams.get("q") ?? "";
    const barrierKey = `${projectID}:${q}`;
    const barrier = state.searchBarriers.get(barrierKey);
    state.searchBarriers.delete(barrierKey);
    await barrier;
    const duplicateIndex = state.duplicateProjectSearches.findIndex(
      (item) => item.projectID === projectID && item.query === q,
    );
    if (duplicateIndex !== -1) {
      const [duplicate] = state.duplicateProjectSearches.splice(duplicateIndex, 1);
      writeJSON(res, 409, {
        error: {
          code: "duplicate_issue",
          message: "possible duplicate",
          details: {
            duplicate_candidates: duplicate?.candidates ?? [],
          },
        },
      });
      return;
    }
    writeJSON(res, 200, {
      query: q,
      results: searchIssues(state, { projectID, q }).map((issue) => ({ issue })),
      fetched_at: now,
    });
    return;
  }

  if (url.pathname === "/api/v1/events/stream") {
    const lastEventID = req.headers["last-event-id"];
    state.seenStreamLastEventIDs.push(Array.isArray(lastEventID) ? lastEventID[0] : lastEventID);
    res.writeHead(200, {
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "Content-Type": "text/event-stream",
    });
    res.write(": connected\n\n");
    state.streams.add(res);
    // Middleman's own SSE endpoint and real daemons send periodic
    // keepalive comments. Beyond fidelity, the heartbeat matters on
    // Linux WebKit: its network stack delivers a fetch-stream chunk's
    // tail beyond ~128 bytes only when new data arrives on the socket,
    // so a heartbeat-free stream leaves the final bytes of an emitted
    // frame undelivered and the page never reacts to the event.
    const heartbeat = setInterval(() => {
      res.write(": keepalive\n\n");
    }, 250);
    req.on("close", () => {
      clearInterval(heartbeat);
      state.streams.delete(res);
    });
    return;
  }

  const issueRoute =
    /^\/api\/v1\/projects\/(\d+)\/issues\/([^/]+)\/(comments|labels|metadata|actions)(?:\/([^/]+))?$/.exec(
      url.pathname,
    );
  if (issueRoute) {
    await handleIssueMutation(state, req, res, url, {
      projectID: Number(issueRoute[1]),
      ref: decodeURIComponent(issueRoute[2] ?? ""),
      kind: issueRoute[3] ?? "",
      label: issueRoute[4] ? decodeURIComponent(issueRoute[4]) : undefined,
    });
    return;
  }

  const issueDetailRoute = /^\/api\/v1\/issues\/([^/]+)$/.exec(url.pathname);
  if (issueDetailRoute) {
    writeIssueDetail(state, res, decodeURIComponent(issueDetailRoute[1] ?? ""));
    return;
  }

  switch (url.pathname) {
    case "/api/v1/instance":
      writeJSON(res, 200, {
        instance_uid: "kata-e2e",
        version: "0.0.0-e2e",
        schema_version: 1,
      });
      return;
    case "/api/v1/projects":
      if (req.method === "POST") {
        await handleProjectCreate(state, req, res);
        return;
      }
      writeJSON(res, 200, {
        projects: state.projects,
        fetched_at: now,
      });
      return;
    case "/api/v1/issues":
      {
        const barrier = state.issuesBarrier;
        state.issuesBarrier = undefined;
        await barrier;
        const duplicate = state.duplicateIssueListResponses.shift();
        if (duplicate) {
          writeJSON(res, 409, {
            error: {
              code: "duplicate_issue",
              message: "possible duplicate",
              details: {
                duplicate_candidates: duplicate.candidates,
              },
            },
          });
          return;
        }
      }
      writeJSON(res, 200, {
        issues: issuesForStatus(state.issues, url.searchParams.get("status")),
        fetched_at: now,
      });
      return;
    case "/api/v1/events":
      {
        const issueUID = url.searchParams.get("issue_uid");
        if (url.searchParams.get("after_id") === "0") {
          await state.eventsBarrier;
          state.eventsBarrier = undefined;
        }
        const afterID = Number(url.searchParams.get("after_id") ?? "0");
        const projectID = url.searchParams.get("project_id");
        const events = state.events.filter((event) => {
          if (event.event_id <= afterID) return false;
          if (projectID !== null && event.project_id !== Number(projectID)) return false;
          if (issueUID !== null && event.issue_uid !== issueUID) return false;
          return true;
        });
        const limit = Number(url.searchParams.get("limit") ?? "0");
        const page = limit > 0 ? events.slice(0, limit) : events;
        state.onEventsRequest?.(state, url);
        writeJSON(res, 200, {
          reset_required: false,
          events: page,
          next_after_id: page.at(-1)?.event_id ?? afterID,
        });
      }
      return;
    default:
      writeJSON(res, 404, { error: "not_found", message: url.pathname });
  }
}

function emitKataReset(state: BackendState, eventID: number): void {
  const frame = [
    `id: ${eventID}`,
    "event: sync.reset_required",
    `data: ${JSON.stringify({ event_id: eventID, reset_after_id: eventID })}`,
    "",
    "",
  ].join("\n");
  for (const stream of state.streams) {
    stream.write(frame);
  }
}

function emitKataEvent(state: BackendState, event: EventRow): void {
  const frame = [`id: ${event.event_id}`, `event: ${event.type}`, `data: ${JSON.stringify(event)}`, "", ""].join("\n");
  for (const stream of state.streams) {
    stream.write(frame);
  }
}

function issuesForStatus(rows: IssueSummary[], status: string | null): IssueSummary[] {
  if (status === "closed") return rows.filter((issue) => issue.status === "closed");
  if (status === "open") return rows.filter((issue) => issue.status === "open");
  return rows;
}

async function handleProjectCreate(state: BackendState, req: IncomingMessage, res: ServerResponse): Promise<void> {
  const payload = await readJSONBody(req);
  const name = typeof payload.name === "string" ? payload.name.trim() : "";
  if (!name) {
    writeJSON(res, 400, { error: "bad_request", message: "name is required" });
    return;
  }
  const existing = state.projects.find((project) => project.name === name);
  if (existing) {
    writeJSON(res, 200, { project: existing });
    return;
  }
  const project: ProjectRow = {
    id: Math.max(0, ...state.projects.map((item) => item.id)) + 1,
    uid: `project-${name
      .replace(/[^a-z0-9]+/gi, "-")
      .replace(/^-|-$/g, "")
      .toLowerCase()}`,
    name,
    metadata: { area: "Unfiled", sidebar_order: state.projects.length + 1 },
    open_count: 0,
  };
  state.projects = [...state.projects, project];
  writeJSON(res, 200, { project });
}

async function handleProjectRename(
  state: BackendState,
  req: IncomingMessage,
  res: ServerResponse,
  projectID: number,
): Promise<void> {
  if (req.method !== "PATCH") {
    writeJSON(res, 405, { error: "method_not_allowed", message: "method not allowed" });
    return;
  }
  const payload = await readJSONBody(req);
  const name = typeof payload.name === "string" ? payload.name.trim() : "";
  if (!name) {
    writeJSON(res, 400, { error: "bad_request", message: "name is required" });
    return;
  }
  const existing = state.projects.find((project) => project.id === projectID);
  if (!existing) {
    writeJSON(res, 404, { error: "not_found", message: "project not found" });
    return;
  }
  const project: ProjectRow = { ...existing, name };
  state.projects = state.projects.map((item) => (item.id === projectID ? project : item));
  state.issues = state.issues.map((issue) =>
    issue.project_id === projectID
      ? {
          ...issue,
          project_name: name,
          qualified_id: `${name}#${issue.short_id}`,
        }
      : issue,
  );
  writeJSON(res, 200, { project, aliases: [] });
}

function searchIssues(state: BackendState, input: { projectID: number; q: string }): IssueSummary[] {
  const q = input.q.trim().toLowerCase();
  return state.issues.filter((issue) => {
    if (issue.project_id !== input.projectID) return false;
    if (!q) return true;
    return [issue.title, issue.body, issue.qualified_id, issue.project_name, issue.owner, issue.labels.join(" ")]
      .filter(Boolean)
      .join(" ")
      .toLowerCase()
      .includes(q);
  });
}

function handleMsgvaultRequest(state: MsgvaultBackendState, req: IncomingMessage, res: ServerResponse): void {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  switch (url.pathname) {
    case "/health":
      writeJSON(res, 200, { status: "ok" });
      return;
    case "/api/v1/stats":
      if (!state.authorized) {
        writeJSON(res, 401, { error: "unauthorized", message: "bad key" });
        return;
      }
      writeJSON(res, 200, { total_messages: 1 });
      return;
    case "/api/v1/search":
      writeJSON(res, 200, {
        query: url.searchParams.get("q") ?? "",
        total: 1,
        page: 1,
        page_size: 20,
        messages: [messageSummary()],
      });
      return;
    case "/api/v1/messages/101":
      writeJSON(res, 200, {
        ...messageSummary(),
        body: "Deploy details are ready for the project sync.",
        body_html: "",
        attachments: [],
      });
      return;
    case "/api/v1/messages/filter":
      writeJSON(res, 200, { messages: [messageSummary()] });
      return;
    case "/api/v1/aggregates":
      writeJSON(res, 200, {
        view_type: url.searchParams.get("view_type") ?? "senders",
        rows: [],
      });
      return;
    default:
      writeJSON(res, 404, { error: "not_found", message: url.pathname });
  }
}

async function handleCreateRecurrence(
  state: BackendState,
  req: IncomingMessage,
  res: ServerResponse,
  projectID: number,
): Promise<void> {
  if (req.method !== "POST") {
    writeJSON(res, 405, { error: "method_not_allowed" });
    return;
  }
  const payload = await readJSONBody(req);
  const template = isRecord(payload.template) ? payload.template : {};
  const id = state.nextRecurrenceID++;
  const recurrence = recurrenceRow({
    id,
    uid: `recurrence-${id}`,
    project_id: projectID,
    rrule: typeof payload.rrule === "string" ? payload.rrule : "FREQ=DAILY;INTERVAL=1",
    dtstart: typeof payload.dtstart === "string" ? payload.dtstart : today,
    timezone: typeof payload.timezone === "string" ? payload.timezone : "UTC",
    template_title: typeof template.title === "string" ? template.title : "Recurring task",
    template_body: typeof template.body === "string" ? template.body : "",
    template_labels: Array.isArray(template.labels)
      ? template.labels.filter((label): label is string => typeof label === "string")
      : [],
    template_metadata: isRecord(template.metadata) ? template.metadata : {},
    author: typeof payload.actor === "string" ? payload.actor : "e2e",
  });
  state.recurrences.push(recurrence);
  res.setHeader("ETag", `"rev-${recurrence.revision}"`);
  writeJSON(res, 201, { recurrence });
}

async function handleRecurrenceDetail(
  state: BackendState,
  req: IncomingMessage,
  res: ServerResponse,
  route: { projectID: number; uid: string },
): Promise<void> {
  const found = state.recurrences.find(
    (recurrence) => recurrence.project_id === route.projectID && recurrence.uid === route.uid,
  );
  if (!found) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }

  if (req.method === "GET") {
    res.setHeader("ETag", `"rev-${found.revision}"`);
    writeJSON(res, 200, { recurrence: found });
    return;
  }

  if (req.method === "PATCH") {
    const payload = await readJSONBody(req);
    if (typeof payload.rrule === "string") found.rrule = payload.rrule;
    if (typeof payload.dtstart === "string") found.dtstart = payload.dtstart;
    if (typeof payload.timezone === "string") found.timezone = payload.timezone;
    if (isRecord(payload.template)) {
      if (typeof payload.template.title === "string") found.template_title = payload.template.title;
      if (typeof payload.template.body === "string") found.template_body = payload.template.body;
      if (Array.isArray(payload.template.labels)) {
        found.template_labels = payload.template.labels.filter((label): label is string => typeof label === "string");
      }
      if (isRecord(payload.template.metadata)) found.template_metadata = payload.template.metadata;
    }
    found.revision += 1;
    found.updated_at = now;
    res.setHeader("ETag", `"rev-${found.revision}"`);
    writeJSON(res, 200, { changed: true, recurrence: found });
    return;
  }

  if (req.method === "DELETE") {
    found.deleted_at = now;
    found.revision += 1;
    found.updated_at = now;
    res.statusCode = 204;
    res.end();
    return;
  }

  writeJSON(res, 405, { error: "method_not_allowed" });
}

function messageSummary() {
  return {
    id: 101,
    conversation_id: 501,
    subject: "Project sync",
    from: "alice@example.com",
    to: ["bob@example.com"],
    cc: [],
    bcc: [],
    sent_at: now,
    snippet: "Deploy details are ready.",
    labels: ["work"],
    has_attachments: false,
    size_bytes: 2048,
    deleted_at: null,
  };
}

async function handleIssueEdit(
  state: BackendState,
  req: IncomingMessage,
  res: ServerResponse,
  route: { projectID: number; ref: string },
): Promise<void> {
  const found = state.issues.find((issue) => issue.project_id === route.projectID && issue.uid === route.ref);
  if (!found) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }

  if (req.method !== "PATCH") {
    writeJSON(res, 405, { error: "method_not_allowed" });
    return;
  }

  const payload = await readJSONBody(req);
  if (typeof payload.title === "string") {
    found.title = payload.title;
  }
  if (typeof payload.body === "string") {
    found.body = payload.body;
  }
  if (isRecord(payload.links_delta)) {
    applyLinksDelta(state, found, payload.links_delta);
  }
  found.revision += 1;
  res.setHeader("ETag", `"rev-${found.revision}"`);
  writeJSON(res, 200, { changed: true, issue: found });
}

async function handleIssueCreate(
  state: BackendState,
  req: IncomingMessage,
  res: ServerResponse,
  projectID: number,
): Promise<void> {
  if (req.method !== "POST") {
    writeJSON(res, 405, { error: "method_not_allowed" });
    return;
  }
  const project = state.projects.find((candidate) => candidate.id === projectID);
  if (!project) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }
  const payload = await readJSONBody(req);
  const title = typeof payload.title === "string" ? payload.title.trim() : "";
  if (!title) {
    writeJSON(res, 400, { error: "bad_request" });
    return;
  }
  const id = Math.max(0, ...state.issues.map((issue) => issue.id)) + 1;
  const shortID = `IN-${id}`;
  const issue = issueSummary({
    id,
    uid: `issue-capture-${id}`,
    project_id: project.id,
    project_uid: project.uid,
    project_name: project.name,
    short_id: shortID,
    qualified_id: `${project.name}#${shortID}`,
    title,
    body: typeof payload.body === "string" ? payload.body : "",
    labels: Array.isArray(payload.labels)
      ? payload.labels.filter((label): label is string => typeof label === "string")
      : [],
    metadata: {},
  });
  state.issues = [issue, ...state.issues];
  adjustProjectOpenCount(state, project.id, 1);
  res.setHeader("ETag", `"rev-${issue.revision}"`);
  writeJSON(res, 201, { changed: true, issue });
}

function applyLinksDelta(state: BackendState, source: IssueSummary, delta: Record<string, unknown>): void {
  const refs = Array.isArray(delta.add_related)
    ? delta.add_related.filter((ref): ref is string => typeof ref === "string" && ref.trim() !== "")
    : [];
  for (const ref of refs) {
    const peer = state.issues.find((issue) => issue.uid === ref || issue.short_id === ref);
    if (!peer) continue;
    const duplicate = state.links.some(
      (link) =>
        link.type === "related" &&
        ((link.from.uid === source.uid && link.to.uid === peer.uid) ||
          (link.from.uid === peer.uid && link.to.uid === source.uid)),
    );
    if (duplicate) continue;
    state.links.push(
      linkRow({
        id: state.links.length + 1,
        project_id: source.project_id,
        from: source,
        to: peer,
        type: "related",
      }),
    );
  }
}

async function handleIssueMutation(
  state: BackendState,
  req: IncomingMessage,
  res: ServerResponse,
  url: URL,
  route: { projectID: number; ref: string; kind: string; label?: string | undefined },
): Promise<void> {
  const found = state.issues.find((issue) => issue.project_id === route.projectID && issue.uid === route.ref);
  if (!found) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }

  if (req.method === "POST" && route.kind === "comments") {
    const payload = await readJSONBody(req);
    const body = typeof payload.body === "string" ? payload.body : "";
    const author = typeof payload.actor === "string" ? payload.actor : "e2e";
    const comment = commentRow({ id: state.nextCommentID++, issue_id: found.id, author, body });
    state.commentsByUID.set(found.uid, [comment, ...(state.commentsByUID.get(found.uid) ?? [])]);
    writeJSON(res, 200, { changed: true, issue: found, comment });
    return;
  }

  if (req.method === "POST" && route.kind === "labels") {
    const payload = await readJSONBody(req);
    const label = typeof payload.label === "string" ? payload.label : "";
    const author = typeof payload.actor === "string" ? payload.actor : "e2e";
    if (label && !found.labels.includes(label)) {
      found.labels = [...found.labels, label];
    }
    writeJSON(res, 200, {
      changed: true,
      issue: found,
      label: label ? { issue_id: found.id, label, author, created_at: now } : undefined,
    });
    return;
  }

  if (req.method === "DELETE" && route.kind === "labels" && route.label) {
    void url.searchParams.get("actor");
    found.labels = found.labels.filter((label) => label !== route.label);
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  if (req.method === "PUT" && route.kind === "metadata") {
    state.seenIfMatches.push(req.headers["if-match"]?.toString() ?? "");
    if (state.failNextMetadataMessage !== undefined) {
      const message = state.failNextMetadataMessage;
      state.failNextMetadataMessage = undefined;
      writeJSON(res, 503, { error: { code: "metadata_unavailable", message } });
      return;
    }
    const payload = await readJSONBody(req);
    const patch = isRecord(payload.patch) ? payload.patch : {};
    found.metadata = { ...found.metadata, ...patch };
    found.revision += 1;
    res.setHeader("ETag", `"rev-${found.revision}"`);
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "assign") {
    const payload = await readJSONBody(req);
    if (state.failNextAssignOwner !== undefined) {
      const detail = state.failNextAssignOwner;
      state.failNextAssignOwner = undefined;
      writeJSON(res, 503, { error: { code: "owner_unavailable", message: detail } });
      return;
    }
    const owner = typeof payload.owner === "string" ? payload.owner : "";
    if (owner) {
      found.owner = owner;
    }
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "unassign") {
    delete found.owner;
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "priority") {
    const payload = await readJSONBody(req);
    if (typeof payload.priority === "number") {
      found.priority = payload.priority;
    } else {
      delete found.priority;
    }
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "move") {
    const payload = await readJSONBody(req);
    const toProjectUID = typeof payload.to_project_uid === "string" ? payload.to_project_uid : "";
    const project = state.projects.find((candidate) => candidate.uid === toProjectUID);
    if (!project) {
      writeJSON(res, 404, { error: "not_found" });
      return;
    }
    const wasOpen = found.status !== "closed";
    const fromProjectID = found.project_id;
    found.project_id = project.id;
    found.project_uid = project.uid;
    found.project_name = project.name;
    found.short_id = `${project.name.slice(0, 3).toLowerCase()}-${found.id}`;
    found.qualified_id = `${project.name}#${found.short_id}`;
    found.revision += 1;
    if (wasOpen && fromProjectID !== project.id) {
      adjustProjectOpenCount(state, fromProjectID, -1);
      adjustProjectOpenCount(state, project.id, 1);
    }
    res.setHeader("ETag", `"rev-${found.revision}"`);
    writeJSON(res, 200, { changed: true, issue: found, new_short_id: found.short_id });
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "close") {
    const payload = await readJSONBody(req);
    const wasOpen = found.status !== "closed";
    found.status = "closed";
    found.closed_at = now;
    found.metadata = {
      ...found.metadata,
      closed_reason: typeof payload.reason === "string" ? payload.reason : "done",
      closed_message: typeof payload.message === "string" ? payload.message : "",
    };
    if (wasOpen) {
      adjustProjectOpenCount(state, found.project_id, -1);
    }
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "reopen") {
    const wasClosed = found.status === "closed";
    found.status = "open";
    found.closed_at = undefined;
    const nextMetadata = { ...found.metadata };
    delete nextMetadata.closed_reason;
    delete nextMetadata.closed_message;
    found.metadata = nextMetadata;
    if (wasClosed) {
      adjustProjectOpenCount(state, found.project_id, 1);
    }
    writeJSON(res, 200, { changed: true, issue: found });
    return;
  }

  writeJSON(res, 405, { error: "method_not_allowed" });
}

function adjustProjectOpenCount(state: BackendState, projectID: number, delta: number): void {
  state.projects = state.projects.map((project) =>
    project.id === projectID ? { ...project, open_count: Math.max(0, project.open_count + delta) } : project,
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function writeIssueDetail(state: BackendState, res: ServerResponse, uid: string): void {
  const found = state.issues.find((issue) => issue.uid === uid);
  if (!found) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }
  writeJSON(res, 200, {
    issue: found,
    comments: state.commentsByUID.get(found.uid) ?? [],
    labels: found.labels.map((label) => ({ issue_id: found.id, label, author: "e2e", created_at: now })),
    links: state.links.filter((link) => link.from.uid === found.uid || link.to.uid === found.uid),
    children: state.issues.filter(
      (issue) => issue.project_uid === found.project_uid && issue.parent_short_id === found.short_id,
    ),
  });
}

async function readJSONBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }
  if (chunks.length === 0) return {};
  const body = Buffer.concat(chunks).toString("utf8");
  if (!body) return {};
  const parsed = JSON.parse(body);
  return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
}

async function configureKataHome(backendURL: string): Promise<{ home: string; restore: () => void }> {
  const home = await mkdtemp(path.join(os.tmpdir(), "middleman-kata-e2e-"));
  await mkdir(home, { recursive: true });
  await writeFile(
    path.join(home, "config.toml"),
    ['active_daemon = "e2e"', "", "[[daemon]]", 'name = "e2e"', `url = "${backendURL}"`, ""].join("\n"),
  );

  const previous = process.env.KATA_HOME;
  process.env.KATA_HOME = home;
  return {
    home,
    restore: () => {
      if (previous === undefined) {
        delete process.env.KATA_HOME;
      } else {
        process.env.KATA_HOME = previous;
      }
    },
  };
}

async function configureKataHomeDaemons(
  daemons: { name: string; url: string }[],
  activeDaemon: string,
): Promise<{ home: string; restore: () => void }> {
  const home = await mkdtemp(path.join(os.tmpdir(), "middleman-kata-e2e-"));
  await mkdir(home, { recursive: true });
  await writeFile(
    path.join(home, "config.toml"),
    [
      `active_daemon = ${JSON.stringify(activeDaemon)}`,
      "",
      ...daemons.flatMap((daemon) => [
        "[[daemon]]",
        `name = ${JSON.stringify(daemon.name)}`,
        `url = ${JSON.stringify(daemon.url)}`,
        "",
      ]),
    ].join("\n"),
  );

  const previous = process.env.KATA_HOME;
  process.env.KATA_HOME = home;
  return {
    home,
    restore: () => {
      if (previous === undefined) {
        delete process.env.KATA_HOME;
      } else {
        process.env.KATA_HOME = previous;
      }
    },
  };
}

function writeJSON(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

async function closeServer(server: Server): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    server.close((err) => {
      if (err) reject(err);
      else resolve();
    });
  });
}

test("kata workspace reads tasks through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const payRentRow = page.getByRole("button", {
      name: /(?=.*Pay rent)(?=.*Finances#FIN-1)(?=.*project: Finances)(?=.*owner: Wes)(?=.*priority: 0)(?=.*home)/,
    });
    await expect(payRentRow).toBeVisible();
    await payRentRow.click();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");

    await page.getByRole("button", { name: /^Kata\s+1$/ }).click();

    await expect(page.getByRole("heading", { name: "Kata", level: 2 })).toBeVisible();
    await expect(page.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects?include=stats");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues?status=open");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace initial load does not mutate the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startDefaultIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events/stream");
    expect(backend.state.seenPaths.filter((path) => !path.startsWith("GET "))).toEqual([]);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace toggles and reloads the task detail layout", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(`${server.info.base_url}/kata`);

    const separator = page.getByRole("separator", { name: "Resize Kata panes" });
    await expect(separator).toHaveAttribute("aria-orientation", "horizontal");
    await page.getByRole("button", { name: "Switch to side-by-side layout" }).click();
    await expect(separator).toHaveAttribute("aria-orientation", "vertical");

    await page.reload();
    await expect(page.getByRole("separator", { name: "Resize Kata panes" })).toHaveAttribute(
      "aria-orientation",
      "vertical",
    );
    await expect(page.getByRole("button", { name: "Switch to stacked layout" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata quick capture creates and selects a new inbox task through the configured external daemon", async ({
  page,
}) => {
  const backend = await startKataBackend({ projects: [inboxProject, ...projects] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await page.getByRole("button", { name: "New task" }).click();
    const dialog = page.getByRole("dialog", { name: "New task" });
    const input = dialog.getByRole("textbox", { name: "Quick capture" });
    await expect(input).toBeFocused();

    await input.fill("Capture from browser");
    await dialog.getByRole("button", { name: "Capture" }).click();

    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Capture from browser" })).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Inbox" }).getByRole("button", { name: /Capture from browser/ }),
    ).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects/99/issues");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata inbox view only shows tasks from role-based inbox projects", async ({ page }) => {
  const genericInboxProject: ProjectRow = {
    id: 100,
    uid: "project-generic-inbox",
    name: "Inbox",
    metadata: { area: "Personal", sidebar_order: 2 },
    open_count: 1,
  };
  const roleInboxIssue = issueSummary({
    id: 901,
    uid: "issue-role-inbox",
    project_id: inboxProject.id,
    project_uid: inboxProject.uid,
    project_name: inboxProject.name,
    short_id: "role-inbox",
    qualified_id: "Inbox#role-inbox",
    title: "Role-based inbox task",
    body: "This task belongs in the inbox view.",
    labels: ["triage"],
  });
  const genericInboxIssue = issueSummary({
    id: 902,
    uid: "issue-generic-inbox",
    project_id: genericInboxProject.id,
    project_uid: genericInboxProject.uid,
    project_name: genericInboxProject.name,
    short_id: "generic-inbox",
    qualified_id: "Inbox#generic-inbox",
    title: "Generic Inbox project task",
    body: "A project name alone should not make this an inbox task.",
    labels: ["triage"],
  });
  const backend = await startKataBackend({
    projects: [inboxProject, genericInboxProject, ...projects],
    issues: [roleInboxIssue, genericInboxIssue, ...issues],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?view=inbox`);

    await expect(page.getByRole("heading", { name: "Inbox", level: 2, exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: /Role-based inbox task/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Generic Inbox project task/ })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues?status=open");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace reloads from reset frames on the configured daemon stream", async ({ page }) => {
  const backend = await startKataBackend({ issues: [issues[0]!] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events/stream");

    backend.state.issues = [
      {
        ...issues[1]!,
        metadata: { ...issues[1]!.metadata, scheduled_on: today },
      },
    ];
    emitKataReset(backend.state, 6);

    await expect(page.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace applies a final reset frame when the configured daemon stream closes", async ({ page }) => {
  const backend = await startKataBackend({ issues: [issues[0]!] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events/stream");

    backend.state.issues = [
      {
        ...issues[1]!,
        metadata: { ...issues[1]!.metadata, scheduled_on: today },
      },
    ];
    for (const stream of backend.state.streams) {
      stream.write(
        ["id: 6", "event: sync.reset_required", `data: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}`].join(
          "\n",
        ),
      );
      stream.end();
    }

    await expect(page.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace ignores stale metadata update frames from the configured daemon stream", async ({ page }) => {
  const backend = await startKataBackend({ issues: [issues[0]!] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const payRentRow = page.locator(".kata-list").getByRole("button", { name: /Pay rent/ });
    await expect(payRentRow).toBeVisible();
    await payRentRow.click();
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events/stream");
    await expect.poll(() => backend.state.streams.size).toBeGreaterThan(0);
    const issueLoadsBeforeEvent = backend.state.seenPaths.filter(
      (path) => path === "GET /api/v1/issues?status=open",
    ).length;
    const detailLoadsBeforeEvent = backend.state.seenPaths.filter(
      (path) => path === "GET /api/v1/issues/issue-rent",
    ).length;

    backend.state.issues = backend.state.issues.map((issue) =>
      issue.uid === "issue-rent"
        ? {
            ...issue,
            metadata: { ...issue.metadata, deadline_on: "2026-06-10" },
            revision: 5,
          }
        : issue,
    );
    emitKataEvent(
      backend.state,
      eventRow({
        event_id: 700,
        event_uid: "event-rent-metadata-updated",
        type: "issue.metadata_updated",
        project_id: issues[0]!.project_id,
        project_uid: issues[0]!.project_uid,
        project_name: issues[0]!.project_name,
        issue: issues[0]!,
        payload: {
          revision_new: 2,
          diff: {
            deadline_on: { from: null, to: "2026-06-01" },
          },
        },
      }),
    );

    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path === "GET /api/v1/issues?status=open").length)
      .toBeGreaterThan(issueLoadsBeforeEvent);
    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path === "GET /api/v1/issues/issue-rent").length)
      .toBeGreaterThan(detailLoadsBeforeEvent);
    await expect(payRentRow).toContainText("Due Jun 10");
    await expect(detail.getByRole("button", { name: "Edit due date" })).toContainText("Due Jun 10");

    await detail.getByRole("button", { name: "Edit due date" }).click();
    await detail.getByRole("button", { name: "Clear due date" }).click();

    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");
    expect(backend.state.seenIfMatches).toContain('"rev-5"');
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata message unlink failure keeps the linked message visible", async ({ page }) => {
  const linkedIssue = issueSummary({
    ...issues[0]!,
    metadata: {
      ...issues[0]!.metadata,
      mail_links: [
        {
          message_id: 2001,
          conversation_id: 2001,
          subject: "Lease renewal",
          from: "alice@example.com",
          sent_at: "2026-05-15T09:00:00Z",
          added_at: "2026-05-18T00:00:00Z",
        },
      ],
    },
  });
  const backend = await startKataBackend({ issues: [linkedIssue] });
  backend.state.failNextMetadataMessage = "";
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const taskLinks = page.getByRole("region", { name: "Linked messages" });
    await expect(taskLinks).toContainText("Lease renewal");
    await taskLinks.getByRole("button", { name: "Unlink Lease renewal" }).click();

    await expect(page.getByText("Could not unlink message.")).toBeVisible();
    await expect(taskLinks).toContainText("Lease renewal");
    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.metadata.mail_links).toEqual(
      linkedIssue.metadata.mail_links,
    );
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata route reset ignores a stale routed view response", async ({ page }) => {
  let releaseIssues = () => {};
  const stalledIssues = new Promise<void>((resolve) => {
    releaseIssues = resolve;
  });
  const inboxIssue = issueSummary({
    id: 909,
    uid: "issue-inbox-stale",
    project_id: inboxProject.id,
    project_uid: inboxProject.uid,
    project_name: inboxProject.name,
    short_id: "inbox-stale",
    qualified_id: "Inbox#inbox-stale",
    title: "Inbox response that should stay stale",
    body: "This routed response should not replace the winning Today route.",
    labels: ["triage"],
  });
  const backend = await startKataBackend({
    projects: [inboxProject, ...projects],
    issues: [...issues, inboxIssue],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("heading", { name: "Today", level: 2 })).toBeVisible();
    const issueReadsBefore = backend.state.seenPaths.filter((path) => path === "GET /api/v1/issues?status=open").length;
    backend.state.issuesBarrier = stalledIssues;

    await page.goto(`${server.info.base_url}/kata?view=inbox`);
    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path === "GET /api/v1/issues?status=open").length)
      .toBeGreaterThan(issueReadsBefore);

    await page.goto(`${server.info.base_url}/kata`);
    releaseIssues();

    await expect(page.getByRole("heading", { name: "Today", level: 2 })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
  } finally {
    releaseIssues();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata bootstrap applies a route change made while the initial task load is stalled", async ({ page }) => {
  let releaseIssues = () => {};
  const stalledIssues = new Promise<void>((resolve) => {
    releaseIssues = resolve;
  });
  const inboxIssue = issueSummary({
    id: 910,
    uid: "issue-inbox-bootstrap",
    project_id: inboxProject.id,
    project_uid: inboxProject.uid,
    project_name: inboxProject.name,
    short_id: "inbox-bootstrap",
    qualified_id: "Inbox#inbox-bootstrap",
    title: "Route change during bootstrap",
    body: "The final route should own the first visible workspace load.",
    labels: ["triage"],
  });
  const backend = await startKataBackend({
    projects: [inboxProject, ...projects],
    issues: [...issues, inboxIssue],
    issuesBarrier: stalledIssues,
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues?status=open");

    await page.evaluate(() => {
      window.__middleman_navigate_to_route?.("/kata?view=inbox");
    });
    releaseIssues();

    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Today", level: 2 })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /Route change during bootstrap/ })).toBeVisible();
  } finally {
    releaseIssues();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace shows duplicate candidates from reset refreshes without replacing filtered results", async ({
  page,
}) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const taskList = page.locator(".kata-list");
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await page.getByLabel("Search tasks").fill("q3");
    await page.getByRole("button", { name: /Project scope: All projects/ }).click();
    const projectInput = page.getByRole("combobox", { name: "Project scope" });
    await projectInput.fill("kat");
    await page.getByRole("option", { name: /Kata/ }).click();

    await expect(page).toHaveURL(/scope=project-kata/);
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/2/search?q=q3");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events/stream");

    backend.state.duplicateProjectSearches.push({
      projectID: 2,
      query: "q3",
      candidates: [{ title: "Pay rent", qualified_id: "Finances#FIN-1", reason: "same title" }],
    });
    const projectSearchPath = "GET /api/v1/projects/2/search?q=q3";
    emitKataReset(backend.state, 6);

    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path === projectSearchPath).length)
      .toBeGreaterThanOrEqual(2);
    await expect(page.getByRole("list", { name: "Duplicate candidates" })).toContainText("Pay rent");
    await expect(page.getByRole("list", { name: "Duplicate candidates" })).toContainText("Finances#FIN-1");
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path === projectSearchPath).length)
      .toBeGreaterThanOrEqual(2);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace surfaces duplicate candidates from a multi-event catch-up page", async ({ page }) => {
  let releaseEvents!: () => void;
  const eventsBarrier = new Promise<void>((resolve) => {
    releaseEvents = resolve;
  });
  const backend = await startKataBackend({
    events: [
      eventRow({
        event_id: 100,
        event_uid: "event-catch-up-duplicate",
        type: "issue.updated",
        project_id: issues[1]!.project_id,
        project_uid: issues[1]!.project_uid,
        project_name: issues[1]!.project_name,
        issue: issues[1]!,
      }),
      eventRow({
        event_id: 101,
        event_uid: "event-catch-up-later",
        type: "issue.updated",
        project_id: issues[1]!.project_id,
        project_uid: issues[1]!.project_uid,
        project_name: issues[1]!.project_name,
        issue: issues[1]!,
      }),
    ],
    eventsBarrier,
    onEventsRequest: (state, url) => {
      if (url.searchParams.get("after_id") !== "0") return;
      state.duplicateIssueListResponses.push({
        candidates: [{ title: "Pay rent", qualified_id: "Finances#FIN-1", reason: "same title" }],
      });
      state.onEventsRequest = undefined;
    },
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?view=all&scope=project-kata`);

    const taskList = page.locator(".kata-list");
    await expect(page).toHaveURL(/scope=project-kata/);
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events?after_id=0&limit=100");
    const issueListPath = "GET /api/v1/issues?status=open";
    const issueListCount = backend.state.seenPaths.filter((path) => path === issueListPath).length;

    releaseEvents();

    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path === issueListPath).length)
      .toBeGreaterThan(issueListCount);
    await expect(page.getByRole("list", { name: "Duplicate candidates" })).toContainText("Pay rent");
    await expect(page.getByRole("list", { name: "Duplicate candidates" })).toContainText("Finances#FIN-1");
    await expect
      .poll(() => backend.state.seenStreamLastEventIDs.length, {
        message: "Kata stream should open after catch-up",
      })
      .toBeGreaterThan(0);
    expect(backend.state.seenStreamLastEventIDs).not.toContain("101");
  } finally {
    releaseEvents();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace applies cursor catch-up events before opening the configured daemon stream", async ({ page }) => {
  const bootstrapIssue = issueSummary({
    id: 301,
    uid: "issue-bootstrap-only",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "FIN-bootstrap",
    qualified_id: "Finances#FIN-bootstrap",
    title: "Bootstrap-only task",
    body: "This task should be replaced by the catch-up refresh.",
    labels: ["home"],
    metadata: { scheduled_on: today },
  });
  const catchupIssue = issueSummary({
    ...bootstrapIssue,
    title: "Catch-up refreshed task",
    body: "This task was loaded after the cursor catch-up event.",
  });
  const backend = await startKataBackend({
    issues: [bootstrapIssue],
    events: [
      eventRow({
        event_id: 9,
        event_uid: "event-catch-up-refresh",
        type: "issue.updated",
        project_id: catchupIssue.project_id,
        project_uid: catchupIssue.project_uid,
        project_name: catchupIssue.project_name,
        issue: catchupIssue,
      }),
    ],
    onEventsRequest: (state) => {
      state.issues = [catchupIssue];
      state.onEventsRequest = undefined;
    },
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("button", { name: /Catch-up refreshed task/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Bootstrap-only task/ })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events?after_id=0&limit=100");
    await expect
      .poll(() => backend.state.seenPaths.some((path) => path.startsWith("GET /api/v1/events/stream")))
      .toBe(true);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace reconnects after a closed configured daemon stream", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path.startsWith("GET /api/v1/events/stream")).length)
      .toBe(1);
    for (const stream of backend.state.streams) {
      stream.end();
    }

    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path.startsWith("GET /api/v1/events/stream")).length)
      .toBeGreaterThanOrEqual(2);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace reconnects a closed configured daemon stream", async ({ page }) => {
  const backend = await startKataBackend({ issues: [issues[0]!] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path.startsWith("GET /api/v1/events/stream")).length)
      .toBe(1);
    for (const stream of backend.state.streams) {
      stream.end();
    }

    await expect
      .poll(() => backend.state.seenPaths.filter((path) => path.startsWith("GET /api/v1/events/stream")).length)
      .toBeGreaterThanOrEqual(2);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    backend.state.issues = [
      {
        ...issues[1]!,
        metadata: { ...issues[1]!.metadata, scheduled_on: today },
      },
    ];
    emitKataReset(backend.state, 6);

    await expect(page.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata detail formats task events from the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend({
    events: [
      eventRow({
        event_id: 7,
        event_uid: "event-links-changed",
        type: "issue.links_changed",
        project_id: issues[0]!.project_id,
        project_uid: issues[0]!.project_uid,
        project_name: issues[0]!.project_name,
        issue: issues[0]!,
        payload: {
          blocks_added: [{ uid: "issue-q3", short_id: "kat-7" }],
          related_removed: ["old-1", "old-2"],
        },
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(detail).toContainText("+blocks");
    await expect(detail).toContainText("-related (2)");
    await expect(detail).not.toContainText("issue.links_changed");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/events?limit=100");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata task list sorts by priority when requested", async ({ page }) => {
  const high = {
    ...issueSummary({
      id: 201,
      uid: "issue-high-priority",
      project_id: 1,
      project_uid: "project-finance",
      project_name: "Finances",
      short_id: "high",
      qualified_id: "Finances#high",
      title: "Zulu high priority",
      body: "High priority body.",
      priority: 0,
      labels: ["home"],
      metadata: { scheduled_on: today },
    }),
    updated_at: "2026-05-14T08:00:00Z",
  };
  const low = {
    ...issueSummary({
      id: 202,
      uid: "issue-low-priority",
      project_id: 1,
      project_uid: "project-finance",
      project_name: "Finances",
      short_id: "low",
      qualified_id: "Finances#low",
      title: "Alpha low priority",
      body: "Low priority body.",
      priority: 2,
      labels: ["home"],
      metadata: { scheduled_on: today },
    }),
    updated_at: "2026-05-16T08:00:00Z",
  };
  const backend = await startKataBackend({ issues: [low, high] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const rows = page.locator(".issue-list .issue-row");
    await expect(rows.first()).toContainText("Alpha low priority");
    await expect(rows.nth(1)).toContainText("Zulu high priority");

    await page.getByRole("button", { name: /Sort by Priority/ }).click();

    await expect(rows.first()).toContainText("Zulu high priority");
    await expect(rows.nth(1)).toContainText("Alpha low priority");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata task list defaults to newest first and sort controls switch priority", async ({ page }) => {
  const urgentOld = {
    ...issueSummary({
      id: 211,
      uid: "issue-urgent-old",
      project_id: 1,
      project_uid: "project-finance",
      project_name: "Finances",
      short_id: "urgent-old",
      qualified_id: "Finances#urgent-old",
      title: "Urgent older task",
      body: "Urgent task body.",
      priority: 0,
      labels: ["home"],
      metadata: { scheduled_on: today },
    }),
    updated_at: "2026-05-14T08:00:00Z",
  };
  const routineNew = {
    ...issueSummary({
      id: 212,
      uid: "issue-routine-new",
      project_id: 2,
      project_uid: "project-kata",
      project_name: "Kata",
      short_id: "routine-new",
      qualified_id: "Kata#routine-new",
      title: "Routine newer task",
      body: "Routine task body.",
      priority: 3,
      labels: ["work"],
      metadata: { scheduled_on: today },
    }),
    updated_at: "2026-05-16T08:00:00Z",
  };
  const backend = await startKataBackend({ issues: [routineNew, urgentOld] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const rows = page.locator(".issue-list .issue-row");
    await expect(rows.first()).toContainText("Routine newer task");
    await expect(rows.nth(1)).toContainText("Urgent older task");

    await page.getByRole("button", { name: /Sort by Priority/ }).click();
    await expect(rows.first()).toContainText("Urgent older task");
    await expect(rows.nth(1)).toContainText("Routine newer task");

    await page.getByRole("button", { name: /Sort by Updated/ }).click();
    await expect(rows.first()).toContainText("Routine newer task");
    await expect(rows.nth(1)).toContainText("Urgent older task");

    await page.getByRole("button", { name: /Sort by Updated/ }).click();
    await expect(rows.first()).toContainText("Urgent older task");
    await expect(rows.nth(1)).toContainText("Routine newer task");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata task list sorting preserves visible groups", async ({ page }) => {
  const overdueHigh = {
    ...issueSummary({
      id: 221,
      uid: "issue-overdue-high",
      project_id: 1,
      project_uid: "project-finance",
      project_name: "Finances",
      short_id: "overdue-high",
      qualified_id: "Finances#overdue-high",
      title: "Overdue high priority",
      body: "Overdue task body.",
      priority: 0,
      labels: ["home"],
      metadata: { deadline_on: previousLocalDateString() },
    }),
    updated_at: "2026-05-14T08:00:00Z",
  };
  const todayLow = {
    ...issueSummary({
      id: 222,
      uid: "issue-today-low",
      project_id: 2,
      project_uid: "project-kata",
      project_name: "Kata",
      short_id: "today-low",
      qualified_id: "Kata#today-low",
      title: "Today low priority",
      body: "Today task body.",
      priority: 3,
      labels: ["work"],
      metadata: { scheduled_on: today },
    }),
    updated_at: "2026-05-16T08:00:00Z",
  };
  const backend = await startKataBackend({ issues: [todayLow, overdueHigh] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?view=today`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const issueList = page.locator(".issue-list");
    const overdueGroup = issueList.getByRole("region", { name: "Overdue" });
    const todayGroup = issueList.getByRole("region", { name: "Today" });

    await page.getByRole("button", { name: /Sort by Priority/ }).click();

    await expect(overdueGroup).toContainText("Overdue high priority");
    await expect(todayGroup).toContainText("Today low priority");

    await expect(overdueGroup).toContainText("Overdue high priority");
    await expect(todayGroup).toContainText("Today low priority");
    await expect(overdueGroup.locator(".issue-row").first()).toContainText("Overdue high priority");
    await expect(todayGroup.locator(".issue-row").first()).toContainText("Today low priority");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata task list keyboard navigation moves focus and selection", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await page.getByRole("button", { name: "All Open" }).click();
    const rows = page.locator(".issue-list .issue-row");
    await expect(rows.first()).toContainText("Email Susan re: Q3");
    await expect(rows.nth(1)).toContainText("Pay rent");

    await rows.first().focus();
    await page.keyboard.press("j");

    await expect(rows.nth(1)).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues/issue-rent");

    await page.keyboard.press("k");

    await expect(rows.first()).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );

    await page.keyboard.press("End");

    await expect(rows.nth(1)).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");

    await page.keyboard.press("Home");

    await expect(rows.first()).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata single configured daemon hides the daemon switcher", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    await expect(page.getByTestId("daemon-chip")).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata sidebar hides task inbox projects", async ({ page }) => {
  const inboxProject = {
    id: 3,
    uid: "project-capture-inbox",
    name: "Capture Inbox",
    metadata: { area: "Unfiled", role: "inbox", sidebar_order: 1 },
    open_count: 1,
  };
  const backend = await startKataBackend({
    projects: [...projects, inboxProject],
    issues: [
      ...issues,
      issueSummary({
        id: 33,
        uid: "issue-inbox",
        project_id: inboxProject.id,
        project_uid: inboxProject.uid,
        project_name: inboxProject.name,
        short_id: "cap-1",
        qualified_id: "Capture Inbox#cap-1",
        title: "Triage capture",
        body: "Captured before filing.",
        labels: [],
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Capture Inbox\s+1$/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata sidebar switches system views and renders project areas", async ({ page }) => {
  let releaseProjectIssues!: () => void;
  const projectIssuesBarrier = new Promise<void>((resolve) => {
    releaseProjectIssues = resolve;
  });
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await expect(page.getByRole("button", { name: "Inbox" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Today" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Logbook" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Personal" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Work" })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Kata\s+1$/ })).toBeVisible();

    await page.getByRole("button", { name: "Inbox" }).click();
    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toBeVisible();

    backend.state.issuesBarrier = projectIssuesBarrier;
    const issueRequestsBeforeProjectClick = backend.state.seenPaths.filter(
      (seenPath) => seenPath === "GET /api/v1/issues?status=open",
    ).length;
    await page.getByRole("button", { name: /^Finances\s+1$/ }).click();
    await expect
      .poll(() => backend.state.seenPaths.filter((seenPath) => seenPath === "GET /api/v1/issues?status=open").length)
      .toBeGreaterThan(issueRequestsBeforeProjectClick);
    await page.getByRole("button", { name: "Inbox" }).click();
    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toBeVisible();
    releaseProjectIssues();
    await page.waitForTimeout(1100);
    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toBeVisible();
    await expect(page).toHaveURL(/view=inbox/);
    await expect(page).not.toHaveURL(/scope=project-finance/);

    await page.getByRole("button", { name: "Today" }).click();
    await expect(page.getByRole("heading", { name: "Today", level: 2 })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
  } finally {
    releaseProjectIssues();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project create submits inline input and switches scope", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await page.getByRole("button", { name: "New project" }).click();
    const input = page.getByRole("textbox", { name: "New project name" });
    await expect(input).toBeVisible();
    await input.fill("Sabbatical");
    await input.press("Enter");

    await expect(page.getByRole("button", { name: /^Sabbatical\s+0$/ })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Sabbatical", level: 2 })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project create input cancels on Escape", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await page.getByRole("button", { name: "New project" }).click();
    const input = page.getByRole("textbox", { name: "New project name" });
    await expect(input).toBeVisible();
    await input.fill("Will Cancel");
    await input.press("Escape");

    await expect(page.getByRole("textbox", { name: "New project name" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Will Cancel\s+0$/ })).toHaveCount(0);
    expect(backend.state.seenPaths).not.toContain("POST /api/v1/projects");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project rename submits inline input", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const listTitle = page.locator(".kata-list h2");
    const titleBeforeRename = await listTitle.innerText();

    await page.getByRole("button", { name: "Rename Finances" }).click();
    const input = page.getByRole("textbox", { name: "Rename project" });
    await expect(input).toBeVisible();
    await expect(listTitle).toHaveText(titleBeforeRename);
    await input.fill("Wellness");
    await input.press("Enter");

    await expect(page.getByRole("button", { name: /^Wellness\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("PATCH /api/v1/projects/1");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project rows can be renamed by double-clicking", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await page.getByRole("button", { name: /^Finances\s+1$/ }).dblclick();
    const input = page.getByRole("textbox", { name: "Rename project" });
    await expect(input).toBeVisible();
    await input.fill("Wellness");
    await input.press("Enter");

    await expect(page.getByRole("button", { name: /^Wellness\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("PATCH /api/v1/projects/1");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project row double-click enters rename", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await page.getByRole("button", { name: /^Finances\s+1$/ }).dblclick({ delay: 300 });

    await expect(page.getByRole("textbox", { name: "Rename project" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project rename input cancels on Escape", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    await page.getByRole("button", { name: "Rename Finances" }).click();
    const input = page.getByRole("textbox", { name: "Rename project" });
    await expect(input).toBeVisible();
    await input.fill("Different");
    await input.press("Escape");

    await expect(page.getByRole("textbox", { name: "Rename project" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Different\s+1$/ })).toHaveCount(0);
    expect(backend.state.seenPaths).not.toContain("PATCH /api/v1/projects/1");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata parent row expands children loaded from detail", async ({ page }) => {
  const parent = issueSummary({
    id: 101,
    uid: "issue-parent",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "parent",
    qualified_id: "Finances#parent",
    title: "Parent task",
    body: "Parent task body.",
    labels: ["home"],
    child_counts: { open: 1, total: 1 },
    metadata: { scheduled_on: today },
  });
  const child = issueSummary({
    id: 102,
    uid: "issue-child",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "child",
    qualified_id: "Finances#child",
    title: "Child task",
    body: "Child task body.",
    labels: ["home"],
    parent_short_id: "parent",
    metadata: { scheduled_on: today },
  });
  const backend = await startKataBackend({ issues: [parent, child] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();
    const parentRow = page.getByRole("button", { name: /Parent task/ });
    await expect(parentRow).toBeVisible();
    await expect(page.getByRole("button", { name: /Child task/ })).toHaveCount(0);
    await expect(page.getByText("1 task")).toBeVisible();

    await parentRow.press("ArrowRight");

    await expect(parentRow).toHaveAttribute("aria-expanded", "true");
    const childRow = page.getByRole("button", { name: /Child task/ });
    await expect(childRow).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues/issue-parent");

    await parentRow.focus();
    await page.keyboard.press("j");

    await expect(childRow).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Child task body.");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues/issue-child");

    await page.keyboard.press("k");

    await expect(parentRow).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Parent task body.");

    await parentRow.press("ArrowLeft");

    await expect(parentRow).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByRole("button", { name: /Child task/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata task list selects visible parent when child sorts first", async ({ page }) => {
  const parent = issueSummary({
    id: 101,
    uid: "issue-parent",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "parent",
    qualified_id: "Finances#parent",
    title: "Parent task",
    body: "Parent task body.",
    labels: ["home"],
    child_counts: { open: 1, total: 1 },
    metadata: { scheduled_on: today },
  });
  const child = issueSummary({
    id: 102,
    uid: "issue-child",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "child",
    qualified_id: "Finances#child",
    title: "Child task",
    body: "Child task body.",
    labels: ["home"],
    parent_short_id: "parent",
    metadata: { scheduled_on: today },
  });
  const backend = await startKataBackend({ issues: [child, parent] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("status", { name: "Connection: online" })).toBeVisible();

    const parentRow = page.getByRole("button", { name: /Parent task/ });
    await expect(parentRow).toBeVisible();
    await parentRow.click();
    await expect(parentRow).toHaveAttribute("aria-current", "true");
    await expect(page.getByRole("button", { name: /Child task/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Parent task body.");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues/issue-parent");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace switches between configured external daemons", async ({ page }) => {
  const homeProject = {
    id: 101,
    uid: "project-home",
    name: "Home",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const workProject = {
    id: 202,
    uid: "project-work",
    name: "Work",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 1,
  };
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [
      issueSummary({
        id: 1011,
        uid: "issue-home-yard",
        project_id: homeProject.id,
        project_uid: homeProject.uid,
        project_name: homeProject.name,
        short_id: "home-1",
        qualified_id: "Home#home-1",
        title: "Rake the yard",
        body: "Visible only from the home daemon.",
        labels: ["home"],
        metadata: { scheduled_on: today },
      }),
    ],
  });
  const work = await startKataBackend({
    projects: [workProject],
    issues: [
      issueSummary({
        id: 2021,
        uid: "issue-work-release",
        project_id: workProject.id,
        project_uid: workProject.uid,
        project_name: workProject.name,
        short_id: "work-1",
        qualified_id: "Work#work-1",
        title: "Ship the release",
        body: "Visible only from the work daemon.",
        labels: ["work"],
        metadata: { scheduled_on: today },
      }),
    ],
  });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-shared-main`);

    const taskList = page.locator(".kata-list");
    await expect(page.getByTestId("daemon-chip")).toContainText("home");
    await expect(taskList.getByRole("button", { name: /Rake the yard/ })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Ship the release/ })).toHaveCount(0);

    await page.getByRole("button", { name: "All Open" }).click();
    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();

    await expect(page.getByTestId("daemon-chip")).toContainText("work");
    await expect(taskList.getByRole("button", { name: /Ship the release/ })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Rake the yard/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("kata daemon switch restarts the target stream after stale route churn", async ({ page }) => {
  let releaseEvents = () => {};
  const stalledEvents = new Promise<void>((resolve) => {
    releaseEvents = resolve;
  });
  const homeProject = {
    id: 101,
    uid: "project-home",
    name: "Home",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const workProject = {
    id: 202,
    uid: "project-work",
    name: "Work",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 1,
  };
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [
      issueSummary({
        id: 1011,
        uid: "issue-home-yard",
        project_id: homeProject.id,
        project_uid: homeProject.uid,
        project_name: homeProject.name,
        short_id: "home-1",
        qualified_id: "Home#home-1",
        title: "Rake the yard",
        body: "Visible only from the home daemon.",
        labels: ["home"],
        metadata: { scheduled_on: today },
      }),
    ],
  });
  const work = await startKataBackend({
    projects: [workProject],
    issues: [
      issueSummary({
        id: 2021,
        uid: "issue-work-release",
        project_id: workProject.id,
        project_uid: workProject.uid,
        project_name: workProject.name,
        short_id: "work-1",
        qualified_id: "Work#work-1",
        title: "Ship the release",
        body: "Visible only from the work daemon.",
        labels: ["work"],
        metadata: { scheduled_on: today },
      }),
    ],
    eventsBarrier: stalledEvents,
  });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByTestId("daemon-chip")).toContainText("home");
    await expect(page.getByRole("button", { name: /Rake the yard/ })).toBeVisible();
    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();
    await expect.poll(() => work.state.seenPaths).toContain("GET /api/v1/events?limit=100");

    await page.getByRole("button", { name: "All Open" }).click();
    releaseEvents();

    await expect(page.getByTestId("daemon-chip")).toContainText("work");
    await expect(page.getByRole("button", { name: /Ship the release/ })).toBeVisible();
    await expect
      .poll(() => work.state.seenPaths.filter((path) => path.startsWith("GET /api/v1/events/stream")).length)
      .toBeGreaterThanOrEqual(1);
  } finally {
    releaseEvents();
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("kata daemon switch rehydrates linked task titles for matching peer ids", async ({ page }) => {
  const homeProject = {
    id: 101,
    uid: "project-home",
    name: "Home",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 2,
  };
  const workProject = {
    id: 202,
    uid: "project-work",
    name: "Work",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 2,
  };
  const homeMain = issueSummary({
    id: 1011,
    uid: "issue-shared-main",
    project_id: homeProject.id,
    project_uid: homeProject.uid,
    project_name: homeProject.name,
    short_id: "main",
    qualified_id: "Home#main",
    title: "Shared linked task",
    body: "Home daemon selected task.",
    labels: ["home"],
    metadata: { scheduled_on: today },
  });
  const homeLinked = issueSummary({
    id: 1012,
    uid: "issue-shared-linked",
    project_id: homeProject.id,
    project_uid: homeProject.uid,
    project_name: homeProject.name,
    short_id: "linked",
    qualified_id: "Home#linked",
    title: "Home linked title",
    body: "Home linked task.",
    labels: ["home"],
  });
  const workMain = issueSummary({
    ...homeMain,
    project_id: workProject.id,
    project_uid: workProject.uid,
    project_name: workProject.name,
    qualified_id: "Work#main",
    body: "Work daemon selected task.",
    labels: ["work"],
  });
  const workLinked = issueSummary({
    ...homeLinked,
    project_id: workProject.id,
    project_uid: workProject.uid,
    project_name: workProject.name,
    qualified_id: "Work#linked",
    title: "Work linked title",
    body: "Work linked task.",
    labels: ["work"],
  });
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [homeMain, homeLinked],
    links: [linkRow({ id: 1, project_id: homeProject.id, from: homeMain, to: homeLinked, type: "related" })],
  });
  const work = await startKataBackend({
    projects: [workProject],
    issues: [workMain, workLinked],
    links: [linkRow({ id: 1, project_id: workProject.id, from: workMain, to: workLinked, type: "related" })],
  });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const links = page.getByRole("region", { name: "Links" });
    await expect(page.getByTestId("daemon-chip")).toContainText("home");
    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Shared linked task/ })
      .click();
    await expect(page.getByRole("heading", { name: "Shared linked task" })).toBeVisible();
    await expect(links).toContainText("Home linked title");

    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();

    await expect(page.getByTestId("daemon-chip")).toContainText("work");
    await expect(links).toContainText("Work linked title");
    await expect(links).not.toContainText("Home linked title");
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("kata daemon switch clears stale task route when the target daemon has no tasks", async ({ page }) => {
  const homeProject = {
    id: 101,
    uid: "project-home",
    name: "Home",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const emptyProject = {
    id: 303,
    uid: "project-empty",
    name: "Empty",
    metadata: { area: "Other", sidebar_order: 1 },
    open_count: 0,
  };
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [
      issueSummary({
        id: 1011,
        uid: "issue-home-yard",
        project_id: homeProject.id,
        project_uid: homeProject.uid,
        project_name: homeProject.name,
        short_id: "home-1",
        qualified_id: "Home#home-1",
        title: "Rake the yard",
        body: "Visible only from the home daemon.",
        labels: ["home"],
        metadata: { scheduled_on: today },
      }),
    ],
  });
  const empty = await startKataBackend({
    projects: [emptyProject],
    issues: [],
  });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "empty", url: empty.url },
    ],
    "home",
  );
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-home-yard`);

    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Rake the yard");
    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-empty").click();

    await expect(page.getByTestId("daemon-chip")).toContainText("empty");
    await expect(page).not.toHaveURL(/issue=/);
    await expect(page.locator(".kata-list")).toContainText("No tasks");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).not.toContainText("Rake the yard");
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await empty.close();
  }
});

test("kata search filters tasks through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const taskList = page.locator(".kata-list");
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();

    await page.getByLabel("Search tasks").fill("q3");
    await page.getByRole("combobox", { name: "Status: Open" }).click();
    await page.getByRole("option", { name: "All" }).click();
    await page.getByRole("textbox", { name: "Owner" }).fill("Susan");
    await page.getByRole("textbox", { name: "Label" }).fill("work");
    await page.getByRole("button", { name: /Project scope: All projects/ }).click();
    const projectInput = page.getByRole("combobox", { name: "Project scope" });
    await projectInput.fill("kat");
    await projectInput.press("Enter");

    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page).toHaveURL(/scope=project-kata/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/2/search?q=q3");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues?status=closed&limit=500");

    await page.getByRole("button", { name: /Project scope: Kata/ }).click();
    await page.getByRole("option", { name: "All projects" }).click();
    await expect(page).not.toHaveURL(/scope=/);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata search clears loading after the newest overlapping daemon search finishes", async ({ page }) => {
  let releaseOldSearch!: () => void;
  const oldSearchBarrier = new Promise<void>((resolve) => {
    releaseOldSearch = resolve;
  });
  const backend = await startKataBackend({
    searchBarriers: new Map([["2:old", oldSearchBarrier]]),
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const taskList = page.locator(".kata-list");
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await page.getByRole("button", { name: /Project scope: All projects/ }).click();
    const projectInput = page.getByRole("combobox", { name: "Project scope" });
    await projectInput.fill("kat");
    await projectInput.press("Enter");
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();

    await page.getByLabel("Search tasks").fill("old");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/2/search?q=old");
    await expect(page.getByText("Loading snapshot")).toBeAttached();

    await page.getByLabel("Search tasks").fill("q3");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/2/search?q=q3");
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page.getByText("Loading snapshot")).toHaveCount(0);

    releaseOldSearch();
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues/issue-q3");
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    await expect(page.getByText("Loading snapshot")).toHaveCount(0);
  } finally {
    releaseOldSearch();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata route selects the requested task and app header reset clears the URL detail", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);

    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
    await expect(page).toHaveURL(/issue=issue-q3/);

    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Pay rent/ })
      .click();

    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");
    await expect(page).toHaveURL(/issue=issue-rent/);

    await page.locator(".header-center").getByRole("button", { name: "Kata" }).click();

    await expect(page).toHaveURL(/\/kata$/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Select a task");
    await expect(page.getByRole("region", { name: "Task detail" })).not.toContainText("Send June rent from checking.");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata URL state restores view, selection history, and project scope", async ({ page }) => {
  const deadlineIssue = issueSummary({
    id: 44,
    uid: "issue-kata-deadline",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-deadline",
    qualified_id: "Kata#kat-deadline",
    title: "Kata deadline task",
    body: "Deadline scoped task.",
    labels: ["work"],
    metadata: { deadline_on: today },
  });
  const backend = await startKataBackend({ projects: [...projects, inboxProject], issues: [...issues, deadlineIssue] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?view=inbox`);
    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toBeVisible();

    await page.goto(`${server.info.base_url}/kata?view=deadlines&scope=project-kata&issue=issue-kata-deadline`);
    await expect(page.getByRole("button", { name: /Kata deadline task/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Email Susan re: Q3/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Deadline scoped task.");

    await page.locator(".header-center").getByRole("button", { name: "Kata" }).click();
    await expect(page).toHaveURL(/\/kata$/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Select a task");

    await page.goto(`${server.info.base_url}/kata?view=deadlines&scope=project-kata&issue=issue-kata-deadline`);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Deadline scoped task.");
    await page.getByRole("button", { name: /Project scope: Kata/ }).click();
    await page.getByRole("option", { name: "All projects" }).click();
    await expect(page).toHaveURL(/view=deadlines/);
    await expect(page).not.toHaveURL(/scope=/);
    await expect(page.getByRole("heading", { name: "Deadlines", level: 2 })).toBeVisible();

    await page.goto(`${server.info.base_url}/kata?view=deadlines&scope=project-kata&issue=issue-kata-deadline`);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Deadline scoped task.");

    await page.getByRole("button", { name: /^Finances\s+1$/ }).click();
    await expect(page).toHaveURL(/scope=project-finance/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");

    await page.goBack();
    await expect(page).toHaveURL(/view=deadlines/);
    await expect(page).toHaveURL(/scope=project-kata/);
    await expect(page).toHaveURL(/issue=issue-kata-deadline/);
    await expect(page.getByRole("button", { name: /Kata deadline task/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Email Susan re: Q3/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Deadline scoped task.");

    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail).toContainText("Confirm the Q3 project review agenda.");

    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Pay rent/ })
      .click();
    await expect(detail).toContainText("Send June rent from checking.");
    await expect(page).toHaveURL(/issue=issue-rent/);

    await page.goBack();
    await expect(detail).toContainText("Confirm the Q3 project review agenda.");
    await expect(page).toHaveURL(/issue=issue-q3/);

    await page.getByRole("button", { name: /^Kata\s+1$/ }).click();
    await expect(page).toHaveURL(/scope=project-kata/);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("docs task links resolve through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const docsRoot = await createDocsFixture();
  await writeFile(
    path.join(docsRoot, "kata-link.md"),
    ["# Linked Task", "", "Open #kat-7 from this note.", ""].join("\n"),
  );
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "notes",
        name: "Notes",
        path: docsRoot,
      },
    });
    expect(res.status()).toBe(201);

    await page.goto(`${server.info.base_url}/docs?folder=notes&doc=kata-link.md`);
    await expect(page.getByRole("heading", { name: "Linked Task" })).toBeVisible();

    await page.getByRole("link", { name: "#kat-7" }).click();

    await expect(page).toHaveURL(/\/kata\?issue=issue-q3/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("command palette opens task and docs search results", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const docsRoot = await createDocsFixture();
  await writeFile(
    path.join(docsRoot, "q3-notes.md"),
    ["# Q3 Notes", "", "Confirm the Q3 project review agenda before sending.", ""].join("\n"),
  );
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "notes",
        name: "Notes",
        path: docsRoot,
      },
    });
    expect(res.status()).toBe(201);

    await page.goto(server.info.base_url);
    await page.keyboard.press(process.platform === "darwin" ? "Meta+K" : "Control+K");
    const dialog = page.getByRole("dialog", { name: "Command palette" });
    await expect(dialog).toBeVisible();
    await dialog.locator(".palette-input").fill("q3");

    const taskGroup = dialog.locator(".palette-group", { hasText: "Kata tasks" });
    const docsGroup = dialog.locator(".palette-group", { hasText: "Docs" });
    const taskRow = taskGroup.locator(".palette-row", { hasText: "Email Susan re: Q3" });
    const docRow = docsGroup.locator(".palette-row", { hasText: "q3-notes.md" });
    await expect(taskRow).toBeVisible();
    await expect(docRow).toBeVisible();

    await taskRow.click();
    await expect(page).toHaveURL(/\/kata\?issue=issue-q3/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );

    await page.keyboard.press(process.platform === "darwin" ? "Meta+K" : "Control+K");
    await expect(dialog).toBeVisible();
    await dialog.locator(".palette-input").fill("q3");
    await expect(docRow).toBeVisible();
    await docRow.click();

    await expect(page).toHaveURL(/\/docs\?folder=notes&doc=q3-notes\.md/);
    await expect(page.getByRole("heading", { name: "Q3 Notes", level: 1 })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("docs task links use the folder-bound external daemon", async ({ page }) => {
  const homeProject = {
    id: 101,
    uid: "project-home",
    name: "Home",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const workProject = {
    id: 202,
    uid: "project-work",
    name: "Work",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 1,
  };
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [
      issueSummary({
        id: 1011,
        uid: "issue-home",
        project_id: homeProject.id,
        project_uid: homeProject.uid,
        project_name: homeProject.name,
        short_id: "shared-1",
        qualified_id: "Home#shared-1",
        title: "Default daemon task",
        body: "This task should not open from the bound docs folder.",
        labels: ["home"],
      }),
    ],
  });
  const work = await startKataBackend({
    projects: [workProject],
    issues: [
      issueSummary({
        id: 2021,
        uid: "issue-work",
        project_id: workProject.id,
        project_uid: workProject.uid,
        project_name: workProject.name,
        short_id: "shared-1",
        qualified_id: "Work#shared-1",
        title: "Bound daemon task",
        body: "Opened through the folder daemon binding.",
        labels: ["work"],
      }),
    ],
  });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const docsRoot = await createDocsFixture();
  await writeFile(path.join(docsRoot, "bound-link.md"), ["# Bound Link", "", "Open #shared-1 here.", ""].join("\n"));
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "work-notes",
        name: "Work Notes",
        path: docsRoot,
        daemon: "work",
      },
    });
    expect(res.status()).toBe(201);
    await expect(res.json()).resolves.toMatchObject({ folder: { daemon: "work" } });

    await page.goto(`${server.info.base_url}/docs?folder=work-notes&doc=bound-link.md`);
    await expect(page.getByRole("heading", { name: "Bound Link" })).toBeVisible();

    await page.getByRole("link", { name: "#shared-1" }).click();

    await expect(page).toHaveURL(/\/kata\?issue=issue-work/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Opened through the folder daemon binding.",
    );
    await expect.poll(() => work.state.seenPaths).toContain("GET /api/v1/issues?status=open");
    await expect.poll(() => work.state.seenPaths).toContain("GET /api/v1/issues/issue-work");
    expect(home.state.seenPaths).not.toContain("GET /api/v1/issues/issue-home");
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("message linking follows the daemon activated by a folder-bound docs link", async ({ page }) => {
  const workProject = {
    id: 202,
    uid: "project-work",
    name: "Work",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 1,
  };
  const work = await startKataBackend({
    projects: [workProject],
    issues: [
      issueSummary({
        id: 2021,
        uid: "issue-work",
        project_id: workProject.id,
        project_uid: workProject.uid,
        project_name: workProject.name,
        short_id: "shared-1",
        qualified_id: "Work#shared-1",
        title: "Bound daemon task",
        body: "Opened through the folder daemon binding.",
        labels: ["work"],
      }),
    ],
  });
  const msgvault = await startMsgvaultBackend();
  msgvault.state.authorized = true;
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: "http://127.0.0.1:9" },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const docsRoot = await createDocsFixture();
  await writeFile(path.join(docsRoot, "bound-link.md"), ["# Bound Link", "", "Open #shared-1 here.", ""].join("\n"));
  const envName = `MSGVAULT_E2E_KEY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), "middleman-messages-bound-daemon-e2e-"));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  const server = await startIsolatedE2EServer();

  try {
    const configureMessages = await page.request.post(`${server.info.base_url}/api/v1/msgvault/configure`, {
      headers: middlemanCSRFHeader,
      data: {
        url: msgvault.url,
        api_key_env: envName,
      },
    });
    expect(configureMessages.status()).toBe(200);
    const addFolder = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "work-notes",
        name: "Work Notes",
        path: docsRoot,
        daemon: "work",
      },
    });
    expect(addFolder.status()).toBe(201);

    await page.goto(`${server.info.base_url}/docs?folder=work-notes&doc=bound-link.md`);
    await page.getByRole("link", { name: "#shared-1" }).click();
    await expect(page).toHaveURL(/\/kata\?issue=issue-work/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Opened through the folder daemon binding.",
    );

    await page.getByRole("button", { name: "Messages" }).click();
    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible();
    await searchBox.fill("project");
    await page.getByRole("search", { name: "Search messages" }).getByRole("button", { name: "Search" }).click();
    await page.getByRole("button", { name: /Project sync/ }).click();
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Link to task" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await msgvault.close();
    await work.close();
    if (previousEnv === undefined) {
      delete process.env[envName];
    } else {
      process.env[envName] = previousEnv;
    }
    if (previousSavedSearchesPath === undefined) {
      delete process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
    } else {
      process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = previousSavedSearchesPath;
    }
  }
});

test("docs issue autocomplete searches the folder-bound external daemon", async ({ page }) => {
  const homeProject = {
    id: 101,
    uid: "project-home",
    name: "Home",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const workProject = {
    id: 202,
    uid: "project-work",
    name: "Work",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 1,
  };
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [
      issueSummary({
        id: 1011,
        uid: "issue-home",
        project_id: homeProject.id,
        project_uid: homeProject.uid,
        project_name: homeProject.name,
        short_id: "shared-1",
        qualified_id: "Home#shared-1",
        title: "Default daemon completion",
        body: "This task belongs to the default daemon.",
        labels: ["home"],
      }),
    ],
  });
  const work = await startKataBackend({
    projects: [workProject],
    issues: [
      issueSummary({
        id: 2021,
        uid: "issue-work",
        project_id: workProject.id,
        project_uid: workProject.uid,
        project_name: workProject.name,
        short_id: "shared-1",
        qualified_id: "Work#shared-1",
        title: "Bound daemon completion",
        body: "This task belongs to the bound daemon.",
        labels: ["work"],
      }),
    ],
  });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const docsRoot = await createDocsFixture();
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "work-notes",
        name: "Work Notes",
        path: docsRoot,
        daemon: "work",
      },
    });
    expect(res.status()).toBe(201);

    const editor = await openDocsEditor(page, server.info.base_url, "/docs?folder=work-notes&doc=README.md");
    await clearEditor(page, editor);

    await page.keyboard.type("see #shared");

    const tooltip = autocompleteTooltip(page);
    await expect(tooltip).toBeVisible();
    await expect(tooltip).toContainText("Bound daemon completion");
    await expect(tooltip).not.toContainText("Default daemon completion");
    await expect.poll(() => work.state.seenPaths).toContain("GET /api/v1/issues?status=open");
    expect(home.state.seenPaths).not.toContain("GET /api/v1/issues?status=open");
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("docs issue autocomplete scopes qualified suggestions and preserves no-match text", async ({ page }) => {
  const householdProject = {
    id: 301,
    uid: "project-household",
    name: "household",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const personalProject = {
    id: 302,
    uid: "project-personal",
    name: "personal",
    metadata: { area: "Personal", sidebar_order: 2 },
    open_count: 1,
  };
  const backend = await startKataBackend({
    projects: [householdProject, personalProject],
    issues: [
      issueSummary({
        id: 3011,
        uid: "issue-rent",
        project_id: householdProject.id,
        project_uid: householdProject.uid,
        project_name: householdProject.name,
        short_id: "rent",
        qualified_id: "household#rent",
        title: "Pay rent",
        body: "Send rent from checking.",
        labels: ["home"],
      }),
      issueSummary({
        id: 3021,
        uid: "issue-yoga",
        project_id: personalProject.id,
        project_uid: personalProject.uid,
        project_name: personalProject.name,
        short_id: "yoga",
        qualified_id: "personal#yoga",
        title: "Morning yoga",
        body: "Stretch before work.",
        labels: ["health"],
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const docsRoot = await createDocsFixture();
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "notes",
        name: "Notes",
        path: docsRoot,
      },
    });
    expect(res.status()).toBe(201);

    const editor = await openDocsEditor(page, server.info.base_url, "/docs?folder=notes&doc=README.md");
    await clearEditor(page, editor);

    await page.keyboard.type("see household/#r");
    const tooltip = autocompleteTooltip(page);
    await expect(tooltip).toBeVisible();
    await expect(tooltip).toContainText("household/#rent");
    await expect(tooltip).not.toContainText("personal/#yoga");

    await tooltip.getByRole("option", { name: /household\/#rent/ }).click();
    await expect(editor).toContainText("see household/#rent");

    await clearEditor(page, editor);
    const issueRequestsBeforeNoMatch = backend.state.seenPaths.filter((seenPath) =>
      seenPath.startsWith("GET /api/v1/issues?"),
    ).length;
    await page.keyboard.type("nothing #zzzzzz");
    await expect
      .poll(() => backend.state.seenPaths.filter((seenPath) => seenPath.startsWith("GET /api/v1/issues?")).length)
      .toBeGreaterThan(issueRequestsBeforeNoMatch);
    await expect(editor).toContainText("nothing #zzzzzz");
    await expect(autocompleteTooltip(page).getByRole("option", { name: /zzzzzz/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata linked message pills route to Messages setup when Messages is not configured", async ({ page }) => {
  const backend = await startKataBackend({
    issues: [
      issueSummary({
        id: 22,
        uid: "issue-q3",
        project_id: 2,
        project_uid: "project-kata",
        project_name: "Kata",
        short_id: "kat-7",
        qualified_id: "Kata#kat-7",
        title: "Email Susan re: Q3",
        body: "Confirm the Q3 project review agenda.",
        owner: "Susan",
        labels: ["work"],
        metadata: {
          mail_links: [
            {
              message_id: 101,
              conversation_id: 501,
              subject: "Project sync",
              from: "alice@example.com",
              sent_at: "2026-05-15T10:00:00Z",
              added_at: "2026-05-15T10:00:00Z",
            },
          ],
        },
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);

    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Email Susan re: Q3");
    const links = page.getByRole("region", { name: "Linked messages" });
    await expect(links).toBeVisible();
    const pill = links.locator(".pill-open");
    await expect(pill).toBeVisible();
    await expect(pill).toBeEnabled();
    await pill.click();
    await expect(page).toHaveURL(/\/messages\?message=101$/);
    await expect(page.getByRole("button", { name: "Set up Messages" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata linked message pills become active after same-session Messages setup", async ({ page }) => {
  const backend = await startKataBackend({
    issues: [
      issueSummary({
        id: 22,
        uid: "issue-q3",
        project_id: 2,
        project_uid: "project-kata",
        project_name: "Kata",
        short_id: "kat-7",
        qualified_id: "Kata#kat-7",
        title: "Email Susan re: Q3",
        body: "Confirm the Q3 project review agenda.",
        owner: "Susan",
        labels: ["work"],
        metadata: {
          mail_links: [
            {
              message_id: 101,
              conversation_id: 501,
              subject: "Project sync",
              from: "alice@example.com",
              sent_at: "2026-05-15T10:00:00Z",
              added_at: "2026-05-15T10:00:00Z",
            },
          ],
        },
      }),
    ],
  });
  const msgvault = await startMsgvaultBackend();
  msgvault.state.authorized = true;
  const kataHome = await configureKataHome(backend.url);
  const envName = `MSGVAULT_E2E_KEY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), "middleman-messages-kata-setup-e2e-"));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);

    const links = page.getByRole("region", { name: "Linked messages" });
    const pill = links.locator(".pill-open");
    await expect(pill).toBeVisible();
    await expect(pill).toBeEnabled();

    await appHeaderTab(page, "Messages").click();
    await expect(page).toHaveURL(/\/messages$/);
    await page.getByRole("button", { name: "Set up Messages" }).click();
    await page.getByLabel("Message source URL").fill(msgvault.url);
    await page.getByLabel("API key env var name").fill(envName);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByPlaceholder("Search messages...")).toBeVisible();

    await appHeaderTab(page, "Kata").click();
    await expect(page).toHaveURL(/\/kata\?issue=issue-q3$/);
    await expect(pill).toBeEnabled();
    await pill.click();
    await expect(page).toHaveURL(/\/messages\?message=101$/);
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await msgvault.close();
    await backend.close();
    if (previousEnv === undefined) {
      delete process.env[envName];
    } else {
      process.env[envName] = previousEnv;
    }
    if (previousSavedSearchesPath === undefined) {
      delete process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
    } else {
      process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = previousSavedSearchesPath;
    }
  }
});

test("kata detail comments and labels mutate through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend({
    issues: [
      ...issues,
      issueSummary({
        id: 303,
        uid: "issue-duplicate-fin",
        project_id: 2,
        project_uid: "project-kata",
        project_name: "Kata",
        short_id: "FIN-1",
        qualified_id: "Kata#FIN-1",
        title: "Duplicate finance reference",
        body: "This task forces qualified comment references.",
        labels: ["work"],
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail).toContainText("Verify amount against the lease.");
    await expect(detail.getByRole("button", { name: "Remove home" })).toBeVisible();

    const composer = detail.getByRole("textbox", { name: "Comment" });
    await composer.fill("see #");
    await expect(detail.getByRole("listbox", { name: "Insert task reference" })).toBeVisible();
    await composer.press("Enter");
    await expect(composer).toHaveValue("see #Finances#FIN-1 ");

    await composer.fill("see #r");
    await expect(detail.getByRole("listbox", { name: "Insert task reference" })).toBeVisible();
    await composer.press("Escape");
    await expect(detail.getByRole("listbox", { name: "Insert task reference" })).toHaveCount(0);
    await expect(composer).toHaveValue("see #r");

    await composer.fill("First reply with **markdown**");
    await detail.getByRole("button", { name: "Add comment" }).click();
    const firstComment = detail.locator(".comment").first();
    await expect(firstComment).toContainText("First reply with markdown");
    await expect(firstComment).not.toContainText("**markdown**");
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects/1/issues/issue-rent/comments");

    await detail.getByRole("button", { name: "Add label" }).click();
    await detail.getByLabel("New label").fill("urgent");
    await detail.getByLabel("New label").press("Enter");
    await expect(detail.getByRole("button", { name: "Remove urgent" })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects/1/issues/issue-rent/labels");

    await detail.getByRole("button", { name: "Remove home" }).click();
    await expect(detail.getByRole("button", { name: "Remove home" })).toHaveCount(0);
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("DELETE /api/v1/projects/1/issues/issue-rent/labels/home?actor=middleman");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata task links render, navigate, and add related links through the configured external daemon", async ({
  page,
}) => {
  const budget = issueSummary({
    id: 33,
    uid: "issue-budget",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "budget",
    qualified_id: "Finances#budget",
    title: "Quarterly budget review with a long title",
    body: "Parent budgeting task.",
    labels: ["home"],
  });
  const backend = await startKataBackend({
    issues: [...issues, budget],
    links: [
      linkRow({
        id: 1,
        project_id: 1,
        from: budget,
        to: issues[0]!,
        type: "parent",
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    const links = detail.getByRole("region", { name: "Links" });
    await expect(links.getByRole("heading", { name: "Links" })).toBeVisible();
    await expect(links).toContainText("Quarterly budget review with a long title");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/issues/issue-budget");

    await links.getByRole("button", { name: /parent\s+budget/ }).click();
    await expect(detail.getByRole("heading", { name: "Quarterly budget review with a long title" })).toBeVisible();
    await expect(page).toHaveURL(/issue=issue-budget/);

    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Pay rent/ })
      .click();
    await links.getByLabel("Related issue", { exact: true }).fill("kat-7");
    await links.getByRole("button", { name: "Link" }).click();

    await expect(links.getByRole("button", { name: /related\s+kat-7/ })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("PATCH /api/v1/projects/1/issues/issue-rent");
    expect(backend.state.links.some((link) => link.type === "related" && link.to.uid === "issue-q3")).toBe(true);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata detail properties mutate through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend({
    issues: [
      {
        ...issues[0]!,
        metadata: { ...issues[0]!.metadata, deadline_on: "2026-05-01" },
      },
      {
        ...issues[1]!,
        metadata: { ...issues[1]!.metadata, scheduled_on: today },
      },
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("button", { name: "Owner: Wes" })).toBeVisible();

    await detail.getByRole("button", { name: "Edit scheduled" }).click();
    await detail.getByRole("button", { name: /Scheduled:/ }).press("Escape");
    await expect(detail.getByRole("button", { name: "Edit scheduled" })).toContainText(/Scheduled/);

    await detail.getByRole("button", { name: "Edit scheduled" }).click();
    await detail.getByRole("button", { name: "Clear scheduled" }).click();
    await expect(detail.getByRole("button", { name: "Edit scheduled" })).toContainText("When");
    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.metadata.scheduled_on).toBeNull();

    await detail.getByRole("button", { name: "Edit due date" }).click();
    await expect(detail.getByRole("button", { name: /Due: May 1/ })).toBeVisible();
    await detail.getByRole("button", { name: "Clear due date" }).click();
    await expect(detail.getByRole("button", { name: "Edit due date" })).toContainText("No due date");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.metadata.deadline_on).toBeNull();

    await detail.getByRole("button", { name: "Owner: Wes" }).click();
    await detail.getByLabel("Owner", { exact: true }).fill("agent:planner");
    await detail.getByLabel("Owner", { exact: true }).press("Enter");
    await expect(detail.getByRole("button", { name: "Owner: agent:planner" })).toContainText("agent:planner");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/assign");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.owner).toBe("agent:planner");

    await detail.getByRole("button", { name: "Owner: agent:planner" }).click();
    await detail.getByLabel("Owner", { exact: true }).fill("sus");
    await detail.getByRole("option", { name: "Susan" }).click();
    await expect(detail.getByRole("button", { name: "Owner: Susan" })).toContainText("Susan");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.owner).toBe("Susan");

    await detail.getByRole("button", { name: "Owner: Susan" }).click();
    await detail.getByRole("option", { name: "Unassigned" }).click();
    await expect(detail.getByRole("button", { name: "Owner: Unassigned" })).toContainText("Unassigned");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/unassign");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.owner).toBeUndefined();

    await detail.getByRole("button", { name: "Edit priority" }).click();
    await detail.getByLabel("Priority", { exact: true }).selectOption("2");
    await expect(detail.getByRole("button", { name: "Edit priority" })).toContainText("P2");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/priority");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata owner assignment failure keeps the custom owner editor open", async ({ page }) => {
  const backend = await startKataBackend();
  backend.state.failNextAssignOwner = "owner unavailable";
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("button", { name: "Owner: Wes" })).toBeVisible();

    await detail.getByRole("button", { name: "Owner: Wes" }).click();
    const ownerInput = detail.getByLabel("Owner", { exact: true });
    await ownerInput.fill("agent:new");
    await ownerInput.press("Enter");

    await expect(page.getByRole("status", { name: "Connection: error" })).toContainText("owner unavailable");
    await expect(detail.getByLabel("Owner", { exact: true })).toHaveValue("agent:new");
    await expect(detail.getByRole("button", { name: "Owner: Wes" })).toHaveCount(0);
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.owner).toBe("Wes");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata detail property editors reset when switching tasks", async ({ page }) => {
  const backend = await startKataBackend({
    issues: [
      issues[0]!,
      {
        ...issues[1]!,
        metadata: { ...issues[1]!.metadata, scheduled_on: today },
      },
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await detail.getByRole("button", { name: "Edit scheduled" }).click();
    await expect(detail.getByRole("group", { name: "Edit scheduled" })).toBeVisible();

    await page.getByRole("button", { name: /Email Susan re: Q3/ }).click();

    await expect(detail.getByRole("heading", { name: "Email Susan re: Q3" })).toBeVisible();
    await expect(detail.getByRole("group", { name: "Edit scheduled" })).toHaveCount(0);
    await expect(detail.getByRole("button", { name: "Edit scheduled" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project crumb moves tasks through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(detail.getByRole("button", { name: "Move issue from Finances" })).toBeVisible();

    await detail.getByRole("button", { name: "Move issue from Finances" }).click();
    const input = detail.getByRole("combobox", { name: "Move issue project" });
    await expect(input).toBeFocused();
    await input.fill("kat");
    await input.press("Enter");

    await expect(detail.getByRole("button", { name: "Move issue from Kata" })).toBeVisible();
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/move");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.project_uid).toBe("project-kata");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata detail title and description edit through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();

    await detail.getByRole("button", { name: "Edit title" }).click();
    const titleInput = detail.getByRole("textbox", { name: "Edit title" });
    await expect(titleInput).toBeFocused();
    await titleInput.fill("scratch title");
    await titleInput.press("Escape");
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.title).toBe("Pay rent");

    await detail.getByRole("button", { name: "Edit title" }).click();
    await titleInput.fill("Pay rent (updated)");
    await titleInput.press("Enter");
    await expect(detail.getByRole("heading", { name: "Pay rent (updated)" })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("PATCH /api/v1/projects/1/issues/issue-rent");

    await page.getByRole("button", { name: "All Open" }).click();
    await page.getByRole("button", { name: /Email Susan re: Q3/ }).click();
    await expect(detail.getByRole("heading", { name: "Email Susan re: Q3" })).toBeVisible();
    await page.getByRole("button", { name: /Pay rent \(updated\)/ }).click();
    await expect(detail.getByRole("heading", { name: "Pay rent (updated)" })).toBeVisible();

    await detail.getByRole("button", { name: "Edit description" }).click();
    const bodyEditor = detail.getByRole("textbox", { name: "Edit description" });
    await expect(bodyEditor).toBeFocused();
    await expect(bodyEditor).toHaveValue(/Due to landlord/);
    await bodyEditor.fill("about-to-cancel");
    await detail.getByRole("button", { name: "Cancel" }).click();
    await expect(detail.locator(".body-display")).toContainText("Due to landlord");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.body).toContain("Due to landlord");

    await detail.getByRole("button", { name: "Edit description" }).click();
    await bodyEditor.fill("Updated body **markdown**");
    await detail.getByRole("button", { name: "Save" }).click();
    await expect(detail.locator(".body-display")).toContainText("Updated body");
    await expect(detail.locator(".body-display")).not.toContainText("**markdown**");

    await detail.getByRole("button", { name: "Edit description" }).click();
    await expect(detail.getByRole("textbox", { name: "Edit description" })).toHaveValue("Updated body **markdown**");
    await detail.getByRole("textbox", { name: "Edit description" }).fill("Saved via keyboard **body**");
    await detail.getByRole("textbox", { name: "Edit description" }).press("Control+Enter");
    await expect(detail.locator(".body-display")).toContainText("Saved via keyboard");
    await expect(detail.locator(".body-display")).not.toContainText("**body**");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata complete dialog closes and reopens through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
    const detail = page.getByRole("region", { name: "Task detail" });
    const financeCount = page.locator(".project-groups button", { hasText: "Finances" }).locator(".count");
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(financeCount).toHaveText("1");

    await detail.getByRole("button", { name: "Complete" }).click();
    const dialog = page.getByRole("dialog", { name: "Complete task" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("Pay rent")).toBeVisible();
    await expect(dialog.getByText("Finances#FIN-1")).toBeVisible();
    await expect(dialog.getByRole("radio", { name: /Done/ })).toBeChecked();
    await expect(dialog.getByRole("radio", { name: /Won't do/ })).toBeVisible();
    await expect(dialog.getByRole("radio", { name: /Duplicate/ })).toBeVisible();
    await expect(dialog.getByRole("radio", { name: /Superseded/ })).toBeVisible();

    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
    await expect(detail.getByRole("button", { name: "Complete" })).toBeVisible();

    await detail.getByRole("button", { name: "Complete" }).click();
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(detail.getByRole("button", { name: "Complete" })).toBeVisible();

    await detail.getByRole("button", { name: "Complete" }).click();
    await expect(dialog).toBeVisible();
    await dialog.getByPlaceholder(/What was done/).fill("Done via wire transfer");
    await dialog.getByRole("button", { name: "Complete" }).click();

    await expect(detail.getByRole("button", { name: "Reopen" })).toBeVisible();
    await expect(financeCount).toHaveText("0");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/close");
    const issue = backend.state.issues.find((item) => item.uid === "issue-rent");
    expect(issue?.status).toBe("closed");
    expect(issue?.metadata).toMatchObject({
      closed_reason: "done",
      closed_message: "Done via wire transfer",
    });

    await detail.getByRole("button", { name: "Reopen" }).click();
    await expect(detail.getByRole("button", { name: "Complete" })).toBeVisible();
    await expect(financeCount).toHaveText("1");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/reopen");
    expect(issue?.status).toBe("open");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata complete dialog submits alternate close reasons through the configured external daemon", async ({
  page,
}) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();

    await detail.getByRole("button", { name: "Complete" }).click();
    const dialog = page.getByRole("dialog", { name: "Complete task" });
    await dialog.getByRole("radio", { name: /Won't do/ }).click();
    await dialog.getByPlaceholder(/What was done/).fill("  landlord switched autopay  ");
    await dialog.getByRole("button", { name: "Complete" }).click();

    await expect(detail.getByRole("button", { name: "Reopen" })).toBeVisible();
    const issue = backend.state.issues.find((item) => item.uid === "issue-rent");
    expect(issue?.metadata).toMatchObject({
      closed_reason: "wontfix",
      closed_message: "  landlord switched autopay  ",
    });
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata overflow menu reveals checklist and deletes through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);
    const detail = page.getByRole("region", { name: "Task detail" });
    const kataCount = page.locator(".project-groups button", { hasText: "Kata" }).locator(".count");
    await expect(detail.getByRole("heading", { name: "Email Susan re: Q3" })).toBeVisible();
    await expect(detail.getByRole("region", { name: "Checklist" })).toHaveCount(0);
    await expect(kataCount).toHaveText("1");

    await detail.getByRole("button", { name: "More actions" }).click();
    const menu = detail.getByRole("menu", { name: "Task actions" });
    await expect(menu).toBeVisible();
    await expect(menu.getByRole("menuitem", { name: "Add checklist" })).toBeVisible();
    await expect(menu.getByRole("menuitem", { name: "Mark as recurring..." })).toBeVisible();
    await expect(menu.getByRole("menuitem", { name: "Delete issue" })).toBeVisible();

    await menu.getByRole("menuitem", { name: "Add checklist" }).click();
    await expect(detail.getByRole("region", { name: "Checklist" })).toBeVisible();

    await detail.getByRole("button", { name: "More actions" }).click();
    await detail.getByRole("menuitem", { name: "Delete issue" }).click();
    const dialog = page.getByRole("dialog", { name: "Delete issue" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("Email Susan re: Q3")).toBeVisible();
    await dialog.getByRole("button", { name: "Delete" }).click();

    await expect(detail.getByRole("button", { name: "Reopen" })).toBeVisible();
    await expect(kataCount).toHaveText("0");
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects/2/issues/issue-q3/actions/close");
    const issue = backend.state.issues.find((item) => item.uid === "issue-q3");
    expect(issue?.metadata).toMatchObject({
      closed_reason: "wontfix",
      closed_message: "Deleted from issue detail.",
    });
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata recurrence panel creates edits and deletes through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Email Susan re: Q3" })).toBeVisible();

    await detail.getByRole("button", { name: "More actions" }).click();
    await detail.getByRole("menuitem", { name: "Mark as recurring..." }).click();

    const createDialog = page.getByRole("dialog", { name: "New recurrence" });
    await expect(createDialog).toBeVisible();
    await createDialog.getByLabel("Title").fill("Weekly Q3 follow-up");
    await createDialog.getByRole("button", { name: "Save" }).click();

    const recurrence = detail.getByRole("region", { name: "Recurrence" });
    await expect(recurrence.getByRole("heading", { name: "Recurring" })).toBeVisible();
    await expect(recurrence.getByRole("button", { name: "Weekly Q3 follow-up" })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects/2/recurrences");

    await recurrence.getByRole("button", { name: "Weekly Q3 follow-up" }).click();
    const editDialog = page.getByRole("dialog", { name: "Edit recurrence" });
    await expect(editDialog).toBeVisible();
    await editDialog.getByLabel("Title").fill("Weekly project follow-up");
    await editDialog.getByRole("button", { name: "Save" }).click();

    await expect(recurrence.getByRole("button", { name: "Weekly project follow-up" })).toBeVisible();
    await expect.poll(() => backend.state.seenPaths).toContain("PATCH /api/v1/projects/2/recurrences/recurrence-1");

    await recurrence.getByRole("button", { name: "Delete recurrence" }).click();
    const deleteDialog = page.getByRole("dialog", { name: "Delete recurrence" });
    await expect(deleteDialog).toBeVisible();
    await expect(deleteDialog.getByText("Weekly project follow-up")).toBeVisible();
    await deleteDialog.getByRole("button", { name: "Delete" }).click();

    await expect(recurrence).toHaveCount(0);
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("DELETE /api/v1/projects/2/recurrences/recurrence-1?actor=middleman");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata checklist edits through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    const existing = detail.getByRole("checkbox", { name: "Send Zelle" });
    await expect(existing).toBeVisible();
    await expect(existing).not.toBeChecked();

    await existing.click();
    await expect(existing).toBeChecked();
    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");

    await detail.getByLabel("New checklist item").fill("Archive receipt");
    await detail.getByLabel("New checklist item").press("Enter");
    await expect(detail.getByRole("checkbox", { name: "Archive receipt" })).toBeVisible();

    await detail.getByRole("button", { name: "Remove Send Zelle" }).click();
    await expect(detail.getByRole("checkbox", { name: "Send Zelle" })).toHaveCount(0);
    await detail.getByRole("button", { name: "Remove Archive receipt" }).click();
    await expect(detail.getByRole("checkbox")).toHaveCount(0);
    await expect(detail.getByLabel("New checklist item")).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata navigation remains usable on narrow screens", async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 900 });
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const navigation = page.getByRole("complementary", { name: "Kata navigation" });
    await expect(navigation).toBeVisible();
    await navigation.getByRole("button", { name: "All Open" }).click();
    await expect(page.getByRole("heading", { name: "All Open", level: 2 })).toBeVisible();

    await navigation.getByRole("button", { name: /^Kata\s+1$/ }).click();
    await expect(page.getByRole("heading", { name: "Kata", level: 2 })).toBeVisible();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata metadata mutations preserve ETags through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const result = await page.evaluate(async () => {
      const response = await fetch("/api/v1/kata/proxy/api/v1/projects/1/issues/issue-rent/metadata", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"rev-1"',
        },
        body: JSON.stringify({
          actor: "middleman",
          patch: { deadline_on: "2026-06-01" },
        }),
      });
      return {
        etag: response.headers.get("etag"),
        ok: response.ok,
        body: await response.json(),
      };
    });

    expect(result).toMatchObject({
      ok: true,
      etag: '"rev-2"',
      body: {
        changed: true,
        issue: {
          uid: "issue-rent",
          metadata: { deadline_on: "2026-06-01" },
        },
      },
    });
    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");
    expect(backend.state.seenIfMatches).toContain('"rev-1"');
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});
