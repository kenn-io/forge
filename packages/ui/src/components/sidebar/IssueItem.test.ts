import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

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
      onclick: () => {},
    },
    context: new Map<symbol, unknown>([
      [STORES_KEY, { issues: { toggleIssueStar: vi.fn() } }],
    ]),
  });
}

describe("IssueItem", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows a workspace indicator when the issue has an attached workspace", () => {
    renderItem(mkIssue({
      workspace: { id: "ws-issue-2", status: "ready" },
    }));

    expect(screen.getByLabelText("Workspace attached (ready)")).toBeTruthy();
  });
});
