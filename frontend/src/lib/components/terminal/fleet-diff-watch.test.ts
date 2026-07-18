import { describe, expect, it } from "vite-plus/test";
import { shouldRetryFleetDiffWatch } from "./fleet-diff-watch.js";

describe("shouldRetryFleetDiffWatch", () => {
  it.each([
    { status: 404, retry: false },
    { status: 405, retry: false },
    { status: 408, retry: true },
    { status: 409, retry: true },
    { status: 425, retry: true },
    { status: 429, retry: true },
    { status: 500, retry: true },
    { status: 501, retry: false },
    { status: 503, retry: true },
  ])("returns $retry for HTTP $status", ({ status, retry }) => {
    expect(shouldRetryFleetDiffWatch(status)).toBe(retry);
  });
});
