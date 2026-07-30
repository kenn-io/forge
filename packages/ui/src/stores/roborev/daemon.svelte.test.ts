import type { RoborevClient } from "../../api/roborev/client.js";
import type { ForgeClient } from "../../types.js";
import { describe, expect, it, vi } from "vite-plus/test";

import { createDaemonStore } from "./daemon.svelte.js";

describe("createDaemonStore", () => {
  it("shares an in-flight poll with a manual health check", async () => {
    let resolveHealth!: (response: {
      data: {
        available: boolean;
        endpoint: string;
        version: string;
      };
    }) => void;
    const health = new Promise<{
      data: {
        available: boolean;
        endpoint: string;
        version: string;
      };
    }>((resolve) => {
      resolveHealth = resolve;
    });
    const forgeGet = vi.fn().mockReturnValue(health);
    const roborevGet = vi.fn().mockResolvedValue({
      data: {
        active_workers: 0,
        applied_jobs: 0,
        canceled_jobs: 0,
        completed_jobs: 0,
        failed_jobs: 0,
        max_workers: 1,
        queue_paused: false,
        queued_jobs: 0,
        rebased_jobs: 0,
        running_jobs: 0,
        skipped_jobs: 0,
        version: "test",
      },
    });
    const store = createDaemonStore({
      client: { GET: roborevGet } as unknown as RoborevClient,
      forgeClient: { GET: forgeGet } as unknown as ForgeClient,
    });

    try {
      store.startPolling();
      const retry = store.checkHealth();

      expect(forgeGet).toHaveBeenCalledTimes(1);

      resolveHealth({
        data: {
          available: true,
          endpoint: "http://roborev:7373",
          version: "test",
        },
      });
      await retry;

      expect(store.isAvailable()).toBe(true);
    } finally {
      store.stopPolling();
    }
  });

  it("ignores health responses from a stopped polling generation", async () => {
    let resolveOldHealth!: (response: {
      data: {
        available: boolean;
        endpoint: string;
        version: string;
      };
    }) => void;
    const oldHealth = new Promise<{
      data: {
        available: boolean;
        endpoint: string;
        version: string;
      };
    }>((resolve) => {
      resolveOldHealth = resolve;
    });
    let resolveCurrentHealth!: (response: {
      data: {
        available: boolean;
        endpoint: string;
        version: string;
      };
    }) => void;
    const currentHealth = new Promise<{
      data: {
        available: boolean;
        endpoint: string;
        version: string;
      };
    }>((resolve) => {
      resolveCurrentHealth = resolve;
    });
    const unavailable = {
      data: {
        available: false,
        endpoint: "http://roborev:7373",
        version: "",
      },
    };
    const forgeGet = vi
      .fn()
      .mockReturnValueOnce(oldHealth)
      .mockResolvedValueOnce(unavailable)
      .mockReturnValueOnce(currentHealth);
    const roborevGet = vi.fn().mockResolvedValue({
      data: {
        active_workers: 1,
        applied_jobs: 2,
        canceled_jobs: 3,
        completed_jobs: 4,
        failed_jobs: 5,
        max_workers: 6,
        queue_paused: false,
        queued_jobs: 7,
        rebased_jobs: 8,
        running_jobs: 9,
        skipped_jobs: 10,
        version: "test",
      },
    });
    const onRecover = vi.fn();
    const store = createDaemonStore({
      client: { GET: roborevGet } as unknown as RoborevClient,
      forgeClient: { GET: forgeGet } as unknown as ForgeClient,
      onRecover,
    });

    try {
      store.startPolling();
      expect(forgeGet).toHaveBeenCalledTimes(1);

      store.stopPolling();
      store.startPolling();
      await vi.waitFor(() => {
        expect(forgeGet).toHaveBeenCalledTimes(2);
        expect(store.isLoading()).toBe(false);
      });
      expect(store.isAvailable()).toBe(false);

      const currentCheck = store.checkHealth();
      expect(store.isLoading()).toBe(true);

      resolveOldHealth({
        data: {
          available: true,
          endpoint: "http://roborev:7373",
          version: "stale",
        },
      });
      await oldHealth;
      await Promise.resolve();
      await Promise.resolve();

      expect(store.isAvailable()).toBe(false);
      expect(store.isLoading()).toBe(true);
      expect(store.getQueuedJobs()).toBe(0);
      expect(store.getWasEverAvailable()).toBe(false);
      expect(onRecover).not.toHaveBeenCalled();
      expect(roborevGet).not.toHaveBeenCalled();

      resolveCurrentHealth(unavailable);
      await currentCheck;
      expect(store.isLoading()).toBe(false);
    } finally {
      store.stopPolling();
    }
  });

  it("polls quickly while unavailable and returns to the healthy cadence after recovery", async () => {
    vi.useFakeTimers();

    const forgeGet = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          available: false,
          endpoint: "http://roborev:7373",
          version: "",
        },
      })
      .mockResolvedValue({
        data: {
          available: true,
          endpoint: "http://roborev:7373",
          version: "test",
        },
      });
    const roborevGet = vi.fn().mockResolvedValue({
      data: {
        active_workers: 0,
        applied_jobs: 0,
        canceled_jobs: 0,
        completed_jobs: 0,
        failed_jobs: 0,
        max_workers: 1,
        queue_paused: false,
        queued_jobs: 0,
        rebased_jobs: 0,
        running_jobs: 0,
        skipped_jobs: 0,
        version: "test",
      },
    });
    const onRecover = vi.fn();
    const store = createDaemonStore({
      client: { GET: roborevGet } as unknown as RoborevClient,
      forgeClient: { GET: forgeGet } as unknown as ForgeClient,
      onRecover,
    });

    try {
      store.startPolling();
      await vi.advanceTimersByTimeAsync(0);

      expect(forgeGet).toHaveBeenCalledTimes(1);
      expect(store.isAvailable()).toBe(false);

      await vi.advanceTimersByTimeAsync(999);
      expect(forgeGet).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(1);
      expect(forgeGet).toHaveBeenCalledTimes(2);
      expect(store.isAvailable()).toBe(true);
      expect(onRecover).toHaveBeenCalledTimes(1);
      expect(roborevGet).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(29_999);
      expect(forgeGet).toHaveBeenCalledTimes(2);

      await vi.advanceTimersByTimeAsync(1);
      expect(forgeGet).toHaveBeenCalledTimes(3);
    } finally {
      store.stopPolling();
      vi.useRealTimers();
    }
  });
});
