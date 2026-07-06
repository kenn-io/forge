import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import type { KataCurrentView } from "../../stores/kata-workspace.svelte.js";
import KataIssueList from "./KataIssueList.svelte";

function task(overrides: Partial<KataTaskSummary> = {}): KataTaskSummary {
  return {
    id: 1,
    uid: "issue-alignment",
    project_id: 2,
    project_uid: "project-general",
    short_id: "align",
    qualified_id: "General#align",
    title: "Scan pane alignment",
    status: "open",
    project_name: "General",
    metadata: {},
    revision: 1,
    author: "fixture-user",
    owner: "fixture-user",
    priority: 2,
    labels: ["layout"],
    created_at: "2026-05-10T08:00:00Z",
    updated_at: "2026-05-15T08:00:00Z",
    ...overrides,
  };
}

function currentView(issue: KataTaskSummary): KataCurrentView {
  return {
    name: "today",
    fetched_at: "2026-05-16T10:00:00Z",
    groups: [{ id: "today", title: "Today", issues: [issue] }],
  };
}

describe("KataIssueList table geometry (browser)", () => {
  it("paints the header and selected row flush with the table pane", async () => {
    await page.viewport(980, 620);

    const issue = task();
    const { container } = render(KataIssueList, {
      props: {
        currentView: currentView(issue),
        selectedIssueUID: issue.uid,
        loading: false,
        onSelect: () => {},
      },
    });

    const list = container.querySelector(".issue-list") as HTMLElement | null;
    expect(list).not.toBeNull();
    list!.style.width = "840px";
    list!.style.height = "420px";

    await expect.element(page.getByRole("button", { name: /Scan pane alignment/ })).toBeVisible();

    const table = container.querySelector(".table") as HTMLElement | null;
    const header = container.querySelector(".table-header") as HTMLElement | null;
    const selectedRow = container.querySelector(".row.selected") as HTMLElement | null;
    expect(table).not.toBeNull();
    expect(header).not.toBeNull();
    expect(selectedRow).not.toBeNull();

    const tableRect = table!.getBoundingClientRect();
    const headerRect = header!.getBoundingClientRect();
    const rowRect = selectedRow!.getBoundingClientRect();

    expect(Math.round(headerRect.left - tableRect.left)).toBe(0);
    expect(Math.round(tableRect.right - headerRect.right)).toBe(0);
    expect(Math.round(rowRect.left - tableRect.left)).toBe(0);
    expect(Math.round(tableRect.right - rowRect.right)).toBe(0);
  });
});
