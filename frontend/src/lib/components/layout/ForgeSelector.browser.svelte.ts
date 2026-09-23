import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { HostSummary } from "../../api/fleet-snapshot.js";
import { dismissFlash, getFlashes } from "../../stores/flash.svelte.js";
import { navigateToURL } from "../../utils/pageNavigation.js";
import ForgeSelectorRuntimeHarness from "./ForgeSelectorRuntimeHarness.svelte";

// Leaving the SPA would unload the test page, so record the destination.
vi.mock("../../utils/pageNavigation.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../utils/pageNavigation.js")>();
  return { ...actual, navigateToURL: vi.fn() };
});

let snapshotHosts: HostSummary[] = [];
let browserLoginResponse: () => Response = () => new Response(null, { status: 500 });
let browserLoginRequests: { url: string; body: unknown }[] = [];
let originalFetch: typeof globalThis.fetch;
let unmount: (() => void) | undefined;

function requestURL(input: RequestInfo | URL): string {
  if (typeof input === "string") return input;
  return input instanceof URL ? input.href : input.url;
}

async function requestJSON(input: RequestInfo | URL, init?: RequestInit): Promise<unknown> {
  const body = input instanceof Request ? await input.clone().text() : String(init?.body ?? "");
  return body ? JSON.parse(body) : null;
}

function problemResponse(status: number, code: string, detail: string, reason?: string): Response {
  return new Response(JSON.stringify({ status, code, detail, details: reason ? { reason } : undefined }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

async function openHubRow(): Promise<HTMLAnchorElement> {
  await renderSelector();
  await waitForDirectory();
  await page.getByLabelText("Current Forge: Current spoke").click();
  const hub = Array.from(document.querySelectorAll<HTMLAnchorElement>(".forge-selector li a")).find(
    (link) => link.querySelector("strong")?.textContent === "Hub",
  );
  expect(hub).toBeDefined();
  return hub!;
}

function host(nodeID: string, name: string, options: Partial<HostSummary> = {}): HostSummary {
  return {
    id: `host-${nodeID}`,
    configKey: nodeID,
    nodeID,
    name,
    kind: "remote",
    federationRole: "spoke",
    baseURL: `https://${name.toLowerCase()}.example`,
    platform: "linux",
    preferredTransport: "http",
    reachable: true,
    diagnostics: [],
    operationAvailability: {},
    tmuxSessions: [],
    ...options,
  };
}

async function renderSelector(props: { compact?: boolean; fallbackLabel?: string } = {}): Promise<void> {
  const view = await render(ForgeSelectorRuntimeHarness, { props });
  unmount = view.unmount;
}

async function waitForDirectory(): Promise<void> {
  await vi.waitFor(() => {
    expect(document.querySelector(".forge-selector")).not.toBeNull();
  });
}

describe("ForgeSelector (browser)", () => {
  beforeEach(async () => {
    originalFetch = globalThis.fetch;
    snapshotHosts = [];
    browserLoginRequests = [];
    browserLoginResponse = () => new Response(null, { status: 500 });
    vi.mocked(navigateToURL).mockClear();
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = requestURL(input);
      if (url.includes("/browser-login")) {
        browserLoginRequests.push({ url, body: await requestJSON(input, init) });
        return browserLoginResponse();
      }
      return Response.json({
        protocolVersion: 3,
        generation: 1,
        hosts: snapshotHosts,
        projects: [],
        worktrees: [],
        sessions: [],
        workspaces: [],
      });
    });
    await page.viewport(1280, 900);
  });

  afterEach(() => {
    unmount?.();
    unmount = undefined;
    globalThis.fetch = originalFetch;
    for (const flash of getFlashes()) dismissFlash(flash.id);
  });

  describe("switching Forge", () => {
    beforeEach(() => {
      snapshotHosts = [
        host("spoke-a", "Current spoke", { kind: "self" }),
        host("hub", "Hub", { federationRole: "hub", baseURL: "https://hub.example:8443" }),
      ];
    });

    it("signs in to the other Forge and opens the same page there", async () => {
      browserLoginResponse = () =>
        Response.json({
          url: "https://hub.example:8443/pulls?login_ticket=ticket-1",
          expires_at: "2026-09-22T12:01:00Z",
        });
      const hub = await openHubRow();
      hub.click();

      await vi.waitFor(() => {
        expect(navigateToURL).toHaveBeenCalledWith("https://hub.example:8443/pulls?login_ticket=ticket-1");
      });
      expect(browserLoginRequests).toHaveLength(1);
      expect(browserLoginRequests[0]?.url).toContain("/fleet/hosts/hub/browser-login");
      expect(browserLoginRequests[0]?.body).toEqual({ path: window.location.pathname + window.location.search });
    });

    it("ignores repeat clicks while a sign-in is in progress", async () => {
      let release: (response: Response) => void = () => {};
      const pending = new Promise<Response>((resolve) => {
        release = resolve;
      });
      globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestURL(input);
        if (url.includes("/browser-login")) {
          browserLoginRequests.push({ url, body: await requestJSON(input, init) });
          return pending;
        }
        return Response.json({
          protocolVersion: 3,
          generation: 1,
          hosts: snapshotHosts,
          projects: [],
          worktrees: [],
          sessions: [],
          workspaces: [],
        });
      });
      const hub = await openHubRow();
      hub.click();
      hub.click();
      await vi.waitFor(() => {
        expect(hub.getAttribute("aria-busy")).toBe("true");
      });
      release(
        Response.json({ url: "https://hub.example:8443/?login_ticket=ticket-2", expires_at: "2026-09-22T12:01:00Z" }),
      );

      await vi.waitFor(() => {
        expect(navigateToURL).toHaveBeenCalledTimes(1);
      });
      expect(browserLoginRequests).toHaveLength(1);
    });

    it("opens the plain address when this Forge has no direct credential", async () => {
      browserLoginResponse = () =>
        problemResponse(409, "conflict", "no active federation credential", "noDirectFederationCredential");
      const hub = await openHubRow();
      hub.click();

      await vi.waitFor(() => {
        expect(navigateToURL).toHaveBeenCalledWith("https://hub.example:8443");
      });
      expect(getFlashes()).toHaveLength(0);
    });

    it("reports other failures and stays on this Forge", async () => {
      browserLoginResponse = () =>
        problemResponse(502, "upstreamError", "fleet peer browser login failed: peer returned HTTP 503");
      const hub = await openHubRow();
      hub.click();

      await vi.waitFor(() => {
        expect(getFlashes().map((flash) => flash.message)).toContain(
          "fleet peer browser login failed: peer returned HTTP 503",
        );
      });
      expect(navigateToURL).not.toHaveBeenCalled();
      await vi.waitFor(() => {
        expect(hub.getAttribute("aria-busy")).toBeNull();
      });
      hub.click();
      await vi.waitFor(() => {
        expect(browserLoginRequests).toHaveLength(2);
      });
    });

    it.each([
      ["ctrl-click", { ctrlKey: true, button: 0 }],
      ["cmd-click", { metaKey: true, button: 0 }],
      ["shift-click", { shiftKey: true, button: 0 }],
      ["middle-click", { button: 1 }],
    ] as const)("keeps %s as an ordinary link", async (_label, modifiers) => {
      const hub = await openHubRow();
      let componentPrevented: boolean | undefined;
      const stopNavigation = (event: Event) => {
        componentPrevented = event.defaultPrevented;
        event.preventDefault();
      };
      window.addEventListener("click", stopNavigation);
      try {
        hub.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...modifiers }));
      } finally {
        window.removeEventListener("click", stopNavigation);
      }

      expect(componentPrevented).toBe(false);
      expect(browserLoginRequests).toHaveLength(0);
      expect(navigateToURL).not.toHaveBeenCalled();
    });
  });

  it("stays hidden for a one-host snapshot", async () => {
    snapshotHosts = [host("self", "Local", { kind: "self", federationRole: "hub" })];
    await renderSelector();

    await vi.waitFor(() => {
      expect(globalThis.fetch).toHaveBeenCalled();
    });
    expect(document.querySelector(".forge-selector")).toBeNull();
  });

  it("orders the hub first and preserves ordinary target links", async () => {
    snapshotHosts = [
      host("spoke-a", "Current spoke", { kind: "self" }),
      host("spoke-b", "Offline spoke", {
        reachable: false,
        connectionState: "offline",
      }),
      host("hub", "Hub", {
        federationRole: "hub",
        baseURL: "https://hub.example:8443",
        connectionState: "degraded",
        error: "Spoke health check timed out",
      }),
    ];
    await renderSelector();
    await waitForDirectory();

    const trigger = page.getByLabelText("Current Forge: Current spoke");
    await trigger.click();
    const links = Array.from(document.querySelectorAll<HTMLAnchorElement>(".forge-selector li a"));
    expect(links.map((link) => link.querySelector("strong")?.textContent)).toEqual([
      "Hub",
      "Current spoke",
      "Offline spoke",
    ]);
    expect(links[0]?.getAttribute("href")).toBe("https://hub.example:8443");
    expect(links[0]?.getAttribute("target")).toBeNull();
    expect(links[0]?.getAttribute("onclick")).toBeNull();
    expect(links[0]?.textContent).toContain("Hub");
    expect(links[0]?.textContent).toContain("degraded");
    expect(links[0]?.textContent).toContain("Spoke health check timed out");
    expect(links[1]?.textContent).toContain("Current");
    expect(links[1]?.textContent).toContain("online");
    expect(links[2]?.textContent).toContain("offline");
  });

  it("closes before another header control opens", async () => {
    snapshotHosts = [host("spoke-a", "Current spoke", { kind: "self" }), host("hub", "Hub", { federationRole: "hub" })];
    const outside = document.createElement("button");
    outside.textContent = "Other header control";
    document.body.append(outside);

    try {
      await renderSelector();
      await waitForDirectory();

      const trigger = page.getByLabelText("Current Forge: Current spoke");
      await trigger.click();
      await expect.element(page.getByRole("list", { name: "Forge fleet" })).toBeVisible();

      await page.getByRole("button", { name: "Other header control" }).click();
      await vi.waitFor(() => {
        expect(document.querySelector<HTMLDetailsElement>(".forge-selector")?.open).toBe(false);
      });
    } finally {
      outside.remove();
    }
  });

  it.each([1280, 375])("distinguishes devboxes from fleet nodes at %ipx", async (width) => {
    await page.viewport(width, 700);
    snapshotHosts = [
      host("devbox:compute-a", "Compute A", { kind: "devbox", federationRole: "devbox", baseURL: "" }),
      host("spoke-a", "Build node"),
      host("hub", "Main Forge", { kind: "self", federationRole: "hub" }),
      host("devbox:compute-b", "Compute B", {
        kind: "devbox",
        federationRole: "devbox",
        baseURL: "",
        reachable: false,
      }),
    ];
    await renderSelector({ compact: width < 640 });
    await waitForDirectory();
    await page.getByLabelText("Current Forge: Main Forge").click();

    const rows = page.getByRole("listitem");
    await expect.element(rows.filter({ hasText: "Main Forge" })).toHaveTextContent("Hub");
    await expect.element(rows.filter({ hasText: "Build node" })).toHaveTextContent("Spoke");
    await expect.element(rows.filter({ hasText: "Compute A" })).toHaveTextContent("Devbox");
    await expect.element(rows.filter({ hasText: "Compute A" })).toHaveTextContent("online");
    await expect.element(rows.filter({ hasText: "Compute B" })).toHaveTextContent("offline");
    expect(document.querySelectorAll(".forge-selector li a")).toHaveLength(2);
    const menu = page.getByRole("list", { name: "Forge fleet" }).element().getBoundingClientRect();
    expect(menu.right).toBeLessThanOrEqual(width);
  });

  it("removes hosts omitted by a later authoritative snapshot", async () => {
    const hub = host("hub", "Hub", {
      federationRole: "hub",
    });
    const current = host("spoke-a", "Current spoke", { kind: "self" });
    const removed = host("spoke-b", "Removed spoke");
    snapshotHosts = [current, hub, removed];
    await renderSelector();
    await waitForDirectory();

    const trigger = page.getByLabelText("Current Forge: Current spoke");
    await trigger.click();
    await expect.element(page.getByText("Removed spoke", { exact: true })).toBeVisible();
    await trigger.click();

    snapshotHosts = [current, hub];
    await trigger.click();
    await vi.waitFor(() => {
      expect(document.body.textContent).not.toContain("Removed spoke");
    });
  });

  it("fits the compact selector within a phone viewport", async () => {
    await page.viewport(375, 700);
    snapshotHosts = [
      host("spoke-a", "Current spoke with a long name", { kind: "self" }),
      host("hub", "Hub with a long name", {
        federationRole: "hub",
      }),
    ];
    await renderSelector({ compact: true, fallbackLabel: "kenn-forge" });
    await waitForDirectory();

    const trigger = page.getByLabelText("Current Forge: Current spoke with a long name");
    await trigger.click();
    const triggerBox = trigger.element().getBoundingClientRect();
    const menuBox = document.querySelector(".forge-selector ul")?.getBoundingClientRect();
    expect(triggerBox.left).toBeGreaterThanOrEqual(0);
    expect(triggerBox.right).toBeLessThanOrEqual(375);
    expect(menuBox).toBeDefined();
    expect(menuBox?.left).toBeGreaterThanOrEqual(0);
    expect(menuBox?.right).toBeLessThanOrEqual(375);
  });
});
