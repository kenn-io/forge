import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataTaskAPI, KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import type { KataCurrentView } from "../../stores/kata-workspace.svelte.js";
import KataIssueList from "./KataIssueList.svelte";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const baseIssues: KataTaskSummary[] = [
  task({
    id: 101,
    uid: "issue-pay-rent",
    project_id: 2,
    project_uid: "project-finances",
    short_id: "pay-rent",
    qualified_id: "Finances#pay-rent",
    title: "Pay rent",
    project_name: "Finances",
    owner: "fixture-user",
    priority: 0,
    labels: ["home", "monthly"],
    updated_at: "2026-05-14T08:00:00Z",
    metadata: { deadline_on: "2026-05-15" },
  }),
  task({
    id: 102,
    uid: "issue-email-susan",
    project_id: 3,
    project_uid: "project-work",
    short_id: "email-susan",
    qualified_id: "Work#email-susan",
    title: "Email Susan re: Q3",
    project_name: "Work",
    owner: "fixture-user",
    priority: 3,
    updated_at: "2026-05-16T08:00:00Z",
  }),
];

const currentView: KataCurrentView = {
  name: "today",
  fetched_at: "2026-05-16T10:00:00Z",
  groups: [
    {
      id: "overdue",
      title: "Overdue",
      issues: [baseIssues[0]!],
    },
    {
      id: "today",
      title: "Today",
      issues: [baseIssues[1]!],
    },
  ],
};

describe("KataIssueList", () => {
  afterEach(() => {
    cleanup();
    window.localStorage.clear();
    vi.restoreAllMocks();
  });

  it("renders the heading, table columns, and the selected row metadata", () => {
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: "issue-pay-rent",
        loading: false,
        onSelect: () => {},
      },
    });

    expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Sort by Priority/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Sort by Updated/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Sort by Title/ })).toBeTruthy();

    const row = screen.getByRole("button", {
      name: (name) =>
        name.includes("Pay rent") &&
        name.includes("Finances#pay-rent") &&
        name.includes("project: Finances") &&
        name.includes("owner: fixture-user") &&
        name.includes("priority: 0") &&
        name.includes("home · monthly"),
    });
    expect(row.getAttribute("aria-current")).toBe("true");
    expect(row.classList.contains("selected")).toBe(true);
    expect(within(row).getByText("Pay rent")).toBeTruthy();
    expect(within(row).getByText("Finances#pay-rent")).toBeTruthy();
    expect(within(row).getByText("P0")).toBeTruthy();
    expect(within(row).getByText("home · monthly")).toBeTruthy();
    expect(within(row).getByText("fixture-user")).toBeTruthy();
  });

  it("keeps snapshot loading out of the visual layout", () => {
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: true,
        onSelect: () => {},
      },
    });

    const loading = screen.getByText("Loading snapshot");
    expect(loading.classList.contains("sr-only")).toBe(true);
    expect(screen.queryByText("Updating")).toBeNull();
  });

  it("keeps the header in the scrolling table and places Updated third", () => {
    const { container } = render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    const tableBody = container.querySelector(".table-body");
    const tableHeader = container.querySelector(".table-header");
    expect(tableBody?.contains(tableHeader)).toBe(true);

    const labels = Array.from(tableHeader?.querySelectorAll(".col") ?? []).map((el) => el.textContent?.trim());
    expect(labels.slice(0, 3)).toEqual(["ID", "Title", "Updated"]);
  });

  it("defaults flat lists to recently updated first", () => {
    render(KataIssueList, {
      props: {
        currentView: viewWithIssues(baseIssues),
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    expect(visibleRowTitles()).toEqual(["Email Susan re: Q3", "Pay rent"]);
  });

  it("clicking the Priority column header reorders rows by priority", async () => {
    render(KataIssueList, {
      props: {
        currentView: viewWithIssues(baseIssues),
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    expect(visibleRowTitles()).toEqual(["Email Susan re: Q3", "Pay rent"]);

    await fireEvent.click(screen.getByRole("button", { name: /Sort by Priority/ }));

    expect(visibleRowTitles()).toEqual(["Pay rent", "Email Susan re: Q3"]);

    await fireEvent.click(screen.getByRole("button", { name: /Sort by Priority/ }));

    expect(visibleRowTitles()).toEqual(["Email Susan re: Q3", "Pay rent"]);
  });

  it("clicking the Updated column header flips the default recency order", async () => {
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /Sort by Updated/ }));

    expect(visibleRowTitles()).toEqual(["Pay rent", "Email Susan re: Q3"]);
  });

  it("keeps grouped headings when sorting inside visible groups", async () => {
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /Sort by Priority/ }));

    expect(screen.getByRole("heading", { level: 3, name: /^Overdue\s+1$/ })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 3, name: /^Today\s+1$/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Sort by Priority, currently ascending/ }));

    expect(screen.getByRole("heading", { level: 3, name: /^Overdue\s+1$/ })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 3, name: /^Today\s+1$/ })).toBeTruthy();
  });

  it("hides child tasks from top-level rows and expands them on demand", async () => {
    const parent = task({
      uid: "issue-parent",
      short_id: "parent",
      qualified_id: "Finances#parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    });
    const child = task({
      uid: "issue-child",
      short_id: "child",
      qualified_id: "Finances#child",
      title: "Child task",
      parent_short_id: parent.short_id,
    });
    const api = apiWithDetail(parent, [child]);
    const selected: string[] = [];

    render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent, child]),
        selectedIssueUID: null,
        loading: false,
        api,
        onSelect: (issue: KataTaskSummary) => selected.push(issue.uid),
      },
    });

    expect(screen.getByText("Parent task")).toBeTruthy();
    expect(screen.queryByText("Child task")).toBeNull();

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });

    const childRow = await screen.findByRole("button", { name: /Child task/ });
    expect(childRow).toBeTruthy();
    expect(parentRow.getAttribute("aria-expanded")).toBe("true");

    parentRow.focus();
    await fireEvent.keyDown(parentRow, { key: "j" });
    expect(document.activeElement).toBe(childRow);
    expect(selected[selected.length - 1]).toBe("issue-child");

    await fireEvent.keyDown(parentRow, { key: "ArrowLeft" });
    await waitFor(() => {
      expect(parentRow.getAttribute("aria-expanded")).toBe("false");
    });
    expect(screen.queryByRole("button", { name: /Child task/ })).toBeNull();
  });

  it("j and k move focus and selection through rows", async () => {
    const selected: string[] = [];
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: false,
        onSelect: (issue: KataTaskSummary) => selected.push(issue.uid),
      },
    });

    const rows = visibleRows();
    rows[0]!.focus();
    await fireEvent.keyDown(rows[0]!, { key: "j" });
    expect(document.activeElement).toBe(rows[1]);
    expect(selected[selected.length - 1]).toBe(rows[1]!.dataset.uid);

    await fireEvent.keyDown(rows[1]!, { key: "k" });
    expect(document.activeElement).toBe(rows[0]);
    expect(selected[selected.length - 1]).toBe(rows[0]!.dataset.uid);
  });

  it("Home and End jump to first and last rows", async () => {
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    const rows = visibleRows();
    rows[0]!.focus();
    await fireEvent.keyDown(rows[0]!, { key: "End" });
    expect(document.activeElement).toBe(rows[rows.length - 1]);

    await fireEvent.keyDown(rows[rows.length - 1]!, { key: "Home" });
    expect(document.activeElement).toBe(rows[0]);
  });

  it("resets expanded child rows when resetGeneration changes", async () => {
    const parent = task({
      uid: "issue-parent",
      short_id: "parent",
      qualified_id: "Finances#parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    });
    const child = task({
      uid: "issue-child",
      short_id: "child",
      qualified_id: "Finances#child",
      title: "Child task",
      parent_short_id: parent.short_id,
    });
    const api = apiWithDetail(parent, [child]);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent]),
        selectedIssueUID: null,
        loading: false,
        resetGeneration: 0,
        api,
        onSelect: () => {},
      },
    });

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });
    expect(await screen.findByRole("button", { name: /Child task/ })).toBeTruthy();

    await rerender({
      currentView: viewWithIssues([parent]),
      selectedIssueUID: null,
      loading: false,
      resetGeneration: 1,
      api,
      onSelect: () => {},
    });

    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Child task/ })).toBeNull();
    });
  });

  it("keeps expanded child rows across live refreshes until resetGeneration changes", async () => {
    const parent = task({
      uid: "issue-parent",
      short_id: "parent",
      qualified_id: "Finances#parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    });
    const child = task({
      uid: "issue-child",
      short_id: "child",
      qualified_id: "Finances#child",
      title: "Child task",
      parent_short_id: parent.short_id,
    });
    const api = apiWithDetail(parent, [child]);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent]),
        selectedIssueUID: null,
        loading: false,
        resetGeneration: 0,
        api,
        onSelect: () => {},
      },
    });

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });
    expect(await screen.findByRole("button", { name: /Child task/ })).toBeTruthy();

    await rerender({
      currentView: {
        ...viewWithIssues([{ ...parent, updated_at: "2026-05-17T08:00:00Z" }]),
        fetched_at: "2026-05-17T10:00:00Z",
      },
      selectedIssueUID: null,
      loading: false,
      resetGeneration: 0,
      api,
      onSelect: () => {},
    });

    expect(screen.getByRole("button", { name: /Child task/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Parent task/ }).getAttribute("aria-expanded")).toBe("true");
  });

  it("ignores stale child loads that finish after the list resets", async () => {
    const parent = task({
      uid: "issue-parent",
      short_id: "parent",
      qualified_id: "Finances#parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    });
    const staleChild = task({
      uid: "issue-stale-child",
      short_id: "stale-child",
      qualified_id: "Finances#stale-child",
      title: "Stale child",
      parent_short_id: parent.short_id,
    });
    const freshChild = task({
      uid: "issue-fresh-child",
      short_id: "fresh-child",
      qualified_id: "Finances#fresh-child",
      title: "Fresh child",
      parent_short_id: parent.short_id,
    });
    const staleDetail = deferred<KataTaskDetail>();
    const api = {
      issue: vi
        .fn()
        .mockImplementationOnce(() => staleDetail.promise)
        .mockResolvedValue(apiDetail(parent, [freshChild])),
    } as unknown as KataTaskAPI;

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent]),
        selectedIssueUID: null,
        loading: false,
        resetGeneration: 0,
        api,
        onSelect: () => {},
      },
    });

    await fireEvent.keyDown(screen.getByRole("button", { name: /Parent task/ }), { key: "ArrowRight" });
    await rerender({
      currentView: viewWithIssues([{ ...parent, updated_at: "2026-05-17T08:00:00Z" }]),
      selectedIssueUID: null,
      loading: false,
      resetGeneration: 1,
      api,
      onSelect: () => {},
    });

    staleDetail.resolve(apiDetail(parent, [staleChild]));

    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Stale child/ })).toBeNull();
    });

    await fireEvent.keyDown(screen.getByRole("button", { name: /Parent task/ }), { key: "ArrowRight" });
    expect(await screen.findByRole("button", { name: /Fresh child/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Stale child/ })).toBeNull();
  });
});

function visibleRows(): HTMLElement[] {
  return screen
    .getAllByRole("button")
    .filter((row): row is HTMLElement => row instanceof HTMLElement && row.classList.contains("row"));
}

function visibleRowTitles(): string[] {
  return visibleRows()
    .filter((row) => !row.classList.contains("row--child"))
    .map((row) => row.querySelector(".title-text")?.textContent?.trim() ?? "");
}

function viewWithIssues(issues: KataTaskSummary[]): KataCurrentView {
  return {
    name: "all",
    fetched_at: "2026-05-16T10:00:00Z",
    groups: [{ id: "all", title: "All Open", issues }],
  };
}

function apiWithDetail(issue: KataTaskSummary, children: KataTaskSummary[]): KataTaskAPI {
  return {
    issue: vi.fn(async () => apiDetail(issue, children)),
  } as unknown as KataTaskAPI;
}

function apiDetail(issue: KataTaskSummary, children: KataTaskSummary[]): KataTaskDetail {
  return {
    issue: { ...issue, body: "" },
    comments: [],
    labels: [],
    links: [],
    children,
  };
}

function task(overrides: Partial<KataTaskSummary>): KataTaskSummary {
  return {
    id: 1,
    uid: "issue-uid",
    project_id: 2,
    project_uid: "project-finances",
    short_id: "task",
    qualified_id: "Finances#task",
    title: "Task",
    status: "open",
    project_name: "Finances",
    metadata: {},
    revision: 1,
    author: "fixture-user",
    owner: undefined,
    priority: undefined,
    labels: [],
    created_at: "2026-05-10T08:00:00Z",
    updated_at: "2026-05-15T08:00:00Z",
    ...overrides,
  };
}
