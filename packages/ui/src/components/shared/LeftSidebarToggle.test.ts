import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import LeftSidebarToggle from "./LeftSidebarToggle.svelte";

describe("LeftSidebarToggle", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders the expanded-state collapse control", async () => {
    const onclick = vi.fn();

    render(LeftSidebarToggle, {
      props: {
        state: "expanded",
        label: "Workspaces sidebar",
        onclick,
      },
    });

    const button = screen.getByRole("button", {
      name: "Collapse Workspaces sidebar",
    });
    expect(button.getAttribute("title")).toBe("Collapse Workspaces sidebar");
    expect(button.classList.contains("left-sidebar-toggle")).toBe(true);

    await fireEvent.click(button);

    expect(onclick).toHaveBeenCalledTimes(1);
  });

  it("renders the collapsed-state expand control", () => {
    render(LeftSidebarToggle, {
      props: {
        state: "collapsed",
        label: "sidebar",
        class: "left-sidebar-toggle--push",
      },
    });

    const button = screen.getByRole("button", {
      name: "Expand sidebar",
    });
    expect(button.getAttribute("title")).toBe("Expand sidebar");
    expect(button.classList.contains("left-sidebar-toggle--push")).toBe(true);
  });
});
