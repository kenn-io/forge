import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { PREvent } from "../../api/types.js";
import PRReviewTimelineTestHarness from "./PRReviewTimelineTestHarness.svelte";

const HEAD = "96027d7aa1b2c3d4e5f60718293a4b5c6d7e8f90";
let runtime: OwnedAppRuntime;
let reviewOutput: string;

const providerComment: PREvent = {
  Author: "roborev-ci[bot]",
  Body: "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`180f8e3`)\n\n**Verdict:** Changes require fixes for 1 finding.\n\n### High\n\n- `widget.go:3`: Leaks the connection\n",
  CreatedAt: "2026-11-10T18:00:00Z",
  DedupeKey: "comment-31",
  DirectURL: "https://github.com/acme/widgets/pull/7#issuecomment-31",
  EventType: "issue_comment",
  ID: 31,
  MergeRequestID: 1,
  MetadataJSON: "",
  PlatformExternalID: "31",
  PlatformID: 31,
  Resolvable: false,
  Resolved: false,
  Summary: "",
  ThreadID: null,
};

const localJob = {
  id: 8,
  agent: "codex",
  agentic: false,
  enqueued_at: "2026-11-10T19:00:00Z",
  finished_at: "2026-11-10T19:05:00Z",
  git_ref: HEAD,
  job_type: "review",
  prompt_prebuilt: false,
  repo_id: 1,
  retry_count: 0,
  status: "done",
  verdict: "P",
  finding_counts: { critical: 0, high: 0, medium: 0, low: 0 },
};

function urlOf(input: RequestInfo | URL): URL {
  return new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, "http://localhost");
}

function stubServer(options: { daemonAvailable: boolean }): URL[] {
  const calls: URL[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
    const url = urlOf(input);
    calls.push(url);
    switch (url.pathname) {
      case "/api/v1/roborev/status":
        return Response.json({
          available: options.daemonAvailable,
          version: "0.75.0",
          endpoint: "http://127.0.0.1:7373",
        });
      case "/api/roborev/api/status":
        return Response.json({
          active_workers: 0,
          applied_jobs: 0,
          canceled_jobs: 0,
          completed_jobs: 1,
          failed_jobs: 0,
          max_workers: 2,
          queue_paused: false,
          queued_jobs: 0,
          rebased_jobs: 0,
          running_jobs: 0,
          skipped_jobs: 0,
          version: "0.75.0",
        });
      case "/api/v1/workspaces/ws-1":
        return Response.json({
          id: "ws-1",
          worktree_path: "/work/widgets",
          git_head_ref: "feature/widgets",
          status: "ready",
        });
      case "/api/roborev/api/repos/resolve":
        return Response.json({ tracked: true, repo: { root_path: "/repos/widgets", identity: "", name: "widgets" } });
      case "/api/roborev/api/jobs":
        return Response.json({ jobs: [localJob], has_more: false });
      case "/api/roborev/api/review":
        return Response.json({
          id: 80,
          job_id: 8,
          agent: "codex",
          closed: false,
          created_at: "2026-11-10T19:05:00Z",
          output: reviewOutput,
          prompt: "",
        });
      case "/api/roborev/api/comments":
        return Response.json({ responses: [] });
      default:
        return Response.json({}, { status: 404 });
    }
  });
  return calls;
}

function renderTimeline(workspaceId: string | null) {
  return render(PRReviewTimelineTestHarness, {
    props: { runtime, timelineProps: { events: [providerComment], headSha: HEAD, workspaceId } },
  });
}

describe("PRReviewTimeline", () => {
  beforeEach(() => {
    runtime = makeAppRuntime();
    reviewOutput = "Local review found nothing";
  });

  afterEach(async () => {
    cleanup();
    vi.restoreAllMocks();
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("lists provider and workspace reviews in order and opens each in a drawer", async () => {
    const calls = stubServer({ daemonAvailable: true });
    renderTimeline("ws-1");

    await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(2));
    const [first, second] = screen.getAllByRole("listitem");
    expect(first?.textContent).toContain("180f8e3");
    expect(first?.textContent).toContain("1 finding");
    expect(first?.textContent).toContain("Superseded");
    expect(second?.textContent).toContain("96027d7");
    expect(second?.textContent).toContain("Clean");
    expect(second?.textContent).toContain("Current");

    const jobsQuery = calls.find((url) => url.pathname === "/api/roborev/api/jobs")?.searchParams;
    expect(jobsQuery?.get("repo")).toBe("/repos/widgets");
    expect(jobsQuery?.get("branch")).toBe("feature/widgets");
    expect(jobsQuery?.get("include_findings")).toBe("true");
    expect(calls.find((url) => url.pathname === "/api/roborev/api/repos/resolve")?.searchParams.get("path")).toBe(
      "/work/widgets",
    );

    await fireEvent.click(within(first as HTMLElement).getByRole("button"));
    await waitFor(() => expect(screen.getByText("Leaks the connection", { exact: false })).toBeTruthy());

    await fireEvent.click(within(second as HTMLElement).getByRole("button"));
    await waitFor(() => expect(screen.getByText("Local review found nothing")).toBeTruthy());
    expect(screen.queryByText("Leaks the connection", { exact: false })).toBeNull();
    expect(calls.find((url) => url.pathname === "/api/roborev/api/review")?.searchParams.get("job_id")).toBe("8");
  });

  it("reloads the open local review when the timeline refreshes", async () => {
    stubServer({ daemonAvailable: true });
    renderTimeline("ws-1");

    await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(2));
    await fireEvent.click(within(screen.getAllByRole("listitem")[1] as HTMLElement).getByRole("button"));
    await waitFor(() => expect(screen.getByText("Local review found nothing")).toBeTruthy());

    reviewOutput = "Local review finished later";
    await fireEvent.click(screen.getByRole("button", { name: "Refresh Roborev reviews" }));
    await waitFor(() => expect(screen.getByText("Local review finished later")).toBeTruthy());
  });

  it("keeps provider reviews when the local daemon is unreachable", async () => {
    stubServer({ daemonAvailable: false });
    renderTimeline("ws-1");

    await waitFor(() => expect(screen.getByText("Roborev daemon not reachable")).toBeTruthy());
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });

  it("reads nothing from the daemon without a workspace", async () => {
    const calls = stubServer({ daemonAvailable: true });
    renderTimeline(null);

    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(calls.filter((url) => url.pathname.includes("roborev"))).toEqual([]);
  });
});
