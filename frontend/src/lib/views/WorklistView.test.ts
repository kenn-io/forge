import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { Effect } from "effect";
import { createMockApiFetch, jsonResponse, type MockRouteOverride } from "../../test/mockApiFetch.js";
import {
  WORKLIST_HANDLED_AT,
  worklistResponse,
  worklistRoutes,
  type WorklistServer,
} from "../../test/worklistFixtures.js";
import { makeAppRuntime } from "../app/runtime.js";
import { STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { resetPaneLayoutStoresForTest } from "../stores/paneLayout.svelte.js";
import { dismissFlash, getFlashes } from "../stores/flash.svelte.js";
import { navigate } from "../stores/router.svelte.js";
import { createSettingsStore } from "../stores/settings.svelte.js";
import { createWorklistsStore } from "../stores/worklists.svelte.js";

vi.mock("../components/detail/PullDetailPane.svelte", async () => ({
  default: (await import("./WorklistViewTestPullDetailPane.svelte")).default,
}));
vi.mock("../components/detail/IssueDetail.svelte", async () => ({
  default: (await import("./IssueListViewTestIssueDetail.svelte")).default,
}));

import WorklistView from "./WorklistViewTestHarness.svelte";

const originalFetch = globalThis.fetch;

function currentPath(): string {
  return window.location.pathname + window.location.search;
}

function rail(): HTMLElement | null {
  return screen.queryByRole("region", { name: /items$/ });
}

function rowTitles(): string[] {
  const region = rail();
  if (!region) return [];
  return within(region)
    .queryAllByRole("listitem")
    .map((item) => within(item).getByRole("button").getAttribute("aria-labelledby") ?? "")
    .map((id) => document.getElementById(id)?.textContent?.trim() ?? "");
}

function sectionLists(): [string | null, number][] {
  const region = rail();
  if (!region) return [];
  return within(region)
    .queryAllByRole("list")
    .map((list) => [list.getAttribute("aria-label"), within(list).queryAllByRole("listitem").length]);
}

async function visiblePullPane(): Promise<string> {
  return waitFor(() => {
    const pane = document.querySelector('[data-testid="pull-detail-pane"][data-visible="true"]');
    if (!pane) throw new Error("no visible pull detail pane");
    return pane.textContent ?? "";
  });
}

function selectionBar(): HTMLElement {
  const bar = screen.getByText(/^\d+ selected$/).parentElement;
  if (!bar) throw new Error("no selection bar");
  return bar;
}

function row(title: string): HTMLElement {
  return screen.getByRole("button", { name: title });
}

/** Holds matching requests until the returned release runs. */
function holdRequests(matches: (method: string, path: string) => boolean): () => void {
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const inner = globalThis.fetch;
  globalThis.fetch = async (input, init) => {
    const request = input instanceof Request ? input : new Request(new URL(String(input), window.location.href), init);
    if (matches(request.method.toUpperCase(), new URL(request.url).pathname)) await held;
    return inner(input, init);
  };
  return release;
}

async function rewriteLists(stores: ReturnType<typeof renderView>): Promise<void> {
  const runtime = makeAppRuntime();
  await runtime.runCommand(stores.worklists.refreshAllEffect(), {
    operation: "test agent rewrite",
    safeContext: {},
    onFailure: () => {},
  }).exit;
  await Effect.runPromise(runtime.disposeEffect);
}

function renderView(path: string, server: WorklistServer, overrides: MockRouteOverride[] = []) {
  globalThis.fetch = createMockApiFetch([...overrides, worklistRoutes(server)]).fetch;
  navigate(path);
  const stores = {
    worklists: createWorklistsStore(),
    settings: createSettingsStore(),
    detail: { getDetail: () => null, loadDetail: vi.fn(async () => undefined) },
    issues: { getIssueDetail: () => null, loadIssueDetail: vi.fn(async () => undefined) },
  };
  render(WorklistView, { context: new Map<symbol, unknown>([[STORES_KEY, stores]]) });
  return stores;
}

describe("WorklistView", () => {
  let server: WorklistServer;

  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    resetPaneLayoutStoresForTest();
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe(): void {}
        disconnect(): void {}
        takeRecords(): MutationRecord[] {
          return [];
        }
      },
    );
    vi.stubGlobal("requestAnimationFrame", () => 1);
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    server = {
      lists: [worklistResponse(5, "Review queue"), worklistResponse(4, "Stale issues", [])],
      requests: [],
    };
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
    navigate("/");
    resetModalStack();
    resetPaneLayoutStoresForTest();
    localStorage.clear();
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  });

  it("keeps a review thread's jump to diff on the worklist and shows the files pane", async () => {
    renderView("/worklists/5", server);
    expect(await visiblePullPane()).toContain("PR acme/widgets#42 conversation");

    await fireEvent.click(screen.getByRole("button", { name: "Jump to diff" }));

    await waitFor(() => expect(currentPath()).toMatch(/^\/worklists\/5\?selected=pr%3A42&.*selected_tab=files/));
    expect(await visiblePullPane()).toContain("PR acme/widgets#42 files");
  });

  it("keeps the user's route when a dismissal finishes after they left", async () => {
    const stores = renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    const release = holdRequests((method, path) => method === "POST" && path === "/api/v1/worklists/5/dismiss");

    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    navigate("/pulls");
    release();

    await waitFor(() => expect(stores.worklists.getSummaries().map((summary) => summary.id)).toEqual([4]));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(currentPath()).toBe("/pulls");
    expect(server.lists[0]?.dismissed_at).not.toBeNull();
  });

  it("keeps a newly picked list when a dismissal finishes after the user switched lists", async () => {
    server.lists.push(worklistResponse(3, "Release blockers", []));
    const stores = renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    const release = holdRequests((method, path) => method === "POST" && path === "/api/v1/worklists/5/dismiss");

    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    navigate("/worklists/3");
    await screen.findByText("No items remain on this list.");
    release();

    await waitFor(() => expect(stores.worklists.getSummaries().map((summary) => summary.id)).toEqual([4, 3]));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(currentPath()).toBe("/worklists/3");
    expect(screen.queryByText("Dismissed Review queue.")).toBeNull();
    expect(screen.getByRole("button", { name: "Dismiss" }).hasAttribute("disabled")).toBe(false);
  });

  it("keeps a restored list open when the dismissed copy of it was already loaded", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(currentPath()).toBe("/worklists/4"));
    // Opening the list during its undo window loads it with dismissed_at set.
    navigate("/worklists/5");
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await waitFor(() => expect(screen.queryByText("Dismissed Review queue.")).toBeNull());
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(currentPath()).toBe("/worklists/5");
    expect(rowTitles()).toHaveLength(4);
  });

  it("keeps the user's route when a restore finishes after they left", async () => {
    const stores = renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(currentPath()).toBe("/worklists/4"));
    const release = holdRequests((method, path) => method === "POST" && path === "/api/v1/worklists/5/undismiss");

    await fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    navigate("/pulls");
    release();

    await waitFor(() => expect(stores.worklists.getSummaries().map((summary) => summary.id)).toContain(5));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(currentPath()).toBe("/pulls");
  });

  it("keeps Undo after a failed restore and restores the list on retry", async () => {
    let failRestore = true;
    renderView("/worklists/5", server, [
      (request) => {
        if (request.method !== "POST" || request.url.pathname !== "/api/v1/worklists/5/undismiss" || !failRestore) {
          return null;
        }
        failRestore = false;
        return jsonResponse(
          { title: "Bad Request", status: 400, detail: "restore was refused", code: "badRequest" },
          400,
        );
      },
    ]);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(currentPath()).toBe("/worklists/4"));

    await fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await waitFor(() => expect(getFlashes().map((flash) => flash.message)).toContain("restore was refused"));
    expect(screen.getByText("Dismissed Review queue.")).toBeTruthy();
    expect(currentPath()).toBe("/worklists/4");

    await fireEvent.click(await screen.findByRole("button", { name: "Undo" }));

    await waitFor(() => expect(currentPath()).toMatch(/^\/worklists\/5\?selected=pr%3A42/));
    expect(screen.queryByText("Dismissed Review queue.")).toBeNull();
  });

  it("moves to the next visible row when a rewrite drops the open item", async () => {
    const stores = renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    await fireEvent.click(row("Mirror host stub PR"));
    await waitFor(() => expect(currentPath()).toContain("selected=pr%3A84"));

    server.lists[0]!.items = server.lists[0]!.items.filter((item) => item.number !== 84);
    await rewriteLists(stores);

    // Agent order groups by section, so the row after #84 is issue #7, not the
    // next item by rank.
    await waitFor(() => expect(currentPath()).toMatch(/^\/worklists\/5\?selected=issue%3A7&/));
    const strip = screen.getByRole("region", { name: "Worklist item context" });
    expect(within(strip).getByText("Reporter confirmed on two browsers.")).toBeTruthy();
  });

  it("moves to the previous row when the dropped item was last, and closes the detail once none remain", async () => {
    const stores = renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    await fireEvent.click(row("Retire the legacy exporter"));
    await waitFor(() => expect(currentPath()).toContain("selected=pr%3A55"));

    server.lists[0]!.items = server.lists[0]!.items.filter((item) => item.number !== 55);
    await rewriteLists(stores);
    await waitFor(() => expect(currentPath()).toContain("selected=issue%3A7"));

    server.lists[0]!.items = [];
    await rewriteLists(stores);
    await waitFor(() => expect(currentPath()).toBe("/worklists/5"));
    expect(screen.queryByRole("region", { name: "Worklist item context" })).toBeNull();
  });

  it("keeps the title, recipe, and Dismiss on a list with no items left", async () => {
    server.lists = [worklistResponse(5, "Review queue", [])];
    renderView("/worklists/5", server);

    expect(await screen.findByText("No items remain on this list.")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Review queue" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Recipe" }));
    expect(screen.getByRole("region", { name: "Recipe" }).textContent).toContain("Open pull requests and issues");
  });

  it("opens the newest list and its first item", async () => {
    renderView("/worklists", server);

    await waitFor(() =>
      expect(currentPath()).toBe(
        "/worklists/5?selected=pr%3A42&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&platform_repo_id=1001",
      ),
    );
    expect(await visiblePullPane()).toContain("PR acme/widgets#42 conversation");
    const strip = screen.getByRole("region", { name: "Worklist item context" });
    expect(within(strip).getByText("Approved and green; only a docs nit left.")).toBeTruthy();
    expect(within(strip).getByText(/^triage-agent wrote this item/)).toBeTruthy();
    expect(within(strip).getByText("3 new comments")).toBeTruthy();
  });

  it("groups rows by section in rank order and marks what changed", async () => {
    renderView("/worklists/5", server);

    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    expect(rowTitles()).toEqual([
      "Add browser regression coverage",
      "Mirror host stub PR",
      "Theme toggle does not stick",
      "Retire the legacy exporter",
    ]);
    expect(sectionLists()).toEqual([
      ["Ready to merge", 2],
      ["Needs a decision", 2],
    ]);
    expect(within(row("Add browser regression coverage")).getByText("CI passing → failing")).toBeTruthy();
    expect(within(row("Mirror host stub PR")).getByText("New commits")).toBeTruthy();
    expect(within(row("Retire the legacy exporter")).getByText("No longer synced")).toBeTruthy();
    expect(
      within(row("Theme toggle does not stick")).getByText("Could be a duplicate of an older report."),
    ).toBeTruthy();
  });

  it("collapses a section and skips its rows when stepping", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(screen.getByRole("button", { name: /^Ready to merge/ }));

    expect(rowTitles()).toEqual(["Theme toggle does not stick", "Retire the legacy exporter"]);
    expect(sectionLists()).toEqual([["Needs a decision", 2]]);
  });

  it("shows each item's live status from its synced row", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    const pull = row("Add browser regression coverage");
    expect(pull.getAttribute("aria-describedby")).toBeTruthy();
    expect(within(pull).getByText("acme/widgets#42")).toBeTruthy();
    expect(within(pull).getByText("Approved and green; only a docs nit left.")).toBeTruthy();
    expect(within(pull).getByTitle("Agent score").textContent?.trim()).toBe("9");
    expect(within(row("Retire the legacy exporter")).queryByTitle("Agent score")?.textContent?.trim()).toBe("3");
  });

  it("filters to the items that changed since the list was written", async () => {
    renderView("/worklists/5", server);
    const toggle = await screen.findByRole("button", { name: "3 changed since" });

    await fireEvent.click(toggle);

    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(rowTitles()).toEqual([
      "Add browser regression coverage",
      "Mirror host stub PR",
      "Retire the legacy exporter",
    ]);
  });

  it("says a review may cover an older head when the PR has new commits", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(screen.getByRole("button", { name: "Mirror host stub PR" }));

    await waitFor(() => expect(currentPath()).toContain("selected=pr%3A84"));
    const strip = screen.getByRole("region", { name: "Worklist item context" });
    expect(within(strip).getByText("Review notes may cover an older head")).toBeTruthy();
    expect(await visiblePullPane()).toContain("PR acme/widgets#84");
  });

  it("opens an issue in the issue detail", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(row("Theme toggle does not stick"));

    expect((await screen.findByTestId("issue-detail")).textContent).toContain("Issue acme/widgets#7");
  });

  it("acts on the synced items of a mixed selection", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(row("Add browser regression coverage"), { metaKey: true });
    await fireEvent.click(row("Theme toggle does not stick"), { metaKey: true });
    await fireEvent.click(row("Retire the legacy exporter"), { metaKey: true });
    expect(screen.getByText("3 selected")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Actions" }));

    expect(screen.getByRole("menu", { name: "Actions for 2 items" })).toBeTruthy();
  });

  it("dismisses a list, moves to the next, and restores it on undo", async () => {
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));

    await waitFor(() => expect(currentPath()).toBe("/worklists/4"));
    expect(server.requests).toContain("POST /api/v1/worklists/5/dismiss");
    expect(screen.getByText("Dismissed Review queue.")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await waitFor(() => expect(currentPath()).toMatch(/^\/worklists\/5\?selected=pr%3A42/));
    expect(server.requests).toContain("POST /api/v1/worklists/5/undismiss");
    expect(screen.queryByText("Dismissed Review queue.")).toBeNull();
  });

  it("shows the empty state after the last list is dismissed and brings the list back on undo", async () => {
    server.lists = [worklistResponse(5, "Review queue")];
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(await screen.findByText("No worklists")).toBeTruthy();
    expect(screen.getByText("Dismissed Review queue.")).toBeTruthy();
    expect(currentPath()).toBe("/worklists");

    await fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await waitFor(() => expect(currentPath()).toMatch(/^\/worklists\/5\?selected=pr%3A42/));
    expect(server.requests).toContain("POST /api/v1/worklists/5/undismiss");
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    expect(screen.queryByText("No worklists")).toBeNull();
    expect(screen.queryByText("Dismissed Review queue.")).toBeNull();
  });

  it("falls back to the newest list when the routed one is gone", async () => {
    renderView("/worklists/99", server);

    await waitFor(() => expect(currentPath()).toMatch(/^\/worklists\/5\?selected=pr%3A42/));
  });

  it("leaves a list that was dismissed elsewhere for the newest open one", async () => {
    server.lists[0]!.dismissed_at = "2026-10-20T09:00:00Z";
    renderView("/worklists/5", server);

    await waitFor(() => expect(currentPath()).toBe("/worklists/4"));
    expect(server.requests).toContain("GET /api/v1/worklists/5");
  });

  it("leaves the open list once an agent dismisses it", async () => {
    const stores = renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    server.lists[0]!.dismissed_at = "2026-10-20T09:00:00Z";
    const runtime = makeAppRuntime();
    await runtime.runCommand(stores.worklists.refreshAllEffect(), {
      operation: "test agent dismissal",
      safeContext: {},
      onFailure: () => {},
    }).exit;
    await Effect.runPromise(runtime.disposeEffect);

    await waitFor(() => expect(currentPath()).toBe("/worklists/4"));
    expect(screen.queryByRole("button", { name: "Add browser regression coverage" })).toBeNull();
  });

  it("shows the empty state when every list is dismissed", async () => {
    for (const list of server.lists) list.dismissed_at = "2026-10-20T09:00:00Z";
    renderView("/worklists/5", server);

    expect(await screen.findByText("No worklists")).toBeTruthy();
    expect(rowTitles()).toEqual([]);
  });

  it("reports a failed read instead of claiming there are no worklists", async () => {
    renderView("/worklists", server, [
      (request) =>
        request.method === "GET" && request.url.pathname === "/api/v1/worklists"
          ? jsonResponse({ title: "Unauthorized", status: 401, detail: "sign in again", code: "unauthorized" }, 401)
          : null,
    ]);

    expect((await screen.findByRole("alert")).textContent).toContain("sign in again");
    expect(screen.queryByText("No worklists")).toBeNull();
  });

  it("marks the open item handled from the context strip", async () => {
    server.marks = [];
    renderView("/worklists/5", server);
    const strip = await screen.findByRole("region", { name: "Worklist item context" });
    expect(screen.getByText("0 of 4 handled")).toBeTruthy();
    expect(within(row("Add browser regression coverage")).queryByLabelText("Handled")).toBeNull();

    await fireEvent.click(within(strip).getByRole("button", { name: "Mark handled" }));

    const toggle = await within(strip).findByRole("button", { name: "Handled" });
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(server.marks).toEqual([
      {
        handled: true,
        items: [
          { provider: "github", platform_host: "github.com", platform_repo_id: 1001, item_type: "pr", number: 42 },
        ],
      },
    ]);
    expect(within(row("Add browser regression coverage")).getByLabelText("Handled")).toBeTruthy();
    expect(screen.getByText("1 of 4 handled")).toBeTruthy();

    await fireEvent.click(toggle);

    await within(strip).findByRole("button", { name: "Mark handled" });
    expect(server.marks[1]?.handled).toBe(false);
    expect(within(row("Add browser regression coverage")).queryByLabelText("Handled")).toBeNull();
    expect(screen.getByText("0 of 4 handled")).toBeTruthy();
  });

  it("marks every selected item handled, then offers to unmark them", async () => {
    server.marks = [];
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    await fireEvent.click(row("Add browser regression coverage"), { metaKey: true });
    await fireEvent.click(row("Theme toggle does not stick"), { metaKey: true });
    expect(screen.getByText("2 selected")).toBeTruthy();
    await fireEvent.click(within(selectionBar()).getByRole("button", { name: "Mark handled" }));

    await waitFor(() => expect(screen.queryByText("2 selected")).toBeNull());
    expect(server.marks).toEqual([
      {
        handled: true,
        items: [
          { provider: "github", platform_host: "github.com", platform_repo_id: 1001, item_type: "pr", number: 42 },
          { provider: "github", platform_host: "github.com", platform_repo_id: 1001, item_type: "issue", number: 7 },
        ],
      },
    ]);
    expect(within(row("Add browser regression coverage")).getByLabelText("Handled")).toBeTruthy();
    expect(within(row("Theme toggle does not stick")).getByLabelText("Handled")).toBeTruthy();
    expect(within(row("Mirror host stub PR")).queryByLabelText("Handled")).toBeNull();
    expect(screen.getByText("2 of 4 handled")).toBeTruthy();

    await fireEvent.click(row("Add browser regression coverage"), { metaKey: true });
    await fireEvent.click(row("Theme toggle does not stick"), { metaKey: true });
    await fireEvent.click(within(selectionBar()).getByRole("button", { name: "Mark unhandled" }));

    await waitFor(() => expect(screen.getByText("0 of 4 handled")).toBeTruthy());
    expect(server.marks[1]?.handled).toBe(false);
  });

  it("shows a handled item that is also gone as both", async () => {
    const list = worklistResponse(5, "Review queue");
    list.items = list.items.map((item) => (item.number === 55 ? { ...item, handled_at: WORKLIST_HANDLED_AT } : item));
    server.lists = [list];
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    const gone = row("Retire the legacy exporter");
    expect(within(gone).getByLabelText("Handled")).toBeTruthy();
    expect(within(gone).getByText("No longer synced")).toBeTruthy();
    expect(screen.getByText("1 of 4 handled")).toBeTruthy();
  });

  it("orders rows by score in one flat list and keeps the choice across reloads", async () => {
    const list = worklistResponse(5, "Review queue");
    const scores: Record<number, number | null> = { 42: 9, 84: 7, 7: null, 55: 10 };
    list.items = list.items.map((item) => ({ ...item, score: scores[item.number] ?? null }));
    server.lists = [list, worklistResponse(4, "Stale issues", [])];
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    const agentOrder = screen.getByRole("radio", { name: "Agent order" });
    expect(agentOrder.getAttribute("aria-checked")).toBe("true");

    await fireEvent.click(screen.getByRole("radio", { name: "Score" }));

    expect(rowTitles()).toEqual([
      "Retire the legacy exporter",
      "Add browser regression coverage",
      "Mirror host stub PR",
      "Theme toggle does not stick",
    ]);
    expect(sectionLists()).toEqual([["Review queue", 4]]);

    await fireEvent.click(screen.getByRole("button", { name: "3 changed since" }));
    expect(rowTitles()).toEqual([
      "Retire the legacy exporter",
      "Add browser regression coverage",
      "Mirror host stub PR",
    ]);

    cleanup();
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    expect(screen.getByRole("radio", { name: "Score" }).getAttribute("aria-checked")).toBe("true");
    expect(rowTitles()[0]).toBe("Retire the legacy exporter");

    await fireEvent.click(screen.getByRole("radio", { name: "Agent order" }));
    expect(sectionLists()).toEqual([
      ["Ready to merge", 2],
      ["Needs a decision", 2],
    ]);
  });

  it("shows how many items each list has left in the picker while the trigger keeps the title", async () => {
    const list = worklistResponse(5, "Review queue");
    list.items = list.items.map((item) =>
      item.number === 55 || item.number === 42 ? { ...item, handled_at: WORKLIST_HANDLED_AT } : item,
    );
    server.lists = [list, worklistResponse(4, "Stale issues", [])];
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));
    const trigger = screen.getByRole("combobox", { name: "Worklist: Review queue" });
    expect(trigger.textContent?.trim()).toBe("Review queue");

    await fireEvent.click(trigger);

    const options = screen.getAllByRole("option").map((option) => option.textContent?.trim());
    expect(options).toEqual(["Review queue · 2 left", "Stale issues · done"]);
  });

  it("shows the same status icons as the pull request and issue lists", async () => {
    const list = worklistResponse(5, "Review queue");
    list.items = list.items.map((item) => {
      if (item.number === 84 && item.pull) {
        return {
          ...item,
          pull: {
            ...item.pull,
            ReviewDecision: "APPROVED",
            CIChecksJSON: "{not json",
            stack: { stack_id: 1, position: 2, size: 3 },
            merge_queue: {
              position: 2,
              state: "awaiting_checks",
              enqueued_at: "2026-10-19T12:00:00Z",
              observed_at: "2026-10-19T12:01:00Z",
            },
            workspace: { id: "ws-84", status: "ready" },
            labels: [{ name: "needs-docs", color: "0e8a16" }],
          },
        } as typeof item;
      }
      if (item.number === 7 && item.issue) {
        return {
          ...item,
          issue: {
            ...item.issue,
            workspace: { id: "ws-7", status: "ready" },
            labels: [{ name: "bug", color: "d73a4a" }],
          },
        } as typeof item;
      }
      return item;
    });
    server.lists = [list];
    renderView("/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(4));

    const pull = row("Mirror host stub PR");
    expect(within(pull).getByLabelText("PR approved")).toBeTruthy();
    expect(within(pull).getByLabelText("In merge queue, position 2")).toBeTruthy();
    expect(within(pull).getByLabelText("Stacked: 2/3")).toBeTruthy();
    expect(within(pull).getByTestId("ci-token-unavailable")).toBeTruthy();
    expect(within(pull).getByTitle("Has merge conflicts")).toBeTruthy();
    expect(within(pull).queryByText("Conflict")).toBeNull();
    expect(within(pull).getByLabelText("Workspace attached (ready)")).toBeTruthy();
    expect(within(pull).getByText("needs-docs")).toBeTruthy();

    const issue = row("Theme toggle does not stick");
    expect(within(issue).getByLabelText("Workspace attached (ready)")).toBeTruthy();
    expect(within(issue).getByText("bug")).toBeTruthy();
    expect(within(row("Add browser regression coverage")).queryByTitle("Has merge conflicts")).toBeNull();
  });

  it("names an unnamed writer as an agent", async () => {
    server.lists = [{ ...worklistResponse(5, "Review queue"), created_by: "" }];
    renderView("/worklists", server);

    expect(await screen.findByText(/^Written by an agent /)).toBeTruthy();
    const strip = await screen.findByRole("region", { name: "Worklist item context" });
    expect(within(strip).getByText(/^An agent wrote this item /)).toBeTruthy();
  });
});
