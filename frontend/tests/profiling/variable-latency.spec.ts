import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { expect, test, type Request } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "../e2e-full/support/e2eServer";

// Repeatable link conditions at each navigation, including long tails.
const latencies = [700, 1200, 1500, 900, 4000, 1000, 3500, 800];
const network = { latency: 700, downloadThroughput: 125_000, uploadThroughput: 125_000, offline: false };

test("records navigation while variable-latency refreshes overlap", async ({ page, context, browser }) => {
  test.setTimeout(300_000);
  const server = await startIsolatedE2EServerWithOptions({ freshProcess: true });
  const outputDir =
    process.env.KENN_FORGE_PROFILE_OUT_DIR ??
    path.resolve("test-results/variable-latency", new Date().toISOString().replace(/[:.]/g, "-"));
  const measurements: Array<{ selection: string; iteration: number; durationMs: number }> = [];
  const requests: Array<{
    path: string;
    method: string;
    latencyMs: number;
    startedAt: number;
    durationMs?: number;
    failure?: string | undefined;
  }> = [];
  const byRequest = new Map<Request, (typeof requests)[number]>();
  let latencyMs = network.latency;
  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  await cdp.send("Network.emulateNetworkConditions", network);
  page.on("request", (request) => {
    const pathname = new URL(request.url()).pathname;
    if (!pathname.startsWith("/api/v1/")) return;
    const entry = { path: pathname, method: request.method(), latencyMs, startedAt: performance.now() };
    requests.push(entry);
    byRequest.set(request, entry);
  });
  page.on("requestfinished", (request) => {
    const entry = byRequest.get(request);
    if (entry) entry.durationMs = performance.now() - entry.startedAt;
  });
  page.on("requestfailed", (request) => {
    const entry = byRequest.get(request);
    if (entry) {
      entry.durationMs = performance.now() - entry.startedAt;
      entry.failure = request.failure()?.errorText;
    }
  });
  try {
    const configured = await page.request.put(`${server.info.base_url}/api/v1/settings`, {
      data: { workspaces: { show_agent_status_in_lists: true } },
    });
    expect(configured.ok()).toBe(true);
    for (const { selection, url, pageName, scope } of [
      {
        selection: "pr:1",
        url: "/?view=threaded&collapsed=1&selected=pr:1&provider=github&repo_path=acme%2Fwidgets",
        pageName: "Activity",
        scope: ".activity-detail",
      },
      {
        selection: "issue:10",
        url: "/?view=threaded&collapsed=1&selected=issue:10&provider=github&repo_path=acme%2Fwidgets",
        pageName: "Activity",
        scope: ".activity-detail",
      },
      { selection: "pulls:5", url: "/pulls/github/acme/widgets/5", pageName: "PRs", scope: ".pull-detail" },
    ]) {
      console.log(`variable-latency: loading ${selection}`);
      await page.goto(`${server.info.base_url}${url}`, { waitUntil: "domcontentloaded" });
      const title = page.locator(`${scope} .detail-title`);
      const body = page.locator(`${scope} .body-section .markdown-body`).first();
      const input = page.locator(`${scope} .comment-editor-input`).first();
      await expect(title).toBeVisible({ timeout: 120_000 });
      if (pageName === "Activity") await expect(body).toBeVisible();
      await expect(input).toBeEditable();
      if (selection === "pr:1") await expect(page.locator(`${scope} .btn--merge`)).toBeVisible();
      const expectedTitle = await title.textContent();
      const expectedBody = (await body.count()) ? await body.textContent() : null;
      let draft = "";
      const header = page.getByRole("navigation", { name: "Page", exact: true });
      for (let iteration = 0; iteration < 8; iteration++) {
        latencyMs = latencies[iteration]!;
        await cdp.send("Network.emulateNetworkConditions", { ...network, latency: latencyMs });
        if (pageName === "PRs") {
          await page.evaluate(() => {
            history.pushState(null, "", "/pulls/github/acme/widgets/1");
            window.dispatchEvent(new PopStateEvent("popstate"));
          });
          await expect(title).toHaveText("Add widget caching layer");
        }
        await header.getByRole("button", { name: "Workspaces", exact: true }).click();
        await expect(title).toHaveCount(0);
        const detailPath = `/api/v1/${selection.startsWith("issue") ? "issues" : "pulls"}/github/acme/widgets/${selection.split(":")[1]}`;
        const revalidated =
          iteration === 4
            ? page.waitForResponse(
                (response) => new URL(response.url()).pathname === detailPath && response.request().method() === "GET",
              )
            : undefined;
        const started = await page.evaluate(() => performance.now());
        if (pageName === "Activity") await header.getByRole("button", { name: pageName, exact: true }).click();
        else {
          await page.goBack();
          await page.goBack();
        }
        await expect(title).toHaveText(expectedTitle!, { timeout: 1_000 });
        if (expectedBody !== null) await expect(body).toHaveText(expectedBody, { timeout: 1_000 });
        await expect(input).toBeEditable({ timeout: 1_000 });
        await expect(input).toHaveText(draft);
        await input.press("End");
        await input.pressSequentially(String(iteration));
        draft += String(iteration);
        const painted = await page.evaluate(
          () =>
            new Promise<number>((resolve) =>
              requestAnimationFrame(() => requestAnimationFrame(() => resolve(performance.now()))),
            ),
        );
        const measurement = {
          selection,
          iteration,
          latencyMs,
          durationMs: painted - started,
          mergeVisible: await page.locator(`${scope} .btn--merge`).isVisible(),
        };
        measurements.push(measurement);
        console.log(JSON.stringify(measurement));
        if (selection === "pr:1") await expect(page.locator(`${scope} .btn--merge`)).toBeVisible({ timeout: 1_000 });
        if (revalidated) {
          // Keep one 4-second tail active until the real response arrives.
          expect((await revalidated).ok()).toBe(true);
          await (await revalidated).finished();
          const durationMs = (await page.evaluate(() => performance.now())) - started;
          measurements.push({ selection: `${selection}:tail-revalidation`, iteration, durationMs });
          console.log(JSON.stringify({ selection, tailRevalidationMs: durationMs }));
        }
        // Other iterations navigate again without waiting for HTTP revalidation.
      }
      await expect(input).toHaveText("01234567");
      // Stay after the burst to observe eventual recovery and pending requests.
      console.log(`variable-latency: settling ${selection}`);
      await page.waitForTimeout(8_000);
      await expect(title).toHaveText(expectedTitle!);
    }
  } finally {
    await mkdir(outputDir, { recursive: true });
    await writeFile(
      path.join(outputDir, "timings.json"),
      JSON.stringify({ network, latencies, browserVersion: browser.version(), measurements, requests }, null, 2) + "\n",
    );
    await server.stop();
  }
});
