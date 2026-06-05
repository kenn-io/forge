import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import RepoTreeNode from "./RepoTreeNode.svelte";

afterEach(() => {
  cleanup();
});

describe("RepoTreeNode", () => {
  it("renders a provider icon for host rows", () => {
    render(RepoTreeNode, {
      props: {
        kind: "host",
        label: "github.com",
        ariaLabel: "github.com",
        provider: "github",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    expect(screen.getByText("github.com")).toBeTruthy();
    expect(document.querySelector(".provider-icon")).toBeTruthy();
  });

  it("marks the checkbox indeterminate for the partial state", () => {
    render(RepoTreeNode, {
      props: {
        kind: "owner",
        label: "acme",
        ariaLabel: "github.com/acme",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "partial",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.indeterminate).toBe(true);
    expect(box.checked).toBe(false);
  });

  it("calls onToggleExpand when the caret is clicked", async () => {
    const onToggleExpand = vi.fn();
    render(RepoTreeNode, {
      props: {
        kind: "owner",
        label: "acme",
        ariaLabel: "github.com/acme",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand,
        onToggleSelect: vi.fn(),
      },
    });
    await fireEvent.click(screen.getByLabelText("Toggle acme"));
    expect(onToggleExpand).toHaveBeenCalledOnce();
  });

  it("exposes expanded state via aria-expanded on the caret control", () => {
    // aria-expanded lives on the caret <button>, not the role=option row
    // (which does not support the attribute), so assistive tech can tell
    // whether a collapsible group is open.
    const { unmount } = render(RepoTreeNode, {
      props: {
        kind: "owner",
        label: "acme",
        ariaLabel: "github.com/acme",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    expect(screen.getByLabelText("Toggle acme").getAttribute("aria-expanded")).toBe("true");
    unmount();

    render(RepoTreeNode, {
      props: {
        kind: "owner",
        label: "acme",
        ariaLabel: "github.com/acme",
        depth: 0,
        hasChildren: true,
        expanded: false,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    expect(screen.getByLabelText("Toggle acme").getAttribute("aria-expanded")).toBe("false");
  });

  it("calls onToggleSelect when the checkbox is clicked", async () => {
    const onToggleSelect = vi.fn();
    render(RepoTreeNode, {
      props: {
        kind: "repo",
        label: "api",
        ariaLabel: "github.com/acme/api",
        depth: 1,
        hasChildren: false,
        expanded: false,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect,
      },
    });
    await fireEvent.mouseDown(screen.getByRole("checkbox"));
    expect(onToggleSelect).toHaveBeenCalledOnce();
  });

  it("stays controlled when really clicked (native toggle suppressed)", async () => {
    // A real browser click is mousedown + mouseup + click; a native checkbox's
    // click default action toggles its own .checked, which desyncs it from the
    // controlled `checked={selectionState}` binding (selectionState only changes
    // via the parent's onToggleSelect). The component must cancel that default
    // action so the box reflects selectionState alone. Earlier tests fired
    // mousedown only, which skips the native toggle and hid this bug.
    render(RepoTreeNode, {
      props: {
        kind: "repo",
        label: "api",
        ariaLabel: "github.com/acme/api",
        depth: 1,
        hasChildren: false,
        expanded: false,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.checked).toBe(false);
    await fireEvent.click(box);
    // selectionState is still "unchecked" (parent's onToggleSelect is a stub),
    // so a correctly controlled box stays false. A native toggle would flip it.
    expect(box.checked).toBe(false);
  });

  it("renders highlighted match segments when given segments", () => {
    render(RepoTreeNode, {
      props: {
        kind: "repo",
        label: "web-ui",
        ariaLabel: "github.com/acme/web-ui",
        depth: 1,
        hasChildren: false,
        expanded: false,
        selectionState: "unchecked",
        highlighted: false,
        segments: [
          { text: "web", match: true },
          { text: "-ui", match: false },
        ],
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    const mark = document.querySelector("mark");
    expect(mark?.textContent).toBe("web");
  });
});
