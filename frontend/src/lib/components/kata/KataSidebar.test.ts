import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import type { ComponentProps } from "svelte";
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

type SidebarProps = ComponentProps<typeof KataSidebar>;

function renderSidebar(overrides: Partial<SidebarProps> = {}) {
  return render(KataSidebar, {
    props: {
      areas,
      projects,
      currentView,
      searchFilters: allScopeFilters,
      onOpenView: vi.fn(),
      onOpenProject: vi.fn(),
      onCreateProject: vi.fn(),
      ...overrides,
    },
  });
}

describe("KataSidebar", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders system views, expanded area groups, and project creation in order", () => {
    renderSidebar();

    const navigation = screen.getByRole("region", { name: "Kata navigation" });
    const inbox = within(navigation).getByRole("button", { name: /^Inbox\b/ });
    const personal = within(navigation).getByRole("button", { name: /^Personal\s+1$/ });
    const work = within(navigation).getByRole("button", { name: /^Work\s+1$/ });
    const create = within(navigation).getByRole("button", { name: "New project" });

    expect(personal.getAttribute("aria-expanded")).toBe("true");
    expect(work.getAttribute("aria-expanded")).toBe("true");
    const ordered = [inbox, personal, work, create];
    for (let index = 0; index < ordered.length - 1; index += 1) {
      expect(ordered[index]!.compareDocumentPosition(ordered[index + 1]!)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    }
  });

  it("keeps area collapse state while mounted and resets it after remount", async () => {
    const view = renderSidebar();
    const personal = screen.getByRole("button", { name: /^Personal\s+1$/ });

    await fireEvent.click(personal);
    expect(personal.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("button", { name: /^Finances\b/ })).toBeNull();

    await view.rerender({ areas: [...areas] });
    expect(screen.getByRole("button", { name: /^Personal\s+1$/ }).getAttribute("aria-expanded")).toBe("false");

    view.unmount();
    renderSidebar();
    expect(screen.getByRole("button", { name: /^Personal\s+1$/ }).getAttribute("aria-expanded")).toBe("true");
  });

  it("opens system views and project scopes from the restored sidebar", async () => {
    const onOpenView = vi.fn();
    const onOpenProject = vi.fn();

    renderSidebar({ onOpenView, onOpenProject });

    await fireEvent.click(screen.getByRole("button", { name: /^Inbox\b/ }));
    expect(onOpenView).toHaveBeenCalledWith("inbox");

    await fireEvent.click(screen.getByRole("button", { name: /^Finances\b/ }));
    expect(onOpenProject).toHaveBeenCalledWith("project-finances");
  });

  it("keeps project rows navigation-only without rename affordances", async () => {
    const onOpenProject = vi.fn();
    renderSidebar({
      onOpenProject,
      searchFilters: { ...allScopeFilters, scope: { kind: "project", project_uid: "project-finances" } },
    });

    const finances = screen.getByRole("button", { name: /^Finances\b/ });
    expect(finances.classList.contains("active")).toBe(true);
    expect(screen.queryByRole("button", { name: "Rename Finances" })).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Rename project" })).toBeNull();

    await fireEvent.click(finances);
    await fireEvent.doubleClick(finances);
    expect(screen.queryByRole("textbox", { name: "Rename project" })).toBeNull();
    expect(onOpenProject).toHaveBeenCalledWith("project-finances");
  });

  it("creates a project and opens the created scope", async () => {
    const created = project({ id: 9, uid: "project-new", name: "New Project", open_count: 0 });
    const onCreateProject = vi.fn(async () => created);
    const onOpenProject = vi.fn();

    renderSidebar({ onCreateProject, onOpenProject });

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
