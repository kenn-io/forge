import type { RoborevClient } from "../../api/roborev/client.js";
import type { MiddlemanClient } from "../../types.js";
import { describe, expect, it, vi } from "vite-plus/test";

import { createDaemonStore } from "./daemon.svelte.js";

describe("createDaemonStore", () => {
  it("polls quickly while unavailable and returns to the healthy cadence after recovery", async () => {
    vi.useFakeTimers();

    const middlemanGet = vi
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
      middlemanClient: { GET: middlemanGet } as unknown as MiddlemanClient,
      onRecover,
    });

    try {
      store.startPolling();
      await vi.advanceTimersByTimeAsync(0);

      expect(middlemanGet).toHaveBeenCalledTimes(1);
      expect(store.isAvailable()).toBe(false);

      await vi.advanceTimersByTimeAsync(999);
      expect(middlemanGet).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(1);
      expect(middlemanGet).toHaveBeenCalledTimes(2);
      expect(store.isAvailable()).toBe(true);
      expect(onRecover).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(29_999);
      expect(middlemanGet).toHaveBeenCalledTimes(2);

      await vi.advanceTimersByTimeAsync(1);
      expect(middlemanGet).toHaveBeenCalledTimes(3);
    } finally {
      store.stopPolling();
      vi.useRealTimers();
    }
  });
});
