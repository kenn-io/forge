import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { OwnedAppRuntime } from "../app/runtime.js";
import type { Issue, IssueDetail } from "../api/types.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { client as appClient } from "../api/runtime.js";
import { mockSettings } from "../../test/mockApiFetch.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";
import { createIssuesStore as createRuntimeIssuesStore, type IssuesStoreOptions } from "./issues.svelte.js";
import { involvesMeFilterStorageKey } from "./involves-me-filter.js";

let runtime: OwnedAppRuntime | undefined;

type TestIssuesStoreOptions = Omit<IssuesStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createIssuesStore(options: TestIssuesStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimeIssuesStore({ ...storeOptions, runtime });
}

async function loadIssueDetail(
  store: ReturnType<typeof createIssuesStore>,
  ...args: Parameters<ReturnType<typeof createIssuesStore>["loadIssueDetail"]>
): Promise<void> {
  const result = store.loadIssueDetail(...args);
  expect(result).toBeUndefined();
  await vi.waitFor(() => expect(store.isIssueDetailLoading()).toBe(false));
}

beforeEach(() => {
  runtime = undefined;
  localStorage.clear();
});

afterEach(async () => {
  for (const item of getFlashes()) dismissFlash(item.id);
  vi.unstubAllGlobals();
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

function settingsResponse(hideBots: boolean): Response {
  return Response.json({ ...mockSettings, issues: { hide_bots: hideBots } });
}

function stubSettingsWrites(write: (body: unknown, index: number) => Response | Promise<Response>): {
  readonly bodies: unknown[];
} {
  const bodies: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      if (request.method === "GET") return settingsResponse(false);
      const body: unknown = await request.clone().json();
      bodies.push(body);
      return await write(body, bodies.length - 1);
    }),
  );
  return { bodies };
}

function issue(id: number, author: string, provider = "github"): Issue {
  return {
    ID: id,
    Number: id,
    Title: `Issue ${id}`,
    Author: author,
    State: "open",
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    repo: {
      provider,
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  } as Issue;
}

describe("issues store bot visibility", () => {
  it("hydrates the persisted preference and hides bot-authored issues", async () => {
    const client = {
      GET: vi.fn(async () => ({
        data: [
          issue(1, "alice"),
          issue(2, "renovate[bot]", "github"),
          issue(3, "project_123_bot_4ffca233d8298ea1", "gitlab"),
          issue(4, "group_456_bot_8ea14ffca233d829", "gitlab"),
          issue(5, "release-bot", "forgejo"),
          issue(6, "renovate-bot", "gitea"),
          issue(7, "Talbot", "github"),
          issue(8, "Abbot", "gitea"),
          issue(9, "project_alpha_bot_build", "gitlab"),
        ],
        error: undefined,
      })),
    } as unknown as GeneratedClient;
    const store = createIssuesStore({ client });

    store.loadIssues();
    await vi.waitFor(() => expect(store.isIssuesLoading()).toBe(false));
    store.hydrateDefaults({ hide_bots: true });

    expect(store.getHideBots()).toBe(true);
    expect(store.getIssues().map((item) => item.Author)).toEqual([
      "alice",
      "Talbot",
      "Abbot",
      "project_alpha_bot_build",
    ]);
  });

  it("launches visibility persistence synchronously and adopts the saved response", async () => {
    const requests = stubSettingsWrites(() => settingsResponse(true));
    const store = createIssuesStore({ client: appClient });

    const result = store.setHideBots(true);

    expect(result).toBeUndefined();
    await vi.waitFor(() => expect(requests.bodies).toHaveLength(1));
    expect(requests.bodies[0]).toEqual({ issues: { hide_bots: true } });
    expect(store.getHideBots()).toBe(true);
  });

  it("prevents out-of-order responses by serializing rapid visibility changes", async () => {
    const firstResponse = Promise.withResolvers<Response>();
    const requests = stubSettingsWrites((_body, index) =>
      index === 0 ? firstResponse.promise : settingsResponse(false),
    );
    const store = createIssuesStore({ client: appClient });

    store.setHideBots(true);
    store.setHideBots(false);

    expect(store.getHideBots()).toBe(false);
    await vi.waitFor(() => expect(requests.bodies).toHaveLength(1));

    firstResponse.resolve(settingsResponse(true));
    await vi.waitFor(() => expect(requests.bodies).toHaveLength(2));
    expect(store.getHideBots()).toBe(false);

    expect(requests.bodies).toEqual([{ issues: { hide_bots: true } }, { issues: { hide_bots: false } }]);
    expect(store.getHideBots()).toBe(false);
  });

  it("restores the previous preference when persistence fails", async () => {
    stubSettingsWrites(() =>
      Response.json(
        { type: "about:blank", title: "Settings unavailable", status: 500, detail: "settings unavailable" },
        { status: 500 },
      ),
    );
    const store = createIssuesStore({ client: appClient });

    store.setHideBots(true);

    await vi.waitFor(() => expect(store.getHideBots()).toBe(false));
    expect(getFlash()).toMatchObject({ message: "settings unavailable", tone: "danger" });
  });

  it("restores the previous preference when the settings request throws", async () => {
    stubSettingsWrites(() => {
      throw new Error("network unavailable");
    });
    const store = createIssuesStore({ client: appClient });

    store.setHideBots(true);

    await vi.waitFor(() => expect(store.getHideBots()).toBe(false));
    expect(getFlash()).toMatchObject({ message: "Could not reach Kenn Forge", tone: "danger" });
  });
});

function issueDetail(): IssueDetail {
  return {
    issue: {
      Number: 7,
      State: "open",
      Body: "- [ ] done",
    },
    repo: {
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
    },
    events: [],
    detail_loaded: true,
    repo_owner: "acme",
    repo_name: "widget",
  } as unknown as IssueDetail;
}

function mockClient(overrides: Partial<GeneratedClient> = {}): GeneratedClient {
  return {
    GET: vi.fn(),
    POST: vi.fn(),
    PUT: vi.fn(),
    PATCH: vi.fn(),
    DELETE: vi.fn(),
    OPTIONS: vi.fn(),
    HEAD: vi.fn(),
    TRACE: vi.fn(),
    ...overrides,
  } as unknown as GeneratedClient;
}

describe("createIssuesStore", () => {
  it("persists and sends the Involves me filter", async () => {
    const get = vi.fn(async () => ({ data: [], error: undefined }));
    const store = createIssuesStore({ client: { GET: get } as unknown as GeneratedClient });

    store.setInvolvesMe(true);
    store.loadIssues();
    await vi.waitFor(() => expect(store.isIssuesLoading()).toBe(false));

    expect(localStorage.getItem(involvesMeFilterStorageKey("issues"))).toBe("1");
    expect(get).toHaveBeenCalledWith(
      "/issues",
      expect.objectContaining({
        params: { query: expect.objectContaining({ involves_me: true }) },
      }),
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("reconciles visible activity and issue lists after a background detail sync converges", async () => {
    vi.useFakeTimers();
    const cached = issueDetail();
    cached.detail_fetched_at = "2026-08-12T21:00:00Z";
    const fresh = issueDetail();
    fresh.detail_fetched_at = "2026-08-12T21:03:00Z";
    fresh.issue.Title = "Fresh issue detail";
    const detailResponses = [cached, fresh];
    const get = vi.fn(async (path: string) =>
      path === "/issues"
        ? { data: [], error: undefined }
        : { data: detailResponses.shift() ?? fresh, error: undefined },
    );
    const onDetailSynchronized = vi.fn();
    const store = createIssuesStore({
      client: mockClient({
        GET: get,
        POST: vi.fn().mockResolvedValue({ data: undefined, error: undefined }),
      }),
      getPage: () => "issues",
      onDetailSynchronized,
    });

    await loadIssueDetail(store, "acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: "background",
    });
    await vi.advanceTimersByTimeAsync(300);

    await vi.waitFor(() => expect(store.getIssueDetail()?.issue.Title).toBe("Fresh issue detail"));
    expect(get.mock.calls.filter(([path]) => path === "/issues")).toHaveLength(1);
    expect(onDetailSynchronized).toHaveBeenCalledOnce();
  });

  it("reconciles visible lists when selection closes during a synchronous detail sync", async () => {
    const syncPost = Promise.withResolvers<{ data: IssueDetail; error: undefined }>();
    const post = vi.fn(() => syncPost.promise);
    const get = vi.fn(async (path: string) =>
      path === "/issues" ? { data: [], error: undefined } : { data: issueDetail(), error: undefined },
    );
    const onDetailSynchronized = vi.fn();
    const store = createIssuesStore({
      client: mockClient({ GET: get, POST: post }),
      getPage: () => "issues",
      onDetailSynchronized,
    });

    await loadIssueDetail(store, "acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: true,
    });
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    store.clearIssueDetail();

    syncPost.resolve({ data: issueDetail(), error: undefined });

    await vi.waitFor(() => expect(onDetailSynchronized).toHaveBeenCalledOnce());
    expect(get.mock.calls.filter(([path]) => path === "/issues")).toHaveLength(1);
    expect(store.getIssueDetail()).toBeNull();
  });

  it("reconciles visible lists when selection closes during a background detail sync", async () => {
    vi.useFakeTimers();
    const cached = issueDetail();
    cached.detail_fetched_at = "2026-08-12T21:00:00Z";
    const fresh = issueDetail();
    fresh.detail_fetched_at = "2026-08-12T21:03:00Z";
    const detailResponses = [cached, fresh];
    const syncPost = Promise.withResolvers<{ data: undefined; error: undefined }>();
    const get = vi.fn(async (path: string) =>
      path === "/issues"
        ? { data: [], error: undefined }
        : { data: detailResponses.shift() ?? fresh, error: undefined },
    );
    const onDetailSynchronized = vi.fn();
    const store = createIssuesStore({
      client: mockClient({ GET: get, POST: vi.fn(() => syncPost.promise) }),
      getPage: () => "issues",
      onDetailSynchronized,
    });

    await loadIssueDetail(store, "acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: "background",
    });
    store.clearIssueDetail();

    syncPost.resolve({ data: undefined, error: undefined });
    await vi.advanceTimersByTimeAsync(300);

    await vi.waitFor(() => expect(onDetailSynchronized).toHaveBeenCalledOnce());
    expect(get.mock.calls.filter(([path]) => path === "/issues")).toHaveLength(1);
    expect(store.getIssueDetail()).toBeNull();
  });

  it("applies a local body edit addressed through a provider alias and omitted host", async () => {
    const get = vi.fn().mockResolvedValueOnce({ data: issueDetail() });
    const store = createIssuesStore({ client: mockClient({ GET: get }) });
    await loadIssueDetail(store, "acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    store.setLocalIssueBody("gh", undefined, "acme", "widget", 7, "- [x] done");

    expect(store.getIssueDetail()?.issue.Body).toBe("- [x] done");
    expect(store.hasUnsavedLocalBody()).toBe(true);
  });

  it("coalesces pending issue body saves to the latest captured edit", async () => {
    const initial = issueDetail();
    initial.issue.Body = "initial";
    const firstResponse = Promise.withResolvers<{ data: IssueDetail; error: undefined }>();
    const patch = vi.fn(
      (_path: string, options: { body?: { body?: string } }): Promise<{ data: IssueDetail; error: undefined }> => {
        const response = issueDetail();
        response.issue.Body = options.body?.body ?? "";
        return options.body?.body === "first"
          ? firstResponse.promise
          : Promise.resolve({ data: response, error: undefined });
      },
    );
    const store = createIssuesStore({
      client: mockClient({ GET: vi.fn().mockResolvedValue({ data: initial }), PATCH: patch }),
    });
    const routeRef = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    };
    await loadIssueDetail(store, "acme", "widget", 7, { ...routeRef, sync: false });

    store.setLocalIssueBody("github", "github.com", "acme", "widget", 7, "first");
    const first = store.saveIssueBodyInBackground("acme", "widget", 7, "first", routeRef);
    expect(first).toBeUndefined();
    await vi.waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
    store.setLocalIssueBody("github", "github.com", "acme", "widget", 7, "second");
    store.saveIssueBodyInBackground("acme", "widget", 7, "second", routeRef);
    store.setLocalIssueBody("github", "github.com", "acme", "widget", 7, "third");
    const latest = store.saveIssueBodyInBackground("acme", "widget", 7, "third", routeRef);

    expect(latest).toBeUndefined();
    const confirmedFirst = issueDetail();
    confirmedFirst.issue.Body = "first";
    firstResponse.resolve({ data: confirmedFirst, error: undefined });
    await vi.waitFor(() => expect(patch).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(store.hasUnsavedLocalBody()).toBe(false));
    expect(patch.mock.calls.map(([, options]) => options.body?.body)).toEqual(["first", "third"]);
    expect(store.getIssueDetail()?.issue.Body).toBe("third");
  });

  it("orders opposite issue star requests for the same provider item", async () => {
    const first = Promise.withResolvers<{ data: undefined; error: undefined }>();
    const remove = vi.fn(() => first.promise);
    const add = vi.fn().mockResolvedValue({ data: undefined, error: undefined });
    const listed = issue(7, "alice");
    listed.Starred = true;
    listed.repo_name = "widget";
    listed.repo = { ...listed.repo, name: "widget", repo_path: "acme/widget" };
    const store = createIssuesStore({
      client: mockClient({
        DELETE: remove,
        PUT: add,
        GET: vi.fn().mockResolvedValue({ data: [listed], error: undefined }),
      }),
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    };

    store.loadIssues();
    await vi.waitFor(() => expect(store.isIssuesLoading()).toBe(false));
    const unstar = store.toggleIssueStar(ref, 7, Boolean(store.getIssues()[0]?.Starred));
    await vi.waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect(store.getIssues()[0]?.Starred).toBe(false);
    const star = store.toggleIssueStar(ref, 7, Boolean(store.getIssues()[0]?.Starred));

    expect(unstar).toBeUndefined();
    expect(star).toBeUndefined();
    expect(store.getIssues()[0]?.Starred).toBe(true);
    expect(add).not.toHaveBeenCalled();
    first.resolve({ data: undefined, error: undefined });
    await vi.waitFor(() => expect(add).toHaveBeenCalledTimes(1));
  });

  it("advances detail authority when an issue star is projected", async () => {
    const initial = issueDetail();
    initial.issue.Starred = false;
    const store = createIssuesStore({
      client: mockClient({
        GET: vi.fn().mockResolvedValue({ data: initial, error: undefined }),
        PUT: vi.fn().mockResolvedValue({ data: undefined, error: undefined }),
      }),
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    };
    await loadIssueDetail(store, "acme", "widget", 7, { ...ref, sync: false });
    const before = store.getIssueDetailEnvelopeTick();

    store.toggleIssueStar(ref, 7, false);

    await vi.waitFor(() => expect(store.getIssueDetail()?.issue.Starred).toBe(true));
    expect(store.getIssueDetailEnvelopeTick()).toBeGreaterThan(before);
  });

  it("launches issue state changes synchronously and reports provider failures", async () => {
    const store = createIssuesStore({
      client: mockClient({
        GET: vi.fn().mockResolvedValue({ data: issueDetail(), error: undefined }),
        POST: vi.fn().mockResolvedValue({ error: { detail: "could not close issue" } }),
      }),
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    };
    await loadIssueDetail(store, "acme", "widget", 7, { ...ref, sync: false });

    const result = store.setIssueState(ref, 7, "closed");

    expect(result).toBeUndefined();
    await vi.waitFor(() => expect(getFlash()?.message).toBe("could not close issue"));
    expect(store.getIssueDetail()?.issue.State).toBe("open");
  });

  it("keeps a successful issue state action pending through authoritative reconciliation", async () => {
    const refreshed = Promise.withResolvers<{ data: IssueDetail; error: undefined }>();
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: issueDetail(), error: undefined })
      .mockResolvedValueOnce({ data: [], error: undefined })
      .mockReturnValueOnce(refreshed.promise);
    const store = createIssuesStore({
      client: mockClient({ GET: get, POST: vi.fn().mockResolvedValue({ data: undefined, error: undefined }) }),
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    };
    await loadIssueDetail(store, "acme", "widget", 7, { ...ref, sync: false });
    const settled = vi.fn();

    store.setIssueState(ref, 7, "closed", { onSettled: settled });

    await vi.waitFor(() => expect(store.getIssueDetail()?.issue.State).toBe("closed"));
    expect(settled).not.toHaveBeenCalled();
    const authoritative = issueDetail();
    authoritative.issue.State = "closed";
    refreshed.resolve({ data: authoritative, error: undefined });
    await vi.waitFor(() => expect(settled).toHaveBeenCalledTimes(1));
  });

  it("installs authoritative issue detail when a state mutation needs review", async () => {
    const authoritative = issueDetail();
    authoritative.issue.State = "open";
    const post = vi.fn((path: string) =>
      path.endsWith("/github-state")
        ? Promise.resolve({
            error: {
              code: "conflict",
              type: "about:blank",
              title: "Conflict",
              detail: "issue state changed",
              details: { reason: "stale_state" },
            },
            response: new Response(null, { status: 409 }),
          })
        : Promise.resolve({ data: authoritative, error: undefined }),
    );
    const store = createIssuesStore({
      client: mockClient({ GET: vi.fn().mockResolvedValue({ data: issueDetail() }), POST: post }),
    });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    };
    await loadIssueDetail(store, "acme", "widget", 7, { ...ref, sync: false });
    const settled = Promise.withResolvers<void>();
    const onFailure = vi.fn();

    store.setIssueState(ref, 7, "closed", { onFailure, onSettled: settled.resolve });
    await settled.promise;

    expect(store.getIssueDetail()?.issue.State).toBe("open");
    expect(onFailure).toHaveBeenCalledWith(expect.stringContaining("review"));
  });
});
