import { describe, expect, it } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import type { KataCurrentView } from "../../features/kata/kataWorkspaceAuthority.js";
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

function currentView(issues: readonly KataTaskSummary[]): KataCurrentView {
  return {
    name: "today",
    fetched_at: "2026-05-16T10:00:00Z",
    groups: [{ id: "today", title: "Today", issues: [...issues] }],
  };
}

describe("KataIssueList table geometry (browser)", () => {
  it("opens the column picker from the keyboard and restores focus on Escape", async () => {
    await page.viewport(980, 620);
    const issue = task();
    render(KataIssueList, {
      props: {
        currentView: currentView(issue),
        selectedIssueUID: issue.uid,
        loading: false,
        onSelect: () => {},
      },
    });

    const trigger = page.getByRole("button", { name: "Columns" });
    (trigger.element() as HTMLButtonElement).focus();
    await userEvent.keyboard("{Enter}");
    await expect.element(trigger).toHaveAttribute("aria-expanded", "true");
    const updated = page.getByRole("checkbox", { name: "Updated" });
    await expect.element(updated).toBeVisible();

    await userEvent.keyboard("{Escape}");

    await expect.element(trigger).toHaveAttribute("aria-expanded", "false");
    await expect.element(trigger).toHaveFocus();
  });

  it("paints the header and selected row flush with the table pane", async () => {
    await page.viewport(980, 620);

    const issue = task();
    const { container } = render(KataIssueList, {
      props: {
        currentView: currentView([issue]),
        issueCatalog: [issue],
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

  it("keeps a visible selected row fixed when a refreshed snapshot inserts a row above it", async () => {
    await page.viewport(980, 620);

    const issues = Array.from({ length: 20 }, (_, index) =>
      task({
        id: index + 1,
        uid: `issue-${index + 1}`,
        short_id: `task-${index + 1}`,
        qualified_id: `General#task-${index + 1}`,
        title: `Task ${index + 1}`,
        updated_at: `2026-05-${String(30 - index).padStart(2, "0")}T08:00:00Z`,
      }),
    );
    const selected = issues[8]!;
    const { container, rerender } = render(KataIssueList, {
      props: {
        currentView: currentView(issues),
        issueCatalog: issues,
        selectedIssueUID: selected.uid,
        loading: false,
        onSelect: () => {},
      },
    });

    const list = container.querySelector(".issue-list") as HTMLElement;
    list.style.width = "840px";
    list.style.height = "240px";

    const tableBody = container.querySelector(".table-body") as HTMLDivElement;
    tableBody.style.overflowAnchor = "none";
    await expect.poll(() => tableBody.scrollHeight > tableBody.clientHeight).toBe(true);

    const selectedRow = container.querySelector<HTMLElement>(`button.row[data-uid="${selected.uid}"]`)!;
    let tableRect = tableBody.getBoundingClientRect();
    tableBody.scrollTop += selectedRow.getBoundingClientRect().top - tableRect.top - 72;
    tableRect = tableBody.getBoundingClientRect();
    const selectedTop = selectedRow.getBoundingClientRect().top;
    selectedRow.focus();
    expect(selectedTop).toBeGreaterThanOrEqual(tableRect.top);
    expect(selectedRow.getBoundingClientRect().bottom).toBeLessThanOrEqual(tableRect.bottom);
    expect(document.activeElement).toBe(selectedRow);

    const inserted = task({
      id: 100,
      uid: "issue-inserted",
      short_id: "inserted",
      qualified_id: "General#inserted",
      title: "Inserted task",
      updated_at: "2026-05-31T08:00:00Z",
    });
    const refreshedIssues = [inserted, ...issues];
    await rerender({
      currentView: currentView(refreshedIssues),
      issueCatalog: refreshedIssues,
      selectedIssueUID: selected.uid,
      loading: false,
      onSelect: () => {},
    });

    const refreshedSelectedRow = container.querySelector<HTMLElement>(`button.row[data-uid="${selected.uid}"]`)!;
    expect(Math.round(refreshedSelectedRow.getBoundingClientRect().top)).toBe(Math.round(selectedTop));
    expect(document.activeElement).toBe(refreshedSelectedRow);
  });

  it("keeps a newly selected visible row fixed when its accepted snapshot inserts a row above it", async () => {
    await page.viewport(980, 620);

    const issues = Array.from({ length: 20 }, (_, index) =>
      task({
        id: index + 1,
        uid: `issue-${index + 1}`,
        short_id: `task-${index + 1}`,
        qualified_id: `General#task-${index + 1}`,
        title: `Task ${index + 1}`,
        updated_at: `2026-05-${String(30 - index).padStart(2, "0")}T08:00:00Z`,
      }),
    );
    const previouslySelected = issues[7]!;
    const nextSelected = issues[8]!;
    const { container, rerender } = render(KataIssueList, {
      props: {
        currentView: currentView(issues),
        issueCatalog: issues,
        selectedIssueUID: previouslySelected.uid,
        loading: false,
        onSelect: () => {},
      },
    });

    const list = container.querySelector(".issue-list") as HTMLElement;
    list.style.width = "840px";
    list.style.height = "240px";

    const tableBody = container.querySelector(".table-body") as HTMLDivElement;
    tableBody.style.overflowAnchor = "none";
    await expect.poll(() => tableBody.scrollHeight > tableBody.clientHeight).toBe(true);

    const previouslySelectedRow = container.querySelector<HTMLElement>(
      `button.row[data-uid="${previouslySelected.uid}"]`,
    )!;
    const nextSelectedRow = container.querySelector<HTMLElement>(`button.row[data-uid="${nextSelected.uid}"]`)!;
    let tableRect = tableBody.getBoundingClientRect();
    tableBody.scrollTop += nextSelectedRow.getBoundingClientRect().top - tableRect.top - 72;
    tableRect = tableBody.getBoundingClientRect();
    const nextSelectedTop = nextSelectedRow.getBoundingClientRect().top;
    previouslySelectedRow.focus();
    expect(nextSelectedTop).toBeGreaterThanOrEqual(tableRect.top);
    expect(nextSelectedRow.getBoundingClientRect().bottom).toBeLessThanOrEqual(tableRect.bottom);
    expect(document.activeElement).toBe(previouslySelectedRow);

    const inserted = task({
      id: 100,
      uid: "issue-new-selection-inserted",
      short_id: "new-selection-inserted",
      qualified_id: "General#new-selection-inserted",
      title: "New selection inserted task",
      updated_at: "2026-05-31T08:00:00Z",
    });
    const refreshedIssues = [inserted, ...issues];
    await rerender({
      currentView: currentView(refreshedIssues),
      issueCatalog: refreshedIssues,
      selectedIssueUID: nextSelected.uid,
      loading: false,
      onSelect: () => {},
    });

    const refreshedNextSelectedRow = container.querySelector<HTMLElement>(
      `button.row[data-uid="${nextSelected.uid}"]`,
    )!;
    expect(Math.round(refreshedNextSelectedRow.getBoundingClientRect().top)).toBe(Math.round(nextSelectedTop));
    expect(document.activeElement).toBe(previouslySelectedRow);
  });
});
