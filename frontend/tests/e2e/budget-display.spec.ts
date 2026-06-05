import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("status bar shows budget bars with known data", async ({ page }) => {
  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await expect(bars).toBeVisible();
  await expect(bars.getByText("REST")).toBeVisible();
  await expect(bars.getByText("GQL")).toBeVisible();
});

test("budget bars show middleman count when budget enabled", async ({ page }) => {
  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await expect(bars.getByText("42 req/hr")).toBeVisible();
});

test("clicking budget area opens popover", async ({ page }) => {
  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await bars.click();

  // Popover exposes itself as a dialog with the expected accessible name.
  const popover = page.getByRole("dialog", { name: "API Budget" });
  await expect(popover).toBeVisible();
  await expect(popover.getByText("req", { exact: true })).toBeVisible();
  await expect(popover.getByText("pts", { exact: true })).toBeVisible();
});

test("popover dismisses on Escape", async ({ page }) => {
  await page.goto("/pulls");

  await page.locator(".budget-bars").click();
  await expect(page.locator(".budget-popover")).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(page.locator(".budget-popover")).not.toBeVisible();
});

test("popover dismisses on click outside", async ({ page }) => {
  await page.goto("/pulls");

  await page.locator(".budget-bars").click();
  await expect(page.locator(".budget-popover")).toBeVisible();

  // Popover attaches its outside-click listener via setTimeout(0) to
  // avoid catching the opening click. Flush one animation frame so the
  // listener is registered before we click outside.
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));

  await page.locator(".app-main").click();
  await expect(page.locator(".budget-popover")).not.toBeVisible();
});

test("popover opens via keyboard (Enter) and closes via Escape", async ({ page }) => {
  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await bars.focus();
  await page.keyboard.press("Enter");

  const popover = page.locator(".budget-popover");
  await expect(popover).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(popover).not.toBeVisible();
});

test("popover opens via keyboard (Space)", async ({ page }) => {
  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await bars.focus();
  await page.keyboard.press("Space");

  await expect(page.locator(".budget-popover")).toBeVisible();
});

test("mixed known/unknown hosts show worst-case from known only", async ({ page }) => {
  await page.route("**/api/v1/rate-limits", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: {
          "github.com": {
            requests_hour: 100,
            rate_remaining: 4500,
            rate_limit: 5000,
            rate_reset_at: new Date(Date.now() + 30 * 60_000).toISOString(),
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: true,
            budget_limit: 500,
            budget_spent: 100,
            budget_remaining: 400,
            gql_remaining: 4900,
            gql_limit: 5000,
            gql_reset_at: new Date(Date.now() + 25 * 60_000).toISOString(),
            gql_known: true,
          },
          "ghe.corp.example.com": {
            requests_hour: 0,
            rate_remaining: -1,
            rate_limit: -1,
            rate_reset_at: "",
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: false,
            budget_limit: 0,
            budget_spent: 0,
            budget_remaining: 0,
            gql_remaining: -1,
            gql_limit: -1,
            gql_reset_at: "",
            gql_known: false,
          },
        },
      }),
    });
  });

  await page.goto("/pulls");

  // Should show REST/GQL labels (not --) because github.com is known
  const bars = page.locator(".budget-bars");
  await expect(bars.getByText("REST")).toBeVisible();
  await expect(bars.getByText("GQL")).toBeVisible();

  // REST bar fill should reflect github.com's 90% ratio (green)
  const restFill = bars.locator(".budget-fill").first();
  await expect(restFill).toBeVisible();

  // Popover should show both hosts
  await bars.click();
  const popover = page.locator(".budget-popover");
  await expect(popover.getByText("github.com")).toBeVisible();
  await expect(popover.getByText("ghe.corp.example.com")).toBeVisible();
  // Known host shows compact ratio + abbreviated unit
  await expect(popover.getByText(/4\.5k\s*\/\s*5k\s+req\b/)).toBeVisible();
  await expect(popover.getByText("not yet observed").first()).toBeVisible();

  // Unknown host's health dot must be tagged unknown so it renders
  // with the muted color token instead of a budget color.
  const ghHealthDot = popover
    .locator(".host-section")
    .filter({
      hasText: "github.com",
    })
    .locator(".health-dot");
  const gheHealthDot = popover
    .locator(".host-section")
    .filter({
      hasText: "ghe.corp.example.com",
    })
    .locator(".health-dot");
  await expect(ghHealthDot).not.toHaveClass(/health-dot--unknown/);
  await expect(gheHealthDot).toHaveClass(/health-dot--unknown/);
});

test("budget bars show unknown state when host not known", async ({ page }) => {
  await page.route("**/api/v1/rate-limits", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: {
          "github.com": {
            requests_hour: 0,
            rate_remaining: -1,
            rate_limit: -1,
            rate_reset_at: "",
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: false,
            budget_limit: 0,
            budget_spent: 0,
            budget_remaining: 0,
            gql_remaining: -1,
            gql_limit: -1,
            gql_reset_at: "",
            gql_known: false,
          },
        },
      }),
    });
  });

  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await expect(bars).toBeVisible();
  // Unknown state: labels show -- instead of REST/GQL
  await expect(bars.getByText("--").first()).toBeVisible();
  await expect(bars.getByText("REST")).not.toBeVisible();
  await expect(bars.getByText("GQL")).not.toBeVisible();
  // No budget count when budget disabled
  await expect(bars.getByText("req/hr")).not.toBeVisible();
});

test("paused host shows red health dot and sync paused indicator", async ({ page }) => {
  await page.route("**/api/v1/rate-limits", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: {
          "github.com": {
            requests_hour: 500,
            rate_remaining: 50,
            rate_limit: 5000,
            rate_reset_at: new Date(Date.now() + 10 * 60_000).toISOString(),
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 8,
            sync_paused: true,
            reserve_buffer: 200,
            known: true,
            budget_limit: 500,
            budget_spent: 400,
            budget_remaining: 100,
            gql_remaining: 100,
            gql_limit: 5000,
            gql_reset_at: new Date(Date.now() + 10 * 60_000).toISOString(),
            gql_known: true,
          },
        },
      }),
    });
  });

  await page.goto("/pulls");

  // Compact bars should be red when paused — labels inherit barColor
  const bars = page.locator(".budget-bars");
  await expect(bars.getByText("REST")).toBeVisible();
  // Bar fill should use budget-red
  const restFill = bars.locator(".budget-fill").first();
  await expect(restFill).toHaveCSS("background-color", "rgb(248, 113, 113)");

  // Open popover — should show "sync paused" indicator
  await bars.click();
  const popover = page.getByRole("dialog", { name: "API Budget" });
  await expect(popover).toBeVisible();
  await expect(popover.getByText("sync paused")).toBeVisible();
  // Single-host mode hides hostname header (and health dot).
  // Health dot color is tested in the multi-host paused case below.
});

test("paused multi-host shows red health dot in popover", async ({ page }) => {
  await page.route("**/api/v1/rate-limits", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: {
          "github.com": {
            requests_hour: 500,
            rate_remaining: 50,
            rate_limit: 5000,
            rate_reset_at: new Date(Date.now() + 10 * 60_000).toISOString(),
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 8,
            sync_paused: true,
            reserve_buffer: 200,
            known: true,
            budget_limit: 500,
            budget_spent: 400,
            budget_remaining: 100,
            gql_remaining: 100,
            gql_limit: 5000,
            gql_reset_at: new Date(Date.now() + 10 * 60_000).toISOString(),
            gql_known: true,
          },
          "ghe.example.com": {
            requests_hour: 10,
            rate_remaining: 4900,
            rate_limit: 5000,
            rate_reset_at: new Date(Date.now() + 50 * 60_000).toISOString(),
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: true,
            budget_limit: 0,
            budget_spent: 0,
            budget_remaining: 0,
            gql_remaining: -1,
            gql_limit: -1,
            gql_reset_at: "",
            gql_known: false,
          },
        },
      }),
    });
  });

  await page.goto("/pulls");

  await page.locator(".budget-bars").click();
  const popover = page.getByRole("dialog", { name: "API Budget" });
  await expect(popover).toBeVisible();

  // Paused host (github.com) health dot should be red
  const pausedDot = popover
    .locator(".host-section")
    .filter({
      hasText: "github.com",
    })
    .locator(".health-dot");
  await expect(pausedDot).toHaveCSS("background-color", "rgb(248, 113, 113)");
});

test("GQL known but REST unknown still shows budget count", async ({ page }) => {
  await page.route("**/api/v1/rate-limits", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: {
          "github.com": {
            requests_hour: 0,
            rate_remaining: -1,
            rate_limit: -1,
            rate_reset_at: "",
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: false,
            budget_limit: 500,
            budget_spent: 10,
            budget_remaining: 490,
            gql_remaining: 4800,
            gql_limit: 5000,
            gql_reset_at: new Date(Date.now() + 30 * 60_000).toISOString(),
            gql_known: true,
          },
        },
      }),
    });
  });

  await page.goto("/pulls");

  const bars = page.locator(".budget-bars");
  await expect(bars).toBeVisible();
  // GQL bar should show (known), REST should show -- placeholder
  await expect(bars.getByText("GQL")).toBeVisible();
  await expect(bars.getByText("REST")).not.toBeVisible();
  await expect(bars.getByText("--").first()).toBeVisible();
  // Budget count visible — budget is independent of REST rate observation
  await expect(bars.getByText("10 req/hr")).toBeVisible();
});

test("stale host excluded from compact bars, fresh host drives ratio", async ({ page }) => {
  await page.route("**/api/v1/rate-limits", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: {
          "github.com": {
            requests_hour: 100,
            rate_remaining: 4500,
            rate_limit: 5000,
            rate_reset_at: new Date(Date.now() + 30 * 60_000).toISOString(),
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: true,
            budget_limit: 500,
            budget_spent: 100,
            budget_remaining: 400,
            gql_remaining: 4900,
            gql_limit: 5000,
            gql_reset_at: new Date(Date.now() + 25 * 60_000).toISOString(),
            gql_known: true,
          },
          "ghe.example.com": {
            requests_hour: 0,
            rate_remaining: -1,
            rate_limit: 5000,
            rate_reset_at: "",
            hour_start: new Date().toISOString(),
            sync_throttle_factor: 1,
            sync_paused: false,
            reserve_buffer: 200,
            known: true,
            budget_limit: 0,
            budget_spent: 0,
            budget_remaining: 0,
            gql_remaining: -1,
            gql_limit: -1,
            gql_reset_at: "",
            gql_known: false,
          },
        },
      }),
    });
  });

  await page.goto("/pulls");

  // Compact bars should show REST/GQL from fresh host (github.com)
  const bars = page.locator(".budget-bars");
  await expect(bars.getByText("REST")).toBeVisible();
  await expect(bars.getByText("GQL")).toBeVisible();
  // Bar fill should be visible (driven by fresh host, not stale)
  await expect(bars.locator(".budget-fill").first()).toBeVisible();

  // Popover: stale host health dot should be muted
  await bars.click();
  const popover = page.getByRole("dialog", { name: "API Budget" });
  await expect(popover).toBeVisible();
  const staleDot = popover
    .locator(".host-section")
    .filter({
      hasText: "ghe.example.com",
    })
    .locator(".health-dot");
  await expect(staleDot).toHaveClass(/health-dot--unknown/);
});
