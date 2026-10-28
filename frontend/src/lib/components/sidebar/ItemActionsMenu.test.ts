import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { GeneratedProblemResponse } from "../../api/runtime.js";
import type { Issue, PullRequest } from "../../api/types.js";
import { createMockApiFetch, mockIssueFixture, mockPullFixture } from "../../../test/mockApiFetch.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import type { ListItemTarget } from "../../stores/item-actions.js";
import ItemActionsMenu from "./ItemActionsMenuTestHarness.svelte";

const pullOnGitHub = mockPullFixture(42) as unknown as PullRequest;
const pullOnMirror = mockPullFixture(84) as unknown as PullRequest;
const issue = mockIssueFixture(7) as unknown as Issue;

const mixed: ListItemTarget[] = [
  { kind: "pull", item: pullOnGitHub },
  { kind: "issue", item: issue },
  { kind: "pull", item: pullOnMirror },
];

describe("ItemActionsMenu with a mixed selection", () => {
  const originalFetch = globalThis.fetch;
  const mergePull = vi.fn();
  const mergePullOnHost = vi.fn();
  const getRepo = vi.fn();
  const getRepoOnHost = vi.fn();

  beforeEach(() => {
    globalThis.fetch = createMockApiFetch().fetch;
    mergePull.mockReset().mockResolvedValue({ merged: true, sha: "", message: "" });
    mergePullOnHost.mockReset().mockResolvedValue({ merged: true, sha: "", message: "" });
    // The two repositories share only rebase merges.
    getRepo
      .mockReset()
      .mockResolvedValue({ AllowSquashMerge: true, AllowMergeCommit: false, AllowRebaseMerge: true, operations: {} });
    getRepoOnHost
      .mockReset()
      .mockResolvedValue({ AllowSquashMerge: false, AllowMergeCommit: true, AllowRebaseMerge: true, operations: {} });
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  function renderMenu(targets: readonly ListItemTarget[]) {
    return render(ItemActionsMenu, {
      props: {
        client: makeGeneratedClient({
          PullRequestsService: { mergePull, mergePullOnHost },
          RepositoriesService: { getRepo, getRepoOnHost },
        }),
        anchor: { x: 10, y: 10 },
        returnFocus: undefined,
        targets,
        selectionActive: true,
        onClearSelection: () => {},
        onChanged: () => {},
      },
    });
  }

  it("counts items rather than calling them all pull requests", () => {
    renderMenu(mixed);

    expect(screen.getByRole("menu", { name: "Actions for 3 items" })).toBeTruthy();
    expect(screen.getByRole("menuitem", { name: "Star 3 items" })).toBeTruthy();
  });

  it("merges only the pull requests in the selection, with a method both repositories allow", async () => {
    renderMenu(mixed);

    await fireEvent.click(screen.getByRole("menuitem", { name: "Merge 2 of 3 items..." }));
    const dialog = await screen.findByRole("dialog", { name: "Merge 2 PRs?" });
    const confirm = await waitFor(() => {
      const button = within(dialog).getByRole("button", { name: "Merge 2 PRs" });
      expect(button.hasAttribute("disabled")).toBe(false);
      return button;
    });
    // With one shared method there is nothing to choose.
    expect(within(dialog).queryByRole("radiogroup", { name: "Merge method" })).toBeNull();
    await fireEvent.click(confirm);

    await waitFor(() => expect(mergePull.mock.calls.length + mergePullOnHost.mock.calls.length).toBe(2));
    const calls = [...mergePull.mock.calls, ...mergePullOnHost.mock.calls];
    expect(calls.map((call) => call[0].number).sort()).toEqual([42, 84]);
    expect(calls.map((call) => call[1].method)).toEqual(["rebase", "rebase"]);
    expect(mergePull.mock.calls[0]?.[1]).toMatchObject({
      commit_title: "Add browser regression coverage (#42)",
      expected_head_sha: pullOnGitHub.platform_head_sha,
    });
  });

  it("says when a repository's merge settings could not load instead of loading forever", async () => {
    const failure = { type: "about:blank", title: "Internal error", status: 500, code: "internal", detail: "boom" };
    getRepo.mockRejectedValue(new GeneratedProblemResponse(failure, Response.json(failure, { status: 500 })));
    // A repository no earlier test loaded, so no cached methods hide the failure.
    const gadgets = { ...pullOnGitHub, repo: { ...pullOnGitHub.repo, name: "gadgets", repo_path: "acme/gadgets" } };
    renderMenu([{ kind: "pull", item: gadgets }]);

    await fireEvent.click(screen.getByRole("menuitem", { name: "Merge PR..." }));
    const dialog = await screen.findByRole("dialog", { name: "Merge 1 PR?" });

    await waitFor(() =>
      expect(dialog.textContent).toContain(
        "Could not load merge settings for acme/gadgets. Close and reopen this dialog to try again.",
      ),
    );
    expect(dialog.textContent).not.toContain("Loading merge methods...");
    expect(within(dialog).getByRole("button", { name: "Merge 1 PR" }).hasAttribute("disabled")).toBe(true);
  });

  it("explains why a pull request with failed CI cannot be merged from the list", () => {
    renderMenu([{ kind: "pull", item: { ...pullOnGitHub, CIStatus: "failure" } }]);

    const merge = screen.getByRole("menuitem", { name: /^Merge PR/ });
    expect(merge.getAttribute("aria-disabled") === "true" || merge.hasAttribute("disabled")).toBe(true);
    expect(merge.textContent).toContain("CI failed");
  });

  it("offers no merge for issues alone", () => {
    renderMenu([mixed[1]!]);

    expect(screen.queryByRole("menuitem", { name: /^Merge/ })).toBeNull();
  });
});
