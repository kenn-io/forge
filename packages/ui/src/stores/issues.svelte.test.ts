import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Issue, IssueDetail } from "../api/types.js";
import type { MiddlemanClient } from "../types.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";
import { createIssuesStore } from "./issues.svelte.js";

afterEach(() => {
  for (const item of getFlashes()) dismissFlash(item.id);
});

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
    } as unknown as MiddlemanClient;
    const store = createIssuesStore({ client });

    await store.loadIssues();
    store.hydrateDefaults({ hide_bots: true });

    expect(store.getHideBots()).toBe(true);
    expect(store.getIssues().map((item) => item.Author)).toEqual([
      "alice",
      "Talbot",
      "Abbot",
      "project_alpha_bot_build",
    ]);
  });

  it("persists visibility changes and adopts the saved response", async () => {
    const put = vi.fn(async () => ({
      data: { issues: { hide_bots: true } },
      error: undefined,
    }));
    const store = createIssuesStore({
      client: { PUT: put } as unknown as MiddlemanClient,
    });

    await store.setHideBots(true);

    expect(put).toHaveBeenCalledWith("/settings", {
      body: { issues: { hide_bots: true } },
    });
    expect(store.getHideBots()).toBe(true);
  });

  it("prevents out-of-order responses by serializing rapid visibility changes", async () => {
    let resolveFirst!: (response: { data: { issues: { hide_bots: boolean } }; error: undefined }) => void;
    const firstResponse = new Promise<{
      data: { issues: { hide_bots: boolean } };
      error: undefined;
    }>((resolve) => {
      resolveFirst = resolve;
    });
    const put = vi
      .fn()
      .mockImplementationOnce(async () => firstResponse)
      .mockResolvedValueOnce({
        data: { issues: { hide_bots: false } },
        error: undefined,
      });
    const store = createIssuesStore({
      client: { PUT: put } as unknown as MiddlemanClient,
    });

    const hide = store.setHideBots(true);
    const show = store.setHideBots(false);

    expect(store.getHideBots()).toBe(false);
    expect(put).toHaveBeenCalledTimes(1);

    resolveFirst({
      data: { issues: { hide_bots: true } },
      error: undefined,
    });
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(store.getHideBots()).toBe(false);

    await Promise.all([hide, show]);
    expect(put.mock.calls.map(([, options]) => options.body.issues.hide_bots)).toEqual([true, false]);
    expect(store.getHideBots()).toBe(false);
  });

  it("restores the previous preference when persistence fails", async () => {
    const store = createIssuesStore({
      client: {
        PUT: vi.fn(async () => ({
          data: undefined,
          error: { detail: "settings unavailable" },
        })),
      } as unknown as MiddlemanClient,
    });

    await store.setHideBots(true);

    expect(store.getHideBots()).toBe(false);
    expect(getFlash()).toMatchObject({ message: "settings unavailable", tone: "danger" });
  });

  it("restores the previous preference when the settings request throws", async () => {
    const store = createIssuesStore({
      client: {
        PUT: vi.fn(async () => {
          throw new Error("network unavailable");
        }),
      } as unknown as MiddlemanClient,
    });

    await store.setHideBots(true);

    expect(store.getHideBots()).toBe(false);
    expect(getFlash()).toMatchObject({ message: "network unavailable", tone: "danger" });
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

function mockClient(overrides: Partial<MiddlemanClient> = {}): MiddlemanClient {
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
  } as unknown as MiddlemanClient;
}

describe("createIssuesStore", () => {
  it("applies a local body edit addressed through a provider alias and omitted host", async () => {
    const get = vi.fn().mockResolvedValueOnce({ data: issueDetail() });
    const store = createIssuesStore({ client: mockClient({ GET: get }) });
    await store.loadIssueDetail("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    store.setLocalIssueBody("gh", undefined, "acme", "widget", 7, "- [x] done");

    expect(store.getIssueDetail()?.issue.Body).toBe("- [x] done");
    expect(store.hasUnsavedLocalBody()).toBe(true);
  });
});
