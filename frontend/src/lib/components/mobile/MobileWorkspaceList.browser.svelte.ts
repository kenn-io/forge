import { render } from "vitest-browser-svelte";
import { page } from "vite-plus/test/browser";
import { afterEach, expect, it, vi } from "vite-plus/test";
import "../../../app.css";
import MobileWorkspaceList from "./MobileWorkspaceListTestHarness.svelte";

const mockGet = vi.fn();
const mockPost = vi.fn();
const mockDelete = vi.fn();
const detailStore = { isPullMerging: () => false };

vi.mock("../../context.js", () => ({ getStores: () => ({ detail: detailStore }) }));

vi.mock("../../app/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/runtime.js")>();
  const { makeGeneratedClientFromRouteMocks } = await import("../../testing/test/route-mock-client.js");
  const client = {
    DELETE: (...args: unknown[]) => mockDelete(...args),
    GET: (...args: unknown[]) => mockGet(...args),
    POST: (...args: unknown[]) => mockPost(...args),
  };
  return { ...actual, makeAppRuntime: () => actual.makeAppRuntime(makeGeneratedClientFromRouteMocks(client)) };
});

class MockEventSource {
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  close = vi.fn();
}

const fixture = {
  id: "ws-1",
  created_at: "2026-08-11T12:00:00Z",
  git_head_ref: "feature/mobile-workspaces",
  item_number: 42,
  item_type: "pull_request",
  source_item_visible: true,
  platform_host: "github.com",
  repo_name: "widgets",
  repo_owner: "acme",
  status: "ready",
  tmux_activity_source: "unknown",
  tmux_last_output_at: null,
  tmux_working: false,
  worktree_path: "/tmp/ws-1",
  mr_title: "Build mobile workspaces",
  mr_state: "open",
  mr_additions: 120,
  mr_deletions: 12,
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  },
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

it("colors mobile PR badges by lifecycle state using the desktop palette", async () => {
  localStorage.clear();
  vi.stubGlobal("EventSource", MockEventSource);
  await page.viewport(390, 844);
  mockGet.mockImplementation((path: string) =>
    Promise.resolve({
      data:
        path === "/snapshot"
          ? {
              hosts: [],
              workspaces: [
                { ...fixture, id: "open", item_number: 1 },
                { ...fixture, id: "merged", item_number: 2, mr_state: "merged" },
                { ...fixture, id: "closed", item_number: 3, mr_state: "closed" },
                { ...fixture, id: "draft", item_number: 4, mr_is_draft: true },
                {
                  ...fixture,
                  id: "associated",
                  item_number: 0,
                  item_type: "adhoc",
                  associated_pr_number: 5,
                  mr_state: "merged",
                },
              ],
            }
          : {},
    }),
  );
  render(MobileWorkspaceList, { onOpen: vi.fn(), onOpenItem: vi.fn() });
  for (const [number, accent] of [
    [1, "green"],
    [2, "purple"],
    [3, "red"],
    [4, "amber"],
    [5, "purple"],
  ] as const) {
    const badge = page.getByRole("button", { name: `Open linked item #${number}`, exact: true });
    await expect.element(badge).toBeVisible();
    const swatch = document.createElement("span");
    swatch.style.backgroundColor = `color-mix(in srgb, var(--accent-${accent}) 70%, #ffffff)`;
    swatch.style.color = `color-mix(in srgb, var(--accent-${accent}) 25%, var(--bubble-ink))`;
    document.body.append(swatch);
    try {
      expect(getComputedStyle(badge.element()).backgroundColor).toBe(getComputedStyle(swatch).backgroundColor);
      expect(getComputedStyle(badge.element()).color).toBe(getComputedStyle(swatch).color);
    } finally {
      swatch.remove();
    }
  }
});
