import { execSync } from "node:child_process";
import { readFileSync } from "node:fs";
import type { Locator, Page } from "@playwright/test";
import { expect } from "@playwright/test";

function readEnvFile(): Record<string, string> {
  const envPath = process.env["ROBOREV_ENV_FILE"];
  if (!envPath) {
    throw new Error("ROBOREV_ENV_FILE not set — run via scripts/run-roborev-e2e.sh");
  }
  const content = readFileSync(envPath, "utf-8");
  const env: Record<string, string> = {};
  for (const line of content.split("\n")) {
    const eq = line.indexOf("=");
    if (eq > 0) {
      env[line.slice(0, eq)!] = line.slice(eq + 1);
    }
  }
  return env;
}

function composeExec(cmd: string): void {
  const env = readEnvFile();
  execSync(`docker compose ${cmd}`, {
    cwd: env["COMPOSE_DIR"],
    env: { ...process.env, ...env },
    stdio: "pipe",
    timeout: 30_000,
  });
}

export function stopDaemon(): void {
  composeExec("stop roborev");
}

export function startDaemon(): void {
  composeExec("start roborev");
  waitForDaemonHealthy();
}

function waitForDaemonHealthy(): void {
  const env = readEnvFile();
  const port = env["ROBOREV_PORT"] ?? "17373";
  const daemonURL = `http://127.0.0.1:${port}/api/status`;
  const baseURL = process.env["PLAYWRIGHT_E2E_BASE_URL"];
  if (!baseURL) {
    throw new Error("PLAYWRIGHT_E2E_BASE_URL not set");
  }
  const proxyURL = `${baseURL}/api/roborev/api/status`;
  for (let i = 0; i < 30; i++) {
    try {
      execSync(`curl -sf ${daemonURL}`, {
        stdio: "pipe",
        timeout: 2_000,
      });
      execSync(`curl -sf ${proxyURL}`, {
        stdio: "pipe",
        timeout: 2_000,
      });
      return;
    } catch {
      execSync("sleep 1", { stdio: "pipe" });
    }
  }
  throw new Error("Daemon not healthy through Kenn Forge's proxy after 30 attempts");
}

// Probe the daemon backing the e2e server and assert it matches the
// shape of the script-seeded test daemon. If the e2e server proxies
// to an unmanaged daemon (e.g. someone passes -roborev pointing at a
// real local daemon, or the script-managed daemon's seed has drifted
// from this guard), individual tests would fail with mysterious data
// mismatches. Refuse upfront with a clear pointer at the runner
// script.
//
// Two layers of checks, both required:
//
//  1. /api/status must return a valid daemon-status response shape
//     (every counter present and numeric) and the total job count
//     must fall in a tight band around the seeded size of 73. The
//     shape check is what catches an entirely wrong endpoint; the
//     count check is what catches a real local daemon with a much
//     larger history.
//
//  2. /api/jobs?id=73 must return the seeded mutation fixture with
//     agent="codex" and branch="main". These two fields are
//     immutable across the test run (the rerun test re-enqueues job
//     73 in place but does not touch agent/branch), so they make a
//     load-bearing fingerprint for the seeded daemon. Without this
//     check, a fresh unmanaged daemon with <90 random jobs would
//     pass the count band silently.
const SEEDED_STATUS_KEYS = [
  "queued_jobs",
  "running_jobs",
  "completed_jobs",
  "failed_jobs",
  "canceled_jobs",
  "applied_jobs",
  "rebased_jobs",
] as const;

export async function assertSeededRoborevDaemon(): Promise<void> {
  const baseURL = process.env["PLAYWRIGHT_E2E_BASE_URL"];
  if (!baseURL) {
    throw new Error(
      "PLAYWRIGHT_E2E_BASE_URL is not set. Run roborev e2e tests " +
        "via scripts/run-roborev-e2e.sh, not playwright directly.",
    );
  }

  const statusBody = await fetchDaemonJSON(`${baseURL}/api/roborev/api/status`);

  const counters: Record<string, number> = {};
  const missing: string[] = [];
  for (const key of SEEDED_STATUS_KEYS) {
    const v = statusBody[key];
    if (typeof v !== "number" || !Number.isFinite(v)) {
      missing.push(`${key}=${JSON.stringify(v)}`);
      continue;
    }
    counters[key] = v;
  }
  if (missing.length > 0) {
    throw new Error(
      `roborev daemon at ${baseURL}/api/roborev/api/status ` +
        "returned a /api/status response with missing or " +
        `non-numeric counters: ${missing.join(", ")}. ` +
        "This is not the script-seeded test daemon shape. " +
        wrongDaemonHint(),
    );
  }

  const total = SEEDED_STATUS_KEYS.reduce((sum, k) => sum + (counters[k] ?? 0), 0);
  // The seed creates exactly 79 jobs (bulk IDs 1-69 + mutation
  // fixtures 70-76 + panel fixtures 77-79). The rerun test
  // re-enqueues job 73 in place (no new ID), so total stays at
  // 79 throughout. Allow a little
  // headroom but reject anything outside the tight seeded band so
  // unmanaged daemons cannot slip through.
  const minSeededJobs = 70;
  const maxSeededJobs = 90;
  if (total < minSeededJobs || total > maxSeededJobs) {
    throw new Error(
      `roborev daemon at ${baseURL}/api/roborev/api/status ` +
        `reports total_jobs=${total}, outside the seeded range ` +
        `[${minSeededJobs},${maxSeededJobs}]. ` +
        wrongDaemonHint(),
    );
  }

  // Probe job 73 by ID and assert the seeded mutation-fixture
  // fingerprint. agent and branch never change during the run, so
  // these are load-bearing identity checks that distinguish the
  // seeded DB from any unmanaged daemon — even one with a small,
  // unrelated job set that slipped past the count band.
  const jobsURL = `${baseURL}/api/roborev/api/jobs?id=73`;
  const jobsBody = await fetchDaemonJSON(jobsURL);
  const jobs = jobsBody["jobs"];
  if (!Array.isArray(jobs) || jobs.length !== 1) {
    throw new Error(
      `roborev daemon at ${jobsURL} did not return exactly one job ` +
        `for id=73 (got ${JSON.stringify(jobs)}). The seed always ` +
        "includes mutation fixture 73. " +
        wrongDaemonHint(),
    );
  }
  const job = jobs[0] as Record<string, unknown>;
  const agent = job["agent"];
  const branch = job["branch"];
  if (agent !== "codex" || branch !== "main") {
    throw new Error(
      `roborev daemon at ${jobsURL} returned job 73 with ` +
        `agent=${JSON.stringify(agent)}, ` +
        `branch=${JSON.stringify(branch)}, ` +
        'but the seed pins these to agent="codex" branch="main". ' +
        wrongDaemonHint(),
    );
  }
}

async function fetchDaemonJSON(url: string): Promise<Record<string, unknown>> {
  let body: Record<string, unknown>;
  try {
    const res = await fetch(url, {
      signal: AbortSignal.timeout(3_000),
    });
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }
    body = (await res.json()) as Record<string, unknown>;
  } catch (err) {
    throw new Error(`roborev daemon at ${url} is not reachable (${String(err)}). ` + wrongDaemonHint(), {
      cause: err,
    });
  }
  return body;
}

function wrongDaemonHint(): string {
  return (
    "The cmd/e2e-server -roborev flag defaults to http://127.0.0.1:1 " +
    "(unbindable) so direct playwright runs fail closed instead of " +
    "silently hitting a real local daemon. If you see this guard " +
    "fire, either you ran playwright directly (use " +
    "scripts/run-roborev-e2e.sh instead) or the e2e server is " +
    "pointing at an unmanaged roborev daemon via an explicit " +
    "ROBOREV_ENDPOINT/-roborev override."
  );
}

const defaultWorkspace = {
  id: "roborev-e2e",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "test-repo-alpha",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "test-repo-alpha",
    repo_path: "acme/test-repo-alpha",
  },
  item_type: "pull_request",
  item_number: 1,
  source_item_visible: true,
  git_head_ref: "main",
  worktree_path: "/home/dev/test-repo-alpha",
  status: "ready",
  enrichment_status: "fresh",
  created_at: "2026-04-10T12:00:00Z",
  tmux_session: "",
  tmux_working: false,
  tmux_activity_source: "unknown",
  tmux_last_output_at: null,
};

// The workspace shell is a fixture; all review requests still pass through
// Forge's real proxy to the script-managed, seeded RoboRev daemon.
export async function setupRoborevWorkspace(
  page: Page,
  options: { repoName?: string; branch?: string; worktreePath?: string } = {},
): Promise<void> {
  const repoName = options.repoName ?? defaultWorkspace.repo_name;
  const workspace = {
    ...defaultWorkspace,
    repo_name: repoName,
    worktree_path: options.worktreePath ?? defaultWorkspace.worktree_path,
    git_head_ref: options.branch ?? defaultWorkspace.git_head_ref,
    repo: { ...defaultWorkspace.repo, name: repoName, repo_path: `acme/${repoName}` },
  };
  await page.route("**/api/v1/snapshot**", (route) => route.fulfill({ json: { hosts: [], workspaces: [workspace] } }));
  await page.route("**/api/v1/workspaces/roborev-e2e**", (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = path.endsWith("/runtime")
      ? { launch_targets: [], sessions: [] }
      : path.endsWith("/files") || path.endsWith("/diff")
        ? { files: [], stale: false, whitespace_only_count: 0 }
        : path.endsWith("/commits")
          ? { commits: [] }
          : workspace;
    return route.fulfill({ json });
  });
}

export async function openWorkspaceReviews(page: Page, baseURL = ""): Promise<void> {
  await page.goto(`${baseURL}/terminal/roborev-e2e`);
  const reviews = page.locator(".panel-toggle-btn", { hasText: "Reviews" });
  await expect(reviews).toBeVisible();
  if (!(await reviews.evaluate((node) => node.classList.contains("active")))) await reviews.click();
  await expect(page.locator(".sidebar-reviews")).toBeVisible();
}

export async function waitForReviewsReady(page: Page, baseURL = ""): Promise<void> {
  await openWorkspaceReviews(page, baseURL);
  await expect(page.locator(".picker-button")).toBeVisible({ timeout: 15_000 });
  await page.locator(".picker-button").click();
  const unscopedJobs = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      response.ok() &&
      url.pathname.endsWith("/api/roborev/api/jobs") &&
      url.searchParams.get("limit") === "50" &&
      !url.searchParams.has("repo") &&
      !url.searchParams.has("branch")
    );
  });
  await page.getByRole("button", { name: "All Repos", exact: true }).click();
  await unscopedJobs;
  await expect(page.locator(".picker-button")).toContainText("All Repos");
  await expect(page.locator(".sidebar-reviews .loading-bar")).toHaveCount(0);
  await expect(page.locator(".job-table")).toBeVisible();
}

export async function waitForJobRows(page: Page, min: number): Promise<void> {
  const rows = page.locator(".job-row");
  await expect(async () => {
    const count = await rows.count();
    expect(count).toBeGreaterThanOrEqual(min);
  }).toPass({ timeout: 10_000 });
}

export async function openDrawer(page: Page, jobId: number, baseURL = ""): Promise<void> {
  await waitForReviewsReady(page, baseURL);
  await page
    .locator(".job-row")
    .filter({ has: page.locator(".col-id .mono", { hasText: new RegExp(`^${jobId}$`) }) })
    .click();
  await expect(page.getByRole("region", { name: "Review details" })).toBeVisible({
    timeout: 10_000,
  });
}

export function reviewAction(page: Page, name: string): Locator {
  return page.getByRole("group", { name: "Review actions" }).getByRole("button", { name });
}
