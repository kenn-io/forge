import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

function workspaceRepoRef(
  owner = "acme",
  name = "widgets",
  host = "github.com",
) {
  return {
    provider: "github",
    platform_host: host,
    owner,
    name,
    repo_path: `${owner}/${name}`,
  };
}

const testWorkspace = {
  id: "ws-123",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  repo: workspaceRepoRef(),
  item_type: "pull_request",
  item_number: 42,
  git_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-123",
  tmux_session: "middleman-ws-123",
  tmux_pane_title: null,
  tmux_working: false,
  status: "ready",
  created_at: "2026-04-10T12:00:00Z",
  mr_title: "Add auth middleware",
  mr_state: "open",
  mr_is_draft: false,
};

const testIssueWorkspace = {
  id: "ws-issue-7",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  repo: workspaceRepoRef(),
  item_type: "issue",
  item_number: 7,
  git_head_ref: "middleman/issue-7",
  worktree_path: "/tmp/worktrees/ws-issue-7",
  tmux_session: "middleman-ws-issue-7",
  status: "ready",
  created_at: "2026-04-10T12:00:00Z",
  mr_title: "Theme toggle does not stick",
  mr_state: "open",
};

const testIssueWorkspaceWithAssociatedPR = {
  ...testIssueWorkspace,
  associated_pr_number: 42,
};

const roborevRepos = {
  repos: [
    {
      name: "widgets",
      root_path: "/home/dev/widgets",
      count: 5,
    },
  ],
  total_count: 1,
};

const roborevJobs = {
  jobs: [
    {
      id: 1,
      status: "done",
      verdict: "pass",
      agent: "code-review",
      job_type: "review",
      git_ref: "abc12345",
      commit_subject: "Add auth middleware",
      enqueued_at: "2026-04-10T12:00:00Z",
      branch: "feature/auth",
      repo_name: "widgets",
      repo_id: 1,
      agentic: false,
      prompt_prebuilt: false,
      retry_count: 0,
    },
  ],
  has_more: false,
  stats: { done: 1, closed: 0, open: 0 },
};

const roborevStatus = {
  available: true,
  version: "0.52.0",
  endpoint: "http://127.0.0.1:17373",
  active_workers: 1,
  max_workers: 4,
  queued_jobs: 2,
  running_jobs: 1,
  completed_jobs: 5,
  failed_jobs: 0,
  canceled_jobs: 0,
};

const workspaceRuntime = {
  launch_targets: [
    {
      key: "codex",
      label: "Codex",
      kind: "agent",
      source: "builtin",
      command: ["codex"],
      available: true,
    },
    {
      key: "shell",
      label: "Shell",
      kind: "shell",
      source: "system",
      command: ["tmux"],
      available: false,
      disabled_reason: "tmux not found",
    },
    {
      key: "plain_shell",
      label: "Plain shell",
      kind: "plain_shell",
      source: "system",
      command: ["/bin/sh"],
      available: true,
    },
  ],
  sessions: [],
};

type RuntimeTarget = (typeof workspaceRuntime.launch_targets)[number];
type RuntimeSession = {
  key: string;
  workspace_id: string;
  target_key: string;
  label: string;
  kind: RuntimeTarget["kind"];
  status: "starting" | "running" | "exited" | "error";
  created_at: string;
};
type WorkspaceRuntime = Omit<typeof workspaceRuntime, "sessions"> & {
  sessions: RuntimeSession[];
};
type RuntimeEvents = {
  launches: string[];
  renames: Array<{ sessionKey: string; label: string }>;
  deletes: string[];
};

/**
 * Mock all routes needed for terminal view tests.
 * Registers mockApi first (catch-all), then layers
 * workspace and roborev routes on top so they take
 * priority (Playwright uses LIFO route matching).
 */
type WorkspaceFixture =
  | typeof testWorkspace
  | typeof testIssueWorkspace
  | typeof testIssueWorkspaceWithAssociatedPR;

async function setupTerminalMocks(
  page: import("@playwright/test").Page,
  opts?: {
    workspace?: WorkspaceFixture;
    roborevRepos?: typeof roborevRepos;
    roborevJobs?: typeof roborevJobs;
    roborevStatus?: typeof roborevStatus;
    workspaceDetailResponses?: Array<{
      status: number;
      body?: unknown;
    }>;
    workspaceDeleteResponses?: Array<{
      status: number;
      body?: unknown;
    }>;
    workspaceRetryResponse?: {
      status: number;
      body?: unknown;
    };
    runtime?: WorkspaceRuntime;
    runtimeEvents?: RuntimeEvents;
  },
): Promise<{ runtime: WorkspaceRuntime }> {
  const ws = opts?.workspace ?? testWorkspace;
  const rrRepos = opts?.roborevRepos ?? roborevRepos;
  const rrJobs = opts?.roborevJobs ?? roborevJobs;
  const rrStatus = opts?.roborevStatus ?? roborevStatus;
  const detailResponses = [
    ...(opts?.workspaceDetailResponses ?? []),
  ];
  const deleteResponses = [
    ...(opts?.workspaceDeleteResponses ?? []),
  ];
  const runtime = JSON.parse(
    JSON.stringify(opts?.runtime ?? workspaceRuntime),
  ) as WorkspaceRuntime;

  // Register catch-all first — later routes override.
  await mockApi(page);

  await page.route(
    "**/api/v1/events",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    },
  );

  // Register list route first, then specific route.
  // Playwright uses LIFO matching, so the specific
  // /workspaces/:id registered last takes priority
  // over the list-only pattern.
  await page.route(
    "**/api/v1/workspaces",
    async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ workspaces: [ws] }),
        });
        return;
      }
      await route.fulfill({ status: 200 });
    },
  );

  await page.route(
    `**/api/v1/workspaces/${ws.id}/retry`,
    async (route) => {
      const response = opts?.workspaceRetryResponse ?? {
        status: 202,
        body: { ...ws, status: "creating" },
      };
      await route.fulfill({
        status: response.status,
        contentType: "application/json",
        body: JSON.stringify(response.body ?? {}),
      });
    },
  );

  await page.route(
    (url) => url.pathname === `/api/v1/workspaces/${ws.id}`,
    async (route) => {
      if (route.request().method() === "GET") {
        const nextResponse = detailResponses.shift();
        if (nextResponse) {
          await route.fulfill({
            status: nextResponse.status,
            contentType: "application/json",
            body: JSON.stringify(
              nextResponse.body ?? {},
            ),
          });
          return;
        }
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(ws),
        });
        return;
      }
      // DELETE
      const nextDelete = deleteResponses.shift();
      if (nextDelete) {
        if (nextDelete.body === undefined) {
          await route.fulfill({ status: nextDelete.status });
          return;
        }
        await route.fulfill({
          status: nextDelete.status,
          contentType: "application/json",
          body: JSON.stringify(nextDelete.body),
        });
        return;
      }
      await route.fulfill({ status: 204 });
    },
  );

  await page.route(
    `**/api/v1/workspaces/${ws.id}/runtime`,
    async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(runtime),
        });
        return;
      }
      await route.fulfill({ status: 405 });
    },
  );

  await page.route(
    `**/api/v1/workspaces/${ws.id}/runtime/sessions`,
    async (route) => {
      if (route.request().method() !== "POST") {
        await route.fulfill({ status: 405 });
        return;
      }
      const body = JSON.parse(
        route.request().postData() ?? "{}",
      ) as { target_key?: string };
      const target = runtime.launch_targets.find(
        (candidate) => candidate.key === body.target_key,
      );
      if (!target || !target.available) {
        await route.fulfill({
          status: 400,
          contentType: "application/json",
          body: JSON.stringify({
            detail: "launch target unavailable",
          }),
        });
        return;
      }
      opts?.runtimeEvents?.launches.push(target.key);
      let session = runtime.sessions.find(
        (candidate) =>
          candidate.target_key === target.key &&
          ["running", "starting"].includes(candidate.status),
      );
      if (!session) {
        const previous = runtime.sessions.find(
          (candidate) => candidate.target_key === target.key,
        );
        session = {
          key: previous?.key ?? `${ws.id}:${target.key}`,
          workspace_id: ws.id,
          target_key: target.key,
          label: target.label,
          kind: target.kind,
          status: "running",
          created_at: "2026-04-10T12:00:00Z",
        };
        runtime.sessions = [
          ...runtime.sessions.filter(
            (candidate) => candidate.key !== session.key,
          ),
          session,
        ];
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(session),
      });
    },
  );

  await page.route(
    (url) =>
      url.pathname.startsWith(
        `/api/v1/workspaces/${ws.id}/runtime/sessions/`,
      ),
    async (route) => {
      const url = new URL(route.request().url());
      const sessionKey = decodeURIComponent(
        url.pathname.split("/").at(-1) ?? "",
      );

      if (route.request().method() === "PATCH") {
        const body = JSON.parse(
          route.request().postData() ?? "{}",
        ) as { label?: string };
        const label = body.label?.trim() ?? "";
        const index = runtime.sessions.findIndex(
          (session) => session.key === sessionKey,
        );
        if (index < 0 || !label) {
          await route.fulfill({
            status: 404,
            contentType: "application/json",
            body: JSON.stringify({ detail: "session not found" }),
          });
          return;
        }
        const updated = {
          ...runtime.sessions[index],
          label,
        };
        runtime.sessions = runtime.sessions.map((session) =>
          session.key === sessionKey ? updated : session,
        );
        opts?.runtimeEvents?.renames.push({ sessionKey, label });
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(updated),
        });
        return;
      }

      if (route.request().method() !== "DELETE") {
        await route.continue();
        return;
      }
      runtime.sessions = runtime.sessions.filter(
        (session) => session.key !== sessionKey,
      );
      opts?.runtimeEvents?.deletes.push(sessionKey);
      await route.fulfill({ status: 204 });
    },
  );

  // Route roborev API calls using a predicate to avoid
  // matching Vite module URLs like /@fs/.../api/roborev/...
  await page.route(
    (url) => url.pathname === "/api/v1/roborev/status",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(rrStatus),
      });
    },
  );

  await page.route(
    (url) => url.pathname.startsWith("/api/roborev/"),
    async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname.endsWith("/api/repos")) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(rrRepos),
        });
        return;
      }
      if (
        url.pathname.endsWith("/api/jobs") ||
        url.pathname.includes("/api/jobs?")
      ) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(rrJobs),
        });
        return;
      }
      if (url.pathname.endsWith("/status")) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(rrStatus),
        });
        return;
      }
      if (url.pathname.includes("/stream/events")) {
        await route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: "",
        });
        return;
      }
      await route.fulfill({ status: 404 });
    },
  );

  return { runtime };
}

async function dragWorkflowTabToGroup(
  page: import("@playwright/test").Page,
  tabLabel: string,
  groupIndex: number,
  position: "center" | "left-edge",
): Promise<void> {
  await page.evaluate(
    ({ tabLabel, groupIndex, position }) => {
      const source = Array.from(
        document.querySelectorAll('[role="tab"]'),
      ).find((element) =>
        element.textContent?.includes(tabLabel),
      );
      const target = Array.from(
        document.querySelectorAll(
          '[aria-label="Workflow group drop targets"]',
        ),
      )[groupIndex];
      if (!(source instanceof HTMLElement)) {
        throw new Error(`Missing workflow tab: ${tabLabel}`);
      }
      if (!(target instanceof HTMLElement)) {
        throw new Error(`Missing workflow group: ${groupIndex}`);
      }

      const transfer = new DataTransfer();
      source.dispatchEvent(
        new DragEvent("dragstart", {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        }),
      );

      const rect = target.getBoundingClientRect();
      const clientX =
        position === "left-edge"
          ? rect.left + 4
          : rect.left + rect.width / 2;
      const clientY = rect.top + rect.height / 2;
      target.dispatchEvent(
        new DragEvent("dragover", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          dataTransfer: transfer,
        }),
      );
      target.dispatchEvent(
        new DragEvent("drop", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          dataTransfer: transfer,
        }),
      );
      source.dispatchEvent(
        new DragEvent("dragend", {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        }),
      );
    },
    { tabLabel, groupIndex, position },
  );
}

function workflowDragRuntime(): WorkspaceRuntime {
  return {
    ...workspaceRuntime,
    sessions: [
      {
        key: "ws-123:codex",
        workspace_id: "ws-123",
        target_key: "codex",
        label: "Codex",
        kind: "agent",
        status: "running",
        created_at: "2026-04-10T12:00:00Z",
      },
      {
        key: "ws-123:reviewer",
        workspace_id: "ws-123",
        target_key: "codex",
        label: "Reviewer",
        kind: "agent",
        status: "running",
        created_at: "2026-04-10T12:00:00Z",
      },
    ],
  };
}

function workflowDragLayout() {
  return {
    version: 1,
    open: false,
    dock: "bottom",
    height: 300,
    activeSessionKey: null,
    tree: null,
    terminalGroups: [],
    activeTerminalGroupID: null,
    sessionRegions: {
      "ws-123:codex": "workflow",
      "ws-123:reviewer": "workflow",
    },
    workflowMode: "tabs",
    workflowTree: {
      type: "split",
      id: "workflow-root",
      direction: "horizontal",
      ratio: 0.5,
      first: {
        type: "leaf",
        id: "workflow-left",
        tabs: ["home", "session:ws-123:codex"],
        activeTabKey: "session:ws-123:codex",
      },
      second: {
        type: "leaf",
        id: "workflow-right",
        tabs: ["session:ws-123:reviewer"],
        activeTabKey: "session:ws-123:reviewer",
      },
    },
    activeWorkflowLeafID: "workflow-left",
    recentWorkflowLeafIDs: ["workflow-left", "workflow-right"],
    customSessionLabels: {},
  };
}

function topDockedTerminalWorkflowLayout() {
  return {
    version: 1,
    open: true,
    dock: "top",
    height: 300,
    activeSessionKey: null,
    tree: null,
    terminalGroups: [],
    activeTerminalGroupID: null,
    sessionRegions: {
      "ws-123:codex": "workflow",
    },
    workflowMode: "tabs",
    workflowTree: {
      type: "leaf",
      id: "workflow-root",
      tabs: ["home", "terminal", "session:ws-123:codex"],
      activeTabKey: "home",
    },
    activeWorkflowLeafID: "workflow-root",
    recentWorkflowLeafIDs: ["workflow-root"],
    customSessionLabels: {},
  };
}

function closedTopDockedTerminalWorkflowLayout() {
  return {
    version: 1,
    open: false,
    dock: "top",
    height: 300,
    activeSessionKey: "ws-123:plain_shell",
    tree: {
      type: "leaf",
      id: "terminal-leaf",
      sessionKey: "ws-123:plain_shell",
    },
    terminalGroups: [
      {
        id: "terminal-group",
        activeSessionKey: "ws-123:plain_shell",
        tree: {
          type: "leaf",
          id: "terminal-leaf",
          sessionKey: "ws-123:plain_shell",
        },
      },
    ],
    activeTerminalGroupID: "terminal-group",
    sessionRegions: {
      "ws-123:plain_shell": "terminal",
    },
    workflowMode: "tabs",
    workflowTree: {
      type: "leaf",
      id: "workflow-root",
      tabs: ["home", "terminal"],
      activeTabKey: "home",
    },
    activeWorkflowLeafID: "workflow-root",
    recentWorkflowLeafIDs: ["workflow-root"],
    customSessionLabels: {},
  };
}

function shellWorkflowPreset() {
  return {
    id: "preset-shell",
    name: "Shell focus",
    createdAt: "2026-04-10T12:00:00.000Z",
    updatedAt: "2026-04-10T12:00:00.000Z",
    sessions: [],
    layout: {
      version: 1,
      open: false,
      dock: "bottom",
      height: 300,
      activeSessionKey: null,
      tree: null,
      terminalGroups: [],
      activeTerminalGroupID: null,
      sessionRegions: {},
      workflowMode: "tabs",
      workflowTree: {
        type: "leaf",
        id: "workflow-root",
        tabs: ["home", "shell"],
        activeTabKey: "shell",
      },
      activeWorkflowLeafID: "workflow-root",
      recentWorkflowLeafIDs: ["workflow-root"],
      customSessionLabels: {},
    },
  };
}

test(
  "roborev status mock ignores Vite module URLs",
  async ({ page }) => {
    await setupTerminalMocks(page);
    await page.goto("/");

    const response = await page.evaluate(async () => {
      const res = await fetch(
        "/@fs/tmp/project/api/v1/roborev/status",
      );
      return {
        status: res.status,
        body: await res.json(),
      };
    });

    expect(response).toEqual({
      status: 404,
      body: {
        error:
          "Unhandled GET /@fs/tmp/project/api/v1/roborev/status",
      },
    });
  },
);

test(
  "provider-aware detail mocks enforce provider and host identity",
  async ({ page }) => {
    await setupTerminalMocks(page);
    await page.goto("/");

    const statuses = await page.evaluate(async () => {
      const paths = [
        "/api/v1/pulls/github/acme/widgets/42",
        "/api/v1/pulls/github/acme/widgets/84",
        "/api/v1/host/example.com/pulls/github/acme/widgets/84",
        "/api/v1/pulls/gitlab/acme/widgets/42",
        "/api/v1/issues/github/acme/widgets/7",
        "/api/v1/issues/gitlab/acme/widgets/7",
      ];
      return Object.fromEntries(
        await Promise.all(
          paths.map(async (path) => {
            const response = await fetch(path);
            return [path, response.status];
          }),
        ),
      );
    });

    expect(statuses).toEqual({
      "/api/v1/pulls/github/acme/widgets/42": 200,
      "/api/v1/pulls/github/acme/widgets/84": 404,
      "/api/v1/host/example.com/pulls/github/acme/widgets/84": 200,
      "/api/v1/pulls/gitlab/acme/widgets/42": 404,
      "/api/v1/issues/github/acme/widgets/7": 200,
      "/api/v1/issues/gitlab/acme/widgets/7": 404,
    });
  },
);

test.describe("terminal state icons", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
  });

  test(
    "creating workspace shows spinner icon",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: {
          ...testWorkspace,
          status: "creating",
        },
      });

      await page.goto("/terminal/ws-123");

      const stateMessage = page.locator(
        ".state-message",
      );
      await expect(stateMessage).toContainText(
        "Setting up workspace...",
      );
      await expect(
        stateMessage.locator(".spinner"),
      ).toBeVisible();
    },
  );

  test(
    "workspace load failure shows alert icon and retry recovers",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspaceDetailResponses: [
          {
            status: 500,
            body: { error: "Internal error" },
          },
          {
            status: 200,
            body: testWorkspace,
          },
        ],
      });

      await page.goto("/terminal/ws-123");

      const stateMessage = page.locator(
        ".state-message.error",
      );
      await expect(stateMessage).toContainText(
        "Failed to load workspace (500)",
      );
      await expect(
        stateMessage.getByLabel(
          "Workspace load failed",
        ),
      ).toBeVisible();

      await stateMessage
        .getByRole("button", { name: "Retry" })
        .click();

      await expect(
        page.locator(".header-name"),
      ).toContainText("Add auth middleware");
    },
  );

  test(
    "workspace setup error retries setup and recovers",
    async ({ page }) => {
      let retryCalls = 0;
      await setupTerminalMocks(page, {
        workspaceDetailResponses: [
          {
            status: 200,
            body: {
              ...testWorkspace,
              status: "error",
              error_message:
                "tmux bootstrap failed",
            },
          },
          {
            status: 200,
            body: testWorkspace,
          },
        ],
        workspaceRetryResponse: {
          status: 202,
          body: { ...testWorkspace, status: "creating" },
        },
      });
      await page.route(
        "**/api/v1/workspaces/ws-123/retry",
        async (route) => {
          retryCalls += 1;
          await route.fulfill({
            status: 202,
            contentType: "application/json",
            body: JSON.stringify({
              ...testWorkspace,
              status: "creating",
            }),
          });
        },
      );

      await page.goto("/terminal/ws-123");

      const stateMessage = page.locator(
        ".state-message.error",
      );
      await expect(stateMessage).toContainText(
        "tmux bootstrap failed",
      );
      await expect(
        stateMessage.getByLabel(
          "Workspace setup failed",
        ),
      ).toBeVisible();

      await stateMessage
        .getByRole("button", { name: "Retry" })
        .click();

      expect(retryCalls).toBe(1);
      await expect(
        page.locator(".header-name"),
      ).toContainText("Add auth middleware");
    },
  );

  test(
    "workspace setup error can be deleted",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspaceDetailResponses: [
          {
            status: 200,
            body: {
              ...testWorkspace,
              status: "error",
              error_message: "ensure clone failed",
            },
          },
        ],
      });

      await page.goto("/terminal/ws-123");

      const stateMessage = page.locator(
        ".state-message.error",
      );
      await expect(stateMessage).toContainText(
        "ensure clone failed",
      );

      await stateMessage
        .getByRole("button", { name: "Delete" })
        .click();

      await expect(page).toHaveURL(/\/workspaces$/);
    },
  );

  test(
    "force-delete prompt confirms and retries delete with force=true",
    async ({ page }) => {
      const deleteRequests: string[] = [];
      page.on("request", (req) => {
        if (
          req.method() === "DELETE" &&
          req.url().includes("/api/v1/workspaces/ws-123")
        ) {
          deleteRequests.push(req.url());
        }
      });

      await setupTerminalMocks(page, {
        workspaceDeleteResponses: [
          {
            status: 409,
            body: {
              detail: "Worktree has uncommitted changes.",
            },
          },
          { status: 204 },
        ],
      });

      await page.goto("/terminal/ws-123");

      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();

      const dialog = page.getByRole("dialog", {
        name: "Force delete workspace?",
      });
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText(
        "Worktree has uncommitted changes.",
      );

      await dialog
        .getByRole("button", { name: "Force delete" })
        .click();

      await expect(page).toHaveURL(/\/workspaces$/);
      expect(deleteRequests).toHaveLength(2);
      expect(deleteRequests[1]).toContain("force=true");
    },
  );

  test(
    "force-delete prompt cancel keeps the workspace and the modal closes",
    async ({ page }) => {
      const deleteRequests: string[] = [];
      page.on("request", (req) => {
        if (
          req.method() === "DELETE" &&
          req.url().includes("/api/v1/workspaces/ws-123")
        ) {
          deleteRequests.push(req.url());
        }
      });

      await setupTerminalMocks(page, {
        workspaceDeleteResponses: [
          {
            status: 409,
            body: {
              detail: "Worktree has uncommitted changes.",
            },
          },
        ],
      });

      await page.goto("/terminal/ws-123");

      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();

      const dialog = page.getByRole("dialog", {
        name: "Force delete workspace?",
      });
      await expect(dialog).toBeVisible();

      await dialog
        .getByRole("button", { name: "Cancel" })
        .click();

      await expect(dialog).toBeHidden();
      await expect(page).not.toHaveURL(/\/workspaces$/);
      expect(deleteRequests).toHaveLength(1);
    },
  );

  test(
    "force-delete prompt traps focus, makes background inert, and restores focus on cancel",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspaceDeleteResponses: [
          {
            status: 409,
            body: {
              detail: "Worktree has uncommitted changes.",
            },
          },
        ],
      });

      await page.goto("/terminal/ws-123");

      const headerDelete = page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" });
      await headerDelete.click();

      const dialog = page.getByRole("dialog", {
        name: "Force delete workspace?",
      });
      await expect(dialog).toBeVisible();

      const cancel = dialog.getByRole("button", { name: "Cancel" });
      const force = dialog.getByRole("button", {
        name: "Force delete",
      });

      // Initial focus lands on Cancel — the safe default for a destructive action.
      await expect(cancel).toBeFocused();

      // The workspace shell beneath the modal is inert and unreachable.
      await expect(page.locator(".terminal-view")).toHaveAttribute(
        "inert",
        "",
      );

      // Tab cycles within the dialog (Cancel -> Force delete -> Cancel).
      await page.keyboard.press("Tab");
      await expect(force).toBeFocused();
      await page.keyboard.press("Tab");
      await expect(cancel).toBeFocused();
      await page.keyboard.press("Shift+Tab");
      await expect(force).toBeFocused();

      // Closing the dialog restores focus to the trigger.
      await cancel.click();
      await expect(dialog).toBeHidden();
      await expect(page.locator(".terminal-view")).not.toHaveAttribute(
        "inert",
        "",
      );
      await expect(headerDelete).toBeFocused();
    },
  );

  test(
    "force-delete prompt is dismissed when the workspace route changes",
    async ({ page }) => {
      const deleteRequests: string[] = [];
      page.on("request", (req) => {
        if (req.method() === "DELETE") {
          deleteRequests.push(req.url());
        }
      });

      await setupTerminalMocks(page, {
        workspaceDeleteResponses: [
          {
            status: 409,
            body: {
              detail: "Worktree has uncommitted changes.",
            },
          },
        ],
      });

      await page.goto("/terminal/ws-123");

      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();

      const dialog = page.getByRole("dialog", {
        name: "Force delete workspace?",
      });
      await expect(dialog).toBeVisible();

      // The component persists across workspaceId changes (no {#key} wrapper),
      // so the prompt would otherwise stay open after navigation and leak a
      // destructive confirmation onto the next workspace.
      await page.evaluate(() => {
        history.pushState(null, "", "/workspaces");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });

      await expect(dialog).toBeHidden();
      // Only the initial DELETE fired — no force-delete reached the server
      // after the route change dismissed the prompt.
      expect(deleteRequests).toHaveLength(1);
      expect(deleteRequests[0]).not.toContain("force=true");
    },
  );

  test(
    "in-flight 409 response does not surface a prompt after the user has navigated away",
    async ({ page }) => {
      await setupTerminalMocks(page);

      // Replace the default DELETE handler with one that holds the
      // 409 response long enough for the test to navigate before
      // it lands.
      await page.route(
        (url) =>
          url.pathname === "/api/v1/workspaces/ws-123",
        async (route) => {
          if (route.request().method() !== "DELETE") {
            await route.fallback();
            return;
          }
          await new Promise((resolve) => setTimeout(resolve, 400));
          await route.fulfill({
            status: 409,
            contentType: "application/json",
            body: JSON.stringify({
              detail: "Worktree has uncommitted changes.",
            }),
          });
        },
      );

      await page.goto("/terminal/ws-123");

      // Kick off the DELETE, then immediately leave the workspace.
      // The DELETE handler in handleDelete is async, so this is the
      // exact race condition the post-await guard exists to handle.
      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();
      await page.evaluate(() => {
        history.pushState(null, "", "/workspaces");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });

      // Wait past the route delay so the response settles either way.
      await page.waitForTimeout(700);

      await expect(
        page.getByRole("dialog", {
          name: "Force delete workspace?",
        }),
      ).toBeHidden();
    },
  );

  test(
    "stale 409 does not reopen the prompt after the user leaves and returns to the same workspace",
    async ({ page }) => {
      await setupTerminalMocks(page);

      await page.route(
        (url) =>
          url.pathname === "/api/v1/workspaces/ws-123",
        async (route) => {
          if (route.request().method() !== "DELETE") {
            await route.fallback();
            return;
          }
          // Hold the response long enough for the test to navigate
          // away and back before it lands.
          await new Promise((resolve) => setTimeout(resolve, 500));
          await route.fulfill({
            status: 409,
            contentType: "application/json",
            body: JSON.stringify({
              detail: "Worktree has uncommitted changes.",
            }),
          });
        },
      );

      await page.goto("/terminal/ws-123");

      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();

      // A round-trip back to the same workspace would defeat an
      // id-only guard: the captured targetId matches the current
      // workspaceId, but the prompt should still not reappear
      // because the user has explicitly left and returned. Two
      // separate evaluate calls mirror real user clicks — each
      // pops an event-loop turn so Svelte's effects flush between
      // them and the generation counter advances.
      await page.evaluate(() => {
        history.pushState(null, "", "/workspaces");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });
      await page.evaluate(() => {
        history.pushState(null, "", "/terminal/ws-123");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });

      await page.waitForTimeout(800);

      await expect(
        page.getByRole("dialog", {
          name: "Force delete workspace?",
        }),
      ).toBeHidden();
    },
  );

  test(
    "in-flight successful DELETE does not yank the user off the route they chose",
    async ({ page }) => {
      await setupTerminalMocks(page);

      await page.route(
        (url) =>
          url.pathname === "/api/v1/workspaces/ws-123",
        async (route) => {
          if (route.request().method() !== "DELETE") {
            await route.fallback();
            return;
          }
          // Delay long enough for the user to navigate away
          // before the 204 lands.
          await new Promise((resolve) => setTimeout(resolve, 400));
          await route.fulfill({ status: 204 });
        },
      );

      await page.goto("/terminal/ws-123");

      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();

      // Without the post-await guard in handleDelete, the stale
      // 204 would call navigate("/workspaces") and the user would
      // be yanked away from /pulls.
      await page.evaluate(() => {
        history.pushState(null, "", "/pulls");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });

      await page.waitForTimeout(700);

      await expect(page).toHaveURL(/\/pulls$/);
    },
  );

  test(
    "successful DELETE after A→B→A still navigates away from the deleted workspace",
    async ({ page }) => {
      await setupTerminalMocks(page);

      await page.route(
        (url) =>
          url.pathname === "/api/v1/workspaces/ws-123",
        async (route) => {
          if (route.request().method() !== "DELETE") {
            await route.fallback();
            return;
          }
          await new Promise((resolve) => setTimeout(resolve, 500));
          await route.fulfill({ status: 204 });
        },
      );

      await page.goto("/terminal/ws-123");

      await page
        .locator(".header-bar")
        .getByRole("button", { name: "Delete" })
        .click();

      // Round-trip back to the same workspace before the 204 lands.
      // The generation token has advanced, but the workspace the
      // user is looking at has just been destroyed on the server —
      // we must still navigate away rather than leave them staring
      // at a dead workspace.
      await page.evaluate(() => {
        history.pushState(null, "", "/workspaces");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });
      await page.evaluate(() => {
        history.pushState(null, "", "/terminal/ws-123");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });

      await expect(page).toHaveURL(/\/workspaces$/, {
        timeout: 2000,
      });
    },
  );
});

test.describe("workspace launch home", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-list-sidebar-width",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
    await setupTerminalMocks(page);
  });

  test(
    "shows Worktree Home and does not attach a terminal by default",
    async ({ page }) => {
      const terminalSockets: string[] = [];
      page.on("websocket", (socket) => {
        const url = socket.url();
        if (url.includes("/terminal")) {
          terminalSockets.push(url);
        }
      });

      await page.goto("/terminal/ws-123");

      await expect(
        page.getByRole("tab", { name: "Home" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Launch" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Codex" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Shell" }),
      ).toBeDisabled();
      await expect(
        page.getByRole("button", {
          name: "Open terminal panel",
        }),
      ).toBeVisible();
      await expect(page.getByText("Plain shell")).toHaveCount(0);
      await expect
        .poll(() => terminalSockets.length)
        .toBe(0);
    },
  );

  test(
    "does not attach restored runtime sessions until selected",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        runtime: {
          ...workspaceRuntime,
          sessions: [
            {
              key: "ws-123:codex",
              workspace_id: "ws-123",
              target_key: "codex",
              label: "Codex",
              kind: "agent",
              status: "running",
              created_at: "2026-04-10T12:00:00Z",
            },
          ],
        },
      });

      await page.addInitScript(() => {
        const OriginalWebSocket = window.WebSocket;
        const urls: string[] = [];
        Object.defineProperty(
          window,
          "__middlemanWebSocketUrls",
          {
            value: urls,
          },
        );
        window.WebSocket = class extends OriginalWebSocket {
          constructor(
            url: string | URL,
            protocols?: string | string[],
          ) {
            urls.push(String(url));
            if (protocols === undefined) {
              super(url);
            } else {
              super(url, protocols);
            }
          }
        };
      });

      await page.goto("/terminal/ws-123");

      const tabs = page.getByRole("region", { name: "Workflow panes" });
      await expect(
        tabs.getByRole("tab", { name: "Codex" }),
      ).toBeVisible();
      const initialTerminalSockets = await page.evaluate(() =>
        (
          (
            window as unknown as {
              __middlemanWebSocketUrls: string[];
            }
          ).__middlemanWebSocketUrls ?? []
        ).filter((url) =>
          url.includes("/ws/v1/workspaces/ws-123/"),
        ),
      );
      expect(initialTerminalSockets).toEqual([]);

      await tabs.getByRole("tab", { name: "Codex" }).click();

      await expect(
        page.locator(".terminal-container"),
      ).toBeVisible();
      await expect
        .poll(async () => {
          const urls = await page.evaluate(() =>
            (
              (
                window as unknown as {
                  __middlemanWebSocketUrls: string[];
                }
              ).__middlemanWebSocketUrls ?? []
            ).filter((url) =>
              url.includes("/ws/v1/workspaces/ws-123/"),
            ),
          );
          return urls.some((url) =>
            url.includes(
              "/runtime/sessions/ws-123%3Acodex/terminal",
            ),
          );
        })
        .toBe(true);
    },
  );

  test(
    "selects the top-docked terminal when moving an inactive workflow tab into it",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        runtime: {
          ...workspaceRuntime,
          sessions: [
            {
              key: "ws-123:codex",
              workspace_id: "ws-123",
              target_key: "codex",
              label: "Codex",
              kind: "agent",
              status: "running",
              created_at: "2026-04-10T12:00:00Z",
            },
          ],
        },
      });
      await page.addInitScript((layout) => {
        localStorage.setItem(
          "middleman-workspace-terminal-layout:ws-123",
          JSON.stringify(layout),
        );
        localStorage.setItem(
          "middleman-workspace-active-tab:ws-123",
          "home",
        );
      }, topDockedTerminalWorkflowLayout());

      await page.goto("/terminal/ws-123");

      const workflow = page.getByRole("region", {
        name: "Workflow panes",
      });
      await expect(
        workflow.getByRole("tab", { name: "Home" }),
      ).toHaveAttribute("aria-selected", "true");
      await expect(
        workflow.getByRole("tab", { name: "Terminal" }),
      ).toHaveAttribute("aria-selected", "false");

      await workflow
        .getByRole("button", { name: "Move Codex to terminal" })
        .click();

      await expect(
        workflow.getByRole("tab", { name: "Terminal" }),
      ).toHaveAttribute("aria-selected", "true");
      await expect(
        page.locator(".workflow-leaf .terminal-container"),
      ).toBeVisible();
    },
  );

  test(
    "keeps a closed top-docked terminal reachable when terminal sessions exist",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        runtime: {
          ...workspaceRuntime,
          sessions: [
            {
              key: "ws-123:plain_shell",
              workspace_id: "ws-123",
              target_key: "plain_shell",
              label: "Shell",
              kind: "plain_shell",
              status: "running",
              created_at: "2026-04-10T12:00:00Z",
            },
          ],
        },
      });
      await page.addInitScript((layout) => {
        localStorage.setItem(
          "middleman-workspace-terminal-layout:ws-123",
          JSON.stringify(layout),
        );
        localStorage.setItem(
          "middleman-workspace-active-tab:ws-123",
          "home",
        );
      }, closedTopDockedTerminalWorkflowLayout());

      await page.goto("/terminal/ws-123");

      const workflow = page.getByRole("region", {
        name: "Workflow panes",
      });
      const terminalTab = workflow.getByRole("tab", {
        name: "Terminal",
      });
      await expect(terminalTab).toBeVisible();
      await expect(terminalTab).toHaveAttribute(
        "aria-selected",
        "false",
      );
      await expect(
        page.locator(".workflow-leaf .terminal-container"),
      ).toHaveCount(0);

      await terminalTab.click();

      await expect(terminalTab).toHaveAttribute(
        "aria-selected",
        "true",
      );
      await expect(
        page.locator(".workflow-leaf .terminal-container"),
      ).toBeVisible();
    },
  );

  test(
    "applies a workflow preset that restores the Shell workflow tab",
    async ({ page }) => {
      await page.addInitScript((preset) => {
        localStorage.removeItem(
          "middleman-workspace-terminal-layout:ws-123",
        );
        localStorage.setItem(
          "middleman-workspace-layout-presets",
          JSON.stringify([preset]),
        );
        localStorage.setItem(
          "middleman-workspace-active-tab:ws-123",
          "home",
        );
      }, shellWorkflowPreset());
      await setupTerminalMocks(page);

      await page.goto("/terminal/ws-123");

      await page
        .getByRole("button", { name: "Workflow presets" })
        .click();
      await page
        .getByRole("dialog", { name: "Workflow presets" })
        .getByRole("button", { name: "Shell focus", exact: true })
        .click();

      const workflow = page.getByRole("region", {
        name: "Workflow panes",
      });
      await expect(
        workflow.getByRole("tab", { name: "Shell" }),
      ).toHaveAttribute("aria-selected", "true");
      await expect(
        page.locator(".workflow-leaf .terminal-container"),
      ).toBeVisible();
    },
  );

  test(
    "workflow pane drops append in the center and split at the edge",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        runtime: workflowDragRuntime(),
      });
      await page.addInitScript((layout) => {
        localStorage.setItem(
          "middleman-workspace-terminal-layout:ws-123",
          JSON.stringify(layout),
        );
      }, workflowDragLayout());

      await page.goto("/terminal/ws-123");

      await expect(page.locator(".workflow-leaf")).toHaveCount(2);
      await dragWorkflowTabToGroup(page, "Reviewer", 0, "center");
      await expect(page.locator(".workflow-leaf")).toHaveCount(1);
      await expect(
        page
          .locator(".workflow-leaf")
          .first()
          .getByRole("tab", { name: /Reviewer/ }),
      ).toBeVisible();

      await page.evaluate((layout) => {
        localStorage.setItem(
          "middleman-workspace-terminal-layout:ws-123",
          JSON.stringify(layout),
        );
      }, workflowDragLayout());
      await page.reload();

      await expect(page.locator(".workflow-leaf")).toHaveCount(2);
      await dragWorkflowTabToGroup(page, "Reviewer", 0, "left-edge");
      await expect(page.locator(".workflow-leaf")).toHaveCount(2);
      await expect(
        page
          .locator(".workflow-leaf")
          .first()
          .getByRole("tab", { name: /Reviewer/ }),
      ).toBeVisible();
    },
  );

  test(
    "saves updates applies and deletes workflow presets",
    async ({ page }) => {
      await page.addInitScript(() => {
        localStorage.removeItem(
          "middleman-workspace-terminal-layout:ws-123",
        );
        localStorage.removeItem(
          "middleman-workspace-layout-presets",
        );
      });
      const runtimeEvents: RuntimeEvents = {
        launches: [],
        renames: [],
        deletes: [],
      };
      const mocked = await setupTerminalMocks(page, {
        runtimeEvents,
      });

      await page.goto("/terminal/ws-123");
      await page
        .getByRole("button", { name: "Codex" })
        .click();
      await expect(
        page.getByRole("tab", { name: "Codex" }),
      ).toBeVisible();
      await page
        .getByRole("button", {
          name: "Open terminal panel",
        })
        .click();
      await expect(
        page.locator(".terminal-panel.open .terminal-container"),
      ).toBeVisible();

      await page
        .getByRole("button", { name: "Rename Codex" })
        .click();
      const renameDialog = page.getByRole("dialog", {
        name: "Rename tab",
      });
      await renameDialog.getByLabel("Name").fill("Reviewer");
      await renameDialog
        .getByRole("button", { name: "Save" })
        .click();
      await expect(
        page.getByRole("tab", { name: "Reviewer" }),
      ).toBeVisible();

      let presetPromptMessage = "";
      page.once("dialog", async (dialog) => {
        presetPromptMessage = dialog.message();
        await dialog.accept("Review pair");
      });
      await page
        .getByRole("button", { name: "Workflow presets" })
        .click();
      await page
        .getByRole("dialog", { name: "Workflow presets" })
        .getByRole("button", { name: "Save as preset" })
        .click();
      expect(presetPromptMessage).toBe("Preset name");
      await expect
        .poll(() =>
          page.evaluate(() => {
            const raw = localStorage.getItem(
              "middleman-workspace-layout-presets",
            );
            const presets = raw ? JSON.parse(raw) : [];
            return presets.map((preset: { name: string }) => preset.name);
          }),
        )
        .toEqual(["Review pair"]);

      await page
        .getByRole("button", { name: "Rename Reviewer" })
        .click();
      await renameDialog.getByLabel("Name").fill("Navigator");
      await renameDialog
        .getByRole("button", { name: "Save" })
        .click();
      await expect(
        page.getByRole("tab", { name: "Navigator" }),
      ).toBeVisible();

      await page
        .getByRole("button", { name: "Workflow presets" })
        .click();
      const presetDialog = page.getByRole("dialog", {
        name: "Workflow presets",
      });
      await expect(
        presetDialog.getByRole("button", {
          name: "Update selected",
        }),
      ).toBeEnabled();
      await presetDialog
        .getByRole("button", { name: "Update selected" })
        .click();
      await expect
        .poll(() =>
          page.evaluate(() => {
            const raw = localStorage.getItem(
              "middleman-workspace-layout-presets",
            );
            const presets = raw ? JSON.parse(raw) : [];
            return presets[0]?.sessions.find(
              (session: { targetKey: string }) =>
                session.targetKey === "codex",
            )?.label;
          }),
        )
        .toBe("Navigator");

      await page
        .locator(
          '.terminal-panel .panel-action[aria-label="Close terminal panel"]',
        )
        .click();
      await expect(
        page.locator(".terminal-panel.open"),
      ).toHaveCount(0);
      mocked.runtime.sessions = [];
      runtimeEvents.launches = [];
      runtimeEvents.renames = [];
      runtimeEvents.deletes = [];

      await page
        .getByRole("button", { name: "Workflow presets" })
        .click();
      await page
        .getByRole("dialog", { name: "Workflow presets" })
        .getByRole("button", { name: "Review pair", exact: true })
        .click();
      await expect
        .poll(() => runtimeEvents.launches)
        .toEqual(["codex", "plain_shell"]);
      await expect
        .poll(() => runtimeEvents.renames)
        .toEqual([
          {
            sessionKey: "ws-123:codex",
            label: "Navigator",
          },
        ]);
      await expect(
        page.getByRole("tab", { name: "Navigator" }),
      ).toBeVisible();
      await expect(
        page.locator(".terminal-panel.open .terminal-container"),
      ).toBeVisible();
      await expect
        .poll(() =>
          page.evaluate(() => {
            const raw = localStorage.getItem(
              "middleman-workspace-terminal-layout:ws-123",
            );
            const layout = raw ? JSON.parse(raw) : null;
            return {
              open: layout?.open,
              codexRegion: layout?.sessionRegions?.["ws-123:codex"],
              shellRegion:
                layout?.sessionRegions?.["ws-123:plain_shell"],
            };
          }),
        )
        .toEqual({
          open: true,
          codexRegion: "workflow",
          shellRegion: "terminal",
        });

      await page
        .getByRole("button", { name: "Workflow presets" })
        .click();
      await page
        .getByRole("dialog", { name: "Workflow presets" })
        .getByRole("button", { name: "Delete Review pair" })
        .click();
      await expect
        .poll(() =>
          page.evaluate(() => {
            const raw = localStorage.getItem(
              "middleman-workspace-layout-presets",
            );
            return raw ? JSON.parse(raw).length : 0;
          }),
        )
        .toBe(0);
      await expect(
        page
          .getByRole("dialog", { name: "Workflow presets" })
          .getByText("No presets saved"),
      ).toBeVisible();
    },
  );

  test(
    "launches an agent into a compact running tab",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      await page
        .getByRole("button", { name: "Codex" })
        .click();

      const tabs = page.getByRole("region", { name: "Workflow panes" });
      await expect(
        tabs.getByRole("tab", { name: "Codex" }),
      ).toBeVisible();
      await expect(
        page.locator(".terminal-container"),
      ).toBeVisible();
    },
  );

  test(
    "xterm workspace terminal sends resize frames after viewport changes",
    async ({ page }) => {
      test.setTimeout(60_000);
      await page.addInitScript(() => {
        type RecordedSocket = {
          sent: unknown[];
          url: string;
        };
        const recordedSockets: RecordedSocket[] = [];
        Object.defineProperty(
          window,
          "__middlemanRecordedTerminalSockets",
          {
            value: recordedSockets,
          },
        );
        const NativeWebSocket = window.WebSocket;

        class MockTerminalWebSocket extends EventTarget {
          static CONNECTING = 0;
          static OPEN = 1;
          static CLOSING = 2;
          static CLOSED = 3;

          binaryType = "arraybuffer";
          extensions = "";
          onclose: ((event: CloseEvent) => void) | null = null;
          onerror: ((event: Event) => void) | null = null;
          onmessage: ((event: MessageEvent) => void) | null = null;
          onopen: ((event: Event) => void) | null = null;
          protocol = "";
          readyState = MockTerminalWebSocket.OPEN;
          readonly url: string;
          private readonly record?: RecordedSocket;

          constructor(
            url: string | URL,
            protocols?: string | string[],
          ) {
            super();
            this.url = String(url);
            if (!this.url.includes("/ws/v1/workspaces/")) {
              return new NativeWebSocket(url, protocols);
            }
            this.record = { url: this.url, sent: [] };
            recordedSockets.push(this.record);
            queueMicrotask(() => {
              const event = new Event("open");
              this.dispatchEvent(event);
              this.onopen?.(event);
            });
          }

          close(): void {
            this.readyState = MockTerminalWebSocket.CLOSED;
            const event = new CloseEvent("close");
            this.dispatchEvent(event);
            this.onclose?.(event);
          }

          send(data: unknown): void {
            if (!this.record) return;
            if (typeof data === "string") {
              this.record.sent.push(data);
              return;
            }
            this.record.sent.push("[binary]");
          }
        }

        window.WebSocket =
          MockTerminalWebSocket as unknown as typeof WebSocket;
      });

      await page.goto("/terminal/ws-123", {
        waitUntil: "domcontentloaded",
      });
      const launchTarget = page.getByRole("button", { name: "Codex" });
      await expect(launchTarget).toBeVisible();
      await launchTarget.click();

      await expect(
        page.locator(".terminal-container .xterm"),
      ).toBeVisible();
      await page.evaluate(() => {
        for (const socket of (
          window as unknown as {
            __middlemanRecordedTerminalSockets: Array<{
              sent: unknown[];
            }>;
          }
        ).__middlemanRecordedTerminalSockets) {
          socket.sent = [];
        }
      });

      await page.setViewportSize({ width: 900, height: 700 });
      await page.setViewportSize({ width: 1200, height: 800 });

      await expect
        .poll(async () =>
          page.evaluate(() =>
            (
              window as unknown as {
                __middlemanRecordedTerminalSockets: Array<{
                  sent: unknown[];
                }>;
              }
            ).__middlemanRecordedTerminalSockets.some((socket) =>
              socket.sent.some((frame) => {
                if (typeof frame !== "string") return false;
                try {
                  return JSON.parse(frame).type === "resize";
                } catch {
                  return false;
                }
              }),
            ),
          ),
        )
        .toBe(true);
    },
  );

  test(
    "xterm workspace terminal sends multiline browser paste as one payload",
    async ({ page }) => {
      await page.addInitScript(() => {
        type RecordedSocket = {
          sent: unknown[];
          url: string;
        };
        const recordedSockets: RecordedSocket[] = [];
        Object.defineProperty(
          window,
          "__middlemanRecordedTerminalSockets",
          {
            value: recordedSockets,
          },
        );
        const NativeWebSocket = window.WebSocket;

        class MockTerminalWebSocket extends EventTarget {
          static CONNECTING = 0;
          static OPEN = 1;
          static CLOSING = 2;
          static CLOSED = 3;

          binaryType = "arraybuffer";
          extensions = "";
          onclose: ((event: CloseEvent) => void) | null = null;
          onerror: ((event: Event) => void) | null = null;
          onmessage: ((event: MessageEvent) => void) | null = null;
          onopen: ((event: Event) => void) | null = null;
          protocol = "";
          readyState = MockTerminalWebSocket.OPEN;
          readonly url: string;
          private readonly record?: RecordedSocket;

          constructor(
            url: string | URL,
            protocols?: string | string[],
          ) {
            super();
            this.url = String(url);
            if (!this.url.includes("/ws/v1/workspaces/")) {
              return new NativeWebSocket(url, protocols);
            }
            this.record = { url: this.url, sent: [] };
            recordedSockets.push(this.record);
            queueMicrotask(() => {
              const event = new Event("open");
              this.dispatchEvent(event);
              this.onopen?.(event);
            });
          }

          close(): void {
            this.readyState = MockTerminalWebSocket.CLOSED;
            const event = new CloseEvent("close");
            this.dispatchEvent(event);
            this.onclose?.(event);
          }

          send(data: unknown): void {
            if (!this.record) return;
            if (data instanceof ArrayBuffer) {
              this.record.sent.push(
                Array.from(new Uint8Array(data)),
              );
              return;
            }
            if (ArrayBuffer.isView(data)) {
              this.record.sent.push(
                Array.from(
                  new Uint8Array(
                    data.buffer,
                    data.byteOffset,
                    data.byteLength,
                  ),
                ),
              );
              return;
            }
            this.record.sent.push(data);
          }
        }

        window.WebSocket =
          MockTerminalWebSocket as unknown as typeof WebSocket;
      });

      await page.goto("/terminal/ws-123", {
        waitUntil: "domcontentloaded",
      });
      const launchTarget = page.getByRole("button", { name: "Codex" });
      await expect(launchTarget).toBeVisible();
      await launchTarget.click();

      await expect(page.locator(".terminal-container .xterm")).toBeVisible();
      await page.evaluate(() => {
        for (const socket of (
          window as unknown as {
            __middlemanRecordedTerminalSockets: Array<{
              sent: unknown[];
            }>;
          }
        ).__middlemanRecordedTerminalSockets) {
          socket.sent = [];
        }
      });

      const terminal = page.locator(".terminal-container").first();
      await terminal.evaluate((element) => {
        const event = new Event("paste", {
          bubbles: true,
          cancelable: true,
        }) as ClipboardEvent;
        Object.defineProperty(event, "clipboardData", {
          value: {
            getData: (type: string) =>
              type === "text/plain" ? "first\nsecond\nthird" : "",
          },
        });
        element.dispatchEvent(event);
      });

      await expect
        .poll(async () =>
          page.evaluate(() => {
            const decoder = new TextDecoder();
            return (
              window as unknown as {
                __middlemanRecordedTerminalSockets: Array<{
                  sent: unknown[];
                }>;
              }
            ).__middlemanRecordedTerminalSockets
              .flatMap((socket) => socket.sent)
              .map((frame) =>
                Array.isArray(frame)
                  ? decoder.decode(new Uint8Array(frame))
                  : frame,
              );
          }),
        )
        .toContainEqual("first\nsecond\nthird");
    },
  );

  test(
    "opens the plain shell from the bottom terminal panel",
    async ({ page }) => {
      const terminalSockets: string[] = [];
      page.on("websocket", (socket) => {
        terminalSockets.push(socket.url());
      });

      await page.goto("/terminal/ws-123");
      await page
        .getByRole("button", {
          name: "Open terminal panel",
        })
        .click();

      await expect(
        page.locator(".terminal-panel.open .terminal-container"),
      ).toBeVisible();
      await expect
        .poll(() =>
          terminalSockets.some((url) =>
            url.includes(
              "/runtime/sessions/ws-123%3Aplain_shell/terminal",
            ),
          ),
        )
        .toBe(true);
    },
  );

  test(
    "restarts an exited shell session when opening the terminal panel",
    async ({ page }) => {
      const shellEnsures: string[] = [];
      page.on("request", (request) => {
        if (
          request.method() === "POST" &&
          request.url().includes("/runtime/sessions")
        ) {
          shellEnsures.push(request.url());
        }
      });

      await setupTerminalMocks(page, {
        runtime: {
          ...workspaceRuntime,
          sessions: [
            {
              key: "ws-123:plain_shell",
              workspace_id: "ws-123",
              target_key: "plain_shell",
              label: "Plain shell",
              kind: "plain_shell",
              status: "exited",
              created_at: "2026-04-10T12:00:00Z",
            },
          ],
        },
      });

      await page.goto("/terminal/ws-123");
      await page
        .getByRole("button", {
          name: "Open terminal panel",
        })
        .click();

      await expect
        .poll(() => shellEnsures.length)
        .toBe(1);
      await expect(
        page.locator(".terminal-panel.open .terminal-container"),
      ).toBeVisible();
    },
  );
});

// -------------------------------------------------------
// Group 1: Toggle Behavior
// -------------------------------------------------------

test.describe("sidebar toggle behavior", () => {
  test.beforeEach(async ({ page }) => {
    // Clear any persisted sidebar state before each test.
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-list-sidebar-width",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
    await setupTerminalMocks(page);
  });

  test(
    "workspace row shows working indicator with activity source",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: {
          ...testWorkspace,
          tmux_pane_title: "⠴ t3code-b5014b03",
          tmux_working: true,
          tmux_activity_source: "title",
        },
      });

      await page.goto("/terminal/ws-123");

      const row = page.locator(".ws-row", {
        hasText: "Add auth middleware",
      });
      const pulse = row.locator(".working-pulse");
      await expect(pulse).toBeVisible();
      await expect(pulse).toHaveAttribute(
        "title",
        "Working (title): ⠴ t3code-b5014b03",
      );
      await expect(pulse).toHaveAttribute(
        "aria-label",
        "Working (title): ⠴ t3code-b5014b03",
      );
    },
  );

  test(
    "workspace list polls while mounted",
    async ({ page }) => {
      await setupTerminalMocks(page);
      let listRequests = 0;
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            listRequests += 1;
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({
                workspaces: [testWorkspace],
              }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );

      await page.goto("/terminal/ws-123");

      await expect
        .poll(() => listRequests)
        .toBeGreaterThanOrEqual(1);
      await expect
        .poll(() => listRequests, { timeout: 6500 })
        .toBeGreaterThanOrEqual(2);
    },
  );

  test(
    "workspace list resize reclamps the right sidebar",
    async ({ page }) => {
      await page.setViewportSize({ width: 980, height: 720 });
      await page.goto("/terminal/ws-123");

      const listSidebar = page.locator(
        ".workspace-list-sidebar",
      );
      await expect(listSidebar).toBeVisible();

      const prBtn = page.locator(".seg-btn", {
        hasText: "PR",
      });
      await prBtn.click();
      const rightSidebar = page.locator(".right-sidebar");
      await expect(rightSidebar).toBeVisible();

      const initialListWidth = await listSidebar.evaluate(
        (el) => el.getBoundingClientRect().width,
      );
      const initialRightSidebarWidth =
        await rightSidebar.evaluate(
          (el) => el.getBoundingClientRect().width,
        );

      const handle = page.getByRole("button", {
        name: "Resize sidebar",
      });
      await expect(handle).toBeVisible();
      await handle.focus();
      for (let i = 0; i < 8; i += 1) {
        await page.keyboard.press("ArrowRight");
      }

      await expect
        .poll(async () =>
          rightSidebar.evaluate(
            (el) => el.getBoundingClientRect().width,
          ),
        )
        .toBeLessThan(initialRightSidebarWidth - 20);

      const resizedListWidth = await listSidebar.evaluate(
        (el) => el.getBoundingClientRect().width,
      );
      expect(resizedListWidth).toBeGreaterThan(
        initialListWidth + 40,
      );

      const terminalWidth = await page
        .locator(".terminal-area")
        .evaluate((el) => el.getBoundingClientRect().width);
      expect(terminalWidth).toBeGreaterThanOrEqual(
        300,
      );
    },
  );

  test(
    "segmented control visible in terminal header",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      const segControl = page.locator(".seg-control");
      await expect(segControl).toBeVisible();
      await expect(
        segControl.locator(".seg-btn", { hasText: "PR" }),
      ).toBeVisible();
      await expect(
        segControl.locator(".seg-btn", {
          hasText: "Reviews",
        }),
      ).toBeVisible();
    },
  );

  test(
    "clicking PR segment opens sidebar with PR content",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      const prBtn = page.locator(".seg-btn", {
        hasText: "PR",
      });
      await expect(prBtn).toBeVisible();
      await prBtn.click();

      // Sidebar should now be visible
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      // PR button should be active
      await expect(prBtn).toHaveClass(/active/);
    },
  );

  test(
    "clicking active segment closes sidebar",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      const prBtn = page.locator(".seg-btn", {
        hasText: "PR",
      });
      // Open
      await prBtn.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // Click same segment again — should close
      await prBtn.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toHaveCount(0);
      await expect(prBtn).not.toHaveClass(/active/);
    },
  );

  test(
    "clicking Reviews switches tab without closing",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      const prBtn = page.locator(".seg-btn", {
        hasText: "PR",
      });
      const reviewsBtn = page.locator(".seg-btn", {
        hasText: "Reviews",
      });

      // Open PR tab
      await prBtn.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(prBtn).toHaveClass(/active/);

      // Switch to Reviews
      await reviewsBtn.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(reviewsBtn).toHaveClass(/active/);
      await expect(prBtn).not.toHaveClass(/active/);
    },
  );

  test(
    "Cmd+] toggles sidebar open and closed",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      // Start closed
      await expect(
        page.locator(".right-sidebar"),
      ).toHaveCount(0);

      // Open via keyboard
      await page.keyboard.press("Meta+]");
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // Close via keyboard
      await page.keyboard.press("Meta+]");
      await expect(
        page.locator(".right-sidebar"),
      ).toHaveCount(0);
    },
  );
});

// -------------------------------------------------------
// Group 2: Persistence
// -------------------------------------------------------

test.describe("sidebar persistence", () => {
  // Persistence tests reload the page, so we must NOT
  // use addInitScript (it re-runs on reload and would
  // clear the values we want to persist). Instead we
  // clear localStorage via evaluate after first goto.
  test.beforeEach(async ({ page }) => {
    await setupTerminalMocks(page);
  });

  async function clearSidebarStorage(
    page: import("@playwright/test").Page,
  ): Promise<void> {
    await page.evaluate(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
  }

  test(
    "sidebar open state persists across reload",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");
      await clearSidebarStorage(page);

      // Open sidebar
      const prBtn = page.locator(".seg-btn", {
        hasText: "PR",
      });
      await prBtn.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // Verify localStorage written
      const stored = await page.evaluate(() =>
        localStorage.getItem(
          "middleman-workspace-sidebar-open",
        ),
      );
      expect(stored).toBe("true");

      // Reload — sidebar should still be open
      await page.reload();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
    },
  );

  test(
    "sidebar tab persists across reload",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");
      await clearSidebarStorage(page);

      // Open Reviews tab
      const reviewsBtn = page.locator(".seg-btn", {
        hasText: "Reviews",
      });
      await reviewsBtn.click();
      await expect(reviewsBtn).toHaveClass(/active/);

      // Verify localStorage
      const tab = await page.evaluate(() =>
        localStorage.getItem(
          "middleman-workspace-sidebar-tab",
        ),
      );
      expect(tab).toBe("reviews");

      // Reload
      await page.reload();
      const reviewsBtnAfter = page.locator(".seg-btn", {
        hasText: "Reviews",
      });
      await expect(reviewsBtnAfter).toHaveClass(/active/);
    },
  );

  test(
    "sidebar width persists after resize and reload",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");
      await clearSidebarStorage(page);

      // Open sidebar
      await page
        .locator(".seg-btn", { hasText: "PR" })
        .click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      const handle = page.locator(
        ".sidebar-resize-handle",
      );
      const box = await handle.boundingBox();
      expect(box).toBeTruthy();

      if (box) {
        // Drag left to make sidebar wider
        await page.mouse.move(
          box.x + 2,
          box.y + box.height / 2,
        );
        await page.mouse.down();
        await page.mouse.move(
          box.x - 100,
          box.y + box.height / 2,
        );
        await page.mouse.up();
      }

      // Width should have increased from default 360
      const width = await page.evaluate(() =>
        localStorage.getItem(
          "middleman-workspace-sidebar-width",
        ),
      );
      expect(Number(width)).toBeGreaterThan(360);

      const savedWidth = Number(width);

      // Reload and check sidebar opens at saved width
      await page.reload();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      const actualWidth = await page
        .locator(".right-sidebar")
        .evaluate((el) => el.offsetWidth);
      // Allow some tolerance for rounding
      expect(actualWidth).toBeGreaterThanOrEqual(
        savedWidth - 2,
      );
      expect(actualWidth).toBeLessThanOrEqual(
        savedWidth + 2,
      );
    },
  );
});

// -------------------------------------------------------
// Group 3: PR Tab
// -------------------------------------------------------

test.describe("sidebar PR tab", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
    await setupTerminalMocks(page);
  });

  test(
    "PR tab loads PR detail for workspace with linked PR",
    async ({ page }) => {
      await page.goto("/terminal/ws-123");

      // Open PR tab
      await page
        .locator(".seg-btn", { hasText: "PR" })
        .click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // PR detail component should show PR title
      await expect(
        page.locator(
          ".right-sidebar .detail-title",
        ),
      ).toContainText("Add browser regression coverage");
    },
  );

  test(
    "workspace without associated PR hides malformed PR tab",
    async ({ page }) => {
      const noLinkedPR = {
        ...testIssueWorkspace,
        associated_pr_number: null,
      };
      await setupTerminalMocks(page, {
        workspace: noLinkedPR,
      });

      await page.goto("/terminal/ws-issue-7");

      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toHaveCount(0);
    },
  );
});

// -------------------------------------------------------
// Group 3.5: Workspace List Bubble
// -------------------------------------------------------

test.describe("workspace list bubble opens right sidebar", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
  });

  test(
    "clicking PR bubble opens PR tab in the right sidebar",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      // Sidebar should start collapsed.
      await expect(
        page.locator(".right-sidebar"),
      ).toHaveCount(0);

      await page
        .locator(
          ".workspace-list-sidebar .ws-row .item-bubble",
        )
        .click();

      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toHaveClass(/\bactive\b/);
    },
  );

  test(
    "clicking issue bubble opens Issue tab for issue workspace",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: testIssueWorkspace,
      });
      await page.goto("/terminal/ws-issue-7");

      await expect(
        page.locator(".right-sidebar"),
      ).toHaveCount(0);

      await page
        .locator(
          ".workspace-list-sidebar .ws-row .item-bubble",
        )
        .click();

      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "Issue" }),
      ).toHaveClass(/\bactive\b/);
    },
  );

  test(
    "Enter keypress on PR bubble does not navigate row",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      const bubble = page.locator(
        ".workspace-list-sidebar .ws-row .item-bubble",
      );
      await bubble.focus();
      await page.keyboard.press("Enter");

      // Sidebar should open without unintended navigation
      // (the row's Enter handler must not fire when the
      // event originates inside the bubble button).
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(page).toHaveURL(/\/terminal\/ws-123$/);
    },
  );

  test(
    "clicking bubble from /workspaces routes and keeps sidebar populated",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/workspaces");

      // The /workspaces route has no specific workspace selected
      // but still mounts the workspace list sidebar.
      await expect(
        page.locator(".workspace-list-sidebar .ws-row"),
      ).toHaveCount(1);
      await expect(
        page.locator(".terminal-main .state-message"),
      ).toContainText("Select a workspace from the sidebar");

      await page
        .locator(
          ".workspace-list-sidebar .ws-row .item-bubble",
        )
        .click();

      // Navigation lands on the terminal route for the clicked
      // workspace, the sidebar stays populated rather than
      // emptying out, and the right sidebar opens to PR.
      await expect(page).toHaveURL(/\/terminal\/ws-123$/);
      await expect(
        page.locator(".workspace-list-sidebar .ws-row"),
      ).toHaveCount(1);
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toHaveClass(/\bactive\b/);
    },
  );

  test(
    "clicking bubble for a different workspace from /terminal navigates and keeps sidebar populated",
    async ({ page }) => {
      const wsA = { ...testWorkspace, id: "ws-aaa", item_number: 1 };
      const wsB = { ...testWorkspace, id: "ws-bbb", item_number: 2 };

      // First catch-all so unmocked detail routes resolve to a valid
      // workspace shape; specific routes below override.
      await mockApi(page);
      await page.route(
        "**/api/v1/events",
        async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "text/event-stream",
            body: "",
          });
        },
      );
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ workspaces: [wsA, wsB] }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );
      for (const ws of [wsA, wsB]) {
        await page.route(
          `**/api/v1/workspaces/${ws.id}`,
          async (route) => {
            if (route.request().method() === "GET") {
              await route.fulfill({
                status: 200,
                contentType: "application/json",
                body: JSON.stringify(ws),
              });
              return;
            }
            await route.fulfill({ status: 204 });
          },
        );
        await page.route(
          `**/api/v1/workspaces/${ws.id}/runtime`,
          async (route) => {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({
                launch_targets: [],
                sessions: [],
              }),
            });
          },
        );
      }

      await page.goto(`/terminal/${wsA.id}`);
      await expect(
        page.locator(".workspace-list-sidebar .ws-row"),
      ).toHaveCount(2);
      await expect(page).toHaveURL(
        new RegExp(`/terminal/${wsA.id}$`),
      );

      // Click the bubble for the other workspace.
      await page
        .locator(
          ".workspace-list-sidebar .ws-row .item-bubble",
          { hasText: `#${wsB.item_number}` },
        )
        .click();

      // Should route to the other workspace, sidebar stays full,
      // right sidebar opens to PR.
      await expect(page).toHaveURL(
        new RegExp(`/terminal/${wsB.id}$`),
      );
      await expect(
        page.locator(".workspace-list-sidebar .ws-row"),
      ).toHaveCount(2);
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toHaveClass(/\bactive\b/);
    },
  );

  test(
    "clicking bubble does not bubble up to row navigation",
    async ({ page }) => {
      // The row click handler must skip when the event originates
      // inside the bubble. If it didn't, the row would navigate
      // before the bubble could open the right sidebar — leaving
      // the sidebar closed.
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      let routeChanges = 0;
      page.on("framenavigated", () => {
        routeChanges += 1;
      });

      await page
        .locator(
          ".workspace-list-sidebar .ws-row .item-bubble",
        )
        .click();

      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      // Click on the bubble for the currently selected workspace
      // should not have triggered a frame/route navigation.
      expect(routeChanges).toBe(0);
    },
  );

  test(
    "clicking bubble twice toggles the right sidebar",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      const bubble = page.locator(
        ".workspace-list-sidebar .ws-row .item-bubble",
      );

      await bubble.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      await bubble.click();
      await expect(
        page.locator(".right-sidebar"),
      ).toHaveCount(0);
    },
  );

  test(
    "PR bubble x-position stays stable across rows with varied meta",
    async ({ page }) => {
      // Regression: the bubble previously sat inside .ws-row-text and
      // its X position drifted left when the row had no push pills or
      // diff stats. Pinning the bubble to its own right column makes
      // the X position identical across rows regardless of meta.
      const wsBare = {
        ...testWorkspace,
        id: "ws-bare",
        item_number: 1,
        git_head_ref: "fix/x",
      };
      const wsBranchOnly = {
        ...testWorkspace,
        id: "ws-branch-long",
        item_number: 22,
        git_head_ref:
          "feature/very-long-branch-name-that-fills-the-row",
      };
      const wsAhead = {
        ...testWorkspace,
        id: "ws-ahead",
        item_number: 333,
        git_head_ref: "feature/ahead",
        commits_ahead: 7,
        commits_behind: 0,
      };
      const wsAheadBehindDiff = {
        ...testWorkspace,
        id: "ws-busy",
        item_number: 4444,
        git_head_ref: "feature/busy",
        commits_ahead: 12,
        commits_behind: 5,
        mr_additions: 1500,
        mr_deletions: 2400,
      };
      const list = [wsBare, wsBranchOnly, wsAhead, wsAheadBehindDiff];

      await mockApi(page);
      await page.route(
        "**/api/v1/events",
        async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "text/event-stream",
            body: "",
          });
        },
      );
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ workspaces: list }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );
      for (const ws of list) {
        await page.route(
          `**/api/v1/workspaces/${ws.id}`,
          async (route) => {
            if (route.request().method() === "GET") {
              await route.fulfill({
                status: 200,
                contentType: "application/json",
                body: JSON.stringify(ws),
              });
              return;
            }
            await route.fulfill({ status: 204 });
          },
        );
        await page.route(
          `**/api/v1/workspaces/${ws.id}/runtime`,
          async (route) => {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({
                launch_targets: [],
                sessions: [],
              }),
            });
          },
        );
      }

      await page.goto("/workspaces");
      await expect(
        page.locator(".workspace-list-sidebar .ws-row"),
      ).toHaveCount(list.length);
      await expect(
        page.locator(".workspace-list-sidebar .ws-row", {
          hasText: "feature/busy",
        }).locator(".diff-stats"),
      ).toHaveCount(1);

      const bubbles = page.locator(
        ".workspace-list-sidebar .ws-row .item-bubble",
      );
      const boxes: Array<{ right: number }> = [];
      for (let i = 0; i < list.length; i += 1) {
        const box = await bubbles.nth(i).boundingBox();
        expect(box).not.toBeNull();
        if (box != null) {
          boxes.push({ right: box.x + box.width });
        }
      }

      const rights = boxes.map((b) => b.right);
      const maxRight = Math.max(...rights);
      const minRight = Math.min(...rights);
      // All bubbles should align to the same right column. Allow a
      // sub-pixel tolerance for browser rounding.
      expect(maxRight - minRight).toBeLessThanOrEqual(1);
    },
  );

  test(
    "filters workspace rows by repo, title, and item number",
    async ({ page }) => {
      const wsTitle = {
        ...testWorkspace,
        id: "ws-title",
        repo_owner: "kenn-io",
        repo_name: "kataflow",
        repo: workspaceRepoRef("kenn-io", "kataflow"),
        item_number: 9,
        mr_title: "Migrate native HTTP surface to Huma v2",
      };
      const wsRepo = {
        ...testWorkspace,
        id: "ws-repo",
        repo_owner: "kenn-io",
        repo_name: "kenn-platform",
        repo: workspaceRepoRef("kenn-io", "kenn-platform"),
        item_number: 2,
        mr_title: "Hosted code fetch and caching strategy",
      };
      const wsNumber = {
        ...testIssueWorkspace,
        id: "ws-number",
        repo_owner: "kenn-io",
        repo_name: "middleman",
        repo: workspaceRepoRef("kenn-io", "middleman"),
        item_number: 224,
        mr_title: "Add notification inbox triage",
      };
      const list = [wsTitle, wsRepo, wsNumber];

      await mockApi(page);
      await page.route(
        "**/api/v1/events",
        async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "text/event-stream",
            body: "",
          });
        },
      );
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ workspaces: list }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );

      await page.goto("/workspaces");

      const rows = page.locator(".workspace-list-sidebar .ws-row");
      const filter = page.getByLabel("Filter workspaces");
      await expect(rows).toHaveCount(3);

      await filter.fill("huma");
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText(
        "Migrate native HTTP surface to Huma v2",
      );

      await filter.fill("kenn-platform");
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText(
        "Hosted code fetch and caching strategy",
      );

      await filter.fill("#224");
      await expect(rows).toHaveCount(1);
      await expect(rows.first()).toContainText(
        "Add notification inbox triage",
      );

      await filter.fill("not-present");
      await expect(rows).toHaveCount(0);
      await expect(
        page.locator(".workspace-list-sidebar"),
      ).toContainText("No workspaces match.");
    },
  );
});

// -------------------------------------------------------
// Group 3.5: Delayed-response navigation (no flash, no
// stale-action targets)
// -------------------------------------------------------

test.describe("delayed-response navigation", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
  });

  test(
    "switching workspaces holds previous data and blocks actions until new load resolves",
    async ({ page }) => {
      // Workspace A loads instantly. Workspace B's GET is held back
      // so the UI is forced into the transition window where the
      // previous workspace's data is still on screen and any
      // mutating actions must target the new id but be blocked.
      const wsA = {
        ...testWorkspace,
        id: "ws-aaa",
        item_number: 1,
        mr_title: "A title",
      };
      const wsB = {
        ...testWorkspace,
        id: "ws-bbb",
        item_number: 2,
        mr_title: "B title",
      };

      await mockApi(page);
      await page.route(
        "**/api/v1/events",
        async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "text/event-stream",
            body: "",
          });
        },
      );
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ workspaces: [wsA, wsB] }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );

      // wsA — instant.
      await page.route(
        `**/api/v1/workspaces/${wsA.id}`,
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify(wsA),
            });
            return;
          }
          await route.fulfill({ status: 204 });
        },
      );
      await page.route(
        `**/api/v1/workspaces/${wsA.id}/runtime`,
        async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              launch_targets: [],
              sessions: [],
            }),
          });
        },
      );

      // wsB — delayed. Resolved manually below so the test can
      // observe the in-place transition.
      let releaseB: () => void = () => {};
      const bDelay = new Promise<void>((resolve) => {
        releaseB = resolve;
      });
      await page.route(
        `**/api/v1/workspaces/${wsB.id}`,
        async (route) => {
          if (route.request().method() === "GET") {
            await bDelay;
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify(wsB),
            });
            return;
          }
          await route.fulfill({ status: 204 });
        },
      );
      await page.route(
        `**/api/v1/workspaces/${wsB.id}/runtime`,
        async (route) => {
          await bDelay;
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              launch_targets: [],
              sessions: [],
            }),
          });
        },
      );

      await page.goto(`/terminal/${wsA.id}`);

      // Confirm wsA is visible (its title sits in the header bar).
      await expect(
        page.locator(".terminal-main .header-name"),
      ).toContainText(wsA.mr_title);

      // Click row B from the sidebar.
      await page
        .locator(".workspace-list-sidebar .ws-row", {
          hasText: `#${wsB.item_number}`,
        })
        .click();

      // URL has switched to B, but B's data hasn't arrived yet —
      // the header bar should still show A's data (no flash to
      // a loading/empty state).
      await expect(page).toHaveURL(
        new RegExp(`/terminal/${wsB.id}$`),
      );
      await expect(
        page.locator(".terminal-main .header-name"),
      ).toContainText(wsA.mr_title);

      // While the URL points at B but the screen still shows A,
      // the Delete button must be disabled so a click can't
      // delete B while the user looks at A.
      await expect(
        page.locator(".terminal-main .header-btn.danger"),
      ).toBeDisabled();

      // Release B's response — the UI should update in place to
      // wsB without ever rendering a "Loading..." flash.
      releaseB();
      await expect(
        page.locator(".terminal-main .header-name"),
      ).toContainText(wsB.mr_title);
      await expect(
        page.locator(".terminal-main .header-btn.danger"),
      ).toBeEnabled();
    },
  );

  test(
    "terminal panel closes when navigating to a different workspace",
    async ({ page }) => {
      // Regression: keeping the bottom terminal open across a workspace
      // change kept the previous workspace's shell TerminalPane
      // mounted with its WebSocket pointing at workspace A. The
      // user could see workspace B but type into A's shell.
      const wsA = {
        ...testWorkspace,
        id: "ws-aaa",
        item_number: 1,
        mr_title: "A title",
      };
      const wsB = {
        ...testWorkspace,
        id: "ws-bbb",
        item_number: 2,
        mr_title: "B title",
      };
      const shellSession = (wsId: string) => ({
        key: `${wsId}:plain_shell`,
        workspace_id: wsId,
        target_key: "plain_shell",
        label: "Plain shell",
        kind: "plain_shell",
        status: "running" as const,
        created_at: "2026-04-10T12:00:00Z",
      });

      await mockApi(page);
      await page.route("**/api/v1/events", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: "",
        });
      });
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ workspaces: [wsA, wsB] }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );
      for (const ws of [wsA, wsB]) {
        await page.route(
          `**/api/v1/workspaces/${ws.id}`,
          async (route) => {
            if (route.request().method() === "GET") {
              await route.fulfill({
                status: 200,
                contentType: "application/json",
                body: JSON.stringify(ws),
              });
              return;
            }
            await route.fulfill({ status: 204 });
          },
        );
        await page.route(
          `**/api/v1/workspaces/${ws.id}/runtime`,
          async (route) => {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({
                ...workspaceRuntime,
                sessions: [shellSession(ws.id)],
              }),
            });
          },
        );
      }

      await page.goto(`/terminal/${wsA.id}`);
      // Open the terminal panel for A.
      await page
        .getByRole("button", { name: "Open terminal panel" })
        .click();
      await expect(
        page.locator(".terminal-panel.open .terminal-container"),
      ).toBeVisible();

      // Navigate to B by clicking its row.
      await page
        .locator(".workspace-list-sidebar .ws-row", {
          hasText: `#${wsB.item_number}`,
        })
        .click();
      await expect(page).toHaveURL(
        new RegExp(`/terminal/${wsB.id}$`),
      );

      // The panel must close so the previous workspace's shell
      // pane unmounts and its WebSocket tears down. Otherwise
      // keystrokes from B's session would be routed to A's shell.
      await expect(
        page.locator(".terminal-panel.open .terminal-container"),
      ).toHaveCount(0);
    },
  );

  test(
    "previous workspace's runtime sessions are not visible while B's runtime is loading",
    async ({ page }) => {
      // Regression: after the workspace fetch resolved, runtime
      // still held the previous workspace's payload until its own
      // fetch completed. The workspace stage briefly rendered A's
      // session tabs (and launch targets) inside B's view, with
      // actionsBlocked already false.
      const wsA = {
        ...testWorkspace,
        id: "ws-aaa",
        item_number: 1,
        mr_title: "A title",
      };
      const wsB = {
        ...testWorkspace,
        id: "ws-bbb",
        item_number: 2,
        mr_title: "B title",
      };
      const sessionA = {
        key: "ws-aaa:helper",
        workspace_id: "ws-aaa",
        target_key: "helper",
        label: "Helper A",
        kind: "agent" as const,
        status: "running" as const,
        created_at: "2026-04-10T12:00:00Z",
      };

      await mockApi(page);
      await page.route("**/api/v1/events", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: "",
        });
      });
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ workspaces: [wsA, wsB] }),
            });
            return;
          }
          await route.fulfill({ status: 200 });
        },
      );
      // wsA: instant.
      await page.route(
        `**/api/v1/workspaces/${wsA.id}`,
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify(wsA),
            });
            return;
          }
          await route.fulfill({ status: 204 });
        },
      );
      await page.route(
        `**/api/v1/workspaces/${wsA.id}/runtime`,
        async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              ...workspaceRuntime,
              sessions: [sessionA],
            }),
          });
        },
      );
      // wsB: workspace GET is fast, runtime GET is held.
      await page.route(
        `**/api/v1/workspaces/${wsB.id}`,
        async (route) => {
          if (route.request().method() === "GET") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify(wsB),
            });
            return;
          }
          await route.fulfill({ status: 204 });
        },
      );
      let releaseBRuntime: () => void = () => {};
      const bRuntimeDelay = new Promise<void>((resolve) => {
        releaseBRuntime = resolve;
      });
      await page.route(
        `**/api/v1/workspaces/${wsB.id}/runtime`,
        async (route) => {
          await bRuntimeDelay;
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify(workspaceRuntime),
          });
        },
      );

      await page.goto(`/terminal/${wsA.id}`);
      // A's session tab should be visible.
      await expect(
        page.locator(".workspace-stage .group-tab-panel"),
      ).not.toHaveCount(0);

      await page
        .locator(".workspace-list-sidebar .ws-row", {
          hasText: `#${wsB.item_number}`,
        })
        .click();

      // The header should have moved to B as soon as wsB's
      // workspace GET resolves. But because B's runtime fetch is
      // still in flight, the workspace stage must show the
      // "Loading workspace runtime..." state, not A's session
      // panes.
      await expect(
        page.locator(".terminal-main .header-name"),
      ).toContainText(wsB.mr_title);
      await expect(
        page.locator(".workspace-stage .state-message"),
      ).toContainText("Loading workspace runtime");

      releaseBRuntime();

      // Once B's runtime resolves, the loading state goes away.
      await expect(
        page.locator(".workspace-stage .state-message"),
      ).toHaveCount(0);
    },
  );
});

// -------------------------------------------------------
// Group 4: Issue Workspace Sidebar
// -------------------------------------------------------

test.describe("issue workspace sidebar", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
  });

  test(
    "issue workspaces show an Issue segment instead of PR and Reviews when no PR is linked",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: testIssueWorkspace,
      });
      await page.goto("/terminal/ws-issue-7");

      await expect(
        page.locator(".seg-btn", { hasText: "Issue" }),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toHaveCount(0);
      await expect(
        page.locator(".seg-btn", { hasText: "Reviews" }),
      ).toHaveCount(0);
    },
  );

  test(
    "issue segment opens issue detail for issue-backed workspaces",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: testIssueWorkspace,
      });
      await page.goto("/terminal/ws-issue-7");

      await page
        .locator(".seg-btn", { hasText: "Issue" })
        .click();

      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();
      await expect(
        page.locator(".right-sidebar .detail-title"),
      ).toContainText("Theme toggle does not stick");
    },
  );

  test(
    "issue segment includes workspace platform_host in detail requests",
    async ({ page }) => {
      const mirroredWorkspace = {
        ...testIssueWorkspace,
        platform_host: "example.com",
        repo: workspaceRepoRef("acme", "widgets", "example.com"),
      };
      const seenHosts: string[] = [];
      const mirroredIssueDetail = {
        issue: {
          ID: 2,
          RepoID: 2,
          GitHubID: 202,
          Number: 7,
          URL: "https://example.com/acme/widgets/issues/7",
          Title: "Mirror host issue",
          Author: "marius",
          State: "open",
          Body: "",
          CommentCount: 1,
          LabelsJSON: "[]",
          CreatedAt: "2026-03-28T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          ClosedAt: null,
          Starred: false,
        },
        events: [],
        platform_host: "example.com",
        repo_owner: "acme",
        repo_name: "widgets",
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
      };

      await setupTerminalMocks(page, {
        workspace: mirroredWorkspace,
      });

      await page.route(
        "**/api/v1/host/example.com/issues/github/acme/widgets/7",
        async (route) => {
          seenHosts.push("example.com");
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify(mirroredIssueDetail),
          });
        },
      );
      await page.route(
        "**/api/v1/host/example.com/issues/github/acme/widgets/7/sync",
        async (route) => {
          seenHosts.push("example.com");
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify(mirroredIssueDetail),
          });
        },
      );

      await page.goto("/terminal/ws-issue-7");
      await page
        .locator(".seg-btn", { hasText: "Issue" })
        .click();

      await expect(
        page.locator(".right-sidebar .detail-title"),
      ).toContainText("Mirror host issue");
      await expect.poll(() => seenHosts).toEqual([
        "example.com",
      ]);
    },
  );

  test(
    "issue workspace with associated PR shows Issue and PR tabs but no Reviews",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: testIssueWorkspaceWithAssociatedPR,
      });
      await page.goto("/terminal/ws-issue-7");

      await expect(
        page.locator(".seg-btn", { hasText: "Issue" }),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toBeVisible();
      await expect(
        page.locator(".seg-btn", { hasText: "Reviews" }),
      ).toHaveCount(0);
    },
  );

  test(
    "issue workspace PR tab hides workspace create and open actions",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        workspace: testIssueWorkspaceWithAssociatedPR,
      });
      await page.goto("/terminal/ws-issue-7");

      await page
        .locator(".seg-btn", { hasText: "PR" })
        .click();

      await expect(
        page.locator(".right-sidebar .detail-title"),
      ).toContainText("Add browser regression coverage");
      await expect(
        page.locator(".right-sidebar .btn--workspace"),
      ).toHaveCount(0);
    },
  );

  test(
    "issue workspace gains PR tab after workspace_status refetch and keeps manual PR selection",
    async ({ page }) => {
      let currentWorkspace: WorkspaceFixture = {
        ...testIssueWorkspace,
        associated_pr_number: null,
      };

      await page.addInitScript(() => {
        localStorage.setItem(
          "middleman-workspace-sidebar-open",
          "true",
        );
        localStorage.setItem(
          "middleman-workspace-sidebar-tab",
          "pr",
        );

        const instances: Array<{
          listeners: Map<string, Set<(event: MessageEvent) => void>>;
        }> = [];

        class FakeEventSource {
          listeners = new Map<string, Set<(event: MessageEvent) => void>>();

          constructor() {
            instances.push(this);
          }

          addEventListener(type: string, callback: (event: MessageEvent) => void): void {
            const bucket = this.listeners.get(type) ?? new Set();
            bucket.add(callback);
            this.listeners.set(type, bucket);
          }

          close(): void {}
        }

        (window as typeof window & { EventSource: typeof EventSource }).EventSource = FakeEventSource as unknown as typeof EventSource;
        (window as typeof window & {
          __emitWorkspaceStatus: (payload: { id: string }) => void;
        }).__emitWorkspaceStatus = (payload) => {
          const event = new MessageEvent("workspace_status", {
            data: JSON.stringify(payload),
          });
          for (const instance of instances) {
            const listeners = instance.listeners.get("workspace_status") ?? new Set();
            for (const listener of listeners) {
              listener(event);
            }
          }
        };
      });

      await mockApi(page);
      await page.route(
        "**/api/v1/workspaces",
        async (route) => {
          if (route.request().method() !== "GET") {
            await route.fulfill({ status: 200 });
            return;
          }
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ workspaces: [currentWorkspace] }),
          });
        },
      );
      await page.route(
        "**/api/v1/workspaces/ws-issue-7",
        async (route) => {
          if (route.request().method() !== "GET") {
            await route.fulfill({ status: 204 });
            return;
          }
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify(currentWorkspace),
          });
        },
      );

      await page.goto("/terminal/ws-issue-7");

      await expect(
        page.locator(".seg-btn", { hasText: "PR" }),
      ).toHaveCount(0);

      currentWorkspace = testIssueWorkspaceWithAssociatedPR;
      await page.evaluate(() => {
        (window as typeof window & {
          __emitWorkspaceStatus: (payload: { id: string }) => void;
        }).__emitWorkspaceStatus({ id: "ws-issue-7" });
      });

      const issueButton = page.locator(
        ".seg-btn",
        { hasText: "Issue" },
      );
      const prButton = page.locator(
        ".seg-btn",
        { hasText: "PR" },
      );
      await expect(prButton).toBeVisible();
      await expect(issueButton).toBeVisible();

      await prButton.click();
      await expect(prButton).toHaveClass(/active/);
      await expect(
        page.locator(".right-sidebar .detail-title"),
      ).toContainText("Add browser regression coverage");

      await page.evaluate(() => {
        (window as typeof window & {
          __emitWorkspaceStatus: (payload: { id: string }) => void;
        }).__emitWorkspaceStatus({ id: "ws-issue-7" });
      });
      await expect(prButton).toHaveClass(/active/);
    },
  );
});

// -------------------------------------------------------
// Group 5: Reviews Tab
// -------------------------------------------------------

test.describe("sidebar Reviews tab", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem(
        "middleman-workspace-sidebar-tab",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-open",
      );
      localStorage.removeItem(
        "middleman-workspace-sidebar-width",
      );
    });
  });

  test(
    "Reviews tab preserves a daemon version that already starts with v",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        roborevStatus: {
          ...roborevStatus,
          version: "v0.52.0",
        },
      });
      await page.goto("/terminal/ws-123");

      await page
        .locator(".seg-btn", { hasText: "Reviews" })
        .click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      await expect(
        page.locator(
          '.right-sidebar .daemon-status [title="Daemon version"]',
        ),
      ).toHaveText("v0.52.0");
    },
  );

  test(
    "Reviews tab shows job list when roborev repo matches",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      // Open Reviews tab
      await page
        .locator(".seg-btn", { hasText: "Reviews" })
        .click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // Job list should render the mock job
      await expect(
        page.locator(
          ".right-sidebar .job-row",
        ),
      ).toBeVisible();
      await expect(
        page.locator(".right-sidebar .job-row"),
      ).toContainText("Add auth middleware");
    },
  );

  test(
    "Reviews tab shows empty state when no repo matches",
    async ({ page }) => {
      await setupTerminalMocks(page, {
        roborevRepos: { repos: [], total_count: 0 },
      });
      await page.goto("/terminal/ws-123");

      // Open Reviews tab
      await page
        .locator(".seg-btn", { hasText: "Reviews" })
        .click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // Should show empty/no-reviews message
      await expect(
        page.locator(".right-sidebar .empty-state"),
      ).toContainText("No reviews");
    },
  );

  test(
    "branch picker shows and clears branch filter",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      // Open Reviews tab
      await page
        .locator(".seg-btn", { hasText: "Reviews" })
        .click();
      await expect(
        page.locator(".right-sidebar"),
      ).toBeVisible();

      // Branch filter should show workspace branch
      const picker = page.locator(
        '.right-sidebar .picker-button[title="Filter by repository"]',
      );
      await expect(picker).toContainText("feature/auth");

      // Selecting All Repos clears the branch filter
      await picker.click();
      await page
        .locator(".right-sidebar .dropdown-item", {
          hasText: "All Repos",
        })
        .click();
      await expect(picker).toContainText("All Repos");
    },
  );

  test(
    "selecting a job does not navigate to /reviews",
    async ({ page }) => {
      await setupTerminalMocks(page);
      await page.goto("/terminal/ws-123");

      // Open Reviews tab
      await page
        .locator(".seg-btn", { hasText: "Reviews" })
        .click();
      await expect(
        page.locator(".right-sidebar .job-row"),
      ).toBeVisible();

      // Click the job row
      await page
        .locator(".right-sidebar .job-row")
        .first()
        .click();

      // URL should stay on /terminal, not navigate
      await expect(page).toHaveURL(/\/terminal\/ws-123/);
      // Job row should get selected state
      await expect(
        page
          .locator(".right-sidebar .job-row")
          .first(),
      ).toHaveClass(/selected/);
    },
  );
});
