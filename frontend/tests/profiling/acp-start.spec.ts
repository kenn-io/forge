// Reproducible ACP chat start profiling harness. See README.md in this
// directory for how to run it and how to read the artifacts.
//
// Each round starts a fresh isolated e2e backend, so the first chat of
// each agent starts cold: no shared agent host is running yet. Later
// chats of that agent in the same round are warm. Every chat is
// launched by clicking its card in the workspace launcher, and timed
// from that click until the chat's composer accepts a prompt.

import { execFileSync } from "node:child_process";
import { readdir, readFile, mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, request as playwrightRequest, test, type APIRequestContext, type Page } from "@playwright/test";
import { startIsolatedWorkspaceE2EServerWithOptions } from "../e2e-full/support/e2eServer";

type Agent = { key: string; label: string; command: string[] };

type LaunchMeasurement = {
  agent: string;
  round: number;
  launch: number;
  start: "cold" | "warm";
  // All times are milliseconds from the click on the launch card.
  requestStart: number;
  requestEnd: number;
  ready: number;
};

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

const rounds = Math.max(1, Number(process.env.KENN_FORGE_PROFILE_ROUNDS ?? "3") || 3);
const warmLaunches = Math.max(1, Number(process.env.KENN_FORGE_PROFILE_WARM_LAUNCHES ?? "3") || 3);

const outputDir =
  process.env.KENN_FORGE_PROFILE_OUT_DIR ??
  path.join(frontendDir, "test-results", "acp-start-profile", new Date().toISOString().replace(/[:.]/g, "-"));

// The fixture agent answers at once, so its time is Forge's own share of a
// start. Real agents are opt-in: "label=command arg;label=command".
function agents(): Agent[] {
  const fixture: Agent = {
    key: "acp-fixture",
    label: "Fixture Chat",
    command: [process.execPath, path.join(frontendDir, "tests/fixtures/acp-agent.mjs")],
  };
  const extra = (process.env.KENN_FORGE_PROFILE_ACP_AGENTS ?? "")
    .split(";")
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "")
    .map((entry, index): Agent => {
      const separator = entry.indexOf("=");
      const label = entry.slice(0, separator).trim();
      const command = entry.slice(separator + 1).trim();
      if (separator < 0 || !label || !command) {
        throw new Error(`KENN_FORGE_PROFILE_ACP_AGENTS entry ${entry} must be label=command`);
      }
      return { key: `acp-profile-${index}`, label, command: command.split(/\s+/) };
    });
  return [fixture, ...extra];
}

async function createWorkspace(api: APIRequestContext): Promise<string> {
  const created = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", { data: {} });
  expect(created.status()).toBe(202);
  const { id } = (await created.json()) as { id: string };
  await expect
    .poll(async () => ((await (await api.get(`/api/v1/workspaces/${id}`)).json()) as { status: string }).status, {
      timeout: 60_000,
    })
    .toBe("ready");
  return id;
}

async function stopSessions(api: APIRequestContext, workspaceId: string): Promise<void> {
  const runtime = (await (await api.get(`/api/v1/workspaces/${workspaceId}/runtime`)).json()) as {
    sessions?: { key: string }[] | null;
  };
  for (const session of runtime.sessions ?? []) {
    const stopped = await api.delete(
      `/api/v1/workspaces/${workspaceId}/runtime/sessions/${encodeURIComponent(session.key)}`,
    );
    expect(stopped.ok(), await stopped.text()).toBe(true);
  }
}

// Shared agent hosts are detached from the server and would outlive the
// round for their idle timeout; end the ones this round's server started.
async function stopAgentHosts(stateDir: string): Promise<void> {
  for (const pid of await readdir("/proc")) {
    if (!/^\d+$/.test(pid)) continue;
    const args = await readFile(`/proc/${pid}/cmdline`, "utf8").then(
      (text) => text.split("\0"),
      () => [],
    );
    if (args[1] === "acp-host" && args[2]?.startsWith(stateDir + path.sep)) {
      process.kill(Number(pid));
    }
  }
}

async function measureLaunch(page: Page, baseURL: string, workspaceId: string, agent: Agent) {
  await page.goto(`${baseURL}/terminal/${workspaceId}`);
  const card = page.getByRole("region", { name: "Session launcher" }).getByRole("button", { name: agent.label });
  await expect(card).toBeEnabled();
  // The page records the click and polls each frame for an enabled composer,
  // so the measurement does not include Playwright's own polling.
  await page.evaluate(() => {
    const timing = { click: 0, ready: 0 };
    (window as unknown as { acpStartTiming: typeof timing }).acpStartTiming = timing;
    document.addEventListener(
      "click",
      () => {
        timing.click = performance.now();
        const poll = () => {
          const composer = document.querySelector<HTMLTextAreaElement>('textarea[aria-label="Message agent"]');
          if (composer && !composer.disabled) timing.ready = performance.now();
          else requestAnimationFrame(poll);
        };
        requestAnimationFrame(poll);
      },
      { capture: true, once: true },
    );
  });
  await card.click();
  await page.waitForFunction(
    () => (window as unknown as { acpStartTiming: { ready: number } }).acpStartTiming.ready > 0,
    undefined,
    { timeout: 60_000 },
  );
  return await page.evaluate(() => {
    const timing = (window as unknown as { acpStartTiming: { click: number; ready: number } }).acpStartTiming;
    const launch = performance
      .getEntriesByType("resource")
      .filter((entry) => /\/runtime\/sessions$/.test(new URL(entry.name).pathname))
      .at(-1) as PerformanceResourceTiming | undefined;
    if (!launch) throw new Error("no launch request was recorded");
    return {
      requestStart: launch.requestStart - timing.click,
      requestEnd: launch.responseEnd - timing.click,
      ready: timing.ready - timing.click,
    };
  });
}

function median(values: number[]): number {
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.floor((sorted.length - 1) / 2)]!;
}

function summarize(measurements: LaunchMeasurement[]): string {
  const lines = ["agent           start  n   click->request  request  response->ready  click->ready (median, ms)"];
  for (const agent of new Set(measurements.map((m) => m.agent))) {
    for (const start of ["cold", "warm"] as const) {
      const group = measurements.filter((m) => m.agent === agent && m.start === start);
      if (group.length === 0) continue;
      const cell = (pick: (m: LaunchMeasurement) => number) => median(group.map(pick)).toFixed(0).padStart(6);
      lines.push(
        `${agent.padEnd(15)} ${start}   ${String(group.length).padEnd(3)} ${cell((m) => m.requestStart).padStart(14)}  ${cell(
          (m) => m.requestEnd - m.requestStart,
        ).padStart(7)}  ${cell((m) => m.ready - m.requestEnd).padStart(15)}  ${cell((m) => m.ready).padStart(12)}`,
      );
    }
  }
  return lines.join("\n");
}

test("ACP chat start time", async ({ page, browserName }) => {
  test.skip(browserName !== "chromium", "profiling uses Chromium");
  test.setTimeout(rounds * 240_000);
  const profiled = agents();
  const measurements: LaunchMeasurement[] = [];
  await mkdir(outputDir, { recursive: true });
  try {
    for (let round = 0; round < rounds; round += 1) {
      const server = await startIsolatedWorkspaceE2EServerWithOptions({ freshProcess: true });
      const api = await playwrightRequest.newContext({ baseURL: server.info.base_url });
      try {
        const saved = await api.put("/api/v1/settings", {
          data: {
            agents: profiled.map((agent) => ({
              key: agent.key,
              label: agent.label,
              protocol: "acp",
              command: agent.command,
            })),
          },
        });
        expect(saved.ok(), await saved.text()).toBe(true);
        const workspaceId = await createWorkspace(api);
        for (const agent of profiled) {
          for (let launch = 0; launch <= warmLaunches; launch += 1) {
            const timing = await measureLaunch(page, server.info.base_url, workspaceId, agent);
            measurements.push({ agent: agent.label, round, launch, start: launch === 0 ? "cold" : "warm", ...timing });
            await stopSessions(api, workspaceId);
          }
        }
      } finally {
        await api.dispose();
        await server.stop();
        await stopAgentHosts(path.dirname(server.info.config_path));
      }
    }
  } finally {
    const summary = summarize(measurements);
    console.log(summary);
    await writeFile(path.join(outputDir, "summary.txt"), summary + "\n");
    await writeFile(
      path.join(outputDir, "timings.json"),
      JSON.stringify(
        {
          environment: {
            commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
            browser: page.context().browser()?.version(),
            platform: `${process.platform} ${process.arch}`,
          },
          rounds,
          warmLaunches,
          agents: profiled.map(({ label, command }) => ({ label, command: path.basename(command[0]!) })),
          measurements,
        },
        null,
        2,
      ),
    );
  }
});
