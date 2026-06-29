import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

test.describe("CI dropdown", () => {
  test("failed CI refresh preserves stored pending status", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seedResponse = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/pending`);
      expect(seedResponse.ok()).toBe(true);

      const failResponse = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/fail-refresh`);
      expect(failResponse.ok()).toBe(true);

      const refreshResponse = await page.request.post(
        `${server.info.base_url}/api/v1/pulls/github/acme/widgets/1/ci-refresh`,
        {
          data: {},
        },
      );
      expect(refreshResponse.ok()).toBe(true);
      const refreshedDetail = (await refreshResponse.json()) as {
        merge_request: { CIStatus: string; CIChecksJSON: string };
      };
      expect(refreshedDetail.merge_request.CIStatus).toBe("pending");
      expect(refreshedDetail.merge_request.CIChecksJSON).toContain("in_progress");

      const storedResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1`);
      expect(storedResponse.ok()).toBe(true);
      const storedDetail = (await storedResponse.json()) as {
        merge_request: { CIStatus: string; CIChecksJSON: string };
      };
      expect(storedDetail.merge_request.CIStatus).toBe("pending");
      expect(storedDetail.merge_request.CIChecksJSON).toContain("in_progress");
    } finally {
      await server.stop();
    }
  });

  test("expanded pending CI checks trigger a detail sync refresh", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.addInitScript(() => {
        const realSetInterval = window.setInterval;
        window.setInterval = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) =>
          realSetInterval(handler, timeout === 15_000 ? 100 : timeout, ...args)) as typeof window.setInterval;
      });

      const seedResponse = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/pending`);
      expect(seedResponse.ok()).toBe(true);
      await expect(seedResponse.json()).resolves.toEqual({
        status: "pending",
      });

      const backgroundSync = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "POST" && url.pathname === "/api/v1/pulls/github/acme/widgets/1/sync/async"
        );
      });

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

      const detail = page.locator(".pull-detail");
      const pendingChip = detail.getByRole("button", {
        name: /CI: \d+ (passed|pending|failed|skipped) checks?/i,
      });
      await expect(pendingChip).toBeVisible();
      await backgroundSync;

      const firstRefresh = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "POST" && url.pathname === "/api/v1/pulls/github/acme/widgets/1/ci-refresh"
        );
      });
      await pendingChip.click();
      await firstRefresh;
      await expect(detail.locator(".ci-row .spin").first()).toBeVisible();
      await expect(detail.locator(".ci-row .spin svg").first()).toHaveAttribute("width", "14");

      const successResponse = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/success`);
      expect(successResponse.ok()).toBe(true);
      await expect(successResponse.json()).resolves.toEqual({
        status: "success",
      });

      await expect(page.locator(".pull-detail").getByRole("button", { name: /CI: \d+ passed checks?/i })).toBeVisible();

      const detailResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1`);
      expect(detailResponse.ok()).toBe(true);
      const storedDetail = (await detailResponse.json()) as {
        merge_request: { CIStatus: string; CIChecksJSON: string };
      };
      expect(storedDetail.merge_request.CIStatus).toBe("success");
    } finally {
      await server.stop();
    }
  });

  test("detail chips use the shared centered chip layout", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1");

    const chips = page.locator(".pull-detail .chips-row .chip");
    await expect(chips.first()).toBeVisible();

    const chipLayouts = await chips.evaluateAll((nodes) =>
      nodes.map((node) => {
        const styles = getComputedStyle(node);
        return {
          text: node.textContent?.trim() ?? "",
          minHeight: styles.minHeight,
          lineHeight: styles.lineHeight,
          paddingTop: styles.paddingTop,
          paddingBottom: styles.paddingBottom,
        };
      }),
    );

    expect(chipLayouts.length).toBeGreaterThan(0);

    for (const chip of chipLayouts) {
      expect(chip.minHeight, chip.text).toBe("22px");
      expect(chip.lineHeight, chip.text).not.toBe("normal");
      expect(chip.paddingTop, chip.text).toBe("0px");
      expect(chip.paddingBottom, chip.text).toBe("0px");
    }
  });

  test("expanded CI checks stay below chip without stretching sibling chips", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1");

    const detail = page.locator(".pull-detail");
    const chip = detail.getByRole("button", {
      name: /CI: \d+ (passed|pending|failed|skipped) checks?/i,
    });
    const diffStatsChip = detail.locator(".diff-summary-trigger");
    const labelsButton = detail.getByRole("button", {
      name: /^Labels$/,
    });
    const actionRow = detail.locator(".primary-actions-wrap");
    await chip.waitFor({ state: "visible", timeout: 10_000 });
    const chipStylesBefore = await chip.evaluate((node) => {
      const styles = getComputedStyle(node);
      return {
        backgroundColor: styles.backgroundColor,
        paddingRight: styles.paddingRight,
        lineHeight: styles.lineHeight,
      };
    });
    const chipBox = await chip.boundingBox();
    const diffStatsBox = await diffStatsChip.boundingBox();
    const labelsBox = await labelsButton.boundingBox();
    const actionRowBox = await actionRow.boundingBox();
    await chip.click();

    const checks = detail.locator(".ci-checks");
    await expect(checks).toBeVisible({ timeout: 15_000 });
    await expect(detail.locator(".ci-row")).toHaveCount(4);

    const checksBox = await checks.boundingBox();
    const expandedDiffStatsBox = await diffStatsChip.boundingBox();
    const expandedLabelsBox = await labelsButton.boundingBox();
    const expandedActionRowBox = await actionRow.boundingBox();

    expect(chipBox).not.toBeNull();
    expect(diffStatsBox).not.toBeNull();
    expect(labelsBox).not.toBeNull();
    expect(actionRowBox).not.toBeNull();
    expect(checksBox).not.toBeNull();
    expect(expandedDiffStatsBox).not.toBeNull();
    expect(expandedLabelsBox).not.toBeNull();
    expect(expandedActionRowBox).not.toBeNull();
    expect(chipStylesBefore.backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
    expect(chipStylesBefore.paddingRight).not.toBe("0px");
    expect(chipStylesBefore.lineHeight).not.toBe("normal");
    const ciGap = checksBox!.y - (chipBox!.y + chipBox!.height);
    expect(ciGap).toBeGreaterThan(0);
    expect(ciGap).toBeLessThan(11);
    expect(expandedDiffStatsBox!.height).toBeLessThan(40);
    expect(expandedDiffStatsBox!.y).toBe(diffStatsBox!.y);
    expect(expandedLabelsBox!.y).toBe(labelsBox!.y);
    expect(expandedActionRowBox!.y).toBeGreaterThan(actionRowBox!.y);

    await expect(detail.locator(".ci-name")).toHaveText(["roborev", "build", "lint", "test"]);
    await expect(detail.locator(".ci-duration")).toHaveText(["1m 30s", "45s", "2m"]);
    const roborevRow = detail.locator(".ci-row", {
      hasText: "roborev",
    });
    await expect(roborevRow).toHaveCount(1);
    expect(await roborevRow.evaluate((node) => node.tagName)).not.toBe("A");
  });

  test("mixed-state chip renders all bucket tokens", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/mixed`);
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

      const chip = page.locator(".pull-detail [data-testid='ci-chip']");
      await expect(chip.locator("[data-testid='ci-token-failed']")).toHaveText(/1/);
      await expect(chip.locator("[data-testid='ci-token-pending']")).toHaveText(/1/);
      await expect(chip.locator("[data-testid='ci-token-passed']")).toHaveText(/2/);
      await expect(chip.locator("[data-testid='ci-token-skipped']")).toHaveText(/1/);
    } finally {
      await server.stop();
    }
  });

  test("malformed CIChecksJSON renders the unavailable chip with focus-visible popover", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/malformed`);
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

      const chip = page.locator(".pull-detail [data-testid='ci-chip']");
      await expect(chip).toContainText(/CI:\s*unavailable/i);
      await expect(chip).toHaveAttribute("aria-disabled", "true");
      await expect(chip).toHaveAttribute("title", /CI unavailable:/i);

      const popover = page.locator(".pull-detail [data-testid='ci-unavailable-popover']");
      // Popover is in the DOM but hidden until the chip is focused.
      await expect(popover).toHaveCSS("visibility", "hidden");
      await chip.focus();
      await expect(chip).toBeFocused();
      await expect(popover).toHaveCSS("visibility", "visible");
      await expect(popover).toContainText(/CI unavailable:/i);
    } finally {
      await server.stop();
    }
  });

  test("CIStatus set but CIChecksJSON empty hides the chip (transient sync state)", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/status-only`);
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

      await expect(page.locator(".pull-detail [data-testid='ci-chip']")).toHaveCount(0);
    } finally {
      await server.stop();
    }
  });

  test("dropdown shows summary header, five sections, and all jobs", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      // The payload below contains pending checks, which arm the
      // 15s CI auto-refresh once the dropdown renders. A refresh
      // remounts the checks panel and silently resets the
      // show-more toggle this test exercises, so push the interval
      // out of reach (the auto-refresh behavior itself is covered
      // by "expanded pending CI checks trigger a detail sync
      // refresh" above).
      await page.addInitScript(() => {
        const realSetInterval = window.setInterval;
        window.setInterval = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) =>
          realSetInterval(handler, timeout === 15_000 ? 3_600_000 : timeout, ...args)) as typeof window.setInterval;
      });

      const seed = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/dropdown-mixed`);
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

      const chip = page.locator(".pull-detail [data-testid='ci-chip']");
      await chip.click();

      const panel = page.locator(".pull-detail .ci-checks");
      await expect(panel).toBeVisible();

      await expect(panel.locator(".ci-summary")).toContainText(/\d+ checks/);

      const headings = panel.locator(".ci-section-heading");
      await expect(headings).toHaveCount(5);
      await expect(headings.nth(0)).toContainText(/Failed \(1\)/);
      await expect(headings.nth(1)).toContainText(/Pending \(5\)/);
      await expect(headings.nth(2)).toContainText(/Unknown \(1\)/);
      await expect(headings.nth(3)).toContainText(/Passed \(12\)/);
      await expect(headings.nth(4)).toContainText(/Skipped \(2\)/);

      await expect(panel.locator(".ci-section-passed .ci-row")).toHaveCount(12);
      await expect(panel.locator(".ci-section-skipped .ci-row")).toHaveCount(2);
      await expect(panel.getByRole("button", { name: /Show \d+ more/i })).toHaveCount(0);
      await expect(panel.getByRole("button", { name: /Show fewer/i })).toHaveCount(0);
    } finally {
      await server.stop();
    }
  });
});
