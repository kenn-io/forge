import { cleanup, render, screen } from "@testing-library/svelte";
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

function renderItem(issue: Issue): void {
  render(IssueItem, {
    props: {
      issue,
      selected: false,
      showRepo: false,
      repoLabel: "acme/widgets",
      onclick: () => {},
    },
    context: new Map<symbol, unknown>([[STORES_KEY, { issues: { toggleIssueStar: vi.fn() } }]]),
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

  it("renders the repo chip inside the meta row with no separate repo row", () => {
    render(IssueItem, {
      props: {
        issue: mkIssue({}),
        selected: false,
        showRepo: true,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([[STORES_KEY, { issues: { toggleIssueStar: vi.fn() } }]]),
    });

    expect(document.querySelector(".meta-row .meta-left .kit-chip.repo-chip")).not.toBeNull();
    expect(document.querySelector(".repo-row")).toBeNull();
  });

  it("renders label dots on the title line instead of a label pill row", () => {
    renderItem(
      mkIssue({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "docs", color: "0075ca" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .label-dot")).toHaveLength(2);
    expect(document.querySelector(".title .label-dots")?.getAttribute("title")).toBe("bug, docs");
    expect(screen.getByText("Labels: bug, docs")).toBeTruthy();
    expect(document.querySelector(".label-row")).toBeNull();
  });

  it("keeps title text and meta number/author in the two-line structure", () => {
    renderItem(mkIssue({}));

    expect(document.querySelector(".title .title-text")?.textContent).toBe("Track workspace setup");
    expect(document.querySelector(".meta-left .meta-text")?.textContent).toContain("#2 · alice");
  });
});
