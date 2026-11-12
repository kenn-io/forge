import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { createAppStores } from "../../app-stores.svelte.js";
import { makeAppRuntime } from "../../app/runtime.js";
import { STORES_KEY } from "../../context.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import WorkspaceItemSearch from "./WorkspaceItemSearch.svelte";

let runtime: ReturnType<typeof makeAppRuntime>;
beforeEach(() => {
  runtime = makeAppRuntime();
});
vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtime }));

afterEach(async () => {
  cleanup();
  await Effect.runPromise(runtime.disposeEffect);
  vi.unstubAllGlobals();
});

function setupSearch() {
  const { stores } = createAppStores({ runtime });
  const props = {
    workspaceID: "ws-search",
    viewedPR: null,
    viewedIssue: null,
    hasLinkedPR: false,
    hasLinkedIssue: false,
    disabled: false,
    searchAnchor: document.createElement("button"),
    onSearchClose: vi.fn(),
    onselect: vi.fn(),
  };
  const context = new Map([[STORES_KEY, stores]]);
  return { stores, props, context };
}

it("reopens and filters cached search results", async () => {
  const api = createMockApiFetch();
  vi.stubGlobal("fetch", api.fetch);
  const { props, context } = setupSearch();
  const first = render(WorkspaceItemSearch, { props, context });
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ }, { timeout: 5000 });
  first.unmount();
  const requestCount = api.requests.length;

  render(WorkspaceItemSearch, { props, context });
  expect(screen.queryByRole("status")).toBeNull();
  expect(screen.getByRole("option", { name: /#55.*Refactor theme system/ })).toBeTruthy();
  for (const query of ["luisa widgets", '"theme system"', "ReFaCtOr", "theme%system", "#0055"]) {
    await fireEvent.input(screen.getByRole("combobox"), { target: { value: query } });
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option", { name: /#55.*Refactor theme system/ })).toBeTruthy();
  }
  await fireEvent.keyDown(screen.getByRole("combobox"), { key: "Enter" });
  expect(props.onselect).toHaveBeenCalledWith("pr", expect.objectContaining({ number: 55, repoPath: "acme/widgets" }));
  expect(api.requests).toHaveLength(requestCount);
});

it("recovers failed open searches", async () => {
  const api = createMockApiFetch();
  let firstPullUnavailable = true;
  vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) => {
    if (firstPullUnavailable && (args[0] instanceof Request ? args[0].url : String(args[0])).includes("/pulls")) {
      firstPullUnavailable = false;
      return Promise.resolve(Response.json({ code: "serviceUnavailable", status: 503 }, { status: 503 }));
    }
    return api.fetch(...args);
  });
  const { stores, props, context } = setupSearch();
  render(WorkspaceItemSearch, { props, context });
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ }, { timeout: 5000 });
  expect(firstPullUnavailable).toBe(false);
  const requestCount = api.requests.length;

  vi.stubGlobal("fetch", async () => {
    throw new TypeError("offline");
  });
  stores.workspaceItemSearch.markStale();
  expect(api.requests).toHaveLength(requestCount);
  await fireEvent.input(screen.getByRole("combobox"), { target: { value: "theme system" } });
  await screen.findByRole("alert", {}, { timeout: 5000 });
  expect(screen.getByRole("option", { name: /#55.*Refactor theme system/ })).toBeTruthy();
  expect(screen.queryByRole("status")).toBeNull();
  expect(screen.getByRole("alert")).toBeTruthy();
  const updatedPull = { ...stores.workspaceItemSearch.search("#55").pulls[0]!, Title: "Updated theme system" };
  vi.stubGlobal(
    "fetch",
    createMockApiFetch([({ url }) => (url.pathname === "/api/v1/pulls" ? Response.json([updatedPull]) : undefined)])
      .fetch,
  );
  await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByRole("option", { name: /#55.*Updated theme system/ });
  expect(screen.queryByRole("alert")).toBeNull();
}, 10000);

it.each([503, 403, 504])(
  "classifies a %s response for open searches",
  async (status) => {
    const api = createMockApiFetch();
    let pulls = 0;
    vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) => {
      if ((args[0] instanceof Request ? args[0].url : String(args[0])).includes("/pulls")) {
        pulls++;
        if (status === 403 || pulls === 1) {
          return Promise.resolve(
            status === 504
              ? Response.json({ code: "internalError", status: 504 }, { status })
              : new Response("{", { status, headers: { "Content-Type": "application/json" } }),
          );
        }
      }
      return api.fetch(...args);
    });
    const { props, context } = setupSearch();
    render(WorkspaceItemSearch, { props, context });
    if (status !== 403) {
      await screen.findByRole("option", { name: /#55.*Refactor theme system/ }, { timeout: 5000 });
      expect(screen.queryByRole("alert")).toBeNull();
      expect(pulls).toBe(2);
    } else {
      await screen.findByRole("alert", {}, { timeout: 5000 });
      expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
      expect(pulls).toBe(1);
    }
  },
  10000,
);

it("preserves the query when retrying closed searches", async () => {
  const api = createMockApiFetch();
  vi.stubGlobal("fetch", api.fetch);
  const { props, context } = setupSearch();
  render(WorkspaceItemSearch, { props, context });
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ });
  await fireEvent.input(screen.getByRole("combobox"), { target: { value: "theme system" } });
  vi.stubGlobal("fetch", async () =>
    Response.json({ type: "about:blank", code: "forbidden", status: 403, detail: "denied" }, { status: 403 }),
  );
  await fireEvent.click(screen.getByRole("checkbox", { name: "Include closed" }));
  await screen.findByRole("alert");
  const closedApi = createMockApiFetch();
  let unavailable = true;
  vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) => {
    if (unavailable && (args[0] instanceof Request ? args[0].url : String(args[0])).includes("/pulls")) {
      unavailable = false;
      return Promise.resolve(Response.json({ code: "serviceUnavailable", status: 503 }, { status: 503 }));
    }
    return closedApi.fetch(...args);
  });
  await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ });
  expect(unavailable).toBe(false);
  for (const path of ["/api/v1/pulls", "/api/v1/issues"]) {
    expect(closedApi.requests.find(({ url }) => url.pathname === path)?.url.searchParams.get("q")).toBe("theme system");
  }
  expect((screen.getByRole("checkbox", { name: "Include closed" }) as HTMLInputElement).checked).toBe(true);

  vi.stubGlobal("fetch", async () =>
    Response.json({ type: "about:blank", code: "forbidden", status: 403, detail: "denied" }, { status: 403 }),
  );
  await fireEvent.input(screen.getByRole("combobox"), { target: { value: "theme system" } });
  await screen.findByRole("alert");
  expect(screen.queryByRole("option", { name: /#55.*Refactor theme system/ })).toBeNull();
  expect(screen.queryByText("No PRs or issues found")).toBeNull();
}, 10000);
