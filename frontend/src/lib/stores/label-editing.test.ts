import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { OwnedAppRuntime } from "../app/runtime.js";
import {
  createDetailStore as createRuntimeDetailStore,
  type DetailStore,
  type DetailStoreOptions,
} from "./detail.svelte.js";
import {
  createIssuesStore as createRuntimeIssuesStore,
  type IssuesStore,
  type IssuesStoreOptions,
} from "./issues.svelte.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import type { Label } from "../api/types.js";

let runtime: OwnedAppRuntime | undefined;

type TestIssuesStoreOptions = Omit<IssuesStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createIssuesStore(options: TestIssuesStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimeIssuesStore({ ...storeOptions, runtime });
}

async function loadIssueDetail(store: IssuesStore, ...args: Parameters<IssuesStore["loadIssueDetail"]>): Promise<void> {
  store.loadIssueDetail(...args);
  await vi.waitFor(() => expect(store.isIssueDetailLoading()).toBe(false));
}

type TestDetailStoreOptions = Omit<DetailStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createDetailStore(options: TestDetailStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimeDetailStore({ ...storeOptions, runtime });
}

async function loadDetail(store: DetailStore, ...args: Parameters<DetailStore["loadDetail"]>): Promise<void> {
  store.loadDetail(...args);
  await vi.waitFor(() => expect(store.isDetailLoading()).toBe(false));
}

beforeEach(() => {
  runtime = undefined;
});

afterEach(async () => {
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

const routeRef = {
  provider: "github",
  platformHost: "github.com",
  repoPath: "octo/repo",
};
const otherRouteRef = {
  provider: "gitlab",
  platformHost: "gitlab.example",
  repoPath: "octo/repo",
};

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function label(name: string): Label {
  return {
    name,
    color: name === "bug" ? "d73a4a" : "fbca04",
    is_default: false,
  };
}

describe("label editing stores", () => {
  it("optimistically projects pull labels and rolls back a failed acknowledged command", async () => {
    const put = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          merge_request: { Number: 1, labels: [label("bug")] },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(() => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });
    const settled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });

    store.setPullLabels("octo", "repo", 1, [label("triage")], { onSettled: settled.resolve });
    await vi.waitFor(() =>
      expect(store.getDetail()?.merge_request.labels?.map((item) => item.name)).toEqual(["triage"]),
    );
    put.resolve({
      error: {
        code: "validationError",
        detail: "labels rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await settled.promise;

    expect(store.getDetail()?.merge_request.labels?.map((item) => item.name)).toEqual(["bug"]);
  });

  it("optimistically projects issue labels and rolls back a failed acknowledged command", async () => {
    const put = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          issue: { Number: 2, labels: [label("bug")], UpdatedAt: "2026-05-15T12:00:00Z" },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(() => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createIssuesStore({ client });
    const settled = Promise.withResolvers<void>();
    await loadIssueDetail(store, "octo", "repo", 2, routeRef);

    store.setIssueLabels("octo", "repo", 2, [label("triage")], { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getIssueDetail()?.issue.labels?.map((item) => item.name)).toEqual(["triage"]));
    put.resolve({
      error: {
        code: "validationError",
        detail: "labels rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await settled.promise;

    expect(store.getIssueDetail()?.issue.labels?.map((item) => item.name)).toEqual(["bug"]);
  });

  it("updates visible pull labels from the label mutation response", async () => {
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          merge_request: { Number: 1, labels: [label("bug")] },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(async () => ({ data: { labels: [label("triage")] } })),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });

    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });
    const settled = Promise.withResolvers<void>();
    store.setPullLabels("octo", "repo", 1, [label("triage")], { onSettled: settled.resolve });
    await settled.promise;

    expect(client.PUT).toHaveBeenCalledWith(
      "/pulls/{provider}/{owner}/{name}/{number}/labels",
      expect.objectContaining({ body: { labels: ["triage"] } }),
    );
    expect(store.getDetail()?.merge_request.labels?.map((item) => item.name)).toEqual(["triage"]);
  });

  it("does not apply stale pull label responses after provider navigation", async () => {
    const put = deferred<{ data: { labels: Label[] } }>();
    const client = {
      GET: vi.fn(async (_path: string, options: { params?: { path?: { provider?: string } } }) => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: options.params?.path?.provider ?? "github",
            platform_host: options.params?.path?.provider === "gitlab" ? "gitlab.example" : "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          merge_request: {
            Number: 1,
            labels: [label(options.params?.path?.provider === "gitlab" ? "gitlab-label" : "bug")],
          },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(async () => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });

    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });
    const settled = Promise.withResolvers<void>();
    store.setPullLabels("octo", "repo", 1, [label("triage")], { onSettled: settled.resolve });
    await loadDetail(store, "octo", "repo", 1, otherRouteRef);
    put.resolve({ data: { labels: [label("triage")] } });
    await settled.promise;

    expect(store.getDetail()?.repo.provider).toBe("gitlab");
    expect(store.getDetail()?.merge_request.labels?.map((item) => item.name)).toEqual(["gitlab-label"]);
  });

  it("rebases a failed pull label change onto a newer detail read", async () => {
    const put = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    let getCalls = 0;
    const client = {
      GET: vi.fn(async () => {
        getCalls++;
        return {
          data: {
            repo_owner: "octo",
            repo_name: "repo",
            repo: {
              provider: "github",
              platform_host: "github.com",
              owner: "octo",
              name: "repo",
              repo_path: "octo/repo",
            },
            merge_request: { Number: 1, labels: [label(getCalls === 1 ? "bug" : "upstream")] },
            events: [],
          },
        };
      }),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(() => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });
    const settled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });

    store.setPullLabels("octo", "repo", 1, [label("triage")], { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("triage"));
    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });
    expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("triage");

    put.resolve({
      error: {
        code: "validationError",
        detail: "labels rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await settled.promise;

    expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("upstream");
  });

  it("rebases a pending pull label projection through the public sync launcher", async () => {
    const put = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    const synced = {
      repo_owner: "octo",
      repo_name: "repo",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "octo",
        name: "repo",
        repo_path: "octo/repo",
      },
      merge_request: { Number: 1, labels: [label("upstream")] },
      events: [],
    };
    const client = {
      GET: vi.fn(async () => ({ data: { ...synced, merge_request: { Number: 1, labels: [label("bug")] } } })),
      POST: vi.fn(async () => ({ data: synced })),
      PUT: vi.fn(() => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });
    const settled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });

    store.setPullLabels("octo", "repo", 1, [label("triage")], { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("triage"));
    const syncSettled = Promise.withResolvers<boolean>();
    const result = store.syncDetailNow("octo", "repo", 1, routeRef, {
      onSuccess: syncSettled.resolve,
      onFailure: () => syncSettled.resolve(false),
    });
    expect(result).toBeUndefined();
    await expect(syncSettled.promise).resolves.toBe(true);
    expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("triage");

    put.resolve({
      error: {
        code: "validationError",
        detail: "labels rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await settled.promise;

    expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("upstream");
  });

  it("fences pull label commands accepted behind a stale conflict", async () => {
    const first = Promise.withResolvers<{
      error: {
        code: string;
        detail: string;
        details: { reason: string };
        title: string;
        type: string;
      };
      response: Response;
    }>();
    const synced = {
      repo_owner: "octo",
      repo_name: "repo",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "octo",
        name: "repo",
        repo_path: "octo/repo",
      },
      merge_request: { Number: 1, labels: [label("upstream")] },
      events: [],
    };
    const put = vi.fn(() => first.promise);
    const client = {
      GET: vi.fn(async () => ({ data: { ...synced, merge_request: { Number: 1, labels: [label("bug")] } } })),
      POST: vi.fn(async () => ({ data: synced })),
      PUT: put,
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });
    const firstSettled = Promise.withResolvers<void>();
    const secondSettled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...routeRef, sync: false });

    store.setPullLabels("octo", "repo", 1, [label("triage")], { onSettled: firstSettled.resolve });
    store.setPullLabels("octo", "repo", 1, [label("second")], { onSettled: secondSettled.resolve });
    first.resolve({
      error: {
        code: "conflict",
        detail: "pull request changed upstream",
        details: { reason: "stale_state" },
        title: "Conflict",
        type: "about:blank",
      },
      response: new Response(null, { status: 409 }),
    });
    await Promise.all([firstSettled.promise, secondSettled.promise]);

    expect(put).toHaveBeenCalledOnce();
    expect(client.POST).toHaveBeenCalledWith(
      "/pulls/{provider}/{owner}/{name}/{number}/sync",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(store.getDetail()?.merge_request.labels?.[0]?.name).toBe("upstream");
  });

  it("updates visible issue labels from the label mutation response", async () => {
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          issue: {
            Number: 2,
            labels: [label("bug")],
            UpdatedAt: "2026-05-15T12:00:00Z",
          },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(async () => ({ data: { labels: [label("triage")] } })),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createIssuesStore({ client });

    await loadIssueDetail(store, "octo", "repo", 2, routeRef);
    const settled = Promise.withResolvers<void>();
    store.setIssueLabels("octo", "repo", 2, [label("triage")], { onSettled: settled.resolve });
    await settled.promise;

    expect(client.PUT).toHaveBeenCalledWith(
      "/issues/{provider}/{owner}/{name}/{number}/labels",
      expect.objectContaining({ body: { labels: ["triage"] } }),
    );
    expect(store.getIssueDetail()?.issue.labels?.map((item) => item.name)).toEqual(["triage"]);
  });

  it("does not apply stale issue label responses after provider navigation", async () => {
    const put = deferred<{ data: { labels: Label[] } }>();
    const client = {
      GET: vi.fn(async (_path: string, options: { params?: { path?: { provider?: string } } }) => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: options.params?.path?.provider ?? "github",
            platform_host: options.params?.path?.provider === "gitlab" ? "gitlab.example" : "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          issue: {
            Number: 2,
            labels: [label(options.params?.path?.provider === "gitlab" ? "gitlab-label" : "bug")],
            UpdatedAt: "2026-05-15T12:00:00Z",
          },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(async () => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createIssuesStore({ client });

    await loadIssueDetail(store, "octo", "repo", 2, routeRef);
    const settled = Promise.withResolvers<void>();
    store.setIssueLabels("octo", "repo", 2, [label("triage")], { onSettled: settled.resolve });
    await loadIssueDetail(store, "octo", "repo", 2, otherRouteRef);
    put.resolve({ data: { labels: [label("triage")] } });
    await settled.promise;

    expect(store.getIssueDetail()?.repo.provider).toBe("gitlab");
    expect(store.getIssueDetail()?.issue.labels?.map((item) => item.name)).toEqual(["gitlab-label"]);
  });
});

describe("user assignment editing stores", () => {
  it("serializes pull assignee changes without rolling back a newer optimistic choice", async () => {
    const first = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    const second = Promise.withResolvers<{ data: { assignees: string[] } }>();
    const put = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          merge_request: { Number: 1, assignees: ["alice"], requested_reviewers: [] },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: put,
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });
    const firstSettled = Promise.withResolvers<void>();
    const secondSettled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, routeRef);

    store.setPullAssignees("octo", "repo", 1, ["bob"], { onSettled: firstSettled.resolve });
    store.setPullAssignees("octo", "repo", 1, ["carol"], { onSettled: secondSettled.resolve });

    await vi.waitFor(() => expect(store.getDetail()?.merge_request.assignees).toEqual(["carol"]));
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    first.resolve({
      error: {
        code: "validationError",
        detail: "assignees rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await firstSettled.promise;
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(store.getDetail()?.merge_request.assignees).toEqual(["carol"]);

    second.resolve({ data: { assignees: ["carol"] } });
    await secondSettled.promise;
    expect(store.getDetail()?.merge_request.assignees).toEqual(["carol"]);
  });

  it("rolls back pull reviewers when the acknowledged command fails", async () => {
    const put = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          merge_request: { Number: 1, assignees: [], requested_reviewers: ["alice"] },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(() => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createDetailStore({ client });
    const settled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, routeRef);

    store.setPullReviewers("octo", "repo", 1, ["bob"], { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.merge_request.requested_reviewers).toEqual(["bob"]));
    put.resolve({
      error: {
        code: "validationError",
        detail: "reviewers rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await settled.promise;

    expect(store.getDetail()?.merge_request.requested_reviewers).toEqual(["alice"]);
  });

  it("optimistically projects and rolls back issue assignees", async () => {
    const put = Promise.withResolvers<{
      error: { code: string; detail: string; title: string; type: string };
      response: Response;
    }>();
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "octo",
          repo_name: "repo",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "octo",
            name: "repo",
            repo_path: "octo/repo",
          },
          issue: {
            Number: 2,
            assignees: ["alice"],
            labels: [],
            UpdatedAt: "2026-05-15T12:00:00Z",
          },
          events: [],
        },
      })),
      POST: vi.fn(async () => ({ data: undefined })),
      PUT: vi.fn(() => put.promise),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;
    const store = createIssuesStore({ client });
    const settled = Promise.withResolvers<void>();
    await loadIssueDetail(store, "octo", "repo", 2, routeRef);

    store.setIssueAssignees("octo", "repo", 2, ["bob"], { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getIssueDetail()?.issue.assignees).toEqual(["bob"]));
    put.resolve({
      error: {
        code: "validationError",
        detail: "assignees rejected",
        title: "Invalid request",
        type: "about:blank",
      },
      response: new Response(null, { status: 400 }),
    });
    await settled.promise;

    expect(store.getIssueDetail()?.issue.assignees).toEqual(["alice"]);
  });
});
