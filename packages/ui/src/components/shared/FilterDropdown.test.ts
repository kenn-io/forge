import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { tick } from "svelte";
import FilterDropdown from "./FilterDropdown.svelte";

describe("FilterDropdown", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders badge count and toggles multi-select items", async () => {
    const onComments = vi.fn();

    render(FilterDropdown, {
      props: {
        label: "Filters",
        badgeCount: 2,
        sections: [
          {
            title: "Event types",
            items: [
              {
                id: "comment",
                label: "Comments",
                active: true,
                color: "var(--accent-amber)",
                onSelect: onComments,
              },
            ],
          },
        ],
      },
    });

    expect(screen.getByText("2")).toBeTruthy();

    await fireEvent.click(
      screen.getByRole("button", {
        name: /filters/i,
      }),
    );

    await fireEvent.click(
      screen.getByRole("button", {
        name: /comments/i,
      }),
    );

    expect(onComments).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Event types")).toBeTruthy();
  });

  it("does not use the legacy inline polygon icon for the trigger", () => {
    render(FilterDropdown, {
      props: {
        label: "Filters",
        sections: [
          {
            items: [
              {
                id: "comment",
                label: "Comments",
                active: false,
                onSelect: vi.fn(),
              },
            ],
          },
        ],
      },
    });

    const trigger = screen.getByRole("button", {
      name: /filters/i,
    });

    expect(trigger.querySelector('polygon[points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"]')).toBeNull();
  });

  it("supports single-select actions that close after selection", async () => {
    const onDone = vi.fn();

    render(FilterDropdown, {
      props: {
        label: "Status",
        detail: "Done",
        active: true,
        sections: [
          {
            items: [
              {
                id: "done",
                label: "Done",
                active: true,
                onSelect: onDone,
                closeOnSelect: true,
              },
            ],
          },
        ],
      },
    });

    expect(screen.getByText("Done")).toBeTruthy();

    await fireEvent.click(
      screen.getByRole("button", {
        name: /status/i,
      }),
    );

    const dropdown = document.querySelector(".filter-dropdown");
    expect(dropdown).toBeTruthy();

    await fireEvent.click(
      within(dropdown as HTMLElement).getByRole("button", {
        name: /done/i,
      }),
    );

    expect(onDone).toHaveBeenCalledTimes(1);
    expect(document.querySelector(".filter-dropdown")).toBeNull();
  });

  it("supports end-aligned dropdown placement", async () => {
    render(FilterDropdown, {
      props: {
        label: "Filters",
        align: "end",
        sections: [
          {
            items: [
              {
                id: "messages",
                label: "Messages",
                active: true,
                onSelect: vi.fn(),
              },
            ],
          },
        ],
      },
    });

    await fireEvent.click(
      screen.getByRole("button", {
        name: /filters/i,
      }),
    );

    expect(document.querySelector(".filter-dropdown")?.classList.contains("filter-dropdown--align-end")).toBe(true);
  });

  it("closes and blocks selection when disabled flips true while open", async () => {
    const onSelect = vi.fn();
    const onReset = vi.fn();

    const { rerender } = render(FilterDropdown, {
      props: {
        label: "Status",
        disabled: false,
        resetLabel: "Show all",
        onReset,
        sections: [
          {
            items: [
              {
                id: "done",
                label: "Done",
                active: false,
                onSelect,
              },
            ],
          },
        ],
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /status/i }));
    expect(document.querySelector(".filter-dropdown")).toBeTruthy();

    await rerender({
      label: "Status",
      disabled: true,
      resetLabel: "Show all",
      onReset,
      sections: [
        {
          items: [
            {
              id: "done",
              label: "Done",
              active: false,
              onSelect,
            },
          ],
        },
      ],
    });
    await tick();

    expect(document.querySelector(".filter-dropdown")).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });
});
