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
    vi.useRealTimers();
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

  it("opens a graph from a row action without selecting the task", async () => {
    const onSelect = vi.fn();
    const onOpenGraph = vi.fn();
    render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: null,
        loading: false,
        onSelect,
        onOpenGraph,
      },
    });

    const row = screen.getByRole("button", { name: /Pay rent/ });
    const frame = row.parentElement;
    expect(frame).toBeTruthy();
    await fireEvent.click(within(frame!).getByRole("button", { name: "Open reachable graph" }));

    expect(onOpenGraph).toHaveBeenCalledWith(baseIssues[0]);
    expect(onSelect).not.toHaveBeenCalled();
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
    expect(loading.classList.contains("kit-sr-only")).toBe(true);
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
    const onRememberTasks = vi.fn();

    render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent, child]),
        selectedIssueUID: null,
        loading: false,
        api,
        onRememberTasks,
        onSelect: (issue: KataTaskSummary) => selected.push(issue.uid),
      },
    });

    expect(screen.getByText("Parent task")).toBeTruthy();
    expect(screen.queryByText("Child task")).toBeNull();
    expect(screen.getByText("2 tasks")).toBeTruthy();

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });

    const childRow = await screen.findByRole("button", { name: /Child task/ });
    expect(childRow).toBeTruthy();
    expect(parentRow.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("2 tasks")).toBeTruthy();
    const remembered = onRememberTasks.mock.calls[0]?.[0] as readonly KataTaskSummary[] | undefined;
    expect(remembered?.map((issue) => issue.uid)).toEqual([parent.uid, child.uid]);

    parentRow.focus();
    await fireEvent.keyDown(parentRow, { key: "j" });
    await fireEvent.keyUp(childRow, { key: "j" });
    expect(document.activeElement).toBe(childRow);
    await waitFor(() => {
      expect(selected[selected.length - 1]).toBe("issue-child");
    });

    await fireEvent.keyDown(parentRow, { key: "ArrowLeft" });
    await waitFor(() => {
      expect(parentRow.getAttribute("aria-expanded")).toBe("false");
    });
    expect(screen.queryByRole("button", { name: /Child task/ })).toBeNull();
  });

  it("does not show an expanded child again as a flat row", async () => {
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

    render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent, child]),
        selectedIssueUID: null,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    expect(screen.queryByRole("button", { name: /Child task/ })).toBeNull();

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });

    await waitFor(() => {
      expect(screen.getAllByRole("button", { name: /Child task/ })).toHaveLength(1);
    });
    expect(screen.getByRole("button", { name: /Child task/ }).classList.contains("row--child")).toBe(true);
  });

  it("renders a matched child as a top-level row when its parent is absent", async () => {
    // A search or filter can surface a child whose parent is not in the
    // result set. The child has a parent_short_id, but with no visible
    // ancestor to fold into it must still render as its own row instead of
    // being dropped — otherwise the header counts it while the list shows
    // "No tasks".
    const child = task({
      uid: "issue-child",
      short_id: "child",
      qualified_id: "Finances#child",
      title: "Child task",
      parent_short_id: "parent",
    });

    render(KataIssueList, {
      props: {
        currentView: viewWithIssues([child]),
        selectedIssueUID: null,
        loading: false,
        onSelect: () => {},
      },
    });

    expect(screen.getByRole("button", { name: /Child task/ })).toBeTruthy();
    expect(screen.queryByText("No tasks")).toBeNull();
    expect(screen.getByText("1 task")).toBeTruthy();
  });

  it("expands nested child rows beyond one level", async () => {
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
      child_counts: { open: 1, total: 1 },
      parent_short_id: parent.short_id,
    });
    const grandchild = task({
      uid: "issue-grandchild",
      short_id: "grandchild",
      qualified_id: "Finances#grandchild",
      title: "Grandchild task",
      parent_short_id: child.short_id,
    });
    const api = {
      issue: vi.fn(async (uid: string) => {
        if (uid === parent.uid) return apiDetail(parent, [child]);
        return apiDetail(child, [grandchild]);
      }),
    } as unknown as KataTaskAPI;

    render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent]),
        selectedIssueUID: null,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });

    const childRow = await screen.findByRole("button", { name: /Child task/ });
    expect(childRow.getAttribute("aria-expanded")).toBe("false");

    await fireEvent.keyDown(childRow, { key: "ArrowRight" });

    const grandchildRow = await screen.findByRole("button", { name: /Grandchild task/ });
    expect(grandchildRow.classList.contains("row--child")).toBe(true);
    expect(api.issue).toHaveBeenCalledWith(parent.uid);
    expect(api.issue).toHaveBeenCalledWith(child.uid);
  });

  it("expands and collapses every visible task tree from the header controls", async () => {
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
      child_counts: { open: 1, total: 1 },
      parent_short_id: parent.short_id,
    });
    const grandchild = task({
      uid: "issue-grandchild",
      short_id: "grandchild",
      qualified_id: "Finances#grandchild",
      title: "Grandchild task",
      parent_short_id: child.short_id,
    });
    const api = {
      issue: vi.fn(async (uid: string) => {
        if (uid === parent.uid) return apiDetail(parent, [child]);
        return apiDetail(child, [grandchild]);
      }),
    } as unknown as KataTaskAPI;

    render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent]),
        selectedIssueUID: null,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    const expandAll = screen.getByRole("button", { name: "Expand all" });
    const collapseAll = screen.getByRole("button", { name: "Collapse all" });
    expect(collapseAll.hasAttribute("disabled")).toBe(true);

    await fireEvent.click(expandAll);

    const parentRow = screen.getByRole("button", { name: /Parent task/ });
    const childRow = await screen.findByRole("button", { name: /Child task/ });
    const grandchildRow = await screen.findByRole("button", { name: /Grandchild task/ });
    expect(parentRow.getAttribute("aria-expanded")).toBe("true");
    expect(childRow.getAttribute("aria-expanded")).toBe("true");
    expect(grandchildRow.classList.contains("row--child")).toBe(true);
    expect(api.issue).toHaveBeenCalledWith(parent.uid);
    expect(api.issue).toHaveBeenCalledWith(child.uid);
    expect(expandAll.hasAttribute("disabled")).toBe(true);
    expect(collapseAll.hasAttribute("disabled")).toBe(false);

    await fireEvent.click(collapseAll);

    await waitFor(() => {
      expect(parentRow.getAttribute("aria-expanded")).toBe("false");
    });
    expect(screen.queryByRole("button", { name: /Child task/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Grandchild task/ })).toBeNull();
    expect(collapseAll.hasAttribute("disabled")).toBe(true);
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
    await fireEvent.keyUp(rows[1]!, { key: "j" });
    expect(document.activeElement).toBe(rows[1]);
    await waitFor(() => {
      expect(selected[selected.length - 1]).toBe(rows[1]!.dataset.uid);
    });

    await fireEvent.keyDown(rows[1]!, { key: "k" });
    await fireEvent.keyUp(rows[0]!, { key: "k" });
    expect(document.activeElement).toBe(rows[0]);
    await waitFor(() => {
      expect(selected[selected.length - 1]).toBe(rows[0]!.dataset.uid);
    });
  });

  it("debounces keyboard navigation so only the final row is selected", async () => {
    const { selected, rows } = renderKeyboardList(viewWithIssues([...baseIssues, thirdIssue()]));
    await fireEvent.keyDown(rows[0]!, { key: "j" });
    await fireEvent.keyDown(rows[1]!, { key: "j", repeat: true });
    await fireEvent.keyUp(rows[2]!, { key: "j" });
    expect(document.activeElement).toBe(rows[2]);
    expect(selected).toEqual([]);

    vi.advanceTimersByTime(50);
    expect(selected).toEqual([rows[2]!.dataset.uid]);
  });

  it("holds selection while a navigation key repeats slower than the debounce", async () => {
    const { selected, rows } = renderKeyboardList(viewWithIssues([...baseIssues, thirdIssue()]));
    // OS key-repeat slower than the 50ms debounce: each repeat arrives
    // after the timer has already expired. The held key must keep the
    // selection pending so intermediate rows never commit.
    await fireEvent.keyDown(rows[0]!, { key: "j" });
    vi.advanceTimersByTime(100);
    expect(selected).toEqual([]);

    await fireEvent.keyDown(rows[1]!, { key: "j", repeat: true });
    vi.advanceTimersByTime(100);
    expect(selected).toEqual([]);

    await fireEvent.keyUp(rows[2]!, { key: "j" });
    vi.advanceTimersByTime(50);
    expect(selected).toEqual([rows[2]!.dataset.uid]);
  });

  it("commits the selection when Shift is released before the navigation key", async () => {
    const { selected, rows } = renderKeyboardList();
    // Shift+g jumps to the end; the keydown reports key "G" but releasing
    // Shift first makes the keyup report key "g". The physical code is
    // stable across both, so the held entry must still clear.
    await fireEvent.keyDown(rows[0]!, { key: "G", code: "KeyG", shiftKey: true });
    expect(document.activeElement).toBe(rows[rows.length - 1]);
    vi.advanceTimersByTime(50);
    expect(selected).toEqual([]);

    await fireEvent.keyUp(rows[rows.length - 1]!, { key: "g", code: "KeyG" });
    vi.advanceTimersByTime(50);
    expect(selected).toEqual([rows[rows.length - 1]!.dataset.uid]);
  });

  it("drops a pending keyboard selection when workspace navigation begins", async () => {
    vi.useFakeTimers();
    const selected: string[] = [];
    const props = {
      currentView,
      selectedIssueUID: null,
      loading: false,
      navigationGeneration: 0,
      onSelect: (issue: KataTaskSummary) => selected.push(issue.uid),
    };
    const { rerender } = render(KataIssueList, { props });

    const rows = visibleRows();
    rows[0]!.focus();
    await fireEvent.keyDown(rows[0]!, { key: "j" });

    // Navigation starts while the key is still held: the view data has
    // not arrived yet (same currentView, no remount), so only the
    // generation bump can stop the release from committing stale.
    await rerender({ ...props, navigationGeneration: 1 });
    await fireEvent.keyUp(rows[1]!, { key: "j" });
    vi.advanceTimersByTime(100);
    expect(selected).toEqual([]);
  });

  it("clicking a row selects immediately and cancels a pending keyboard selection", async () => {
    const { selected, rows } = renderKeyboardList();
    await fireEvent.keyDown(rows[0]!, { key: "j" });
    expect(selected).toEqual([]);

    await fireEvent.click(rows[0]!);
    expect(selected).toEqual([rows[0]!.dataset.uid]);

    await fireEvent.keyUp(rows[0]!, { key: "j" });
    vi.advanceTimersByTime(100);
    expect(selected).toEqual([rows[0]!.dataset.uid]);
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

  it("expands restored ancestors root-first and scrolls the selected row nearest", async () => {
    const root = task({
      uid: "issue-reveal-root",
      short_id: "reveal-root",
      qualified_id: "Finances#reveal-root",
      title: "Root task",
    });
    const parent = task({
      uid: "issue-reveal-parent",
      short_id: "reveal-parent",
      qualified_id: "Finances#reveal-parent",
      title: "Parent task",
      parent_short_id: root.short_id,
    });
    const child = task({
      uid: "issue-reveal-child",
      short_id: "reveal-child",
      qualified_id: "Finances#reveal-child",
      title: "Child task",
      parent_short_id: parent.short_id,
    });
    const scrollIntoView = vi.fn();
    vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(scrollIntoView);
    const api = {
      issue: vi.fn(async (uid: string) => (uid === root.uid ? apiDetail(root, [parent]) : apiDetail(parent, [child]))),
    } as unknown as KataTaskAPI;

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([child]),
        selectedIssueUID: child.uid,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    await rerender({
      currentView: viewWithIssues([child]),
      selectedIssueUID: child.uid,
      loading: false,
      api,
      revealRequest: { uid: child.uid, chain: [root, parent, child], generation: 1 },
      onSelect: () => {},
    });

    const rootRow = await screen.findByRole("button", { name: /Root task/ });
    const childRow = screen.getByRole("button", { name: /Child task/ });
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" }));
    expect(rootRow.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("button", { name: /Parent task/ }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getAllByRole("button", { name: /Child task/ })).toHaveLength(1);
    expect(document.activeElement).not.toBe(childRow);
  });

  it("merges a restored reveal chain with authoritative siblings during expand all", async () => {
    const root = task({
      uid: "issue-reveal-root",
      short_id: "reveal-root",
      qualified_id: "Finances#reveal-root",
      title: "Root task",
      child_counts: { open: 2, total: 2 },
    });
    const restoredChild = task({
      uid: "issue-reveal-child",
      short_id: "reveal-child",
      qualified_id: "Finances#reveal-child",
      title: "Restored child",
      child_counts: { open: 1, total: 1 },
      parent_short_id: root.short_id,
    });
    const restoredGrandchild = task({
      uid: "issue-reveal-restored-grandchild",
      short_id: "reveal-restored-grandchild",
      qualified_id: "Finances#reveal-restored-grandchild",
      title: "Restored grandchild",
      parent_short_id: restoredChild.short_id,
    });
    const sibling = task({
      uid: "issue-reveal-sibling",
      short_id: "reveal-sibling",
      qualified_id: "Finances#reveal-sibling",
      title: "Sibling task",
      child_counts: { open: 1, total: 1 },
      parent_short_id: root.short_id,
    });
    const grandchild = task({
      uid: "issue-reveal-grandchild",
      short_id: "reveal-grandchild",
      qualified_id: "Finances#reveal-grandchild",
      title: "Sibling grandchild",
      parent_short_id: sibling.short_id,
    });
    const api = {
      issue: vi.fn(async (uid: string) => {
        if (uid === root.uid) return apiDetail(root, [sibling, restoredChild]);
        if (uid === restoredChild.uid) return apiDetail(restoredChild, [restoredGrandchild]);
        return apiDetail(sibling, [grandchild]);
      }),
    } as unknown as KataTaskAPI;

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([restoredChild]),
        selectedIssueUID: restoredChild.uid,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    await rerender({
      currentView: viewWithIssues([restoredChild]),
      selectedIssueUID: restoredChild.uid,
      loading: false,
      api,
      revealRequest: { uid: restoredChild.uid, chain: [root, restoredChild], generation: 1 },
      onSelect: () => {},
    });

    expect(await screen.findByRole("button", { name: /Sibling task/ })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: /Restored child/ })).toHaveLength(1);
    expect(api.issue).toHaveBeenCalledWith(root.uid);

    await fireEvent.click(screen.getByRole("button", { name: "Expand all" }));

    expect(await screen.findByRole("button", { name: /Sibling grandchild/ })).toBeTruthy();
    expect(await screen.findByRole("button", { name: /Restored grandchild/ })).toBeTruthy();
    expect(api.issue).toHaveBeenCalledWith(sibling.uid);
    expect(api.issue).toHaveBeenCalledWith(restoredChild.uid);
    expect(screen.getAllByRole("button", { name: /Restored child/ })).toHaveLength(1);

    await rerender({
      currentView: viewWithIssues([root]),
      selectedIssueUID: null,
      loading: false,
      api,
      revealRequest: null,
      onSelect: () => {},
    });
    expect(screen.getByRole("button", { name: /Restored child/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Restored grandchild/ })).toBeTruthy();
  });

  it("keeps a contextual successor visible without admitting unrelated filtered siblings", async () => {
    const root = task({
      uid: "issue-filtered-reveal-root",
      short_id: "filtered-reveal-root",
      qualified_id: "Finances#filtered-reveal-root",
      title: "Filtered reveal root",
      child_counts: { open: 1, total: 3 },
    });
    const contextualChild = task({
      uid: "issue-filtered-contextual-child",
      short_id: "filtered-contextual-child",
      qualified_id: "Finances#filtered-contextual-child",
      title: "Closed contextual child",
      status: "closed",
      parent_short_id: root.short_id,
    });
    const openSibling = task({
      uid: "issue-filtered-open-sibling",
      short_id: "filtered-open-sibling",
      qualified_id: "Finances#filtered-open-sibling",
      title: "Open sibling",
      parent_short_id: root.short_id,
    });
    const closedSibling = task({
      uid: "issue-filtered-closed-sibling",
      short_id: "filtered-closed-sibling",
      qualified_id: "Finances#filtered-closed-sibling",
      title: "Unrelated closed sibling",
      status: "closed",
      parent_short_id: root.short_id,
    });
    const api = apiWithDetail(root, [closedSibling, openSibling, contextualChild]);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([root]),
        selectedIssueUID: contextualChild.uid,
        loading: false,
        statusFilter: "open",
        api,
        onSelect: () => {},
      },
    });

    await rerender({
      currentView: viewWithIssues([root]),
      selectedIssueUID: contextualChild.uid,
      loading: false,
      statusFilter: "open",
      api,
      revealRequest: {
        uid: contextualChild.uid,
        chain: [root, contextualChild],
        generation: 1,
      },
      onSelect: () => {},
    });

    expect(await screen.findByRole("button", { name: /Closed contextual child/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Open sibling/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Unrelated closed sibling/ })).toBeNull();
  });

  it("drops a synthetic reveal successor and its owned expansion after reveal cleanup", async () => {
    const root = task({
      uid: "issue-reveal-root-cleanup",
      short_id: "reveal-root-cleanup",
      qualified_id: "Finances#reveal-root-cleanup",
      title: "Cleanup root",
      child_counts: { open: 1, total: 1 },
    });
    const restoredChild = task({
      uid: "issue-reveal-child-cleanup",
      short_id: "reveal-child-cleanup",
      qualified_id: "Finances#reveal-child-cleanup",
      title: "Temporary restored child",
      parent_short_id: root.short_id,
    });
    const api = apiWithDetail(root, []);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([restoredChild]),
        selectedIssueUID: restoredChild.uid,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    await rerender({
      currentView: viewWithIssues([restoredChild]),
      selectedIssueUID: restoredChild.uid,
      loading: false,
      api,
      revealRequest: { uid: restoredChild.uid, chain: [root, restoredChild], generation: 1 },
      onSelect: () => {},
    });
    expect(await screen.findByRole("button", { name: /Temporary restored child/ })).toBeTruthy();

    await rerender({
      currentView: viewWithIssues([root]),
      selectedIssueUID: null,
      loading: false,
      api,
      revealRequest: null,
      onSelect: () => {},
    });

    await waitFor(() => expect(screen.queryByRole("button", { name: /Temporary restored child/ })).toBeNull());
    expect(screen.getByRole("button", { name: /Cleanup root/ }).getAttribute("aria-expanded")).toBe("false");
  });

  it("releases the previous reveal expansion when a newer chain supersedes it", async () => {
    const oldRoot = task({
      uid: "issue-old-reveal-root",
      short_id: "old-reveal-root",
      qualified_id: "Finances#old-reveal-root",
      title: "Old reveal root",
      child_counts: { open: 1, total: 1 },
    });
    const oldChild = task({
      uid: "issue-old-reveal-child",
      short_id: "old-reveal-child",
      qualified_id: "Finances#old-reveal-child",
      title: "Old reveal child",
      parent_short_id: oldRoot.short_id,
    });
    const newRoot = task({
      uid: "issue-new-reveal-root",
      short_id: "new-reveal-root",
      qualified_id: "Finances#new-reveal-root",
      title: "New reveal root",
      child_counts: { open: 1, total: 1 },
    });
    const newChild = task({
      uid: "issue-new-reveal-child",
      short_id: "new-reveal-child",
      qualified_id: "Finances#new-reveal-child",
      title: "New reveal child",
      parent_short_id: newRoot.short_id,
    });
    const api = {
      issue: vi.fn(async (uid: string) =>
        uid === oldRoot.uid ? apiDetail(oldRoot, [oldChild]) : apiDetail(newRoot, [newChild]),
      ),
    } as unknown as KataTaskAPI;

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([oldRoot, newRoot]),
        selectedIssueUID: oldChild.uid,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    await rerender({
      currentView: viewWithIssues([oldRoot, newRoot]),
      selectedIssueUID: oldChild.uid,
      loading: false,
      api,
      revealRequest: { uid: oldChild.uid, chain: [oldRoot, oldChild], generation: 1 },
      onSelect: () => {},
    });
    expect(await screen.findByRole("button", { name: /Old reveal child/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Old reveal root/ }).getAttribute("aria-expanded")).toBe("true");

    await rerender({
      currentView: viewWithIssues([oldRoot, newRoot]),
      selectedIssueUID: newChild.uid,
      loading: false,
      api,
      revealRequest: { uid: newChild.uid, chain: [newRoot, newChild], generation: 2 },
      onSelect: () => {},
    });

    expect(await screen.findByRole("button", { name: /New reveal child/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Old reveal root/ }).getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByRole("button", { name: /New reveal root/ }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.queryByRole("button", { name: /Old reveal child/ })).toBeNull();
  });

  it("preserves a user-owned expansion when reveal cleanup crosses the same chain", async () => {
    const root = task({
      uid: "issue-user-expanded-root",
      short_id: "user-expanded-root",
      qualified_id: "Finances#user-expanded-root",
      title: "User expanded root",
      child_counts: { open: 1, total: 1 },
    });
    const child = task({
      uid: "issue-user-expanded-child",
      short_id: "user-expanded-child",
      qualified_id: "Finances#user-expanded-child",
      title: "User expanded child",
      parent_short_id: root.short_id,
    });
    const api = apiWithDetail(root, [child]);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([root]),
        selectedIssueUID: null,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    const rootRow = screen.getByRole("button", { name: /User expanded root/ });
    await fireEvent.keyDown(rootRow, { key: "ArrowRight" });
    expect(await screen.findByRole("button", { name: /User expanded child/ })).toBeTruthy();

    await rerender({
      currentView: viewWithIssues([root]),
      selectedIssueUID: child.uid,
      loading: false,
      api,
      revealRequest: { uid: child.uid, chain: [root, child], generation: 1 },
      onSelect: () => {},
    });
    await rerender({
      currentView: viewWithIssues([root]),
      selectedIssueUID: null,
      loading: false,
      api,
      revealRequest: null,
      onSelect: () => {},
    });

    expect(screen.getByRole("button", { name: /User expanded root/ }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("button", { name: /User expanded child/ })).toBeTruthy();
  });

  it("continues a seeded reveal chain when authoritative refresh fails", async () => {
    const root = task({
      uid: "issue-reveal-root-failure",
      short_id: "reveal-root-failure",
      qualified_id: "Finances#reveal-root-failure",
      title: "Fallback root",
    });
    const child = task({
      uid: "issue-reveal-child-failure",
      short_id: "reveal-child-failure",
      qualified_id: "Finances#reveal-child-failure",
      title: "Fallback child",
      parent_short_id: root.short_id,
    });
    const api = {
      issue: vi.fn(async () => {
        throw new Error("child refresh failed");
      }),
    } as unknown as KataTaskAPI;

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([child]),
        selectedIssueUID: child.uid,
        loading: false,
        api,
        onSelect: () => {},
      },
    });

    await rerender({
      currentView: viewWithIssues([child]),
      selectedIssueUID: child.uid,
      loading: false,
      api,
      revealRequest: { uid: child.uid, chain: [root, child], generation: 1 },
      onSelect: () => {},
    });

    const rootRow = await screen.findByRole("button", { name: /Fallback root/ });
    expect(await screen.findByRole("button", { name: /Fallback child/ })).toBeTruthy();
    expect(rootRow.getAttribute("aria-expanded")).toBe("true");
    expect(api.issue).toHaveBeenCalledWith(root.uid);
  });

  it("keeps group headings while scrolling to a restored top-level task", async () => {
    const scrollIntoView = vi.fn();
    vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(scrollIntoView);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView,
        selectedIssueUID: baseIssues[0]!.uid,
        loading: false,
        onSelect: () => {},
      },
    });
    await fireEvent.click(screen.getByRole("button", { name: /Sort by Priority/ }));

    await rerender({
      currentView,
      selectedIssueUID: baseIssues[0]!.uid,
      loading: false,
      revealRequest: { uid: baseIssues[0]!.uid, chain: [baseIssues[0]!], generation: 1 },
      onSelect: () => {},
    });

    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" }));
    expect(screen.getByRole("heading", { level: 3, name: /^Overdue\s+1$/ })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 3, name: /^Today\s+1$/ })).toBeTruthy();
  });

  it("clears reveal-owned expansion after ordinary row selection", async () => {
    const parent = task({
      uid: "issue-reveal-parent",
      short_id: "reveal-parent",
      qualified_id: "Finances#reveal-parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    });
    const child = task({
      uid: "issue-reveal-child",
      short_id: "reveal-child",
      qualified_id: "Finances#reveal-child",
      title: "Child task",
      parent_short_id: parent.short_id,
    });
    const onSelect = vi.fn();
    const api = apiWithDetail(parent, [child]);

    const { rerender } = render(KataIssueList, {
      props: {
        currentView: viewWithIssues([parent, child]),
        selectedIssueUID: child.uid,
        loading: false,
        api,
        onSelect,
      },
    });

    await rerender({
      currentView: viewWithIssues([parent, child]),
      selectedIssueUID: child.uid,
      loading: false,
      api,
      revealRequest: { uid: child.uid, chain: [parent, child], generation: 1 },
      onSelect,
    });

    const childRow = await screen.findByRole("button", { name: /Child task/ });
    await fireEvent.click(childRow);

    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(child));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Child task/ })).toBeNull());
    expect(screen.getByRole("button", { name: /Parent task/ }).getAttribute("aria-expanded")).toBe("false");
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

// Shared setup for the fake-timer keyboard tests: renders the list,
// records selections, and focuses the first row ready for key events.
function renderKeyboardList(view: KataCurrentView = currentView) {
  vi.useFakeTimers();
  const selected: string[] = [];
  render(KataIssueList, {
    props: {
      currentView: view,
      selectedIssueUID: null,
      loading: false,
      onSelect: (issue: KataTaskSummary) => selected.push(issue.uid),
    },
  });
  const rows = visibleRows();
  rows[0]!.focus();
  return { selected, rows };
}

function thirdIssue(): KataTaskSummary {
  return task({
    id: 103,
    uid: "issue-water-plants",
    short_id: "water-plants",
    qualified_id: "Home#water-plants",
    title: "Water plants",
    updated_at: "2026-05-13T08:00:00Z",
  });
}

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
