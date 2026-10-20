import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import {
  emitBrowserEventSource,
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, mockSettings, type MockRouteOverride } from "./test/mockApiFetch.js";
import { WORKLIST_HANDLED_AT, worklistResponse, worklistRoutes, type WorklistServer } from "./test/worklistFixtures.js";

let mounted: MountedBrowserApp | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
  sessionStorage.clear();
  await resetKeyboardModuleState();
});

it("adds the Worklists tab when an agent writes a list and opens its first item", async () => {
  await page.viewport(1400, 900);
  const server: WorklistServer = { lists: [], requests: [] };
  mounted = await mountBrowserApp("/", { overrides: [worklistRoutes(server)] });
  await expect.element(page.getByRole("button", { name: "Activity", exact: true })).toBeVisible();
  expect(document.querySelector(".app-top-bar")?.textContent).not.toContain("Worklists");

  server.lists = [worklistResponse(5, "Review queue")];
  emitBrowserEventSource("worklists_changed", { worklist_id: 5 });
  await page.getByRole("button", { name: "Worklists", exact: true }).click();

  await vi.waitFor(() =>
    expect(window.location.pathname + window.location.search).toBe(
      "/worklists/5?selected=pr%3A42&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&platform_repo_id=1001",
    ),
  );
  const strip = page.getByRole("region", { name: "Worklist item context" });
  await expect.element(strip.getByText("Approved and green; only a docs nit left.")).toBeVisible();
  await expect.element(page.getByRole("tab", { name: "Conversation" })).toBeVisible();

  pressKey("j");
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A84"));
  await expect.element(strip.getByText("Review notes may cover an older head")).toBeVisible();
});

it("does not loop or re-read the open list on the agent status poll", async () => {
  await page.viewport(1400, 900);
  const errors: unknown[] = [];
  const onError = (event: ErrorEvent): void => {
    errors.push(event.error ?? event.message);
  };
  window.addEventListener("error", onError);
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [] };
  const agentStatusSettings: MockRouteOverride = (request) => {
    if (request.method !== "GET" || request.url.pathname !== "/api/v1/settings") return null;
    return jsonResponse({
      ...mockSettings,
      workspaces: { ...mockSettings.workspaces, show_agent_status_in_lists: true },
    });
  };
  try {
    mounted = await mountBrowserApp("/worklists/5", { overrides: [agentStatusSettings, worklistRoutes(server)] });
    await expect.element(page.getByRole("heading", { name: "Review queue" })).toBeVisible();
    // Showing a hidden page runs the poll at once.
    let visibility: DocumentVisibilityState = "hidden";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    document.dispatchEvent(new Event("visibilitychange"));
    visibility = "visible";
    document.dispatchEvent(new Event("visibilitychange"));
    await new Promise((resolve) => setTimeout(resolve, 1000));

    // Only opening the list reads it; a loop or a poll refresh would read again.
    const detailReads = server.requests.filter((request) => request === "GET /api/v1/worklists/5");
    expect(detailReads).toHaveLength(1);
    expect(errors).toEqual([]);
  } finally {
    window.removeEventListener("error", onError);
    vi.restoreAllMocks();
  }
});

it("fits every row in the rail at its narrowest width", async () => {
  await page.viewport(1400, 900);
  localStorage.setItem("kenn-forge-worklist-rail-width", "320");
  const long = "A very long agent note that keeps going well past the width of a narrow rail ".repeat(3);
  const list = worklistResponse(5, "Review queue");
  list.items = list.items.map((item) => ({
    ...item,
    note: long,
    caveat: long,
    pull: item.pull && {
      ...item.pull,
      MergeableState: "dirty",
      ReviewDecision: "APPROVED",
      stack: { stack_id: 1, position: 12, size: 14 },
      merge_queue: {
        position: 3,
        state: "awaiting_checks",
        enqueued_at: "2026-10-19T12:00:00Z",
        observed_at: "2026-10-19T12:01:00Z",
      },
      workspace: { id: `ws-${item.number}`, status: "ready", agent_state: "working" },
      labels: [{ name: "a-rather-long-label-name", color: "0e8a16" }],
    },
    issue: item.issue && {
      ...item.issue,
      workspace: { id: `ws-${item.number}`, status: "ready", agent_state: "input" },
      labels: [{ name: "a-rather-long-label-name", color: "d73a4a" }],
    },
  })) as typeof list.items;
  const server: WorklistServer = { lists: [list, worklistResponse(4, "Stale issues", [])], requests: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  const rail = page.getByRole("region", { name: "Review queue items" });
  await expect.element(rail.getByRole("button", { name: "Theme toggle does not stick" })).toBeVisible();

  const region = rail.element();
  expect(region.getBoundingClientRect().width).toBeLessThanOrEqual(320);
  expect(region.scrollWidth).toBeLessThanOrEqual(region.clientWidth);
  const railRight = region.getBoundingClientRect().right;
  for (const row of region.querySelectorAll<HTMLElement>("li > button")) {
    expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth);
    for (const part of row.querySelectorAll<HTMLElement>("*")) {
      expect(part.getBoundingClientRect().right).toBeLessThanOrEqual(railRight + 0.5);
    }
  }
});

it("marks the open item handled with d and moves on in rank order", async () => {
  await page.viewport(1400, 900);
  const list = worklistResponse(5, "Review queue");
  list.items = list.items.map((item) => (item.number === 84 ? { ...item, handled_at: WORKLIST_HANDLED_AT } : item));
  const server: WorklistServer = { lists: [list], requests: [], marks: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A42"));
  const rail = page.getByRole("region", { name: "Review queue items" });
  await expect.element(rail.getByRole("button", { name: "Add browser regression coverage" })).toBeVisible();

  pressKey("d");

  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A84"));
  await expect.element(page.getByText("2 of 4 handled")).toBeVisible();
  expect(server.marks?.map((mark) => mark.items.map((item) => item.number))).toEqual([[42]]);

  pressKey("d");

  await vi.waitFor(() => expect(window.location.search).toContain("selected=issue%3A7"));
  expect(server.marks).toHaveLength(1);
  const handledRow = rail.getByRole("button", { name: "Add browser regression coverage" }).element();
  const title = handledRow.querySelector<HTMLElement>(".worklist-row-body");
  expect(Number(getComputedStyle(title!).opacity)).toBeLessThan(1);
  const openRow = rail.getByRole("button", { name: "Theme toggle does not stick" }).element();
  expect(Number(getComputedStyle(openRow.querySelector<HTMLElement>(".worklist-row-body")!).opacity)).toBe(1);
});

it("keeps the open item when marking it handled with d fails", async () => {
  await page.viewport(1400, 900);
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [], marks: [] };
  const failingMarks: MockRouteOverride = (request) => {
    if (request.method !== "POST" || request.url.pathname !== "/api/v1/worklists/5/handled") return null;
    return jsonResponse({ title: "Internal Server Error", status: 500, detail: "mark failed" }, 500);
  };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [failingMarks, worklistRoutes(server)] });
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A42"));

  pressKey("d");

  await expect.element(page.getByText("0 of 4 handled")).toBeVisible();
  await expect.element(page.getByText(/mark failed|Could not mark the item handled/)).toBeVisible();
  expect(window.location.search).toContain("selected=pr%3A42");
});

it("steps and marks rows in score order when the rail is sorted by score", async () => {
  await page.viewport(1400, 900);
  localStorage.setItem("kenn-forge:worklists:order", "score");
  const list = worklistResponse(5, "Review queue");
  const scores: Record<number, number | null> = { 42: 9, 84: 7, 7: null, 55: 10 };
  list.items = list.items.map((item) => ({ ...item, score: scores[item.number] ?? null }));
  const server: WorklistServer = { lists: [list], requests: [], marks: [] };
  mounted = await mountBrowserApp(
    "/worklists/5?selected=pr%3A55&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&platform_repo_id=1001",
    { overrides: [worklistRoutes(server)] },
  );
  const rail = page.getByRole("region", { name: "Review queue items" });
  await expect.element(rail.getByRole("button", { name: "Retire the legacy exporter" })).toBeVisible();
  await expect.element(page.getByRole("radio", { name: "Score" })).toHaveAttribute("aria-checked", "true");

  // The highest-scored item is last in agent order, so d only moves on in score order.
  pressKey("d");
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A42"));
  await expect.element(page.getByText("1 of 4 handled")).toBeVisible();
  expect(server.marks?.map((mark) => mark.items.map((item) => item.number))).toEqual([[55]]);

  pressKey("j");
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A84"));

  pressKey("j");
  await vi.waitFor(() => expect(window.location.search).toContain("selected=issue%3A7"));

  pressKey("k");
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A84"));
});

it("keeps handled marks on items an agent keeps when it rewrites the list", async () => {
  await page.viewport(1400, 900);
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [], marks: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  const strip = page.getByRole("region", { name: "Worklist item context" });
  await strip.getByRole("button", { name: "Mark handled" }).click();
  await expect.element(strip.getByRole("button", { name: "Handled" })).toBeVisible();

  const kept = server.lists[0]!.items.find((item) => item.number === 42)!;
  const rewritten = worklistResponse(5, "Review queue, rewritten");
  rewritten.items = rewritten.items
    .filter((item) => item.number !== 55)
    .map((item) => (item.number === 42 ? { ...item, handled_at: kept.handled_at } : item));
  server.lists = [rewritten];
  emitBrowserEventSource("worklists_changed", { worklist_id: 5 });

  await expect.element(page.getByRole("heading", { name: "Review queue, rewritten" })).toBeVisible();
  await expect.element(page.getByText("1 of 3 handled")).toBeVisible();
  await expect.element(strip.getByRole("button", { name: "Handled" })).toBeVisible();
  const rail = page.getByRole("region", { name: "Review queue, rewritten items" });
  await expect
    .element(rail.getByRole("button", { name: "Add browser regression coverage" }).getByLabelText("Handled"))
    .toBeVisible();
});
