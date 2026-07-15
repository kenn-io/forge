import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import RepoPreviewTable from "./RepoPreviewTable.svelte";
import { applyRangeSelection, rowKey, type RepoImportRow, type SortState } from "./repoImportSelection.js";

const rows: RepoImportRow[] = ["api", "web", "worker"].map((name) => ({
  provider: "github",
  platform_host: "github.com",
  owner: "acme",
  name,
  repo_path: `acme/${name}`,
  description: `${name} repository`,
  private: false,
  fork: false,
  pushed_at: "2026-07-15T12:00:00Z",
  already_configured: false,
}));

const sort: SortState = { field: "name", direction: "asc" };

describe("RepoPreviewTable (browser)", () => {
  it("forwards Shift-click so the caller can select the visible range", async () => {
    let selected = new Set<string>();
    let anchorKey: string | null = null;

    const props = () => ({
      rows,
      selected,
      filterText: "",
      statusFilter: "all" as const,
      hideForks: false,
      hidePrivate: false,
      sort,
      onFilterText: () => {},
      onStatusFilter: () => {},
      onHideForks: () => {},
      onHidePrivate: () => {},
      onSort: () => {},
      onToggle: (row: RepoImportRow, checked: boolean, shiftKey: boolean) => {
        const clickedKey = rowKey(row);
        if (shiftKey) {
          const result = applyRangeSelection({ selected, visibleRows: rows, anchorKey, clickedKey, checked });
          selected = result.selected;
          anchorKey = result.anchorKey;
          return;
        }
        selected = new Set(selected);
        if (checked) selected.add(clickedKey);
        else selected.delete(clickedKey);
        anchorKey = clickedKey;
      },
      onSelectVisible: () => {},
      onDeselectVisible: () => {},
    });

    const { rerender } = render(RepoPreviewTable, { props: props() });
    const first = page.getByRole("checkbox", { name: "Select acme/api" });
    const third = page.getByRole("checkbox", { name: "Select acme/worker" });

    await first.click();
    await rerender(props());
    await expect.element(first).toBeChecked();

    await third.click({ modifiers: ["Shift"] });
    await rerender(props());

    for (const row of rows) {
      await expect.element(page.getByRole("checkbox", { name: `Select ${row.repo_path}` })).toBeChecked();
    }
  });
});
