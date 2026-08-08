import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { resolveToolingStatus, resetToolingStatusForTest } from "./tooling-status.svelte.js";
import type { ToolingStatusValue } from "./embed-config.svelte.js";

const win = window as any;

const serverStatus: ToolingStatusValue = {
  git: { available: true, version: "2.44.0" },
  gh: { available: true, authenticated: true, user: "octocat", host: "github.com" },
  glab: { available: false, authenticated: false },
};

let runtime: OwnedAppRuntime;

function toolingResponse(status: ToolingStatusValue): Response {
  return new Response(JSON.stringify(status), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  runtime = makeAppRuntime();
});

afterEach(async () => {
  delete win.__kenn_forge_config;
  await Effect.runPromise(runtime.disposeEffect);
  resetToolingStatusForTest();
  vi.unstubAllGlobals();
});

describe("resolveToolingStatus", () => {
  it("returns the embedder's tooling without fetching when embedded", async () => {
    const fetcher = vi.fn(async () => toolingResponse(serverStatus));
    vi.stubGlobal("fetch", fetcher);
    const embedded: ToolingStatusValue = {
      git: { available: true },
    };
    win.__kenn_forge_config = { embed: { tooling: embedded } };
    win.__kenn_forge_notify_config_changed();

    expect(resolveToolingStatus(runtime)).toEqual(embedded);
    await Promise.resolve();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("returns undefined while embedded with no tooling pushed yet", async () => {
    const fetcher = vi.fn(async () => toolingResponse(serverStatus));
    vi.stubGlobal("fetch", fetcher);
    win.__kenn_forge_config = { embed: {} };
    win.__kenn_forge_notify_config_changed();

    expect(resolveToolingStatus(runtime)).toBeUndefined();
    await Promise.resolve();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("fetches the server probe once in standalone mode", async () => {
    const fetcher = vi.fn(async () => toolingResponse(serverStatus));
    vi.stubGlobal("fetch", fetcher);

    expect(resolveToolingStatus(runtime)).toBeUndefined();
    resolveToolingStatus(runtime);
    await vi.waitFor(() => {
      expect(resolveToolingStatus(runtime)).toEqual(serverStatus);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("leaves the status unknown when the standalone fetch fails", async () => {
    const fetcher = vi.fn(async () => {
      throw new Error("server unreachable");
    });
    vi.stubGlobal("fetch", fetcher);

    expect(resolveToolingStatus(runtime)).toBeUndefined();
    await Promise.resolve();
    await Promise.resolve();
    expect(resolveToolingStatus(runtime)).toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("aborts a disposed runtime's probe and lets a replacement runtime retry", async () => {
    const signals: AbortSignal[] = [];
    const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      signals.push(request.signal);
      return new Promise<Response>(() => undefined);
    });
    vi.stubGlobal("fetch", fetcher);
    expect(resolveToolingStatus(runtime)).toBeUndefined();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));

    await Effect.runPromise(runtime.disposeEffect);
    expect(signals[0]?.aborted).toBe(true);
    runtime = makeAppRuntime();
    resetToolingStatusForTest();
    expect(resolveToolingStatus(runtime)).toBeUndefined();

    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  });
});
