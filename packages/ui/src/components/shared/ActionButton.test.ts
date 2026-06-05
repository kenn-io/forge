import { cleanup, render, screen } from "@testing-library/svelte";
import { createRawSnippet } from "svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";
import ActionButton from "./ActionButton.svelte";

describe("ActionButton", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders a consistent shared action button contract", () => {
    render(ActionButton, {
      props: {
        tone: "danger",
        surface: "outline",
        class: "btn--close",
        label: "Close",
      },
    });

    const button = screen.getByRole("button", { name: "Close" });
    expect(button.classList.contains("action-button")).toBe(true);
    expect(button.classList.contains("action-button--danger")).toBe(true);
    expect(button.classList.contains("action-button--outline")).toBe(true);
    expect(button.classList.contains("btn--close")).toBe(true);
    expect(button.getAttribute("type")).toBe("button");
  });

  it("supports solid success buttons for primary actions", () => {
    render(ActionButton, {
      props: {
        tone: "success",
        surface: "solid",
        label: "Merge",
      },
    });

    const button = screen.getByRole("button", { name: "Merge" });
    expect(button.classList.contains("action-button--success")).toBe(true);
    expect(button.classList.contains("action-button--solid")).toBe(true);
  });

  it("renders full and short labels for responsive action rows", () => {
    render(ActionButton, {
      props: {
        label: "Approve workflows",
        shortLabel: "Workflows",
      },
    });

    const button = screen.getByRole("button", {
      name: "Approve workflows",
    });
    expect(button.querySelector(".action-button__label")?.textContent).toBe("Approve workflows");
    expect(button.querySelector(".action-button__short-label")?.textContent).toBe("Workflows");
  });

  it("renders trailing content after responsive labels", () => {
    render(ActionButton, {
      props: {
        label: "Merge",
        shortLabel: "Merge",
        trailing: createRawSnippet(() => ({
          render: () => '<span data-testid="trailing">v</span>',
        })),
      },
    });

    const button = screen.getByRole("button", { name: "Merge" });
    const labelsAndTrailing = Array.from(button.children).map((child) => child.textContent);
    expect(labelsAndTrailing).toEqual(["Merge", "Merge", "v"]);
  });
});
