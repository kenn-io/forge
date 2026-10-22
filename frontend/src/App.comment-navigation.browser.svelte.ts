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

it.each([
  ["prs", "/host/github.com/pulls/github/acme/widgets/42"],
  ["issues", "/host/github.com/issues/github/acme/widgets/7"],
] as const)("reveals comments behind a maximized sibling in %s", async (surface, route) => {
  await page.viewport(1600, 1000);
  mounted = await mountBrowserApp(route, { overrides: [override] });
  await expect.poll(() => document.querySelector('[data-comment-id="25"]'), { timeout: 10_000 }).not.toBeNull();
  const { getPaneLayoutStore } = await import("./lib/stores/paneLayout.svelte.js");
  const layout = getPaneLayoutStore(surface);
  layout.splitTab("kata", layout.leafIDForTab("conversation")!, "horizontal", "after");
  await expect.poll(() => layout.paneRender()?.onScreenTabs).toContain("kata");
  const maximizeSibling = async () => {
    const pane = document.querySelector('[data-pane-key="kata"]')!;
    const button = pane
      .closest(".tabbed-panel-leaf")!
      .querySelector<HTMLButtonElement>('[data-testid="pane-toggle-zoom"]')!;
    button.click();
    await expect.poll(() => layout.paneRender()?.onScreenTabs).not.toContain("conversation");
  };
  const expectTarget = async (id: string) => {
    await expect.poll(() => document.activeElement?.getAttribute("data-comment-id")).toBe(id);
    const target = document.activeElement as HTMLElement;
    expect(target.closest("[hidden]")).toBeNull();
    expect(target.getBoundingClientRect().top).toBeGreaterThanOrEqual(0);
    expect(target.getBoundingClientRect().top).toBeLessThan(window.innerHeight);
    expect(layout.paneRender()?.onScreenTabs).toContain("conversation");
  };
  await maximizeSibling();
  expect(window.location.pathname).toBe(route);
  firePopstate(`${route}#issuecomment-125`);
  await expectTarget("25");

  // A later manual zoom stays in place until another explicit request, including
  // the same comment URL, asks to reveal the conversation again.
  await maximizeSibling();
  firePopstate(`${route}#issuecomment-125`);
  await expectTarget("25");
  await maximizeSibling();
  firePopstate(`${route}#issuecomment-102`);
  await expectTarget("2");
});
