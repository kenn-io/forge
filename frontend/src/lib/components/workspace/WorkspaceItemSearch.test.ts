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
