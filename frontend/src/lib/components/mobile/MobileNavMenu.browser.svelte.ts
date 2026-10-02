import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { HostSummary } from "../../api/fleet-snapshot.js";
import type { Page } from "../../stores/router.svelte.js";
import MobileNavMenuTestHarness from "./MobileNavMenuTestHarness.svelte";

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
    baseURL: `https://${name.toLowerCase().replace(/\s+/g, "-")}.example`,
    platform: "linux",
    preferredTransport: "http",
    reachable: true,
    diagnostics: [],
    operationAvailability: {},
    tmuxSessions: [],
    ...options,
  };
}

async function openMenu(currentPage: Page = "mobile-workspaces"): Promise<HTMLElement> {
  const view = await render(MobileNavMenuTestHarness, {
    props: {
      nav: {
        page: () => currentPage,
        isModeVisible: () => true,
        onNavigate: vi.fn(),
        onDesktopView: vi.fn(),
      },
    },
  });
  unmount = view.unmount;
  await page.getByRole("button", { name: "Menu" }).click();
  return vi.waitFor(() => {
    const panel = document.querySelector<HTMLElement>(".kit-modal-panel");
    expect(panel?.querySelector(".mobile-nav-sheet")).not.toBeNull();
    return panel!;
  });
}

// The element a tap at the center of `node` would reach; a clipped or covered
// control is not the topmost element at its own center.
function reachable(node: Element): boolean {
  const box = node.getBoundingClientRect();
  const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
  return hit !== null && (hit === node || node.contains(hit));
}

describe("MobileNavMenu sheet (browser)", () => {
  beforeEach(() => {
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
  });

  afterEach(() => {
    unmount?.();
    unmount = undefined;
    globalThis.fetch = originalFetch;
  });

  it.each([
    { width: 390, height: 844 },
    { width: 390, height: 480 },
  ])("opens as a bottom sheet of mode tiles at $width x $height", async ({ width, height }) => {
    await page.viewport(width, height);
    const panel = await openMenu("mobile-workspaces");

    const panelBox = panel.getBoundingClientRect();
    expect(Math.abs(panelBox.bottom - window.innerHeight)).toBeLessThanOrEqual(1);
    expect(panelBox.top).toBeGreaterThanOrEqual(0);
    expect(panelBox.left).toBeGreaterThanOrEqual(0);
    expect(panelBox.right).toBeLessThanOrEqual(window.innerWidth);

    const tiles = Array.from(panel.querySelectorAll<HTMLElement>("nav[aria-label='Phone mode'] button"));
    expect(tiles.map((tile) => tile.textContent?.trim())).toEqual(["Activity", "PRs", "Issues", "Workspaces"]);
    for (const tile of tiles) expect(tile.querySelector("svg")).not.toBeNull();
    const [activity, pulls, issues] = tiles.map((tile) => tile.getBoundingClientRect());
    expect(Math.abs(activity!.top - pulls!.top)).toBeLessThanOrEqual(1);
    expect(pulls!.left).toBeGreaterThan(activity!.right);
    expect(issues!.top).toBeGreaterThanOrEqual(activity!.bottom);

    const current = tiles.find((tile) => tile.getAttribute("aria-current") === "page")!;
    expect(current.textContent?.trim()).toBe("Workspaces");
    expect(getComputedStyle(current).backgroundColor).not.toBe(getComputedStyle(tiles[0]!).backgroundColor);
    expect(getComputedStyle(current).borderTopColor).not.toBe(getComputedStyle(tiles[0]!).borderTopColor);

    const desktop = page.getByRole("button", { name: "Open desktop view" }).element() as HTMLElement;
    const desktopBox = desktop.getBoundingClientRect();
    const lastTile = tiles.at(-1)!.getBoundingClientRect();
    expect(desktopBox.top).toBeGreaterThanOrEqual(lastTile.bottom);
    expect(Math.abs(desktopBox.left - activity!.left)).toBeLessThanOrEqual(1);
    expect(Math.abs(desktopBox.right - lastTile.right)).toBeLessThanOrEqual(1);

    // Every control stays tappable even when the short viewport makes the
    // sheet scroll.
    for (const control of [...tiles, desktop]) {
      control.scrollIntoView({ block: "nearest" });
      expect(reachable(control)).toBe(true);
    }
  });

  it("hides the Forge row when there is no other Forge to switch to", async () => {
    await page.viewport(390, 844);
    snapshotHosts = [host("self", "This Forge", { kind: "self" })];
    const panel = await openMenu();
    await vi.waitFor(() => expect(globalThis.fetch).toHaveBeenCalled());

    const forgeRow = panel.querySelector<HTMLElement>(".mobile-nav-sheet__forge")!;
    expect(getComputedStyle(forgeRow).display).toBe("none");
  });

  it("switches Forge from inside the sheet when the fleet has another Forge", async () => {
    await page.viewport(390, 844);
    snapshotHosts = [host("self", "This Forge", { kind: "self" }), host("hub", "Hub Forge", { federationRole: "hub" })];
    await openMenu();

    const trigger = page.getByLabelText("Current Forge: This Forge");
    await expect.element(trigger).toBeVisible();
    await trigger.click();

    const links = await vi.waitFor(() => {
      const found = Array.from(document.querySelectorAll<HTMLAnchorElement>(".mobile-nav-sheet .forge-selector li a"));
      expect(found).toHaveLength(2);
      return found;
    });
    const hub = links.find((link) => link.textContent?.includes("Hub Forge"))!;
    expect(hub.getAttribute("href")).toBe("https://hub-forge.example");
    for (const link of links) {
      const box = link.getBoundingClientRect();
      expect(box.left).toBeGreaterThanOrEqual(0);
      expect(box.right).toBeLessThanOrEqual(window.innerWidth);
      expect(box.bottom).toBeLessThanOrEqual(window.innerHeight);
      expect(reachable(link)).toBe(true);
    }
  });
});
