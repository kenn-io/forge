import { afterEach, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { createMockApiHandler, jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

let mounted: MountedBrowserApp | undefined;
afterEach(async () => {
  mounted?.unmount();
  localStorage.clear();
  await resetKeyboardModuleState();
});

it("accumulates searched and linked items, remembers removals, and adds a revisited target again", async () => {
  await page.viewport(1280, 900);
  const fixture = createMockApiHandler();
  const pull = await fixture
    .handle({ method: "GET", url: new URL("http://localhost/api/v1/pulls/github/acme/widgets/55"), bodyText: "" })
    .json();
  const repo = { ...pull.repo, platform_repo_id: 1001 };
  pull.repo = repo;
  pull.merge_request.repo = repo;
  pull.merge_request.Body = "[Related issue](https://github.com/acme/widgets/issues/7)";
  const workspace = {
    id: "ws-visits",
    repo,
    platform: "github",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    item_type: "adhoc",
    item_number: 0,
    source_item_visible: true,
    git_head_ref: "work",
    worktree_path: "/tmp/worktrees/visits",
    status: "ready",
    enrichment_status: "fresh",
    created_at: "2026-04-10T12:00:00Z",
    tmux_session: "",
    tmux_working: false,
  };
  const saved = new Map<string, { type: string; number: number }>();
  const overrides: MockRouteOverride = (request) => {
    const path = request.url.pathname;
    if (path === "/api/v1/workspaces") return jsonResponse({ workspaces: [workspace] });
    if (path === "/api/v1/workspaces/ws-visits") return jsonResponse(workspace);
    if (path === "/api/v1/workspaces/ws-visits/runtime") return jsonResponse({ launch_targets: [], sessions: [] });
    if (path === "/api/v1/workspaces/ws-visits/targets") {
      if (request.method === "PUT") {
        const body = JSON.parse(request.bodyText);
        expect(body.repository).toEqual({ provider: "github", platform_host: "github.com", platform_repo_id: 1001 });
        const key = `${body.type}:${body.number}`;
        if (body.hidden) saved.delete(key);
        else saved.set(key, { type: body.type, number: body.number });
        return new Response(null, { status: 204 });
      }
      return jsonResponse({
        kata_available: false,
        targets: [...saved.values()].map((item, i) => ({
          ...item,
          id: i + 1,
          repo,
          title: `Visited ${item.type} ${item.number}`,
          state: "open",
          source: "tracked",
          unavailable: false,
          url: "",
        })),
      });
    }
    if (/\/pulls\/github\/acme\/widgets\/55(?:\/sync(?:\/async)?)?$/.test(path)) return jsonResponse(pull);
    if (path === "/api/v1/repo/github/acme/widgets/resolve/7")
      return jsonResponse({ item_type: "issue", repo_tracked: true });
    return null;
  };
  mounted = await mountBrowserApp("/terminal/ws-visits", { overrides: [overrides] });
  await page.getByRole("button", { name: "Search PRs and issues", exact: true }).click();
  await page.getByRole("combobox", { name: "Search PRs and issues" }).fill("#55");
  await page.getByRole("option", { name: /#55.*Refactor theme system/ }).click();
  await page.getByText("Targets", { exact: false }).first().click();
  const targets = page.getByRole("list", { name: "Workspace targets" });
  await expect.element(targets.getByText("Visited pr 55")).toBeVisible();
  await page.getByRole("link", { name: "Related issue" }).click();
  await expect.element(targets.getByText("Visited issue 7")).toBeVisible();
  await expect.element(targets.getByText("Visited pr 55")).toBeVisible();
  await targets.getByRole("button", { name: "Remove PR #55 from targets" }).click();
  await expect.element(targets.getByText("Visited pr 55")).not.toBeInTheDocument();
  mounted.unmount();
  mounted = await mountBrowserApp("/terminal/ws-visits", { overrides: [overrides] });
  await page.getByText("Targets", { exact: false }).first().click();
  await expect.element(targets.getByText("Visited issue 7")).toBeVisible();
  await expect.element(targets.getByText("Visited pr 55")).not.toBeInTheDocument();
  await page.getByRole("button", { name: "Search PRs and issues", exact: true }).click();
  await page.getByRole("combobox", { name: "Search PRs and issues" }).fill("#55");
  await page.getByRole("option", { name: /#55.*Refactor theme system/ }).click();
  await expect.element(targets.getByText("Visited pr 55")).toBeVisible();
  expect(saved.size).toBe(2);
}, 30_000);
