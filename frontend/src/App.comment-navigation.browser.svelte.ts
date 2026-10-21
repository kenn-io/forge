import { afterEach, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import {
  firePopstate,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, mockPullFixture, mockIssueFixture, type MockRouteOverride } from "./test/mockApiFetch.js";

let mounted: MountedBrowserApp | null = null;
afterEach(async () => {
  mounted?.unmount();
  mounted = null;
  localStorage.clear();
  await resetKeyboardModuleState();
});

const events = Array.from({ length: 30 }, (_, index) => ({
  ID: index + 1,
  PlatformID: index + 101,
  EventType: "issue_comment",
  Author: "contributor",
  Body: `Discussion ${index + 1}\n\n${"Example text. ".repeat(50)}`,
  Summary: "",
  MetadataJSON: "",
  DedupeKey: `comment-${index + 101}`,
  CreatedAt: `2026-01-01T12:${String(index).padStart(2, "0")}:00Z`,
}));
const pull = mockPullFixture(42);
const issue = mockIssueFixture(7);
const override: MockRouteOverride = (req) => {
  const isPull = /\/pulls\/github\/acme\/widgets\/42(?:\/sync\/async)?$/.test(req.url.pathname);
  const isIssue = /\/issues\/github\/acme\/widgets\/7(?:\/sync\/async)?$/.test(req.url.pathname);
  if (!isPull && !isIssue) return null;
  const item = isPull ? pull : issue;
  return jsonResponse({
    ...(isPull ? { merge_request: item } : { issue: item }),
    repo: item.repo,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    events: events.map((event) => ({ ...event, ...(isPull ? { MergeRequestID: item.ID } : { IssueID: item.ID }) })),
    detail_loaded: true,
    detail_fetched_at: "2026-01-01T13:00:00Z",
    worktree_links: [],
  });
};

it("opens shell-style history navigations at the requested PR or issue comment", async () => {
  await page.viewport(1280, 900);
  const route = "/host/github.com/pulls/github/acme/widgets/42";
  mounted = await mountBrowserApp(`${route}#issuecomment-125`, { overrides: [override] });
  await expect.poll(() => document.activeElement?.textContent, { timeout: 10_000 }).toContain("Discussion 25");
  const scroller = document.querySelector<HTMLElement>(".pull-detail")!;
  const target = document.activeElement as HTMLElement;
  expect(target.getBoundingClientRect().top).toBeGreaterThanOrEqual(0);
  expect(target.getBoundingClientRect().top).toBeLessThan(900);
  expect(scroller).not.toBeNull();

  firePopstate(`${route}#issuecomment-102`);
  await expect.poll(() => document.activeElement?.getAttribute("data-comment-id")).toBe("2");
  (document.activeElement as HTMLElement).blur();
  firePopstate(`${route}#issuecomment-102`);
  await expect.poll(() => document.activeElement?.getAttribute("data-comment-id")).toBe("2");
  firePopstate("/host/github.com/issues/github/acme/widgets/7#issuecomment-124");
  await expect.poll(() => document.activeElement?.textContent, { timeout: 10_000 }).toContain("Discussion 24");
});
