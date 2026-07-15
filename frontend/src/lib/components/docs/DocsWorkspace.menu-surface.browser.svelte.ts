// Regression guard for the phantom --bg-elevated token: the docs menus
// carried kit-popover-card but a scoped background override referencing
// an undefined custom property computed to transparent and outranked the
// kit surface. The generic kit-popover-card contract test renders the
// bare class and cannot see scoped overrides, so this mounts the real
// menus with the real stylesheet chain and asserts their computed
// backgrounds resolve to the opaque shared surface.

import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import { defaultDocsRoute, type DocsRoute } from "../../api/docs/route";
import DocsWorkspace from "./DocsWorkspace.svelte";
import { createMockDocsBackend } from "./docsTestBackend";

function renderWorkspace(overrides: Partial<DocsRoute> = {}) {
  const route: DocsRoute = { ...defaultDocsRoute, ...overrides };
  return render(DocsWorkspace, {
    props: { route, onRouteChange: vi.fn(), api: createMockDocsBackend() },
  });
}

// Resolve what var(--bg-surface) computes to in the loaded theme so the
// assertions catch both a transparent fallthrough and a menu that drifts
// off the shared surface token.
function resolvedSurfaceColor(): string {
  const probe = document.createElement("div");
  probe.style.backgroundColor = "var(--bg-surface)";
  document.body.appendChild(probe);
  try {
    return getComputedStyle(probe).backgroundColor;
  } finally {
    probe.remove();
  }
}

describe("docs menu surfaces (browser)", () => {
  it("folder switcher menu computes to the opaque shared popover surface", async () => {
    renderWorkspace();

    const trigger = page.getByRole("combobox", { name: /^Switch folder:/ });
    await expect.element(trigger).toBeEnabled();
    await trigger.click();

    const menu = document.querySelector<HTMLElement>(".folder-select .kit-select-dropdown__list");
    expect(menu).not.toBeNull();
    const background = getComputedStyle(menu!).backgroundColor;
    expect(background).toMatch(/^rgb\(/);
    expect(background).toBe(resolvedSurfaceColor());
  });

  it("file actions menu computes to the opaque shared popover surface", async () => {
    renderWorkspace({ folder: "notes", doc: "README.md" });

    const trigger = page.getByRole("button", { name: "File actions" });
    await expect.element(trigger).toBeVisible();
    await trigger.click();

    const menu = page.getByRole("menu", { name: "File actions" });
    await expect.element(menu).toBeVisible();
    const background = getComputedStyle(menu.element()).backgroundColor;
    expect(background).toMatch(/^rgb\(/);
    expect(background).toBe(resolvedSurfaceColor());
  });
});
