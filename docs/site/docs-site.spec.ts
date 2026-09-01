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
    await page.goto("/docs/");

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
    await expect(brand).toHaveText("Kenn Forge");
    await expect(brand).toBeVisible();
    await expect(pageTitle).toBeHidden();
    expect(after.fontFamily).toBe(before.fontFamily);
    expect(after.fontWeight).toBe(before.fontWeight);
    expect(after.transform).toBe("none");
    await expect(brand).toBeInViewport();
  }
});

test("serves the canonical favicon", async ({ page }) => {
  await page.goto("/docs/");

  const faviconHref = await page.locator("link[rel~='icon']").getAttribute("href");
  const faviconURL = new URL(faviconHref ?? "", page.url());
  expect(faviconURL.pathname).toBe("/docs/assets/favicon.svg");
  const response = await page.request.get(faviconURL.toString());
  expect(response.ok()).toBe(true);
  expect(await response.text()).toContain('aria-label="kenn-forge"');
});

test("opens only the active generated workflow screenshot in a lightbox", async ({ page }) => {
  await page.goto("/docs/");

  const trigger = page.getByRole("button", {
    name: /View Forge Activity.*at full size/i,
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
  await page.goto("/docs/");

  const trigger = page.getByRole("button", {
    name: /View Forge Activity.*dark mode.*at full size/i,
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
  await page.goto("/docs/");

  await expect(page.locator("figure.workflow-shot img.workflow-shot__image--light")).toBeVisible();
  await expect(page.locator(".workflow-shot__trigger")).toHaveCount(0);
  await expect(page.locator("dialog.workflow-shot-lightbox")).toHaveCount(0);
  await context.close();
});

test("keeps generated workflow screenshots static", async ({ page }) => {
  await page.goto("/docs/assets/generated/maintainer-overview-light.svg");

  await expect
    .poll(() =>
      page.evaluate(() => document.getAnimations().filter((animation) => animation.playState === "running").length),
    )
    .toBe(0);
});

test("links to the canonical kenn-forge downloads", async ({ browser }) => {
  const linuxUserAgent =
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " + "(KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36";
  const context = await browser.newContext({ userAgent: linuxUserAgent });
  const page = await context.newPage();
  let releaseResponse: (() => void) | undefined;
  const releaseGate = new Promise<void>((resolve) => {
    releaseResponse = resolve;
  });
  await page.route("https://api.github.com/repos/kenn-io/forge/releases/latest", async (route) => {
    await releaseGate;
    await route.fulfill({
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
    });
  });

  await page.goto("/docs/");
  await expect(page.locator('a[href="https://github.com/kenn-io/forge"]').first()).toBeVisible();
  await expect(page.getByRole("link", { name: "Download latest release" })).toHaveAttribute(
    "href",
    "https://github.com/kenn-io/forge/releases",
  );

  releaseResponse?.();
  await expect(page.getByRole("link", { name: "Download for Linux x86-64" })).toHaveAttribute(
    "href",
    "https://downloads.example/forge-linux-amd64.tar.gz",
  );
  await expect(page.locator("[data-download-version]")).toHaveText("v1.2.3 · ");

  await page.goto("/docs/quickstart/");
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
  await context.close();

  const fallbackContext = await browser.newContext({ userAgent: linuxUserAgent });
  const fallbackPage = await fallbackContext.newPage();
  await fallbackPage.route("https://api.github.com/repos/kenn-io/forge/releases/latest", (route) => route.abort());
  await fallbackPage.goto("/docs/");
  await expect(fallbackPage.getByRole("link", { name: "Download latest release" })).toHaveAttribute(
    "href",
    "https://github.com/kenn-io/forge/releases",
  );
  await fallbackContext.close();
});

test("keeps the federated fleet in first-level navigation", async ({ page }) => {
  await page.goto("/docs/");

  const primaryNav = page.locator(".md-sidebar--primary nav[data-md-level='0']");
  await expect(
    primaryNav.locator(":scope > ul > li > a.md-nav__link", {
      hasText: "Federated fleet",
    }),
  ).toHaveAttribute("href", /federated-fleet\/$/);
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

    await page.goto("/docs/");
    const firstFrameScheme = await page.evaluate(() => (window as FirstFrameWindow).__firstFrameScheme);
    expect(firstFrameScheme).toBe(preference.expected);
    await context.close();
  }
});

test("keeps following system theme changes until the reader chooses", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/docs/");
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
  await page.goto("/docs/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.locator('label[title="Switch to light mode"]').click();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});

test("[webkit] scales the complete generated workflow screenshot responsively", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "webkit-iphone");

  await page.goto("/docs/");
  await page.setContent(`
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
      html, body { margin: 0; }
      .frame { width: 360px; height: 230.625px; overflow: hidden; }
      #responsive { display: block; width: 360px; height: auto; }
      #scaled-natural { display: block; width: 1280px; height: 820px; transform: scale(0.28125); transform-origin: top left; }
    </style>
    <div class="frame"><img id="responsive" src="/docs/assets/generated/maintainer-overview-light.svg" alt="Responsive rendering"></div>
    <div style="height: 20px"></div>
    <div class="frame"><img id="scaled-natural" src="/docs/assets/generated/maintainer-overview-light.svg" alt="Scaled natural rendering"></div>
  `);

  const responsive = page.locator("#responsive");
  const scaledNatural = page.locator("#scaled-natural");
  await expect(responsive).toHaveJSProperty("complete", true);
  await expect(responsive).toHaveJSProperty("naturalWidth", 1280);
  await expect(responsive).toHaveJSProperty("naturalHeight", 820);
  await expect(scaledNatural).toHaveJSProperty("complete", true);

  const [responsiveBox, scaledNaturalBox, screenshot] = await Promise.all([
    responsive.boundingBox(),
    scaledNatural.boundingBox(),
    page.screenshot(),
  ]);
  expect(responsiveBox).not.toBeNull();
  expect(scaledNaturalBox).not.toBeNull();

  const meanPixelDifference = await page.evaluate(
    async ({ screenshotURL, responsiveRect, referenceRect }) => {
      const image = new Image();
      image.src = screenshotURL;
      await image.decode();
      const canvas = document.createElement("canvas");
      canvas.width = image.naturalWidth;
      canvas.height = image.naturalHeight;
      const context = canvas.getContext("2d", { willReadFrequently: true });
      if (!context) throw new Error("2D canvas context is unavailable");
      context.drawImage(image, 0, 0);

      const scale = window.devicePixelRatio;
      let difference = 0;
      let channels = 0;
      for (let y = 0; y < responsiveRect.height; y += 2) {
        for (let x = 0; x < responsiveRect.width; x += 2) {
          const responsivePixel = context.getImageData(
            Math.round((responsiveRect.x + x) * scale),
            Math.round((responsiveRect.y + y) * scale),
            1,
            1,
          ).data;
          const referencePixel = context.getImageData(
            Math.round((referenceRect.x + x) * scale),
            Math.round((referenceRect.y + y) * scale),
            1,
            1,
          ).data;
          for (let channel = 0; channel < 3; channel++) {
            difference += Math.abs(responsivePixel[channel] - referencePixel[channel]);
            channels++;
          }
        }
      }
      return difference / channels;
    },
    {
      screenshotURL: `data:image/png;base64,${screenshot.toString("base64")}`,
      responsiveRect: responsiveBox!,
      referenceRect: scaledNaturalBox!,
    },
  );

  expect(meanPixelDifference).toBeLessThan(12);
});

test("serves the marketing pitch page at the site root", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { level: 1 })).toContainText("agent workspace");
  const hero = page.locator(".hero .shot img");
  await expect(hero).toBeVisible();
  const heroSrc = await hero.getAttribute("src");
  expect(heroSrc).toMatch(/^\/docs\/assets\/generated\//);
  const heroResponse = await page.request.get(new URL(heroSrc ?? "", page.url()).toString());
  expect(heroResponse.ok()).toBe(true);
  await expect(page.locator(".site-nav a[href='/guide/']")).toBeVisible();
  await expect(page.locator(".site-nav a[href='/docs/']")).toBeVisible();
});

test("guide stops deep-link into the operating docs", async ({ page }) => {
  await page.goto("/guide/");

  const exploreLinks = page.locator(".steps .explore a");
  await expect(exploreLinks).toHaveCount(7);
  const hrefs = await exploreLinks.evaluateAll((links) => links.map((link) => link.getAttribute("href")));
  for (const href of hrefs) {
    expect(href).toMatch(/^\/docs\/.+\/$/);
    const response = await page.request.get(new URL(href ?? "", page.url()).toString());
    expect(response.ok()).toBe(true);
  }
});

test("marketing screenshots open in a lightbox", async ({ page }) => {
  await page.goto("/");

  await page.locator(".hero .image-zoom").click();
  const dialog = page.locator("dialog.image-lightbox");
  await expect(dialog).toBeVisible();
  await expect(dialog.locator("img")).toHaveAttribute("src", /maintainer-overview-dark\.svg$/);
  await dialog.click();
  await expect(dialog).toBeHidden();
});

test("marketing download links keep the canonical releases fallback", async ({ browser }) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.route("https://api.github.com/repos/kenn-io/forge/releases/latest", (route) => route.abort());

  await page.goto("/");
  await expect(page.getByRole("link", { name: "Download latest release" })).toHaveAttribute(
    "href",
    "https://github.com/kenn-io/forge/releases",
  );
  await context.close();
});

test("cross-tier links leave the docs shell with a full navigation", async ({ page }) => {
  type MarkedWindow = Window & { __instantMarker?: boolean };

  await page.goto("/docs/");
  await page.evaluate(() => {
    (window as MarkedWindow).__instantMarker = true;
  });
  await page.locator(".md-sidebar--primary").getByRole("link", { name: "Quick start" }).click();
  await page.waitForURL(/\/docs\/quickstart\/$/);
  expect(await page.evaluate(() => (window as MarkedWindow).__instantMarker)).toBe(true);

  await page.goto("/docs/");
  await page.evaluate(() => {
    (window as MarkedWindow).__instantMarker = true;
  });
  await page.getByRole("link", { name: "Guide to Forge" }).click();
  await page.waitForURL(/\/guide\/$/);
  expect(await page.evaluate(() => (window as MarkedWindow).__instantMarker)).toBeUndefined();
  await expect(page.locator("header.site-header")).toBeVisible();
  await expect(page.locator(".md-header")).toHaveCount(0);

  await page.goto("/docs/");
  await page.locator("a.md-header__button.md-logo").click();
  await page.waitForURL("/");
  await expect(page.locator("header.site-header")).toBeVisible();
  await expect(page.locator(".md-header")).toHaveCount(0);
});

test("serves llms.txt and markdown twins for machine readers", async ({ page }) => {
  const llms = await page.request.get("/llms.txt");
  expect(llms.ok()).toBe(true);
  const llmsBody = await llms.text();
  expect(llmsBody).toContain("https://forge.kenn.io/docs/index.md");
  expect(llmsBody).toContain("https://forge.kenn.io/docs/workflows/activity.md");

  for (const twin of ["/docs/index.md", "/docs/quickstart.md", "/docs/workflows/activity.md"]) {
    const response = await page.request.get(twin);
    expect(response.ok()).toBe(true);
    expect((await response.text()).length).toBeGreaterThan(0);
  }
});
