import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  emitBrowserEventSource,
  getBrowserEventSourceCount,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { createMockApiHandler, jsonResponse, mockSettings, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

let actionsModeEnabled = true;

const capable = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_issue_pr_references: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_workflows: true,
  read_workflow_runs: true,
  workflow_dispatch: true,
  read_labels: true,
  read_markdown_images: true,
  read_authenticated_user: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  label_mutation: true,
  assignee_mutation: true,
  reviewer_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  draft_mutation: true,
  issue_mutation: true,
  review_draft_mutation: false,
  review_thread_resolution: false,
  review_suggestion_application: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  mutation_head_binding: false,
  thread_reply: false,
  thread_resolve: false,
  supported_review_actions: [],
};

const workflowOperation = { available: true };

const repoIds: Record<string, number> = { widgets: 1005, legacy: 1020 };

function summary(name: string, supportsActions: boolean) {
  return {
    owner: "acme",
    name,
    platform_host: "github.com",
    default_platform_host: "github.com",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name,
      repo_path: `acme/${name}`,
      platform_repo_id: repoIds[name] ?? 0,
      capabilities: supportsActions
        ? capable
        : {
            ...capable,
            read_workflows: false,
            read_workflow_runs: false,
            workflow_dispatch: false,
          },
      operations: { dispatch_workflow: workflowOperation },
    },
    operations: { dispatch_workflow: workflowOperation },
    cached_pr_count: 0,
    open_pr_count: 0,
    draft_pr_count: 0,
    cached_issue_count: 0,
    open_issue_count: 0,
    active_authors: [],
    recent_issues: [],
    commit_timeline: [],
    releases: [],
  };
}

function actionsFixtures(): MockRouteOverride {
  return (request) => {
    if (request.method === "GET" && request.url.pathname === "/api/v1/settings") {
      return jsonResponse({
        ...mockSettings,
        modes: { ...mockSettings.modes, actions: actionsModeEnabled },
      });
    }
    if (request.method === "GET" && request.url.pathname === "/api/v1/repos/summary") {
      return jsonResponse([summary("widgets", true), summary("legacy", false)]);
    }
    if (request.method === "GET" && request.url.pathname === "/api/v1/actions/github/acme/widgets/workflows") {
      return jsonResponse({
        repo: { ...summary("widgets", true).repo, default_branch: "trunk" },
        environments: [],
        workflows: [
          {
            id: "deploy.yml",
            name: "Deploy",
            path: ".github/workflows/deploy.yml",
            state: "active",
            available: true,
            definition_sha: "definition-1",
            inputs: [],
            web_url: "https://github.com/acme/widgets/actions/workflows/deploy.yml",
          },
        ],
      });
    }
    if (request.method === "GET" && request.url.pathname === "/api/v1/actions/github/acme/widgets/runs") {
      return jsonResponse({
        repo: summary("widgets", true).repo,
        items: [],
        exhausted: true,
      });
    }
    return null;
  };
}

function actionReadPaths(mounted: MountedBrowserApp): string[] {
  return mounted.api.requests
    .map((request) => request.url.pathname)
    .filter((pathname) => pathname.includes("/actions/"));
}

describe("opt-in workflow Actions route", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
    localStorage.clear();
    actionsModeEnabled = true;
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("redirects a disabled deep link after settings hydration without creating Actions demand", async () => {
    mounted = await mountBrowserApp("/actions");

    await vi.waitFor(() => expect(window.location.pathname).toBe("/"), WAIT);
    expect(actionReadPaths(mounted)).toEqual([]);
    expect(document.querySelector(".actions-page")).toBeNull();
  });

  it("mounts the enabled page and reads workflows only for an Actions-capable repository", async () => {
    mounted = await mountBrowserApp("/actions", { overrides: [actionsFixtures()] });

    await expect.element(page.getByRole("heading", { name: "Actions" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: /Deploy/ })).toBeVisible();
    await vi.waitFor(() => {
      expect(actionReadPaths(mounted!)).toEqual(
        expect.arrayContaining(["/api/v1/actions/github/acme/widgets/workflows"]),
      );
    }, WAIT);
    expect(actionReadPaths(mounted).some((pathname) => pathname.includes("/legacy/"))).toBe(false);
    await expect
      .element(page.getByRole("note", { name: "acme/legacy does not support workflow Actions" }))
      .toBeVisible();
  });

  it("keeps a pull workflow dialog and its draft open when the list refreshes", async () => {
    const fixtures = createMockApiHandler();
    const detailPath = "/api/v1/pulls/github/acme/widgets/42";
    const detail = await fixtures
      .handle({
        method: "GET",
        url: new URL(detailPath, "http://localhost"),
        bodyText: "",
      })
      .json();
    detail.repo = summary("widgets", true).repo;
    let refreshed = false;
    const pullFixtures: MockRouteOverride = (request) => {
      if (request.method === "GET" && request.url.pathname === "/api/v1/pulls") {
        return jsonResponse([
          {
            ...detail.merge_request,
            Title: refreshed ? "Refreshed pull list title" : detail.merge_request.Title,
            repo: detail.repo,
          },
        ]);
      }
      if (request.url.pathname === detailPath || request.url.pathname === `${detailPath}/sync`) {
        return jsonResponse(detail);
      }
      return null;
    };
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/42", {
      overrides: [pullFixtures, actionsFixtures()],
    });
    await page
      .getByRole("region", { name: "Pull request conversation" })
      .getByRole("button", { name: "Run workflow", exact: true })
      .click();
    await page
      .getByRole("region", { name: "GitHub Actions" })
      .getByRole("button", { name: "Deploy", exact: true })
      .click();
    const dialog = page.getByRole("dialog", { name: "Run workflow" });
    await expect.element(dialog).toBeVisible();
    await dialog.getByRole("textbox", { name: "Git ref" }).fill("feature/keep-this-draft");

    refreshed = true;
    emitBrowserEventSource("data_changed", {});

    await expect.element(page.getByText("Refreshed pull list title", { exact: true })).toBeVisible();
    await expect.element(dialog).toBeVisible();
    await expect.element(dialog.getByRole("textbox", { name: "Git ref" })).toHaveValue("feature/keep-this-draft");
  });

  it("releases the mounted workspace and redirects when a settings update disables Actions", async () => {
    mounted = await mountBrowserApp("/actions", { overrides: [actionsFixtures()] });
    await expect.element(page.getByRole("heading", { name: "Actions" })).toBeVisible();
    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(1), WAIT);

    actionsModeEnabled = false;
    emitBrowserEventSource("config.changed", { valid: true, restart_required: false });

    await vi.waitFor(() => expect(window.location.pathname).toBe("/"), WAIT);
    expect(document.querySelector(".actions-page")).toBeNull();
    expect(document.querySelector(".kit-top-bar__tab[aria-current='page']")?.textContent).toContain("Activity");
    expect(Array.from(document.querySelectorAll(".kit-top-bar__tab"), (tab) => tab.textContent?.trim())).not.toContain(
      "Actions",
    );
  });

  it("keeps repository, workflow, dispatch, and runs regions in semantic order when narrow", async () => {
    await page.viewport(600, 800);
    mounted = await mountBrowserApp("/actions", { overrides: [actionsFixtures()] });

    await expect.element(page.getByRole("heading", { name: "Actions" })).toBeVisible();
    await vi.waitFor(() => {
      const layout = document.querySelector<HTMLElement>(".actions-layout");
      expect(layout).toBeTruthy();
      expect(getComputedStyle(layout!).display).toBe("flex");
      expect(getComputedStyle(layout!).flexDirection).toBe("column");
      expect(
        Array.from(document.querySelectorAll(".actions-layout h2"), (heading) => heading.textContent?.trim()),
      ).toEqual(["Repos", "Workflows", "Dispatch", "Recent runs"]);
      expect(document.querySelector<HTMLElement>(".actions-page")!.scrollWidth).toBeLessThanOrEqual(window.innerWidth);
    }, WAIT);
  });

  it("keeps populated run metadata and provider actions reachable without narrow horizontal overflow", async () => {
    await page.viewport(520, 800);
    const longRun: MockRouteOverride = (request) => {
      if (request.method !== "GET" || request.url.pathname !== "/api/v1/actions/github/acme/widgets/runs") return null;
      return jsonResponse({
        repo: { ...summary("widgets", true).repo, default_branch: "trunk" },
        exhausted: true,
        items: [
          {
            actor: "maintainer-with-an-intentionally-long-provider-identity",
            conclusion: "success",
            created_at: "2026-08-27T12:30:00Z",
            event: "workflow_dispatch",
            head_sha: "0123456789abcdef0123456789abcdef01234567",
            id: "long-run",
            name: "Release production assets with a deliberately long workflow name",
            ref: "feature/an-intentionally-long-reference-that-must-not-expand-the-page",
            run_number: 99,
            status: "completed",
            web_url: "https://github.com/acme/widgets/actions/runs/99",
            workflow_id: "deploy.yml",
          },
        ],
      });
    };
    mounted = await mountBrowserApp("/actions", { overrides: [longRun, actionsFixtures()] });
    await expect.element(page.getByRole("button", { name: /Deploy/ })).toBeVisible();
    await page.getByRole("button", { name: /Deploy/ }).click();
    await expect.element(page.getByRole("button", { name: /Run 99/ })).toBeVisible();

    await vi.waitFor(() => {
      const pageElement = document.querySelector<HTMLElement>(".actions-page")!;
      const row = document.querySelector<HTMLElement>(".run-row")!;
      const disclosure = document.querySelector<HTMLElement>(".run-disclosure")!;
      const providerLink = row.querySelector<HTMLAnchorElement>("a")!;
      expect(pageElement.scrollWidth).toBeLessThanOrEqual(pageElement.clientWidth);
      expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth);
      expect(disclosure.scrollWidth).toBeLessThanOrEqual(disclosure.clientWidth);
      expect(providerLink.getBoundingClientRect().right).toBeLessThanOrEqual(row.getBoundingClientRect().right);
    }, WAIT);

    // Narrow panes collapse table columns, so the expanded detail must carry the same fields.
    await page.getByRole("button", { name: /Run 99/ }).click();
    await vi.waitFor(() => {
      const pageElement = document.querySelector<HTMLElement>(".actions-page")!;
      const detail = document.querySelector<HTMLElement>(".run-detail")!;
      expect(detail.textContent).toContain("feature/an-intentionally-long-reference-that-must-not-expand-the-page");
      expect(detail.textContent).toContain("maintainer-with-an-intentionally-long-provider-identity");
      expect(detail.textContent).toContain("0123456");
      expect(pageElement.scrollWidth).toBeLessThanOrEqual(pageElement.clientWidth);
      for (const item of detail.querySelectorAll<HTMLElement>("dd")) {
        expect(item.getBoundingClientRect().right).toBeLessThanOrEqual(detail.getBoundingClientRect().right);
      }
    }, WAIT);
  });

  it("uses one full-height medium row when a single capable repository needs no rail", async () => {
    await page.viewport(760, 800);
    const singleRepository: MockRouteOverride = (request) => {
      if (request.method === "GET" && request.url.pathname === "/api/v1/repos/summary") {
        return jsonResponse([summary("widgets", true)]);
      }
      return null;
    };
    mounted = await mountBrowserApp("/actions", {
      overrides: [singleRepository, actionsFixtures()],
    });

    await expect.element(page.getByRole("heading", { name: "Actions" })).toBeVisible();
    await vi.waitFor(() => {
      const layout = document.querySelector<HTMLElement>(".actions-layout")!;
      const workflows = document.querySelector<HTMLElement>(".workflow-catalog")!;
      const workspace = document.querySelector<HTMLElement>(".workflow-workspace")!;
      expect(document.querySelector(".repository-rail")).toBeNull();
      expect(getComputedStyle(layout).display).toBe("grid");
      expect(Math.round(workflows.getBoundingClientRect().top)).toBe(Math.round(workspace.getBoundingClientRect().top));
      expect(Math.round(workflows.getBoundingClientRect().bottom)).toBe(
        Math.round(layout.getBoundingClientRect().bottom),
      );
      expect(Math.round(workspace.getBoundingClientRect().bottom)).toBe(
        Math.round(layout.getBoundingClientRect().bottom),
      );
    }, WAIT);
  });
});
