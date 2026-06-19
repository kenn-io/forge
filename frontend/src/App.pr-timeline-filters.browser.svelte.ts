// Browser-tier reimplementation of the frontend-only cases of
// frontend/tests/e2e-full/pr-timeline-filters.spec.ts. What lives here is the
// PR detail timeline behavior that is computed in the Svelte EventTimeline
// component, independent of any backend-derived ordering: rendering the seeded
// commit and system event rows, collapsing duplicate merge/close lifecycle rows
// into one purple Merged row, hiding and restoring the system event buckets from
// the View menu, and persisting the bucket filter in localStorage across a
// reload. The app is mounted for real with the PR detail mocked at the fetch
// boundary; the event array mirrors the Go seed (internal/testutil/fixtures.go).
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource.
//
// Kept in Playwright (frontend/tests/e2e-full/pr-timeline-filters.spec.ts): the
// force-push commit-generation ordering and fresh-import ordering cases assert
// the order the Go sync computes via commit_order_key metadata (covered by
// internal/server/e2etest TestE2E_DetailTimelineReturnsForcePushCommitOrderingMetadata),
// the compact review-thread reply layout depends on backend-transformed
// review-thread event cards plus reply round-trips, and the reply-composer
// regroup case drives a live __e2e server hook. Reproducing those backend-shaped
// values in a hand fixture would assert the fixture, not the system.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;
const FILTER_STORAGE_KEY = "middleman-pr-timeline-filter";

// SHA constants mirror the widgets PR#1 force-push generations in the Go seed.
const sha = {
  commit1: "abc1111111111111111111111111111111111111",
  commit2: "abc2222222222222222222222222222222222222",
  commit3: "abc3333333333333333333333333333333333333",
  oldHead: "abc4444444444444444444444444444444444444",
  newCommit: "def3333333333333333333333333333333333333",
  newHead: "def5555555555555555555555555555555555555",
  secondMissingBefore: "abc9999999999999999999999999999999999999",
  secondHead: "def7777777777777777777777777777777777777",
  followUp: "def6666666666666666666666666666666666666",
};

function repoRef(owner: string, name: string) {
  return {
    provider: "github",
    platform_host: "github.com",
    repo_path: `${owner}/${name}`,
    owner,
    name,
    capabilities: {
      read_repositories: true,
      read_merge_requests: true,
      read_issues: true,
      read_comments: true,
      state_mutation: true,
      comment_mutation: true,
    },
  };
}

let nextEventID = 9000;

function ev(mrID: number, eventType: string, fields: Record<string, unknown>) {
  nextEventID += 1;
  return {
    ID: nextEventID,
    MergeRequestID: mrID,
    PlatformID: nextEventID,
    EventType: eventType,
    Author: "",
    Body: "",
    Summary: "",
    MetadataJSON: "",
    DedupeKey: `e-${nextEventID}`,
    CreatedAt: "2026-03-01T00:00:00Z",
    ThreadID: null,
    Resolvable: false,
    Resolved: false,
    ...fields,
  };
}

// widgets PR#1 timeline: comments, a review, two force-push generations of
// commits, three cross-references, a title rename, and a base-ref change.
function widgetsOneEvents() {
  const id = 1001;
  return [
    ev(id, "issue_comment", { Author: "bob", Body: "Looks like a solid approach. Minor nit on naming." }),
    ev(id, "issue_comment", { Author: "carol", Body: "I agree, caching here will help a lot." }),
    ev(id, "review", { Author: "bob", Summary: "APPROVED", Body: "LGTM after addressing the naming nit." }),
    ev(id, "commit", {
      Author: "alice",
      Summary: sha.commit1,
      Body: "feat: add cache store\n\nCache entries now expire when pull request detail data is refreshed.",
    }),
    ev(id, "commit", { Author: "alice", Summary: sha.commit2, Body: "feat: wire cache into handler" }),
    ev(id, "commit", { Author: "alice", Summary: sha.commit3, Body: "test: add cache unit tests" }),
    ev(id, "commit", { Author: "alice", Summary: sha.oldHead, Body: "fix: guard nil cache before rebase" }),
    ev(id, "force_push", {
      Author: "alice",
      Summary: "abc4444 -> def5555",
      MetadataJSON: JSON.stringify({ before_sha: sha.oldHead, after_sha: sha.newHead, ref: "feature/caching" }),
    }),
    ev(id, "commit", { Author: "alice", Summary: sha.newCommit, Body: "test: add cache unit tests after rebase" }),
    ev(id, "commit", { Author: "alice", Summary: sha.newHead, Body: "fix: guard nil cache after rebase" }),
    ev(id, "commit", {
      Author: "alice",
      Summary: sha.secondHead,
      Body: "fix: finish cache rebase after follow-up force push",
    }),
    ev(id, "review", {
      Author: "bob",
      Summary: "COMMENTED",
      Body: "Same timestamp reviewer note between force-push IDs.",
    }),
    ev(id, "force_push", {
      Author: "alice",
      Summary: "abc9999 -> def7777",
      MetadataJSON: JSON.stringify({
        before_sha: sha.secondMissingBefore,
        after_sha: sha.secondHead,
        ref: "feature/caching",
      }),
    }),
    ev(id, "cross_referenced", {
      Author: "carol",
      Summary: "Referenced from acme/widgets#10",
      MetadataJSON: JSON.stringify({
        source_type: "Issue",
        source_owner: "acme",
        source_repo: "widgets",
        source_number: 10,
        source_title: "Widget rendering broken on Safari",
        source_url: "https://github.com/acme/widgets/issues/10",
        is_cross_repository: false,
        will_close_target: false,
      }),
    }),
    ev(id, "cross_referenced", {
      Author: "dave",
      Summary: "Referenced from acme/tools#1",
      MetadataJSON: JSON.stringify({
        source_type: "PullRequest",
        source_owner: "acme",
        source_repo: "tools",
        source_number: 1,
        source_title: "Add CLI flag parser",
        source_url: "https://github.com/acme/tools/pull/1",
        is_cross_repository: true,
        will_close_target: false,
      }),
    }),
    ev(id, "cross_referenced", {
      Author: "mallory",
      Summary: "Referenced from other/repo#77",
      MetadataJSON: JSON.stringify({
        source_type: "PullRequest",
        source_owner: "other",
        source_repo: "repo",
        source_number: 77,
        source_title: "External follow-up PR",
        source_url: "https://github.com/other/repo/pull/77",
        is_cross_repository: true,
        will_close_target: false,
      }),
    }),
    ev(id, "renamed_title", {
      Author: "alice",
      Summary: `"Add widget cache" -> "Add widget caching layer"`,
      MetadataJSON: JSON.stringify({ previous_title: "Add widget cache", current_title: "Add widget caching layer" }),
    }),
    ev(id, "commit", { Author: "alice", Summary: sha.followUp, Body: "chore: tune cache eviction metrics" }),
    ev(id, "base_ref_changed", {
      Author: "alice",
      Summary: "develop -> main",
      MetadataJSON: JSON.stringify({ previous_ref_name: "develop", current_ref_name: "main" }),
    }),
  ];
}

// tools PR#2: duplicate lifecycle rows from provider/import paths collapse to one
// merge row in the UI.
function toolsTwoEvents() {
  const id = 2002;
  return [
    ev(id, "merged", { Author: "", Summary: "merged this", CreatedAt: "2026-03-02T10:00:00Z" }),
    ev(id, "closed", { Author: "alice", Summary: "closed this", CreatedAt: "2026-03-02T10:00:15Z" }),
    ev(id, "merged", { Author: "alice", Summary: "merged this", CreatedAt: "2026-03-02T10:00:15Z" }),
  ];
}

function pullDetail(owner: string, name: string, number: number, title: string, state: string, events: unknown[]) {
  const headSHA = `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`;
  return {
    merge_request: {
      ID: 1000 + number,
      RepoID: name === "tools" ? 2 : 1,
      GitHubID: 1100 + number,
      Number: number,
      URL: `https://github.com/${owner}/${name}/pull/${number}`,
      Title: title,
      Author: "alice",
      State: state,
      IsDraft: false,
      MergeableState: "clean",
      Body: "",
      HeadBranch: "feature/x",
      BaseBranch: "main",
      Additions: 10,
      Deletions: 1,
      CommentCount: 0,
      ReviewDecision: "",
      CIStatus: "success",
      CIChecksJSON: "[]",
      CreatedAt: "2026-02-28T14:00:00Z",
      UpdatedAt: "2026-03-02T14:00:00Z",
      LastActivityAt: "2026-03-02T14:00:00Z",
      MergedAt: state === "merged" ? "2026-03-02T10:00:15Z" : null,
      ClosedAt: state === "merged" ? "2026-03-02T10:00:15Z" : null,
      KanbanStatus: "new",
      Starred: false,
      repo_owner: owner,
      repo_name: name,
      platform_host: "github.com",
      platform_head_sha: headSHA,
      repo: repoRef(owner, name),
      worktree_links: [],
    },
    repo: repoRef(owner, name),
    events,
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: headSHA,
    reviewed_head_sha: headSHA,
    detail_loaded: true,
    detail_fetched_at: "2026-03-02T14:00:00Z",
    worktree_links: [],
  };
}

function detailOverride(
  owner: string,
  name: string,
  number: number,
  title: string,
  state: string,
  events: () => unknown[],
): MockRouteOverride {
  const re = new RegExp(`^/api/v1/pulls/github/${owner}/${name}/${number}(/sync/async)?$`);
  return (req) =>
    re.test(req.url.pathname) ? jsonResponse(pullDetail(owner, name, number, title, state, events())) : null;
}

function overrides(): MockRouteOverride[] {
  return [
    detailOverride("acme", "widgets", 1, "Add widget caching layer", "open", widgetsOneEvents),
    detailOverride("acme", "tools", 2, "Initial project setup", "merged", toolsTwoEvents),
  ];
}

function inDetail(selector: string): Element[] {
  return Array.from(document.querySelectorAll(`.pull-detail ${selector}`));
}

function detailText(selector: string): string {
  return inDetail(selector)
    .map((el) => el.textContent ?? "")
    .join(" ");
}

function eventTypeCount(label: string): number {
  return inDetail(".event-type").filter((el) => (el.textContent ?? "").trim() === label).length;
}

function viewButton(): Element {
  return inDetail(".filter-btn").find((el) => (el.textContent ?? "").includes("View"))!;
}

function filterItem(label: string): Element {
  return Array.from(document.querySelectorAll(".pull-detail .filter-dropdown .filter-item")).find((el) =>
    (el.textContent ?? "").includes(label),
  )!;
}

async function openViewMenu(): Promise<void> {
  await page.elementLocator(viewButton()).click();
  await vi.waitFor(() => expect(document.querySelector(".pull-detail .filter-dropdown")).not.toBeNull(), WAIT);
}

async function toggleBucket(label: string): Promise<void> {
  await page.elementLocator(filterItem(label)).click();
}

describe("PR timeline filters", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
    localStorage.removeItem(FILTER_STORAGE_KEY);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  async function mountTimeline(path: string): Promise<void> {
    mounted = await mountBrowserApp(path, { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".pull-detail .timeline")).not.toBeNull(), WAIT);
  }

  it("renders seeded commit and system timeline events", async () => {
    await mountTimeline("/pulls/github/acme/widgets/1");
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("feat: add cache store"), WAIT);

    expect(eventTypeCount("Force-pushed")).toBe(2);
    expect(detailText(".timeline")).toContain("abc4444 -> def5555");
    expect(detailText(".timeline")).toContain("abc9999 -> def7777");
    expect(eventTypeCount("Referenced")).toBe(3);
    expect(detailText(".timeline")).toContain("Widget rendering broken on Safari");
    expect(detailText(".timeline")).toContain("Title changed");
    expect(detailText(".timeline")).toContain('"Add widget cache" -> "Add widget caching layer"');
    expect(detailText(".timeline")).toContain("Base changed");
    expect(detailText(".timeline")).toContain("develop -> main");
  });

  it("renders merged lifecycle transitions as one purple row", async () => {
    await mountTimeline("/pulls/github/acme/tools/2");
    await vi.waitFor(() => expect(inDetail(".event--compact").length).toBeGreaterThan(0), WAIT);

    const mergedRows = inDetail(".event--compact").filter((el) => (el.textContent ?? "").includes("Merged"));
    expect(mergedRows.length).toBe(1);
    expect(mergedRows[0]!.textContent ?? "").toContain("alice");
    expect(mergedRows[0]!.textContent ?? "").toContain("merged this");
    const mergedType = mergedRows[0]!.querySelector(".event-type");
    expect(mergedType?.getAttribute("style") ?? "").toContain("var(--accent-purple)");
    expect(mergedRows[0]!.querySelector(".dot")?.getAttribute("style") ?? "").toContain("var(--accent-purple)");
    expect(eventTypeCount("Closed")).toBe(0);
  });

  it("keeps commit rows while hiding and restoring system event buckets", async () => {
    await mountTimeline("/pulls/github/acme/widgets/1");
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("feat: add cache store"), WAIT);
    await openViewMenu();

    const cacheCommitRow = (): Element =>
      inDetail(".event--compact").find((el) => (el.textContent ?? "").includes("abc1111"))!;

    // .commit-body-details slides out over 100ms (transition:slide), so it lingers
    // in the DOM after the title appears; assert the count inside waitFor so the
    // slide-out has completed.
    await toggleBucket("Commit details");
    await vi.waitFor(() => {
      expect(cacheCommitRow().querySelector(".commit-title")?.textContent ?? "").toBe("feat: add cache store");
      expect(cacheCommitRow().querySelectorAll(".commit-body-details").length).toBe(0);
    }, WAIT);

    await toggleBucket("Commit details");
    await vi.waitFor(() => {
      expect(cacheCommitRow().querySelectorAll(".commit-title").length).toBe(0);
      expect(cacheCommitRow().querySelector(".commit-body-details")?.textContent ?? "").toContain(
        "Cache entries now expire",
      );
    }, WAIT);

    await toggleBucket("Events");
    await vi.waitFor(() => expect(detailText(".timeline")).not.toContain("Widget rendering broken on Safari"), WAIT);
    expect(detailText(".timeline")).not.toContain('"Add widget cache" -> "Add widget caching layer"');
    expect(detailText(".timeline")).not.toContain("develop -> main");

    await toggleBucket("Events");
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("Widget rendering broken on Safari"), WAIT);

    await toggleBucket("Force pushes");
    await vi.waitFor(() => expect(detailText(".timeline")).not.toContain("abc4444 -> def5555"), WAIT);
    expect(detailText(".timeline")).not.toContain("abc9999 -> def7777");
    // The rebase commits stay visible once their force-push rows are hidden.
    expect(detailText(".timeline")).toContain("fix: guard nil cache after rebase");

    await toggleBucket("Force pushes");
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("abc4444 -> def5555"), WAIT);
  });

  it("persists timeline filter preferences in localStorage", async () => {
    await mountTimeline("/pulls/github/acme/widgets/1");
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("Widget rendering broken on Safari"), WAIT);
    await openViewMenu();

    await toggleBucket("Events");
    await vi.waitFor(() => expect(detailText(".timeline")).not.toContain("Widget rendering broken on Safari"), WAIT);
    const badge = inDetail('[title="View and filter activity"]')[0];
    expect(badge?.textContent ?? "").toContain("1");

    await vi.waitFor(
      () => expect(localStorage.getItem(FILTER_STORAGE_KEY) ?? "").toContain('"showEvents":false'),
      WAIT,
    );

    // Remount is the browser-tier equivalent of a reload: localStorage persists.
    mounted?.unmount();
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/1", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".pull-detail .timeline")).not.toBeNull(), WAIT);
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("feat: add cache store"), WAIT);
    expect(detailText(".timeline")).not.toContain("Widget rendering broken on Safari");
    expect(inDetail('[title="View and filter activity"]')[0]?.textContent ?? "").toContain("1");
  });

  it("keeps commit rows when other event buckets are hidden", async () => {
    await mountTimeline("/pulls/github/acme/widgets/1");
    await vi.waitFor(() => expect(detailText(".timeline")).toContain("feat: add cache store"), WAIT);
    await openViewMenu();

    await toggleBucket("Messages");
    await toggleBucket("Commit details");
    await toggleBucket("Events");
    await toggleBucket("Force pushes");

    const cacheCommitRow = (): Element =>
      inDetail(".event--compact").find((el) => (el.textContent ?? "").includes("abc1111"))!;
    await vi.waitFor(() => {
      expect(cacheCommitRow().querySelector(".commit-title")?.textContent ?? "").toBe("feat: add cache store");
      expect(cacheCommitRow().querySelectorAll(".commit-body-details").length).toBe(0);
    }, WAIT);
    expect(detailText(".timeline")).not.toContain("No activity matches the current filters");
  });
});
