import { devices, expect, test, type Page } from "@playwright/test";

const iPhone13 = devices["iPhone 13"];
test.use({
  viewport: iPhone13.viewport,
  deviceScaleFactor: iPhone13.deviceScaleFactor,
  userAgent: iPhone13.userAgent,
});

async function expectPathname(page: Page, pathname: string): Promise<void> {
  await expect.poll(() => new URL(page.url()).pathname).toBe(pathname);
}

async function expectReadableFocusList(page: Page, itemSelector: string): Promise<void> {
  await expect(page.locator(itemSelector).first()).toBeVisible();

  const metrics = await page.evaluate((selector) => {
    const fontSize = (node: Element | null): number => (node ? Number.parseFloat(getComputedStyle(node).fontSize) : 0);
    const rect = (node: Element | null): DOMRect | null => node?.getBoundingClientRect() ?? null;
    const compactRect = (node: Element | null) => {
      const r = rect(node);
      return r ? { left: r.left, right: r.right, height: r.height } : null;
    };
    const item = document.querySelector(selector);
    const title = item?.querySelector(".title") ?? null;
    const meta = item?.querySelector(".meta-left") ?? null;
    const search = document.querySelector(".focus-list .search-input");
    const stateButton = document.querySelector(".focus-list .state-btn");
    const focusList = document.querySelector(".focus-list");
    const tokenValue = (node: Element | null, name: string): string =>
      node ? getComputedStyle(node).getPropertyValue(name).trim() : "";

    return {
      viewportWidth: window.innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      mobileTypeToken: tokenValue(focusList, "--mobile-type-body"),
      focusHitTarget: tokenValue(focusList, "--focus-mobile-hit-target"),
      searchFontSize: fontSize(search),
      stateButtonFontSize: fontSize(stateButton),
      stateButtonRect: compactRect(stateButton),
      itemFontSize: fontSize(item),
      itemRect: compactRect(item),
      titleFontSize: fontSize(title),
      metaFontSize: fontSize(meta),
      itemBounds: [...document.querySelectorAll(selector)].slice(0, 6).map(compactRect),
    };
  }, itemSelector);

  expect(metrics.mobileTypeToken).toBe("1.24rem");
  expect(metrics.focusHitTarget).toMatch(/rem$/);
  expect(metrics.documentWidth).toBeLessThanOrEqual(metrics.viewportWidth);
  expect(metrics.searchFontSize).toBeGreaterThanOrEqual(16);
  expect(metrics.stateButtonFontSize).toBeGreaterThanOrEqual(15);
  expect(metrics.stateButtonRect?.height ?? 0).toBeGreaterThanOrEqual(44);
  expect(metrics.itemFontSize).toBeGreaterThanOrEqual(16);
  expect(metrics.itemRect?.height ?? 0).toBeGreaterThanOrEqual(72);
  expect(metrics.titleFontSize).toBeGreaterThanOrEqual(19);
  expect(metrics.metaFontSize).toBeGreaterThanOrEqual(15);
  for (const bounds of metrics.itemBounds) {
    expect(bounds?.left ?? 0).toBeGreaterThanOrEqual(0);
    expect(bounds?.right ?? 0).toBeLessThanOrEqual(metrics.viewportWidth);
  }
}

async function expectReadableDetail(page: Page): Promise<void> {
  await expect(
    page.locator(".focus-layout .pull-detail .detail-title, .focus-layout .issue-detail .detail-title"),
  ).toBeVisible();

  const metrics = await page.evaluate(() => {
    const detail = document.querySelector(".pull-detail, .issue-detail");
    const layout = document.querySelector(".focus-layout");
    const fontSize = (selector: string): number => {
      const node = document.querySelector(selector);
      return node ? Number.parseFloat(getComputedStyle(node).fontSize) : 0;
    };
    const rect = (selector: string) => {
      const r = document.querySelector(selector)?.getBoundingClientRect();
      return r ? { left: r.left, right: r.right, height: r.height } : null;
    };
    const tokenValue = (node: Element | null, name: string): string =>
      node ? getComputedStyle(node).getPropertyValue(name).trim() : "";
    const overflowingVisible = [...document.querySelectorAll(".focus-layout *")]
      .filter((el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && r.left < window.innerWidth && r.right > window.innerWidth + 0.5;
      })
      .map((el) => el.className?.toString() || el.tagName.toLowerCase());

    return {
      viewportWidth: window.innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      detailTypeToken: tokenValue(detail, "--detail-mobile-type-body"),
      detailHitTarget: tokenValue(detail, "--detail-mobile-hit-target"),
      mobileTypeToken: tokenValue(layout, "--mobile-type-body"),
      rootFontSize: Number.parseFloat(getComputedStyle(document.documentElement).fontSize),
      titleFontSize: fontSize(".detail-title"),
      metaFontSize: fontSize(".meta-item"),
      bodyFontSize: fontSize(".pull-detail, .issue-detail"),
      chipFontSize: fontSize(".chip, .state-chip, .status-chip"),
      copyNumberFontSize: fontSize(".copy-number-btn"),
      copyNumberRect: rect(".copy-number-btn"),
      overflowingVisible,
    };
  });

  expect(metrics.detailTypeToken).not.toBe("");
  expect(metrics.detailHitTarget).toMatch(/rem$/);
  expect(metrics.mobileTypeToken).toBe("1.24rem");
  expect(metrics.rootFontSize).toBe(13);
  expect(metrics.documentWidth).toBeLessThanOrEqual(metrics.viewportWidth);
  expect(metrics.titleFontSize).toBeGreaterThanOrEqual(19);
  expect(metrics.metaFontSize).toBeGreaterThanOrEqual(15);
  expect(metrics.bodyFontSize).toBeGreaterThanOrEqual(16);
  expect(metrics.chipFontSize).toBeGreaterThanOrEqual(14);
  expect(metrics.copyNumberFontSize).toBeGreaterThanOrEqual(15);
  expect(metrics.copyNumberRect?.height ?? 0).toBeGreaterThanOrEqual(44);
  expect(metrics.overflowingVisible).toEqual([]);
}

test.describe("phone routes", () => {
  test("phone viewport visiting desktop root renders mobile activity without changing URL", async ({ page }) => {
    await page.goto("/");

    await expectPathname(page, "/");
    await expect(page.locator(".mobile-shell")).toBeVisible();
    await expect(page.locator(".mobile-tab--active")).toHaveText("Activity");
    await expect(page.locator(".mobile-topbar .mobile-app-icon")).toBeVisible();
    await expect(page.getByRole("button", { name: "Open desktop view" })).toBeVisible();
    await expect(page.locator(".app-header")).toHaveCount(0);
    await expect(page.locator("footer")).toHaveCount(0);

    const metrics = await page.evaluate(() => {
      const search = document.querySelector(".search-input");
      const rect = search?.getBoundingClientRect();
      return {
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        searchLeft: rect?.left ?? 0,
        searchRight: rect?.right ?? 0,
      };
    });

    expect(metrics.documentWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.searchLeft).toBeGreaterThanOrEqual(0);
    expect(metrics.searchRight).toBeLessThanOrEqual(metrics.viewportWidth);
  });

  test("mobile activity uses a phone-first inbox rather than the desktop threaded list", async ({ page }) => {
    await page.goto("/m?range=30d&view=threaded");

    await expect(page.locator(".mobile-shell")).toBeVisible();
    await expect(page.locator(".mobile-activity-inbox")).toBeVisible();
    await expect(page.getByRole("heading", { name: "What needs attention?" })).toBeVisible();
    await expect(page.getByText("Readable threads first")).toHaveCount(0);
    await expect(page.getByLabel("Activity type")).toBeVisible();
    await expect(page.getByLabel("Time range")).toBeVisible();
    await expect(page.getByLabel("Repository")).toBeVisible();
    await expect(page.locator(".threaded-view")).toHaveCount(0);

    const metrics = await page.evaluate(() => {
      const firstCard = document.querySelector(".mobile-activity-card");
      const firstButton = document.querySelector(".mobile-activity-card button");
      const title = document.querySelector(".mobile-activity-card__title");
      const meta = document.querySelector(".mobile-activity-card__meta");
      const metaItems = meta?.querySelectorAll("span") ?? [];
      const metaRect = meta?.getBoundingClientRect();
      const metaCountRect = metaItems[1]?.getBoundingClientRect();
      const eventLabel = document.querySelector(".mobile-activity-event__body strong");
      const eventAuthor = document.querySelector(".mobile-activity-event__body span");
      const eventTime = document.querySelector(".mobile-activity-event time");
      const mobileTitle = document.querySelector(".mobile-title");
      const mobileTab = document.querySelector(".mobile-tabs a");
      const desktopButton = document.querySelector(".mobile-desktop-link");
      const desktopIcon = document.querySelector(".mobile-desktop-link svg");
      const appIcon = document.querySelector(".mobile-app-icon");
      const typeSelect = document.querySelector(".mobile-filter-dropdown button[aria-label^='Activity type']");
      const rangeSelect = document.querySelector(".mobile-filter-dropdown button[aria-label^='Time range']");
      const repoSelect = document.querySelector(".mobile-filter-dropdown button[aria-label^='Repository']");
      const search = document.querySelector(".search-input");
      const cardRect = firstCard?.getBoundingClientRect();
      const buttonRect = firstButton?.getBoundingClientRect();
      const searchRect = search?.getBoundingClientRect();
      const styleFor = (node: Element | null) => (node ? getComputedStyle(node) : null);
      const themeSample = document.createElement("div");
      themeSample.style.cssText = [
        "position:absolute",
        "left:-9999px",
        "top:0",
        "width:1px",
        "height:1px",
        "background:var(--bg-primary)",
        "border-color:var(--border-default)",
      ].join(";");
      document.body.append(themeSample);
      const surfaceSample = document.createElement("div");
      surfaceSample.style.cssText = [
        "position:absolute",
        "left:-9999px",
        "top:0",
        "width:1px",
        "height:1px",
        "background:var(--bg-surface)",
        "border-radius:var(--radius-lg)",
      ].join(";");
      document.body.append(surfaceSample);
      const insetSample = document.createElement("div");
      insetSample.style.cssText = [
        "position:absolute",
        "left:-9999px",
        "top:0",
        "width:1px",
        "height:1px",
        "background:var(--bg-inset)",
      ].join(";");
      document.body.append(insetSample);
      const compactRect = (node: Element | null) => {
        const r = node?.getBoundingClientRect();
        return r ? { left: r.left, right: r.right, height: r.height } : null;
      };
      const fontSize = (node: Element | null): number =>
        node ? Number.parseFloat(getComputedStyle(node).fontSize) : 0;
      return {
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        mobileTypeToken: getComputedStyle(document.querySelector(".mobile-shell") ?? document.documentElement)
          .getPropertyValue("--mobile-type-body")
          .trim(),
        titleTypeToken: getComputedStyle(document.querySelector(".mobile-shell") ?? document.documentElement)
          .getPropertyValue("--mobile-type-title")
          .trim(),
        rootFontSize: Number.parseFloat(getComputedStyle(document.documentElement).fontSize),
        cardHeight: cardRect?.height ?? 0,
        touchTargetHeight: buttonRect?.height ?? 0,
        titleFontSize: fontSize(title),
        metaFontSize: fontSize(meta),
        metaItemCount: metaItems.length,
        metaRight: metaRect?.right ?? 0,
        metaCountRight: metaCountRect?.right ?? 0,
        eventLabelFontSize: fontSize(eventLabel),
        eventAuthorFontSize: fontSize(eventAuthor),
        eventTimeFontSize: fontSize(eventTime),
        mobileTitleFontSize: fontSize(mobileTitle),
        mobileTabFontSize: fontSize(mobileTab),
        desktopButtonText: desktopButton?.textContent?.trim() ?? "",
        desktopButtonRect: compactRect(desktopButton),
        desktopIconPresent: Boolean(desktopIcon),
        appIconPresent: Boolean(appIcon),
        inboxBackground: styleFor(document.querySelector(".mobile-activity-inbox"))?.backgroundColor ?? "",
        cardBackground: styleFor(firstCard)?.backgroundColor ?? "",
        cardBorderColor: styleFor(firstCard)?.borderColor ?? "",
        cardRadius: styleFor(firstCard)?.borderRadius ?? "",
        searchBackground: styleFor(document.querySelector(".mobile-activity-search"))?.backgroundColor ?? "",
        themeBgPrimary: getComputedStyle(themeSample).backgroundColor,
        themeBgSurface: getComputedStyle(surfaceSample).backgroundColor,
        themeBgInset: getComputedStyle(insetSample).backgroundColor,
        themeBorder: getComputedStyle(themeSample).borderColor,
        themeRadiusLg: getComputedStyle(surfaceSample).borderRadius,
        typeSelectFontSize: fontSize(typeSelect),
        rangeSelectFontSize: fontSize(rangeSelect),
        repoSelectFontSize: fontSize(repoSelect),
        typeSelectRect: compactRect(typeSelect),
        rangeSelectRect: compactRect(rangeSelect),
        repoSelectRect: compactRect(repoSelect),
        searchLeft: searchRect?.left ?? 0,
        searchRight: searchRect?.right ?? 0,
      };
    });

    expect(metrics.documentWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.searchLeft).toBeGreaterThanOrEqual(0);
    expect(metrics.searchRight).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.mobileTypeToken).toBe("1.24rem");
    expect(metrics.titleTypeToken).toBe("1.54rem");
    expect(metrics.rootFontSize).toBe(13);
    expect(metrics.cardHeight).toBeGreaterThanOrEqual(110);
    expect(metrics.touchTargetHeight).toBeGreaterThanOrEqual(44);
    expect(metrics.titleFontSize).toBeGreaterThanOrEqual(19);
    expect(metrics.metaFontSize).toBeGreaterThanOrEqual(15);
    expect(metrics.metaItemCount).toBe(2);
    expect(Math.abs(metrics.metaRight - metrics.metaCountRight)).toBeLessThanOrEqual(1);
    expect(metrics.eventLabelFontSize).toBeGreaterThanOrEqual(15);
    expect(metrics.eventAuthorFontSize).toBeGreaterThanOrEqual(14);
    expect(metrics.eventTimeFontSize).toBeGreaterThanOrEqual(14);
    expect(metrics.mobileTitleFontSize).toBeGreaterThanOrEqual(16);
    expect(metrics.mobileTabFontSize).toBeGreaterThanOrEqual(16);
    expect(metrics.desktopButtonText).toBe("");
    expect(metrics.desktopButtonRect?.height ?? 0).toBeGreaterThanOrEqual(44);
    expect(metrics.desktopIconPresent).toBe(true);
    expect(metrics.appIconPresent).toBe(true);
    expect(metrics.inboxBackground).toBe(metrics.themeBgPrimary);
    expect(metrics.cardBackground).toBe(metrics.themeBgSurface);
    expect(metrics.cardBorderColor).toBe(metrics.themeBorder);
    expect(metrics.cardRadius).toBe(metrics.themeRadiusLg);
    expect(metrics.searchBackground).toBe(metrics.themeBgInset);
    expect(metrics.typeSelectFontSize).toBeGreaterThanOrEqual(15);
    expect(metrics.rangeSelectFontSize).toBeGreaterThanOrEqual(15);
    expect(metrics.repoSelectFontSize).toBeGreaterThanOrEqual(15);
    for (const bounds of [metrics.typeSelectRect, metrics.rangeSelectRect, metrics.repoSelectRect]) {
      expect(bounds?.left ?? 0).toBeGreaterThanOrEqual(0);
      expect(bounds?.right ?? 0).toBeLessThanOrEqual(metrics.viewportWidth);
    }
  });

  test("mobile activity filters can narrow by type, range, and repository", async ({ page }) => {
    await page.goto("/m?range=30d&view=threaded");
    await expect(page.locator(".mobile-activity-inbox")).toBeVisible();

    await page.getByRole("combobox", { name: /Activity type/ }).click();
    await page.getByRole("option", { name: "PRs" }).click();
    await expect(page.getByRole("combobox", { name: "Activity type: PRs" })).toBeVisible();
    await expect(page).toHaveURL(/types=new_pr/);

    await page.getByRole("combobox", { name: /Time range/ }).click();
    await page.getByRole("option", { name: "24h" }).click();
    await expect(page.getByRole("combobox", { name: "Time range: 24h" })).toBeVisible();
    await expect(page).toHaveURL(/range=24h/);

    const activityForRepo = page.waitForResponse((response) => {
      const url = response.url();
      return url.includes("/api/v1/activity") && url.includes("repo=github.com%2Facme%2Fwidgets");
    });
    await page.getByRole("combobox", { name: /Repository/ }).click();
    await page.getByRole("option", { name: "github.com/acme/widgets" }).click();
    await expect(page.getByRole("combobox", { name: "Repository: acme/widgets" })).toBeVisible();
    await activityForRepo;
  });

  test("mobile activity hide-org toggle updates card repo labels and persists", async ({ page }) => {
    await page.addInitScript(() => {
      if (sessionStorage.getItem("middleman:test:mobile-hide-org:init") === "1") {
        return;
      }
      localStorage.removeItem("middleman:hideOrgName");
      sessionStorage.setItem("middleman:test:mobile-hide-org:init", "1");
    });

    await page.goto("/m?range=30d&view=threaded");
    await expect(page.locator(".mobile-activity-inbox")).toBeVisible();

    const card = page
      .locator(".mobile-activity-card", {
        has: page.locator(".mobile-activity-card__title", {
          hasText: "Add widget caching layer",
        }),
      })
      .first();
    await expect(card).toBeVisible({ timeout: 10_000 });

    const repoLabel = card.locator(".mobile-activity-card__meta span").first();
    const hideOrgToggle = page.getByRole("button", {
      name: "Hide org",
    });
    await expect(hideOrgToggle).toHaveAttribute("aria-pressed", "false");
    await expect(repoLabel).toHaveText("acme/widgets");

    await hideOrgToggle.click();

    await expect(hideOrgToggle).toHaveAttribute("aria-pressed", "true");
    await expect(repoLabel).toHaveText("widgets");
    await expect(repoLabel).not.toHaveText("acme/widgets");

    await page.reload();

    await expect(card).toBeVisible({ timeout: 10_000 });
    await expect(hideOrgToggle).toHaveAttribute("aria-pressed", "true");
    await expect(repoLabel).toHaveText("widgets");
  });

  test("mobile activity card routes to a focused thread detail", async ({ page }) => {
    await page.goto("/m?range=30d&view=threaded");
    await expect(page.getByRole("button", { name: "Open thread" })).toHaveCount(0);
    await expect(page.locator(".mobile-activity-card__button").first()).toBeVisible();

    await page.locator(".mobile-activity-card__button").first().click();

    await expect(page).toHaveURL(/\/focus\/(?:host\/[^/]+\/)?(?:pulls|issues)\//);
    await expect(page.locator(".focus-layout")).toBeVisible();
    await expect(
      page.locator(".focus-layout .pull-detail .detail-title, .focus-layout .issue-detail .detail-title"),
    ).toBeVisible();
    await expectReadableDetail(page);
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("canonical activity phone presentation opens thread detail", async ({ page }) => {
    await page.goto("/");
    await expectPathname(page, "/");
    await expect(page.locator(".mobile-shell")).toBeVisible();
    await expect(page.getByRole("button", { name: "Open thread" })).toHaveCount(0);
    await expect(page.locator(".mobile-activity-card__button").first()).toBeVisible();

    await page.locator(".mobile-activity-card__button").first().click();

    await expect(page).toHaveURL(/\/focus\/(?:host\/[^/]+\/)?(?:pulls|issues)\//);
    await expect(page.locator(".focus-layout")).toBeVisible();
    await expect(
      page.locator(".focus-layout .pull-detail .detail-title, .focus-layout .issue-detail .detail-title"),
    ).toBeVisible();
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("focused PR files tab stays on the phone detail route", async ({ page }) => {
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await expect(page.locator(".focus-layout .pull-detail .detail-title")).toBeVisible();

    await page
      .locator(".focus-layout .detail-tab", {
        hasText: "Files changed",
      })
      .click();

    await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/1\/files$/);
    await expect(page.locator(".focus-layout .files-layout")).toBeVisible();
    await expect(page.locator(".focus-layout .diff-view")).toBeVisible();
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("phone canonical PR files tab stays on the canonical detail route", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1");
    await expectPathname(page, "/pulls/github/acme/widgets/1");
    await expect(page.locator(".focus-layout .pull-detail .detail-title")).toBeVisible();

    await page
      .locator(".focus-layout .detail-tab", {
        hasText: "Files changed",
      })
      .click();

    await expectPathname(page, "/pulls/github/acme/widgets/1/files");
    await expect(page.locator(".focus-layout .files-layout")).toBeVisible();
    await expect(page.locator(".focus-layout .diff-view")).toBeVisible();
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("phone canonical PR files deep link renders focus presentation without changing URL", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1/files");

    await expectPathname(page, "/pulls/github/acme/widgets/1/files");
    await expect(page.locator(".focus-layout .files-layout")).toBeVisible();
    await expect(page.locator(".focus-layout .diff-view")).toBeVisible();
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("phone canonical issue deep link renders focus presentation without changing URL", async ({ page }) => {
    await page.goto("/issues/github/acme/widgets/10");

    await expectPathname(page, "/issues/github/acme/widgets/10");
    await expect(page.locator(".focus-layout .issue-detail .detail-title")).toBeVisible();
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("phone users can opt out of automatic mobile redirect", async ({ page }) => {
    await page.goto("/?desktop=1");

    await expect(page).toHaveURL(/\/?desktop=1$/);
    await expect(page.locator(".app-header")).toBeVisible();
    await expect(page.locator(".mobile-shell")).toHaveCount(0);
  });

  test("mobile PR and issue tabs use dedicated phone routes", async ({ page }) => {
    await page.goto("/m/pulls");
    await expect(page.locator(".mobile-shell")).toBeVisible();
    await expect(page.locator(".mobile-tab--active")).toHaveText("PRs");
    await expect(page.locator(".focus-list")).toBeVisible();
    await expectReadableFocusList(page, ".pull-item");

    await page.getByRole("link", { name: "Issues" }).click();
    await expect(page).toHaveURL(/\/m\/issues(?:\?|$)/);
    await expect(page.locator(".mobile-tab--active")).toHaveText("Issues");
    await expect(page.locator(".focus-list")).toBeVisible();
    await expectReadableFocusList(page, ".issue-item");
  });
});

test.describe("high-density phone routes", () => {
  const pixel7 = devices["Pixel 7"];
  test.use({
    viewport: pixel7.viewport,
    deviceScaleFactor: pixel7.deviceScaleFactor,
    userAgent: pixel7.userAgent,
  });

  test("mobile activity sizing stays rem-based and readable on high-density Android displays", async ({ page }) => {
    await page.goto("/m?range=30d&view=threaded");

    await expect(page.locator(".mobile-shell")).toBeVisible();
    await expect(page.locator(".mobile-activity-inbox")).toBeVisible();
    await page.getByRole("combobox", { name: /Activity type/ }).click();
    await expect(page.getByRole("option", { name: "All" })).toBeVisible();

    const metrics = await page.evaluate(() => {
      const fontSize = (selector: string): number => {
        const node = document.querySelector(selector);
        return node ? Number.parseFloat(getComputedStyle(node).fontSize) : 0;
      };
      const shell = document.querySelector(".mobile-shell");
      const inbox = document.querySelector(".mobile-activity-inbox");
      const tokenValue = (node: Element | null, name: string): string =>
        node ? getComputedStyle(node).getPropertyValue(name).trim() : "";
      const filterControls = [
        ...document.querySelectorAll(".mobile-activity-filter-grid .select-dropdown-trigger, .mobile-filter-toggle"),
      ]
        .map((control) => control.getBoundingClientRect())
        .map((rect) => ({ left: rect.left, right: rect.right }));
      const search = document.querySelector(".search-input")?.getBoundingClientRect();
      const firstOption = document
        .querySelector(".mobile-filter-dropdown .select-dropdown-option")
        ?.getBoundingClientRect();
      return {
        dpr: window.devicePixelRatio,
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        mobileTypeToken: tokenValue(shell, "--mobile-type-body"),
        activityTypeToken: tokenValue(inbox, "--mobile-type-body"),
        densityScale: tokenValue(inbox, "--mobile-device-density-scale"),
        bodyFontSize: fontSize(".mobile-activity-inbox"),
        filterFontSize: fontSize(".mobile-activity-filter-grid .select-dropdown-trigger"),
        filterOptionFontSize: fontSize(".mobile-filter-dropdown .select-dropdown-option"),
        filterOptionHeight: firstOption?.height ?? 0,
        tabFontSize: fontSize(".mobile-tabs a"),
        searchHeight: search?.height ?? 0,
        searchLeft: search?.left ?? 0,
        searchRight: search?.right ?? 0,
        filterControls,
      };
    });

    expect(metrics.dpr).toBeGreaterThanOrEqual(2.5);
    expect(metrics.mobileTypeToken).toBe("1.24rem");
    expect(metrics.activityTypeToken).toBe("1.24rem");
    expect(metrics.densityScale).toBe("");
    expect(metrics.documentWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.bodyFontSize).toBeGreaterThanOrEqual(16);
    expect(metrics.filterFontSize).toBeGreaterThanOrEqual(15);
    expect(metrics.filterOptionFontSize).toBeGreaterThanOrEqual(15);
    expect(metrics.filterOptionHeight).toBeGreaterThanOrEqual(44);
    expect(metrics.tabFontSize).toBeGreaterThanOrEqual(16);
    expect(metrics.searchHeight).toBeGreaterThanOrEqual(44);
    expect(metrics.searchLeft).toBeGreaterThanOrEqual(0);
    expect(metrics.searchRight).toBeLessThanOrEqual(metrics.viewportWidth);
    for (const control of metrics.filterControls) {
      expect(control.left).toBeGreaterThanOrEqual(0);
      expect(control.right).toBeLessThanOrEqual(metrics.viewportWidth);
    }
  });
});
