import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { once } from "node:events";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { expect, test, type Locator, type Page } from "@playwright/test";
import { createDocsFixture } from "./support/docsFixture";
import { startIsolatedE2EServerWithOptions } from "./support/e2eServer";

async function startIsolatedE2EServer() {
  return startIsolatedE2EServerWithOptions({ visibleImportedModes: true });
}

type BackendState = {
  available: boolean;
  authorized: boolean;
  sanitizeFailedIDs: Set<number>;
  statsAuthHeaders: string[];
  aggregateQueries: string[];
  searchQueries: string[];
  threadQueries: number[];
};

type BackendHandle = {
  state: BackendState;
  url: string;
  close: () => Promise<void>;
};

type KataBackendState = {
  issues: KataIssueSummary[];
  metadataPatches: Record<string, unknown>[];
  seenIfMatches: string[];
  seenPaths: string[];
  streams: Set<ServerResponse>;
};

type KataBackendHandle = {
  state: KataBackendState;
  url: string;
  close: () => Promise<void>;
};

const kataNow = "2026-05-15T10:00:00Z";
const middlemanCSRFHeader = { "X-Middleman-Csrf": "1" };
const kataProjects = [
  {
    id: 1,
    uid: "project-messages",
    name: "Messages",
    metadata: { area: "Work", sidebar_order: 1 },
    open_count: 2,
  },
];

const kataIssues = [
  kataIssueSummary({
    id: 42,
    uid: "issue-q3",
    project_id: 1,
    project_uid: "project-messages",
    project_name: "Messages",
    short_id: "kat-7",
    qualified_id: "Kata#kat-7",
    title: "Email Susan re: Q3",
    body: "Confirm the Q3 project review agenda.",
    labels: ["work"],
  }),
  kataIssueSummary({
    id: 43,
    uid: "issue-rent",
    project_id: 1,
    project_uid: "project-messages",
    project_name: "Messages",
    short_id: "kat-8",
    qualified_id: "Kata#kat-8",
    title: "Pay rent",
    body: "Send June rent from checking.",
    labels: ["home"],
  }),
];

type KataIssueSummary = ReturnType<typeof kataIssueSummary>;

async function startMsgvaultBackend(): Promise<BackendHandle> {
  const state: BackendState = {
    available: true,
    authorized: false,
    sanitizeFailedIDs: new Set(),
    aggregateQueries: [],
    statsAuthHeaders: [],
    searchQueries: [],
    threadQueries: [],
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

function kataIssueSummary(input: {
  id: number;
  uid: string;
  project_id: number;
  project_uid: string;
  project_name: string;
  short_id: string;
  qualified_id: string;
  title: string;
  body: string;
  labels: string[];
  metadata?: Record<string, unknown> | undefined;
}) {
  return {
    ...input,
    status: "open",
    metadata: input.metadata ?? {},
    revision: 1,
    author: "e2e",
    created_at: kataNow,
    updated_at: kataNow,
  };
}

async function startKataBackend(): Promise<KataBackendHandle> {
  const rows = kataIssues.map((issue) => ({ ...issue, labels: [...issue.labels], metadata: { ...issue.metadata } }));
  const state: KataBackendState = {
    issues: rows,
    metadataPatches: [],
    seenIfMatches: [],
    seenPaths: [],
    streams: new Set(),
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

async function handleKataRequest(state: KataBackendState, req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  state.seenPaths.push(`${req.method ?? "GET"} ${url.pathname}${url.search}`);

  const recurrencesRoute = /^\/api\/v1\/projects\/(\d+)\/recurrences$/.exec(url.pathname);
  if (recurrencesRoute) {
    writeJSON(res, 200, {
      recurrences: [],
      fetched_at: kataNow,
    });
    return;
  }

  const metadataRoute = /^\/api\/v1\/projects\/(\d+)\/issues\/([^/]+)\/metadata$/.exec(url.pathname);
  if (metadataRoute) {
    await handleKataMetadataMutation(state, req, res, {
      projectID: Number(metadataRoute[1]),
      ref: decodeURIComponent(metadataRoute[2] ?? ""),
    });
    return;
  }

  const issueDetail = /^\/api\/v1\/issues\/([^/]+)$/.exec(url.pathname);
  if (issueDetail) {
    writeKataIssueDetail(state, res, decodeURIComponent(issueDetail[1] ?? ""));
    return;
  }

  if (url.pathname === "/api/v1/events/stream") {
    res.writeHead(200, {
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "Content-Type": "text/event-stream",
    });
    res.write(": connected\n\n");
    state.streams.add(res);
    req.on("close", () => {
      state.streams.delete(res);
    });
    return;
  }

  switch (url.pathname) {
    case "/api/v1/instance":
      writeJSON(res, 200, {
        instance_uid: "kata-messages-e2e",
        version: "0.0.0-e2e",
        schema_version: 1,
      });
      return;
    case "/api/v1/projects":
      writeJSON(res, 200, {
        projects: kataProjects,
        fetched_at: kataNow,
      });
      return;
    case "/api/v1/issues":
      writeJSON(res, 200, {
        issues: filterKataIssues(
          state.issues,
          url.searchParams.get("query") ?? url.searchParams.get("q") ?? "",
          url.searchParams.get("status"),
        ),
        fetched_at: kataNow,
      });
      return;
    case "/api/v1/events":
      writeJSON(res, 200, {
        reset_required: false,
        events: [],
        next_after_id: 0,
      });
      return;
    default:
      writeJSON(res, 404, { error: "not_found", message: url.pathname });
  }
}

function filterKataIssues(
  issues: KataIssueSummary[],
  rawQuery: string,
  status: string | null = null,
): KataIssueSummary[] {
  const query = rawQuery.trim().toLowerCase();
  const statusFiltered =
    status === "open" || status === "closed" ? issues.filter((issue) => issue.status === status) : issues;
  if (!query) return statusFiltered;
  return statusFiltered.filter(
    (issue) =>
      issue.title.toLowerCase().includes(query) ||
      issue.short_id.toLowerCase().includes(query) ||
      issue.qualified_id.toLowerCase().includes(query),
  );
}

async function handleKataMetadataMutation(
  state: KataBackendState,
  req: IncomingMessage,
  res: ServerResponse,
  route: { projectID: number; ref: string },
): Promise<void> {
  const found = state.issues.find((issue) => issue.project_id === route.projectID && issue.uid === route.ref);
  if (!found) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }
  if (req.method !== "PUT") {
    writeJSON(res, 405, { error: "method_not_allowed" });
    return;
  }

  state.seenIfMatches.push(req.headers["if-match"]?.toString() ?? "");
  const payload = await readJSONBody(req);
  const patch = isRecord(payload.patch) ? payload.patch : {};
  state.metadataPatches.push(patch);
  found.metadata = { ...found.metadata, ...patch };
  found.revision += 1;
  res.setHeader("ETag", `"rev-${found.revision}"`);
  writeJSON(res, 200, { changed: true, issue: found });
}

function writeKataIssueDetail(state: KataBackendState, res: ServerResponse, uid: string): void {
  const found = state.issues.find((issue) => issue.uid === uid);
  if (!found) {
    writeJSON(res, 404, { error: "not_found" });
    return;
  }
  res.setHeader("ETag", `"rev-${found.revision}"`);
  writeJSON(res, 200, {
    issue: found,
    comments: [],
    labels: found.labels.map((label) => ({ issue_id: found.id, label, author: "e2e", created_at: kataNow })),
    links: [],
    children: [],
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

async function configureKataHome(backendURL: string): Promise<{ restore: () => void }> {
  const home = await mkdtemp(path.join(os.tmpdir(), "middleman-kata-messages-e2e-"));
  await mkdir(home, { recursive: true });
  await writeFile(
    path.join(home, "config.toml"),
    ['active_daemon = "e2e"', "", "[[daemon]]", 'name = "e2e"', `url = "${backendURL}"`, ""].join("\n"),
  );

  const previous = process.env.KATA_HOME;
  process.env.KATA_HOME = home;
  return {
    restore: () => {
      if (previous === undefined) {
        delete process.env.KATA_HOME;
      } else {
        process.env.KATA_HOME = previous;
      }
    },
  };
}

async function configureEmptyKataHome(): Promise<{ restore: () => void }> {
  const home = await mkdtemp(path.join(os.tmpdir(), "middleman-kata-empty-e2e-"));
  await mkdir(home, { recursive: true });

  const previous = process.env.KATA_HOME;
  process.env.KATA_HOME = home;
  return {
    restore: () => {
      if (previous === undefined) {
        delete process.env.KATA_HOME;
      } else {
        process.env.KATA_HOME = previous;
      }
    },
  };
}

async function configureIsolatedSavedSearches(prefix: string): Promise<{ restore: () => void }> {
  const previous = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  const dir = await mkdtemp(path.join(os.tmpdir(), `middleman-messages-${prefix}-e2e-`));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(dir, "saved-searches.toml");
  return {
    restore: () => {
      if (previous === undefined) {
        delete process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
      } else {
        process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = previous;
      }
    },
  };
}

function handleMsgvaultRequest(state: BackendState, req: IncomingMessage, res: ServerResponse): void {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  switch (url.pathname) {
    case "/health":
      if (!state.available) {
        writeJSON(res, 503, { status: "down" });
        return;
      }
      writeJSON(res, 200, { status: "ok" });
      return;
    case "/api/v1/stats":
      state.statsAuthHeaders.push(req.headers["authorization"] ?? "");
      if (!state.authorized) {
        writeJSON(res, 401, { error: "unauthorized", message: "bad key" });
        return;
      }
      writeJSON(res, 200, { total_messages: 1 });
      return;
    case "/api/v1/search":
      {
        const query = url.searchParams.get("q") ?? "";
        const messages = filterMessages(query);
        state.searchQueries.push(query);
        writeJSON(res, 200, {
          query,
          total: messages.length,
          page: 1,
          page_size: 20,
          messages,
        });
      }
      return;
    case "/api/v1/messages/filter":
      {
        const conversationID = Number(url.searchParams.get("conversation_id") ?? "0");
        state.threadQueries.push(conversationID);
        writeJSON(res, 200, {
          messages: messageCorpus.filter((message) => message.conversation_id === conversationID),
        });
      }
      return;
    case "/api/v1/aggregates":
      {
        const query = url.searchParams.get("search_query") ?? "";
        state.aggregateQueries.push(query);
        writeJSON(res, 200, aggregateResponse(url.searchParams.get("view_type") ?? "senders", filterMessages(query)));
      }
      return;
    default:
      {
        const messageMatch = /^\/api\/v1\/messages\/(\d+)$/.exec(url.pathname);
        if (messageMatch) {
          const id = Number(messageMatch[1]);
          const found = messageCorpus.find((message) => message.id === id);
          if (!found) {
            writeJSON(res, 404, { error: "not_found", message: url.pathname });
            return;
          }
          writeJSON(res, 200, {
            ...found,
            body: detailBody(id),
            body_html: detailHTML(state, id),
            attachments: [],
          });
          return;
        }
      }
      writeJSON(res, 404, { error: "not_found", message: url.pathname });
  }
}

function messageSummary(overrides: Partial<ReturnType<typeof baseMessageSummary>> = {}) {
  return {
    ...baseMessageSummary(),
    ...overrides,
  };
}

function baseMessageSummary() {
  return {
    id: 101,
    conversation_id: 501,
    subject: "Project sync",
    from: "sender-primary@example.com",
    to: ["recipient-review@example.com"],
    cc: [],
    bcc: [],
    sent_at: "2026-05-15T10:00:00Z",
    snippet: "Deploy details are ready.",
    labels: ["work"],
    has_attachments: false,
    size_bytes: 2048,
    deleted_at: null,
  };
}

const messageCorpus = [
  messageSummary(),
  messageSummary({
    id: 102,
    conversation_id: 501,
    subject: "Deploy follow-up",
    from: "sender-followup@example.com",
    snippet: "Following up on release details.",
    sent_at: "2026-05-15T10:05:00Z",
    has_attachments: true,
  }),
  messageSummary({
    id: 103,
    conversation_id: 501,
    subject: "Project sync wrap-up",
    from: "sender-primary@example.com",
    snippet: "Final project sync notes are ready.",
    sent_at: "2026-05-15T10:10:00Z",
  }),
  messageSummary({
    id: 42,
    conversation_id: 700,
    subject: "Weekly status update",
    from: "sender-weekly@example.com",
    snippet: "Weekly project status is ready.",
    labels: ["Inbox", "weekly"],
    sent_at: "2026-05-14T09:00:00Z",
  }),
  messageSummary({
    id: 43,
    conversation_id: 700,
    subject: "Re: Weekly status update",
    from: "sender-primary@example.com",
    snippet: "Thanks for the weekly notes.",
    labels: ["weekly"],
    sent_at: "2026-05-14T09:15:00Z",
  }),
  messageSummary({
    id: 900,
    conversation_id: 900,
    subject: "Solo update",
    from: "sender-solo@example.com",
    snippet: "Solo status is ready.",
    labels: ["solo"],
    sent_at: "2026-05-13T08:00:00Z",
  }),
  messageSummary({
    id: 901,
    conversation_id: 901,
    subject: "Archived launch plan",
    from: "sender-primary@example.com",
    snippet: "Old launch notes.",
    labels: ["archive"],
    sent_at: "2026-03-01T08:00:00Z",
  }),
  messageSummary({
    id: 902,
    conversation_id: 902,
    subject: "External vendor update",
    from: "vendor@external.test",
    to: ["sender-primary@example.com"],
    snippet: "External vendor notes.",
    labels: ["vendor"],
    sent_at: "2026-05-15T11:00:00Z",
  }),
];

function detailBody(id: number): string {
  if (id === 101) return "Deploy details are ready for the project sync.";
  if (id === 102) return "Follow-up deploy details are ready.";
  if (id === 103) return "Final project sync notes are ready.";
  if (id === 42) return "Weekly project status body.";
  if (id === 43) return "Weekly reply body.";
  if (id === 900) return "Solo update body.";
  return `Body for message ${id}.`;
}

function detailHTML(state: BackendState, id: number): string {
  if (state.sanitizeFailedIDs.has(id)) {
    return Array.from({ length: 257 }, (_, idx) => `<img src="https://images.example.test/${idx}.png">`).join("");
  }
  if (id === 101) {
    return [
      "<p>HTML body for message 101.</p>",
      '<p><a href="https://example.com/messages/101">example.com</a></p>',
      '<img src="cid:logo" alt="logo">',
      '<img src="https://images.example.test/project-sync.png" alt="project sync">',
    ].join("");
  }
  return "";
}

const queryOperators = new Set(["from", "label", "has", "after", "before", "domain", "newer_than"]);
const relativeDatePattern = /^(\d+)([dwmy])$/;
const messagesFixtureNow = new Date("2026-05-16T00:00:00.000Z");

function domainOf(email: string): string {
  const at = email.indexOf("@");
  return at >= 0 ? email.slice(at + 1) : email;
}

function parseRelativeCutoff(value: string): Date | null {
  const match = relativeDatePattern.exec(value.trim().toLowerCase());
  if (!match) return null;
  const amount = Number(match[1]);
  if (!Number.isFinite(amount)) return null;
  const result = new Date(messagesFixtureNow.getTime());
  switch (match[2]) {
    case "d":
      result.setUTCDate(result.getUTCDate() - amount);
      break;
    case "w":
      result.setUTCDate(result.getUTCDate() - amount * 7);
      break;
    case "m":
      result.setUTCMonth(result.getUTCMonth() - amount);
      break;
    case "y":
      result.setUTCFullYear(result.getUTCFullYear() - amount);
      break;
    default:
      return null;
  }
  return Number.isFinite(result.getTime()) ? result : null;
}

function messageMatchesToken(message: ReturnType<typeof messageSummary>, rawToken: string): boolean {
  const colon = rawToken.indexOf(":");
  const op = colon > 0 ? rawToken.slice(0, colon).toLowerCase() : "";
  const value = colon > 0 ? rawToken.slice(colon + 1) : rawToken;
  if (!queryOperators.has(op)) {
    const token = rawToken.toLowerCase();
    return [message.subject, message.snippet, message.from, message.labels.join(" ")]
      .join(" ")
      .toLowerCase()
      .includes(token);
  }

  switch (op) {
    case "from":
      return message.from.toLowerCase().includes(value.toLowerCase());
    case "label":
      return message.labels.some((item) => item.toLowerCase() === value.toLowerCase());
    case "has":
      return value.toLowerCase() === "attachment" ? message.has_attachments : true;
    case "after":
      return message.sent_at >= value;
    case "before":
      return message.sent_at < `${value}T00:00:00Z`;
    case "domain":
      return domainOf(message.from).toLowerCase() === value.toLowerCase();
    case "newer_than": {
      const cutoff = parseRelativeCutoff(value);
      if (cutoff === null) return true;
      return message.sent_at >= cutoff.toISOString();
    }
    default:
      return false;
  }
}

function filterMessages(query: string) {
  const tokens = query.trim().split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return messageCorpus;
  return messageCorpus.filter((message) => tokens.every((token) => messageMatchesToken(message, token)));
}

function aggregateResponse(viewType: string, messages: ReturnType<typeof messageSummary>[]) {
  const keyOf = (message: ReturnType<typeof messageSummary>) => {
    if (viewType === "labels") return message.labels[0] ?? "";
    if (viewType === "domains") return domainOf(message.from);
    return message.from;
  };
  const counts = new Map<string, number>();
  for (const message of messages) {
    const key = keyOf(message);
    if (!key) continue;
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return {
    view_type: viewType,
    rows: [...counts.entries()].map(([key, count]) => ({
      key,
      count,
      total_size: count * 2048,
      attachment_count: 0,
      attachment_size: 0,
    })),
  };
}

function writeJSON(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

function messageRow(page: Page, id: number) {
  return page.locator(`button[data-id="${id}"]`);
}

async function closeServer(server: Server): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    server.close((err) => {
      if (err) reject(err);
      else resolve();
    });
  });
}

type ConfiguredMessagesFixture = {
  backend: BackendHandle;
  envName: string;
  previousEnv: string | undefined;
  previousSavedSearchesPath: string | undefined;
  server: Awaited<ReturnType<typeof startIsolatedE2EServer>>;
  stop: () => Promise<void>;
};

async function startConfiguredMessagesFixture(page: Page, prefix: string): Promise<ConfiguredMessagesFixture> {
  const backend = await startMsgvaultBackend();
  backend.state.authorized = true;
  const envName = `MSGVAULT_E2E_KEY_${prefix.toUpperCase()}_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), `middleman-messages-${prefix}-e2e-`));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  let server: Awaited<ReturnType<typeof startIsolatedE2EServer>> | undefined;
  let stopped = false;

  async function stop(): Promise<void> {
    if (stopped) return;
    stopped = true;
    try {
      if (server) await server.stop();
    } finally {
      try {
        await backend.close();
      } finally {
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
    }
  }

  try {
    server = await startIsolatedE2EServer();
    const res = await page.request.post(`${server.info.base_url}/api/v1/msgvault/configure`, {
      headers: middlemanCSRFHeader,
      data: { url: backend.url, api_key_env: envName },
    });
    expect(res.status()).toBe(200);

    return {
      backend,
      envName,
      previousEnv,
      previousSavedSearchesPath,
      server,
      stop,
    };
  } catch (error) {
    await stop();
    throw error;
  }
}

async function startConfiguredMessagesAndKataFixture(
  page: Page,
  prefix: string,
  seedKata?: (kata: KataBackendHandle) => void,
): Promise<ConfiguredMessagesFixture> {
  const kata = await startKataBackend();
  let kataHome: Awaited<ReturnType<typeof configureKataHome>> | undefined;
  let fixture: ConfiguredMessagesFixture | undefined;

  try {
    seedKata?.(kata);
    kataHome = await configureKataHome(kata.url);
    fixture = await startConfiguredMessagesFixture(page, prefix);
    return {
      ...fixture,
      stop: async () => {
        try {
          await fixture?.stop();
        } finally {
          try {
            kataHome?.restore();
          } finally {
            await kata.close();
          }
        }
      },
    };
  } catch (error) {
    try {
      await fixture?.stop();
    } finally {
      try {
        kataHome?.restore();
      } finally {
        await kata.close();
      }
    }
    throw error;
  }
}

function savedViewsNav(page: Page) {
  return page.getByRole("navigation", { name: "Messages saved views" });
}

function appHeaderTab(page: Page, name: string) {
  return page.locator(".tab-group").getByRole("button", { name, exact: true });
}

async function openDocsEditor(page: Page): Promise<Locator> {
  const editButton = page.getByRole("button", { name: "Edit", exact: true });
  await expect(editButton).toBeEnabled();
  await editButton.click();
  const editor = page.locator(".cm-editor .cm-content");
  await expect(editor).toBeVisible();
  await editor.click();
  return editor;
}

async function replaceEditorText(page: Page, editor: Locator, value: string): Promise<void> {
  await editor.focus();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.press("Delete");
  await page.keyboard.insertText(value);
}

async function expectMessagesWorkspaceUnavailable(page: Page): Promise<void> {
  await expect(page.getByPlaceholder("Search messages...")).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "Messages facets" })).toHaveCount(0);
  await expect(page.locator(".messages-list")).toHaveCount(0);
}

async function expectMessagesTabOpens(page: Page): Promise<void> {
  await expect(appHeaderTab(page, "Messages")).toBeVisible();
  await appHeaderTab(page, "Messages").click();
  await expect(page).toHaveURL(/\/messages/);
  await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();
}

function waitForSavedSearchesPUT(page: Page) {
  return page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/messages/saved-searches" &&
      response.request().method() === "PUT" &&
      response.status() === 200,
  );
}

async function openConfiguredProjectMessage(page: Page, fixture: ConfiguredMessagesFixture): Promise<void> {
  await page.goto(`${fixture.server.info.base_url}/messages?q=project&message=101`);
  await expect(page.getByRole("heading", { name: "Project sync", exact: true })).toBeVisible({ timeout: 8_000 });
}

test("messages setup and search flow uses middleman against a controlled backend", async ({ page }) => {
  const backend = await startMsgvaultBackend();
  const envName = `MSGVAULT_E2E_KEY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), "middleman-messages-e2e-"));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  const server = await startIsolatedE2EServer();
  try {
    await page.goto(`${server.info.base_url}/messages`);

    await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();
    await expect(page.getByText("Messages are not set up.")).toBeVisible();

    await page.getByRole("button", { name: "Set up Messages" }).click();
    await page.getByLabel("Message source URL").fill(backend.url);
    await page.getByLabel("API key env var name").fill(envName);
    await page.getByRole("button", { name: "Save" }).click();

    await expect(page.getByText("Messages key rejected - check `api_key_env`.")).toBeVisible();
    expect(backend.state.statsAuthHeaders).toContain("Bearer secret-key");

    backend.state.authorized = true;
    await page.getByRole("button", { name: "Configure messages" }).click();
    await page.getByRole("button", { name: "Save" }).click();

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible();
    await expect(searchBox).toBeFocused();
    await searchBox.fill("project");
    await page.getByRole("search", { name: "Search messages" }).getByRole("button", { name: "Search" }).click();

    await expect(page).toHaveURL(/\/messages\?q=project$/);
    await expect(messageRow(page, 101)).toBeVisible();
    await expect.poll(() => backend.state.searchQueries).toContain("project");

    await messageRow(page, 101).click();

    await expect(page).toHaveURL(/\/messages\?q=project&message=101$/);
    await expect(page.getByRole("heading", { name: "Project sync", exact: true })).toBeVisible();
    const textToggle = page.getByRole("button", { name: "Text", exact: true });
    await expect
      .poll(async () => {
        const toggleCount = await textToggle.count();
        if (toggleCount > 0) return "toggle";
        return (await page.getByRole("region", { name: "Message body" }).textContent()) ?? "";
      })
      .not.toBe("");
    if ((await textToggle.count()) > 0) {
      await textToggle.click();
    }
    await expect(page.getByRole("region", { name: "Message body" })).toContainText(
      "Deploy details are ready for the project sync.",
    );
  } finally {
    await server.stop();
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

test("messages configure API rejects mutation requests without the middleman csrf header", async ({ page }) => {
  const backend = await startMsgvaultBackend();
  const envName = `MSGVAULT_E2E_KEY_NO_CSRF_${Date.now()}`;
  const previousEnv = process.env[envName];
  process.env[envName] = "secret-key";
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/msgvault/configure`, {
      data: { url: backend.url, api_key_env: envName },
    });

    expect(res.status()).toBe(403);
    expect(await res.text()).toContain("missingCsrfHeader");
  } finally {
    await server.stop();
    await backend.close();
    if (previousEnv === undefined) {
      delete process.env[envName];
    } else {
      process.env[envName] = previousEnv;
    }
  }
});

test("messages lazy-load retry recovers after repeated chunk request failures", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "chunkretry");
  let failedChunkRequests = 0;
  await page.route(/\/assets\/MessagesFeature-[^/]+\.js$/, async (route) => {
    if (failedChunkRequests < 2) {
      failedChunkRequests += 1;
      await route.abort();
      return;
    }
    await route.continue();
  });

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=project`);

    const retry = page.getByRole("button", { name: "Retry loading Messages" });
    await expect(retry).toBeVisible({ timeout: 8_000 });
    expect(failedChunkRequests).toBe(1);

    await retry.click();

    await expect(retry).toBeVisible({ timeout: 8_000 });
    expect(failedChunkRequests).toBe(2);

    await retry.click();

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible({ timeout: 8_000 });
    await expect(searchBox).toHaveValue("project");
    await expect(messageRow(page, 101)).toBeVisible();
  } finally {
    await fixture.stop();
  }
});

test("messages setup rejects invalid input in the dialog", async ({ page }) => {
  for (const scenario of [
    {
      name: "unsupported scheme",
      url: "ftp://messages.example.com",
      env: "MSGVAULT_API_KEY",
      error: /url/i,
    },
    {
      name: "empty authority",
      url: "https:///foo",
      env: "MSGVAULT_API_KEY",
      error: /url/i,
    },
    {
      name: "lowercase env var",
      url: "https://messages.example.com",
      env: "msgvault_api_key",
      error: /env var name|A-Z_/i,
    },
  ]) {
    await test.step(scenario.name, async () => {
      const server = await startIsolatedE2EServer();
      try {
        await page.goto(`${server.info.base_url}/messages`);
        await page.getByRole("button", { name: "Set up Messages" }).click();
        const dialog = page.getByRole("dialog", { name: "Set up Messages" });

        await page.getByLabel("Message source URL").fill(scenario.url);
        await page.getByLabel("API key env var name").fill(scenario.env);
        await page.getByRole("button", { name: "Save" }).click();

        await expect(dialog).toBeVisible();
        await expect(dialog.getByRole("alert")).toContainText(scenario.error);
      } finally {
        await server.stop();
      }
    });
  }
});

test.describe("messages HTML viewer", () => {
  test("HTML mode renders a sandboxed iframe with placeholder image handles", async ({ page }) => {
    const fixture = await startConfiguredMessagesFixture(page, "htmlviewer");

    try {
      await openConfiguredProjectMessage(page, fixture);

      const iframe = page.locator("iframe.html-iframe");
      await expect(iframe).toBeVisible();
      await expect(iframe).toHaveAttribute("sandbox", "allow-popups allow-popups-to-escape-sandbox");
      const srcdoc = await iframe.getAttribute("srcdoc");
      expect(srcdoc).toContain("HTML body for message 101.");
      expect(srcdoc).toContain("data-remote-image-idx");
      expect(srcdoc).not.toMatch(/<img\s+src="http/);
      await expect(page.getByRole("status").filter({ hasText: /remote image/i })).toBeVisible();
    } finally {
      await fixture.stop();
    }
  });

  test("Load images swaps iframe srcdoc to remote-image proxy URLs", async ({ page }) => {
    const fixture = await startConfiguredMessagesFixture(page, "htmlimages");

    try {
      await openConfiguredProjectMessage(page, fixture);

      const banner = page.getByRole("status").filter({ hasText: /remote image/i });
      await banner.getByRole("button", { name: /Load images/i }).click();
      await expect(banner).toHaveCount(0);

      const srcdoc = await page.locator("iframe.html-iframe").getAttribute("srcdoc");
      expect(srcdoc).toMatch(/src="\/api\/v1\/msgvault\/messages\/101\/remote-image\/[a-f0-9]{32}\/0"/);
      expect(srcdoc).not.toContain("data-remote-image-idx");
    } finally {
      await fixture.stop();
    }
  });

  test("Text mode hides the iframe and shows the plain body", async ({ page }) => {
    const fixture = await startConfiguredMessagesFixture(page, "htmltext");

    try {
      await openConfiguredProjectMessage(page, fixture);

      await page.getByRole("button", { name: "Text", exact: true }).click();
      await expect(page.locator("pre.msg-body").first()).toBeVisible();
      await expect(page.locator("iframe.html-iframe")).toHaveCount(0);
    } finally {
      await fixture.stop();
    }
  });

  test("sanitization failure falls back to text", async ({ page }) => {
    const fixture = await startConfiguredMessagesFixture(page, "htmlsanitize");

    try {
      fixture.backend.state.sanitizeFailedIDs.add(101);
      await openConfiguredProjectMessage(page, fixture);

      await expect(page.getByRole("alert")).toContainText(/Couldn't render HTML/i);
      await expect(page.locator("iframe.html-iframe")).toHaveCount(0);
      await expect(page.locator("pre.msg-body").first()).toBeVisible();
    } finally {
      await fixture.stop();
    }
  });

  test("links inside the iframe open popups in the same browser context", async ({ page, context }) => {
    const fixture = await startConfiguredMessagesFixture(page, "htmlpopup");

    try {
      await openConfiguredProjectMessage(page, fixture);
      await context.route("https://example.com/**", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "text/html",
          body: "<!doctype html><title>example</title>",
        });
      });

      const frame = page.frameLocator("iframe.html-iframe");
      const [popup] = await Promise.all([
        context.waitForEvent("page"),
        frame.locator("a", { hasText: "example.com" }).click(),
      ]);
      await popup.waitForURL(/example\.com/, { waitUntil: "commit" });
      expect(popup.url()).toMatch(/example\.com/);
      await popup.close();
    } finally {
      await fixture.stop();
    }
  });
});

test("messages header tab stays available across setup and health states", async ({ page }) => {
  {
    const savedSearches = await configureIsolatedSavedSearches("header-unconfigured");
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(server.info.base_url);
      await expectMessagesTabOpens(page);
      await expect(page.getByRole("button", { name: "Set up Messages" })).toBeVisible();
      await expectMessagesWorkspaceUnavailable(page);
    } finally {
      await server.stop();
      savedSearches.restore();
    }
  }

  for (const scenario of [
    {
      name: "misconfigured",
      envValue: undefined,
      configureBackend: (backend: BackendHandle) => {
        backend.state.authorized = true;
      },
      banner: "Messages are misconfigured",
    },
    {
      name: "down",
      envValue: "secret-key",
      configureBackend: (backend: BackendHandle) => {
        backend.state.authorized = true;
        backend.state.available = false;
      },
      banner: "Messages unavailable - retrying on next refresh.",
    },
    {
      name: "unauthorized",
      envValue: "secret-key",
      configureBackend: (backend: BackendHandle) => {
        backend.state.authorized = false;
      },
      banner: "Messages key rejected - check `api_key_env`.",
    },
  ]) {
    const backend = await startMsgvaultBackend();
    scenario.configureBackend(backend);
    const savedSearches = await configureIsolatedSavedSearches(`header-${scenario.name}`);
    const envName = `MSGVAULT_E2E_KEY_HEADER_${scenario.name.toUpperCase()}_${Date.now()}`;
    const previousEnv = process.env[envName];
    if (scenario.envValue === undefined) {
      delete process.env[envName];
    } else {
      process.env[envName] = scenario.envValue;
    }
    const server = await startIsolatedE2EServer();
    try {
      const response = await page.request.post(`${server.info.base_url}/api/v1/msgvault/configure`, {
        headers: middlemanCSRFHeader,
        data: { url: backend.url, api_key_env: envName },
      });
      expect(response.status()).toBe(200);
      await page.goto(server.info.base_url);
      await expectMessagesTabOpens(page);
      await expect(page.getByRole("alert")).toContainText(scenario.banner);
      await expectMessagesWorkspaceUnavailable(page);
    } finally {
      await server.stop();
      await backend.close();
      if (previousEnv === undefined) {
        delete process.env[envName];
      } else {
        process.env[envName] = previousEnv;
      }
      savedSearches.restore();
    }
  }

  {
    const fixture = await startConfiguredMessagesFixture(page, "headerok");
    try {
      await page.goto(fixture.server.info.base_url);
      await expectMessagesTabOpens(page);
      await expect(page.getByPlaceholder("Search messages...")).toBeVisible();
      await expect(page.getByRole("navigation", { name: "Messages facets" })).toBeVisible();
    } finally {
      await fixture.stop();
    }
  }
});

test("messages configured degraded states show banners without workspace controls", async ({ page }) => {
  for (const scenario of [
    {
      name: "down",
      setupBackend: (backend: BackendHandle) => {
        backend.state.authorized = true;
        backend.state.available = false;
      },
      banner: "Messages unavailable - retrying on next refresh.",
    },
    {
      name: "unauthorized",
      setupBackend: (backend: BackendHandle) => {
        backend.state.authorized = false;
      },
      banner: "Messages key rejected - check `api_key_env`.",
    },
  ]) {
    const backend = await startMsgvaultBackend();
    const envName = `MSGVAULT_E2E_KEY_${scenario.name.toUpperCase()}_${Date.now()}`;
    const previousEnv = process.env[envName];
    process.env[envName] = "secret-key";
    const savedSearches = await configureIsolatedSavedSearches(`degraded-${scenario.name}`);
    const server = await startIsolatedE2EServer();
    try {
      scenario.setupBackend(backend);
      const res = await page.request.post(`${server.info.base_url}/api/v1/msgvault/configure`, {
        headers: middlemanCSRFHeader,
        data: { url: backend.url, api_key_env: envName },
      });
      expect(res.status()).toBe(200);

      await page.goto(`${server.info.base_url}/messages`);

      await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();
      await expect(page.getByRole("alert")).toContainText(scenario.banner);
      await expectMessagesWorkspaceUnavailable(page);
    } finally {
      await server.stop();
      await backend.close();
      if (previousEnv === undefined) {
        delete process.env[envName];
      } else {
        process.env[envName] = previousEnv;
      }
      savedSearches.restore();
    }
  }
});

test("messages setup with an unset env var shows misconfigured state and can recover", async ({ page }) => {
  const backend = await startMsgvaultBackend();
  backend.state.authorized = true;
  const envName = `MSGVAULT_E2E_KEY_MISCONFIGURED_${Date.now()}`;
  const recoveryEnvName = `MSGVAULT_E2E_KEY_RECOVERY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousRecoveryEnv = process.env[recoveryEnvName];
  delete process.env[envName];
  process.env[recoveryEnvName] = "secret-key";
  const savedSearches = await configureIsolatedSavedSearches("misconfigured");
  const server = await startIsolatedE2EServer();
  try {
    await page.goto(`${server.info.base_url}/messages`);
    await page.getByRole("button", { name: "Set up Messages" }).click();
    await page.getByLabel("Message source URL").fill(backend.url);
    await page.getByLabel("API key env var name").fill(envName);
    await page.getByRole("button", { name: "Save" }).click();

    await expect(page.getByRole("alert")).toContainText("Messages are misconfigured");
    await expect(page.getByRole("alert")).toContainText(envName);
    await expectMessagesWorkspaceUnavailable(page);

    await page.getByRole("button", { name: "Configure messages" }).click();
    await page.getByLabel("API key env var name").fill(recoveryEnvName);
    await page.getByRole("button", { name: "Save" }).click();

    await expect(page.getByPlaceholder("Search messages...")).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
    expect(backend.state.statsAuthHeaders).toContain("Bearer secret-key");
  } finally {
    await server.stop();
    await backend.close();
    if (previousEnv === undefined) {
      delete process.env[envName];
    } else {
      process.env[envName] = previousEnv;
    }
    if (previousRecoveryEnv === undefined) {
      delete process.env[recoveryEnvName];
    } else {
      process.env[recoveryEnvName] = previousRecoveryEnv;
    }
    savedSearches.restore();
  }
});

test("messages list keyboard navigation selects the next row and updates the route", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "keyboard");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible({ timeout: 8_000 });
    await searchBox.fill("deploy");
    await page.getByRole("search", { name: "Search messages" }).getByRole("button", { name: "Search" }).click();

    const rows = page.locator(".messages-list button.row");
    await expect(rows).toHaveCount(2, { timeout: 8_000 });
    await rows.nth(0).click();
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();

    await rows.nth(0).focus();
    await page.keyboard.press("j");

    await expect(page).toHaveURL(/[?&]q=deploy/);
    await expect(page).toHaveURL(/[?&]message=102/);
    await expect(page.getByRole("heading", { name: "Deploy follow-up" })).toBeVisible();
    await expect(page.getByRole("region", { name: "Message body" })).toContainText(
      "Follow-up deploy details are ready.",
    );
  } finally {
    await fixture.stop();
  }
});

test("messages non-integer route message keeps the list and empty detail", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "badroute");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=deploy&message=not-a-number`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toHaveValue("deploy", { timeout: 8_000 });
    await expect(page.locator(".messages-list button.row")).toHaveCount(2, { timeout: 8_000 });
    await expect(page.getByText("Select a message to read it.")).toBeVisible();
    await expect(page.getByRole("region", { name: "Message body" })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Project sync" })).toHaveCount(0);
  } finally {
    await fixture.stop();
  }
});

test("messages direct route and browser history restore query and selected message", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "route");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=project&message=101`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toHaveValue("project", { timeout: 8_000 });
    await expect(page.getByRole("button", { name: /Project sync/ }).first()).toBeVisible();
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();
    await expect(page).toHaveURL(/[?&]q=project/);
    await expect(page).toHaveURL(/[?&]message=101/);

    await searchBox.fill("weekly");
    await searchBox.press("Enter");
    await expect(page).toHaveURL(/\/messages\?q=weekly$/);
    const weekly = page.getByRole("button", { name: /Weekly status update/ }).first();
    await expect(weekly).toBeVisible();
    await weekly.click();

    await expect(page).toHaveURL(/[?&]q=weekly/);
    await expect(page).toHaveURL(/[?&]message=42/);
    await expect(page.getByRole("heading", { name: "Weekly status update" })).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(/\/messages\?q=weekly$/);
    await expect(searchBox).toHaveValue("weekly");
    await expect(page.getByText("Select a message to read it.")).toBeVisible();

    await page.goForward();
    await expect(page).toHaveURL(/[?&]message=42/);
    await expect(page.getByRole("heading", { name: "Weekly status update" })).toBeVisible();
  } finally {
    await fixture.stop();
  }
});

test("docs and messages keep local state while switching modes", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "modestate");
  const docsRoot = await createDocsFixture();

  try {
    const folder = await page.request.post(`${fixture.server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "notes",
        name: "Notes",
        path: docsRoot,
      },
    });
    expect(folder.status()).toBe(201);

    await page.goto(`${fixture.server.info.base_url}/docs?folder=notes&doc=README.md`);
    await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();
    const editor = await openDocsEditor(page);
    await replaceEditorText(page, editor, "# Unsaved Route State\n\nKeep this edit mounted.\n");
    await expect(editor).toContainText("Keep this edit mounted.");

    await appHeaderTab(page, "Messages").click();
    await expect(page).toHaveURL(/\/messages/);
    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible();
    await searchBox.fill("project");
    await page.getByRole("search", { name: "Search messages" }).getByRole("button", { name: "Search" }).click();
    await expect(messageRow(page, 101)).toBeVisible();

    await appHeaderTab(page, "Docs").click();
    await expect(page).toHaveURL(/\/docs/);
    await expect(page.locator(".cm-editor .cm-content")).toContainText("Keep this edit mounted.");
  } finally {
    await fixture.stop();
  }
});

test("messages browser back restores the selected Kata task", async ({ page }) => {
  const fixture = await startConfiguredMessagesAndKataFixture(page, "kataback");

  try {
    await page.goto(`${fixture.server.info.base_url}/kata?issue=issue-q3`);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
      { timeout: 8_000 },
    );

    await appHeaderTab(page, "Messages").click();
    await expect(page).toHaveURL(/\/messages$/);
    await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(/\/kata\?issue=issue-q3$/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );

    await page.goForward();
    await expect(page).toHaveURL(/\/messages$/);
    await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();
  } finally {
    await fixture.stop();
  }
});

test("messages mode switch back to Kata restores the selected task route", async ({ page }) => {
  const fixture = await startConfiguredMessagesAndKataFixture(page, "katareturn");

  try {
    await page.goto(`${fixture.server.info.base_url}/kata?issue=issue-q3`);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
      { timeout: 8_000 },
    );

    await appHeaderTab(page, "Messages").click();
    await expect(page).toHaveURL(/\/messages$/);
    await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();

    await appHeaderTab(page, "Kata").click();
    await expect(page).toHaveURL(/\/kata\?issue=issue-q3$/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
  } finally {
    await fixture.stop();
  }
});

test("messages facets chain filters and clear the focused message", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "facets");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=project&message=101`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toHaveValue("project", { timeout: 8_000 });
    const facets = page.getByRole("navigation", { name: "Messages facets" });
    const primarySender = facets.getByRole("button", { name: /sender-primary@example\.com/ });
    await expect(primarySender).toBeVisible({ timeout: 8_000 });

    await primarySender.click();
    await expect(searchBox).toHaveValue("project from:sender-primary@example.com");
    await expect(page).toHaveURL(/q=project\+from%3Asender-primary%40example\.com/);
    await expect(page).not.toHaveURL(/message=101/);
    await expect.poll(() => fixture.backend.state.searchQueries).toContain("project from:sender-primary@example.com");

    await primarySender.click();
    await expect(searchBox).toHaveValue("project from:sender-primary@example.com");

    const workLabel = facets.getByRole("button", { name: /work/ });
    await expect(workLabel).toBeVisible({ timeout: 8_000 });
    await workLabel.click();
    await expect(searchBox).toHaveValue("project from:sender-primary@example.com label:work");
    await expect
      .poll(() => fixture.backend.state.searchQueries)
      .toContain("project from:sender-primary@example.com label:work");
  } finally {
    await fixture.stop();
  }
});

test("messages narrow viewport hides facets while keeping search usable", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "narrowfacets");

  try {
    await page.setViewportSize({ width: 800, height: 768 });
    await page.goto(`${fixture.server.info.base_url}/messages?q=project`);

    await expect(page.getByPlaceholder("Search messages...")).toHaveValue("project", { timeout: 8_000 });
    await expect(page.locator(".messages-list")).toBeVisible();
    await expect(page.getByRole("button", { name: /Project sync/ }).first()).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Messages facets" })).toBeHidden();
  } finally {
    await fixture.stop();
  }
});

test("messages quick views apply saved-view queries through the real workspace", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "quickviews");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible({ timeout: 8_000 });
    await expect(page.getByRole("button", { name: /Deploy follow-up/ })).toBeVisible();

    await savedViewsNav(page).getByRole("button", { name: "Inbox" }).click();

    await expect(searchBox).toHaveValue("label:Inbox");
    await expect(page).toHaveURL(/\/messages\?q=label%3AInbox$/);
    await expect(page.getByRole("button", { name: /Weekly status update/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Deploy follow-up/ })).toHaveCount(0);
    await expect.poll(() => fixture.backend.state.searchQueries).toContain("label:Inbox");

    await savedViewsNav(page).getByRole("button", { name: "Has attachments" }).click();
    await expect(searchBox).toHaveValue("has:attachment");
    await expect(page).toHaveURL(/\/messages\?q=has%3Aattachment$/);
    await expect(page.getByRole("button", { name: /Deploy follow-up/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Project sync/ })).toHaveCount(0);
    await expect.poll(() => fixture.backend.state.searchQueries).toContain("has:attachment");

    await savedViewsNav(page).getByRole("button", { name: "Recent" }).click();
    await expect(searchBox).toHaveValue("newer_than:30d");
    await expect(page).toHaveURL(/\/messages\?q=newer_than%3A30d$/);
    await expect(page.getByRole("button", { name: /Deploy follow-up/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Archived launch plan/ })).toHaveCount(0);
    await expect.poll(() => fixture.backend.state.searchQueries).toContain("newer_than:30d");

    const facets = page.getByRole("navigation", { name: "Messages facets" });
    await facets.getByRole("button", { name: /^example\.com\s+\d+$/ }).click();
    await expect(searchBox).toHaveValue("newer_than:30d domain:example.com");
    await expect(messageRow(page, 101)).toBeVisible();
    await expect(page.getByRole("button", { name: /External vendor update/ })).toHaveCount(0);
    await expect.poll(() => fixture.backend.state.searchQueries).toContain("newer_than:30d domain:example.com");
  } finally {
    await fixture.stop();
  }
});

test("messages saved searches can be saved, applied, highlighted, and deleted", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "savedsearches");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible({ timeout: 8_000 });
    await searchBox.fill("from:sender-primary@example.com");
    await searchBox.press("Enter");
    await expect(page).toHaveURL(/q=from%3Asender-primary%40example\.com/);

    const nav = savedViewsNav(page);
    await nav.getByRole("button", { name: "Save current search" }).click();
    const nameInput = nav.getByRole("textbox", { name: "Saved search name" });
    await nameInput.fill("Primary sender messages");
    const savePersisted = waitForSavedSearchesPUT(page);
    await nameInput.press("Enter");
    await savePersisted;

    const savedSearch = nav.getByRole("button", { name: "Primary sender messages", exact: true });
    await expect(savedSearch).toBeVisible();
    await expect(nav.getByRole("button", { name: "Delete saved search Primary sender messages" })).toBeVisible();

    await searchBox.fill("");
    await searchBox.press("Enter");
    await expect(searchBox).toHaveValue("");

    await savedSearch.click();
    await expect(searchBox).toHaveValue("from:sender-primary@example.com");
    await expect(savedSearch).toHaveAttribute("aria-pressed", "true");
    await expect.poll(() => fixture.backend.state.searchQueries).toContain("from:sender-primary@example.com");

    const deletePersisted = waitForSavedSearchesPUT(page);
    await nav.getByRole("button", { name: "Delete saved search Primary sender messages" }).click();
    await deletePersisted;
    await expect(savedSearch).toHaveCount(0);
    await expect(nav.getByText("No saved searches yet.")).toBeVisible();

    const savedSearches = await page.request.get(`${fixture.server.info.base_url}/api/v1/messages/saved-searches`);
    expect(savedSearches.status()).toBe(200);
    await expect(savedSearches.json()).resolves.toMatchObject({ searches: [] });
  } finally {
    await fixture.stop();
  }
});

test("messages saved searches ignore stale browser storage", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "legacysavedsearches");
  let savedSearchPUTs = 0;
  page.on("request", (request) => {
    if (
      request.method() === "PUT" &&
      request.url() === `${fixture.server.info.base_url}/api/v1/messages/saved-searches`
    ) {
      savedSearchPUTs += 1;
    }
  });

  try {
    await page.goto(fixture.server.info.base_url);
    await page.evaluate(() => {
      localStorage.setItem(
        "middleman:messagesSavedSearches/v1",
        JSON.stringify([{ name: "Legacy local", query: "from:legacy@example.com" }]),
      );
    });

    await page.goto(`${fixture.server.info.base_url}/messages`);

    const nav = savedViewsNav(page);
    await expect(nav.getByText("No saved searches yet.")).toBeVisible({ timeout: 8_000 });
    await expect(nav.getByRole("button", { name: "Legacy local", exact: true })).toHaveCount(0);
    expect(savedSearchPUTs).toBe(0);

    const savedSearches = await page.request.get(`${fixture.server.info.base_url}/api/v1/messages/saved-searches`);
    expect(savedSearches.status()).toBe(200);
    await expect(savedSearches.json()).resolves.toMatchObject({ searches: [] });
  } finally {
    await fixture.stop();
  }
});

test("messages saved searches rehydrate after browser reload", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "savedreload");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages`);

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible({ timeout: 8_000 });
    await searchBox.fill("from:sender-weekly@example.com");
    await searchBox.press("Enter");
    await expect(page).toHaveURL(/q=from%3Asender-weekly%40example\.com/);

    const nav = savedViewsNav(page);
    await nav.getByRole("button", { name: "Save current search" }).click();
    await nav.getByRole("textbox", { name: "Saved search name" }).fill("Weekly sender messages");
    const savePersisted = waitForSavedSearchesPUT(page);
    await nav.getByRole("textbox", { name: "Saved search name" }).press("Enter");
    await savePersisted;
    await expect(nav.getByRole("button", { name: "Weekly sender messages", exact: true })).toBeVisible();

    await page.reload();
    const reloadedNav = savedViewsNav(page);
    const savedSearch = reloadedNav.getByRole("button", { name: "Weekly sender messages", exact: true });
    await expect(savedSearch).toBeVisible({ timeout: 8_000 });

    const reloadedSearchBox = page.getByPlaceholder("Search messages...");
    await reloadedSearchBox.fill("");
    await reloadedSearchBox.press("Enter");
    await expect(reloadedSearchBox).toHaveValue("");

    await savedSearch.click();
    await expect(reloadedSearchBox).toHaveValue("from:sender-weekly@example.com");
    await expect(savedSearch).toHaveAttribute("aria-pressed", "true");
  } finally {
    await fixture.stop();
  }
});

test("messages conversation stack shows thread peers", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "threadstack");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=project&message=102`);

    const conversation = page.getByRole("region", { name: "Conversation" });
    await expect(conversation.getByRole("heading", { name: "Deploy follow-up" })).toBeVisible({ timeout: 8_000 });
    await expect(conversation.getByText(/3\s+msgs?/i)).toBeVisible();
    const peers = conversation.getByRole("button", { name: /open message/i });
    await expect(peers).toHaveCount(2);
    await expect(conversation.getByRole("button", { name: /Open message .*2026-05-15T10:00:00Z/ })).toContainText(
      "Project sync",
    );
    await expect(conversation.getByRole("button", { name: /Open message .*2026-05-15T10:10:00Z/ })).toContainText(
      "Project sync wrap-up",
    );
    await expect(page.getByRole("region", { name: "Message body" })).toContainText(
      "Follow-up deploy details are ready.",
    );
  } finally {
    await fixture.stop();
  }
});

test("messages collapsed thread peer switches the selected message", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "peerswitch");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=project&message=101`);

    const conversation = page.getByRole("region", { name: "Conversation" });
    await expect(conversation.getByRole("heading", { name: "Project sync" })).toBeVisible({ timeout: 8_000 });
    await conversation.getByRole("button", { name: /Open message .*2026-05-15T10:05:00Z/ }).click();

    await expect(page).toHaveURL(/[?&]q=project/);
    await expect(page).toHaveURL(/[?&]message=102/);
    await expect(conversation.getByRole("heading", { name: "Deploy follow-up" })).toBeVisible();
    await expect(page.getByRole("region", { name: "Message body" })).toContainText(
      "Follow-up deploy details are ready.",
    );
    await expect(conversation.getByRole("button", { name: /Open message .*2026-05-15T10:05:00Z/ })).toHaveCount(0);
    await expect(conversation.getByRole("button", { name: /Open message .*2026-05-15T10:00:00Z/ })).toHaveCount(1);
    await expect(conversation.getByRole("button", { name: /Open message .*2026-05-15T10:10:00Z/ })).toHaveCount(1);
  } finally {
    await fixture.stop();
  }
});

test("messages singleton conversation renders without stack chrome", async ({ page }) => {
  const fixture = await startConfiguredMessagesFixture(page, "singleton");

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?q=solo&message=900`);

    await expect(page.getByRole("heading", { name: "Solo update" })).toBeVisible({ timeout: 8_000 });
    await expect.poll(() => fixture.backend.state.threadQueries).toContain(900);
    await expect(page.getByText("Loading conversation...")).toHaveCount(0);
    const conversation = page.getByRole("region", { name: "Conversation" });
    await expect(conversation.getByText(/\bmsgs?\b/i)).toHaveCount(0);
    await expect(conversation.getByRole("button", { name: /open message/i })).toHaveCount(0);
  } finally {
    await fixture.stop();
  }
});

test("messages hides task linking controls when no external Kata daemon is configured", async ({ page }) => {
  const backend = await startMsgvaultBackend();
  backend.state.authorized = true;
  const kataHome = await configureEmptyKataHome();
  const envName = `MSGVAULT_E2E_KEY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), "middleman-messages-no-kata-e2e-"));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/messages`);

    await page.getByRole("button", { name: "Set up Messages" }).click();
    await page.getByLabel("Message source URL").fill(backend.url);
    await page.getByLabel("API key env var name").fill(envName);
    await page.getByRole("button", { name: "Save" }).click();

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible();
    await searchBox.fill("project");
    await page.getByRole("search", { name: "Search messages" }).getByRole("button", { name: "Search" }).click();
    await messageRow(page, 101).click();
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();

    await expect(page.getByRole("button", { name: "Link to task" })).toHaveCount(0);
    await expect(
      page.getByRole("navigation", { name: "Messages facets" }).getByRole("button", { name: "Linked messages" }),
    ).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
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

test("messages direct linked view loads seeded rows and row subjects open detail", async ({ page }) => {
  const fixture = await startConfiguredMessagesAndKataFixture(page, "linkeddirect", (kata) => {
    const q3 = kata.state.issues.find((issue) => issue.uid === "issue-q3");
    const rent = kata.state.issues.find((issue) => issue.uid === "issue-rent");
    expect(q3).toBeTruthy();
    expect(rent).toBeTruthy();
    q3!.metadata = {
      ...q3!.metadata,
      mail_links: [
        {
          message_id: 101,
          conversation_id: 501,
          subject: "Project sync",
          from: "sender-primary@example.com",
          sent_at: "2026-05-15T10:00:00Z",
          added_at: "2026-05-15T10:00:00Z",
        },
      ],
    };
    rent!.metadata = {
      ...rent!.metadata,
      mail_links: [
        {
          message_id: 42,
          conversation_id: 700,
          subject: "Weekly status update",
          from: "sender-weekly@example.com",
          sent_at: "2026-05-14T09:00:00Z",
          added_at: "2026-05-15T10:00:00Z",
        },
      ],
    };
  });

  try {
    await page.goto(`${fixture.server.info.base_url}/messages?view=linked`);

    const linked = page.getByRole("region", { name: "Linked messages" });
    await expect(linked.locator(".linked-table")).toBeVisible({ timeout: 8_000 });
    await expect(linked.locator("tbody tr")).toHaveCount(2);
    await expect(linked).toContainText("Project sync");
    await expect(linked).toContainText("Weekly status update");

    await linked.getByRole("button", { name: "Weekly status update" }).click();

    await expect(page).toHaveURL(/[?&]view=linked/);
    await expect(page).toHaveURL(/[?&]message=42/);
    await expect(page.getByRole("heading", { name: "Weekly status update" })).toBeVisible();
    await expect.poll(() => fixture.backend.state.threadQueries).toContain(700);
  } finally {
    await fixture.stop();
  }
});

test("messages detail reverse-link pill opens the linked Kata task", async ({ page }) => {
  const msgvault = await startMsgvaultBackend();
  msgvault.state.authorized = true;
  const kata = await startKataBackend();
  const linkedIssue = kata.state.issues.find((issue) => issue.uid === "issue-q3");
  expect(linkedIssue).toBeTruthy();
  linkedIssue!.metadata = {
    ...linkedIssue!.metadata,
    mail_links: [
      {
        message_id: 101,
        conversation_id: 501,
        subject: "Project sync",
        from: "sender-primary@example.com",
        sent_at: "2026-05-15T10:00:00Z",
        added_at: "2026-05-15T10:00:00Z",
      },
    ],
  };
  const kataHome = await configureKataHome(kata.url);
  const envName = `MSGVAULT_E2E_KEY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), "middleman-messages-reverse-link-e2e-"));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  const server = await startIsolatedE2EServer();

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/msgvault/configure`, {
      headers: middlemanCSRFHeader,
      data: { url: msgvault.url, api_key_env: envName },
    });
    expect(res.status()).toBe(200);

    await page.goto(`${server.info.base_url}/messages?q=project&message=101`);
    await expect(page.getByRole("heading", { name: "Project sync", exact: true })).toBeVisible({ timeout: 8_000 });

    const linkedTasks = page.getByRole("region", { name: "Linked tasks" });
    await expect(linkedTasks.locator(".reverse-pill")).toHaveCount(1);
    await expect(linkedTasks.getByRole("button", { name: /Kata#kat-7/ })).toBeVisible();

    await linkedTasks.getByRole("button", { name: /Kata#kat-7/ }).click();

    await expect(page).toHaveURL(/\/kata\?issue=issue-q3/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );
  } finally {
    await server.stop();
    kataHome.restore();
    await kata.close();
    await msgvault.close();
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

test("message links a message to an external Kata task and refreshes linked messages", async ({ page }) => {
  const msgvault = await startMsgvaultBackend();
  msgvault.state.authorized = true;
  const kata = await startKataBackend();
  const kataHome = await configureKataHome(kata.url);
  const envName = `MSGVAULT_E2E_KEY_${Date.now()}`;
  const previousEnv = process.env[envName];
  const previousSavedSearchesPath = process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH;
  process.env[envName] = "secret-key";
  const savedSearchesDir = await mkdtemp(path.join(os.tmpdir(), "middleman-messages-link-e2e-"));
  process.env.MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH = path.join(savedSearchesDir, "saved-searches.toml");
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/messages`);

    await page.getByRole("button", { name: "Set up Messages" }).click();
    await page.getByLabel("Message source URL").fill(msgvault.url);
    await page.getByLabel("API key env var name").fill(envName);
    await page.getByRole("button", { name: "Save" }).click();

    const searchBox = page.getByPlaceholder("Search messages...");
    await expect(searchBox).toBeVisible();
    await searchBox.fill("project");
    await page.getByRole("search", { name: "Search messages" }).getByRole("button", { name: "Search" }).click();
    await messageRow(page, 101).click();
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();

    await page.getByRole("button", { name: "Link to task" }).click();
    const dialog = page.getByRole("dialog", { name: "Link to task" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("Search tasks").fill("q3");
    await dialog.getByRole("option", { name: /Kata#kat-7.*Email Susan re: Q3/ }).click();
    await dialog.getByRole("button", { name: "Link", exact: true }).click();

    await expect(page.getByRole("status").filter({ hasText: "Linked to Kata#kat-7." })).toBeVisible();
    await expect(dialog).toBeHidden();
    await expect.poll(() => kata.state.seenPaths).toContain("PUT /api/v1/projects/1/issues/issue-q3/metadata");
    expect(kata.state.seenIfMatches).toContain('"rev-1"');
    await expect
      .poll(() => {
        const linkedIssue = kata.state.issues.find((issue) => issue.uid === "issue-q3");
        const links = linkedIssue?.metadata.mail_links;
        if (!Array.isArray(links)) return [];
        return links.map((link) => (isRecord(link) ? link.message_id : undefined));
      })
      .toEqual([101]);
    expect(kata.state.metadataPatches).toHaveLength(1);

    await page
      .getByRole("navigation", { name: "Messages facets" })
      .getByRole("button", { name: "Linked messages" })
      .click();
    const linked = page.getByRole("region", { name: "Linked messages" });
    await expect(linked).toContainText("Project sync");
    await expect(linked.getByRole("button", { name: "Kata#kat-7" })).toBeVisible();

    const linkedMessagesURL = page.url();
    await linked.getByRole("button", { name: "Kata#kat-7" }).click();

    await expect(page).toHaveURL(/\/kata\?issue=issue-q3/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );

    await page.goBack();
    await expect(page).toHaveURL(linkedMessagesURL);
    const restoredLinked = page.getByRole("region", { name: "Linked messages" });
    await expect(restoredLinked).toContainText("Project sync");
    await expect(restoredLinked.getByRole("button", { name: "Kata#kat-7" })).toBeVisible();

    await page.goForward();
    await expect(page).toHaveURL(/\/kata\?issue=issue-q3/);
    await expect(page.getByRole("region", { name: "Task detail" })).toContainText(
      "Confirm the Q3 project review agenda.",
    );

    const taskLinks = page.getByRole("region", { name: "Linked messages" });
    await expect(taskLinks).toContainText("Project sync");
    await taskLinks.getByTitle("Open sender-primary@example.com - Project sync").click();

    await expect(page).toHaveURL(/\/messages\?message=101$/);
    await expect(page.getByRole("heading", { name: "Project sync" })).toBeVisible();

    await page.goto(`${server.info.base_url}/kata?issue=issue-q3`);
    const taskLinksAfterReturn = page.getByRole("region", { name: "Linked messages" });
    await expect(taskLinksAfterReturn).toContainText("Project sync");
    await taskLinksAfterReturn.getByRole("button", { name: "Unlink Project sync" }).click();

    await expect.poll(() => kata.state.metadataPatches).toHaveLength(2);
    expect(kata.state.seenIfMatches).toContain('"rev-2"');
    expect(kata.state.metadataPatches[1]).toEqual({ mail_links: null });
    await expect
      .poll(() => {
        const linkedIssue = kata.state.issues.find((issue) => issue.uid === "issue-q3");
        const links = linkedIssue?.metadata.mail_links;
        if (!Array.isArray(links)) return [];
        return links.map((link) => (isRecord(link) ? link.message_id : undefined));
      })
      .toEqual([]);
    await expect(page.getByRole("region", { name: "Linked messages" })).toHaveCount(0);
  } finally {
    await server.stop();
    kataHome.restore();
    await kata.close();
    await msgvault.close();
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
