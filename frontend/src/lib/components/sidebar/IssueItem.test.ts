import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { Issue } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import IssueItem from "./IssueItem.svelte";

const mkIssue = (overrides: Record<string, unknown>): Issue =>
  ({
    Number: 2,
    Title: "Track workspace setup",
    Author: "alice",
    State: "open",
    LastActivityAt: "2026-05-01T12:00:00Z",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    Starred: false,
    labels: [],
    ...overrides,
  }) as unknown as Issue;

// jsdom reports zero layout widths, so tests choose whether the title is
// ellipsis-truncated by stubbing the measurements the popover gate reads.
function setElementWidths(el: Element, widths: { scrollWidth: number; clientWidth: number }): void {
  Object.defineProperty(el, "scrollWidth", { configurable: true, value: widths.scrollWidth });
  Object.defineProperty(el, "clientWidth", { configurable: true, value: widths.clientWidth });
}

function renderItem(issue: Issue, useWorkspaceActivityForRecency = false): void {
  render(IssueItem, {
    props: {
      issue,
      selected: false,
      showRepo: false,
      repoLabel: "acme/widgets",
      onclick: () => {},
    },
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          issues: { toggleIssueStar: vi.fn() },
          activity: { getUseWorkspaceActivityForRecency: () => useWorkspaceActivityForRecency },
        },
      ],
    ]),
  });
}

describe("IssueItem", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows a workspace indicator when the issue has an attached workspace", () => {
    renderItem(
      mkIssue({
        workspace: { id: "ws-issue-2", status: "ready" },
      }),
    );

    expect(screen.getByLabelText("Workspace attached (ready)")).toBeTruthy();
  });
  it("renders the repo name inside the meta row with no separate repo row", () => {
    render(IssueItem, {
      props: {
        issue: mkIssue({}),
        selected: false,
        showRepo: true,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([
        [
          STORES_KEY,
          {
            issues: { toggleIssueStar: vi.fn() },
            activity: { getUseWorkspaceActivityForRecency: () => false },
          },
        ],
      ]),
    });

    expect(document.querySelector(".meta-row .meta-left .repo-name")).not.toBeNull();
    expect(document.querySelector(".kit-chip.repo-chip")).toBeNull();
    expect(document.querySelector(".repo-row")).toBeNull();
  });

  it("renders label pills on the title line", () => {
    renderItem(
      mkIssue({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "docs", color: "0075ca" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .kit-color-label")).toHaveLength(2);
    expect(screen.getByText("bug")).toBeTruthy();
    expect(screen.getByText("docs")).toBeTruthy();
    expect(document.querySelector(".label-dots")).toBeNull();
  });

  it("caps label pills at two plus an overflow count on the title line", () => {
    renderItem(
      mkIssue({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "docs", color: "0075ca" },
          { name: "help wanted", color: "008672" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .kit-color-label")).toHaveLength(2);
    expect(screen.getByText("+1")).toBeTruthy();
    expect(document.querySelector(".label-dots")).toBeNull();
  });

  it("keeps title text, top-right number, and plain author in the two-line structure", () => {
    renderItem(mkIssue({}));

    expect(document.querySelector(".title .title-text")?.textContent).toBe("Track workspace setup");
    expect(document.querySelector(".title .item-number")?.textContent).toBe("#2");
    expect(document.querySelector(".meta-left .meta-text")?.textContent).toBe("alice");
  });

  it("renders no state chip for open issues", () => {
    renderItem(mkIssue({ State: "open" }));

    expect(screen.queryByText("Open")).toBeNull();
  });

  it("renders a Closed state chip for closed issues", () => {
    renderItem(mkIssue({ State: "closed" }));

    expect(screen.getByText("Closed")).toBeTruthy();
  });

  it("shows the full title while the row has keyboard focus and the title is truncated", async () => {
    renderItem(mkIssue({ Title: "An issue title too long for the sidebar" }));
    const row = screen.getByRole("button", { name: /An issue title too long for the sidebar/i });
    setElementWidths(row.querySelector(".title-text")!, { scrollWidth: 320, clientWidth: 160 });

    await fireEvent.focusIn(row);
    const tooltip = screen.getByRole("tooltip");
    expect(Array.from(tooltip.children, (line) => line.textContent)).toEqual([
      "An issue title too long for the sidebar",
      "acme/widgets",
    ]);
    expect(row.getAttribute("aria-describedby")).toBe(tooltip.id);

    await fireEvent.focusOut(row);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("shows no focus popover when the title fits the row", async () => {
    renderItem(mkIssue({ Title: "Short issue" }));
    const row = screen.getByRole("button", { name: /Short issue/i });
    setElementWidths(row.querySelector(".title-text")!, { scrollWidth: 160, clientWidth: 160 });

    await fireEvent.focusIn(row);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
