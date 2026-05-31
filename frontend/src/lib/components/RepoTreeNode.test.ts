import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

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
