import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { createAppStores } from "../../app-stores.svelte.js";
import { makeAppRuntime } from "../../app/runtime.js";
import { STORES_KEY } from "../../context.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import WorkspaceItemSearch from "./WorkspaceItemSearch.svelte";

const runtime = makeAppRuntime();
vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtime }));

afterEach(async () => {
  cleanup();
  await Effect.runPromise(runtime.disposeEffect);
  vi.unstubAllGlobals();
});

it("reopens and filters the shared open-item list without waiting for requests", async () => {
  const api = createMockApiFetch();
  let firstPullUnavailable = true;
  vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) => {
    if (firstPullUnavailable && (args[0] instanceof Request ? args[0].url : String(args[0])).includes("/pulls")) {
      firstPullUnavailable = false;
      return Promise.resolve(Response.json({ code: "serviceUnavailable", status: 503 }, { status: 503 }));
    }
    return api.fetch(...args);
  });
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
  const first = render(WorkspaceItemSearch, { props, context });
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ }, { timeout: 5000 });
  expect(firstPullUnavailable).toBe(false);
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

  vi.stubGlobal("fetch", async () =>
    Response.json({ type: "about:blank", code: "forbidden", status: 403, detail: "denied" }, { status: 403 }),
  );
  await fireEvent.click(screen.getByRole("checkbox", { name: "Include closed" }));
  await screen.findByRole("alert", {}, { timeout: 5000 });
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
  expect((screen.getByRole("combobox") as HTMLInputElement).value).toBe("theme system");
  expect((screen.getByRole("checkbox", { name: "Include closed" }) as HTMLInputElement).checked).toBe(true);

  vi.stubGlobal("fetch", async () =>
    Response.json({ type: "about:blank", code: "forbidden", status: 403, detail: "denied" }, { status: 403 }),
  );
  await fireEvent.input(screen.getByRole("combobox"), { target: { value: "theme system" } });
  await screen.findByRole("alert", {}, { timeout: 5000 });
  expect(screen.getByRole("option", { name: /#55.*Refactor theme system/ })).toBeTruthy();
  await fireEvent.input(screen.getByRole("combobox"), { target: { value: "no matching item" } });
  await screen.findByRole("alert", {}, { timeout: 5000 });
  expect(screen.queryByRole("option", { name: /#55/ })).toBeNull();
  expect((screen.getByRole("combobox") as HTMLInputElement).value).toBe("no matching item");
}, 10000);
