import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { HostSummary } from "../../api/fleet-snapshot.js";
import ForgeSelectorRuntimeHarness from "./ForgeSelectorRuntimeHarness.svelte";

let snapshotHosts: HostSummary[] = [];
let originalFetch: typeof globalThis.fetch;
let unmount: (() => void) | undefined;

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

function renderSelector(props: { compact?: boolean; fallbackLabel?: string } = {}): void {
  const view = render(ForgeSelectorRuntimeHarness, { props });
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
    globalThis.fetch = vi.fn(async () =>
      Response.json({
        protocolVersion: 3,
        generation: 1,
        hosts: snapshotHosts,
        projects: [],
        worktrees: [],
        sessions: [],
        workspaces: [],
      }),
    );
    await page.viewport(1280, 900);
  });

  afterEach(() => {
    unmount?.();
    unmount = undefined;
    globalThis.fetch = originalFetch;
  });

  it("stays hidden for a one-host snapshot", async () => {
    snapshotHosts = [host("self", "Local", { kind: "self", federationRole: "hub" })];
    renderSelector();

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
    renderSelector();
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
      renderSelector();
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

  it("removes hosts omitted by a later authoritative snapshot", async () => {
    const hub = host("hub", "Hub", {
      federationRole: "hub",
    });
    const current = host("spoke-a", "Current spoke", { kind: "self" });
    const removed = host("spoke-b", "Removed spoke");
    snapshotHosts = [current, hub, removed];
    renderSelector();
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
    renderSelector({ compact: true, fallbackLabel: "kenn-forge" });
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
