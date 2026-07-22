import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import type { Issue, PullRequest } from "../../packages/ui/src/api/types.js";
import { HOST_STATE_KEY, STORES_KEY } from "../../packages/ui/src/context.js";
import IssueItem from "../../packages/ui/src/components/sidebar/IssueItem.svelte";
import PullItem from "../../packages/ui/src/components/sidebar/PullItem.svelte";

const selectedBlue = "rgb(1, 2, 3)";
const selectedBackground = "rgb(4, 5, 6)";

function selectedItemTarget(): {
  target: HTMLDivElement;
  context: Map<symbol, unknown>;
} {
  const wrapper = document.createElement("div");
  wrapper.style.setProperty("--accent-blue", selectedBlue);
  wrapper.style.setProperty("--accent-teal", "rgb(7, 8, 9)");
  wrapper.style.setProperty("--bg-row-selected", selectedBackground);
  document.body.appendChild(wrapper);

  return {
    target: wrapper,
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          pulls: { togglePRStar: vi.fn() },
          issues: { toggleIssueStar: vi.fn() },
        },
      ],
      [HOST_STATE_KEY, { getActiveWorktreeKey: () => "worktree-1" }],
    ]),
  };
}

function expectWorkspaceSelectionStyle(row: HTMLElement): void {
  const rowStyle = getComputedStyle(row);
  const titleStyle = getComputedStyle(row.querySelector<HTMLElement>(".title-text")!);

  expect(rowStyle.borderLeftColor).toBe(selectedBlue);
  expect(rowStyle.backgroundColor).toBe(selectedBackground);
  expect(titleStyle.fontWeight).toBe("600");
}

describe("PR and issue sidebar selection", () => {
  afterEach(() => {
    cleanup();
    document.body.replaceChildren();
  });

  it("uses the workspace selected-row treatment for a PR with an active worktree", () => {
    const pr = {
      Number: 1,
      Title: "Keep selected PR visible",
      Author: "alice",
      State: "open",
      IsDraft: false,
      KanbanStatus: "new",
      CIStatus: "",
      CIChecksJSON: "",
      MergeableState: "clean",
      ReviewDecision: "",
      LastActivityAt: new Date().toISOString(),
      PlatformExternalID: "ext-1",
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
      worktree_links: [{ worktree_key: "worktree-1", worktree_branch: "feature" }],
      Starred: false,
      labels: [],
    } as unknown as PullRequest;

    render(PullItem, {
      ...selectedItemTarget(),
      props: {
        pr,
        selected: true,
        showRepo: false,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
    });

    expectWorkspaceSelectionStyle(document.querySelector<HTMLElement>(".pull-item")!);
  });

  it("uses the workspace selected-row treatment for an issue", () => {
    const issue = {
      Number: 2,
      Title: "Keep selected issue visible",
      Author: "alice",
      State: "open",
      LastActivityAt: new Date().toISOString(),
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
    } as unknown as Issue;

    render(IssueItem, {
      ...selectedItemTarget(),
      props: {
        issue,
        selected: true,
        showRepo: false,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
    });

    expectWorkspaceSelectionStyle(document.querySelector<HTMLElement>(".issue-item")!);
  });
});
