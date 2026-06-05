import { expect, test } from "@playwright/test";

// Honor PLAYWRIGHT_CHROMIUM_BINARY when set so contributors on systems
// where Playwright's bundled Chromium can't launch (missing host libs)
// can point at a system Chromium. CI uses the bundled binary.
const chromiumBinary = process.env.PLAYWRIGHT_CHROMIUM_BINARY;
if (chromiumBinary) {
  test.use({ launchOptions: { executablePath: chromiumBinary } });
}

// Run sequentially: each test mutates the shared fixture body, so a
// parallel run would race on the same upstream PR state.
test.describe.serial("PR description task list", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1");
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 15_000 });
    await page.locator(".body-section .markdown-body").waitFor({ state: "visible" });
    // Give the page-load background sync time to settle so it can't
    // race with our optimistic click and clobber the local body.
    await page.waitForTimeout(1500);
  });

  test("checkbox clicks toggle locally and persist on reload", async ({ page }) => {
    const body = page.locator(".body-section .markdown-body");
    const cb0 = body.locator('input[type="checkbox"][data-task-index="0"]');
    const cb1 = body.locator('input[type="checkbox"][data-task-index="1"]');

    await expect(cb0).not.toBeChecked();
    await cb0.click();
    await expect(cb0).toBeChecked();
    await cb1.click();
    await expect(cb1).toBeChecked();

    // Allow the debounced PATCH (~400ms) to land.
    await page.waitForTimeout(900);
    await page.reload();
    const reloadedBody = page.locator(".body-section .markdown-body");
    await reloadedBody.waitFor({ state: "visible" });

    await expect(reloadedBody.locator('input[type="checkbox"][data-task-index="0"]')).toBeChecked();
    await expect(reloadedBody.locator('input[type="checkbox"][data-task-index="1"]')).toBeChecked();
  });

  test("drag handle reorders a task item and persists on reload", async ({ page }) => {
    const body = page.locator(".body-section .markdown-body");
    const firstLabel = await body.locator('.task-list-item--interactive[data-task-index="0"]').textContent();
    expect(firstLabel ?? "").toMatch(/Cmd\+K/);

    const handle0 = body.locator('.task-drag-handle[data-task-index="0"]');
    const item2 = body.locator('.task-list-item--interactive[data-task-index="2"]');
    const handleBox = await handle0.boundingBox();
    const targetBox = await item2.boundingBox();
    if (!handleBox || !targetBox) {
      throw new Error("missing bounding boxes for drag");
    }
    const startX = handleBox.x + handleBox.width / 2;
    const startY = handleBox.y + handleBox.height / 2;
    const targetX = targetBox.x + 20;
    // Below the midpoint -> "drop after" so the item lands at index 2.
    const targetY = targetBox.y + targetBox.height * 0.85;
    await page.mouse.move(startX, startY);
    await page.mouse.down();
    const steps = 8;
    for (let i = 1; i <= steps; i++) {
      await page.mouse.move(startX + ((targetX - startX) * i) / steps, startY + ((targetY - startY) * i) / steps, {
        steps: 4,
      });
    }
    await page.mouse.up();

    await page.waitForTimeout(900);
    await page.reload();
    const reloadedBody = page.locator(".body-section .markdown-body");
    await reloadedBody.waitFor({ state: "visible" });

    // The originally-first item ("Cmd+K …") now sits at index 2 after
    // the drag; the originally-second item ("Tab/Shift+Tab …") is now
    // at index 0.
    const slot0 = await reloadedBody.locator('.task-list-item--interactive[data-task-index="0"]').textContent();
    const slot2 = await reloadedBody.locator('.task-list-item--interactive[data-task-index="2"]').textContent();
    expect(slot0 ?? "").toMatch(/Tab\/Shift\+Tab/);
    expect(slot2 ?? "").toMatch(/Cmd\+K/);
  });

  test("queued body save wins when an older PATCH finishes after a newer click", async ({ page }) => {
    // Hold the first PATCH response so we can queue a newer body
    // while it's in flight. This exercises the single-flight body-
    // save queue: out-of-order responses must NOT clobber the
    // newer body, and the final persisted body must include both
    // toggles.
    const body = page.locator(".body-section .markdown-body");
    const cb0 = body.locator('input[type="checkbox"][data-task-index="0"]');
    const cb1 = body.locator('input[type="checkbox"][data-task-index="1"]');
    const cb0Initial = await cb0.isChecked();
    const cb1Initial = await cb1.isChecked();

    let patchRequests = 0;
    let releaseFirstPatch: () => void = () => undefined;
    const firstPatchHeld = new Promise<void>((resolve) => {
      releaseFirstPatch = resolve;
    });
    const patchRoute = /\/api\/v1\/pulls\/[^/]+\/[^/]+\/[^/]+\/1$/;
    await page.route(patchRoute, async (route) => {
      if (route.request().method() !== "PATCH") {
        await route.fallback();
        return;
      }
      patchRequests++;
      if (patchRequests === 1) {
        await firstPatchHeld;
      }
      await route.continue();
    });
    // Separate counter for completed responses — route.continue()
    // returns before the server replies, so request counts alone
    // can't tell us a PATCH has actually persisted.
    let patchResponses = 0;
    const onResponse = (resp: import("@playwright/test").Response) => {
      if (resp.request().method() === "PATCH" && patchRoute.test(resp.url())) {
        patchResponses++;
      }
    };
    page.on("response", onResponse);

    // Toggle the first checkbox; debounce fires and starts PATCH A
    // (which the route intercept holds).
    await cb0.click();
    await expect(cb0).toBeChecked({ checked: !cb0Initial });
    // Wait past the 400ms debounce so PATCH A has been dispatched
    // and is now blocked on firstPatchHeld.
    await expect.poll(() => patchRequests, { timeout: 3_000 }).toBe(1);

    // Toggle the second checkbox while PATCH A is in flight. Its
    // debounced save lands in the per-target queue, holding the
    // body with BOTH toggles.
    await cb1.click();
    await expect(cb1).toBeChecked({ checked: !cb1Initial });
    // Give the debounce a chance to fire and queue PATCH B. The
    // queue must hold it back until A returns.
    await page.waitForTimeout(800);
    expect(patchRequests).toBe(1);
    expect(patchResponses).toBe(0);

    // Release PATCH A. The queue then drains and fires PATCH B
    // with the latest body (both toggles). Wait for BOTH PATCH
    // responses to come back from the server before reloading,
    // otherwise the reload could race the second save and see
    // stale data.
    releaseFirstPatch();
    await expect.poll(() => patchResponses, { timeout: 5_000 }).toBe(2);
    expect(patchRequests).toBe(2);

    // Reload and confirm BOTH toggles persisted server-side. If
    // serialization regressed (PATCH A overwrote B's body via an
    // out-of-order response), the second checkbox would revert.
    page.off("response", onResponse);
    await page.unroute(patchRoute);
    await page.reload();
    const reloadedBody = page.locator(".body-section .markdown-body");
    await reloadedBody.waitFor({ state: "visible" });
    await expect(reloadedBody.locator('input[type="checkbox"][data-task-index="0"]')).toBeChecked({
      checked: !cb0Initial,
    });
    await expect(reloadedBody.locator('input[type="checkbox"][data-task-index="1"]')).toBeChecked({
      checked: !cb1Initial,
    });
  });
});
