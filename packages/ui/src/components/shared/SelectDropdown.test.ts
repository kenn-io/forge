import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import SelectDropdown from "./SelectDropdown.svelte";

describe("SelectDropdown", () => {
  afterEach(() => {
    cleanup();
  });

  const defaultOptions = [
    { value: "new", label: "New" },
    { value: "reviewing", label: "Reviewing" },
    { value: "waiting", label: "Waiting" },
  ];

  function renderDropdown(props = {}) {
    return render(SelectDropdown, {
      props: {
        value: "reviewing",
        options: defaultOptions,
        onchange: vi.fn(),
        ...props,
      },
    });
  }

  it("renders a custom trigger instead of a native select", async () => {
    const onchange = vi.fn();

    const { container } = renderDropdown({ onchange });

    expect(container.querySelector("select")).toBeNull();

    await fireEvent.click(screen.getByRole("combobox", { name: "Reviewing" }));

    const listbox = screen.getByRole("listbox");
    await fireEvent.click(within(listbox).getByRole("option", { name: "New" }));

    expect(onchange).toHaveBeenCalledWith("new");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("updates aria-activedescendant when arrow keys move the highlight", async () => {
    renderDropdown();

    const trigger = screen.getByRole("combobox", { name: "Reviewing" });
    await fireEvent.keyDown(trigger, { key: "ArrowDown" });
    await fireEvent.keyDown(trigger, { key: "ArrowDown" });

    const waiting = screen.getByRole("option", { name: "Waiting" });
    expect(trigger.getAttribute("aria-activedescendant")).toBe(waiting.id);
  });

  it("keeps options out of the tab sequence", async () => {
    renderDropdown();

    await fireEvent.click(screen.getByRole("combobox", { name: "Reviewing" }));

    const options = screen.getAllByRole("option");
    for (const option of options) {
      expect(option.getAttribute("tabindex")).toBe("-1");
    }
  });

  it("can use a shorter label for the selected trigger", () => {
    renderDropdown({
      value: "github.com/acme/very-long-service",
      options: [
        {
          value: "github.com/acme/very-long-service",
          label: "github.com/acme/very-long-service",
          triggerLabel: "acme/very-long-service",
        },
      ],
      title: "Repository",
    });

    expect(
      screen
        .getByRole("combobox", {
          name: "Repository: acme/very-long-service",
        })
        .textContent?.trim(),
    ).toBe("acme/very-long-service");
  });

  it("closes when focus moves outside the dropdown", async () => {
    const { container } = renderDropdown();
    const outside = document.createElement("button");
    document.body.append(outside);

    await fireEvent.click(screen.getByRole("combobox", { name: "Reviewing" }));
    await fireEvent.focusOut(container.querySelector(".select-dropdown")!, {
      relatedTarget: outside,
    });

    expect(screen.queryByRole("listbox")).toBeNull();
  });
});
