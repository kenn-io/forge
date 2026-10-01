import { afterEach, expect, it } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";
import "../../../app.css";
import TabbedPanelTreeTestHarness from "./TabbedPanelTreeTestHarness.svelte";
import { activateTabbedPanelTab, type TabbedPanelNode } from "./tabbed-panel-layout.js";

afterEach(cleanup);

it("keeps the selected tab's top highlight through initial focus and tab switches", async () => {
  let node: TabbedPanelNode = {
    type: "split",
    id: "split-1",
    direction: "horizontal",
    ratio: 0.5,
    first: { type: "leaf", id: "leaf-1", tabs: ["feed", "detail"], activeTabKey: "detail" },
    second: { type: "leaf", id: "leaf-2", tabs: ["files"], activeTabKey: "files" },
  };
  const view = await render(TabbedPanelTreeTestHarness, {
    node,
    activeTabKey: "detail",
    onSelectTab: async (tabKey: string) => {
      node = activateTabbedPanelTab(node, tabKey)!;
      await view.rerender({ node, activeTabKey: tabKey });
    },
  });

  // Selection remains visible in both the focused and unfocused split panes.
  // Moving focus must not replace a tab's selection marker with a pane border.
  for (const label of [null, "Feed, Feed updating", "Files, Files need attention", "Detail"]) {
    if (label) {
      const selectedTab = view.getByRole("tab", { name: label, exact: true });
      await selectedTab.click();
      await expect.element(selectedTab).toHaveAttribute("aria-selected", "true");
    }
    for (const tab of document.querySelectorAll(".tabbed-panel-tab")) {
      const selected = tab.querySelector("[role='tab']")?.getAttribute("aria-selected") === "true";
      const highlight = getComputedStyle(tab, "::before");
      if (selected) {
        expect(highlight.content).toBe('""');
        expect(parseFloat(highlight.height)).toBeGreaterThan(0);
        expect(highlight.backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
      } else {
        expect(highlight.content).toBe("none");
      }
    }
  }
});
