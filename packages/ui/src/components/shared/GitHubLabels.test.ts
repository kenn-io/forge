import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";
import GitHubLabels from "./GitHubLabels.svelte";
import type { IssueLabel } from "../../api/types.js";

function makeLabel(overrides: Partial<IssueLabel> = {}): IssueLabel {
  return {
    name: "bug",
    color: "d73a4a",
    description: "",
    is_default: false,
    ...overrides,
  };
}

describe("GitHubLabels", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows compact overflow after two labels", () => {
    render(GitHubLabels, {
      props: {
        mode: "compact",
        labels: [
          makeLabel({ name: "bug" }),
          makeLabel({ name: "feature", color: "a2eeef" }),
          makeLabel({ name: "docs", color: "0075ca" }),
        ],
      },
    });

    expect(screen.getByText("bug")).toBeTruthy();
    expect(screen.getByText("feature")).toBeTruthy();
    expect(screen.queryByText("docs")).toBeNull();
    expect(screen.getByText("+1")).toBeTruthy();
  });

  it("renders all labels in full mode", () => {
    render(GitHubLabels, {
      props: {
        mode: "full",
        labels: [
          makeLabel({ name: "bug" }),
          makeLabel({ name: "feature", color: "a2eeef" }),
          makeLabel({ name: "docs", color: "0075ca" }),
        ],
      },
    });

    expect(screen.getByText("bug")).toBeTruthy();
    expect(screen.getByText("feature")).toBeTruthy();
    expect(screen.getByText("docs")).toBeTruthy();
    expect(screen.queryByText("+1")).toBeNull();
  });

  it("uses readable foreground colors for light and dark labels", () => {
    render(GitHubLabels, {
      props: {
        mode: "full",
        labels: [makeLabel({ name: "light", color: "fef2c0" }), makeLabel({ name: "dark", color: "0f172a" })],
      },
    });

    expect(getComputedStyle(screen.getByText("light")).color).toBe("rgb(31, 35, 40)");
    expect(getComputedStyle(screen.getByText("dark")).color).toBe("rgb(255, 255, 255)");
  });

  it("chooses the better contrast text color for saturated mid-tone labels", () => {
    render(GitHubLabels, {
      props: {
        mode: "full",
        labels: [makeLabel({ name: "ready", color: "28a745" })],
      },
    });

    expect(getComputedStyle(screen.getByText("ready")).color).toBe("rgb(31, 35, 40)");
  });

  it("uses white text when it has better contrast than the dark foreground", () => {
    render(GitHubLabels, {
      props: {
        mode: "full",
        labels: [makeLabel({ name: "success", color: "0e8a16" })],
      },
    });

    expect(getComputedStyle(screen.getByText("success")).color).toBe("rgb(255, 255, 255)");
  });

  it("renders nothing for null, undefined, and empty label inputs", () => {
    const { container, rerender } = render(GitHubLabels, {
      props: {
        mode: "full",
        labels: null,
      },
    });

    expect(container.querySelector(".github-labels")).toBeNull();
    expect(container.querySelectorAll(".label-pill")).toHaveLength(0);

    rerender({
      mode: "full",
    });

    expect(container.querySelector(".github-labels")).toBeNull();
    expect(container.querySelectorAll(".label-pill")).toHaveLength(0);

    rerender({
      mode: "compact",
      labels: [],
    });

    expect(container.querySelector(".github-labels")).toBeNull();
    expect(container.querySelectorAll(".label-pill")).toHaveLength(0);
  });
});
