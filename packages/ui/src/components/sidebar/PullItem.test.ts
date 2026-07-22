import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { PullRequest } from "../../api/types.js";
import { HOST_STATE_KEY, STORES_KEY } from "../../context.js";
import { __resetCIWarnings } from "../../utils/ci-buckets-warn.js";
import { hashColor } from "@kenn-io/kit-ui";
import PullItem from "./PullItem.svelte";

const mkPR = (overrides: Record<string, unknown>): PullRequest =>
  ({
    Number: 1,
    Title: "title",
    Author: "x",
    State: "open",
    IsDraft: false,
    KanbanStatus: "new",
    CIStatus: "",
    CIChecksJSON: "",
    MergeableState: "clean",
    ReviewDecision: "",
    LastActivityAt: new Date().toISOString(),
    PlatformExternalID: "ext-1",
    repo_owner: "o",
    repo_name: "n",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "o",
      name: "n",
      repo_path: "o/n",
    },
    worktree_links: [],
    Starred: false,
    ...overrides,
  }) as unknown as PullRequest;

function renderItem(pr: PullRequest): void {
  render(PullItem, {
    props: {
      pr,
      selected: false,
      showRepo: false,
      repoLabel: "o/n",
      onclick: () => {},
    },
    context: new Map<symbol, unknown>([
      [STORES_KEY, { pulls: { togglePRStar: vi.fn() } }],
      [HOST_STATE_KEY, {}],
    ]),
  });
}

describe("PullItem CI cluster", () => {
  beforeEach(() => {
    __resetCIWarnings();
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders compact tokens for a mixed-state PR", () => {
    const checks = [
      {
        status: "completed",
        conclusion: "failure",
        name: "f",
        url: "",
        app: "",
      },
      {
        status: "completed",
        conclusion: "success",
        name: "p1",
        url: "",
        app: "",
      },
      {
        status: "in_progress",
        conclusion: "",
        name: "pe",
        url: "",
        app: "",
      },
    ];
    renderItem(mkPR({ CIChecksJSON: JSON.stringify(checks) }));
    expect(document.querySelector("[data-testid='ci-token-failed']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-pending']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-passed']")).not.toBeNull();
  });

  it("Pending token is static (no spin animation) in sidebar", () => {
    renderItem(
      mkPR({
        CIChecksJSON: JSON.stringify([{ status: "in_progress", conclusion: "" }]),
      }),
    );
    const pendingTok = document.querySelector("[data-testid='ci-token-pending']")!;
    expect(pendingTok.querySelector(".spin")).toBeNull();
  });

  it("hides cluster when PR has no CI", () => {
    renderItem(mkPR({}));
    expect(document.querySelector("[data-testid^='ci-token-']")).toBeNull();
  });

  it("hides cluster when CIStatus is set but CIChecksJSON is empty", () => {
    renderItem(mkPR({ CIStatus: "success", CIChecksJSON: "" }));
    expect(document.querySelector("[data-testid^='ci-token-']")).toBeNull();
  });

  it("renders unavailable token when CIChecksJSON is malformed without leaking the raw payload via title or accessible name", () => {
    const sentinel = "supersecret_sentinel_xyz";
    renderItem(mkPR({ CIChecksJSON: `{"x":"${sentinel}",`, Title: "Sample PR" }));
    expect(document.querySelector("[data-testid='ci-token-unavailable']")).not.toBeNull();
    const titleAttr = document.querySelector("[data-testid='ci-token-unavailable']")?.getAttribute("title") ?? "";
    expect(titleAttr).not.toContain(sentinel);
    expect(titleAttr).toMatch(/CI unavailable:/i);
    const button = screen.getByRole("button", { name: /Sample PR/i });
    const ciNameMatch = screen.queryByRole("button", {
      name: new RegExp(sentinel),
    });
    expect(ciNameMatch).toBeNull();
    expect(screen.getByRole("button", { name: /CI unavailable:/i })).toBe(button);
  });

  it("row button exposes 'CI unavailable:' through its accessible name for malformed CI", () => {
    renderItem(mkPR({ CIChecksJSON: "{not json", Title: "Sample PR" }));
    const titleMatch = screen.getByRole("button", {
      name: /Sample PR/i,
    });
    const ciMatch = screen.getByRole("button", {
      name: /CI unavailable:/i,
    });
    expect(ciMatch).toBe(titleMatch);
  });

  it("row button exposes the CI cluster summary through its accessible name for normal CI", () => {
    const checks = [
      {
        status: "completed",
        conclusion: "failure",
        name: "f",
        url: "",
        app: "",
      },
      {
        status: "completed",
        conclusion: "success",
        name: "p1",
        url: "",
        app: "",
      },
      {
        status: "completed",
        conclusion: "success",
        name: "p2",
        url: "",
        app: "",
      },
      {
        status: "in_progress",
        conclusion: "",
        name: "pe",
        url: "",
        app: "",
      },
    ];
    renderItem(
      mkPR({
        CIChecksJSON: JSON.stringify(checks),
        Title: "Sample PR",
      }),
    );
    const titleMatch = screen.getByRole("button", {
      name: /Sample PR/i,
    });
    expect(screen.getByRole("button", { name: /1 failed/i })).toBe(titleMatch);
    expect(screen.getByRole("button", { name: /1 pending/i })).toBe(titleMatch);
    expect(screen.getByRole("button", { name: /2 passed/i })).toBe(titleMatch);
  });

  it("dedupes console.warn across many rows with identical malformed payloads", async () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    renderItem(mkPR({ CIChecksJSON: "{not json", Number: 1 }));
    cleanup();
    renderItem(mkPR({ CIChecksJSON: "{not json", Number: 1 }));
    cleanup();
    renderItem(mkPR({ CIChecksJSON: "{not json", Number: 1 }));
    expect(spy.mock.calls.filter((c) => typeof c[0] === "string" && c[0].includes("Malformed"))).toHaveLength(1);
    spy.mockRestore();
  });

  it("renders a single Unknown token for an unknown-only payload (acceptance-matrix Unknown-only row)", () => {
    renderItem(
      mkPR({
        CIChecksJSON: JSON.stringify([
          {
            status: "completed",
            conclusion: "mystery_state",
            name: "",
            url: "",
            app: "",
          },
        ]),
      }),
    );
    expect(document.querySelector("[data-testid='ci-token-unknown']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-failed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-pending']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-passed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-skipped']")).toBeNull();
  });
});

describe("PullItem repository label", () => {
  it("keeps repo name color tied to repository identity when the display label changes", () => {
    const pr = mkPR({
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
    });
    render(PullItem, {
      props: {
        pr,
        selected: false,
        showRepo: true,
        repoLabel: "widgets",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([
        [STORES_KEY, { pulls: { togglePRStar: vi.fn() } }],
        [HOST_STATE_KEY, {}],
      ]),
    });

    expect(document.querySelector(".meta-left .repo-name")?.getAttribute("style")).toContain(
      hashColor("github|github.com|acme/widgets"),
    );
  });
});

describe("PullItem kanban status", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows a workspace indicator when the PR has an attached workspace", () => {
    renderItem(
      mkPR({
        Title: "Cache widget details",
        workspace: { id: "ws-pr-1", status: "ready" },
      }),
    );

    expect(screen.getByLabelText("Workspace attached (ready)")).toBeTruthy();
  });

  it("shows an approved indicator when the PR is approved", () => {
    renderItem(
      mkPR({
        Title: "Cache widget details",
        ReviewDecision: "APPROVED",
      }),
    );

    expect(screen.getByLabelText("PR approved")).toBeTruthy();
  });

  it("shows a changes requested indicator when the PR explicitly requests changes", () => {
    renderItem(
      mkPR({
        Title: "Cache widget details",
        ReviewDecision: "CHANGES_REQUESTED",
      }),
    );

    expect(screen.getByLabelText("Changes requested")).toBeTruthy();
  });

  it("hides the review indicator when the PR has no terminal review decision", () => {
    renderItem(
      mkPR({
        Title: "Cache widget details",
        ReviewDecision: "REVIEW_REQUIRED",
      }),
    );

    expect(screen.queryByLabelText("PR approved")).toBeNull();
    expect(screen.queryByLabelText("Changes requested")).toBeNull();
  });

  it("never renders a status chip, regardless of kanban status or PR state", () => {
    const cases = [
      { State: "open", KanbanStatus: "new" },
      { State: "open", KanbanStatus: "reviewing" },
      { State: "closed", KanbanStatus: "reviewing" },
      { State: "merged", KanbanStatus: "awaiting_merge" },
    ];

    for (const overrides of cases) {
      renderItem(
        mkPR({
          Title: "Cache widget details",
          Author: "alice",
          LastActivityAt: "2026-05-01T12:00:00Z",
          repo_owner: "acme",
          repo_name: "widgets",
          ...overrides,
        }),
      );

      expect(document.querySelector(".status-chip")).toBeNull();
      cleanup();
    }
  });
});

describe("PullItem compact layout", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders the repo name inside the meta row with no separate repo row", () => {
    render(PullItem, {
      props: {
        pr: mkPR({}),
        selected: false,
        showRepo: true,
        repoLabel: "o/n",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([
        [STORES_KEY, { pulls: { togglePRStar: vi.fn() } }],
        [HOST_STATE_KEY, {}],
      ]),
    });

    expect(document.querySelector(".meta-row .meta-left .repo-name")).not.toBeNull();
    expect(document.querySelector(".kit-chip.repo-chip")).toBeNull();
    expect(document.querySelector(".repo-row")).toBeNull();
  });

  it("renders label pills on the title line", () => {
    renderItem(
      mkPR({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "sync", color: "0075ca" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .kit-color-label")).toHaveLength(2);
    expect(screen.getByText("bug")).toBeTruthy();
    expect(screen.getByText("sync")).toBeTruthy();
    expect(document.querySelector(".label-dots")).toBeNull();
  });

  it("caps label pills at two plus an overflow count on the title line", () => {
    renderItem(
      mkPR({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "sync", color: "0075ca" },
          { name: "docs", color: "008672" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .kit-color-label")).toHaveLength(2);
    expect(screen.getByText("+1")).toBeTruthy();
    expect(document.querySelector(".label-dots")).toBeNull();
  });

  it("keeps title text, top-right number, and plain author in the two-line structure", () => {
    renderItem(mkPR({ Title: "Cache widget details", Author: "alice", Number: 7 }));

    expect(document.querySelector(".title .title-text")?.textContent).toBe("Cache widget details");
    expect(document.querySelector(".title .item-number")?.textContent).toBe("#7");
    expect(document.querySelector(".meta-left .meta-text")?.textContent).toBe("alice");
  });
});
