import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataProjectSummary, KataTaskSearchFilters } from "../../api/kata/taskTypes.js";
import type { KataAreaSummary, KataCurrentView } from "../../stores/kata-workspace.svelte.js";
import KataSidebar from "./KataSidebar.svelte";

const projects: KataProjectSummary[] = [
  project({ id: 1, uid: "project-inbox", name: "Inbox", metadata: { role: "inbox" }, open_count: 2 }),
  project({ id: 2, uid: "project-finances", name: "Finances", metadata: { area: "Personal" }, open_count: 1 }),
  project({ id: 3, uid: "project-work", name: "Work notes", metadata: { area: "Work" }, open_count: 4 }),
];

const areas: KataAreaSummary[] = [
  { name: "Personal", projects: [projects[1]!] },
  { name: "Work", projects: [projects[2]!] },
];

const currentView: KataCurrentView = {
  name: "today",
  fetched_at: "2026-05-16T10:00:00Z",
  groups: [{ id: "today", title: "Today", issues: [] }],
};

const allScopeFilters: KataTaskSearchFilters = {
  scope: { kind: "all" },
  status: "open",
  owner: "",
  label: "",
  query: "",
};

describe("KataSidebar", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("opens system views and project scopes from the restored sidebar", async () => {
    const onOpenView = vi.fn();
    const onOpenProject = vi.fn();

    render(KataSidebar, {
      props: {
        areas,
        projects,
        currentView,
        searchFilters: allScopeFilters,
        onOpenView,
        onOpenProject,
        onCreateProject: vi.fn(),
        onRenameProject: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /^Inbox\b/ }));
    expect(onOpenView).toHaveBeenCalledWith("inbox");

    await fireEvent.click(screen.getByRole("button", { name: /^Finances\b/ }));
    expect(onOpenProject).toHaveBeenCalledWith("project-finances");
  });

  it("double-clicking a project enters rename mode", async () => {
    const onOpenProject = vi.fn();

    render(KataSidebar, {
      props: {
        areas,
        projects,
        currentView,
        searchFilters: allScopeFilters,
        onOpenView: vi.fn(),
        onOpenProject,
        onCreateProject: vi.fn(),
        onRenameProject: vi.fn(),
      },
    });

    await fireEvent.doubleClick(screen.getByRole("button", { name: /^Finances\b/ }));

    const input = screen.getByRole("textbox", { name: "Rename project" });
    expect(input).toBeTruthy();
    await waitFor(() => expect(input).toBe(document.activeElement));
  });

  it("creates a project and opens the created scope", async () => {
    const created = project({ id: 9, uid: "project-new", name: "New Project", open_count: 0 });
    const onCreateProject = vi.fn(async () => created);
    const onOpenProject = vi.fn();

    render(KataSidebar, {
      props: {
        areas,
        projects,
        currentView,
        searchFilters: allScopeFilters,
        onOpenView: vi.fn(),
        onOpenProject,
        onCreateProject,
        onRenameProject: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "New project" }));
    const input = screen.getByRole("textbox", { name: "New project name" });
    await waitFor(() => expect(input).toBe(document.activeElement));
    await fireEvent.input(input, { target: { value: "New Project" } });
    await fireEvent.submit(input.closest("form")!);

    await waitFor(() => {
      expect(onCreateProject).toHaveBeenCalledWith("New Project");
      expect(onOpenProject).toHaveBeenCalledWith("project-new");
    });
  });

  it("renames a project from the project row", async () => {
    const onRenameProject = vi.fn(async () => {});

    render(KataSidebar, {
      props: {
        areas,
        projects,
        currentView,
        searchFilters: { ...allScopeFilters, scope: { kind: "project", project_uid: "project-finances" } },
        onOpenView: vi.fn(),
        onOpenProject: vi.fn(),
        onCreateProject: vi.fn(),
        onRenameProject,
      },
    });

    const personal = screen.getByRole("region", { name: "Personal" });
    await fireEvent.click(within(personal).getByRole("button", { name: "Rename Finances" }));

    const input = screen.getByRole("textbox", { name: "Rename project" });
    await fireEvent.input(input, { target: { value: "Household" } });
    await fireEvent.submit(input.closest("form")!);

    await waitFor(() => {
      expect(onRenameProject).toHaveBeenCalledWith(2, "Household");
    });
  });
});

function project(overrides: Partial<KataProjectSummary>): KataProjectSummary {
  return {
    id: 1,
    uid: "project",
    name: "Project",
    metadata: {},
    open_count: 0,
    ...overrides,
  };
}
