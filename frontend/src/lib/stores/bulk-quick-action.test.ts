import { Effect } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import { makeAppRuntime } from "../app/runtime.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { startTestTelemetry } from "../testing/telemetry.js";
import { getFlashes, dismissFlash } from "./flash.svelte.js";
import { runLinkedRemoteItemQuickAction, runQuickActionOnItems } from "./bulk-quick-action.js";
import {
  createdWorkspaceRef,
  recordWorkspaceCreated,
  resetWorkspaceCreatePendingForTest,
} from "./workspace-create-pending.svelte.js";
import { getInlineWorkspaceController, resetWorkspaceHostForTest } from "./workspace-host.svelte.js";

afterEach(() => {
  resetWorkspaceCreatePendingForTest();
  resetWorkspaceHostForTest();
});

it.each(["partial", "all"])("counts one error flash for %s bulk workspace creation failures", async (failure) => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  let creates = 0;
  const handoff = vi.fn();
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces") {
        creates++;
        return failure === "partial" && creates === 1
          ? Response.json({ id: "ws-partial", status: "ready" })
          : Response.json({ code: "forbidden", title: "Create denied", status: 403 }, { status: 403 });
      }
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-partial/runtime/agent-handoffs") {
        handoff();
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
        });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  try {
    runQuickActionOnItems(
      runtime,
      pulls.slice(0, 2).map((item) => ({ kind: "pull", item: { ...item, workspace: undefined } })),
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() =>
      expect(getFlashes()).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            tone: failure === "partial" ? "warning" : "danger",
            message: expect.stringContaining(failure === "partial" ? "started 1, failed 1" : "failed 2"),
          }),
        ]),
      ),
    );
    expect(creates).toBe(2);
    expect(handoff).toHaveBeenCalledTimes(failure === "partial" ? 1 : 0);
    await vi.waitFor(() =>
      expect(sent).toHaveBeenCalledExactlyOnceWith(
        { event: "error_shown", properties: { surface: "web", kind: "workspace_opened" } },
        {},
      ),
    );
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("reports a rejected workspace retry and still runs the other selected items", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const handoffs: string[] = [];
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-failed/retry") {
        return Response.json(
          { code: "workspaceNotFound", title: "Not Found", detail: "workspace not found", status: 404 },
          { status: 404 },
        );
      }
      if (method === "POST" && url.pathname === "/api/v1/workspaces") {
        return Response.json({ id: "ws-new", status: "creating" });
      }
      const handoff = url.pathname.match(/^\/api\/v1\/workspaces\/([^/]+)\/runtime\/agent-handoffs$/);
      if (method === "POST" && handoff) {
        handoffs.push(handoff[1] ?? "");
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
        });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  const [failed, fresh] = pulls;
  if (!failed || !fresh) throw new Error("fixture needs two pull requests");
  try {
    runQuickActionOnItems(
      runtime,
      [
        { kind: "pull", item: { ...failed, workspace: { id: "ws-failed", status: "error" } } },
        { kind: "pull", item: { ...fresh, workspace: undefined } },
      ],
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() =>
      expect(getFlashes()).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            tone: "warning",
            message: expect.stringContaining("started 1, failed 1"),
          }),
        ]),
      ),
    );
    expect(
      getFlashes()
        .map((flash) => flash.message)
        .join(" "),
    ).toContain("workspace not found");
    expect(handoffs).toEqual(["ws-new"]);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("launches in a retried workspace in the order the actions were run", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const prompts: string[] = [];
  // Like the server, a retry starts only from the error state and is
  // rejected once the workspace is ready again.
  let status = "error";
  const mixed = createMockApiFetch([
    ({ method, url, bodyText }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-failed/retry") {
        if (status === "ready") {
          return Response.json(
            { code: "conflict", title: "Conflict", detail: "workspace is not in error status", status: 409 },
            { status: 409 },
          );
        }
        status = "creating";
        return Response.json({ id: "ws-failed", status });
      }
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-failed/runtime/agent-handoffs") {
        status = "ready";
        prompts.push((JSON.parse(bodyText) as { message: string }).message);
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 5 },
        });
      }
      return undefined;
    },
  ]);
  // The first run's retry answers only after the second run's retry would have.
  const firstRetry = Promise.withResolvers<void>();
  let retries = 0;
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.pathname.endsWith("/retry") && ++retries === 1) await firstRetry.promise;
    return mixed.fetch(input, init);
  });
  const runtime = makeAppRuntime();
  const [pull] = pulls;
  if (!pull) throw new Error("fixture needs a pull request");
  const target = { kind: "pull" as const, item: { ...pull, workspace: { id: "ws-failed", status: "error" } } };
  try {
    runQuickActionOnItems(runtime, [target], { label: "First", agent: "helper", prompt: "first" }, "");
    runQuickActionOnItems(runtime, [target], { label: "Second", agent: "helper", prompt: "second" }, "");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(prompts).toEqual([]);
    firstRetry.resolve();
    await vi.waitFor(() => expect(prompts).toEqual(["first", "second"]));
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("creates on the fleet member the saved default names, or reports it unavailable", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const node = "b".repeat(32);
  for (const available of [true, false]) {
    const creates: string[] = [];
    const handoffs: string[] = [];
    const mixed = createMockApiFetch([
      ({ method, url }) => {
        if (method === "GET" && url.pathname === "/api/v1/snapshot") {
          return Response.json({
            hosts: [
              {
                configKey: node,
                kind: "remote",
                name: "build-b",
                baseURL: "https://build-b.example.test",
                operationAvailability: {
                  workspaceWrite: available ? { available } : { available, unavailableReason: "Host is offline." },
                },
              },
            ],
          });
        }
        if (method === "POST" && url.pathname.endsWith("/workspaces")) {
          creates.push(url.pathname);
          return Response.json({ id: "ws-remote", status: "ready" });
        }
        if (method === "POST" && url.pathname.endsWith("/runtime/agent-handoffs")) {
          handoffs.push(url.pathname);
          return Response.json({
            session: { key: "runtime-1", target_key: "helper" },
            initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
          });
        }
        return undefined;
      },
    ]);
    vi.stubGlobal("fetch", mixed.fetch);
    const runtime = makeAppRuntime();
    try {
      runQuickActionOnItems(
        runtime,
        [{ kind: "pull", item: { ...pulls[0]!, workspace: undefined } }],
        { label: "Review", agent: "helper", prompt: "Review this change" },
        "https://build-b.example.test",
      );
      await vi.waitFor(() =>
        expect(getFlashes()).toEqual([
          expect.objectContaining({
            message: available
              ? expect.stringContaining('Started "Review" in 1 workspace')
              : expect.stringContaining("your default machine, build-b, is unavailable: Host is offline."),
          }),
        ]),
      );
      expect(creates).toEqual(available ? [`/api/v1/fleet/hosts/${node}/workspaces`] : []);
      expect(handoffs).toEqual(
        available ? [`/api/v1/fleet/hosts/${node}/workspaces/ws-remote/runtime/agent-handoffs`] : [],
      );
    } finally {
      await Effect.runPromise(runtime.disposeEffect);
      for (const flash of getFlashes()) dismissFlash(flash.id);
      vi.unstubAllGlobals();
      // Each pass starts with no workspace linked to the item.
      resetWorkspaceCreatePendingForTest();
      resetWorkspaceHostForTest();
    }
  }
});

it("refuses the existing workspace another machine reports while it is being deleted", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const pull = pulls[0]!;
  const node = "b".repeat(32);
  const handoffs: string[] = [];
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method === "GET" && url.pathname === "/api/v1/snapshot") {
        return Response.json({
          hosts: [
            {
              configKey: node,
              kind: "remote",
              name: "build-b",
              baseURL: "https://build-b.example.test",
              operationAvailability: { workspaceWrite: { available: true } },
            },
          ],
          workspaces: [
            {
              id: "ws-going",
              status: "deletion_failed",
              fleet_host_key: node,
              item_type: "pull_request",
              item_number: pull.Number,
              platform_host: pull.repo.platform_host,
              repo_owner: pull.repo.owner,
              repo_name: pull.repo.name,
              repo: pull.repo,
            },
          ],
        });
      }
      if (method === "POST" && url.pathname === `/api/v1/fleet/hosts/${node}/workspaces`) {
        return Response.json({ code: "workspaceAlreadyExists", title: "Conflict", status: 409 }, { status: 409 });
      }
      if (method === "POST" && url.pathname.endsWith("/runtime/agent-handoffs")) {
        handoffs.push(url.pathname);
        return Response.json({});
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  try {
    runQuickActionOnItems(
      runtime,
      [{ kind: "pull", item: { ...pull, workspace: undefined } }],
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "https://build-b.example.test",
    );
    await vi.waitFor(() =>
      expect(getFlashes()).toEqual([
        expect.objectContaining({ tone: "danger", message: expect.stringContaining("its workspace is being deleted") }),
      ]),
    );
    expect(handoffs).toEqual([]);
    const identity = {
      provider: pull.repo.provider,
      platformHost: pull.repo.platform_host,
      owner: pull.repo.owner,
      name: pull.repo.name,
      repoPath: pull.repo.repo_path,
      number: pull.Number,
      itemType: "pull",
    } as const;
    expect(createdWorkspaceRef(identity)).toBeNull();
    expect(getInlineWorkspaceController("prs").effectiveWorkspaceRef(identity, null)).toBeNull();
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("launches in a workspace this session created on another machine, and asks that machine again when its setup failed", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const pull = pulls[0]!;
  const posts: string[] = [];
  let handoffs = 0;
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method !== "POST") return undefined;
      posts.push(url.pathname);
      if (url.pathname === "/api/v1/devboxes/compute-a/workspaces") {
        return Response.json({ id: "ws-remote", status: "error", reused: true });
      }
      if (url.pathname.endsWith("/runtime/agent-handoffs") && handoffs++ === 0) {
        return Response.json(
          { code: "conflict", title: "Conflict", detail: "workspace setup failed: clone failed", status: 409 },
          { status: 409 },
        );
      }
      if (url.pathname === "/api/v1/devboxes/compute-a/workspaces/ws-remote/retry") {
        return Response.json({ id: "ws-remote", status: "creating" });
      }
      if (url.pathname.endsWith("/runtime/agent-handoffs")) {
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
        });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  // The detail's create recorded the workspace with the stale status its
  // response carried; only the devbox knows its setup failed since.
  recordWorkspaceCreated(
    {
      provider: pull.repo.provider,
      platformHost: pull.repo.platform_host,
      owner: pull.repo.owner,
      name: pull.repo.name,
      repoPath: pull.repo.repo_path,
      number: pull.Number,
      itemType: "pull",
    },
    { id: "ws-remote", status: "creating", hostKey: "devbox:compute-a" },
  );
  try {
    runQuickActionOnItems(
      runtime,
      [{ kind: "pull", item: { ...pull, workspace: undefined } }],
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() => expect(getFlashes()).toEqual([expect.objectContaining({ tone: "success" })]));
    expect(posts).toEqual([
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime/agent-handoffs",
      "/api/v1/devboxes/compute-a/workspaces",
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/retry",
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime/agent-handoffs",
    ]);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("links the item to the workspace its machine makes when the old one was deleted there", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const pull = pulls[0]!;
  const posts: string[] = [];
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method !== "POST") return undefined;
      posts.push(url.pathname);
      if (url.pathname === "/api/v1/devboxes/compute-a/workspaces/ws-deleted/runtime/agent-handoffs") {
        return Response.json(
          { code: "workspaceNotFound", title: "Not Found", detail: "workspace not found", status: 404 },
          { status: 404 },
        );
      }
      if (url.pathname === "/api/v1/devboxes/compute-a/workspaces") {
        return Response.json({ id: "ws-replacement", status: "creating", created: true });
      }
      if (url.pathname.endsWith("/runtime/agent-handoffs")) {
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
        });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  const identity = {
    provider: pull.repo.provider,
    platformHost: pull.repo.platform_host,
    owner: pull.repo.owner,
    name: pull.repo.name,
    repoPath: pull.repo.repo_path,
    number: pull.Number,
    itemType: "pull",
  } as const;
  const deleted = { id: "ws-deleted", status: "ready", hostKey: "devbox:compute-a", hostLabel: "compute-a (devbox)" };
  recordWorkspaceCreated(identity, deleted);
  getInlineWorkspaceController("prs").recordCreated(identity, deleted);
  try {
    runQuickActionOnItems(
      runtime,
      [{ kind: "pull", item: { ...pull, workspace: undefined } }],
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() => expect(getFlashes()).toEqual([expect.objectContaining({ tone: "success" })]));
    const replacement = {
      id: "ws-replacement",
      status: "creating",
      hostKey: "devbox:compute-a",
      hostLabel: "compute-a (devbox)",
    };
    expect(createdWorkspaceRef(identity)).toEqual(replacement);
    expect(getInlineWorkspaceController("prs").effectiveWorkspaceRef(identity, null)).toEqual(replacement);
    expect(posts).toEqual([
      "/api/v1/devboxes/compute-a/workspaces/ws-deleted/runtime/agent-handoffs",
      "/api/v1/devboxes/compute-a/workspaces",
      "/api/v1/devboxes/compute-a/workspaces/ws-replacement/runtime/agent-handoffs",
    ]);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

describe("quick actions in an item's workspace on another machine", () => {
  const identity = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
    number: 7,
    itemType: "pull",
  } as const;
  const request = {
    kind: "pull",
    repo: { provider: "github", platformHost: "github.com", owner: "acme", name: "widgets" },
    number: 7,
  } as const;
  const linked = { id: "ws-old", status: "ready", hostKey: "devbox:compute-a", hostLabel: "compute-a (devbox)" };
  const action = { label: "Review", agent: "helper", prompt: "Review this change" };
  const delivered = () =>
    Response.json({
      session: { key: "runtime-1", target_key: "helper" },
      initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
    });
  const problem = (status: number, code: string, detail: string) =>
    Response.json({ code, title: "Failed", detail, status }, { status });

  function run(respond: (path: string) => Response | undefined) {
    const posts: string[] = [];
    const mixed = createMockApiFetch([
      ({ method, url }) => {
        if (method !== "POST") return undefined;
        posts.push(url.pathname);
        return respond(url.pathname);
      },
    ]);
    vi.stubGlobal("fetch", mixed.fetch);
    const runtime = makeAppRuntime();
    const opened: string[] = [];
    const start = () =>
      runLinkedRemoteItemQuickAction(runtime, identity, request, linked, "pull_request", action, undefined, (ref) =>
        opened.push(ref.id),
      );
    return { posts, opened, start, runtime };
  }

  afterEach(async () => {
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  });

  it("launches in the linked workspace without asking its machine for one", async () => {
    const { posts, opened, start, runtime } = run((path) =>
      path.endsWith("/agent-handoffs") ? delivered() : undefined,
    );
    try {
      start();
      await vi.waitFor(() => expect(opened).toEqual(["ws-old"]));
      expect(posts).toEqual(["/api/v1/devboxes/compute-a/workspaces/ws-old/runtime/agent-handoffs"]);
      expect(getFlashes()).toEqual([]);
    } finally {
      await Effect.runPromise(runtime.disposeEffect);
    }
  });

  it("runs a second action in the first one's replacement after the machine reports the workspace gone", async () => {
    const { posts, opened, start, runtime } = run((path) => {
      if (path === "/api/v1/devboxes/compute-a/workspaces/ws-old/runtime/agent-handoffs") {
        return problem(404, "workspaceNotFound", "workspace not found");
      }
      if (path === "/api/v1/devboxes/compute-a/workspaces") return Response.json({ id: "ws-new", status: "ready" });
      return path.endsWith("/agent-handoffs") ? delivered() : undefined;
    });
    try {
      start();
      start();
      await vi.waitFor(() => expect(opened).toEqual(["ws-new", "ws-new"]));
      expect(posts).toEqual([
        "/api/v1/devboxes/compute-a/workspaces/ws-old/runtime/agent-handoffs",
        "/api/v1/devboxes/compute-a/workspaces",
        "/api/v1/devboxes/compute-a/workspaces/ws-new/runtime/agent-handoffs",
        "/api/v1/devboxes/compute-a/workspaces/ws-new/runtime/agent-handoffs",
      ]);
      expect(getFlashes()).toEqual([]);
    } finally {
      await Effect.runPromise(runtime.disposeEffect);
    }
  });

  it("opens a replacement only once the action starts there", async () => {
    const { posts, opened, start, runtime } = run((path) => {
      if (path === "/api/v1/devboxes/compute-a/workspaces/ws-old/runtime/agent-handoffs") {
        return problem(404, "workspaceNotFound", "workspace not found");
      }
      if (path === "/api/v1/devboxes/compute-a/workspaces") return Response.json({ id: "ws-new", status: "ready" });
      return path.endsWith("/agent-handoffs") ? problem(403, "forbidden", "agent unavailable") : undefined;
    });
    try {
      start();
      await vi.waitFor(() =>
        expect(getFlashes()).toEqual([
          expect.objectContaining({ tone: "danger", message: '"Review" could not start: agent unavailable' }),
        ]),
      );
      expect(posts).toEqual([
        "/api/v1/devboxes/compute-a/workspaces/ws-old/runtime/agent-handoffs",
        "/api/v1/devboxes/compute-a/workspaces",
        "/api/v1/devboxes/compute-a/workspaces/ws-new/runtime/agent-handoffs",
      ]);
      expect(opened).toEqual([]);
    } finally {
      await Effect.runPromise(runtime.disposeEffect);
    }
  });

  it("reports any other refusal from the machine without asking it for a workspace", async () => {
    const { posts, opened, start, runtime } = run((path) =>
      path.endsWith("/agent-handoffs") ? problem(403, "forbidden", "not allowed") : undefined,
    );
    try {
      start();
      await vi.waitFor(() =>
        expect(getFlashes()).toEqual([
          expect.objectContaining({ tone: "danger", message: '"Review" could not start: not allowed' }),
        ]),
      );
      expect(posts).toEqual(["/api/v1/devboxes/compute-a/workspaces/ws-old/runtime/agent-handoffs"]);
      expect(opened).toEqual([]);
    } finally {
      await Effect.runPromise(runtime.disposeEffect);
    }
  });
});

it("runs a row action on an item whose remote workspace is being set up again, after that setup", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const pull = pulls[0]!;
  const identity = {
    provider: pull.repo.provider,
    platformHost: pull.repo.platform_host,
    owner: pull.repo.owner,
    name: pull.repo.name,
    repoPath: pull.repo.repo_path,
    number: pull.Number,
    itemType: "pull",
  } as const;
  const linked = { id: "ws-remote", status: "ready", hostKey: "devbox:compute-a", hostLabel: "compute-a (devbox)" };
  recordWorkspaceCreated(identity, linked);
  const create = Promise.withResolvers<Response>();
  const posts: string[] = [];
  let handoffs = 0;
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method !== "POST") return undefined;
      posts.push(url.pathname);
      if (url.pathname === "/api/v1/devboxes/compute-a/workspaces") return create.promise;
      if (url.pathname === "/api/v1/devboxes/compute-a/workspaces/ws-remote/retry") {
        return Response.json({ id: "ws-remote", status: "creating" });
      }
      if (url.pathname.endsWith("/runtime/agent-handoffs")) {
        if (handoffs++ === 0) {
          return Response.json(
            { code: "conflict", title: "Conflict", detail: "workspace setup failed: clone failed", status: 409 },
            { status: 409 },
          );
        }
        return Response.json({
          session: { key: `runtime-${handoffs}`, target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
        });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  const action = { label: "Review", agent: "helper", prompt: "Review this change" };
  try {
    runLinkedRemoteItemQuickAction(
      runtime,
      identity,
      {
        kind: "pull",
        repo: {
          provider: pull.repo.provider,
          platformHost: pull.repo.platform_host,
          owner: pull.repo.owner,
          name: pull.repo.name,
        },
        number: pull.Number,
      },
      linked,
      "pull_request",
      action,
    );
    await vi.waitFor(() => expect(posts).toContain("/api/v1/devboxes/compute-a/workspaces"));
    runQuickActionOnItems(runtime, [{ kind: "pull", item: { ...pull, workspace: undefined } }], action, "");
    create.resolve(Response.json({ id: "ws-remote", status: "error", reused: true }));
    await vi.waitFor(() => expect(getFlashes()).toEqual([expect.objectContaining({ tone: "success" })]));
    expect(posts).toEqual([
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime/agent-handoffs",
      "/api/v1/devboxes/compute-a/workspaces",
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/retry",
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime/agent-handoffs",
      "/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime/agent-handoffs",
    ]);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});
