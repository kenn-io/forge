import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// App-shell smoke + live-header theme-toggle wiring.
//
// Both facts used to live in tests/e2e/theme-toggle.spec.ts, which was removed
// when the toggle's detailed render assertions moved to the browser tier
// (src/lib/components/layout/ThemeToggle.browser.svelte.ts). That browser test
// mounts ThemeToggle in isolation, so it cannot prove that the real /pulls route
// renders mocked data and counts, that Issues navigation works, or that
// AppHeader actually mounts the toggle and the click path reaches the theme
// store in the shipped shell. That route-level coverage is restored here; the
// computed-style / icon-fill / icon-swap assertions stay in the browser tier.

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("renders mocked frontend data", async ({ page }) => {
  await page.goto("/pulls");

  await expect(page.getByText("Add browser regression coverage")).toBeVisible();
  await expect(
    page
      .getByRole("region", { name: "Pull requests" })
      .getByRole("button", { name: /^github\.com\/acme\/widgets \d+$/ })
      .first(),
  ).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("3 PRs")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("2 repos")).toBeVisible();

  await page.getByRole("button", { name: "Issues" }).click();

  await expect(page.getByText("Theme toggle does not stick")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("1 issues")).toBeVisible();
});

test("header theme toggle flips dark mode in the live shell", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");

  const root = page.locator("html");
  const button = page.getByTitle("Toggle theme");

  // Detailed computed-style / icon-fill / icon-swap assertions live in the
  // browser-tier ThemeToggle test. Here we only verify the integration it
  // cannot: AppHeader mounts the real toggle, and clicking it flips html.dark
  // through the shipped shell.
  await expect(button).toBeVisible();
  await expect(root).not.toHaveClass(/dark/);

  await button.click();
  await expect(root).toHaveClass(/dark/);

  await button.click();
  await expect(root).not.toHaveClass(/dark/);
});

test("loads bundled Inter and JetBrains Mono fonts across themes", async ({ page }) => {
  await page.goto("/pulls");
  await expect(page.getByText("Add browser regression coverage")).toBeVisible();

  const fontState = await page.evaluate(async () => {
    await document.fonts.ready;
    const bodyFont = getComputedStyle(document.body).fontFamily;
    const monoToken = getComputedStyle(document.documentElement).getPropertyValue("--font-mono").trim();
    const loadedFamilies = [...document.fonts]
      .filter((face) => face.status === "loaded")
      .map((face) => face.family.replace(/^["']|["']$/g, ""));
    return {
      theme: document.documentElement.dataset.kitTheme ?? "",
      bodyFont,
      monoToken,
      loadedFamilies: [...new Set(loadedFamilies)],
    };
  });

  expect(fontState.theme).toBe("quiet");
  expect(fontState.bodyFont).toMatch(/^"?Inter"?\b/);
  expect(fontState.monoToken).toMatch(/^"?JetBrains Mono"?\b/);
  expect(fontState.loadedFamilies).toContain("Inter");
  expect(fontState.loadedFamilies).toContain("JetBrains Mono");
});

test("holds first paint until bundled fonts decode", async ({ page }) => {
  const startTime = new Date("2026-11-20T12:00:00Z");
  await page.clock.install({ time: startTime });
  await page.clock.pauseAt(startTime);
  await page.addInitScript(() => {
    const pending: string[] = [];
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const originalLoad = Object.getOwnPropertyDescriptor(FontFace.prototype, "load")?.value as FontFace["load"];
    FontFace.prototype.load = function (this: FontFace) {
      const family = this.family.replace(/^["']|["']$/g, "");
      if (family === "Inter" || family === "JetBrains Mono") {
        pending.push(family);
        return gate.then(() => originalLoad.call(this));
      }
      return originalLoad.call(this);
    };
    Object.assign(window, {
      __pendingFontLoads: pending,
      __releaseFontLoads: release,
    });
  });

  await page.goto("/pulls", { waitUntil: "domcontentloaded" });

  await expect
    .poll(async () =>
      page.evaluate(() => [...new Set((window as unknown as { __pendingFontLoads: string[] }).__pendingFontLoads)]),
    )
    .toEqual(expect.arrayContaining(["Inter", "JetBrains Mono"]));
  // Advance part of the hold window while staying short of the 3000ms fallback
  // timer; the page must remain hidden until both bundled fonts finish loading.
  await page.clock.runFor(1_000);
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).opacity)).toBe("0");

  await page.evaluate(() => {
    (window as unknown as { __releaseFontLoads: () => void }).__releaseFontLoads();
  });

  // Virtual time stays paused at 1000ms (< 3000ms), so revealing here proves
  // font decoding, not the fallback timer, made the page visible.
  await expect.poll(async () => page.evaluate(() => getComputedStyle(document.documentElement).opacity)).toBe("1");
  expect(await page.evaluate(() => Date.now())).toBe(startTime.getTime() + 1_000);
  const loadedFamilies = await page.evaluate(async () => {
    await document.fonts.ready;
    return [
      ...new Set(
        [...document.fonts]
          .filter((face) => face.status === "loaded")
          .map((face) => face.family.replace(/^["']|["']$/g, "")),
      ),
    ];
  });
  expect(loadedFamilies).toEqual(expect.arrayContaining(["Inter", "JetBrains Mono"]));

  await page.clock.resume();
  await expect(page.getByText("Add browser regression coverage")).toBeVisible();
});
