// @vitest-environment node
//
// Converted from frontend/tests/e2e-full/pool-options.spec.ts. This is a
// node-environment Vitest test that drives the REAL pooled e2e-server support
// module against a REAL Go test server (cmd/e2e-server), exercising the
// lease -> stop -> re-lease reset cycle across option combinations.
//
// PLAYWRIGHT_E2E_FRONTEND_READY=1 makes ensureEmbeddedFrontend() a no-op so
// the pool does not rebuild/copy the frontend dist before spawning. This test
// only exercises API endpoints (/api/v1/repos, /api/v1/settings), so the
// embedded SPA bundle is irrelevant; the server serves a placeholder index
// with status 200 when the real bundle is absent, which satisfies the pool's
// reachability probe. The flag must be set before the first spawn, hence at
// module top.
process.env.PLAYWRIGHT_E2E_FRONTEND_READY = "1";

import { afterAll, describe, expect, it } from "vite-plus/test";
import { startIsolatedE2EServerWithOptions } from "../../../tests/e2e-full/support/e2eServer";

type RepoRow = { Owner: string; Name: string };
type SettingsPayload = { modes: Record<string, boolean> };

async function fetchRepos(baseURL: string): Promise<RepoRow[]> {
  const response = await fetch(`${baseURL}/api/v1/repos`);
  expect(response.ok).toBe(true);
  return (await response.json()) as RepoRow[];
}

async function fetchModes(baseURL: string): Promise<Record<string, boolean>> {
  const response = await fetch(`${baseURL}/api/v1/settings`);
  expect(response.ok).toBe(true);
  return ((await response.json()) as SettingsPayload).modes;
}

describe("isolated e2e server pool", () => {
  // The pooled `go run` child handle keeps this worker's event loop alive,
  // which makes the full-suite Vitest close hit its 10s force-close timeout.
  // stop() has release semantics (the process stays pooled for reuse), so
  // explicitly SIGTERM the pooled server once the test is done. The support
  // module's own exit hooks remain as backstop and tolerate the early kill.
  const leasedPIDs = new Set<number>();
  afterAll(() => {
    for (const pid of leasedPIDs) {
      try {
        process.kill(pid, "SIGTERM");
      } catch {
        // Process already exited.
      }
    }
  });
  // The pooled lease options (defaultPlatformHost, visibleImportedModes)
  // interact across lease → stop → re-lease cycles: stop() resets the
  // server to defaults in the background, and the next lease re-resets
  // when its options differ. This test walks the combinations
  // sequentially in one worker, so later leases exercise reuse of the
  // same pooled process rather than fresh spawns, pinning that no
  // option state leaks from one lease into the next.
  //
  // Generous timeout: the first lease may compile cmd/e2e-server via
  // `go run` on a cold build cache.
  it("pooled server leases reset cleanly across option combinations", async () => {
    const ghe = await startIsolatedE2EServerWithOptions({ defaultPlatformHost: "ghe.example.com" });
    leasedPIDs.add(ghe.info.pid);
    try {
      const repos = await fetchRepos(ghe.info.base_url);
      expect(repos.some((repo) => repo.Owner === "enterprise" && repo.Name === "service")).toBe(true);
    } finally {
      await ghe.stop();
    }

    const modes = await startIsolatedE2EServerWithOptions({ visibleImportedModes: true });
    leasedPIDs.add(modes.info.pid);
    try {
      const visibility = await fetchModes(modes.info.base_url);
      expect(visibility.kata).toBe(true);
      expect(visibility.docs).toBe(true);
      expect(visibility.messages).toBe(true);
      const repos = await fetchRepos(modes.info.base_url);
      expect(repos.some((repo) => repo.Owner === "enterprise")).toBe(false);
    } finally {
      await modes.stop();
    }

    const plain = await startIsolatedE2EServerWithOptions();
    leasedPIDs.add(plain.info.pid);
    try {
      const visibility = await fetchModes(plain.info.base_url);
      expect(visibility.kata).toBe(false);
      expect(visibility.docs).toBe(false);
      expect(visibility.messages).toBe(false);
      const repos = await fetchRepos(plain.info.base_url);
      expect(repos.some((repo) => repo.Owner === "enterprise")).toBe(false);
      expect(repos.some((repo) => repo.Owner === "acme" && repo.Name === "widgets")).toBe(true);
    } finally {
      await plain.stop();
    }
  }, 180_000);
});
