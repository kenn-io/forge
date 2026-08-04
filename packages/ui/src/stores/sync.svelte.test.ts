import { describe, expect, it, vi } from "vite-plus/test";
import type { ForgeClient } from "../types.js";
import { createSyncStore } from "./sync.svelte.js";

describe("sync store", () => {
  it("keeps a triggered sync running until the server observes or completes it", async () => {
    const previousLastRunAt = "2026-08-02T19:00:00Z";
    const completedLastRunAt = "2026-08-02T20:00:00Z";
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: previousLastRunAt, last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore({
      client: {
        GET: get,
        POST: vi.fn(async () => ({ error: undefined })),
      } as unknown as ForgeClient,
    });
    const completed = vi.fn();
    store.subscribeSyncComplete(completed);
    store.setSyncStatus({ running: false, last_run_at: previousLastRunAt, last_error: "" });

    await store.triggerSync();

    expect(store.getSyncState()?.running).toBe(true);
    expect(completed).not.toHaveBeenCalled();

    store.setSyncStatus({ running: true, last_run_at: previousLastRunAt, last_error: "" });
    store.setSyncStatus({ running: false, last_run_at: completedLastRunAt, last_error: "" });

    expect(completed).toHaveBeenCalledOnce();
  });

  it("accepts a triggered sync completion even when running was not observed", async () => {
    const previousLastRunAt = "2026-08-02T19:00:00Z";
    const completedLastRunAt = "2026-08-02T20:00:00Z";
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: previousLastRunAt, last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore({
      client: {
        GET: get,
        POST: vi.fn(async () => ({ error: undefined })),
      } as unknown as ForgeClient,
    });
    const completed = vi.fn();
    store.subscribeSyncComplete(completed);
    store.setSyncStatus({ running: false, last_run_at: previousLastRunAt, last_error: "" });

    await store.triggerSync();
    store.setSyncStatus({ running: false, last_run_at: completedLastRunAt, last_error: "" });

    expect(store.getSyncState()).toEqual({
      running: false,
      last_run_at: completedLastRunAt,
      last_error: "",
    });
    expect(completed).toHaveBeenCalledOnce();
  });

  it("does not complete a triggered sync from stale idle status when local status was null", async () => {
    const staleLastRunAt = "2026-08-02T19:00:00Z";
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: staleLastRunAt, last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore({
      client: {
        GET: get,
        POST: vi.fn(async () => ({ error: undefined })),
      } as unknown as ForgeClient,
    });
    const completed = vi.fn();
    store.subscribeSyncComplete(completed);

    await store.triggerSync();

    expect(get.mock.calls.filter(([path]) => path === "/sync/status")).toHaveLength(2);
    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: staleLastRunAt,
      last_error: "",
    });
    expect(completed).not.toHaveBeenCalled();
  });

  it("ignores a status poll that started before a triggered sync", async () => {
    let resolveOldStatus!: (value: { data: { running: boolean; last_run_at: string; last_error: string } }) => void;
    let resolveTrigger!: (value: { error: undefined }) => void;
    let syncStatusCalls = 0;
    const get = vi.fn((path: string) => {
      if (path === "/rate-limits") {
        return Promise.resolve({ data: { provider_pools: {}, local_ceilings: {} } });
      }
      syncStatusCalls += 1;
      if (syncStatusCalls === 1) {
        return new Promise((resolve) => {
          resolveOldStatus = resolve;
        });
      }
      return Promise.resolve({
        data: { running: true, last_run_at: "2026-08-02T20:00:00Z", last_error: "" },
      });
    });
    const post = vi.fn(
      () =>
        new Promise<{ error: undefined }>((resolve) => {
          resolveTrigger = resolve;
        }),
    );
    const store = createSyncStore({
      client: { GET: get, POST: post } as unknown as ForgeClient,
    });
    const completed = vi.fn();
    store.subscribeSyncComplete(completed);

    const oldPoll = store.refreshSyncStatus();
    await vi.waitFor(() => expect(syncStatusCalls).toBe(1));
    const triggered = store.triggerSync();
    await vi.waitFor(() => expect(store.getSyncState()?.running).toBe(true));

    resolveOldStatus({
      data: { running: false, last_run_at: "2026-08-02T19:00:00Z", last_error: "" },
    });
    await oldPoll;

    expect(store.getSyncState()?.running).toBe(true);
    expect(completed).not.toHaveBeenCalled();

    resolveTrigger({ error: undefined });
    await triggered;
  });

  it("passes selected repo filters as sync priorities", async () => {
    const post = vi.fn(async () => ({ error: undefined }));
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: "", last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore({
      client: {
        GET: get,
        POST: post,
      } as unknown as ForgeClient,
      getPriorityRepos: () => "github|github.com/acme/first, github|github.com/acme/second",
    });

    await store.triggerSync();

    expect(post).toHaveBeenCalledWith("/sync", {
      params: {
        query: {
          priority_repo: ["github|github.com/acme/first", "github|github.com/acme/second"],
        },
      },
    });
  });

  it("passes one provider-qualified repository as the only sync scope", async () => {
    const post = vi.fn(async () => ({ error: undefined }));
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: "", last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore({
      client: {
        GET: get,
        POST: post,
      } as unknown as ForgeClient,
    });

    await store.triggerRepoSync("gitlab|gitlab.example.com/group/subgroup/project");

    expect(post).toHaveBeenCalledWith("/sync", {
      params: {
        query: {
          only_repo: ["gitlab|gitlab.example.com/group/subgroup/project"],
        },
      },
    });
  });
});
