// Browser-tier migration of the panel-layout portion of
// frontend/tests/e2e/design-system.spec.ts ("design system page renders panel
// and typeahead examples"). The selected-tab chrome geometry, the split
// divider metrics, and the scroll-overflow behavior all come from the real
// TabbedPanelTree UI primitive (plain props, no stores, no backend), which the
// shipped DesignSystemTabbedPanelDemo configures. They are migrated here by
// mounting that real primitive with the demo's exact node tree / tabs / labels
// (DesignSystemPanelHarness) in Chromium and asserting the same
// getComputedStyle / getBoundingClientRect / scroll facts the e2e pinned.
//
// Deliberately NOT migrated from that e2e (and left to the original Playwright
// spec): the drag-to-split / drag-to-reorder mutations (they need real pointer
// drag + DataTransfer choreography against the live layout) and the
// RepoTypeahead dropdown states (RepoTypeahead reads getStores() and the API
// client, i.e. app-shell context). See the migration report notes.

import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

// app.css carries --chrome-border-width, --chrome-active-accent-width, and
// --chrome-pane-divider-width that the panel chrome resolves against.
import "../../../app.css";

import DesignSystemPanelHarness from "./DesignSystemPanelHarness.svelte";

describe("design system tabbed panel demo (browser)", () => {
  it("renders selected-tab chrome and split divider with real metrics", async () => {
    const { container } = render(DesignSystemPanelHarness);

    const assert = expect;

    const demo = container.querySelector('[data-testid="design-system-tabbed-panel-demo"]');
    assert(demo).not.toBeNull();

    await expect.element(page.getByTestId("design-system-panel-overview")).toBeVisible();

    // Selected tab seam: the active tab sits flush against the body, pulling
    // its bottom border off and overlapping the tab-bar divider by 1px so the
    // active surface reads as continuous with the panel below it.
    const overviewTab = demo!.querySelector('[data-tabbed-panel-tab-key="overview"]') as HTMLElement;
    assert(overviewTab).not.toBeNull();
    const tabBar = overviewTab.parentElement as HTMLElement;
    assert(tabBar).not.toBeNull();

    const tabStyle = getComputedStyle(overviewTab);
    const tabBarStyle = getComputedStyle(tabBar);
    const tabBarDivider = getComputedStyle(tabBar, "::after");
    const tabRect = overviewTab.getBoundingClientRect();
    const tabBarRect = tabBar.getBoundingClientRect();

    assert(tabStyle.borderBottomWidth).toBe("0px");
    assert(tabStyle.marginBottom).toBe("-1px");
    assert(getComputedStyle(overviewTab, "::after").content).toBe("none");
    assert(tabBarStyle.borderBottomWidth).toBe("0px");
    assert(tabBarDivider.height).toBe("1px");
    assert(Math.round(tabRect.bottom - tabBarRect.bottom)).toBe(1);
    assert(tabStyle.zIndex).toBe("2");

    // Split divider: a 3px gutter with no inline padding; the two leaves drop
    // their facing borders so the seam is a single divider line.
    const divider = demo!.querySelector('[aria-label="Resize design system panel split"]') as HTMLElement;
    assert(divider).not.toBeNull();
    const split = divider.closest(".tabbed-panel-split");
    const firstLeaf = split?.querySelector(".tabbed-panel-split-child.first > .tabbed-panel-leaf") ?? null;
    const secondLeaf = split?.querySelector(".tabbed-panel-split-child.second > .tabbed-panel-leaf") ?? null;
    assert(firstLeaf).not.toBeNull();
    assert(secondLeaf).not.toBeNull();

    const dividerRect = divider.getBoundingClientRect();
    const dividerStyle = getComputedStyle(divider);
    const firstLeafStyle = getComputedStyle(firstLeaf as Element);
    const secondLeafStyle = getComputedStyle(secondLeaf as Element);

    assert(Math.round(dividerRect.width)).toBe(3);
    assert(`${dividerStyle.paddingLeft}/${dividerStyle.paddingRight}`).toBe("0px/0px");
    assert(firstLeafStyle.borderTopWidth).toBe("0px");
    assert(firstLeafStyle.borderRightWidth).toBe("0px");
    assert(secondLeafStyle.borderTopWidth).toBe("0px");
    assert(secondLeafStyle.borderLeftWidth).toBe("0px");
  });

  it("scrolls the active panel body when its content overflows", async () => {
    const { container } = render(DesignSystemPanelHarness);

    const assert = expect;

    // Activity has nine rows in a 300px panel, so its body must scroll.
    await page.getByRole("tab", { name: /Activity/ }).click();
    await expect.element(page.getByTestId("design-system-panel-activity")).toBeVisible();

    const article = container.querySelector('[data-testid="design-system-panel-activity"]') as HTMLElement;
    assert(article).not.toBeNull();
    const panel = article.parentElement as HTMLElement;
    assert(panel).not.toBeNull();

    panel.scrollTop = 0;
    const overflowY = getComputedStyle(panel).overflowY;
    const before = panel.scrollTop;
    panel.scrollTop = 48;

    assert(overflowY).toBe("auto");
    assert(panel.scrollHeight).toBeGreaterThan(panel.clientHeight);
    assert(panel.scrollTop).toBeGreaterThan(before);
  });
});
