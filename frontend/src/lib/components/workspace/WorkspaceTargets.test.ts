import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import WorkspaceTargets from "./WorkspaceTargets.svelte";

let runtime: OwnedAppRuntime;
vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtime }));
beforeEach(() => {
  runtime = makeAppRuntime();
});
afterEach(async () => {
  cleanup();
  await Effect.runPromise(runtime.disposeEffect);
  vi.restoreAllMocks();
});

it.each([
  [undefined, "/api/v1/workspaces/ws-1/targets"],
  ["peer-a", "/api/v1/fleet/hosts/peer-a/workspaces/ws-1/targets"],
  ["devbox:worker-a", "/api/v1/devboxes/worker-a/workspaces/ws-1/targets"],
])("filters closed targets and removes an implicit target on %s", async (workspaceHostKey, expectedPath) => {
  const repo = {
    provider: "github",
    platform_host: "github.com",
    platform_repo_id: 101,
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  };
  const targets = [
    {
      id: 0,
      type: "issue",
      number: 1,
      state: "open",
      title: "Original issue",
      source: "owner",
      repo,
      unavailable: false,
      url: "",
    },
    {
      id: 2,
      type: "issue",
      number: 2,
      state: "closed",
      title: "Completed issue",
      source: "tracked",
      repo,
      unavailable: false,
      url: "",
    },
    {
      id: 3,
      type: "pr",
      number: 3,
      state: "merged",
      title: "Merged pull",
      source: "tracked",
      repo,
      unavailable: false,
      url: "",
    },
    {
      id: 4,
      type: "pr",
      number: 4,
      state: "",
      title: "Unavailable pull",
      source: "tracked",
      repo,
      unavailable: true,
      url: "",
    },
  ];
  const writes: unknown[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    if (request.method === "PUT") {
      expect(new URL(request.url).pathname).toBe(expectedPath);
      writes.push(await request.json());
      return new Response(null, { status: 204 });
    }
    return Response.json({ targets, kata_available: false });
  });
  const onselect = vi.fn();
  render(WorkspaceTargets, { props: { workspaceID: "ws-1", workspaceHostKey, onselect } });
  await waitFor(() => expect(screen.getByText("Targets").textContent).toContain("2"));
  screen.getByText("Targets").closest("details")!.open = true;
  expect(screen.getByText("Original issue")).toBeTruthy();
  expect(screen.getByText("Unavailable pull")).toBeTruthy();
  expect(screen.queryByText("Completed issue")).toBeNull();
  expect(screen.queryByText("Merged pull")).toBeNull();
  await fireEvent.click(screen.getByRole("checkbox", { name: "Show closed" }));
  expect(screen.getByText("Completed issue")).toBeTruthy();
  expect(screen.getByText("Merged pull")).toBeTruthy();
  await fireEvent.click(screen.getByRole("button", { name: "Remove Issue #1 from targets" }));
  await waitFor(() => expect(screen.queryByText("Original issue")).toBeNull());
  expect(onselect).not.toHaveBeenCalled();
  expect(writes).toEqual([
    {
      repository: { provider: "github", platform_host: "github.com", platform_repo_id: 101 },
      type: "issue",
      number: 1,
      hidden: true,
    },
  ]);
});
