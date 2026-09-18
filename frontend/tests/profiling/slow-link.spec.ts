import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { expect, request, test, type Page } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "../e2e-full/support/e2eServer";

const profile = {
  latency: 700,
  downloadThroughput: 125_000,
  uploadThroughput: 125_000,
  offline: false,
};
const iterations = 3;
const idleMs = 60_000;
const outageMs = 3_000;
type Measurement = { scenario: string; iteration: number; durationMs: number };

function median(values: number[]): number {
  const sorted = [...values].sort((left, right) => left - right);
  return (sorted[Math.floor((sorted.length - 1) / 2)]! + sorted[Math.floor(sorted.length / 2)]!) / 2;
}

async function switchRoute(page: Page, route: string): Promise<number> {
  return page.evaluate((next) => {
    const started = performance.now();
    history.pushState(null, "", next);
    window.dispatchEvent(new PopStateEvent("popstate"));
    return started;
  }, route);
}

async function waitForDetail(page: Page, title: string): Promise<number> {
  const visible = await page.waitForFunction(
    (expected) => {
      const heading = [...document.querySelectorAll<HTMLElement>(".detail-title")].find(
        (node) => node.textContent?.trim() === expected && node.getClientRects().length > 0,
      );
      return heading ? performance.now() : false;
    },
    title,
    { timeout: 60_000 },
  );
  return (await visible.jsonValue()) as number;
}

test("records slow-link navigation and traffic", async ({ page, browser, context }) => {
  test.setTimeout(600_000);
  const outputDir =
    process.env.KENN_FORGE_PROFILE_OUT_DIR ??
    path.resolve("test-results/slow-link-profile", new Date().toISOString().replace(/[:.]/g, "-"));
  await mkdir(outputDir, { recursive: true });
  const server = await startIsolatedE2EServerWithOptions({ freshProcess: true });
  const api = await request.newContext({ baseURL: server.info.base_url });
  const measurements: Measurement[] = [];
  const traffic = { httpEncodedBodyBytes: 0, requests: 0, failedRequests: 0 };
  const httpRequests: Array<{ url: string; method: string }> = [];
  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  cdp.on("Network.dataReceived", (event) => {
    traffic.httpEncodedBodyBytes += event.encodedDataLength;
  });
  cdp.on("Network.requestWillBeSent", ({ request: sent }) => {
    traffic.requests++;
    httpRequests.push({ url: new URL(sent.url).pathname, method: sent.method });
  });
  cdp.on("Network.loadingFailed", () => {
    traffic.failedRequests++;
  });
  let idle: Record<string, number> | null = null;
  let coldTraffic: typeof traffic | null = null;
  let completed = false;

  try {
    const routes = [
      "/pulls/github/acme/widgets/1",
      "/pulls/github/acme/widgets/5",
      "/issues/github/acme/widgets/10",
      "/issues/github/acme/widgets/11",
      "/issues/github/acme/widgets/12",
    ];
    const titles = new Map<string, string>();
    for (const route of routes) {
      const response = await api.get(`/api/v1${route}`);
      expect(response.ok()).toBe(true);
      const detail = (await response.json()) as { merge_request?: { Title: string }; issue?: { Title: string } };
      titles.set(route, (detail.merge_request ?? detail.issue)!.Title);
    }

    console.log("slow-link: fixtures ready");
    await cdp.send("Network.emulateNetworkConditions", profile);
    await page.goto(`${server.info.base_url}/pulls`, { waitUntil: "domcontentloaded" });
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 120_000 });
    measurements.push({
      scenario: "cold-pull-list",
      iteration: 0,
      durationMs: await page.evaluate(() => performance.now()),
    });
    coldTraffic = { ...traffic };
    console.log("slow-link: cold list measured");

    // Await each HTTP revalidation after measuring first visible detail, so
    // later iterations start with a settled response and a primed snapshot.
    for (let iteration = 0; iteration <= iterations; iteration++) {
      for (const route of routes.slice(0, 4)) {
        const response = page.waitForResponse(
          (candidate) =>
            new URL(candidate.url()).pathname === `/api/v1${route}` && candidate.request().method() === "GET",
        );
        const started = await switchRoute(page, route);
        const visible = await waitForDetail(page, titles.get(route)!);
        measurements.push({
          scenario: `${iteration === 0 ? "first" : "repeat"}-${route.startsWith("/pulls") ? "pull" : "issue"}-detail`,
          iteration,
          durationMs: visible - started,
        });
        const refreshed = await response;
        expect(refreshed.ok()).toBe(true);
        await refreshed.finished();
      }
    }

    const comment = "Synthetic slow-link benchmark comment";
    const editor = page.locator(".issue-detail .comment-editor-shell").first();
    await editor.locator(".comment-editor-input").fill(comment);
    const acknowledgement = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/api/v1/issues/github/acme/widgets/11/comments",
    );
    const actionStarted = performance.now();
    await editor.getByRole("button", { name: "Comment", exact: true }).click();
    expect((await acknowledgement).ok()).toBe(true);
    measurements.push({
      scenario: "comment-server-acknowledgement",
      iteration: 0,
      durationMs: performance.now() - actionStarted,
    });
    await expect(page.locator(".issue-detail .event-body").filter({ hasText: comment }).first()).toBeVisible();
    measurements.push({ scenario: "comment-visible", iteration: 0, durationMs: performance.now() - actionStarted });
    console.log("slow-link: detail visits and comment measured; starting idle window");

    await switchRoute(page, "/pulls");
    await page.locator(".pull-item").first().waitFor({ state: "visible" });
    await page.waitForTimeout(2_000);
    const beforeIdle = { ...traffic };
    const idleStarted = performance.now();
    await page.waitForTimeout(idleMs);
    const idleDuration = performance.now() - idleStarted;
    idle = {
      durationMs: idleDuration,
      httpEncodedBodyBytesPerMinute:
        ((traffic.httpEncodedBodyBytes - beforeIdle.httpEncodedBodyBytes) * 60_000) / idleDuration,
      requestsPerMinute: ((traffic.requests - beforeIdle.requests) * 60_000) / idleDuration,
    };
    console.log("slow-link: idle measured");

    const offlineFailure = page.waitForEvent("requestfailed", {
      predicate: (failed) => new URL(failed.url()).pathname === `/api/v1${routes[4]}`,
      timeout: 10_000,
    });
    await cdp.send("Network.emulateNetworkConditions", { ...profile, offline: true });
    await switchRoute(page, routes[4]!);
    expect((await offlineFailure).failure()?.errorText).toContain("ERR_INTERNET_DISCONNECTED");
    await page.waitForTimeout(outageMs);
    await cdp.send("Network.emulateNetworkConditions", profile);
    const recoveredAt = performance.now();
    // Measure an explicit retry by leaving and revisiting the failed route.
    await switchRoute(page, "/issues");
    await page.locator(".issue-item").first().waitFor({ state: "visible" });
    await switchRoute(page, routes[4]!);
    await waitForDetail(page, titles.get(routes[4]!)!);
    measurements.push({
      scenario: "outage-explicit-revisit-recovery",
      iteration: 0,
      durationMs: performance.now() - recoveredAt,
    });
    completed = true;
  } finally {
    try {
      const result = {
        completed,
        capturedAt: new Date().toISOString(),
        environment: {
          commit:
            process.env.KENN_FORGE_PROFILE_LABEL ??
            execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
          browserVersion: browser.version(),
          platform: `${process.platform}/${process.arch}`,
        },
        networkProfile: { ...profile, throughputUnit: "bytes/second", latencyUnit: "milliseconds", outageMs },
        iterations,
        measurements,
        coldTraffic,
        idle,
        traffic,
        httpRequests,
      };
      await writeFile(path.join(outputDir, "timings.json"), JSON.stringify(result, null, 2) + "\n");
      const grouped = Map.groupBy(measurements, (measurement) => measurement.scenario);
      const summary = [...grouped].map(([scenario, values]) => {
        return `${scenario}: median ${median(values.map((value) => value.durationMs)).toFixed(1)} ms (${values.length} samples)`;
      });
      summary.push(`idle: ${JSON.stringify(idle)}`, `cold traffic: ${JSON.stringify(coldTraffic)}`);
      if (process.env.KENN_FORGE_PROFILE_BASELINE) {
        const baseline = JSON.parse(await readFile(process.env.KENN_FORGE_PROFILE_BASELINE, "utf8")) as {
          measurements: Measurement[];
        };
        for (const [scenario, values] of grouped) {
          const previous = baseline.measurements
            .filter((measurement) => measurement.scenario === scenario)
            .map((value) => value.durationMs);
          if (previous.length === 0) continue;
          const current = values.map((value) => value.durationMs);
          summary.push(`${scenario} median change: ${(median(current) - median(previous)).toFixed(1)} ms`);
        }
      }
      await writeFile(path.join(outputDir, "summary.txt"), summary.join("\n") + "\n");
      console.log(`\n${summary.join("\n")}\nartifacts: ${outputDir}`);
    } finally {
      await api.dispose();
      await server.stop();
    }
  }
});
