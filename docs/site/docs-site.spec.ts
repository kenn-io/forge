import { expect, test } from "@playwright/test";

type HeaderStyle = {
  fontFamily: string;
  fontWeight: string;
  transform: string;
};

type FirstFrameWindow = Window & {
  __firstFrameScheme?: Promise<string | null>;
};

test("keeps the site brand stable while scrolling", async ({ page }) => {
  for (const viewport of [
    { width: 1280, height: 800 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/");

    const topics = page.locator(".md-header__topic");
    const brand = topics.first();
    const pageTitle = topics.nth(1);
    const before = await brand.evaluate<HeaderStyle>((element) => {
      const style = getComputedStyle(element);
      return {
        fontFamily: style.fontFamily,
        fontWeight: style.fontWeight,
        transform: style.transform,
      };
    });

    await page.evaluate(() => scrollTo(0, 500));
    await page.waitForTimeout(450);

    const after = await brand.evaluate<HeaderStyle>((element) => {
      const style = getComputedStyle(element);
      return {
        fontFamily: style.fontFamily,
        fontWeight: style.fontWeight,
        transform: style.transform,
      };
    });
    await expect(brand).toHaveText("kenn-forge");
    await expect(brand).toBeVisible();
    await expect(pageTitle).toBeHidden();
    expect(after.fontFamily).toBe(before.fontFamily);
    expect(after.fontWeight).toBe(before.fontWeight);
    expect(after.transform).toBe("none");
    await expect(brand).toBeInViewport();
  }
});

test("serves the canonical favicon", async ({ page }) => {
  await page.goto("/");

  const faviconHref = await page.locator("link[rel~='icon']").getAttribute("href");
  const faviconURL = new URL(faviconHref ?? "", page.url());
  expect(faviconURL.pathname).toBe("/assets/favicon.svg");
  const response = await page.request.get(faviconURL.toString());
  expect(response.ok()).toBe(true);
  expect(await response.text()).toContain('aria-label="kenn-forge"');
});

test("opens only the active generated workflow screenshot in a lightbox", async ({ page }) => {
  await page.goto("/");

  const trigger = page.getByRole("button", {
    name: /View kenn-forge Activity.*at full size/i,
  });
  const dialog = page.getByRole("dialog", { name: "Expanded workflow screenshot" });

  await trigger.click();
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveAttribute("aria-labelledby", "workflow-shot-lightbox-title");
  await expect(dialog.locator("#workflow-shot-lightbox-title")).toHaveText("Expanded workflow screenshot");
  await expect(dialog.locator("img")).toHaveAttribute("src", /maintainer-overview-light\.svg$/);
  await dialog.getByRole("button", { name: "Close expanded screenshot" }).click();
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await dialog.click({ position: { x: 2, y: 2 } });
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});

test("opens the dark generated workflow screenshot for the active theme", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");

  const trigger = page.getByRole("button", {
    name: /View kenn-forge Activity.*dark mode.*at full size/i,
  });
  await trigger.click();

  const dialog = page.getByRole("dialog", { name: "Expanded workflow screenshot" });
  await expect(dialog.locator("img")).toHaveAttribute("src", /maintainer-overview-dark\.svg$/);
  await context.close();
});

test("keeps generated workflow screenshots inline without native dialog support", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "light" });
  await context.addInitScript(() => {
    Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
      configurable: true,
      value: undefined,
    });
  });
  const page = await context.newPage();
  await page.goto("/");

  await expect(page.locator("figure.workflow-shot img.workflow-shot__image--light")).toBeVisible();
  await expect(page.locator(".workflow-shot__trigger")).toHaveCount(0);
  await expect(page.locator("dialog.workflow-shot-lightbox")).toHaveCount(0);
  await context.close();
});

test("keeps generated workflow screenshots static", async ({ page }) => {
  await page.goto("/assets/generated/maintainer-overview-light.svg");

  await expect
    .poll(() => page.evaluate(() => document.getAnimations().filter((animation) => animation.playState === "running").length))
    .toBe(0);
});

test("links to the canonical kenn-forge downloads", async ({ page }) => {
  await page.route("https://api.github.com/repos/kenn-io/forge/releases/latest", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        tag_name: "v1.2.3",
        assets: [
          {
            name: "forge_1.2.3_linux_amd64.tar.gz",
            browser_download_url: "https://downloads.example/forge-linux-amd64.tar.gz",
          },
          {
            name: "forge_1.2.3_darwin_arm64.tar.gz",
            browser_download_url: "https://downloads.example/forge-darwin-arm64.tar.gz",
          },
          {
            name: "SHA256SUMS",
            browser_download_url: "https://downloads.example/SHA256SUMS",
          },
        ],
      }),
    }),
  );

  await page.goto("/");
  await expect(page.locator('a[href="https://github.com/kenn-io/forge"]').first()).toBeVisible();
  await expect(page.getByRole("link", { name: "Download latest release" })).toHaveAttribute(
    "href",
    "https://github.com/kenn-io/forge/releases",
  );
  await expect(page.locator("[data-download-version]")).toHaveText("v1.2.3 · ");

  await page.goto("/quickstart/");
  await expect(page.getByRole("link", { name: "forge_<version>_linux_amd64.tar.gz" })).toHaveAttribute(
    "href",
    "https://downloads.example/forge-linux-amd64.tar.gz",
  );
  await expect(page.getByRole("link", { name: "SHA256SUMS" })).toHaveAttribute(
    "href",
    "https://downloads.example/SHA256SUMS",
  );
  await expect(page.getByRole("link", { name: "GitHub Releases" })).toHaveAttribute(
    "href",
    "https://github.com/kenn-io/forge/releases",
  );
});

test("places Fleet under advanced and experimental navigation", async ({ page }) => {
  await page.goto("/");

  const primaryNav = page.locator(".md-sidebar--primary nav[data-md-level='0']");
  const advancedLabel = primaryNav.locator("label.md-nav__link", {
    hasText: "Advanced / experimental",
  });
  await expect(advancedLabel).toBeVisible();

  const advancedItem = advancedLabel.locator("xpath=ancestor::li[1]");
  await expect(advancedItem.getByRole("link", { name: "Fleet" })).toHaveAttribute("href", /federated-fleet\/$/);
  await expect(primaryNav.locator(":scope > ul > li > a.md-nav__link", { hasText: "Fleet" })).toHaveCount(0);
});

test("applies the browser theme before the runtime bundle", async ({ browser }) => {
  for (const preference of [
    { colorScheme: "light" as const, expected: "default" },
    { colorScheme: "dark" as const, expected: "slate" },
  ]) {
    const context = await browser.newContext({ colorScheme: preference.colorScheme });
    await context.addInitScript(() => {
      const target = window as FirstFrameWindow;
      target.__firstFrameScheme = new Promise((resolve) => {
        requestAnimationFrame(() => {
          resolve(document.body?.getAttribute("data-md-color-scheme") ?? null);
        });
      });
    });
    const page = await context.newPage();
    await page.route("**/assets/javascripts/bundle.*.min.js", (route) => route.abort());

    await page.goto("/");
    const firstFrameScheme = await page.evaluate(() => (window as FirstFrameWindow).__firstFrameScheme);
    expect(firstFrameScheme).toBe(preference.expected);
    await context.close();
  }
});

test("keeps following system theme changes until the reader chooses", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});

test("persists an explicit light override on a dark system", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.locator('label[title="Switch to light mode"]').click();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});
