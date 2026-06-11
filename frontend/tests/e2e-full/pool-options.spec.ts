import { expect, test, type APIRequestContext } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "./support/e2eServer";

type RepoRow = { Owner: string; Name: string };
type SettingsPayload = { modes: Record<string, boolean> };

async function fetchRepos(request: APIRequestContext, baseURL: string): Promise<RepoRow[]> {
  const response = await request.get(`${baseURL}/api/v1/repos`);
  expect(response.ok()).toBe(true);
  return (await response.json()) as RepoRow[];
}

async function fetchModes(request: APIRequestContext, baseURL: string): Promise<Record<string, boolean>> {
  const response = await request.get(`${baseURL}/api/v1/settings`);
  expect(response.ok()).toBe(true);
  return ((await response.json()) as SettingsPayload).modes;
}

// The pooled lease options (defaultPlatformHost, visibleImportedModes)
// interact across lease → stop → re-lease cycles: stop() resets the
// server to defaults in the background, and the next lease re-resets
// when its options differ. This test walks the combinations
// sequentially in one worker, so later leases exercise reuse of the
// same pooled process rather than fresh spawns, pinning that no
// option state leaks from one lease into the next.
test("pooled server leases reset cleanly across option combinations", async ({ request }) => {
  const ghe = await startIsolatedE2EServerWithOptions({ defaultPlatformHost: "ghe.example.com" });
  try {
    const repos = await fetchRepos(request, ghe.info.base_url);
    expect(repos.some((repo) => repo.Owner === "enterprise" && repo.Name === "service")).toBe(true);
  } finally {
    await ghe.stop();
  }

  const modes = await startIsolatedE2EServerWithOptions({ visibleImportedModes: true });
  try {
    const visibility = await fetchModes(request, modes.info.base_url);
    expect(visibility.kata).toBe(true);
    expect(visibility.docs).toBe(true);
    expect(visibility.messages).toBe(true);
    const repos = await fetchRepos(request, modes.info.base_url);
    expect(repos.some((repo) => repo.Owner === "enterprise")).toBe(false);
  } finally {
    await modes.stop();
  }

  const plain = await startIsolatedE2EServerWithOptions();
  try {
    const visibility = await fetchModes(request, plain.info.base_url);
    expect(visibility.kata).toBe(false);
    expect(visibility.docs).toBe(false);
    expect(visibility.messages).toBe(false);
    const repos = await fetchRepos(request, plain.info.base_url);
    expect(repos.some((repo) => repo.Owner === "enterprise")).toBe(false);
    expect(repos.some((repo) => repo.Owner === "acme" && repo.Name === "widgets")).toBe(true);
  } finally {
    await plain.stop();
  }
});
