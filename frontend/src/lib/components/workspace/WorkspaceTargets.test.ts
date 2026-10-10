import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { WorkspaceTarget } from "../../api/generated/models/workspaceTarget.js";
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
  let targets = [
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
      targets = targets.filter((target) => target.number !== 1);
      return new Response(null, { status: 204 });
    }
    return Response.json({ targets, kata_available: false });
  });
  const onselect = vi.fn();
  render(WorkspaceTargets, { props: { workspaceID: "ws-1", workspaceHostKey, onselect } });
  await waitFor(() => expect(screen.getByRole("button", { name: /^Targets/ }).textContent).toContain("2"));
  await fireEvent.click(screen.getByRole("button", { name: /^Targets/ }));
  expect(screen.getByText("Original issue")).toBeTruthy();
  expect(screen.getByText("Unavailable pull")).toBeTruthy();
  expect(screen.queryByText("Completed issue")).toBeNull();
  expect(screen.queryByText("Merged pull")).toBeNull();
  await fireEvent.click(screen.getByRole("checkbox", { name: "Show closed" }));
  expect(screen.getByRole("button", { name: /^Targets/ }).getAttribute("aria-expanded")).toBe("true");
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

it("names targets with their visible title, repository or daemon, and state", async () => {
  const targets = [
    ...["widgets", "tools"].map((name): WorkspaceTarget => ({
      id: 1,
      type: "issue",
      number: 7,
      state: "open",
      title: "Fix startup",
      source: "tracked",
      unavailable: false,
      url: "",
      repo: {
        provider: "github",
        platform_host: "github.com",
        platform_repo_id: name === "widgets" ? 101 : 202,
        owner: "acme",
        name,
        repo_path: `acme/${name}`,
      },
    })),
    {
      id: 3,
      type: "issue",
      number: 8,
      state: "",
      title: "",
      source: "tracked",
      unavailable: true,
      url: "https://github.com/acme/widgets/issues/8",
      repo: {
        provider: "github",
        platform_host: "github.com",
        platform_repo_id: 101,
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
    },
    ...["work", "personal"].map((daemon_id): WorkspaceTarget => ({
      id: 4,
      type: "kata",
      number: 0,
      state: "open",
      title: "",
      source: "tracked",
      unavailable: false,
      url: "",
      kata: { daemon_id, project_uid: "project-1", issue_uid: "task-1", reference: "TASK-1" },
    })),
  ];
  vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ targets, kata_available: true }));
  const onselect = vi.fn();
  render(WorkspaceTargets, { props: { workspaceID: "ws-1", onselect } });
  await fireEvent.click(await screen.findByRole("button", { name: /^Targets/ }));
  await fireEvent.click(await screen.findByRole("button", { name: "Fix startup Issue #7 acme/tools open" }));
  expect(onselect).toHaveBeenCalledWith(targets[1]);
  expect(screen.getByRole("button", { name: "Fix startup Issue #7 acme/widgets open" })).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "https://github.com/acme/widgets/issues/8 Issue #8 acme/widgets Unavailable" }),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Linked task TASK-1 work open" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Linked task TASK-1 personal open" })).toBeTruthy();
});

it("refreshes a devbox target list after removal and discards an older response", async () => {
  const target: WorkspaceTarget = {
    id: 1,
    type: "issue",
    number: 7,
    state: "open",
    title: "Dismissed issue",
    source: "tracked",
    unavailable: false,
    url: "",
    repo: {
      provider: "github",
      platform_host: "github.com",
      platform_repo_id: 101,
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  };
  let savedTargets = [target, { ...target, id: 2, number: 8, title: "Remaining issue" }];
  const delayedRead = Promise.withResolvers<Response>();
  let reads = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    expect(new URL(request.url).pathname).toBe("/api/v1/devboxes/worker-a/workspaces/ws-1/targets");
    if (request.method === "PUT") {
      expect(await request.json()).toEqual({
        repository: { provider: "github", platform_host: "github.com", platform_repo_id: 101 },
        type: "issue",
        number: 7,
        hidden: true,
      });
      savedTargets = [{ ...savedTargets[1]!, title: "Updated remaining issue" }];
      return new Response(null, { status: 204 });
    }
    reads++;
    if (reads === 2) return delayedRead.promise;
    return Response.json({ targets: savedTargets, kata_available: false });
  });
  const props = { workspaceID: "ws-1", workspaceHostKey: "devbox:worker-a", onselect: vi.fn() };
  const view = render(WorkspaceTargets, { props });
  await fireEvent.click(await screen.findByRole("button", { name: /^Targets/ }));
  await screen.findByText("Dismissed issue");
  await view.rerender({ ...props, refreshToken: 1 });
  await waitFor(() => expect(reads).toBe(2));
  await fireEvent.click(screen.getByRole("button", { name: "Remove Issue #7 from targets" }));
  await waitFor(() => expect(screen.queryByText("Dismissed issue")).toBeNull());

  delayedRead.resolve(Response.json({ targets: [target], kata_available: false }));
  await screen.findByText("Updated remaining issue");
  expect(screen.queryByText("Dismissed issue")).toBeNull();
  expect(screen.getByRole("button", { name: /^Targets/ }).textContent).toContain("1");
});
