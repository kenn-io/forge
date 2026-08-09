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

function rateLimits(
  providerPools: Record<string, unknown>,
  localCeilings: Record<string, unknown> = {},
): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/rate-limits") return null;
    const normalizedPools = Object.fromEntries(
      Object.entries(providerPools).map(([key, value]) => [
        key,
        { ...(value as Record<string, unknown>), platform_host: key },
      ]),
    );
    return jsonResponse({ provider_pools: normalizedPools, local_ceilings: localCeilings });
  };
}

function syncStatus(status: Record<string, unknown>): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/sync/status") return null;
    return jsonResponse(status);
  };
}

function credentialAwareRateLimits(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/rate-limits") return null;
    const reset = new Date(Date.now() + 30 * 60_000).toISOString();
    return jsonResponse({
      provider_pools: {
        "github:github.com:installation:42": {
          provider: "github",
          platform_host: "github.com",
          rate_principal: "installation:42",
          principal_label: "GitHub App installation 42",
          reserve_buffer: 200,
          sync_throttle_factor: 1,
          sync_paused: false,
          rest: { remaining: 14900, limit: 15000, reset_at: reset, known: true, requests: 100 },
          graphql: { remaining: 9900, limit: 10000, reset_at: reset, known: true, requests: 25 },
        },
        "github:github.com:user:7": {
          provider: "github",
          platform_host: "github.com",
          rate_principal: "user:7",
          principal_label: "GitHub user maintainer",
          reserve_buffer: 200,
          sync_throttle_factor: 1,
          sync_paused: false,
          rest: { remaining: 4900, limit: 5000, reset_at: reset, known: true, requests: 10 },
          graphql: { remaining: -1, limit: -1, reset_at: "", known: false, requests: 0 },
        },
      },
      local_ceilings: {
        "github.com": {
          provider: "github",
          platform_host: "github.com",
          rate_principal: "host",
          principal_label: "Host credential",
          limit: 50000,
          spent: 42,
          remaining: 49958,
        },
      },
    });
  };
}

const unknownResource = {
  remaining: -1,
  limit: -1,
  reset_at: "",
  known: false,
  requests: 0,
};

function providerHost(rest: Record<string, unknown>, graphql: Record<string, unknown> = unknownResource) {
  return {
    provider: "github",
    platform_host: "github.com",
    rate_principal: "host",
    principal_label: "Host credential",
    reserve_buffer: 200,
    sync_throttle_factor: 1,
    sync_paused: false,
    rest,
    graphql,
  };
}

const knownHost = providerHost(
  {
    remaining: 4500,
    limit: 5000,
    reset_at: new Date(Date.now() + 30 * 60_000).toISOString(),
    known: true,
    requests: 100,
  },
  {
    remaining: 4900,
    limit: 5000,
    reset_at: new Date(Date.now() + 25 * 60_000).toISOString(),
    known: true,
    requests: 25,
  },
);

const unknownHost = providerHost(unknownResource);

const pausedHost = providerHost(
  {
    remaining: 50,
    limit: 5000,
    reset_at: new Date(Date.now() + 10 * 60_000).toISOString(),
    known: true,
    requests: 500,
  },
  {
    remaining: 100,
    limit: 5000,
    reset_at: new Date(Date.now() + 10 * 60_000).toISOString(),
    known: true,
    requests: 100,
  },
);

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
    // in the status bar right section) renders deterministically.
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

  it("shows credential-specific provider quota separately from the local sync ceiling", async () => {
    const bars = await mountStatusBar([credentialAwareRateLimits()]);
    const popover = await openPopover(bars);
    expect(popover.textContent).toContain("Provider quota");
    expect(popover.textContent).toContain("GitHub App installation 42");
    expect(popover.textContent).toContain("GitHub user maintainer");
    expect(popover.textContent).toContain("Local sync ceiling");
    expect(popover.textContent).toContain("42 / 50k requests");
    expect(popover.textContent).not.toContain("Eager refresh");
    expect(popover.textContent).not.toContain("budgeted req/hr");
  });

  it("budget bars keep eager refresh budget out of the compact status", async () => {
    const bars = await mountStatusBar();
    expect(bars.textContent).not.toContain("Refresh");
    expect(bars.textContent).not.toContain("42 / 500");
    expect(bars.querySelector(".budget-count")).toBeNull();
  });

  // The popover dialog exposes provider REST/GraphQL pools and the separate
  // local process ceiling from the same payload.
  it("clicking budget area opens popover with per-host breakdown", async () => {
    const bars = await mountStatusBar();
    const popover = await openPopover(bars);
    const units = Array.from(popover.querySelectorAll(".row-unit")).map((el) => el.textContent?.trim());
    expect(units).toContain("req");
    expect(units).toContain("pts");
    expect(popover.textContent).toContain("Provider quota");
    expect(popover.textContent).toContain("Local sync ceiling");
    expect(popover.textContent).toContain("42 / 50k requests");
    expect(popover.textContent).toContain("provider quota above is authoritative");
    expect(popover.querySelector(".budget-spent")?.textContent).toBe("42");

    const ceilingNote = popover.querySelector<HTMLElement>(".budget-row--ceiling .row-note");
    expect(ceilingNote).not.toBeNull();
    const noteLineHeight = Number.parseFloat(getComputedStyle(ceilingNote!).lineHeight);
    expect(ceilingNote!.getBoundingClientRect().height).toBeGreaterThan(noteLineHeight * 1.5);

    const ceilingLabel = popover.querySelector<HTMLElement>(".budget-row--ceiling .row-label");
    const ceilingValue = popover.querySelector<HTMLElement>(".budget-row--ceiling .row-value");
    expect(ceilingLabel).not.toBeNull();
    expect(ceilingValue).not.toBeNull();
    expect(
      Math.abs(ceilingLabel!.getBoundingClientRect().top - ceilingValue!.getBoundingClientRect().top),
    ).toBeLessThan(2);

    // The bar runs with overflow="visible" so the popover's absolute
    // bottom-anchored panel can open fully above the 24px bar rather than
    // being cut to the section's height.
    const popoverRect = popover.getBoundingClientRect();
    const barRect = document.querySelector(".kit-status-bar")!.getBoundingClientRect();
    expect(popoverRect.height).toBeGreaterThan(100);
    // Subpixel tolerance: with the real app.css tokens loaded the inline
    // wrapper's box can sit a fraction of a px off the bar edge; the
    // invariant is that the panel opens above the bar, not into it.
    expect(popoverRect.bottom).toBeLessThanOrEqual(barRect.top + 1);
  });

  it("marks a local ceiling that has been fully spent", async () => {
    const bars = await mountStatusBar([
      rateLimits(
        { "github.com": knownHost },
        {
          "github.com": {
            provider: "github",
            platform_host: "github.com",
            limit: 500,
            spent: 500,
            remaining: 0,
          },
        },
      ),
    ]);

    expect(bars.textContent).not.toContain("Refresh");
    expect(bars.textContent).not.toContain("500 / 500");
    expect(bars.querySelector(".budget-count")).toBeNull();

    const popover = await openPopover(bars);
    const spent = popover.querySelector<HTMLElement>(".budget-spent");
    expect(spent?.textContent).toBe("500");
    expect(spent?.style.color).toBe("var(--budget-red)");
    expect(popover.textContent).toContain("Local sync ceiling");
  });

  it("identifies a local ceiling failure while provider quota remains healthy", async () => {
    const failedAt = new Date(Date.now() - 5 * 60_000).toISOString();
    const failedResetAt = new Date(Date.now() + 30 * 60_000).toISOString();
    const unrelatedResetAt = new Date(Date.now() + 5 * 60_000).toISOString();
    await mountStatusBar([
      rateLimits(
        { "github.com": knownHost },
        {
          "github:github.com:user:7": {
            provider: "github",
            platform_host: "github.com",
            rate_principal: "user:7",
            principal_label: "GitHub user maintainer",
            limit: 500,
            spent: 450,
            remaining: 50,
            reset_at: failedResetAt,
          },
          "gitlab:gitlab.example.com": {
            provider: "gitlab",
            platform_host: "gitlab.example.com",
            rate_principal: "host",
            principal_label: "Unrelated host credential",
            limit: 900,
            spent: 900,
            remaining: 0,
            reset_at: unrelatedResetAt,
          },
        },
      ),
      syncStatus({
        running: false,
        last_run_at: failedAt,
        last_error: "list open PRs: local sync emergency ceiling exhausted",
        last_error_code: "localSyncCeilingExhausted",
        last_error_ceiling_key: "github:github.com:user:7",
        last_error_ceiling_reset_at: failedResetAt,
      }),
    ]);

    const failure = document.querySelector<HTMLElement>(".status-item--local-ceiling");
    expect(failure).not.toBeNull();
    expect(failure?.textContent).toContain("local sync ceiling reached");
    expect(failure?.textContent).toContain("450 / 500");
    expect(failure?.textContent).not.toContain("900 / 900");
    expect(failure?.title).toContain("resets in 30m");
    expect(failure?.title).not.toContain("resets in 5m");

    const restFill = document.querySelector<HTMLElement>(".budget-fill");
    expect(restFill?.style.background).toBe("var(--budget-green)");
  });

  it("does not pair a long-running prior-window failure with the next live ceiling", async () => {
    const failedResetAt = new Date(Date.now() - 5 * 60_000).toISOString();
    const completedAt = new Date(Date.now()).toISOString();
    const liveResetAt = new Date(Date.now() + 55 * 60_000).toISOString();
    await mountStatusBar([
      rateLimits(
        { "github.com": knownHost },
        {
          "github:github.com:user:7": {
            provider: "github",
            platform_host: "github.com",
            rate_principal: "user:7",
            principal_label: "GitHub user maintainer",
            limit: 500,
            spent: 0,
            remaining: 500,
            reset_at: liveResetAt,
          },
        },
      ),
      syncStatus({
        running: false,
        last_run_at: completedAt,
        last_error: "list open PRs: local sync emergency ceiling exhausted",
        last_error_code: "localSyncCeilingExhausted",
        last_error_ceiling_key: "github:github.com:user:7",
        last_error_ceiling_reset_at: failedResetAt,
      }),
    ]);

    const failure = document.querySelector<HTMLElement>(".status-item--local-ceiling");
    expect(failure).not.toBeNull();
    expect(failure?.textContent).toBe("local sync ceiling reached");
    expect(failure?.textContent).not.toContain("0 / 500");
    expect(failure?.title).not.toContain("resets in");
  });

  it("popover dismisses on Escape and restores focus to the trigger", async () => {
    const bars = await mountStatusBar();
    await openPopover(bars);

    pressKey("Escape", {}, document);
    await vi.waitFor(() => expect(document.querySelector(".budget-popover")).toBeNull(), WAIT);
    expect(document.activeElement).toBe(bars);
  });

  it("popover dismisses on press outside", async () => {
    const bars = await mountStatusBar();
    await openPopover(bars);

    // kit's dismissable helper listens for mousedown outside the wrapper;
    // the opening interaction happened before mount, so no settling delay
    // is needed.
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

    // Open popover — should identify the actual provider reserve.
    const popover = await openPopover(bars);
    expect(popover.textContent).toContain("provider reserve reached");
  });

  it("paused multi-host shows red health dot in popover", async () => {
    const freshSecondHost = providerHost({
      remaining: 4900,
      limit: 5000,
      reset_at: new Date(Date.now() + 50 * 60_000).toISOString(),
      known: true,
      requests: 10,
    });
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
        "github.com": providerHost(unknownResource, {
          remaining: 4800,
          limit: 5000,
          reset_at: new Date(Date.now() + 30 * 60_000).toISOString(),
          known: true,
          requests: 10,
        }),
      }),
    ]);

    // GQL bar should show (known), REST should show -- placeholder
    expect(bars.textContent).toContain("GQL");
    expect(bars.textContent).not.toContain("REST");
    expect(bars.textContent).toContain("--");
    // The compact status remains provider-only.
    expect(bars.textContent).not.toContain("Refresh");
    expect(bars.textContent).not.toContain("10 / 500");
    expect(bars.querySelector(".budget-count")).toBeNull();

    const popover = await openPopover(bars);
    expect(popover.textContent).toContain("Provider quota");
    expect(popover.textContent).not.toContain("Local sync ceiling");
  });

  it("stale host excluded from compact bars, fresh host drives ratio", async () => {
    const staleHost = providerHost({ ...unknownResource, limit: 5000, known: true });
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
