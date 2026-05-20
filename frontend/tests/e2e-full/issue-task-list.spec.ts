import { expect, test } from "@playwright/test";

// Honor PLAYWRIGHT_CHROMIUM_BINARY when set so contributors on systems
// where Playwright's bundled Chromium can't launch (missing host libs)
// can point at a system Chromium. CI uses the bundled binary.
const chromiumBinary = process.env.PLAYWRIGHT_CHROMIUM_BINARY;
if (chromiumBinary) {
  test.use({ launchOptions: { executablePath: chromiumBinary } });
}

// Run sequentially: each test mutates the shared fixture body, so a
// parallel run would race on the same upstream issue state.
test.describe.serial("issue description task list", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/issues/github/acme/widgets/11");
    await page
      .locator(".issue-detail")
      .waitFor({ state: "visible", timeout: 15_000 });
    await page
      .locator(".body-section .markdown-body")
      .waitFor({ state: "visible" });
    // Give the page-load background sync time to settle so it can't
    // race with our optimistic click and clobber the local body.
    await page.waitForTimeout(1500);
  });

  test("checkbox clicks toggle locally and persist on reload", async ({
    page,
  }) => {
    const body = page.locator(".body-section .markdown-body");
    const cb0 = body.locator('input[type="checkbox"][data-task-index="0"]');
    const cb1 = body.locator('input[type="checkbox"][data-task-index="1"]');
    const cb0Expected = !(await cb0.isChecked());
    const cb1Expected = !(await cb1.isChecked());
    const checkboxMarker = (checked: boolean) => checked ? "[x]" : "[ ]";
    const patchRoute = /\/api\/v1\/issues\/[^/]+\/[^/]+\/[^/]+\/11$/;
    const persisted = page.waitForResponse((resp) => {
      if (
        resp.request().method() !== "PATCH"
        || !patchRoute.test(resp.url())
        || !resp.ok()
      ) {
        return false;
      }
      const body = resp.request().postData() ?? "";
      return body.includes(
        `${checkboxMarker(cb0Expected)} System preference detected on first launch`,
      )
        && body.includes(
          `${checkboxMarker(cb1Expected)} Manual toggle in settings overrides system`,
        );
    }, { timeout: 5_000 });

    await cb0.click();
    await expect(cb0).toBeChecked({ checked: cb0Expected });
    await cb1.click();
    await expect(cb1).toBeChecked({ checked: cb1Expected });

    await persisted;
    await page.reload();
    const reloadedBody = page.locator(".body-section .markdown-body");
    await reloadedBody.waitFor({ state: "visible" });

    await expect(
      reloadedBody.locator('input[type="checkbox"][data-task-index="0"]'),
    ).toBeChecked({ checked: cb0Expected });
    await expect(
      reloadedBody.locator('input[type="checkbox"][data-task-index="1"]'),
    ).toBeChecked({ checked: cb1Expected });
  });

  test("drag handle reorders a task item and persists on reload", async ({
    page,
  }) => {
    const body = page.locator(".body-section .markdown-body");
    const firstLabel = await body
      .locator('.task-list-item--interactive[data-task-index="0"]')
      .textContent();
    expect(firstLabel ?? "").toMatch(/System preference/);

    const handle0 = body.locator(
      '.task-drag-handle[data-task-index="0"]',
    );
    const item2 = body.locator(
      '.task-list-item--interactive[data-task-index="2"]',
    );
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
      await page.mouse.move(
        startX + ((targetX - startX) * i) / steps,
        startY + ((targetY - startY) * i) / steps,
        { steps: 4 },
      );
    }
    await page.mouse.up();

    await page.waitForTimeout(900);
    await page.reload();
    const reloadedBody = page.locator(".body-section .markdown-body");
    await reloadedBody.waitFor({ state: "visible" });

    // The originally-first item ("System preference …") now sits at
    // index 2 after the drag; the originally-second item ("Manual
    // toggle …") is now at index 0.
    const slot0 = await reloadedBody
      .locator('.task-list-item--interactive[data-task-index="0"]')
      .textContent();
    const slot2 = await reloadedBody
      .locator('.task-list-item--interactive[data-task-index="2"]')
      .textContent();
    expect(slot0 ?? "").toMatch(/Manual toggle/);
    expect(slot2 ?? "").toMatch(/System preference/);
  });

  test("queued body save wins when an older PATCH finishes after a newer click", async ({
    page,
  }) => {
    // Hold the first PATCH response so we can queue a newer body
    // while it's in flight. Mirrors the PR test — verifies the
    // single-flight body-save queue for the issue path.
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
    const patchRoute = /\/api\/v1\/issues\/[^/]+\/[^/]+\/[^/]+\/11$/;
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
      if (
        resp.request().method() === "PATCH"
        && patchRoute.test(resp.url())
      ) {
        patchResponses++;
      }
    };
    page.on("response", onResponse);

    await cb0.click();
    await expect(cb0).toBeChecked({ checked: !cb0Initial });
    await expect
      .poll(() => patchRequests, { timeout: 3_000 })
      .toBe(1);

    await cb1.click();
    await expect(cb1).toBeChecked({ checked: !cb1Initial });
    await page.waitForTimeout(800);
    expect(patchRequests).toBe(1);
    expect(patchResponses).toBe(0);

    // Release PATCH A. Wait for BOTH PATCH responses to come back
    // from the server before reloading, so the reload doesn't race
    // the second save.
    releaseFirstPatch();
    await expect.poll(() => patchResponses, { timeout: 5_000 }).toBe(2);
    expect(patchRequests).toBe(2);

    page.off("response", onResponse);
    await page.unroute(patchRoute);
    await page.reload();
    const reloadedBody = page.locator(".body-section .markdown-body");
    await reloadedBody.waitFor({ state: "visible" });
    await expect(
      reloadedBody.locator('input[type="checkbox"][data-task-index="0"]'),
    ).toBeChecked({ checked: !cb0Initial });
    await expect(
      reloadedBody.locator('input[type="checkbox"][data-task-index="1"]'),
    ).toBeChecked({ checked: !cb1Initial });
  });
});
