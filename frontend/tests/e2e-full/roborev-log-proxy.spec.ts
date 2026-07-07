import { once } from "node:events";
import { createServer, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { test, expect } from "@playwright/test";

import { startIsolatedE2EServerWithOptions, type IsolatedE2EServer } from "./support/e2eServer";

const job = {
  id: 501,
  repo_id: 1,
  repo_name: "fake-repo",
  repo_path: "/tmp/fake-repo",
  git_ref: "feedface",
  branch: "main",
  agent: "codex",
  model: "gpt-test",
  status: "done",
  job_type: "review",
  enqueued_at: "2026-04-11T11:00:00Z",
  started_at: "2026-04-11T11:01:00Z",
  finished_at: "2026-04-11T11:02:00Z",
  agentic: false,
  prompt_prebuilt: false,
  retry_count: 0,
  closed: false,
  verdict: "P",
  commit_subject: "render log output",
};

function writeJSON(res: ServerResponse, body: unknown, status = 200): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

async function startFakeRoborevDaemon(): Promise<{
  url: string;
  requests: string[];
  close: () => Promise<void>;
}> {
  const requests: string[] = [];
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://127.0.0.1");
    requests.push(`${url.pathname}${url.search}`);

    switch (url.pathname) {
      case "/api/status":
        writeJSON(res, {
          version: "fake-log-daemon",
          queued_jobs: 0,
          running_jobs: 0,
          completed_jobs: 1,
          failed_jobs: 0,
          canceled_jobs: 0,
          active_workers: 0,
          max_workers: 0,
        });
        return;
      case "/api/jobs":
        writeJSON(res, {
          jobs: [job],
          has_more: false,
          stats: { done: 1, closed: 0, open: 1 },
        });
        return;
      case "/api/review":
        writeJSON(res, {
          id: 601,
          job_id: job.id,
          agent: job.agent,
          prompt: "Review commit",
          output: "No issues found.",
          created_at: "2026-04-11T11:02:10Z",
          closed: false,
          verdict_bool: 1,
          job,
        });
        return;
      case "/api/comments":
        writeJSON(res, { responses: [] });
        return;
      case "/api/repos":
        writeJSON(res, {
          repos: [
            {
              id: 1,
              root_path: job.repo_path,
              name: job.repo_name,
              total_count: 1,
            },
          ],
          total_count: 1,
        });
        return;
      case "/api/job/output":
        writeJSON(res, {
          job_id: job.id,
          status: job.status,
          has_more: false,
          lines: [
            {
              ts: "2026-04-11T11:01:30Z",
              text: "proxy rendered log line",
              line_type: "text",
            },
          ],
        });
        return;
      case "/api/stream/events":
        res.writeHead(204);
        res.end();
        return;
      default:
        writeJSON(res, { error: `unexpected ${url.pathname}` }, 404);
    }
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const { port } = server.address() as AddressInfo;

  return {
    url: `http://127.0.0.1:${port}`,
    requests,
    close: async () => {
      await new Promise<void>((resolve, reject) => {
        server.close((err) => {
          if (err) reject(err);
          else resolve();
        });
      });
    },
  };
}

async function startMiddlemanWithRoborev(endpoint: string): Promise<IsolatedE2EServer> {
  const previous = process.env.ROBOREV_ENDPOINT;
  process.env.ROBOREV_ENDPOINT = endpoint;
  try {
    return await startIsolatedE2EServerWithOptions({
      visibleImportedModes: true,
      freshProcess: true,
    });
  } finally {
    if (previous === undefined) {
      delete process.env.ROBOREV_ENDPOINT;
    } else {
      process.env.ROBOREV_ENDPOINT = previous;
    }
  }
}

test("completed job Log tab renders output through the middleman roborev proxy", async ({ page }) => {
  const daemon = await startFakeRoborevDaemon();
  let middleman: IsolatedE2EServer | undefined;
  try {
    middleman = await startMiddlemanWithRoborev(daemon.url);

    await page.goto(`${middleman.info.base_url}/reviews/${job.id}`);
    await expect(page.locator(".drawer")).toBeVisible();

    await page.locator(".tab-bar .tab", { hasText: "Log" }).click();

    await expect(page.locator(".log-text")).toContainText("proxy rendered log line");
    expect(daemon.requests).toContain(`/api/job/output?job_id=${job.id}`);
  } finally {
    await middleman?.stop();
    await daemon.close();
  }
});
