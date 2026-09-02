import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import type { GeneratedClient } from "../api/generated-api.js";
import type { OwnedAppRuntime } from "../app/runtime.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import {
  createPullsStore as createRuntimePullsStore,
  type PullsStore,
  type PullsStoreOptions,
} from "./pulls.svelte.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";
import { involvesMeFilterStorageKey } from "./involves-me-filter.js";
import { unassignedFilterStorageKey } from "./unassigned-filter.js";

let runtime: OwnedAppRuntime | undefined;

type TestPullsStoreOptions = Omit<PullsStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createPullsStore(options: TestPullsStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimePullsStore({ ...storeOptions, runtime });
}

async function loadPulls(store: PullsStore): Promise<void> {
  store.loadPulls();
  await vi.waitFor(() => expect(store.isLoading()).toBe(false));
}

beforeEach(() => {
  runtime = undefined;
  localStorage.clear();
});

afterEach(async () => {
  for (const item of getFlashes()) dismissFlash(item.id);
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

function pull(id: number, repoName: string, lastActivityAt: string, overrides: Partial<PullRequest> = {}): PullRequest {
  const provider = overrides.repo?.provider ?? "github";
  const platformHost = overrides.repo?.platform_host ?? "github.com";
  const owner = overrides.repo?.owner ?? overrides.repo_owner ?? "acme";
  const name = overrides.repo?.name ?? overrides.repo_name ?? repoName;
  const repoPath = overrides.repo?.repo_path ?? `${owner}/${name}`;
  return {
    ID: id,
    Number: id,
    Title: `PR ${id}`,
    LastActivityAt: lastActivityAt,
    repo_owner: owner,
    repo_name: name,
    platform_host: platformHost,
    repo: {
      provider,
      platform_host: platformHost,
      owner,
      name,
      repo_path: repoPath,
    },
    State: "open",
    IsDraft: false,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    MergeableState: "clean",
    KanbanStatus: "new",
    ...overrides,
  } as PullRequest;
}

function clientWithPulls(data: PullRequest[]): GeneratedClient {
  return {
    GET: vi.fn(async () => ({ data, error: undefined })),
  } as unknown as GeneratedClient;
}

describe("pulls store display order", () => {
  it("reports when a bounded list filled the requested chunk", async () => {
    const get = vi.fn(async () => ({
      data: Array.from({ length: 30 }, (_, index) => pull(index + 1, "api", "2026-05-20T15:00:00Z")),
      error: undefined,
    }));
    const store = createPullsStore({
      client: { GET: get } as unknown as GeneratedClient,
    });

    store.loadPulls({ limit: 30 });
    await vi.waitFor(() => expect(store.isLoading()).toBe(false));

    expect(store.isListCapped()).toBe(true);

    get.mockClear();
    const refresh = runtime!.runCommand(store.reconcilePullsEffect(), {
      operation: "reconcile bounded pulls",
      safeContext: {},
      onFailure: () => {},
    });
    await refresh.exit;

    expect(get).toHaveBeenCalledWith(
      "/pulls",
      expect.objectContaining({ params: { query: expect.objectContaining({ limit: 30 }) } }),
    );
  });

  it("preserves the bounded list request after starring a pull request", async () => {
    const listed = pull(7, "api", "2026-05-20T15:00:00Z");
    const get = vi.fn(async () => ({ data: [listed], error: undefined }));
    const store = createPullsStore({
      client: {
        GET: get,
        PUT: vi.fn(async () => ({ data: undefined, error: undefined })),
      } as unknown as GeneratedClient,
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "api",
      repoPath: "acme/api",
    };
    store.loadPulls({ limit: 30 });
    await vi.waitFor(() => expect(store.isLoading()).toBe(false));
    get.mockClear();

    store.togglePRStar(ref, 7, false);

    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    expect(get).toHaveBeenCalledWith(
      "/pulls",
      expect.objectContaining({ params: { query: expect.objectContaining({ limit: 30 }) } }),
    );
  });

  it("persists and sends the Involves me filter", async () => {
    const get = vi.fn(async () => ({ data: [], error: undefined }));
    const store = createPullsStore({ client: { GET: get } as unknown as GeneratedClient });

    store.setInvolvesMe(true);
    await loadPulls(store);

    expect(localStorage.getItem(involvesMeFilterStorageKey("pulls"))).toBe("1");
    expect(get).toHaveBeenCalledWith(
      "/pulls",
      expect.objectContaining({
        params: { query: expect.objectContaining({ involves_me: true }) },
      }),
    );
  });

  it("persists and sends the Unassigned filter", async () => {
    const get = vi.fn(async () => ({ data: [], error: undefined }));
    const store = createPullsStore({ client: { GET: get } as unknown as GeneratedClient });

    store.setUnassigned(true);
    await loadPulls(store);

    expect(localStorage.getItem(unassignedFilterStorageKey("pulls"))).toBe("1");
    expect(get).toHaveBeenCalledWith(
      "/pulls",
      expect.objectContaining({
        params: { query: expect.objectContaining({ unassigned: true }) },
      }),
    );
  });

  it("aborts a superseded list request", async () => {
    let firstSignal: AbortSignal | undefined;
    const get = vi
      .fn()
      .mockImplementationOnce((_path: string, options?: { signal?: AbortSignal }) => {
        firstSignal = options?.signal;
        return new Promise((_resolve, reject) => {
          firstSignal?.addEventListener("abort", () => {
            reject(new DOMException("aborted", "AbortError"));
          });
        });
      })
      .mockResolvedValueOnce({ data: [], error: undefined });
    const store = createPullsStore({
      client: { GET: get } as unknown as GeneratedClient,
    });

    store.loadPulls();
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    store.loadPulls();
    await vi.waitFor(() => expect(store.isLoading()).toBe(false));

    expect(firstSignal?.aborted).toBe(true);
  });

  it("flashes star failures without replacing list load errors", async () => {
    const store = createPullsStore({
      client: {
        DELETE: vi.fn(async () => ({ error: { detail: "permission denied" } })),
      } as unknown as GeneratedClient,
    });

    const result = store.togglePRStar(
      { provider: "github", platformHost: "github.com", owner: "acme", name: "api", repoPath: "acme/api" },
      7,
      true,
    );

    expect(result).toBeUndefined();
    await vi.waitFor(() => expect(getFlash()?.message).toBe("permission denied"));
    expect(getFlash()).toMatchObject({ message: "permission denied", tone: "danger" });
    expect(store.getError()).toBeNull();
  });

  it("sends unstar identity without a DELETE request body", async () => {
    const listed = pull(7, "api", "2026-05-20T15:00:00Z", { Starred: true });
    const remove = vi.fn().mockResolvedValue({ data: undefined, error: undefined });
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: [listed], error: undefined })
      .mockResolvedValue({ data: [{ ...listed, Starred: false }], error: undefined });
    const store = createPullsStore({
      client: { DELETE: remove, GET: get } as unknown as GeneratedClient,
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "api",
      repoPath: "acme/api",
    };

    await loadPulls(store);
    store.togglePRStar(ref, 7, true);

    await vi.waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect(remove).toHaveBeenCalledWith("/starred", {
      params: {
        query: {
          item_type: "pr",
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          number: 7,
        },
      },
      signal: expect.any(AbortSignal),
    });
    expect(remove.mock.calls[0]?.[1]).not.toHaveProperty("body");
  });

  it("orders opposite star requests for the same provider item", async () => {
    const first = Promise.withResolvers<{ data: undefined; error: undefined }>();
    const remove = vi.fn(() => first.promise);
    const add = vi.fn().mockResolvedValue({ data: undefined, error: undefined });
    const listed = pull(7, "api", "2026-05-20T15:00:00Z", { Starred: true });
    const projectDetailStar = vi.fn();
    const store = createPullsStore({
      client: {
        DELETE: remove,
        PUT: add,
        GET: vi.fn().mockResolvedValue({ data: [listed], error: undefined }),
      } as unknown as GeneratedClient,
      optimisticDetailStarUpdate: projectDetailStar,
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "api",
      repoPath: "acme/api",
    };

    await loadPulls(store);
    const unstar = store.togglePRStar(ref, 7, Boolean(store.getPulls()[0]?.Starred));
    await vi.waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect(store.getPulls()[0]?.Starred).toBe(false);
    expect(projectDetailStar).toHaveBeenCalledWith(ref, 7, false, expect.any(Number));
    expect(projectDetailStar.mock.calls[0]?.[3]).toBeGreaterThan(0);
    const star = store.togglePRStar(ref, 7, Boolean(store.getPulls()[0]?.Starred));

    expect(unstar).toBeUndefined();
    expect(star).toBeUndefined();
    expect(store.getPulls()[0]?.Starred).toBe(true);
    expect(add).not.toHaveBeenCalled();
    first.resolve({ data: undefined, error: undefined });
    await vi.waitFor(() => expect(add).toHaveBeenCalledTimes(1));
  });

  it("preserves the API order for flat display", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z"),
        pull(2, "web", "2026-05-20T14:00:00Z"),
        pull(3, "api", "2026-05-20T13:00:00Z"),
      ]),
      getGroupByRepo: () => false,
    });

    await loadPulls(store);

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 2, 3]);
  });

  it("groups by repo in first-seen order rather than global activity order", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z"),
        pull(2, "web", "2026-05-20T14:00:00Z"),
        pull(3, "api", "2026-05-20T13:00:00Z"),
      ]),
      getGroupByRepo: () => true,
    });

    await loadPulls(store);

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 3, 2]);
  });

  it("filters pull requests by review state, readiness, CI, merge conflicts, and multiple kanban statuses", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z", {
          ReviewDecision: "APPROVED",
          KanbanStatus: "reviewing",
        }),
        pull(2, "web", "2026-05-20T14:00:00Z", {
          IsDraft: true,
          KanbanStatus: "waiting",
        }),
        pull(3, "worker", "2026-05-20T13:00:00Z", {
          CIStatus: "failure",
          KanbanStatus: "awaiting_merge",
        }),
        pull(4, "api", "2026-05-20T12:00:00Z", {
          MergeableState: "dirty",
          KanbanStatus: "new",
        }),
      ]),
    });

    await loadPulls(store);

    store.toggleAttributeFilter("ready");
    store.toggleKanbanStatusFilter("reviewing");
    store.toggleKanbanStatusFilter("awaiting_merge");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 3]);

    store.toggleAttributeFilter("failed_ci");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([3]);

    store.toggleAttributeFilter("failed_ci");
    store.toggleAttributeFilter("merge_conflicts");
    store.toggleKanbanStatusFilter("reviewing");
    store.toggleKanbanStatusFilter("awaiting_merge");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([4]);
  });

  it("filters pull requests to entries with a workspace", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z", { workspace: { id: "ws-1", status: "ready" } }),
        pull(2, "web", "2026-05-20T14:00:00Z"),
      ]),
    });

    await loadPulls(store);
    store.toggleAttributeFilter("has_workspace");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1]);
  });

  it("matches empty, missing, and unknown kanban statuses as New", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z", { KanbanStatus: "" as PullRequest["KanbanStatus"] }),
        pull(2, "web", "2026-05-20T14:00:00Z", {
          KanbanStatus: undefined as unknown as PullRequest["KanbanStatus"],
        }),
        pull(3, "worker", "2026-05-20T13:00:00Z", { KanbanStatus: "later" as PullRequest["KanbanStatus"] }),
        pull(4, "api", "2026-05-20T12:00:00Z", { KanbanStatus: "reviewing" }),
      ]),
    });

    await loadPulls(store);

    store.toggleKanbanStatusFilter("new");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 2, 3]);
  });
});
