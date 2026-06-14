// PR-detail palette commands (`pr.approve`, `pr.ready`,
// `pr.approveWorkflows`). The merge palette command is intentionally not
// registered (the trigger lives in PullDetail.svelte's local component
// state). The app is mounted for real so the asserted approve POST flows
// through the same closure the detail-pane button uses.

import { cleanup, screen, waitFor } from "@testing-library/svelte";
import { fireEvent } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, pressKey, resetKeyboardModuleState } from "./test/appHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const capabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  label_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  issue_mutation: true,
  review_draft_mutation: false,
  review_thread_resolution: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  mutation_head_binding: false,
  thread_reply: false,
  thread_resolve: false,
  supported_review_actions: [],
};

const repo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  capabilities,
};

const closedPR55: MockRouteOverride = (req) => {
  if (req.method !== "GET" || req.url.pathname !== "/api/v1/pulls/github/acme/widgets/55") return null;
  return jsonResponse({
    merge_request: {
      ID: 3,
      RepoID: 1,
      GitHubID: 301,
      Number: 55,
      URL: "https://github.com/acme/widgets/pull/55",
      Title: "Refactor theme system",
      Author: "luisa",
      State: "closed",
      IsDraft: false,
      Body: "Consolidates theme tokens.",
      HeadBranch: "refactor/theme",
      BaseBranch: "main",
      Additions: 80,
      Deletions: 40,
      CommentCount: 0,
      ReviewDecision: "",
      CIStatus: "pending",
      CIChecksJSON: "[]",
      CreatedAt: "2026-03-29T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      MergedAt: null,
      ClosedAt: "2026-03-30T14:00:00Z",
      KanbanStatus: "new",
      Starred: false,
      repo_owner: "acme",
      repo_name: "widgets",
      platform_host: "github.com",
      repo,
      worktree_links: [],
    },
    events: [],
    repo,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  });
};

function paletteInput(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>(".palette-input");
  expect(input).not.toBeNull();
  return input!;
}

async function openPaletteWith(query: string): Promise<void> {
  pressKey("k", { meta: true });
  await waitFor(() => {
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeNull();
    expect(document.activeElement).toBe(paletteInput());
  });
  await fireEvent.input(paletteInput(), { target: { value: query } });
}

function paletteRowsNamed(pattern: RegExp): HTMLElement[] {
  // Palette rows render as <button class="palette-row">; query by the
  // actual role so a regression that surfaces the command anyway would
  // fail this assertion.
  return screen.queryAllByRole("button", { name: pattern });
}

describe("PR-detail palette commands", () => {
  vi.setConfig({ testTimeout: 20_000 });

  beforeEach(() => {
    installAppDomGlobals();
  });

  afterEach(async () => {
    cleanup();
    vi.unstubAllGlobals();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("Approve PR runs from the palette and triggers the approve flow", async () => {
    const app = await mountApp("/pulls/github/acme/widgets/42");
    await waitFor(() => expect(document.body.textContent).toContain("Adds Playwright smoke tests"));

    await openPaletteWith("approve pr");
    // The command must actually surface for the open, approvable PR —
    // this also keeps the absence assertions below non-vacuous.
    await waitFor(() => expect(paletteRowsNamed(/Approve PR/i).length).toBeGreaterThan(0));

    pressKey("Enter", {}, paletteInput());

    // The action wires through the same closure the detail-pane approve
    // button uses; observable effect is the approve POST on the wire.
    const approvePost = await waitFor(() => {
      const post = app.api.requests.find(
        (req) => req.method === "POST" && req.url.pathname === "/api/v1/pulls/github/acme/widgets/42/approve",
      );
      expect(post).toBeTruthy();
      return post!;
    });

    // The approve must pin the head the detail view rendered so the server
    // can reject the action when the head moved after review.
    const body = JSON.parse(approvePost.bodyText || "{}") as { expected_head_sha?: string };
    expect(body.expected_head_sha).toBe("42aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa42");
  });

  it("Approve PR is absent from the palette when the PR is closed", async () => {
    await mountApp("/pulls/github/acme/widgets/55", { overrides: [closedPR55] });
    await waitFor(() => expect(document.body.textContent).toContain("Consolidates theme tokens"));

    await openPaletteWith("approve pr");

    expect(paletteRowsNamed(/Approve PR/i)).toHaveLength(0);
  });

  it("Mark ready for review appears only when the PR is a draft", async () => {
    await mountApp("/pulls/github/acme/widgets/42");
    await waitFor(() => expect(document.body.textContent).toContain("Adds Playwright smoke tests"));

    await openPaletteWith("ready for review");

    // Non-draft PR; the action should be filtered out by `when`.
    expect(paletteRowsNamed(/Mark ready for review/i)).toHaveLength(0);
  });
});
