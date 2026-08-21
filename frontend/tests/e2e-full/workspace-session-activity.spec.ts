import { execFileSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  devices,
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

import { startIsolatedWorkspaceE2EServer } from "./support/e2eServer";

type WorkspaceResponse = {
  id: string;
  status: string;
  error_message?: string | null;
  tmux_activity_source?: string;
};

type ActivityResponse = {
  items: Array<{ item_number: number; item_type: string }> | null;
  item_activity: Array<{ item_number: number; item_type: string }> | null;
  workspace_activity: Array<{ item_number: number; item_type: string; activity_at: string }> | null;
};

type PullResponse = Array<{
  Number: number;
  LastActivityAt: string;
  workspace?: { id: string; status: string };
  last_workspace_activity_at?: string;
}>;

type IssueResponse = PullResponse;

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceID: string): Promise<void> {
  await expect
    .poll(async () => {
      const response = await api.get(`/api/v1/workspaces/${workspaceID}`);
      expect(response.ok(), await response.text()).toBe(true);
      const workspace = (await response.json()) as WorkspaceResponse;
      if (workspace.status === "error") throw new Error(workspace.error_message ?? "workspace creation failed");
      return workspace.status;
    })
    .toBe("ready");
}

async function pull(api: APIRequestContext, number: number): Promise<PullResponse[number] | undefined> {
  const response = await api.get("/api/v1/pulls?state=open");
  expect(response.ok(), await response.text()).toBe(true);
  return ((await response.json()) as PullResponse).find((item) => item.Number === number);
}

async function issue(api: APIRequestContext, number: number): Promise<IssueResponse[number] | undefined> {
  const response = await api.get("/api/v1/issues?state=open");
  expect(response.ok(), await response.text()).toBe(true);
  return ((await response.json()) as IssueResponse).find((item) => item.Number === number);
}

async function listPulls(api: APIRequestContext): Promise<PullResponse> {
  const response = await api.get("/api/v1/pulls?state=open");
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()) as PullResponse;
}

async function listIssues(api: APIRequestContext): Promise<IssueResponse> {
  const response = await api.get("/api/v1/issues?state=open");
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()) as IssueResponse;
}

async function selectActivityViewItem(page: Page, label: string): Promise<void> {
  const panel = page.locator(".activity-filters__panel");
  if (!(await panel.isVisible())) {
    await page.locator(".activity-feed .activity-filters__trigger").click();
    await expect(panel).toBeVisible();
  }
  await panel.locator(".activity-filters__item", { hasText: label }).click();
}

async function activityStatusCount(page: Page, label: "PRs" | "issues"): Promise<number> {
  const item = page
    .getByRole("contentinfo")
    .locator(".status-item")
    .filter({ hasText: new RegExp(`^\\d+ ${label}$`) });
  await expect(item).toBeVisible();
  const text = await item.textContent();
  const count = Number.parseInt(text ?? "", 10);
  expect(count).not.toBeNaN();
  return count;
}

test.describe("workspace session activity across item surfaces", () => {
  test.describe.configure({ timeout: 120_000 });

  test("first observation keeps refs idle, then output promotes Activity, Pulls, and Issues", async ({
    browser,
    page,
  }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace activity flow",
    );

    const activitySignalDir = mkdtempSync(path.join(tmpdir(), "kenn-forge-activity-signal-"));
    const changeSignal = path.join(activitySignalDir, "change");
    const server = await startIsolatedWorkspaceE2EServer();
    const api = await playwrightRequest.newContext({ baseURL: server.info.base_url });
    try {
      const currentSettingsResponse = await api.get("/api/v1/settings");
      expect(currentSettingsResponse.ok(), await currentSettingsResponse.text()).toBe(true);
      const currentSettings = (await currentSettingsResponse.json()) as {
        activity: Record<string, unknown>;
      };
      const settings = await api.put("/api/v1/settings", {
        data: {
          agents: [
            {
              key: "activity-e2e",
              label: "Activity E2E",
              command: [
                "sh",
                "-c",
                'printf seed; : > "$1/seeded.$$"; while [ ! -f "$1/change" ]; do sleep 0.1; done; printf changed; sleep 60',
                "activity-e2e",
                activitySignalDir,
              ],
              enabled: true,
            },
          ],
        },
      });
      expect(settings.ok(), await settings.text()).toBe(true);

      const created = await api.post("/api/v1/workspaces", {
        data: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          mr_number: 1,
        },
      });
      expect(created.status(), await created.text()).toBe(202);
      const workspace = (await created.json()) as WorkspaceResponse;
      await waitForWorkspaceReady(api, workspace.id);

      const issueCreated = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(issueCreated.status(), await issueCreated.text()).toBe(202);
      const issueWorkspace = (await issueCreated.json()) as WorkspaceResponse;
      await waitForWorkspaceReady(api, issueWorkspace.id);

      const launched = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
        data: { target_key: "activity-e2e", display_region: "terminal" },
      });
      expect(launched.ok(), await launched.text()).toBe(true);

      const issueLaunched = await api.post(`/api/v1/workspaces/${issueWorkspace.id}/runtime/sessions`, {
        data: { target_key: "activity-e2e", display_region: "terminal" },
      });
      expect(issueLaunched.ok(), await issueLaunched.text()).toBe(true);

      await expect
        .poll(() => readdirSync(activitySignalDir).filter((entry) => entry.startsWith("seeded.")).length)
        .toBe(2);
      for (const workspaceID of [workspace.id, issueWorkspace.id]) {
        await expect
          .poll(async () => {
            const response = await api.get(`/api/v1/workspaces/${workspaceID}`);
            expect(response.ok(), await response.text()).toBe(true);
            return ((await response.json()) as WorkspaceResponse).tmux_activity_source;
          })
          .toBe("none");
      }

      const seeded = await api.get("/api/v1/activity?since=2026-01-01T00:00:00Z");
      expect(seeded.ok(), await seeded.text()).toBe(true);
      expect(((await seeded.json()) as ActivityResponse).workspace_activity ?? []).toHaveLength(0);

      await expect
        .poll(async () => await pull(api, 1))
        .toMatchObject({
          workspace: { id: workspace.id, status: "ready" },
        });
      expect((await pull(api, 1))?.last_workspace_activity_at).toBeUndefined();

      await expect
        .poll(async () => await issue(api, 10))
        .toMatchObject({
          workspace: { id: issueWorkspace.id, status: "ready" },
        });
      expect((await issue(api, 10))?.last_workspace_activity_at).toBeUndefined();

      const providerOrderedPulls = await listPulls(api);
      const providerOrderedIssues = await listIssues(api);
      expect(providerOrderedPulls.length).toBeGreaterThan(1);
      expect(providerOrderedIssues.length).toBeGreaterThan(1);

      writeFileSync(changeSignal, "change\n", "utf8");

      await expect
        .poll(async () => (await pull(api, 1))?.last_workspace_activity_at, { timeout: 20_000 })
        .not.toBeUndefined();
      await expect
        .poll(async () => (await issue(api, 10))?.last_workspace_activity_at, { timeout: 20_000 })
        .not.toBeUndefined();
      expect((await listPulls(api)).map((pullRequest) => pullRequest.Number)).toEqual(
        providerOrderedPulls.map((pullRequest) => pullRequest.Number),
      );
      expect((await listIssues(api)).map((issue) => issue.Number)).toEqual(
        providerOrderedIssues.map((issue) => issue.Number),
      );
      const providerAuthoritativeActivity = await api.get("/api/v1/activity?since=2026-01-01T00:00:00Z");
      expect(providerAuthoritativeActivity.ok(), await providerAuthoritativeActivity.text()).toBe(true);
      expect(((await providerAuthoritativeActivity.json()) as ActivityResponse).workspace_activity ?? []).toHaveLength(
        0,
      );

      const enableWorkspaceRecency = await api.put("/api/v1/settings", {
        data: {
          activity: {
            ...currentSettings.activity,
            use_workspace_activity_for_recency: true,
          },
        },
      });
      expect(enableWorkspaceRecency.ok(), await enableWorkspaceRecency.text()).toBe(true);

      await expect
        .poll(
          async () => {
            const response = await api.get("/api/v1/activity?since=2026-01-01T00:00:00Z");
            const activity = (await response.json()) as ActivityResponse;
            const subjects = activity.workspace_activity ?? [];
            return (
              subjects.some((subject) => subject.item_type === "pr" && subject.item_number === 1) &&
              subjects.some((subject) => subject.item_type === "issue" && subject.item_number === 10)
            );
          },
          { timeout: 20_000 },
        )
        .toBe(true);

      await expect
        .poll(async () => {
          const pulls = await listPulls(api);
          return {
            hasCompetingItems: pulls.length > 1,
            firstNumber: pulls[0]?.Number,
            workspaceID: pulls[0]?.workspace?.id,
            hasNewerWorkspaceActivity:
              pulls[0]?.last_workspace_activity_at !== undefined &&
              Date.parse(pulls[0].last_workspace_activity_at) > Date.parse(pulls[0].LastActivityAt),
          };
        })
        .toEqual({
          hasCompetingItems: true,
          firstNumber: 1,
          workspaceID: workspace.id,
          hasNewerWorkspaceActivity: true,
        });

      await expect
        .poll(async () => {
          const issues = await listIssues(api);
          return {
            hasCompetingItems: issues.length > 1,
            firstNumber: issues[0]?.Number,
            workspaceID: issues[0]?.workspace?.id,
            hasNewerWorkspaceActivity:
              issues[0]?.last_workspace_activity_at !== undefined &&
              Date.parse(issues[0].last_workspace_activity_at) > Date.parse(issues[0].LastActivityAt),
          };
        })
        .toEqual({
          hasCompetingItems: true,
          firstNumber: 10,
          workspaceID: issueWorkspace.id,
          hasNewerWorkspaceActivity: true,
        });

      await page.goto(`${server.info.base_url}/`);
      await expect(page.locator(".activity-table .activity-row").first()).toBeVisible();
      await selectActivityViewItem(page, "Threaded");
      await selectActivityViewItem(page, "24h");
      await selectActivityViewItem(page, "Notifications");
      const pullActivityRow = page.locator(".threaded-view .item-row", { hasText: "Add widget caching layer" }).first();
      await expect(pullActivityRow).toBeVisible();
      await expect(pullActivityRow.locator('.cell--time[title="Recent workspace activity"]')).toBeVisible();
      await expect(pullActivityRow.locator(".thread-caret")).toHaveCount(0);
      const issueActivityRow = page
        .locator(".threaded-view .item-row", { hasText: "Widget rendering broken on Safari" })
        .first();
      await expect(issueActivityRow).toBeVisible();
      await expect(issueActivityRow.locator('.cell--time[title="Recent workspace activity"]')).toBeVisible();
      await expect(issueActivityRow.locator(".thread-caret")).toHaveCount(0);

      await page.goto(`${server.info.base_url}/pulls`);
      const pullRow = page.locator(".pull-item", { hasText: "Add widget caching layer" }).first();
      await expect(pullRow).toBeVisible();
      await expect(page.locator(".pull-item").first()).toContainText("Add widget caching layer");
      await expect(pullRow.locator('.time[title="Recent workspace activity"]')).toBeVisible();
      await expect(pullRow.getByLabel("Workspace attached (ready)")).toBeVisible();

      await page.goto(`${server.info.base_url}/issues`);
      const issueRow = page.locator(".issue-item", { hasText: "Widget rendering broken on Safari" }).first();
      await expect(issueRow).toBeVisible();
      await expect(page.locator(".issue-item").first()).toContainText("Widget rendering broken on Safari");
      await expect(issueRow.locator('.time[title="Recent workspace activity"]')).toBeVisible();
      await expect(issueRow.getByLabel("Workspace attached (ready)")).toBeVisible();

      const phoneContext = await browser.newContext({ ...devices["iPhone 13"] });
      try {
        const phonePage = await phoneContext.newPage();
        await phonePage.goto(`${server.info.base_url}/m?range=24h&event_types=none`);

        await expect(phonePage.locator(".mobile-shell")).toBeVisible();
        const mobileCards = phonePage.locator(".mobile-activity-card");
        await expect(mobileCards.first()).toBeVisible();
        const topTitles = await mobileCards
          .locator(".mobile-activity-card__title")
          .evaluateAll((titles) => titles.slice(0, 2).map((title) => title.textContent?.trim()));
        expect(new Set(topTitles)).toEqual(new Set(["Add widget caching layer", "Widget rendering broken on Safari"]));

        const mobileIssueCard = mobileCards.filter({ hasText: "Widget rendering broken on Safari" }).first();
        await expect(mobileIssueCard.locator('time[title="Recent workspace activity"]')).toBeVisible();
        await expect(mobileIssueCard.getByLabel("Workspace attached (ready)")).toBeVisible();
        await expect(mobileIssueCard.locator(".mobile-activity-events")).toHaveCount(0);
        expect(await phonePage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

        await mobileIssueCard.locator(".mobile-activity-card__button").click();
        await expect(phonePage).toHaveURL(/\/focus\/issues\/github\/acme\/widgets\/10$/);
      } finally {
        await phoneContext.close();
      }

      // Exercise bot filtering against the authoritative parent/workspace
      // projections, not event rows. PR #7 has recent parent activity but no
      // selected event, while issue #13 is outside 24h until workspace output
      // gives it a current workspace-only activity timestamp.
      rmSync(changeSignal, { force: true });
      const seededSessionCount = readdirSync(activitySignalDir).filter((entry) => entry.startsWith("seeded.")).length;
      const botIssueCreated = await api.post("/api/v1/issues/github/acme/widgets/13/workspace", { data: {} });
      expect(botIssueCreated.status(), await botIssueCreated.text()).toBe(202);
      const botIssueWorkspace = (await botIssueCreated.json()) as WorkspaceResponse;
      await waitForWorkspaceReady(api, botIssueWorkspace.id);

      const botIssueLaunched = await api.post(`/api/v1/workspaces/${botIssueWorkspace.id}/runtime/sessions`, {
        data: { target_key: "activity-e2e", display_region: "terminal" },
      });
      expect(botIssueLaunched.ok(), await botIssueLaunched.text()).toBe(true);
      await expect
        .poll(() => readdirSync(activitySignalDir).filter((entry) => entry.startsWith("seeded.")).length)
        .toBe(seededSessionCount + 1);
      await expect
        .poll(async () => {
          const response = await api.get(`/api/v1/workspaces/${botIssueWorkspace.id}`);
          expect(response.ok(), await response.text()).toBe(true);
          return ((await response.json()) as WorkspaceResponse).tmux_activity_source;
        })
        .toBe("none");

      writeFileSync(changeSignal, "change\n", "utf8");
      await expect
        .poll(
          async () => {
            const response = await api.get("/api/v1/activity?since=2026-01-01T00:00:00Z");
            expect(response.ok(), await response.text()).toBe(true);
            return ((await response.json()) as ActivityResponse).workspace_activity?.some(
              (subject) => subject.item_type === "issue" && subject.item_number === 13,
            );
          },
          { timeout: 20_000 },
        )
        .toBe(true);

      const since24h = encodeURIComponent(new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString());
      const botSubjectsResponse = await api.get(
        `/api/v1/activity?since=${since24h}&types=commit&item_types=pr,issue,repo`,
      );
      expect(botSubjectsResponse.ok(), await botSubjectsResponse.text()).toBe(true);
      const botSubjects = (await botSubjectsResponse.json()) as ActivityResponse;
      expect(botSubjects.items ?? []).not.toContainEqual(expect.objectContaining({ item_type: "pr", item_number: 7 }));
      expect(botSubjects.items ?? []).not.toContainEqual(
        expect.objectContaining({ item_type: "issue", item_number: 13 }),
      );
      expect(botSubjects.item_activity ?? []).toContainEqual(
        expect.objectContaining({ item_type: "pr", item_number: 7 }),
      );
      expect(botSubjects.item_activity ?? []).not.toContainEqual(
        expect.objectContaining({ item_type: "issue", item_number: 13 }),
      );
      expect(botSubjects.workspace_activity ?? []).toContainEqual(
        expect.objectContaining({ item_type: "issue", item_number: 13 }),
      );

      const settingsLoaded = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return url.pathname === "/api/v1/settings" && response.request().method() === "GET";
      });
      await page.goto(`${server.info.base_url}/`);
      await settingsLoaded;
      await expect(page.locator(".activity-table .activity-row").first()).toBeVisible();
      await selectActivityViewItem(page, "Threaded");
      await selectActivityViewItem(page, "24h");
      await expect(page.locator(".activity-filters__trigger")).toContainText("Threaded · 24h");
      const botPullTitle = "Bump lodash from 4.17.20 to 4.17.21";
      const botIssueTitle = "Security advisory: prototype pollution";
      const botPullRow = page.locator(".threaded-view .item-row", { hasText: botPullTitle });
      const botIssueRow = page.locator(".threaded-view .item-row", { hasText: botIssueTitle });
      await expect(botPullRow).toBeVisible();
      await expect(botIssueRow).toBeVisible();
      const pullCountBefore = await activityStatusCount(page, "PRs");
      const issueCountBefore = await activityStatusCount(page, "issues");

      await selectActivityViewItem(page, "Hide bots");
      await expect(botPullRow).toHaveCount(0);
      await expect(botIssueRow).toHaveCount(0);
      await expect(page.getByRole("contentinfo").locator(".status-item").filter({ hasText: /PRs$/ })).toHaveText(
        `${pullCountBefore - 1} PRs`,
      );
      await expect(
        page
          .getByRole("contentinfo")
          .locator(".status-item")
          .filter({ hasText: /issues$/ }),
      ).toHaveText(`${issueCountBefore - 1} issues`);

      const botPhoneContext = await browser.newContext({ ...devices["iPhone 13"] });
      try {
        const botPhonePage = await botPhoneContext.newPage();
        const mobileSettingsLoaded = botPhonePage.waitForResponse((response) => {
          const url = new URL(response.url());
          return url.pathname === "/api/v1/settings" && response.request().method() === "GET";
        });
        await botPhonePage.goto(`${server.info.base_url}/m`);
        await mobileSettingsLoaded;
        await botPhonePage.getByRole("button", { name: /^Filters/ }).click();
        await botPhonePage.getByRole("combobox", { name: /Time range/ }).click();
        await botPhonePage.getByRole("option", { name: "24h" }).click();
        const botPullCard = botPhonePage.locator(".mobile-activity-card", { hasText: botPullTitle });
        const botIssueCard = botPhonePage.locator(".mobile-activity-card", { hasText: botIssueTitle });
        await expect(botPullCard).toBeVisible();
        await expect(botIssueCard).toBeVisible();
        await botPhonePage.getByRole("switch", { name: "Hide bots", exact: true }).click();
        await expect(botPullCard).toHaveCount(0);
        await expect(botIssueCard).toHaveCount(0);
      } finally {
        await botPhoneContext.close();
      }
    } finally {
      await api.dispose();
      await server.stop();
      rmSync(activitySignalDir, { force: true, recursive: true });
    }
  });
});
