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
  vi.useRealTimers();
});

it("reopens and filters the shared open-item list without waiting for requests", async () => {
  const api = createMockApiFetch();
  vi.stubGlobal("fetch", api.fetch);
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
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ });
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
  await runtime.runCommand(stores.workspaceItemSearch.refreshEffect, {
    operation: "test failed background refresh",
    safeContext: {},
    onFailure: () => {},
  }).exit;
  expect(screen.getByRole("option", { name: /#55.*Refactor theme system/ })).toBeTruthy();
  expect(screen.queryByRole("status")).toBeNull();
  expect(screen.getByRole("alert")).toBeTruthy();
  const updatedPull = { ...stores.workspaceItemSearch.search("#55").pulls[0]!, Title: "Updated theme system" };
  vi.stubGlobal(
    "fetch",
    createMockApiFetch([({ url }) => (url.pathname === "/api/v1/pulls" ? Response.json([updatedPull]) : undefined)])
      .fetch,
  );
  await fireEvent.input(screen.getByRole("combobox"), { target: { value: "theme system" } });
  await screen.findByRole("option", { name: /#55.*Updated theme system/ });
  expect(screen.queryByRole("alert")).toBeNull();
});

it("counts one completed empty search while filtering each input immediately", async () => {
  const events: unknown[] = [];
  const api = createMockApiFetch([
    ({ url, bodyText }) => {
      if (url.pathname !== "/api/v1/telemetry/events") return undefined;
      events.push(JSON.parse(bodyText));
      return Response.json({ status: "queued" });
    },
  ]);
  vi.stubGlobal("fetch", api.fetch);
  const { stores } = createAppStores({ runtime });
  render(WorkspaceItemSearch, {
    props: {
      workspaceID: "ws-search",
      viewedPR: null,
      viewedIssue: null,
      hasLinkedPR: false,
      hasLinkedIssue: false,
      disabled: false,
      searchAnchor: document.createElement("button"),
      onSearchClose: vi.fn(),
      onselect: vi.fn(),
    },
    context: new Map([[STORES_KEY, stores]]),
  });
  await screen.findByRole("option", { name: /#55.*Refactor theme system/ });
  vi.useFakeTimers();
  for (const query of ["absent a", "absent b", "absent c"]) {
    await fireEvent.input(screen.getByRole("combobox"), { target: { value: query } });
    expect(screen.queryByRole("option")).toBeNull();
    await vi.advanceTimersByTimeAsync(100);
    expect(events).toEqual([]);
  }
  await vi.advanceTimersByTimeAsync(500);
  expect(events).toEqual([]);
  await fireEvent.change(screen.getByRole("combobox"));
  await vi.advanceTimersByTimeAsync(0);
  expect(events).toEqual([{ event: "search_empty", properties: { surface: "web" } }]);
});
