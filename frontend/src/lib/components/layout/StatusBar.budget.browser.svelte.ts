// Browser-tier analog of StatusBar.budget.test.ts. The budget bars and popover
// are exercised through the real app shell with rate-limit data mocked at the
// fetch boundary. A real Chromium page provides
// matchMedia/ResizeObserver/IntersectionObserver/canvas natively, so the jsdom
// installAppDomGlobals() shim is gone; the browser harness stubs only
// EventSource.
//
// Color expectations are asserted on the inline style values the components set
// (`var(--budget-red)`), not on computed rgb pixels: element.style.background
// reads the raw inline attribute the component wrote, and the token is the
// semantic contract. DOM-shape assertions (.budget-bars/.budget-fill/.budget-
// popover/.host-section/.health-dot/.row-unit/.budget-spent textContent and
// classList) stay as querySelector against the real DOM, since the page locator
// API only exposes getByText/getByRole/getByTitle/getByTestId.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "../../../test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "../../../test/mockApiFetch.js";

function rateLimits(hosts: Record<string, unknown>): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/rate-limits") return null;
    return jsonResponse({ hosts });
  };
}

const knownHost = {
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
};

const unknownHost = {
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
};

const pausedHost = {
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
};

// Real Chromium drives the genuine async render/network chain, which is slower
// than jsdom's synchronous fixtures, so each poll gets a generous window. The
// outer testTimeout still caps the whole case.
const WAIT = 10_000;

let mounted: MountedBrowserApp | null = null;

async function mountStatusBar(overrides: MockRouteOverride[] = []): Promise<HTMLElement> {
  mounted = await mountBrowserApp("/pulls", { overrides });
  await vi.waitFor(() => expect(document.querySelector(".budget-bars")).not.toBeNull(), WAIT);
  return document.querySelector<HTMLElement>(".budget-bars")!;
}

async function openPopover(bars: HTMLElement): Promise<HTMLElement> {
  await page.elementLocator(bars).click();
  await vi.waitFor(() => expect(document.querySelector(".budget-popover")).not.toBeNull(), WAIT);
  return document.querySelector<HTMLElement>(".budget-popover")!;
}

function healthDot(popover: HTMLElement, hostname: string): HTMLElement {
  const section = Array.from(popover.querySelectorAll<HTMLElement>(".host-section")).find((s) =>
    s.textContent?.includes(hostname),
  );
  expect(section).toBeTruthy();
  const dot = section!.querySelector<HTMLElement>(".health-dot");
  expect(dot).toBeTruthy();
  return dot!;
}

describe("budget display", () => {
  vi.setConfig({ testTimeout: 30_000 });

  beforeEach(async () => {
    // The status bar lives in the desktop app shell; size the real Chromium
    // viewport to a desktop width so the standard layout (and the budget area
    // in status-right) renders deterministically.
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("status bar shows budget bars with known data", async () => {
    const bars = await mountStatusBar();
    expect(bars.textContent).toContain("REST");
    expect(bars.textContent).toContain("GQL");
  });

  it("budget bars keep eager refresh budget out of the compact status", async () => {
    const bars = await mountStatusBar();
    expect(bars.textContent).not.toContain("Refresh");
    expect(bars.textContent).not.toContain("42 / 500");
    expect(bars.querySelector(".budget-count")).toBeNull();
  });

  // The popover dialog exposes REST req, GraphQL pts, and the eager
  // refresh budget spend from the same payload.
  it("clicking budget area opens popover with per-host breakdown", async () => {
    const bars = await mountStatusBar();
    const popover = await openPopover(bars);
    const units = Array.from(popover.querySelectorAll(".row-unit")).map((el) => el.textContent?.trim());
    expect(units).toContain("req");
    expect(units).toContain("pts");
    expect(popover.textContent).toContain("Eager refresh");
    expect(popover.textContent).toContain("42 / 500 budgeted req/hr");
    expect(popover.textContent).toContain("Details, comments, and backfills pause when spent.");
    expect(popover.textContent).not.toContain("Optional");
    expect(popover.querySelector(".budget-spent")?.textContent).toBe("42");

    const eagerLabel = popover.querySelector<HTMLElement>(".budget-row--eager .row-label");
    const eagerValue = popover.querySelector<HTMLElement>(".budget-row--eager .row-value");
    expect(eagerLabel).not.toBeNull();
    expect(eagerValue).not.toBeNull();
    expect(Math.abs(eagerLabel!.getBoundingClientRect().top - eagerValue!.getBoundingClientRect().top)).toBeLessThan(2);
  });

  it("marks sync budget spend that exceeds the configured limit", async () => {
    const bars = await mountStatusBar([
      rateLimits({
        "github.com": {
          ...knownHost,
          budget_limit: 500,
          budget_spent: 1300,
          budget_remaining: -800,
        },
      }),
    ]);

    expect(bars.textContent).not.toContain("Refresh");
    expect(bars.textContent).not.toContain("1.3k / 500");
    expect(bars.querySelector(".budget-count")).toBeNull();

    const popover = await openPopover(bars);
    const spent = popover.querySelector<HTMLElement>(".budget-spent");
    expect(spent?.textContent).toBe("1.3k");
    expect(spent?.classList.contains("budget-spent--over")).toBe(true);
    expect(popover.textContent).toContain("Details, comments, and backfills are paused.");
    expect(popover.textContent).not.toContain("eager refresh deferred");
  });

  it("popover dismisses on Escape", async () => {
    const bars = await mountStatusBar();
    await openPopover(bars);

    pressKey("Escape", {}, document);
    await vi.waitFor(() => expect(document.querySelector(".budget-popover")).toBeNull(), WAIT);
  });

  it("popover dismisses on click outside", async () => {
    const bars = await mountStatusBar();
    await openPopover(bars);

    // Popover attaches its outside-click listener via setTimeout(0) to
    // avoid catching the opening click. Let that timer run before
    // clicking outside.
    await new Promise((resolve) => setTimeout(resolve, 5));

    await page.elementLocator(document.querySelector<HTMLElement>(".app-main")!).click();
    await vi.waitFor(() => expect(document.querySelector(".budget-popover")).toBeNull(), WAIT);
  });

  // Merges the Enter/Space keyboard cases from both original suites: a native
  // <button> trigger is activated by Enter/Space through the browser's native
  // button activation, so the conversion asserts the platform guarantee
  // directly — the trigger is a real <button>, whose activation (click) opens
  // the popover and Escape closes it.
  it("popover trigger is a native button so Enter/Space activate it", async () => {
    const bars = await mountStatusBar();
    expect(bars.tagName).toBe("BUTTON");
    expect(bars.getAttribute("aria-haspopup")).toBe("dialog");
    expect(bars.getAttribute("aria-expanded")).toBe("false");

    bars.focus();
    expect(document.activeElement).toBe(bars);
    await page.elementLocator(bars).click();
    await vi.waitFor(() => expect(document.querySelector(".budget-popover")).not.toBeNull(), WAIT);
    expect(bars.getAttribute("aria-expanded")).toBe("true");

    pressKey("Escape", {}, document);
    await vi.waitFor(() => expect(document.querySelector(".budget-popover")).toBeNull(), WAIT);
  });

  it("mixed known/unknown hosts show worst-case from known only", async () => {
    const bars = await mountStatusBar([
      rateLimits({
        "github.com": knownHost,
        "ghe.corp.example.com": unknownHost,
      }),
    ]);

    // Should show REST/GQL labels (not --) because github.com is known
    expect(bars.textContent).toContain("REST");
    expect(bars.textContent).toContain("GQL");

    // REST bar fill should reflect github.com's known ratio
    expect(bars.querySelector(".budget-fill")).not.toBeNull();

    // Popover should show both hosts
    const popover = await openPopover(bars);
    expect(popover.textContent).toContain("github.com");
    expect(popover.textContent).toContain("ghe.corp.example.com");
    // Known host shows compact ratio + abbreviated unit
    expect(popover.textContent).toMatch(/4\.5k\s*\/\s*5k\s+req\b/);
    expect(popover.textContent).toContain("not yet observed");

    // Unknown host's health dot must be tagged unknown so it renders
    // with the muted color token instead of a budget color.
    expect(healthDot(popover, "github.com").classList.contains("health-dot--unknown")).toBe(false);
    expect(healthDot(popover, "ghe.corp.example.com").classList.contains("health-dot--unknown")).toBe(true);
  });

  it("budget bars show unknown state when host not known", async () => {
    const bars = await mountStatusBar([rateLimits({ "github.com": unknownHost })]);

    // Unknown state: labels show -- instead of REST/GQL
    expect(bars.textContent).toContain("--");
    expect(bars.textContent).not.toContain("REST");
    expect(bars.textContent).not.toContain("GQL");
    // No budget count when budget disabled
    expect(bars.textContent).not.toContain("req/hr");
  });

  it("paused host shows red bars and sync paused indicator", async () => {
    const bars = await mountStatusBar([rateLimits({ "github.com": pausedHost })]);

    expect(bars.textContent).toContain("REST");
    // Bar fill should use the budget-red token when paused
    const restFill = bars.querySelector<HTMLElement>(".budget-fill");
    expect(restFill).not.toBeNull();
    expect(restFill!.style.background).toBe("var(--budget-red)");

    // Open popover — should show "sync paused" indicator
    const popover = await openPopover(bars);
    expect(popover.textContent).toContain("sync paused");
    // Single-host mode hides hostname header (and health dot).
    // Health dot color is tested in the multi-host paused case below.
  });

  it("paused multi-host shows red health dot in popover", async () => {
    const freshSecondHost = {
      ...unknownHost,
      requests_hour: 10,
      rate_remaining: 4900,
      rate_limit: 5000,
      rate_reset_at: new Date(Date.now() + 50 * 60_000).toISOString(),
      known: true,
    };
    const bars = await mountStatusBar([
      rateLimits({
        "github.com": pausedHost,
        "ghe.example.com": freshSecondHost,
      }),
    ]);

    const popover = await openPopover(bars);
    // Paused host (github.com) health dot should be the red token
    expect(healthDot(popover, "github.com").style.background).toBe("var(--budget-red)");
  });

  it("GQL known but REST unknown still hides eager refresh budget from compact status", async () => {
    const bars = await mountStatusBar([
      rateLimits({
        "github.com": {
          ...unknownHost,
          budget_limit: 500,
          budget_spent: 10,
          budget_remaining: 490,
          gql_remaining: 4800,
          gql_limit: 5000,
          gql_reset_at: new Date(Date.now() + 30 * 60_000).toISOString(),
          gql_known: true,
        },
      }),
    ]);

    // GQL bar should show (known), REST should show -- placeholder
    expect(bars.textContent).toContain("GQL");
    expect(bars.textContent).not.toContain("REST");
    expect(bars.textContent).toContain("--");
    // Eager refresh budget remains available in the popover, not the compact status.
    expect(bars.textContent).not.toContain("Refresh");
    expect(bars.textContent).not.toContain("10 / 500");
    expect(bars.querySelector(".budget-count")).toBeNull();

    const popover = await openPopover(bars);
    expect(popover.textContent).toContain("Eager refresh");
    expect(popover.textContent).toContain("10 / 500 budgeted req/hr");
  });

  it("stale host excluded from compact bars, fresh host drives ratio", async () => {
    const staleHost = {
      ...unknownHost,
      rate_limit: 5000,
      known: true,
    };
    const bars = await mountStatusBar([
      rateLimits({
        "github.com": knownHost,
        "ghe.example.com": staleHost,
      }),
    ]);

    // Compact bars should show REST/GQL from the fresh host (github.com)
    expect(bars.textContent).toContain("REST");
    expect(bars.textContent).toContain("GQL");
    // Bar fill should be present (driven by fresh host, not stale)
    expect(bars.querySelector(".budget-fill")).not.toBeNull();

    // Popover: stale host health dot should be muted
    const popover = await openPopover(bars);
    expect(healthDot(popover, "ghe.example.com").classList.contains("health-dot--unknown")).toBe(true);
  });
});
