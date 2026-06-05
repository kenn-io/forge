import { describe, expect, it, vi } from "vite-plus/test";

import { createDetailStore } from "@middleman/ui/stores/detail";
import { createIssuesStore } from "@middleman/ui/stores/issues";
import * as flash from "@middleman/ui/stores/flash";
import type { MiddlemanClient } from "@middleman/ui";

describe("provider-aware detail API routes", () => {
  it("loads PR detail through the provider item endpoint", async () => {
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "Group/SubGroup",
          repo_name: "Project",
          merge_request: { Number: 12 },
          events: [],
        },
      })),
      POST: vi.fn(),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("Group/SubGroup", "Project", 12, {
      sync: false,
      provider: "gitlab",
      platformHost: "gitlab.example.com:8443",
      repoPath: "Group/SubGroup/Project",
    } as never);

    expect(client.GET).toHaveBeenCalledWith("/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}", {
      params: {
        path: {
          provider: "gitlab",
          platform_host: "gitlab.example.com:8443",
          owner: "Group/SubGroup",
          name: "Project",
          number: 12,
        },
      },
    });
  });

  it("refreshes pending PR CI through the provider CI endpoint", async () => {
    const detail = {
      repo_owner: "Group/SubGroup",
      repo_name: "Project",
      repo: {
        provider: "gitlab",
        platform_host: "gitlab.example.com:8443",
        owner: "Group/SubGroup",
        name: "Project",
        repo_path: "Group/SubGroup/Project",
      },
      merge_request: { Number: 12 },
      events: [],
    };
    const client = {
      GET: vi.fn(async () => ({
        data: detail,
      })),
      POST: vi.fn(async () => ({
        data: detail,
      })),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("Group/SubGroup", "Project", 12, {
      sync: false,
      provider: "gitlab",
      platformHost: "gitlab.example.com:8443",
      repoPath: "Group/SubGroup/Project",
    } as never);

    await store.refreshPendingCI("Group/SubGroup", "Project", 12, {
      provider: "gitlab",
      platformHost: "gitlab.example.com:8443",
      repoPath: "Group/SubGroup/Project",
    });

    expect(client.POST).toHaveBeenCalledWith(
      "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/ci-refresh",
      {
        params: {
          path: {
            provider: "gitlab",
            platform_host: "gitlab.example.com:8443",
            owner: "Group/SubGroup",
            name: "Project",
            number: 12,
          },
        },
      },
    );
  });

  it("flashes CI refresh warnings returned with preserved detail", async () => {
    const detail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
      merge_request: { Number: 1, CIStatus: "pending" },
      warnings: ["Could not refresh CI checks; showing last known status."],
      events: [],
    };
    const client = {
      GET: vi.fn(async () => ({ data: detail })),
      POST: vi.fn(async () => ({ data: detail })),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const showFlash = vi.spyOn(flash, "showFlash").mockImplementation(() => {});
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: false,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);

    await store.refreshPendingCI("acme", "widgets", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    });

    expect(showFlash).toHaveBeenCalledWith("Could not refresh CI checks; showing last known status.");
    showFlash.mockRestore();
  });

  it("flashes hard CI refresh request failures", async () => {
    const detail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
      merge_request: { Number: 1, CIStatus: "pending" },
      events: [],
    };
    const client = {
      GET: vi.fn(async () => ({ data: detail })),
      POST: vi.fn(async () => ({
        error: { detail: "refresh PR CI: upstream unavailable" },
      })),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const showFlash = vi.spyOn(flash, "showFlash").mockImplementation(() => {});
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: false,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);

    await store.refreshPendingCI("acme", "widgets", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    });

    expect(showFlash).toHaveBeenCalledWith("refresh PR CI: upstream unavailable");
    showFlash.mockRestore();
  });

  it("serializes overlapping pending PR CI refreshes", async () => {
    const detail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
      merge_request: { Number: 1 },
      events: [],
    };
    let resolvePost!: () => void;
    const postDone = new Promise<void>((resolve) => {
      resolvePost = resolve;
    });
    const client = {
      GET: vi.fn(async () => ({ data: detail })),
      POST: vi.fn(async () => {
        await postDone;
        return { data: detail };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: false,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);

    const first = store.refreshPendingCI("acme", "widgets", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    });
    const second = store.refreshPendingCI("acme", "widgets", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    });
    await Promise.resolve();

    expect(client.POST).toHaveBeenCalledTimes(1);
    resolvePost();
    await Promise.all([first, second]);
  });

  it("keeps CI refreshes in CI-only mode when workflow approval sync is disabled", async () => {
    const pendingDetail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        capabilities: { workflow_approval: true },
      },
      merge_request: {
        Number: 1,
        State: "open",
        CIStatus: "pending",
        CIChecksJSON: JSON.stringify([{ name: "build", status: "in_progress", conclusion: "" }]),
        CIHadPending: true,
      },
      workflow_approval: { checked: false, required: false, count: 0 },
      events: [],
    };
    const postPaths: string[] = [];
    const client = {
      GET: vi.fn(async () => ({ data: pendingDetail })),
      POST: vi.fn(async (path: string) => {
        postPaths.push(path);
        return { data: pendingDetail };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: false,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);

    await store.refreshPendingCI("acme", "widgets", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
      workflowApprovalSync: false,
    });

    expect(postPaths).toEqual(["/pulls/{provider}/{owner}/{name}/{number}/ci-refresh"]);
  });

  it("promotes CI refresh results that may need workflow approval to foreground PR sync", async () => {
    const pendingDetail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        capabilities: { workflow_approval: true },
      },
      merge_request: {
        Number: 1,
        State: "open",
        CIStatus: "pending",
        CIChecksJSON: JSON.stringify([{ name: "build", status: "in_progress", conclusion: "" }]),
        CIHadPending: true,
      },
      workflow_approval: { checked: false, required: false, count: 0 },
      events: [],
    };
    const syncedDetail = {
      ...pendingDetail,
      workflow_approval: { checked: true, required: true, count: 1 },
    };
    const postPaths: string[] = [];
    const client = {
      GET: vi.fn(async () => ({ data: pendingDetail })),
      POST: vi.fn(async (path: string) => {
        postPaths.push(path);
        return {
          data: path.endsWith("/sync") ? syncedDetail : pendingDetail,
        };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: false,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);

    await store.refreshPendingCI("acme", "widgets", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    });

    expect(postPaths).toEqual([
      "/pulls/{provider}/{owner}/{name}/{number}/ci-refresh",
      "/pulls/{provider}/{owner}/{name}/{number}/sync",
    ]);
    expect(store.getDetail()?.workflow_approval).toEqual({
      checked: true,
      required: true,
      count: 1,
    });
  });

  it("promotes pending workflow approval checks to foreground PR sync", async () => {
    const pendingDetail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        capabilities: { workflow_approval: true },
      },
      merge_request: {
        Number: 1,
        State: "open",
        CIStatus: "pending",
        CIChecksJSON: JSON.stringify([{ name: "build", status: "in_progress", conclusion: "" }]),
        CIHadPending: true,
      },
      workflow_approval: { checked: false, required: false, count: 0 },
      events: [],
    };
    const syncedDetail = {
      ...pendingDetail,
      workflow_approval: { checked: true, required: true, count: 1 },
    };
    const postPaths: string[] = [];
    const client = {
      GET: vi.fn(async () => ({ data: pendingDetail })),
      POST: vi.fn(async (path: string) => {
        postPaths.push(path);
        return { data: syncedDetail };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: "background",
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);
    await Promise.resolve();
    await Promise.resolve();

    expect(postPaths).toEqual(["/pulls/{provider}/{owner}/{name}/{number}/sync"]);
    expect(store.getDetail()?.workflow_approval).toEqual({
      checked: true,
      required: true,
      count: 1,
    });
  });

  it("keeps ordinary pending CI refreshes on the async PR sync endpoint", async () => {
    const detail = {
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        capabilities: { workflow_approval: true },
      },
      merge_request: {
        Number: 1,
        State: "open",
        CIStatus: "pending",
        CIChecksJSON: JSON.stringify([{ name: "build", status: "in_progress", conclusion: "" }]),
        CIHadPending: false,
      },
      workflow_approval: { checked: false, required: false, count: 0 },
      events: [],
    };
    const postPaths: string[] = [];
    const client = {
      GET: vi.fn(async () => ({ data: detail })),
      POST: vi.fn(async (path: string) => {
        postPaths.push(path);
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createDetailStore({ client });

    await store.loadDetail("acme", "widgets", 1, {
      sync: "background",
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
    } as never);
    await Promise.resolve();
    await Promise.resolve();

    expect(postPaths).toEqual(["/pulls/{provider}/{owner}/{name}/{number}/sync/async"]);
  });

  it("loads issue detail through the provider item endpoint", async () => {
    const client = {
      GET: vi.fn(async () => ({
        data: {
          repo_owner: "Group/SubGroup",
          repo_name: "Project",
          issue: { Number: 7 },
          events: [],
        },
      })),
      POST: vi.fn(),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;
    const store = createIssuesStore({ client });

    await store.loadIssueDetail("Group/SubGroup", "Project", 7, {
      sync: false,
      provider: "gitlab",
      platformHost: "gitlab.example.com:8443",
      repoPath: "Group/SubGroup/Project",
    } as never);

    expect(client.GET).toHaveBeenCalledWith("/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}", {
      params: {
        path: {
          provider: "gitlab",
          platform_host: "gitlab.example.com:8443",
          owner: "Group/SubGroup",
          name: "Project",
          number: 7,
        },
      },
    });
  });
});
