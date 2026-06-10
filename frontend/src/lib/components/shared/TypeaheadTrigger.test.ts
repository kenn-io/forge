import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import TypeaheadTrigger from "./TypeaheadTrigger.svelte";

describe("TypeaheadTrigger", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("selects the highlighted filtered option after typing with clear enabled", async () => {
    const onChange = vi.fn();

    render(TypeaheadTrigger, {
      props: {
        ariaLabel: "Project scope",
        options: [
          { value: "project-finances", label: "Finances" },
          { value: "project-kata", label: "Kata" },
        ],
        allowClear: true,
        clearLabel: "All projects",
        onChange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Project scope: All projects" }));
    const input = screen.getByRole("combobox", { name: "Project scope" });
    await fireEvent.input(input, { target: { value: "kat" } });
    await fireEvent.keyDown(input, { key: "Enter" });

    expect(onChange).toHaveBeenCalledWith("project-kata");
  });

  it("keeps the query open when selection reports failure", async () => {
    const onChange = vi.fn(async () => false);

    render(TypeaheadTrigger, {
      props: {
        ariaLabel: "Owner",
        options: [{ value: "agent:planner", label: "agent:planner" }],
        allowCustom: true,
        onChange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Owner: None" }));
    const input = screen.getByRole("combobox", { name: "Owner" }) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "agent:new" } });
    await fireEvent.keyDown(input, { key: "Enter" });

    expect(onChange).toHaveBeenCalledWith("agent:new");
    expect(screen.getByRole("combobox", { name: "Owner" })).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Owner" }) as HTMLInputElement).value).toBe("agent:new");
  });

  it("renders the option list as a bounded popover", async () => {
    const { container } = render(TypeaheadTrigger, {
      props: {
        ariaLabel: "Move issue project",
        options: [{ value: "project-1", label: "Very long project name" }],
        onChange: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Move issue project: None" }));

    const list = container.querySelector(".typeahead-list");
    expect(list).toBeTruthy();
    expect(list!.getAttribute("data-surface")).toBe("solid");
  });
});
