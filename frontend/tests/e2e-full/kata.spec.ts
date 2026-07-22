import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { once } from "node:events";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { expect, type Locator, type Page, test } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "./support/e2eServer";
import { createDocsFixture } from "./support/docsFixture";

// freshProcess everywhere in this file: kata tests point the server
// at per-test daemon catalogs via process.env.KATA_HOME, which only
// a process spawned after the env is set can inherit — pooled
// servers cannot.
async function startIsolatedE2EServer() {
  return startIsolatedE2EServerWithOptions({
    visibleImportedModes: true,
    freshProcess: true,
  });
}

async function startDefaultIsolatedE2EServer() {
  return startIsolatedE2EServerWithOptions({ freshProcess: true });
}

async function expectKataDaemonSwitcherReady(page: Page): Promise<void> {
  const chip = page.getByTestId("daemon-chip");
  await expect(chip).toBeVisible();
  await expect(chip.locator(".dot")).toHaveCount(0);

  const kataHeader = page.locator(".kata-header");
  await expect(kataHeader.getByText("Daemon", { exact: true })).toHaveCount(0);
  await expect(kataHeader.getByText(/Connecting|Connection|authentication required/i)).toHaveCount(0);
  await expect(page.getByRole("status").filter({ hasText: /Connecting|Connection/i })).toHaveCount(0);

  await chip.click();
  const menu = page.getByRole("menu", { name: "Configured Kata daemons" });
  await expect(menu).toBeVisible();
  await expect(menu.getByText("Switch daemon", { exact: true })).toHaveCount(0);
  await expect(menu.getByText("Configured Kata daemons", { exact: true })).toHaveCount(0);
  await expect(menu.locator(".daemon-row .row-meta").first()).toContainText(
    /connected|needs auth|unreachable|Connection|Live updates disconnected/,
  );
  await chip.click();
  await expect(menu).toBeHidden();
}

async function graphFilterMenu(graph: Locator): Promise<Locator> {
  const menu = graph.locator(".graph-filter-menu .kit-filter-dropdown__panel");
  if (!(await menu.isVisible().catch(() => false))) {
    await graph.getByRole("button", { name: /^Graph filters\b/ }).click();
    await expect(menu).toBeVisible();
  }
  return menu;
}

async function graphFilterItem(graph: Locator, id: string): Promise<Locator> {
  const menu = await graphFilterMenu(graph);
  const itemLabels: Record<string, { section: string; label: string }> = {
    "context-1": { section: "Context", label: "1 edge" },
    "context-2": { section: "Context", label: "2 edges" },
    "context-3": { section: "Context", label: "3 edges" },
    "context-all": { section: "Context", label: "All" },
    "depth-1": { section: "Depth", label: "1 edge" },
    "depth-2": { section: "Depth", label: "2 edges" },
    "depth-3": { section: "Depth", label: "3 edges" },
    "depth-full": { section: "Depth", label: "Full" },
    "direction-LR": { section: "Direction", label: "Left to right" },
    "direction-TB": { section: "Direction", label: "Top to bottom" },
    "direction-follow": { section: "Direction", label: "Follow split" },
    "layout-compact": { section: "Layout", label: "Compact" },
    "layout-elk": { section: "Layout", label: "ELK" },
    "visibility-hide-done": { section: "Visibility", label: "Hide done" },
  };
  const target = itemLabels[id] ?? { section: "", label: id };
  const index = await menu.evaluate((root, wanted) => {
    let currentSection = "";
    let itemIndex = -1;
    for (const element of Array.from(root.children)) {
      if (element.classList.contains("kit-filter-dropdown__section-title")) {
        currentSection = element.textContent?.trim() ?? "";
        continue;
      }
      if (!element.classList.contains("kit-filter-dropdown__item")) continue;
      itemIndex += 1;
      const label = element.querySelector(".kit-filter-dropdown__label")?.textContent?.trim() ?? "";
      if (currentSection === wanted.section && label === wanted.label) return itemIndex;
    }
    return -1;
  }, target);
  expect(index).toBeGreaterThanOrEqual(0);
  const item = menu.locator(".kit-filter-dropdown__item").nth(index);
  await expect(item).toBeVisible();
  return item;
}

async function selectGraphFilterItem(graph: Locator, id: string): Promise<void> {
  const item = await graphFilterItem(graph, id);
  await item.click();
  await expect(item).toHaveClass(/(^|\s)active(\s|$)/);
}

type ElementBox = { x: number; y: number; width: number; height: number };

function expectFloatingPanelAnchored(trigger: ElementBox, panel: ElementBox): void {
  expect(Math.abs(panel.x + panel.width - (trigger.x + trigger.width))).toBeLessThanOrEqual(2);
  const belowGap = Math.abs(panel.y - (trigger.y + trigger.height + 4));
  const aboveGap = Math.abs(trigger.y - (panel.y + panel.height + 4));
  expect(Math.min(belowGap, aboveGap)).toBeLessThanOrEqual(2);
}

async function expectTaskColumnsAligned(page: Page, columns: readonly string[]): Promise<void> {
  for (const column of columns) {
    await expect(page.locator(`.table-header .col-${column}`)).toBeVisible();
    await expect(page.locator(`.issue-row .cell-${column}`).first()).toHaveCount(1);
  }

  const header = page.locator(".table-header");
  const row = page.locator(".issue-row").first();
  const headerGrid = await header.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      columns: style.gridTemplateColumns,
      gap: style.columnGap,
      paddingLeft: style.paddingLeft,
      paddingRight: style.paddingRight,
    };
  });
  const rowGrid = await row.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      columns: style.gridTemplateColumns,
      gap: style.columnGap,
      paddingLeft: style.paddingLeft,
      paddingRight: style.paddingRight,
    };
  });
  expect(rowGrid).toEqual(headerGrid);

  const headerBox = await header.boundingBox();
  const rowBox = await row.boundingBox();
  expect(headerBox).not.toBeNull();
  expect(rowBox).not.toBeNull();
  expect(Math.abs(headerBox!.x - rowBox!.x)).toBeLessThanOrEqual(1);
  expect(Math.abs(headerBox!.width - rowBox!.width)).toBeLessThanOrEqual(1);
}

type BackendState = {
  commentsByUID: Map<string, CommentRow[]>;
  events: EventRow[];
  issues: IssueSummary[];
  links: LinkRow[];
  nextCommentID: number;
  nextEventID: number;
  nextRecurrenceID: number;
  nextMutationResponseIssue?: IssueSummary | undefined;
  nextProjectResponse?: ProjectRow | undefined;
  publishMutationEvents: boolean;
  recurrences: RecurrenceRow[];
  projects: ProjectRow[];
  readyIssueUIDs: Set<string>;
  seenIfMatches: string[];
  seenPaths: string[];
  streams: Set<ServerResponse>;
  failNextAssignOwner?: string | undefined;
  failNextProjectsStatus?: number | undefined;
  failNextReadyStatus?: number | undefined;
  failNextMetadataMessage?: string | undefined;
  failNextMoveMessage?: string | undefined;
  issuesBarrier?: Promise<void> | undefined;
  projectCreateBarrier?: Promise<void> | undefined;
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

const now = "2026-05-15T10:00:00Z";
const today = localDateString();
const middlemanCSRFHeader = { "X-Middleman-Csrf": "1" };
const kataProjectEventPageSize = 100;

function daemonAPIPath(...segments: string[]): string {
  return ["", "api", "v1", ...segments].join("/");
}

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
  readyIssueUIDs?: string[] | undefined;
  links?: LinkRow[] | undefined;
  recurrences?: RecurrenceRow[] | undefined;
  issuesBarrier?: Promise<void> | undefined;
  publishMutationEvents?: boolean | undefined;
  failNextProjectsStatus?: number | undefined;
  failNextReadyStatus?: number | undefined;
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
  parent?: { uid: string; short_id: string; project: string; qualified_id: string; status: string } | undefined;
  parent_short_id?: string | undefined;
  child_counts?: { open: number; total: number } | undefined;
  metadata?: Record<string, unknown> | undefined;
  blocks?: Array<{ uid: string; short_id: string; project: string; qualified_id: string; status: string }> | undefined;
  blocked_by?:
    | Array<{ uid: string; short_id: string; project: string; qualified_id: string; status: string }>
    | undefined;
  related?: Array<{ uid: string; short_id: string; project: string; qualified_id: string; status: string }> | undefined;
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

type IssueLinkPeerSource = Pick<IssueSummary, "uid" | "short_id" | "project_name" | "qualified_id" | "status">;

function issueLinkPeer(issue: IssueLinkPeerSource) {
  return {
    uid: issue.uid,
    short_id: issue.short_id,
    project: issue.project_name,
    qualified_id: issue.qualified_id,
    status: issue.status,
  };
}

function workspaceStateFixture() {
  const parent = issueSummary({
    id: 101,
    uid: "issue-child-workflow-parent",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-child-parent",
    qualified_id: "Kata#kat-child-parent",
    title: "Parent child workflow",
    body: "Parent task for the child workflow.",
    owner: "Susan",
    labels: ["work"],
    child_counts: { open: 1, total: 1 },
  });
  const child = issueSummary({
    id: 102,
    uid: "issue-child-workflow",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-child",
    qualified_id: "Kata#kat-child",
    title: "Child child workflow",
    body: "Child task in the child workflow.",
    owner: "Susan",
    labels: ["work"],
    parent: issueLinkPeer(parent),
    parent_short_id: parent.short_id,
  });
  return {
    child,
    issues: [...issues, parent, child],
    parent,
    projects: [
      inboxProject,
      ...projects.map((project) =>
        project.uid === "project-kata" ? { ...project, open_count: project.open_count + 2 } : project,
      ),
    ],
  };
}

async function seedRestoredKataWorkspace(
  page: Page,
  baseURL: string,
  options: { projectScoped?: boolean } = {},
): Promise<void> {
  await page.goto(`${baseURL}/kata`);
  if (options.projectScoped ?? true) {
    await page.getByRole("button", { name: /Project scope: All projects/ }).click();
    const projectInput = page.getByRole("combobox", { name: "Project scope" });
    await projectInput.fill("kata");
    await projectInput.press("Enter");
  }
  await page.getByRole("combobox", { name: "Status: Open" }).click();
  await page.getByRole("option", { name: "All" }).click();
  await page.getByRole("textbox", { name: "Owner" }).fill("Susan");
  await page.getByRole("textbox", { name: "Label" }).fill("work");
  await page.getByLabel("Search tasks").fill("child");
  await page.getByTestId("daemon-chip").click();
  const daemonRow = page.getByRole("menu", { name: "Configured Kata daemons" }).locator(".daemon-row").first();
  await expect(daemonRow).toBeEnabled();
  await page.getByTestId("daemon-chip").click();

  const parentRow = page.getByRole("button", { name: /Parent child workflow/ });
  await expect(parentRow).toBeVisible();
  await parentRow.press("ArrowRight");
  await expect(parentRow).toHaveAttribute("aria-expanded", "true");
  const childRow = page.getByRole("button", { name: /Child child workflow/ });
  await expect(childRow).toBeVisible();
  await childRow.click();
  await expect(page.getByRole("heading", { name: "Child child workflow" })).toBeVisible();
}

function commentRow(input: {
  id: number;
  uid?: string;
  issue_id: number;
  author: string;
  body: string;
  created_at?: string;
}) {
  return {
    id: input.id,
    uid: input.uid ?? `comment-${input.id}`,
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
    content_hash: `event-content-${input.event_id}`,
    hlc_counter: input.event_id,
    hlc_physical_ms: Date.parse(input.created_at ?? now),
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
  from: IssueLinkPeerSource;
  to: IssueLinkPeerSource;
  type: "parent" | "blocks" | "related";
  author?: string | undefined;
}) {
  return {
    id: input.id,
    project_id: input.project_id,
    from: issueLinkPeer(input.from),
    to: issueLinkPeer(input.to),
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
  return page.locator(".kit-top-bar__tabs").getByRole("button", { name, exact: true });
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
    events: [...(options.events ?? [])],
    issues: rows,
    links: [...(options.links ?? [])],
    nextCommentID: 2,
    nextEventID: Math.max(0, ...(options.events ?? []).map((event) => event.event_id)) + 1,
    nextRecurrenceID: 1,
    publishMutationEvents: options.publishMutationEvents ?? true,
    recurrences: [...(options.recurrences ?? [])],
    projects: options.projects ?? projects,
    readyIssueUIDs: new Set(
      options.readyIssueUIDs ?? rows.filter((issue) => issue.status === "open").map((issue) => issue.uid),
    ),
    seenIfMatches: [],
    seenPaths: [],
    streams: new Set(),
    failNextProjectsStatus: options.failNextProjectsStatus,
    failNextReadyStatus: options.failNextReadyStatus,
    issuesBarrier: options.issuesBarrier,
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

  const recurrencesRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/recurrences$`).exec(url.pathname);
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

  const recurrenceRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/recurrences/([^/]+)$`).exec(url.pathname);
  if (recurrenceRoute) {
    await handleRecurrenceDetail(state, req, res, {
      projectID: Number(recurrenceRoute[1]),
      uid: decodeURIComponent(recurrenceRoute[2] ?? ""),
    });
    return;
  }

  const projectRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)$`).exec(url.pathname);
  if (projectRoute) {
    await handleProjectRename(state, req, res, Number(projectRoute[1]));
    return;
  }

  const projectEventsRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/events$`).exec(url.pathname);
  if (projectEventsRoute) {
    const projectID = Number(projectEventsRoute[1]);
    const afterID = Number(url.searchParams.get("after_id") ?? "0");
    const requestedLimit = Number(url.searchParams.get("limit") ?? "0");
    const limit = requestedLimit > 0 ? Math.min(requestedLimit, kataProjectEventPageSize) : kataProjectEventPageSize;
    const events = state.events.filter((event) => event.project_id === projectID && event.event_id > afterID);
    const page = events.slice(0, limit);
    writeJSON(res, 200, {
      reset_required: false,
      events: page,
      next_after_id: page.at(-1)?.event_id ?? afterID,
    });
    return;
  }

  const projectReadyRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/ready$`).exec(url.pathname);
  if (projectReadyRoute) {
    if (state.failNextReadyStatus !== undefined) {
      const status = state.failNextReadyStatus;
      state.failNextReadyStatus = undefined;
      writeJSON(res, status, { error: { code: "internal", message: "ready tasks unavailable" } });
      return;
    }
    const projectID = Number(projectReadyRoute[1]);
    writeJSON(res, 200, {
      issues: readyIssues(state, projectID),
      fetched_at: now,
    });
    return;
  }

  const issueListRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/issues$`).exec(url.pathname);
  if (issueListRoute) {
    if (req.method === "GET") {
      await handleProjectIssueList(state, res, Number(issueListRoute[1]), url);
      return;
    }
    await handleIssueCreate(state, req, res, Number(issueListRoute[1]));
    return;
  }

  const reachableGraphRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/issues/([^/]+)/graph$`).exec(
    url.pathname,
  );
  if (reachableGraphRoute) {
    writeReachableGraph(state, res, {
      projectID: Number(reachableGraphRoute[1]),
      ref: decodeURIComponent(reachableGraphRoute[2] ?? ""),
      url,
    });
    return;
  }

  const issueEditRoute = new RegExp(`^${daemonAPIPath("projects")}/(\\d+)/issues/([^/]+)$`).exec(url.pathname);
  if (issueEditRoute) {
    await handleIssueEdit(state, req, res, {
      projectID: Number(issueEditRoute[1]),
      ref: decodeURIComponent(issueEditRoute[2] ?? ""),
    });
    return;
  }

  if (url.pathname === daemonAPIPath("events", "stream")) {
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

  const issueRoute = new RegExp(
    `^${daemonAPIPath("projects")}/(\\d+)/issues/([^/]+)/(comments|labels|metadata|actions)(?:/([^/]+))?$`,
  ).exec(url.pathname);
  if (issueRoute) {
    await handleIssueMutation(state, req, res, url, {
      projectID: Number(issueRoute[1]),
      ref: decodeURIComponent(issueRoute[2] ?? ""),
      kind: issueRoute[3] ?? "",
      label: issueRoute[4] ? decodeURIComponent(issueRoute[4]) : undefined,
    });
    return;
  }

  const issueDetailRoute = new RegExp(`^${daemonAPIPath("issues")}/([^/]+)$`).exec(url.pathname);
  if (issueDetailRoute) {
    const uid = decodeURIComponent(issueDetailRoute[1] ?? "");
    writeIssueDetail(state, res, uid);
    return;
  }

  switch (url.pathname) {
    case daemonAPIPath("instance"):
      writeJSON(res, 200, {
        instance_uid: "kata-e2e",
        version: "0.0.0-e2e",
        schema_version: 1,
      });
      return;
    case daemonAPIPath("projects"):
      if (req.method === "POST") {
        await handleProjectCreate(state, req, res);
        return;
      }
      if (state.failNextProjectsStatus !== undefined) {
        const status = state.failNextProjectsStatus;
        state.failNextProjectsStatus = undefined;
        writeJSON(res, status, { error: { code: "internal", message: "projects unavailable" } });
        return;
      }
      writeJSON(res, 200, {
        projects: state.projects.map((project) => {
          const projectIssues = state.issues.filter((issue) => issue.project_id === project.id);
          return {
            ...project,
            revision: 1,
            created_at: now,
            stats: {
              open: projectIssues.filter((issue) => issue.status === "open").length,
              closed: projectIssues.filter((issue) => issue.status === "closed").length,
            },
          };
        }),
        fetched_at: now,
      });
      return;
    case daemonAPIPath("issues"):
      {
        const barrier = state.issuesBarrier;
        state.issuesBarrier = undefined;
        await barrier;
      }
      const projectID = url.searchParams.get("project_id");
      writeJSON(res, 200, {
        issues: issuesForStatus(
          projectID === null ? state.issues : state.issues.filter((issue) => issue.project_id === Number(projectID)),
          url.searchParams.get("status"),
        ),
        fetched_at: now,
      });
      return;
    case daemonAPIPath("ready"):
      if (state.failNextReadyStatus !== undefined) {
        const status = state.failNextReadyStatus;
        state.failNextReadyStatus = undefined;
        writeJSON(res, status, { error: { code: "internal", message: "ready tasks unavailable" } });
        return;
      }
      writeJSON(res, 200, {
        issues: readyIssues(state),
        fetched_at: now,
      });
      return;
    case daemonAPIPath("events"):
      {
        const afterID = Number(url.searchParams.get("after_id") ?? "0");
        const events = state.events.filter((event) => event.event_id > afterID);
        const limit = Number(url.searchParams.get("limit") ?? "0");
        const page = limit > 0 ? events.slice(0, limit) : events;
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

function emitDaemonChange(state: BackendState, event: EventRow): void {
  const frame = [`id: ${event.event_id}`, `event: ${event.type}`, `data: ${JSON.stringify(event)}`, "", ""].join("\n");
  for (const stream of state.streams) {
    stream.write(frame);
  }
}

function publishMutationChange(state: BackendState, issue: IssueSummary, type: string): void {
  if (!state.publishMutationEvents) return;
  const event = eventRow({
    event_id: state.nextEventID++,
    event_uid: `event-${type.replaceAll(".", "-")}-${issue.uid}-${state.nextEventID}`,
    type,
    project_id: issue.project_id,
    project_uid: issue.project_uid,
    project_name: issue.project_name,
    issue,
  });
  state.events.push(event);
  queueMicrotask(() => emitDaemonChange(state, event));
}

function publishProjectMutationChange(state: BackendState, project: ProjectRow, type: string): void {
  if (!state.publishMutationEvents) return;
  const event = eventRow({
    event_id: state.nextEventID++,
    event_uid: `event-${type.replaceAll(".", "-")}-${project.uid}-${state.nextEventID}`,
    type,
    project_id: project.id,
    project_uid: project.uid,
    project_name: project.name,
  });
  state.events.push(event);
  queueMicrotask(() => emitDaemonChange(state, event));
}

function issuesForStatus(rows: IssueSummary[], status: string | null): IssueSummary[] {
  if (status === "closed") return rows.filter((issue) => issue.status === "closed");
  if (status === "open") return rows.filter((issue) => issue.status === "open");
  return rows;
}

function readyIssues(state: BackendState, projectID?: number): IssueSummary[] {
  return state.issues.filter(
    (issue) =>
      issue.status === "open" &&
      state.readyIssueUIDs.has(issue.uid) &&
      (projectID === undefined || issue.project_id === projectID),
  );
}

type GraphEdgeRow = { from_uid: string; to_uid: string; kind: "parent" | "blocks" | "related"; layout: boolean };

function canonicalGraphEdges(state: BackendState): GraphEdgeRow[] {
  const byUID = new Map(state.issues.map((issue) => [issue.uid, issue]));
  const byProjectShort = new Map(state.issues.map((issue) => [`${issue.project_uid}:${issue.short_id}`, issue]));
  const edges = new Map<string, GraphEdgeRow>();
  const add = (fromUID: string | undefined, toUID: string | undefined, kind: GraphEdgeRow["kind"]) => {
    if (!fromUID || !toUID) return;
    const key = `${kind}:${fromUID}:${toUID}`;
    if (!edges.has(key)) edges.set(key, { from_uid: fromUID, to_uid: toUID, kind, layout: true });
  };

  for (const issue of state.issues) {
    if (issue.parent_short_id) {
      add(byProjectShort.get(`${issue.project_uid}:${issue.parent_short_id}`)?.uid, issue.uid, "parent");
    }
    for (const peer of issue.blocks ?? []) add(issue.uid, peer.uid, "blocks");
    for (const peer of issue.blocked_by ?? []) add(peer.uid, issue.uid, "blocks");
    for (const peer of issue.related ?? []) add(issue.uid, peer.uid, "related");
  }
  for (const link of state.links) {
    add(link.from.uid, link.to.uid, link.type);
  }

  for (const edge of [...edges.values()]) {
    if (edge.kind !== "blocks") continue;
    const directKey = `${edge.kind}:${edge.from_uid}:${edge.to_uid}`;
    const queue = [...edges.values()].filter(
      (candidate) =>
        candidate.kind === "blocks" &&
        candidate.from_uid === edge.from_uid &&
        candidate.to_uid !== edge.to_uid &&
        `${candidate.kind}:${candidate.from_uid}:${candidate.to_uid}` !== directKey,
    );
    const seen = new Set<string>([edge.from_uid]);
    while (queue.length > 0) {
      const next = queue.shift()!;
      if (next.to_uid === edge.to_uid) {
        edge.layout = false;
        break;
      }
      if (seen.has(next.to_uid)) continue;
      seen.add(next.to_uid);
      queue.push(
        ...[...edges.values()].filter(
          (candidate) =>
            candidate.kind === "blocks" &&
            candidate.from_uid === next.to_uid &&
            `${candidate.kind}:${candidate.from_uid}:${candidate.to_uid}` !== directKey,
        ),
      );
    }
  }

  return [...edges.values()].filter((edge) => byUID.has(edge.from_uid) && byUID.has(edge.to_uid));
}

function writeReachableGraph(
  state: BackendState,
  res: ServerResponse,
  input: { projectID: number; ref: string; url: URL },
): void {
  const source = state.issues.find(
    (issue) => issue.project_id === input.projectID && (issue.uid === input.ref || issue.short_id === input.ref),
  );
  if (!source) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }

  const depthRaw = input.url.searchParams.get("depth") ?? "full";
  const maxDepth = depthRaw === "full" ? Number.POSITIVE_INFINITY : Number(depthRaw);
  const hideDone = input.url.searchParams.get("hide_done") === "true";
  const edges = canonicalGraphEdges(state);
  const distances = new Map<string, number>([[source.uid, 0]]);
  const queue = [source.uid];
  while (queue.length > 0) {
    const uid = queue.shift()!;
    const distance = distances.get(uid) ?? 0;
    if (distance >= maxDepth) continue;
    for (const edge of edges) {
      const nextUID = edge.from_uid === uid ? edge.to_uid : edge.to_uid === uid ? edge.from_uid : null;
      if (!nextUID || distances.has(nextUID)) continue;
      distances.set(nextUID, distance + 1);
      queue.push(nextUID);
    }
  }
  const nodeUIDs = new Set(
    state.issues
      .filter((issue) => distances.has(issue.uid))
      .filter((issue) => issue.uid === source.uid || !hideDone || issue.status !== "closed")
      .map((issue) => issue.uid),
  );
  writeJSON(res, 200, {
    source_uid: source.uid,
    depth: depthRaw,
    hide_done: hideDone,
    nodes: state.issues.filter((issue) => nodeUIDs.has(issue.uid)).sort((a, b) => a.uid.localeCompare(b.uid)),
    edges: edges
      .filter((edge) => nodeUIDs.has(edge.from_uid) && nodeUIDs.has(edge.to_uid))
      .sort((a, b) => `${a.kind}:${a.from_uid}:${a.to_uid}`.localeCompare(`${b.kind}:${b.from_uid}:${b.to_uid}`)),
    unresolved_refs: [],
  });
}

async function handleProjectCreate(state: BackendState, req: IncomingMessage, res: ServerResponse): Promise<void> {
  await state.projectCreateBarrier;
  state.projectCreateBarrier = undefined;
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
  const responseProject = state.nextProjectResponse ?? project;
  state.nextProjectResponse = undefined;
  writeJSON(res, 200, { project: responseProject });
  publishProjectMutationChange(state, project, "project.created");
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
  publishProjectMutationChange(state, project, "project.updated");
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
  const project = state.projects.find((candidate) => candidate.id === projectID);
  if (project) publishProjectMutationChange(state, project, "recurrence.created");
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
    const project = state.projects.find((candidate) => candidate.id === route.projectID);
    if (project) publishProjectMutationChange(state, project, "recurrence.updated");
    return;
  }

  if (req.method === "DELETE") {
    found.deleted_at = now;
    found.revision += 1;
    found.updated_at = now;
    res.statusCode = 204;
    res.end();
    const project = state.projects.find((candidate) => candidate.id === route.projectID);
    if (project) publishProjectMutationChange(state, project, "recurrence.deleted");
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
  publishMutationChange(state, found, "issue.updated");
}

async function handleProjectIssueList(
  state: BackendState,
  res: ServerResponse,
  projectID: number,
  url: URL,
): Promise<void> {
  const project = state.projects.find((candidate) => candidate.id === projectID);
  if (!project) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }
  writeJSON(res, 200, {
    issues: issuesForStatus(
      state.issues.filter((issue) => issue.project_id === projectID),
      url.searchParams.get("status"),
    ),
    fetched_at: now,
  });
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
  publishMutationChange(state, issue, "issue.created");
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
    publishMutationChange(state, found, "issue.comment_added");
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
    publishMutationChange(state, found, "issue.label_added");
    return;
  }

  if (req.method === "DELETE" && route.kind === "labels" && route.label) {
    void url.searchParams.get("actor");
    found.labels = found.labels.filter((label) => label !== route.label);
    writeJSON(res, 200, { changed: true, issue: found });
    publishMutationChange(state, found, "issue.label_removed");
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
    writeJSON(res, 200, { changed: true, issue: mutationResponseIssue(state, found) });
    publishMutationChange(state, found, "issue.metadata_updated");
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
    publishMutationChange(state, found, "issue.assigned");
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "unassign") {
    delete found.owner;
    writeJSON(res, 200, { changed: true, issue: found });
    publishMutationChange(state, found, "issue.unassigned");
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
    publishMutationChange(state, found, "issue.priority_updated");
    return;
  }

  if (req.method === "POST" && route.kind === "actions" && route.label === "move") {
    const payload = await readJSONBody(req);
    if (state.failNextMoveMessage !== undefined) {
      const message = state.failNextMoveMessage;
      state.failNextMoveMessage = undefined;
      writeJSON(res, 503, { error: { code: "move_unavailable", message } });
      return;
    }
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
    publishMutationChange(state, found, "issue.moved");
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
    writeJSON(res, 200, { changed: true, issue: mutationResponseIssue(state, found) });
    publishMutationChange(state, found, "issue.closed");
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
    publishMutationChange(state, found, "issue.reopened");
    return;
  }

  writeJSON(res, 405, { error: "method_not_allowed" });
}

function adjustProjectOpenCount(state: BackendState, projectID: number, delta: number): void {
  state.projects = state.projects.map((project) =>
    project.id === projectID ? { ...project, open_count: Math.max(0, project.open_count + delta) } : project,
  );
}

function mutationResponseIssue(state: BackendState, issue: IssueSummary): IssueSummary {
  const responseIssue = state.nextMutationResponseIssue;
  state.nextMutationResponseIssue = undefined;
  return responseIssue ?? issue;
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
  const parent = found.parent ? state.issues.find((issue) => issue.uid === found.parent?.uid) : undefined;
  res.setHeader("ETag", `"rev-${found.revision}"`);
  writeJSON(res, 200, {
    issue: found,
    parent: parent
      ? {
          uid: parent.uid,
          short_id: parent.short_id,
          qualified_id: parent.qualified_id,
          status: parent.status,
          title: parent.title,
        }
      : undefined,
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
    server.closeIdleConnections();
  });
}

test("kata workspace reads tasks through Middleman snapshots", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  const snapshotIntents: Array<Record<string, string>> = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname !== "/api/v1/kata/tasks/snapshot") return;
    snapshotIntents.push(Object.fromEntries(url.searchParams));
  });

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();
    await expectKataDaemonSwitcherReady(page);
    const payRentRow = page.getByRole("button", {
      name: /(?=.*Pay rent)(?=.*Finances#FIN-1)(?=.*project: Finances)(?=.*owner: Wes)(?=.*priority: 0)(?=.*home)/,
    });
    await expect(payRentRow).toBeVisible();
    await payRentRow.click();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");

    await page.getByRole("button", { name: /^Kata\s+\d+$/ }).click();

    await expect(page.getByRole("heading", { name: "Kata", level: 2 })).toBeVisible();
    const q3Row = page.getByRole("button", { name: /Email Susan re: Q3/ });
    await expect(q3Row).toBeVisible();
    await q3Row.click();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
    await expect
      .poll(() => snapshotIntents.some((intent) => intent.scope === "global" && intent.authority === "open"))
      .toBe(true);
    await expect
      .poll(() =>
        snapshotIntents.some((intent) => intent.project_uid === "project-kata" && intent.authority === "open"),
      )
      .toBe(true);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata reachable graph renders and selects tasks through the configured external daemon", async ({ page }) => {
  const followUp = issueSummary({
    id: 33,
    uid: "issue-follow-up",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-8",
    qualified_id: "Kata#kat-8",
    title: "Review Q3 notes",
    body: "Read the agenda notes after the Q3 email is sent.",
    labels: ["work"],
  });
  const deepFollowUp = issueSummary({
    id: 34,
    uid: "issue-deep-follow-up",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-9",
    qualified_id: "Kata#kat-9",
    title: "Review implementation notes",
    body: "This task should remain rendered while context only changes emphasis.",
    labels: ["work"],
  });
  const graphRoot = {
    ...issues[0]!,
    blocks: [issueLinkPeer(issues[1]!), issueLinkPeer(followUp)],
  };
  const graphQ3 = {
    ...issues[1]!,
    blocks: [issueLinkPeer(followUp), issueLinkPeer(deepFollowUp)],
  };
  const backend = await startKataBackend({ issues: [graphRoot, graphQ3, followUp, deepFollowUp] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await page.evaluate(() => window.__middleman_kata_graph_debug?.reset());
    await detail.getByRole("button", { name: "Open reachable graph" }).click();

    const graph = page.getByRole("region", { name: "Reachable task graph" });
    await expect(graph).toBeVisible();
    await expect
      .poll(() => page.evaluate(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.nodeCount ?? 0))
      .toBeGreaterThan(1);
    const debugBeforeSelection = await page.evaluate(() => window.__middleman_kata_graph_debug?.snapshot());
    expect(debugBeforeSelection?.latestGraph?.sourceUID).toBe("issue-rent");
    expect(debugBeforeSelection?.latestGraph?.layoutDirection).toBe("TB");
    expect(debugBeforeSelection?.latestGraph?.nodeIds).toEqual(
      expect.arrayContaining(["issue-rent", "issue-q3", "issue-follow-up", "issue-deep-follow-up"]),
    );
    expect(debugBeforeSelection?.latestGraph?.edgeCount).toBe(4);
    expect(debugBeforeSelection?.latestGraph?.layoutEdgeCount).toBe(3);
    await selectGraphFilterItem(graph, "depth-1");
    await expect(await graphFilterItem(graph, "context-all")).toBeEnabled();
    await expect
      .poll(() =>
        page.evaluate(() => {
          const latest = window.__middleman_kata_graph_debug?.snapshot().latestGraph;
          return latest
            ? {
                depthLimit: latest.depthLimit,
                nodeIds: latest.nodeIds,
                hasDeepFollowUp: latest.nodeIds.includes("issue-deep-follow-up"),
                edgeCount: latest.edgeCount,
                layoutEdgeCount: latest.layoutEdgeCount,
              }
            : null;
        }),
      )
      .toMatchObject({
        depthLimit: "1",
        nodeIds: expect.arrayContaining(["issue-rent", "issue-q3", "issue-follow-up"]),
        hasDeepFollowUp: false,
        edgeCount: 3,
        layoutEdgeCount: 2,
      });
    await selectGraphFilterItem(graph, "depth-full");
    await expect
      .poll(() =>
        page.evaluate(() => {
          const latest = window.__middleman_kata_graph_debug?.snapshot().latestGraph;
          return latest
            ? {
                depthLimit: latest.depthLimit,
                hasDeepFollowUp: latest.nodeIds.includes("issue-deep-follow-up"),
                edgeCount: latest.edgeCount,
                layoutEdgeCount: latest.layoutEdgeCount,
              }
            : null;
        }),
      )
      .toMatchObject({
        depthLimit: "full",
        hasDeepFollowUp: true,
        edgeCount: 4,
        layoutEdgeCount: 3,
      });
    const fullSnapshotBeforeContext = await page.evaluate(() => {
      const latest = window.__middleman_kata_graph_debug?.snapshot().latestGraph;
      return latest
        ? {
            nodeIds: latest.nodeIds,
            edgeCount: latest.edgeCount,
            layoutEdgeCount: latest.layoutEdgeCount,
            nodePositions: latest.nodePositions,
          }
        : null;
    });
    expect(fullSnapshotBeforeContext).not.toBeNull();
    await selectGraphFilterItem(graph, "context-1");
    await expect(await graphFilterItem(graph, "visibility-hide-done")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect
      .poll(() =>
        page.evaluate(() => {
          const latest = window.__middleman_kata_graph_debug?.snapshot().latestGraph;
          return latest
            ? {
                contextDepth: latest.contextDepth,
                nodeIds: latest.nodeIds,
                edgeCount: latest.edgeCount,
                layoutEdgeCount: latest.layoutEdgeCount,
                nodePositions: latest.nodePositions,
                deepEdgeIsContext:
                  latest.edges.find((edge) => edge.id === "blocks:issue-q3:issue-deep-follow-up")?.isDepthContext ??
                  false,
              }
            : null;
        }),
      )
      .toMatchObject({
        contextDepth: "1",
        nodeIds: fullSnapshotBeforeContext?.nodeIds,
        edgeCount: fullSnapshotBeforeContext?.edgeCount,
        layoutEdgeCount: fullSnapshotBeforeContext?.layoutEdgeCount,
        nodePositions: fullSnapshotBeforeContext?.nodePositions,
        deepEdgeIsContext: true,
      });
    const graphNodes = graph.locator(".svelte-flow__node");
    await expect(graphNodes.filter({ hasText: "Pay rent" })).toBeVisible();
    const linkedNode = graphNodes.filter({ hasText: "Email Susan re: Q3" }).first();
    await expect(linkedNode).toBeVisible();

    const linkedBox = await linkedNode.boundingBox();
    const graphBox = await graph.boundingBox();
    if (!linkedBox || !graphBox) throw new Error("Expected linked graph node and graph region to have layout boxes");
    expect(linkedBox.width).toBeGreaterThan(0);
    expect(linkedBox.height).toBeGreaterThan(0);
    const linkedOffset = {
      x: linkedBox.x - graphBox.x,
      y: linkedBox.y - graphBox.y,
    };

    await linkedNode.click();

    await expect(detail.getByRole("heading", { name: "Email Susan re: Q3" })).toBeVisible();
    await expect(detail).toContainText("Confirm the Q3 project review agenda.");
    await expect(graph).toBeVisible();
    await expect(graphNodes.filter({ hasText: "Pay rent" })).toBeVisible();
    await expect(linkedNode).toBeVisible();
    const debugAfterSelection = await page.evaluate(() => window.__middleman_kata_graph_debug?.snapshot());
    expect(debugAfterSelection?.latestGraph?.selectedUID).toBe("issue-q3");
    expect(debugAfterSelection?.latestGraph?.contextDepth).toBe("1");
    expect(debugAfterSelection?.latestGraph?.nodeIds).toEqual(expect.arrayContaining(["issue-rent", "issue-q3"]));
    expect(
      debugAfterSelection?.latestGraph?.edges.find((edge) => edge.id === "blocks:issue-rent:issue-q3"),
    ).toMatchObject({
      isDepthContext: false,
    });
    const incomingEdgeClass = await page.evaluate(() =>
      document.querySelector('.svelte-flow__edge[data-id="blocks:issue-rent:issue-q3"]')?.getAttribute("class"),
    );
    expect(incomingEdgeClass).toContain("kata-graph-edge--selected-adjacent");
    expect(incomingEdgeClass).not.toContain("kata-graph-edge--depth-context");
    const linkedBoxAfterSelection = await linkedNode.boundingBox();
    const graphBoxAfterSelection = await graph.boundingBox();
    if (!linkedBoxAfterSelection || !graphBoxAfterSelection) {
      throw new Error("Expected linked graph node and graph region to keep layout boxes after selection");
    }
    expect(Math.abs(linkedBoxAfterSelection.x - graphBoxAfterSelection.x - linkedOffset.x)).toBeLessThanOrEqual(1);
    expect(Math.abs(linkedBoxAfterSelection.y - graphBoxAfterSelection.y - linkedOffset.y)).toBeLessThanOrEqual(1);

    await page.getByRole("button", { name: "Switch to side-by-side layout" }).click();
    await expect(page.getByRole("separator", { name: "Resize Kata panes" })).toHaveAttribute(
      "aria-orientation",
      "vertical",
    );
    await expect(graph).toHaveAttribute("data-layout-direction", "LR");
    await expect
      .poll(() => page.evaluate(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutDirection))
      .toBe("LR");
    await selectGraphFilterItem(graph, "direction-TB");
    await page.keyboard.press("Escape");
    await expect(graph).toHaveAttribute("data-layout-direction", "TB");
    await expect
      .poll(() => page.evaluate(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutDirection))
      .toBe("TB");
    const toolbarMetrics = await page.evaluate(() => {
      const toolbar = document.querySelector<HTMLElement>(".graph-toolbar");
      const graphFilters = document.querySelector<HTMLElement>(".graph-filter-menu .kit-filter-dropdown__btn");
      if (!toolbar || !graphFilters) return null;
      const toolbarRect = toolbar.getBoundingClientRect();
      const graphFiltersRect = graphFilters.getBoundingClientRect();
      return {
        overflowX: toolbar.scrollWidth - toolbar.clientWidth,
        graphFiltersRight: graphFiltersRect.right,
        graphFiltersBottom: graphFiltersRect.bottom,
        toolbarRight: toolbarRect.right,
        toolbarBottom: toolbarRect.bottom,
      };
    });
    expect(toolbarMetrics).not.toBeNull();
    expect(toolbarMetrics!.overflowX).toBeLessThanOrEqual(1);
    expect(toolbarMetrics!.graphFiltersRight).toBeLessThanOrEqual(toolbarMetrics!.toolbarRight + 1);
    expect(toolbarMetrics!.graphFiltersBottom).toBeLessThanOrEqual(toolbarMetrics!.toolbarBottom + 1);

    await graph.getByRole("button", { name: "Back to task list" }).click();
    await expect(page.getByLabel("Search tasks")).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata stale generation recovery retries preserved selection and graph intent through the real workspace", async ({
  page,
}) => {
  const graphRoot = { ...issues[0]!, blocks: [issueLinkPeer(issues[1]!)] };
  const backend = await startKataBackend({ issues: [graphRoot, issues[1]!] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  const desiredIntents: string[] = [];
  let injectedStaleGeneration = false;

  await page.route("**/api/v1/kata/tasks/snapshot*", async (route) => {
    const url = new URL(route.request().url());
    if (
      url.searchParams.get("selected_issue_uid") === issues[1]!.uid &&
      url.searchParams.get("graph_source_uid") === graphRoot.uid
    ) {
      desiredIntents.push(url.search);
      if (!injectedStaleGeneration) {
        injectedStaleGeneration = true;
        const response = await route.fetch();
        const body = (await response.json()) as Record<string, unknown>;
        await route.fulfill({ response, json: { ...body, generation: 0 } });
        return;
      }
    }
    await route.continue();
  });

  try {
    await page.goto(`${server.info.base_url}/kata?issue=${graphRoot.uid}`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: graphRoot.title })).toBeVisible();
    await detail.getByRole("button", { name: "Open reachable graph" }).click();

    const graph = page.getByRole("region", { name: "Reachable task graph" });
    await expect(graph).toBeVisible();
    await graph.getByRole("button", { name: new RegExp(issues[1]!.title) }).click();

    await expect(page.getByRole("alert")).toContainText("generation moved backwards");
    await expect(detail.getByRole("heading", { name: graphRoot.title })).toBeVisible();
    await page.getByRole("button", { name: "Retry Kata snapshot" }).click();

    await expect(detail.getByRole("heading", { name: issues[1]!.title })).toBeVisible();
    await expect(graph).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`issue=${issues[1]!.uid}`));
    expect(desiredIntents).toHaveLength(2);
    expect(desiredIntents[1]).toBe(desiredIntents[0]);

    const refreshedTitle = "Q3 selection recovered from the live stream";
    backend.state.issues = backend.state.issues.map((issue) =>
      issue.uid === issues[1]!.uid ? { ...issue, title: refreshedTitle, revision: issue.revision + 1 } : issue,
    );
    emitDaemonChange(
      backend.state,
      eventRow({
        event_id: backend.state.nextEventID++,
        event_uid: "event-stale-recovery-stream",
        type: "issue.updated",
        project_id: issues[1]!.project_id,
        project_uid: issues[1]!.project_uid,
        project_name: issues[1]!.project_name,
        issue: issues[1]!,
      }),
    );
    await expect(detail.getByRole("heading", { name: refreshedTitle })).toBeVisible();
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
  const middlemanKataReads: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (request.method() === "GET" && url.pathname.startsWith("/api/v1/kata/tasks/")) {
      middlemanKataReads.push(url.pathname);
    }
  });

  try {
    await page.goto(`${server.info.base_url}/kata`);

    await expectKataDaemonSwitcherReady(page);
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect.poll(() => middlemanKataReads).toContain("/api/v1/kata/tasks/snapshot");
    await expect.poll(() => middlemanKataReads).toContain("/api/v1/kata/tasks/events");
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

test("kata quick capture appears from replacement snapshot without response-selected detail", async ({ page }) => {
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
    await expect(page.getByRole("button", { name: /Inbox#IN-23 Capture from browser/ })).toBeVisible();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Select a task");
    await expect(page).not.toHaveURL(/issue=/);
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
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace replaces its accepted snapshot after compact invalidation", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  const snapshotRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/kata/tasks/snapshot") snapshotRequests.push(url.search);
  });

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect.poll(() => backend.state.streams.size).toBeGreaterThan(0);
    const requestsBeforeInvalidation = snapshotRequests.length;

    backend.state.issues = backend.state.issues.map((issue) =>
      issue.uid === "issue-rent" ? { ...issue, title: "Pay rent from replacement snapshot", revision: 2 } : issue,
    );
    emitDaemonChange(
      backend.state,
      eventRow({
        event_id: 101,
        event_uid: "event-rent-replaced",
        type: "issue.updated",
        project_id: issues[0]!.project_id,
        project_uid: issues[0]!.project_uid,
        project_name: issues[0]!.project_name,
        issue: issues[0]!,
      }),
    );

    await expect(page.getByRole("button", { name: /Pay rent from replacement snapshot/ })).toBeVisible();
    expect(snapshotRequests.length).toBeGreaterThan(requestsBeforeInvalidation);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata visible selection stays anchored when its accepted snapshot inserts a row above", async ({ page }) => {
  const initialIssues = Array.from({ length: 20 }, (_, index) => ({
    ...issueSummary({
      id: 300 + index,
      uid: `issue-visible-anchor-${index + 1}`,
      project_id: 2,
      project_uid: "project-kata",
      project_name: "Kata",
      short_id: `visible-anchor-${index + 1}`,
      qualified_id: `Kata#visible-anchor-${index + 1}`,
      title: `Visible anchor task ${index + 1}`,
      body: `Visible anchor task ${index + 1} body.`,
      labels: ["work"],
    }),
    updated_at: `2026-05-${String(30 - index).padStart(2, "0")}T08:00:00Z`,
  }));
  const selected = initialIssues[8]!;
  const inserted = {
    ...issueSummary({
      id: 399,
      uid: "issue-visible-anchor-inserted",
      project_id: 2,
      project_uid: "project-kata",
      project_name: "Kata",
      short_id: "visible-anchor-inserted",
      qualified_id: "Kata#visible-anchor-inserted",
      title: "Inserted above visible selection",
      body: "This task arrives with the accepted selected snapshot.",
      labels: ["work"],
    }),
    updated_at: "2026-05-31T08:00:00Z",
  };
  const backend = await startKataBackend({ issues: initialIssues });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  let holdUnselectedReplacement = false;
  let heldUnselectedReplacement = false;
  let releaseUnselectedReplacement!: () => void;
  let markUnselectedReplacementStarted!: () => void;
  let markUnselectedReplacementFinished!: () => void;
  const unselectedReplacementBarrier = new Promise<void>((resolve) => {
    releaseUnselectedReplacement = resolve;
  });
  const unselectedReplacementStarted = new Promise<void>((resolve) => {
    markUnselectedReplacementStarted = resolve;
  });
  const unselectedReplacementFinished = new Promise<void>((resolve) => {
    markUnselectedReplacementFinished = resolve;
  });
  await page.route("**/api/v1/kata/tasks/snapshot*", async (route) => {
    const url = new URL(route.request().url());
    if (holdUnselectedReplacement && !heldUnselectedReplacement && !url.searchParams.get("selected_issue_uid")) {
      heldUnselectedReplacement = true;
      markUnselectedReplacementStarted();
      await unselectedReplacementBarrier;
      await route.abort("aborted");
      markUnselectedReplacementFinished();
      return;
    }
    await route.continue();
  });

  try {
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);

    const tableBody = page.locator(".issue-list .table-body");
    const selectedRow = page.getByRole("button", { name: new RegExp(selected.title) });
    await expect(selectedRow).toBeVisible();
    await expect.poll(() => tableBody.evaluate((body) => body.scrollHeight > body.clientHeight)).toBe(true);
    await tableBody.evaluate((body) => {
      body.style.overflowAnchor = "none";
      body.dataset.e2eListInstance = "visible-selection-anchor";
    });
    const bodyBox = await tableBody.boundingBox();
    const selectedBox = await selectedRow.boundingBox();
    expect(bodyBox).not.toBeNull();
    expect(selectedBox).not.toBeNull();
    await tableBody.evaluate(
      (body, delta) => {
        body.scrollTop += delta;
      },
      selectedBox!.y - bodyBox!.y - 72,
    );
    const selectedTop = (await selectedRow.boundingBox())!.y;
    await expect(selectedRow).toBeInViewport();

    backend.state.issues = [inserted, ...backend.state.issues];
    holdUnselectedReplacement = true;
    emitDaemonChange(
      backend.state,
      eventRow({
        event_id: 601,
        event_uid: "event-visible-anchor-inserted",
        type: "issue.created",
        project_id: inserted.project_id,
        project_uid: inserted.project_uid,
        project_name: inserted.project_name,
        issue: inserted,
      }),
    );
    await unselectedReplacementStarted;
    const acceptedSelection = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        url.pathname === "/api/v1/kata/tasks/snapshot" &&
        url.searchParams.get("selected_issue_uid") === selected.uid &&
        response.ok()
      );
    });
    await selectedRow.click();
    await acceptedSelection;

    await expect(page.getByRole("heading", { name: selected.title })).toBeVisible();
    await expect(selectedRow).toHaveAttribute("aria-current", "true");
    await expect(page.getByRole("button", { name: /Inserted above visible selection/ })).toHaveCount(1);
    await expect(tableBody).toHaveAttribute("data-e2e-list-instance", "visible-selection-anchor");
    expect(Math.round((await selectedRow.boundingBox())!.y)).toBe(Math.round(selectedTop));
    releaseUnselectedReplacement();
    await unselectedReplacementFinished;
  } finally {
    releaseUnselectedReplacement();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata daemon switch fences a late old-daemon compact frame delivered on the active stream", async ({ page }) => {
  await page.addInitScript(() => {
    type PendingFrame = { seq: number; bytes: Uint8Array };
    type ControlledStream = {
      controller: ReadableStreamDefaultController<Uint8Array>;
      pending: PendingFrame[];
      waiting: boolean;
      inflightSeq: number | null;
      consumedSeq: number;
      nextSeq: number;
      cancelled: boolean;
    };

    const encoder = new TextEncoder();
    const streams = new Map<string, ControlledStream>();

    function deliver(stream: ControlledStream): void {
      if (!stream.waiting || stream.cancelled) return;
      const nextFrame = stream.pending.shift();
      if (!nextFrame) return;
      stream.waiting = false;
      stream.inflightSeq = nextFrame.seq;
      stream.controller.enqueue(nextFrame.bytes);
    }

    const control = {
      has(daemon: string): boolean {
        return streams.has(daemon);
      },
      push(daemon: string, frame: string): number {
        const stream = streams.get(daemon);
        if (!stream || stream.cancelled) throw new Error(`controlled Kata stream is unavailable: ${daemon}`);
        const seq = ++stream.nextSeq;
        stream.pending.push({ seq, bytes: encoder.encode(frame) });
        deliver(stream);
        return seq;
      },
      consumed(daemon: string): number {
        return streams.get(daemon)?.consumedSeq ?? 0;
      },
    };
    Object.defineProperty(window, "__kataE2EEventStreams", { value: control });

    const originalFetch = window.fetch.bind(window);
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (url.pathname !== "/api/v1/kata/tasks/events") {
        return originalFetch(input, init);
      }

      const daemon = request.headers.get("X-Middleman-Kata-Daemon") ?? "";
      let controlled!: ControlledStream;
      const body = new ReadableStream<Uint8Array>(
        {
          start(controller) {
            controlled = {
              controller,
              pending: [],
              waiting: false,
              inflightSeq: null,
              consumedSeq: 0,
              nextSeq: 0,
              cancelled: false,
            };
            streams.set(daemon, controlled);
            controller.enqueue(encoder.encode(": connected\n\n"));
          },
          pull() {
            if (controlled.inflightSeq !== null) {
              controlled.consumedSeq = controlled.inflightSeq;
              controlled.inflightSeq = null;
            }
            controlled.waiting = true;
            deliver(controlled);
          },
          cancel() {
            controlled.cancelled = true;
          },
        },
        { highWaterMark: 0 },
      );
      return new Response(body, {
        status: 200,
        headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" },
      });
    };
  });

  const home = await startKataBackend();
  const workIssue = issueSummary({
    id: 301,
    uid: "issue-work",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "work-1",
    qualified_id: "Kata#work-1",
    title: "Accepted work task",
    body: "This task belongs to the work daemon.",
    labels: ["work"],
  });
  const work = await startKataBackend({ projects: [projects[1]!], issues: [workIssue] });
  const kataHome = await configureKataHomeDaemons(
    [
      { name: "home", url: home.url },
      { name: "work", url: work.url },
    ],
    "home",
  );
  const server = await startIsolatedE2EServer();
  const snapshotDaemons: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname !== "/api/v1/kata/tasks/snapshot") return;
    snapshotDaemons.push(request.headers()["x-middleman-kata-daemon"] ?? "");
  });

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();

    const workSnapshotResponse = page.waitForResponse((response) => {
      const request = response.request();
      return (
        new URL(response.url()).pathname === "/api/v1/kata/tasks/snapshot" &&
        request.headers()["x-middleman-kata-daemon"] === "work" &&
        response.status() === 200
      );
    });
    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();
    const workSnapshot = (await (await workSnapshotResponse).json()) as {
      server_instance_id: string;
      invalidation_epoch: number;
      event_cursor: number;
    };
    await expect(page.getByRole("button", { name: /Accepted work task/ })).toBeVisible();
    await page.waitForFunction(() =>
      (
        window as unknown as {
          __kataE2EEventStreams: { has(daemon: string): boolean };
        }
      ).__kataE2EEventStreams.has("work"),
    );

    const requestsBeforeStaleFrame = snapshotDaemons.length;
    const staleCursor = workSnapshot.event_cursor + 10;
    const staleFrame = [
      `id: ${staleCursor}`,
      "event: kata.tasks.reset",
      `data: ${JSON.stringify({
        server_instance_id: workSnapshot.server_instance_id,
        daemon_id: "home",
        epoch: workSnapshot.invalidation_epoch + 10,
        cursor: staleCursor,
      })}`,
      "",
      "",
    ].join("\n");
    const staleSeq = await page.evaluate((frame) => {
      return (
        window as unknown as {
          __kataE2EEventStreams: { push(daemon: string, frame: string): number };
        }
      ).__kataE2EEventStreams.push("work", frame);
    }, staleFrame);
    await page.waitForFunction(
      (seq) =>
        (
          window as unknown as {
            __kataE2EEventStreams: { consumed(daemon: string): number };
          }
        ).__kataE2EEventStreams.consumed("work") >= seq,
      staleSeq,
    );

    expect(snapshotDaemons).toHaveLength(requestsBeforeStaleFrame);
    await expect(page.getByRole("button", { name: /Accepted work task/ })).toBeVisible();

    const currentCursor = staleCursor + 1;
    const currentFrame = [
      `id: ${currentCursor}`,
      "event: kata.tasks.reset",
      `data: ${JSON.stringify({
        server_instance_id: workSnapshot.server_instance_id,
        daemon_id: "work",
        epoch: workSnapshot.invalidation_epoch + 11,
        cursor: currentCursor,
      })}`,
      "",
      "",
    ].join("\n");
    const currentSeq = await page.evaluate((frame) => {
      return (
        window as unknown as {
          __kataE2EEventStreams: { push(daemon: string, frame: string): number };
        }
      ).__kataE2EEventStreams.push("work", frame);
    }, currentFrame);
    await page.waitForFunction(
      (seq) =>
        (
          window as unknown as {
            __kataE2EEventStreams: { consumed(daemon: string): number };
          }
        ).__kataE2EEventStreams.consumed("work") >= seq,
      currentSeq,
    );
    await expect.poll(() => snapshotDaemons.length).toBeGreaterThan(requestsBeforeStaleFrame);
    expect(snapshotDaemons.at(-1)).toBe("work");
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
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

    await expect(page.locator(".kit-flash-stack").getByRole("status")).toContainText("Could not unlink message.");
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

test("kata detail formats complete retained multi-page task history from selected snapshot enrichment", async ({
  page,
}) => {
  const selectedHistory = Array.from({ length: 125 }, (_, index) =>
    eventRow({
      event_id: index + 1,
      event_uid: `event-history-${index + 1}`,
      type: "issue.updated",
      project_id: issues[0]!.project_id,
      project_uid: issues[0]!.project_uid,
      project_name: issues[0]!.project_name,
      issue: issues[0]!,
    }),
  );
  const backend = await startKataBackend({
    events: [
      ...selectedHistory,
      eventRow({
        event_id: 126,
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
      eventRow({
        event_id: 127,
        event_uid: "event-project-only",
        type: "project.updated",
        project_id: issues[0]!.project_id,
        project_uid: issues[0]!.project_uid,
        project_name: issues[0]!.project_name,
      }),
      eventRow({
        event_id: 128,
        event_uid: "event-other-project",
        type: "issue.updated",
        project_id: issues[1]!.project_id,
        project_uid: issues[1]!.project_uid,
        project_name: issues[1]!.project_name,
        issue: issues[1]!,
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
    await expect(detail.locator(".events .event-row")).toHaveCount(126);
    await expect(page.getByRole("alert").filter({ hasText: "Could not load selected task history." })).toHaveCount(0);
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/1/events?after_id=0&limit=1000");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/1/events?after_id=100&limit=1000");
    await expect.poll(() => backend.state.seenPaths).toContain("GET /api/v1/projects/1/events?after_id=127&limit=1000");
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
    await expectKataDaemonSwitcherReady(page);
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
    await expectKataDaemonSwitcherReady(page);
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
    await expectKataDaemonSwitcherReady(page);
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

test("kata task columns float and preserve responsive grid alignment", async ({ page }) => {
  await page.setViewportSize({ width: 1180, height: 760 });
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);
    const list = page.locator(".issue-list");
    await list.evaluate((element) => {
      element.style.width = "940px";
      element.style.flex = "none";
    });

    const titleCell = page.locator(".issue-row .cell-title").first();
    const titleWidthBefore = await titleCell.evaluate((element) => element.getBoundingClientRect().width);
    await expectTaskColumnsAligned(page, ["id", "title", "updated", "priority", "due", "owner", "tags"]);

    const trigger = page.getByRole("button", { name: "Columns" });
    await trigger.click();
    const panel = page.locator(".column-picker__panel");
    await expect(panel).toBeVisible();
    expect(await panel.evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    expect(Number(await panel.evaluate((element) => getComputedStyle(element).zIndex))).toBeGreaterThan(0);
    const triggerBox = await trigger.boundingBox();
    const panelBox = await panel.boundingBox();
    expect(triggerBox).not.toBeNull();
    expect(panelBox).not.toBeNull();
    expectFloatingPanelAnchored(triggerBox!, panelBox!);
    expect(panelBox!.x).toBeGreaterThanOrEqual(0);
    expect(panelBox!.y).toBeGreaterThanOrEqual(0);
    expect(panelBox!.x + panelBox!.width).toBeLessThanOrEqual(1181);
    expect(panelBox!.y + panelBox!.height).toBeLessThanOrEqual(761);

    await page.getByRole("checkbox", { name: "Tags" }).click();
    await expect(page.locator(".table-header .col-tags")).toHaveCount(0);
    await expect(page.locator(".issue-row .cell-tags")).toHaveCount(0);
    const titleWidthAfter = await titleCell.evaluate((element) => element.getBoundingClientRect().width);
    expect(titleWidthAfter).toBeGreaterThan(titleWidthBefore);
    await page.keyboard.press("Escape");

    await list.evaluate((element) => {
      element.style.width = "640px";
    });
    await expect(trigger.locator(".action-label")).toBeHidden();
    await expect(page.locator(".table-header .col-owner")).toBeHidden();
    await expectTaskColumnsAligned(page, ["id", "title", "updated", "priority", "due"]);
    await list.evaluate((element) => {
      element.style.width = "940px";
    });
    await expect(page.locator(".table-header .col-owner")).toBeVisible();
    await expect(page.locator(".table-header .col-tags")).toHaveCount(0);

    await page.getByRole("button", { name: /Sort by Owner/ }).click();
    await list.evaluate((element) => {
      element.style.width = "640px";
    });
    await expect(page.getByRole("button", { name: "Sort by Owner, currently ascending" })).toBeVisible();
    await expectTaskColumnsAligned(page, ["id", "title", "updated", "priority", "due", "owner"]);
    await trigger.click();

    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 720, height: 540 });
    expect(await page.evaluate(() => ({ width: window.innerWidth, height: window.innerHeight }))).toEqual({
      width: 720,
      height: 540,
    });
    await trigger.scrollIntoViewIfNeeded();
    await trigger.click();
    await expect
      .poll(async () => {
        const box = await panel.boundingBox();
        return box ? box.x + box.width : Number.POSITIVE_INFINITY;
      })
      .toBeLessThanOrEqual(721);
    await expect
      .poll(async () => {
        const box = await panel.boundingBox();
        return box ? box.y + box.height : Number.POSITIVE_INFINITY;
      })
      .toBeLessThanOrEqual(541);
    const constrainedPanelBox = await panel.boundingBox();
    const constrainedTriggerBox = await trigger.boundingBox();
    expect(constrainedPanelBox).not.toBeNull();
    expect(constrainedTriggerBox).not.toBeNull();
    expect(constrainedPanelBox!.x).toBeGreaterThanOrEqual(0);
    expect(constrainedPanelBox!.y).toBeGreaterThanOrEqual(0);
    expectFloatingPanelAnchored(constrainedTriggerBox!, constrainedPanelBox!);
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
    await expectKataDaemonSwitcherReady(page);
    await page.getByRole("button", { name: "All Open" }).click();
    const rows = page.locator(".issue-list .issue-row");
    await expect(rows.first()).toContainText("Email Susan re: Q3");
    await expect(rows.nth(1)).toContainText("Pay rent");

    await rows.first().focus();
    await page.keyboard.press("j");

    await expect(rows.nth(1)).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");

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

test("kata single configured daemon keeps the daemon switcher visible", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);
    await expect(page.getByTestId("daemon-chip")).toContainText("e2e");
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
    await expectKataDaemonSwitcherReady(page);

    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Capture Inbox\s+1$/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata sidebar switches system views and renders project areas", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);

    await expect(page.getByRole("button", { name: "Inbox" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Today" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Logbook" })).toBeVisible();
    const personal = page.getByRole("button", { name: /^Personal\s+1$/ });
    const work = page.getByRole("button", { name: /^Work\s+1$/ });
    await expect(personal).toHaveAttribute("aria-expanded", "true");
    await expect(work).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Kata\s+1$/ })).toBeVisible();

    await personal.click();
    await expect(personal).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toHaveCount(0);
    await personal.click();
    await expect(personal).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("button", { name: /^Finances\s+1$/ })).toBeVisible();

    await page.getByRole("button", { name: "Inbox" }).click();
    await expect(page.getByRole("heading", { name: "Inbox", level: 2 })).toBeVisible();

    await page.getByRole("button", { name: /^Finances\s+1$/ }).click();
    await expect(page.getByRole("heading", { name: "Finances", level: 2 })).toBeVisible();
    await expect(page).toHaveURL(/scope=project-finance/);

    await page.getByRole("button", { name: "Today" }).click();
    await expect(page.getByRole("heading", { name: "Today", level: 2 })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata project create waits for accepted snapshot scope instead of trusting the response project", async ({
  page,
}) => {
  let releaseSnapshot!: () => void;
  const snapshotBarrier = new Promise<void>((resolve) => {
    releaseSnapshot = resolve;
  });
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);

    await page.getByRole("button", { name: "New project" }).click();
    const input = page.getByRole("textbox", { name: "New project name" });
    await expect(input).toBeVisible();
    await input.fill("Sabbatical");
    backend.state.nextProjectResponse = {
      id: 999,
      uid: "project-response-decoy",
      name: "Sabbatical",
      metadata: { area: "Response only", sidebar_order: 999 },
      open_count: 37,
    };
    backend.state.issuesBarrier = snapshotBarrier;
    await input.press("Enter");

    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects");
    await expect(input).toHaveCount(0);
    await expect(page).not.toHaveURL(/scope=/);
    await expect(page).not.toHaveURL(/project-response-decoy/);
    await expect(page.getByRole("heading", { name: "All Open", level: 2 })).toBeVisible();

    releaseSnapshot();
    await expect(page.getByRole("button", { name: /^Sabbatical\s+0$/ })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Sabbatical", level: 2 })).toBeVisible();
    await expect(page).toHaveURL(/scope=project-sabbatical/);
    await expect(page).not.toHaveURL(/project-response-decoy/);
  } finally {
    releaseSnapshot();
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
    await expectKataDaemonSwitcherReady(page);

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

test("kata parent row expands children from the accepted snapshot catalog", async ({ page }) => {
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
    parent: issueLinkPeer(parent),
    metadata: { scheduled_on: today },
  });
  const backend = await startKataBackend({ issues: [parent, child] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);
    const list = page.locator(".kata-list");
    const parentRow = list.getByRole("button", { name: /Parent task/ });
    await expect(parentRow).toBeVisible();
    await expect(list.getByRole("button", { name: /Child task/ })).toHaveCount(0);
    await expect(page.getByText("2 tasks")).toBeVisible();

    await parentRow.press("ArrowRight");

    await expect(parentRow).toHaveAttribute("aria-expanded", "true");
    const childRow = list.getByRole("button", { name: /Child task/ });
    await expect(childRow).toBeVisible();

    await parentRow.focus();
    await page.keyboard.press("j");

    await expect(childRow).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Child task body.");

    await page.keyboard.press("k");

    await expect(parentRow).toBeFocused();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Parent task body.");

    await parentRow.press("ArrowLeft");

    await expect(parentRow).toHaveAttribute("aria-expanded", "false");
    await expect(list.getByRole("button", { name: /Child task/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata focused nested selection survives a structural snapshot reset", async ({ page }) => {
  const parent = issueSummary({
    id: 401,
    uid: "issue-reset-focus-parent",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "reset-focus-parent",
    qualified_id: "Finances#reset-focus-parent",
    title: "Reset focus parent",
    body: "Parent task for reset focus coverage.",
    labels: ["home"],
    child_counts: { open: 1, total: 1 },
    metadata: { scheduled_on: today },
  });
  const child = issueSummary({
    id: 402,
    uid: "issue-reset-focus-child",
    project_id: 1,
    project_uid: "project-finance",
    project_name: "Finances",
    short_id: "reset-focus-child",
    qualified_id: "Finances#reset-focus-child",
    title: "Reset focus child",
    body: "Child task for reset focus coverage.",
    labels: ["home"],
    parent: issueLinkPeer(parent),
    parent_short_id: parent.short_id,
    metadata: { scheduled_on: today },
  });
  const backend = await startKataBackend({ issues: [parent, child] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);
    const list = page.locator(".kata-list");
    const parentRow = list.getByRole("button", { name: /Reset focus parent/ });
    await parentRow.press("ArrowRight");
    await expect(parentRow).toHaveAttribute("aria-expanded", "true");

    const childRow = list.getByRole("button", { name: /Reset focus child/ });
    await childRow.click();
    await expect(page.getByRole("heading", { name: child.title })).toBeVisible();
    await childRow.focus();
    await expect(childRow).toBeFocused();
    await expect.poll(() => backend.state.streams.size).toBeGreaterThan(0);

    const updatedParent = { ...parent, revision: parent.revision + 1 };
    backend.state.issues = backend.state.issues.map((issue) => (issue.uid === parent.uid ? updatedParent : issue));
    const acceptedReset = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        url.pathname === "/api/v1/kata/tasks/snapshot" &&
        url.searchParams.get("selected_issue_uid") === child.uid &&
        response.ok()
      );
    });
    emitDaemonChange(
      backend.state,
      eventRow({
        event_id: 501,
        event_uid: "event-reset-focused-parent",
        type: "issue.updated",
        project_id: parent.project_id,
        project_uid: parent.project_uid,
        project_name: parent.project_name,
        issue: updatedParent,
      }),
    );
    await acceptedReset;

    await expect(parentRow).toHaveAttribute("aria-expanded", "true");
    await expect(childRow).toBeVisible();
    await expect(childRow).toBeInViewport();
    await expect(childRow).toHaveAttribute("aria-current", "true");
    await expect(childRow).toBeFocused();
    await expect(page.getByRole("heading", { name: child.title })).toBeVisible();
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
    parent: issueLinkPeer(parent),
    metadata: { scheduled_on: today },
  });
  const backend = await startKataBackend({ issues: [child, parent] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);

    const list = page.locator(".kata-list");
    const parentRow = list.getByRole("button", { name: /Parent task/ });
    await expect(parentRow).toBeVisible();
    await parentRow.click();
    await expect(parentRow).toHaveAttribute("aria-current", "true");
    await expect(list.getByRole("button", { name: /Child task/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Parent task body.");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata workspace restores filtered expanded child selection after leaving Kata", async ({ page }) => {
  const fixture = workspaceStateFixture();
  const backend = await startKataBackend({ issues: fixture.issues, projects: fixture.projects });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await seedRestoredKataWorkspace(page, server.info.base_url);
    await expect
      .poll(() => page.evaluate(() => window.localStorage.getItem("middleman:kata:workspace-state/v2")))
      .toContain(fixture.child.uid);
    const focusUIDBeforeLeave = await page.evaluate(() => document.activeElement?.getAttribute("data-uid"));
    expect(focusUIDBeforeLeave).toBe(fixture.child.uid);

    await appHeaderTab(page, "PRs").click();
    await expect(page).toHaveURL(/\/pulls/);
    await page.goto(`${server.info.base_url}/kata`);

    await expect(page.getByRole("button", { name: /Project scope: Kata/ })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Status: All" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Owner" })).toHaveValue("Susan");
    await expect(page.getByRole("textbox", { name: "Label" })).toHaveValue("work");
    await expect(page.getByLabel("Search tasks")).toHaveValue("child");
    const parentRow = page.getByRole("button", { name: /Parent child workflow/ });
    const childRow = page.getByRole("button", { name: /Child child workflow/ });
    await expect(parentRow).toHaveAttribute("aria-expanded", "true");
    await expect(childRow).toHaveAttribute("aria-current", "true");
    await expect(childRow).toBeInViewport();
    await expect(page.getByRole("heading", { name: "Child child workflow" })).toBeVisible();
    await expect(childRow).not.toBeFocused();
    expect(await page.evaluate(() => document.activeElement?.getAttribute("data-uid"))).not.toBe(focusUIDBeforeLeave);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata explicit URL view uses fresh defaults instead of unrelated persisted filters", async ({ page }) => {
  const fixture = workspaceStateFixture();
  const backend = await startKataBackend({ issues: fixture.issues, projects: fixture.projects });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await seedRestoredKataWorkspace(page, server.info.base_url, { projectScoped: false });
    await expect
      .poll(() => page.evaluate(() => window.localStorage.getItem("middleman:kata:workspace-state/v2")))
      .toContain(fixture.child.uid);

    await appHeaderTab(page, "PRs").click();
    await page.goto(`${server.info.base_url}/kata?view=inbox&issue=issue-rent`);

    await expect(page.getByRole("heading", { name: "Inbox", level: 2, exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Owner" })).toHaveValue("");
    await expect(page.getByRole("textbox", { name: "Label" })).toHaveValue("");
    await expect(page.getByLabel("Search tasks")).toHaveValue("");
    await expect(page.locator(".kata-list").getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
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
    const projectRows = page.locator(".kata-sidebar .project-select-button");
    await expect(projectRows.filter({ hasText: /^Work\s+1$/ })).toBeVisible();
    await expect(projectRows.filter({ hasText: /^Home\s+1$/ })).toHaveCount(0);
  } finally {
    await page.close();
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("kata failed daemon switch recovers the nominal daemon through snapshot retry", async ({ page }) => {
  const home = await startKataBackend();
  const work = await startKataBackend({ failNextProjectsStatus: 503 });
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
    await expect(page.locator(".kata-list").getByRole("button", { name: /Pay rent/ })).toBeVisible();

    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();

    await expect(page.getByRole("alert")).toContainText("Kata list projects failed: unexpected status code: 503");
    await expect(page.locator(".kata-list").getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await page.getByRole("button", { name: "Retry Kata snapshot" }).click();

    await expect(page.getByTestId("daemon-chip")).toContainText("home");
    await expect(page.locator(".kata-list").getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect.poll(() => home.state.streams.size).toBeGreaterThan(0);

    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();
    await expect(page.getByTestId("daemon-chip")).toContainText("work");
    await expect(page.locator(".kata-list").getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
  } finally {
    await page.close();
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
    await page.close();
    await server.stop();
    kataHome.restore();
    await home.close();
    await empty.close();
  }
});

test("kata search and presentation filters locally over accepted snapshots", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  const snapshotRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/kata/tasks/snapshot") snapshotRequests.push(url.search);
  });

  try {
    await page.goto(`${server.info.base_url}/kata`);

    const taskList = page.locator(".kata-list");
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();

    const requestsBeforeSearch = snapshotRequests.length;
    await page.getByLabel("Search tasks").fill("q3");
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
    expect(snapshotRequests).toHaveLength(requestsBeforeSearch);
    await page.getByRole("combobox", { name: "Status: Open" }).click();
    await page.getByRole("option", { name: "All" }).click();
    await page.getByRole("textbox", { name: "Owner" }).fill("Susan");
    await page.getByRole("textbox", { name: "Label" }).fill("work");
    await page.getByRole("button", { name: /^Kata\s+1$/ }).click();

    const q3Row = taskList.getByRole("button", { name: /Email Susan re: Q3/ });
    await expect(q3Row).toBeVisible();
    await q3Row.click();
    await expect(page).toHaveURL(/scope=project-kata/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata Ready filters through authoritative global and project snapshots", async ({ page }) => {
  const readyParent = issueSummary({
    id: 41,
    uid: "issue-ready-parent",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-ready",
    qualified_id: "Kata#kat-ready",
    title: "Ship the ready change",
    body: "This task is approved by Kata's dependency graph.",
    owner: "Susan",
    labels: ["work"],
    child_counts: { open: 2, total: 2 },
  });
  const readyChild = issueSummary({
    id: 42,
    uid: "issue-ready-child",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-ready-child",
    qualified_id: "Kata#kat-ready-child",
    title: "Ready follow-up",
    body: "This child is also approved by Kata's dependency graph.",
    owner: "Susan",
    labels: ["work"],
    parent: issueLinkPeer(readyParent),
    parent_short_id: readyParent.short_id,
  });
  const blockedChild = issueSummary({
    id: 43,
    uid: "issue-blocked-child",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-blocked",
    qualified_id: "Kata#kat-blocked",
    title: "Blocked follow-up",
    body: "This child is open but not ready.",
    owner: "Susan",
    labels: ["work"],
    parent: issueLinkPeer(readyParent),
    parent_short_id: readyParent.short_id,
  });
  const backend = await startKataBackend({
    issues: [...issues, readyParent, readyChild, blockedChild],
    readyIssueUIDs: [readyParent.uid, readyChild.uid],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  const snapshotIntents: Array<Record<string, string>> = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/kata/tasks/snapshot") {
      snapshotIntents.push(Object.fromEntries(url.searchParams));
    }
  });

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();

    await page.getByRole("combobox", { name: "Status: Open" }).click();
    await page.getByRole("option", { name: "Ready" }).click();
    await page.getByLabel("Search tasks").fill("Ship the ready change");

    const readyRow = page.getByRole("button", { name: /Ship the ready change/ });
    await expect(readyRow).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await expect
      .poll(() => snapshotIntents.some((intent) => intent.scope === "global" && intent.authority === "ready"))
      .toBe(true);
    await readyRow.press("ArrowRight");
    await expect(readyRow).toHaveAttribute("aria-expanded", "true");
    const readyChildRow = page.getByRole("button", { name: /Ready follow-up/ });
    await expect(readyChildRow).toBeVisible();
    await expect(page.getByRole("button", { name: /Blocked follow-up/ })).toHaveCount(0);
    await readyChildRow.click();
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Ready follow-up" })).toBeVisible();
    await detail.getByRole("button", { name: "Add label" }).click();
    await detail.getByLabel("New label").fill("urgent");
    await detail.getByLabel("New label").press("Enter");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/2/issues/issue-ready-child/labels");
    await expect(page).toHaveURL(/issue=issue-ready-child/);
    await expect(detail.getByRole("heading", { name: "Ready follow-up" })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("combobox", { name: "Status: Ready" })).toBeVisible();
    await expect(page.getByLabel("Search tasks")).toHaveValue("Ship the ready change");
    await expect(page).toHaveURL(/issue=issue-ready-child/);
    await expect(detail.getByRole("heading", { name: "Ready follow-up" })).toBeVisible();

    const navigation = page.getByRole("complementary", { name: "Kata navigation" });
    await navigation.getByRole("button", { name: /^Kata\s+\d+$/ }).click();
    await page.getByRole("combobox", { name: "Status: Open" }).click();
    await page.getByRole("option", { name: "Ready" }).click();
    await expect
      .poll(() =>
        snapshotIntents.some(
          (intent) =>
            intent.scope === "project" && intent.project_uid === "project-kata" && intent.authority === "ready",
        ),
      )
      .toBe(true);
    await expect(page.getByRole("button", { name: /Ship the ready change/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Blocked follow-up/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata Ready snapshot failure keeps the authoritative view empty", async ({ page }) => {
  const backend = await startKataBackend({ failNextReadyStatus: 503 });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expect(page.getByRole("button", { name: /Pay rent/ })).toBeVisible();

    await page.getByRole("combobox", { name: "Status: Open" }).click();
    await page.getByRole("option", { name: "Ready" }).click();

    await expect(page.getByRole("alert")).toContainText(
      "Kata list global ready issues failed: unexpected status code: 503",
    );
    await expect(page.getByRole("combobox", { name: "Status: Ready" })).toBeVisible();
    await expect(page.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata logbook shows closed tasks with a truthful closed status filter", async ({ page }) => {
  const closedKataTask = issueSummary({
    id: 34,
    uid: "issue-logbook-closed",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-logbook",
    qualified_id: "Kata#kat-logbook",
    title: "Logbook closed Kata task",
    body: "This closed task should stay visible in Logbook.",
    status: "closed",
    closed_at: now,
    owner: "Susan",
    labels: ["work"],
  });
  const backend = await startKataBackend({ issues: [...issues, closedKataTask] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?view=logbook`);

    const taskList = page.locator(".kata-list");
    await expect(page.getByRole("heading", { name: "Logbook" })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Status: Closed" })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Logbook closed Kata task/ })).toBeVisible();
    await expect(taskList.getByText("No tasks")).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata status filter hides closed rows while the open reload is pending", async ({ page }) => {
  const closedKataTask = issueSummary({
    id: 33,
    uid: "issue-closed-kata",
    project_id: 2,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: "kat-closed",
    qualified_id: "Kata#kat-closed",
    title: "Closed Kata task",
    body: "This task should disappear when Open is selected.",
    status: "closed",
    closed_at: now,
    owner: "Susan",
    labels: ["work"],
  });
  let releaseOpenIssues!: () => void;
  const openIssuesBarrier = new Promise<void>((resolve) => {
    releaseOpenIssues = resolve;
  });
  const backend = await startKataBackend({ issues: [...issues, closedKataTask] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    const taskList = page.locator(".kata-list");

    await page.getByRole("button", { name: /^Kata\s+1$/ }).click();
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();

    await page.getByRole("combobox", { name: "Status: Open" }).click();
    await page.getByRole("option", { name: "Closed" }).click();
    const closedTask = taskList.getByRole("button", { name: /Closed Kata task/ });
    await expect(closedTask).toBeVisible();
    await closedTask.click();
    await expect(
      page.getByRole("region", { name: "Task detail" }).getByRole("button", { name: "Reopen" }),
    ).toBeVisible();

    backend.state.issuesBarrier = openIssuesBarrier;
    await page.getByRole("combobox", { name: "Status: Closed" }).click();
    await page.getByRole("option", { name: "Open" }).click();

    await expect(page.getByRole("combobox", { name: "Status: Open" })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Closed Kata task/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" }).getByRole("button", { name: "Reopen" })).toHaveCount(
      0,
    );

    releaseOpenIssues();
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toBeVisible();
  } finally {
    releaseOpenIssues();
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

    await page.locator(".kit-top-bar__nav").getByRole("button", { name: "Kata" }).click();

    await expect(page).toHaveURL(/\/kata$/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Select a task");
    await expect(page.getByRole("region", { name: "Task detail" })).not.toContainText("Send June rent from checking.");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata clears only an invalid routed task after snapshot acceptance", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?view=deadlines&scope=project-kata&daemon=e2e&issue=issue-missing`);

    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Select a task");
    await expect.poll(() => new URL(page.url()).searchParams.get("issue")).toBeNull();
    const url = new URL(page.url());
    expect(url.searchParams.get("view")).toBe("deadlines");
    expect(url.searchParams.get("scope")).toBe("project-kata");
    expect(url.searchParams.get("daemon")).toBe("e2e");
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
    const taskList = page.locator(".kata-list");
    await expect(taskList.getByRole("button", { name: /Kata deadline task/ })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Deadline scoped task.");

    await page.locator(".kit-top-bar__nav").getByRole("button", { name: "Kata" }).click();
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
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Select a task");
    await taskList.getByRole("button", { name: /Pay rent/ }).click();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send June rent from checking.");

    await page.goBack();
    await expect(page).toHaveURL(/view=deadlines/);
    await expect(page).toHaveURL(/scope=project-kata/);
    await expect(page).toHaveURL(/issue=issue-kata-deadline/);
    await expect(taskList.getByRole("button", { name: /Kata deadline task/ })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Email Susan re: Q3/ })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Deadline scoped task.");

    await page.goto(`${server.info.base_url}/kata?view=all&issue=issue-q3`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail).toContainText("Confirm the Q3 project review agenda.");
    await page.getByRole("button", { name: /Project scope:/ }).click();
    await page.getByRole("option", { name: "All projects" }).click();

    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Pay rent/ })
      .click();
    await expect(detail).toContainText("Send June rent from checking.");
    await expect(page).toHaveURL(/issue=issue-rent/);

    await page.goBack();
    await expect(detail).toContainText("Confirm the Q3 project review agenda.");
    await expect(page).toHaveURL(/issue=issue-q3/);

    await page.getByRole("button", { name: /^Kata\s+\d+$/ }).click();
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
    await dialog.getByRole("textbox", { name: "Search command palette" }).fill("q3");

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
    await dialog.getByRole("textbox", { name: "Search command palette" }).fill("q3");
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

test("command palette results open tasks on the daemon that served the search", async ({ page }) => {
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
  // The same UID exists on both daemons: opening a palette hit must pin
  // the daemon that served the search, not fall back to whatever daemon
  // the workspace happens to resolve.
  const home = await startKataBackend({
    projects: [homeProject],
    issues: [
      issueSummary({
        id: 1011,
        uid: "issue-shared",
        project_id: homeProject.id,
        project_uid: homeProject.uid,
        project_name: homeProject.name,
        short_id: "shared-1",
        qualified_id: "Home#shared-1",
        title: "Shared provenance task",
        body: "Home daemon copy.",
        labels: ["home"],
      }),
    ],
  });
  const work = await startKataBackend({
    projects: [workProject],
    issues: [
      issueSummary({
        id: 2021,
        uid: "issue-shared",
        project_id: workProject.id,
        project_uid: workProject.uid,
        project_name: workProject.name,
        short_id: "shared-1",
        qualified_id: "Work#shared-1",
        title: "Shared provenance task",
        body: "Work daemon copy.",
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
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);
    await page.getByTestId("daemon-chip").click();
    await page.getByTestId("daemon-row-work").click();
    await expect(page.getByTestId("daemon-chip")).toContainText("work");
    await expect(page.locator(".kata-list").getByRole("button", { name: /Shared provenance task/ })).toBeVisible();
    await page.keyboard.press(process.platform === "darwin" ? "Meta+K" : "Control+K");
    const dialog = page.getByRole("dialog", { name: "Command palette" });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("textbox", { name: "Search command palette" }).fill("provenance");
    const taskRow = dialog
      .locator(".palette-group", { hasText: "Kata tasks" })
      .locator(".palette-row", { hasText: "Shared provenance task" });
    await expect(taskRow).toBeVisible();
    await taskRow.click();

    await expect(page).toHaveURL(/issue=issue-shared/);
    await expect(page).toHaveURL(/daemon=work/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Work daemon copy.");
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("docs task links switch an accepted workspace to the folder-bound external daemon", async ({ page }) => {
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
        uid: "issue-shared",
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
        uid: "issue-shared",
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

    await page.goto(`${server.info.base_url}/kata?issue=issue-shared`);
    await expectKataDaemonSwitcherReady(page);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "This task should not open from the bound docs folder.",
    );
    await appHeaderTab(page, "Docs").click();
    await page.evaluate(() => {
      window.history.pushState({}, "", "/docs?folder=work-notes&doc=bound-link.md");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await expect(page.getByRole("heading", { name: "Bound Link" })).toBeVisible();

    await page.getByRole("link", { name: "#shared-1" }).click();

    await expect(page).toHaveURL(/\/kata\?issue=issue-shared/);
    await expect(page.getByTestId("daemon-chip")).toContainText("work");
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Opened through the folder daemon binding.",
    );

    await page.goBack();
    await expect(page).toHaveURL(/\/docs\?folder=work-notes&doc=bound-link\.md/);
    await expect(page.getByRole("heading", { name: "Bound Link" })).toBeVisible();
  } finally {
    await server.stop();
    kataHome.restore();
    await home.close();
    await work.close();
  }
});

test("docs task links resolve distinct task IDs through the folder-bound external daemon", async ({ page }) => {
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
    await page
      .getByRole("search", { name: "Search messages" })
      .getByRole("button", { name: "Search", exact: true })
      .click();
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

    const referencesResponsePromise = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/kata/tasks/references" && url.searchParams.get("q") === "shared";
    });
    await page.keyboard.type("see #shared");

    const referencesResponse = await referencesResponsePromise;
    expect(referencesResponse.status()).toBe(200);
    expect(new URL(referencesResponse.url()).searchParams.get("limit")).toBe("50");
    expect(referencesResponse.request().headers()["x-middleman-kata-daemon"]).toBe("work");

    const tooltip = autocompleteTooltip(page);
    await expect(tooltip).toBeVisible();
    await expect(tooltip).toContainText("Bound daemon completion");
    await expect(tooltip).not.toContainText("Default daemon completion");
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
    name: "Household display name",
    metadata: { area: "Personal", sidebar_order: 1 },
    open_count: 1,
  };
  const personalProject = {
    id: 302,
    uid: "project-personal",
    name: "Personal display name",
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
        qualified_id: "household-identity#rent",
        title: "Pay rent",
        body: "Send rent from checking.",
        labels: ["home"],
      }),
      issueSummary({
        id: 3021,
        uid: "issue-personal-rent",
        project_id: personalProject.id,
        project_uid: personalProject.uid,
        project_name: personalProject.name,
        short_id: "rent",
        qualified_id: "personal-identity#rent",
        title: "Review personal rent budget",
        body: "Confirm the personal rent allocation.",
        labels: ["finance"],
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

    const ambiguousResponsePromise = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/kata/tasks/references" && url.searchParams.get("q") === "r";
    });
    await page.keyboard.type("see #r");
    const ambiguousResponse = await ambiguousResponsePromise;
    expect(ambiguousResponse.status()).toBe(200);
    expect(new URL(ambiguousResponse.url()).searchParams.get("limit")).toBe("50");
    expect(
      (await ambiguousResponse.json()).references.map((reference: { reference: string }) => reference.reference),
    ).toEqual(expect.arrayContaining(["household-identity#rent", "personal-identity#rent"]));

    const tooltip = autocompleteTooltip(page);
    await expect(tooltip).toBeVisible();
    await expect(tooltip).toContainText("household-identity/#rent");
    await expect(tooltip).toContainText("personal-identity/#rent");

    await tooltip.getByRole("option", { name: /household-identity\/#rent/ }).click();
    await expect(editor).toContainText("see household-identity/#rent");

    const saveResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "PUT" &&
        response.url().includes("/api/v1/docs/folders/notes/file") &&
        response.ok(),
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await saveResponsePromise;
    await page.getByRole("link", { name: "household-identity/#rent" }).click();
    await expect(page).toHaveURL(/\/kata\?issue=issue-rent/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText("Send rent from checking.");

    await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(editor).toBeVisible();
    await editor.click();

    await clearEditor(page, editor);
    const noMatchResponsePromise = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/kata/tasks/references" && url.searchParams.get("q") === "zzzzzz";
    });
    await page.keyboard.type("nothing #zzzzzz");
    const noMatchResponse = await noMatchResponsePromise;
    expect(noMatchResponse.status()).toBe(200);
    expect((await noMatchResponse.json()).references).toEqual([]);
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
    await expect(detail.getByRole("button", { name: "Remove label home" })).toHaveCount(0);

    const composer = detail.getByRole("textbox", { name: "Comment" });
    await composer.fill("see #");
    await expect(detail.getByRole("listbox", { name: "Insert reference" })).toBeVisible();
    await composer.press("Enter");
    await expect(composer).toHaveValue("see #Finances#FIN-1 ");

    await composer.fill("see #r");
    await expect(detail.getByRole("listbox", { name: "Insert reference" })).toBeVisible();
    await composer.press("Escape");
    await expect(detail.getByRole("listbox", { name: "Insert reference" })).toHaveCount(0);
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
    await expect.poll(() => backend.state.seenPaths).toContain("POST /api/v1/projects/1/issues/issue-rent/labels");

    await detail.getByRole("button", { name: "Edit labels" }).click();
    await expect(detail.getByRole("button", { name: "Remove label urgent" })).toBeVisible();
    await detail.getByRole("button", { name: "Remove label home" }).click();
    await expect(detail.getByRole("button", { name: "Remove label home" })).toHaveCount(0);
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
  const closedPeer = {
    ...issueSummary({
      id: 34,
      uid: "issue-closed-link",
      project_id: 1,
      project_uid: "project-finance",
      project_name: "Finances",
      short_id: "closed-link",
      qualified_id: "Finances#closed-link",
      title: "Completed linked task",
      body: "Closed peer body.",
      labels: ["home"],
    }),
    status: "closed" as const,
    closed_reason: "done" as const,
    closed_at: now,
  };
  const backend = await startKataBackend({
    issues: [...issues, budget, closedPeer],
    links: [
      linkRow({
        id: 1,
        project_id: 1,
        from: budget,
        to: issues[0]!,
        type: "parent",
      }),
      linkRow({
        id: 2,
        project_id: 1,
        from: issues[0]!,
        to: closedPeer,
        type: "related",
      }),
    ],
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.setViewportSize({ width: 720, height: 540 });
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
    await page.getByRole("button", { name: "Switch to side-by-side layout" }).click();

    const detail = page.getByRole("region", { name: "Task detail" });
    const links = detail.getByRole("region", { name: "Links" });
    await expect(links.getByRole("heading", { name: "Links" })).toBeVisible();
    await expect(links).toContainText("Quarterly budget review with a long title");
    await expect(links).not.toContainText("Completed linked task");
    await expect(links).toContainText("1 / 2");

    const filterTrigger = links.getByRole("button", { name: "Filter links" });
    await filterTrigger.click();
    const filterPanel = page.locator(".link-filter-panel");
    await expect(filterPanel).toBeVisible();
    expect(await filterPanel.evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    expect(Number(await filterPanel.evaluate((element) => getComputedStyle(element).zIndex))).toBeGreaterThan(0);
    const filterTriggerBox = await filterTrigger.boundingBox();
    const filterPanelBox = await filterPanel.boundingBox();
    expect(filterTriggerBox).not.toBeNull();
    expect(filterPanelBox).not.toBeNull();
    expectFloatingPanelAnchored(filterTriggerBox!, filterPanelBox!);
    expect(filterPanelBox!.x).toBeGreaterThanOrEqual(0);
    expect(filterPanelBox!.y).toBeGreaterThanOrEqual(0);
    expect(filterPanelBox!.x + filterPanelBox!.width).toBeLessThanOrEqual(721);
    expect(filterPanelBox!.y + filterPanelBox!.height).toBeLessThanOrEqual(541);

    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 1280, height: 720 });
    await filterTrigger.focus();
    await page.keyboard.press("Enter");
    await expect(filterPanel).toBeVisible();
    await expect
      .poll(async () => {
        const box = await filterPanel.boundingBox();
        return box ? box.x + box.width : Number.POSITIVE_INFINITY;
      })
      .toBeLessThanOrEqual(1281);
    await expect
      .poll(async () => {
        const box = await filterPanel.boundingBox();
        return box ? box.y + box.height : Number.POSITIVE_INFINITY;
      })
      .toBeLessThanOrEqual(721);
    const wideFilterPanelBox = await filterPanel.boundingBox();
    const wideFilterTriggerBox = await filterTrigger.boundingBox();
    expect(wideFilterPanelBox).not.toBeNull();
    expect(wideFilterTriggerBox).not.toBeNull();
    expect(wideFilterPanelBox!.x).toBeGreaterThanOrEqual(0);
    expect(wideFilterPanelBox!.y).toBeGreaterThanOrEqual(0);
    expect(wideFilterPanelBox!.x + wideFilterPanelBox!.width).toBeLessThanOrEqual(1281);
    expect(wideFilterPanelBox!.y + wideFilterPanelBox!.height).toBeLessThanOrEqual(721);
    expectFloatingPanelAnchored(wideFilterTriggerBox!, wideFilterPanelBox!);
    await filterPanel.getByRole("checkbox", { name: "Closed" }).click();

    await expect(links).toContainText("Completed linked task");
    const linkList = links.locator(".link-list");
    await expect(linkList.getByText("Open", { exact: true })).toBeVisible();
    await expect(linkList.getByText("Closed", { exact: true })).toBeVisible();
    const linkTextGaps = await linkList.getByRole("button", { name: /parent\s+budget/ }).evaluate((row) => {
      const textBox = (selector: string): DOMRect => {
        const element = row.querySelector(selector);
        if (!element) throw new Error(`Missing ${selector}`);
        const range = document.createRange();
        range.selectNodeContents(element);
        return range.getBoundingClientRect();
      };
      const kind = textBox(".link-kind");
      const peer = textBox(".link-peer");
      const title = textBox(".link-title");
      return {
        kindToPeer: peer.x - kind.right,
        peerToTitle: title.x - peer.right,
      };
    });
    expect(linkTextGaps.kindToPeer).toBeLessThanOrEqual(16);
    expect(linkTextGaps.peerToTitle).toBeLessThanOrEqual(16);

    await filterPanel.getByRole("checkbox", { name: "Parent" }).click();
    await expect(links).not.toContainText("Quarterly budget review with a long title");
    await expect(links).toContainText("1 / 2");
    await filterPanel.getByRole("checkbox", { name: "Parent" }).click();
    await filterPanel.getByRole("checkbox", { name: "Related" }).click();
    await page.keyboard.press("Escape");

    await links.getByRole("button", { name: /parent\s+budget/ }).click();
    await expect(detail.getByRole("heading", { name: "Quarterly budget review with a long title" })).toBeVisible();
    await expect(page).toHaveURL(/issue=issue-budget/);

    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Pay rent/ })
      .click();
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(page).toHaveURL(/issue=issue-rent/);
    await filterTrigger.focus();
    await page.keyboard.press("Enter");
    await expect(filterPanel.getByRole("checkbox", { name: "Related" })).not.toBeChecked();
    await filterPanel.getByRole("checkbox", { name: "Related" }).click();
    await page.keyboard.press("Escape");
    const relatedIssue = links.getByLabel("Related issue", { exact: true });
    const linkButton = links.getByRole("button", { name: "Link" });
    await expect(relatedIssue).toBeEnabled();
    await relatedIssue.fill("kat-7");
    await expect(linkButton).toBeEnabled();
    await linkButton.click();

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
    await detail.getByRole("combobox", { name: "Owner" }).fill("agent:planner");
    await detail.getByRole("combobox", { name: "Owner" }).press("Enter");
    await expect(detail.getByRole("button", { name: "Owner: agent:planner" })).toContainText("agent:planner");
    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/assign");
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.owner).toBe("agent:planner");

    await detail.getByRole("button", { name: "Owner: agent:planner" }).click();
    await detail.getByRole("combobox", { name: "Owner" }).fill("sus");
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
    await detail.getByRole("combobox", { name: /Priority/ }).click();
    await detail.getByRole("option", { name: "P2" }).click();
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
    const ownerInput = detail.getByRole("combobox", { name: "Owner" });
    await ownerInput.fill("agent:new");
    await ownerInput.press("Enter");

    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/assign");
    await expect(page.locator(".kit-flash-stack").getByRole("status")).toContainText("owner unavailable");
    await expect(detail.getByRole("combobox", { name: "Owner" })).toHaveValue("agent:new");
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

test("kata More actions moves tasks through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(detail.locator(".crumb-project")).toHaveText("Finances");

    await detail.getByRole("button", { name: "More actions" }).click();
    await detail.getByRole("menuitem", { name: "Move to another project" }).click();
    const picker = detail.getByRole("dialog", { name: "Move to another project" });
    const search = picker.getByRole("searchbox", { name: "Find project" });
    await expect(search).toBeFocused();
    await search.fill("kat");
    await picker.getByRole("button", { name: /Kata/ }).click();

    await expect(picker).toHaveCount(0);
    await expect(detail.locator(".crumb-project")).toHaveText("Kata");
    await expect(detail.locator(".crumb-id")).toHaveText("kat-11");
    await expect(page.getByRole("button", { name: /^Finances\s+0$/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Kata\s+2$/ })).toBeVisible();

    const movePath = "POST /api/v1/projects/1/issues/issue-rent/actions/move";
    await expect.poll(() => backend.state.seenPaths.filter((path) => path === movePath).length).toBe(1);
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")).toMatchObject({
      project_id: 2,
      project_uid: "project-kata",
      project_name: "Kata",
      short_id: "kat-11",
      qualified_id: "Kata#kat-11",
      revision: 2,
    });
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata More actions keeps a failed daemon move open and retries successfully", async ({ page }) => {
  const backend = await startKataBackend();
  backend.state.failNextMoveMessage = "move service unavailable";
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await detail.getByRole("button", { name: "More actions" }).click();
    await detail.getByRole("menuitem", { name: "Move to another project" }).click();

    const picker = detail.getByRole("dialog", { name: "Move to another project" });
    const search = picker.getByRole("searchbox", { name: "Find project" });
    await search.fill("kat");
    const destination = picker.getByRole("button", { name: /Kata/ });
    await destination.click();

    await expect(page.locator(".kit-flash-stack").getByRole("status")).toContainText("move service unavailable");
    await expect(picker).toBeVisible();
    await expect(search).toHaveValue("kat");
    await expect(destination).toBeEnabled();
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")).toMatchObject({
      project_uid: "project-finance",
      short_id: "FIN-1",
      revision: 1,
    });

    await destination.click();
    await expect(picker).toHaveCount(0);
    await expect(detail.locator(".crumb-project")).toHaveText("Kata");
    await expect(detail.locator(".crumb-id")).toHaveText("kat-11");
    const movePath = "POST /api/v1/projects/1/issues/issue-rent/actions/move";
    await expect.poll(() => backend.state.seenPaths.filter((path) => path === movePath).length).toBe(2);
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

test("kata complete dialog closes through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
    const detail = page.getByRole("region", { name: "Task detail" });
    const financeCount = page.getByRole("button", { name: /^Finances\s+\d+$/ }).locator(".count");
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

    await expect(detail.getByText("Select a task")).toBeVisible();
    await expect.poll(() => new URL(page.url()).searchParams.get("issue")).toBeNull();
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
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata closed task can be reopened from Logbook through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();

    await detail.getByRole("button", { name: "Complete" }).click();
    const dialog = page.getByRole("dialog", { name: "Complete task" });
    await dialog.getByRole("button", { name: "Complete" }).click();
    await expect(detail.getByText("Select a task")).toBeVisible();
    await expect
      .poll(
        () =>
          backend.state.seenPaths.filter((path) => path === "POST /api/v1/projects/1/issues/issue-rent/actions/close")
            .length,
      )
      .toBe(1);

    await page.getByRole("button", { name: "Logbook" }).click();
    const taskList = page.locator(".kata-list");
    await expect(page.getByRole("heading", { name: "Logbook" })).toBeVisible();
    await taskList.getByRole("button", { name: /Pay rent/ }).click();
    await expect(detail.getByRole("button", { name: "Reopen" })).toBeVisible();

    await detail.getByRole("button", { name: "Reopen" }).click();

    await expect(detail.getByText("Select a task")).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await expect
      .poll(
        () =>
          backend.state.seenPaths.filter((path) => path === "POST /api/v1/projects/1/issues/issue-rent/actions/reopen")
            .length,
      )
      .toBe(1);
    expect(backend.state.issues.find((item) => item.uid === "issue-rent")?.status).toBe("open");
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

    await expect(detail.getByText("Select a task")).toBeVisible();
    await expect.poll(() => new URL(page.url()).searchParams.get("issue")).toBeNull();
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
    const kataCount = page.getByRole("button", { name: /^Kata\s+\d+$/ }).locator(".count");
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

    await expect(detail.getByText("Select a task")).toBeVisible();
    await expect.poll(() => new URL(page.url()).searchParams.get("issue")).toBeNull();
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
  let holdReplacement = false;
  let releaseReplacement!: () => void;
  const replacementBarrier = new Promise<void>((resolve) => {
    releaseReplacement = resolve;
  });

  await page.route("**/api/v1/kata/tasks/snapshot*", async (route) => {
    const url = new URL(route.request().url());
    if (holdReplacement && url.searchParams.get("selected_issue_uid") === "issue-rent") {
      await replacementBarrier;
    }
    await route.continue();
  });

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);

    const detail = page.getByRole("region", { name: "Task detail" });
    const existing = detail.getByRole("checkbox", { name: "Send Zelle" });
    const checklistInput = detail.getByLabel("New checklist item");
    await expect(existing).toBeVisible();
    await expect(existing).not.toBeChecked();

    holdReplacement = true;
    await existing.click();
    await expect(existing).toBeChecked();
    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");
    const refreshStatus = page.getByRole("status").filter({ hasText: "Change saved. Refreshing Kata snapshot" });
    await expect(refreshStatus).toBeVisible();
    await expect(checklistInput).toBeDisabled();
    await expect(detail).toHaveJSProperty("inert", true);

    releaseReplacement();
    await expect(refreshStatus).toHaveCount(0);
    await expect(checklistInput).toBeEnabled();
    await expect(detail).toHaveJSProperty("inert", false);

    await checklistInput.fill("Archive receipt");
    await checklistInput.press("Enter");
    await expect(detail.getByRole("checkbox", { name: "Archive receipt" })).toBeVisible();

    await detail.getByRole("button", { name: "Remove Send Zelle" }).click();
    await expect(detail.getByRole("checkbox", { name: "Send Zelle" })).toHaveCount(0);
    await detail.getByRole("button", { name: "Remove Archive receipt" }).click();
    await expect(detail.getByRole("checkbox")).toHaveCount(0);
    await expect(detail.getByLabel("New checklist item")).toBeVisible();
  } finally {
    releaseReplacement();
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
    await page
      .locator(".kata-list")
      .getByRole("button", { name: /Email Susan re: Q3/ })
      .click();
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata metadata mutation uses the accepted snapshot ETag and waits for replacement authority", async ({ page }) => {
  const selectedIssue = { ...issues[0]!, revision: 73 };
  const backend = await startKataBackend({
    issues: [selectedIssue, issues[1]!],
    publishMutationEvents: false,
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  let holdReplacement = false;
  let releaseReplacement!: () => void;
  const replacementBarrier = new Promise<void>((resolve) => {
    releaseReplacement = resolve;
  });
  const selectedSnapshotRequests: string[] = [];

  await page.route("**/api/v1/kata/tasks/snapshot*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("selected_issue_uid") === selectedIssue.uid) {
      selectedSnapshotRequests.push(url.search);
      if (holdReplacement) await replacementBarrier;
    }
    await route.continue();
  });

  try {
    await page.goto(`${server.info.base_url}/kata?issue=${selectedIssue.uid}`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(detail.getByRole("button", { name: "Edit scheduled" })).toContainText("Scheduled");
    await expect.poll(() => backend.state.streams.size).toBeGreaterThan(0);
    const snapshotsBeforeMutation = selectedSnapshotRequests.length;

    backend.state.nextMutationResponseIssue = {
      ...selectedIssue,
      title: "Mutation response impostor",
      revision: 999,
      metadata: { ...selectedIssue.metadata, scheduled_on: "2099-12-31" },
    };
    holdReplacement = true;
    await detail.getByRole("button", { name: "Edit scheduled" }).click();
    await detail.getByRole("button", { name: "Clear scheduled" }).click();

    await expect.poll(() => backend.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-rent/metadata");
    expect(backend.state.seenIfMatches).toContain('"rev-73"');
    await expect(detail.getByRole("heading", { name: "Mutation response impostor" })).toHaveCount(0);
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(detail.getByRole("group", { name: "Edit scheduled" })).toBeVisible();
    await expect(detail.getByRole("button", { name: "Scheduled: Pick date" })).toBeDisabled();

    emitDaemonChange(
      backend.state,
      eventRow({
        event_id: 1,
        event_uid: "event-metadata-replacement",
        type: "issue.metadata_updated",
        project_id: selectedIssue.project_id,
        project_uid: selectedIssue.project_uid,
        project_name: selectedIssue.project_name,
        issue: selectedIssue,
      }),
    );
    await expect.poll(() => selectedSnapshotRequests.length).toBeGreaterThan(snapshotsBeforeMutation);
    await expect(detail.getByRole("heading", { name: "Mutation response impostor" })).toHaveCount(0);
    await expect(detail.getByRole("group", { name: "Edit scheduled" })).toBeVisible();
    await expect(detail.getByRole("button", { name: "Scheduled: Pick date" })).toBeDisabled();

    releaseReplacement();
    await expect(detail.getByRole("button", { name: "Edit scheduled" })).toContainText("When");
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
  } finally {
    releaseReplacement();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata successful mutation fences stale actions and retries only snapshot replacement", async ({ page }) => {
  const backend = await startKataBackend({ publishMutationEvents: false });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  let failReplacement = false;
  let failedReplacement = false;
  const selectedSnapshotIntents: string[] = [];

  await page.route("**/api/v1/kata/tasks/snapshot*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("selected_issue_uid") === issues[0]!.uid) {
      selectedSnapshotIntents.push(url.search);
      if (failReplacement && !failedReplacement) {
        failedReplacement = true;
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: "replacement unavailable" }),
        });
        return;
      }
    }
    await route.continue();
  });

  try {
    await page.goto(`${server.info.base_url}/kata?issue=${issues[0]!.uid}`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(detail.getByRole("heading", { name: issues[0]!.title })).toBeVisible();
    failReplacement = true;
    await detail.getByRole("button", { name: "Edit scheduled" }).click();
    await detail.getByRole("button", { name: "Clear scheduled" }).click();

    await expect(page.getByRole("alert")).toContainText("Change saved, but Kata snapshot refresh failed");
    await expect(page.getByRole("button", { name: "New task" })).toBeDisabled();
    expect(
      backend.state.seenPaths.filter((path) => path === "PUT /api/v1/projects/1/issues/issue-rent/metadata"),
    ).toHaveLength(1);

    await page.getByRole("button", { name: "Retry Kata snapshot" }).click();
    await expect(detail.getByRole("button", { name: "Edit scheduled" })).toContainText("When");
    await expect(page.getByRole("button", { name: "New task" })).toBeEnabled();
    expect(selectedSnapshotIntents.at(-1)).toBe(selectedSnapshotIntents.at(-2));
    expect(
      backend.state.seenPaths.filter((path) => path === "PUT /api/v1/projects/1/issues/issue-rent/metadata"),
    ).toHaveLength(1);
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});

test("kata close mutation changes open membership only after replacement snapshot acceptance", async ({ page }) => {
  const selectedIssue = { ...issues[0]!, revision: 19 };
  const backend = await startKataBackend({
    issues: [selectedIssue, issues[1]!],
    publishMutationEvents: false,
  });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();
  let holdReplacement = false;
  let releaseReplacement!: () => void;
  const replacementBarrier = new Promise<void>((resolve) => {
    releaseReplacement = resolve;
  });
  const selectedSnapshotRequests: string[] = [];

  await page.route("**/api/v1/kata/tasks/snapshot*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("selected_issue_uid") === selectedIssue.uid) {
      selectedSnapshotRequests.push(url.search);
      if (holdReplacement) await replacementBarrier;
    }
    await route.continue();
  });

  try {
    await page.goto(`${server.info.base_url}/kata?issue=${selectedIssue.uid}`);
    const taskList = page.locator(".kata-list");
    const detail = page.getByRole("region", { name: "Task detail" });
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect.poll(() => backend.state.streams.size).toBeGreaterThan(0);
    const snapshotsBeforeMutation = selectedSnapshotRequests.length;

    backend.state.nextMutationResponseIssue = {
      ...selectedIssue,
      title: "Closed response impostor",
      revision: 999,
      status: "open",
    };
    holdReplacement = true;
    await detail.getByRole("button", { name: "Complete" }).click();
    const dialog = page.getByRole("dialog", { name: "Complete task" });
    await dialog.getByRole("button", { name: "Complete" }).click();

    await expect
      .poll(() => backend.state.seenPaths)
      .toContain("POST /api/v1/projects/1/issues/issue-rent/actions/close");
    await expect(detail.getByRole("heading", { name: "Closed response impostor" })).toHaveCount(0);
    await expect(detail.getByRole("heading", { name: "Pay rent" })).toBeVisible();
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();
    await expect(page).toHaveURL(/issue=issue-rent/);

    emitDaemonChange(
      backend.state,
      eventRow({
        event_id: 1,
        event_uid: "event-close-replacement",
        type: "issue.closed",
        project_id: selectedIssue.project_id,
        project_uid: selectedIssue.project_uid,
        project_name: selectedIssue.project_name,
        issue: selectedIssue,
      }),
    );
    await expect.poll(() => selectedSnapshotRequests.length).toBeGreaterThan(snapshotsBeforeMutation);
    await expect(detail.getByRole("heading", { name: "Closed response impostor" })).toHaveCount(0);
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toBeVisible();

    await expect
      .poll(async () => {
        const response = await page.request.get(
          `${server.info.base_url}/api/v1/kata/tasks/snapshot?scope=global&authority=open`,
          { headers: { "X-Middleman-Kata-Daemon": "e2e" } },
        );
        const snapshot = (await response.json()) as { issues: Array<{ uid: string }> };
        return snapshot.issues.some((issue) => issue.uid === selectedIssue.uid);
      })
      .toBe(false);

    const acceptedReplacement = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        url.pathname === "/api/v1/kata/tasks/snapshot" &&
        url.searchParams.get("selected_issue_uid") === selectedIssue.uid &&
        response.status() === 200
      );
    });
    releaseReplacement();
    const replacement = (await (await acceptedReplacement).json()) as { member_issue_uids: string[] };
    expect(replacement.member_issue_uids).not.toContain(selectedIssue.uid);
    await expect(taskList.getByRole("button", { name: /Pay rent/ })).toHaveCount(0);
    await expect(page).not.toHaveURL(/issue=/);
    await expect(detail).toContainText("Select a task");
  } finally {
    releaseReplacement();
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});
