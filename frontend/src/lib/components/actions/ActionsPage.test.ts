import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { STORES_KEY } from "../../context.js";
import { createWorkflowActionsStore } from "../../stores/workflow-actions.svelte.js";
import { setGlobalRepo } from "../../stores/filter.svelte.js";
import {
  createMockApiFetch,
  jsonResponse,
  type MockApiHandle,
  type MockRouteOverride,
} from "../../../test/mockApiFetch.js";

const runtimeHolder = vi.hoisted(() => ({ value: undefined as OwnedAppRuntime | undefined }));
vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => runtimeHolder.value,
}));

import ActionsPage from "./ActionsPage.svelte";

const capabilities = {
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

const available = { available: true };

function repoSummary(name: string, supported = true) {
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
      capabilities: supported
        ? capabilities
        : {
            ...capabilities,
            read_workflows: false,
            read_workflow_runs: false,
            workflow_dispatch: false,
          },
      operations: { dispatch_workflow: available },
    },
    operations: { dispatch_workflow: available },
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

function workflowFixtures(): MockRouteOverride {
  const summaries = [
    repoSummary("alpha"),
    repoSummary("beta"),
    repoSummary("legacy", false),
    repoSummary("filtered-out"),
  ];
  return (request) => {
    if (request.method === "GET" && request.url.pathname === "/api/v1/repos/summary") {
      return jsonResponse(summaries);
    }
    const catalog = request.url.pathname.match(/^\/api\/v1\/actions\/github\/acme\/([^/]+)\/workflows$/);
    if (request.method === "GET" && catalog) {
      const name = catalog[1]!;
      return jsonResponse({
        repo: repoSummary(name).repo,
        environments: [{ name: "production" }],
        workflows: [{
          id: `${name}-deploy.yml`,
          name: `${name} deploy`,
          path: `.github/workflows/${name}-deploy.yml`,
          state: "active",
          available: true,
          definition_sha: `${name}-definition`,
          inputs: [],
          web_url: `https://github.com/acme/${name}/actions/workflows/${name}-deploy.yml`,
        }],
      });
    }
    const runs = request.url.pathname.match(/^\/api\/v1\/actions\/github\/acme\/([^/]+)\/runs$/);
    if (request.method === "GET" && runs) {
      const name = runs[1]!;
      return jsonResponse({
        repo: repoSummary(name).repo,
        exhausted: true,
        items: [{
          actor: "octocat",
          conclusion: "success",
          created_at: "2026-08-27T12:30:00Z",
          event: "workflow_dispatch",
          head_sha: "0123456789abcdef",
          id: `${name}-run-1`,
          name: `${name} deploy`,
          ref: "main",
          run_number: 7,
          status: "completed",
          workflow_id: `${name}-deploy.yml`,
        }],
      });
    }
    const jobs = request.url.pathname.match(/^\/api\/v1\/actions\/github\/acme\/([^/]+)\/runs\/([^/]+)\/jobs$/);
    if (request.method === "GET" && jobs) {
      return jsonResponse({
        repo: repoSummary(jobs[1]!).repo,
        items: [{
          id: "job-1",
          name: "Publish",
          status: "completed",
          conclusion: "success",
          steps: [],
        }],
      });
    }
    return null;
  };
}

describe("ActionsPage", () => {
  let runtime: OwnedAppRuntime;
  let api: MockApiHandle;
  let originalFetch: typeof globalThis.fetch;

  beforeEach(() => {
    originalFetch = globalThis.fetch;
    api = createMockApiFetch([workflowFixtures()]);
    globalThis.fetch = api.fetch;
    runtime = makeAppRuntime();
    runtimeHolder.value = runtime;
    setGlobalRepo("github|github.com/acme/alpha,github|github.com/acme/beta,github|github.com/acme/legacy");
  });

  afterEach(async () => {
    cleanup();
    setGlobalRepo(undefined);
    globalThis.fetch = originalFetch;
    runtimeHolder.value = undefined;
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("filters repository summaries, distinguishes unsupported repos, and demands only the selected capable repo", async () => {
    const workflowActions = createWorkflowActionsStore({ runtime });
    render(ActionsPage, {
      context: new Map([[STORES_KEY, { workflowActions }]]),
    });

    const rail = await screen.findByRole("navigation", { name: "Actions repositories" });
    expect(within(rail).getByRole("button", { name: /alpha/ }).getAttribute("aria-current")).toBe("true");
    expect(within(rail).getByRole("button", { name: /beta/ })).toBeTruthy();
    expect(within(rail).getByLabelText("acme/legacy does not support workflow Actions")).toBeTruthy();
    expect(screen.queryByText("filtered-out")).toBeNull();

    await waitFor(() => {
      const paths = api.requests.map((request) => request.url.pathname);
      expect(paths).toContain("/api/v1/actions/github/acme/alpha/workflows");
      expect(paths).toContain("/api/v1/actions/github/acme/alpha/runs");
      expect(paths.some((path) => path.includes("/legacy/"))).toBe(false);
    });
  });

  it("uses the shared workflow form and lazy run jobs for the selected repository", async () => {
    const workflowActions = createWorkflowActionsStore({ runtime });
    render(ActionsPage, {
      context: new Map([[STORES_KEY, { workflowActions }]]),
    });

    await fireEvent.click(await screen.findByRole("button", { name: /alpha deploy/ }));
    expect(screen.getByRole("textbox", { name: "Git ref" })).toBeTruthy();

    const run = await screen.findByRole("button", { name: /Run 7 alpha deploy/ });
    await fireEvent.click(run);
    expect(await screen.findByRole("button", { name: /Publish/ })).toBeTruthy();
    expect(api.requests.map((request) => request.url.pathname)).toContain(
      "/api/v1/actions/github/acme/alpha/runs/alpha-run-1/jobs",
    );
  });
});
