import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { createAppStores } from "../app-stores.svelte.js";
import type { OwnedAppRuntime } from "../app/runtime.js";
import { STORES_KEY, NAVIGATE_KEY, HOST_STATE_KEY, SIDEBAR_KEY } from "../context.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import AppRuntimeHarness from "../../test/AppRuntimeHarness.svelte";
import PullList from "../components/sidebar/PullList.svelte";
import IssueList from "../components/sidebar/IssueList.svelte";
import FocusListView from "../views/FocusListViewRuntimeHarness.svelte";
import { tick } from "svelte";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import type { PullRequest } from "../api/types.js";
import { GeneratedProblemResponse } from "../api/runtime.js";

let runtime: OwnedAppRuntime;
afterEach(async () => {
  cleanup();
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
  localStorage.clear();
});

async function setup(page: "pulls" | "issues", sidebar = false) {
  const sent = vi.fn(async (_body: unknown) => ({ status: "queued" as const }));
  const state = { failed: false, rows: [] as PullRequest[], pending: undefined as Promise<[]> | undefined };
  const list = vi.fn(async (..._args: unknown[]) => {
    if (state.pending) await state.pending;
    if (state.failed)
      throw new GeneratedProblemResponse(
        { code: "forbidden", status: 403, title: "Forbidden" },
        Response.json({}, { status: 403 }),
      );
    return state.rows;
  });
  runtime = makeTestAppRuntime(
    makeGeneratedClient({
      PullRequestsService: { listPulls: list },
      IssuesService: {
        listIssues: async (...args) => {
          await list(...args);
          return [];
        },
      },
      SystemService: { captureTelemetryEvent: sent },
      SyncService: {
        triggerSync: async () => undefined,
        getSyncStatus: async () => ({ running: false, last_error: "" }),
        getRateLimits: async () => ({ provider_pools: {}, local_ceilings: {} }),
      },
    }),
  );
  const presentation = { page: page as string, usesSidebar: true, repo: undefined as string | undefined };
  const commands = vi.spyOn(runtime, "runCommand");
  const { stores } = createAppStores({
    runtime,
    getPage: () => presentation.page,
    hostState: { getGlobalRepo: () => presentation.repo },
    getPullListUsesSidebar: () => presentation.usesSidebar,
  });
  const context = new Map<symbol, unknown>([
    [STORES_KEY, stores],
    [NAVIGATE_KEY, vi.fn()],
    [HOST_STATE_KEY, {}],
    [SIDEBAR_KEY, { toggleSidebar: vi.fn() }],
  ]);
  const view = sidebar
    ? render(AppRuntimeHarness, { props: { component: page === "pulls" ? PullList : IssueList }, context })
    : render(FocusListView, { props: { listType: page === "pulls" ? "mrs" : "issues" }, context });
  await waitFor(() => expect(list).toHaveBeenCalledTimes(1));
  const input = screen.getByRole("searchbox");
  const load = () => (page === "pulls" ? stores.pulls.loadPulls() : stores.issues.loadIssues());
  const loading = () => (page === "pulls" ? stores.pulls.isLoading() : stores.issues.isIssuesLoading());
  const search = (value = "zzz") => fireEvent.input(input, { target: { value } });
  return { sent, state, list, stores, view, context, presentation, commands, load, loading, search };
}

it.each(["pulls", "issues"] as const)(
  "counts %s text searches while display filters and passive reloads stay silent",
  async (page) => {
    const { sent, stores, presentation, search, load, loading } = await setup(page);
    expect(sent).not.toHaveBeenCalled();
    for (const value of ["z", "zz", "zzz"]) await search(value);
    expect(sent).not.toHaveBeenCalled();
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(1));
    expect(sent.mock.calls[0]?.[0]).toEqual({ event: "search_empty", properties: { surface: "web" } });
    load();
    await waitFor(() => expect(loading()).toBe(false));
    expect(sent).toHaveBeenCalledTimes(1);
    if (page === "pulls") stores.pulls.toggleAttributeFilter("draft");
    else stores.issues.setHideBots(true);
    await tick();
    expect(sent).toHaveBeenCalledTimes(1);
    presentation.repo = "github:github.com/owner/project";
    load();
    await waitFor(() => expect(loading()).toBe(false));
    expect(sent).toHaveBeenCalledTimes(1);
    await search("zzz");
    load();
    await waitFor(() => expect(loading()).toBe(false));
    expect(sent).toHaveBeenCalledTimes(1);
    stores.sync.triggerSync();
    load();
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(2));
    await search("another query");
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(3));
  },
);

it.each(["pulls", "issues"] as const)("retains a failed %s search until a successful retry", async (page) => {
  const { sent, state, stores, search, load, loading } = await setup(page);
  state.failed = true;
  await search();
  await waitFor(() => expect(page === "pulls" ? stores.pulls.getError() : stores.issues.getIssuesError()).toBeTruthy());
  await waitFor(() => expect(loading()).toBe(false));
  expect(sent).not.toHaveBeenCalled();
  state.failed = false;
  load();
  await waitFor(() => expect(sent).toHaveBeenCalledTimes(1));
});

it.each(["pulls", "issues"] as const)("ignores superseded and dismissed %s search responses", async (page) => {
  const { sent, state, list, search, view, loading } = await setup(page);
  for (const dismiss of [false, true]) {
    const result = Promise.withResolvers<[]>();
    state.pending = result.promise;
    const query = dismiss ? "departed" : "superseded";
    await search(query);
    await waitFor(() => expect(list).toHaveBeenCalledWith(expect.objectContaining({ q: query }), expect.anything()));
    if (dismiss) view.unmount();
    else await search("");
    state.pending = undefined;
    result.resolve([]);
    await waitFor(() => expect(loading()).toBe(false));
    expect(sent).not.toHaveBeenCalled();
  }
});

it("sends no empty-search event while Focus displays rows hidden by sidebar filters", async () => {
  const { sent, state, stores, presentation, search, commands, loading } = await setup("pulls");
  const response = await createMockApiFetch().fetch(new URL("/api/v1/pulls", location.origin));
  state.rows = await response.json();
  stores.pulls.toggleAttributeFilter("draft");
  presentation.usesSidebar = false;
  for (const page of ["pulls", "focus"]) {
    presentation.page = page;
    await search("theme");
    await screen.findByText("Refactor theme system");
    await waitFor(() => expect(loading()).toBe(false));
    expect(stores.pulls.getFilteredPulls()).toEqual([]);
    await tick();
    await Promise.all(commands.mock.results.map((result) => result.value.exit));
    expect(sent).not.toHaveBeenCalled();
  }
});

it.each(["pulls", "issues"] as const)("settles a pending %s search after a local filter changes", async (page) => {
  const { sent, state, stores, list, search, loading } = await setup(page);
  const result = Promise.withResolvers<[]>();
  state.pending = result.promise;
  await search();
  await waitFor(() => expect(list).toHaveBeenCalledWith(expect.objectContaining({ q: "zzz" }), expect.anything()));
  if (page === "pulls") stores.pulls.toggleAttributeFilter("draft");
  else stores.issues.setHideBots(true);
  state.pending = undefined;
  result.resolve([]);
  await waitFor(() => expect(loading()).toBe(false));
  await waitFor(() => expect(sent).toHaveBeenCalledTimes(1));
});

it.each([
  ["pulls", false],
  ["issues", false],
  ["pulls", true],
  ["issues", true],
] as const)("preserves the pending %s query after whitespace edits in sidebar %s", async (page, sidebar) => {
  const { sent, state, list, search, loading } = await setup(page, sidebar);
  const result = Promise.withResolvers<[]>();
  state.pending = result.promise;
  await search("missing");
  await waitFor(() => expect(list).toHaveBeenCalledWith(expect.objectContaining({ q: "missing" }), expect.anything()));
  await search("missing ");
  result.resolve([]);
  await waitFor(() => expect(loading()).toBe(false));
  await waitFor(() => expect(sent).toHaveBeenCalledTimes(1));
});
