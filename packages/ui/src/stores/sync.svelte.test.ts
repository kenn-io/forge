import { describe, expect, it, vi } from "vitest";
import type { MiddlemanClient } from "../types.js";
import { createSyncStore } from "./sync.svelte.js";

describe("sync store", () => {
  it("passes selected repo filters as sync priorities", async () => {
    const post = vi.fn(async () => ({ error: undefined }));
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return { data: { running: false, last_run_at: "", last_error: "" } };
      }
      return { data: { hosts: {} } };
    });
    const store = createSyncStore({
      client: {
        GET: get,
        POST: post,
      } as unknown as MiddlemanClient,
      getPriorityRepos: () =>
        "github.com/acme/first, github.com/acme/second",
    });

    await store.triggerSync();

    expect(post).toHaveBeenCalledWith("/sync", {
      params: {
        query: {
          priority_repo: [
            "github.com/acme/first",
            "github.com/acme/second",
          ],
        },
      },
    });
  });
});
