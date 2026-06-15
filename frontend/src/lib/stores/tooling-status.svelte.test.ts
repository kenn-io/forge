import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { resolveToolingStatus, resetToolingStatusForTest } from "./tooling-status.svelte.js";
import type { ToolingStatusValue } from "./embed-config.svelte.js";

const win = window as any;

const serverStatus: ToolingStatusValue = {
  git: { available: true, version: "2.44.0" },
  gh: { available: true, authenticated: true, user: "octocat", host: "github.com" },
  glab: { available: false, authenticated: false },
};

afterEach(() => {
  delete win.__middleman_config;
  resetToolingStatusForTest();
});

describe("resolveToolingStatus", () => {
  it("returns the embedder's tooling without fetching when embedded", async () => {
    const fetcher = vi.fn(async () => serverStatus);
    resetToolingStatusForTest(fetcher);
    const embedded: ToolingStatusValue = {
      git: { available: true },
    };
    win.__middleman_config = { embed: { tooling: embedded } };
    win.__middleman_notify_config_changed();

    expect(resolveToolingStatus()).toEqual(embedded);
    await Promise.resolve();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("returns undefined while embedded with no tooling pushed yet", async () => {
    const fetcher = vi.fn(async () => serverStatus);
    resetToolingStatusForTest(fetcher);
    win.__middleman_config = { embed: {} };
    win.__middleman_notify_config_changed();

    expect(resolveToolingStatus()).toBeUndefined();
    await Promise.resolve();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("fetches the server probe once in standalone mode", async () => {
    const fetcher = vi.fn(async () => serverStatus);
    resetToolingStatusForTest(fetcher);

    expect(resolveToolingStatus()).toBeUndefined();
    resolveToolingStatus();
    await vi.waitFor(() => {
      expect(resolveToolingStatus()).toEqual(serverStatus);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("leaves the status unknown when the standalone fetch fails", async () => {
    const fetcher = vi.fn(async () => {
      throw new Error("server unreachable");
    });
    resetToolingStatusForTest(fetcher);

    expect(resolveToolingStatus()).toBeUndefined();
    await Promise.resolve();
    await Promise.resolve();
    expect(resolveToolingStatus()).toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
