import { describe, expect, it, vi } from "vite-plus/test";

import { createDetailStore } from "@middleman/ui/stores/detail";
import { createIssuesStore } from "@middleman/ui/stores/issues";
import type { MiddlemanClient } from "@middleman/ui";
import type { Label } from "@middleman/ui/api/types";

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
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("octo", "repo", 1, routeRef);
    await store.setPullLabels("octo", "repo", 1, ["triage"]);

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
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("octo", "repo", 1, routeRef);
    const mutation = store.setPullLabels("octo", "repo", 1, ["triage"]);
    await store.loadDetail("octo", "repo", 1, otherRouteRef);
    put.resolve({ data: { labels: [label("triage")] } });
    await mutation;

    expect(store.getDetail()?.repo.provider).toBe("gitlab");
    expect(store.getDetail()?.merge_request.labels?.map((item) => item.name)).toEqual(["gitlab-label"]);
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
    } as unknown as MiddlemanClient;
    const store = createIssuesStore({ client });

    await store.loadIssueDetail("octo", "repo", 2, routeRef);
    await store.setIssueLabels("octo", "repo", 2, ["triage"]);

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
    } as unknown as MiddlemanClient;
    const store = createIssuesStore({ client });

    await store.loadIssueDetail("octo", "repo", 2, routeRef);
    const mutation = store.setIssueLabels("octo", "repo", 2, ["triage"]);
    await store.loadIssueDetail("octo", "repo", 2, otherRouteRef);
    put.resolve({ data: { labels: [label("triage")] } });
    await mutation;

    expect(store.getIssueDetail()?.repo.provider).toBe("gitlab");
    expect(store.getIssueDetail()?.issue.labels?.map((item) => item.name)).toEqual(["gitlab-label"]);
  });
});
