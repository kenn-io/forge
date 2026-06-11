import { fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { describe, expect, test, vi } from "vite-plus/test";

import type { KataProjectSummary, KataTaskSearchFilters } from "../../api/kata/taskTypes.js";
import KataSearchPanel from "./KataSearchPanel.svelte";

const filters: KataTaskSearchFilters = {
  scope: { kind: "all" },
  status: "open",
  owner: "",
  label: "",
  query: "",
};

describe("KataSearchPanel", () => {
  test("emits compact search and filter control changes", async () => {
    const changes: KataTaskSearchFilters[] = [];
    const onChange = vi.fn((next: KataTaskSearchFilters) => {
      changes.push(next);
    });

    const { rerender } = render(KataSearchPanel, {
      props: {
        filters,
        projects,
        onChange,
      },
    });
    const applyLatest = async () => {
      const next = changes[changes.length - 1];
      expect(next).toBeTruthy();
      await rerender({ filters: next!, projects, onChange });
    };

    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "rent" } });
    await applyLatest();
    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "All" }));
    await applyLatest();
    await fireEvent.input(screen.getByLabelText("Owner"), { target: { value: "agent:planner" } });
    await applyLatest();
    await fireEvent.input(screen.getByLabelText("Label"), { target: { value: "health" } });
    await applyLatest();
    await fireEvent.click(screen.getByRole("button", { name: /Project scope: All projects/i }));
    const projectInput = screen.getByRole("combobox", { name: "Project scope" });
    expect(document.activeElement).toBe(projectInput);
    await fireEvent.input(projectInput, { target: { value: "hea" } });
    await fireEvent.keyDown(projectInput, { key: "Enter" });

    await waitFor(() => expect(changes.length).toBeGreaterThanOrEqual(5));
    expect(changes[changes.length - 1]).toMatchObject({
      query: "rent",
      status: "all",
      owner: "agent:planner",
      label: "health",
      scope: { kind: "project", project_uid: "project-health" },
    });
  });

  test("displays duplicate candidate details", () => {
    render(KataSearchPanel, {
      props: {
        filters,
        projects: [],
        duplicateCandidates: [{ title: "Pay rent", qualified_id: "Finances#rent", reason: "same title" }],
        onChange: vi.fn(),
      },
    });

    const candidate = screen.getByRole("listitem");
    expect(within(candidate).getByText("Pay rent")).toBeTruthy();
    expect(within(candidate).getByText("Finances#rent")).toBeTruthy();
    expect(within(candidate).getByText("same title")).toBeTruthy();
  });

  test("keeps fast filter edits when parent state has not rerendered yet", async () => {
    const changes: KataTaskSearchFilters[] = [];
    const onChange = vi.fn((next: KataTaskSearchFilters) => {
      changes.push(next);
    });

    render(KataSearchPanel, {
      props: {
        filters,
        projects,
        onChange,
      },
    });

    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "rent" } });
    await fireEvent.click(screen.getByRole("button", { name: /Project scope: All projects/i }));
    const projectInput = screen.getByRole("combobox", { name: "Project scope" });
    await fireEvent.input(projectInput, { target: { value: "hea" } });
    await fireEvent.keyDown(projectInput, { key: "Enter" });

    await waitFor(() => expect(changes.length).toBeGreaterThanOrEqual(2));
    expect(changes[changes.length - 1]).toMatchObject({
      query: "rent",
      scope: { kind: "project", project_uid: "project-health" },
    });
  });
});

const projects: KataProjectSummary[] = [
  {
    id: 1,
    uid: "project-kata",
    name: "Kata",
    open_count: 4,
    metadata: {},
  },
  {
    id: 2,
    uid: "project-health",
    name: "Health",
    open_count: 2,
    metadata: {},
  },
];
