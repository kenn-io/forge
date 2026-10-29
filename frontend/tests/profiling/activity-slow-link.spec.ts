import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "../e2e-full/support/e2eServer";

const network = { latency: 700, downloadThroughput: 125_000, uploadThroughput: 125_000, offline: false };

test("measures Activity restoration and idle traffic on a slow connection", async ({ page, context, browser }) => {
  test.setTimeout(300_000);
  const server = await startIsolatedE2EServerWithOptions({ freshProcess: true });
  const outputDir =
    process.env.KENN_FORGE_PROFILE_OUT_DIR ??
    path.resolve("test-results/activity-slow-link", new Date().toISOString().replace(/[:.]/g, "-"));
  const measurements: Array<{ selection: string; iteration: number; durationMs: number; heldRequests: number }> = [];
  const requests: string[] = [];
  let encodedBodyBytes = 0;
  let idle: { durationMs: number; encodedBodyBytes: number; requests: Record<string, number> } | undefined;
  let hold = false;
  let heldRequests = 0;
  let release = Promise.withResolvers<void>();
  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  await cdp.send("Network.emulateNetworkConditions", network);
  cdp.on("Network.dataReceived", (event) => {
    encodedBodyBytes += event.encodedDataLength;
  });
  page.on("request", (request) => {
    const url = new URL(request.url());
    requests.push(`${request.method()} ${url.pathname}${url.search}`);
  });
  try {
    const configured = await page.request.put(`${server.info.base_url}/api/v1/settings`, {
      data: { workspaces: { show_agent_status_in_lists: true } },
    });
    expect(configured.ok()).toBe(true);
    await page.route("**/api/v1/**", async (route) => {
      if (hold) {
        heldRequests++;
        await release.promise;
      }
      await route.continue();
    });
    for (const selection of ["pr:1", "issue:10"]) {
      const [kind, number] = selection.split(":");
      const detailPath = `/api/v1/${kind === "pr" ? "pulls" : "issues"}/github/acme/widgets/${number}`;
      const detailResponse = () =>
        page.waitForResponse(
          (response) => new URL(response.url()).pathname === detailPath && response.request().method() === "GET",
        );
      const initialRead = detailResponse();
      await page.goto(
        `${server.info.base_url}/?view=threaded&collapsed=1&selected=${selection}&provider=github&repo_path=acme%2Fwidgets`,
        { waitUntil: "domcontentloaded" },
      );
      const title = page.locator(".activity-detail .detail-title");
      await expect(title).toBeVisible({ timeout: 120_000 });
      const expectedTitle = await title.textContent();
      await (await initialRead).finished();
      const header = page.getByRole("navigation", { name: "Page", exact: true });
      for (let iteration = 0; iteration < 3; iteration++) {
        await header.getByRole("button", { name: "Workspaces", exact: true }).click();
        await expect(page.locator(".activity-shell")).toHaveCount(0);
        hold = true;
        heldRequests = 0;
        release = Promise.withResolvers<void>();
        const revalidated = detailResponse();
        const started = await page.evaluate(() => performance.now());
        await header.getByRole("button", { name: "Activity", exact: true }).click();
        // No API response can complete: the remembered detail must render locally.
        await expect(title).toHaveText(expectedTitle!, { timeout: 5_000 });
        await expect(title).toBeVisible();
        const painted = await page.evaluate(
          () =>
            new Promise<number>((resolve) =>
              requestAnimationFrame(() => requestAnimationFrame(() => resolve(performance.now()))),
            ),
        );
        const measurement = { selection, iteration, durationMs: painted - started, heldRequests };
        measurements.push(measurement);
        console.log(JSON.stringify(measurement));
        hold = false;
        release.resolve();
        const response = await revalidated;
        expect(response.ok()).toBe(true);
        await response.finished();
      }
    }
    await page.unrouteAll({ behavior: "wait" });
    const offset = requests.length;
    const bytes = encodedBodyBytes;
    const started = performance.now();
    // Intentional measurement window, not synchronization for an assertion.
    console.log("activity-slow-link: starting 60-second idle measurement");
    await page.waitForTimeout(60_000);
    const counts: Record<string, number> = {};
    for (const request of requests.slice(offset)) counts[request] = (counts[request] ?? 0) + 1;
    idle = { durationMs: performance.now() - started, encodedBodyBytes: encodedBodyBytes - bytes, requests: counts };
    console.log(JSON.stringify({ idle }));
  } finally {
    hold = false;
    release.resolve();
    await page.unrouteAll({ behavior: "wait" });
    await mkdir(outputDir, { recursive: true });
    await writeFile(
      path.join(outputDir, "timings.json"),
      JSON.stringify({ network, browserVersion: browser.version(), measurements, idle }, null, 2) + "\n",
    );
    await server.stop();
  }
});
