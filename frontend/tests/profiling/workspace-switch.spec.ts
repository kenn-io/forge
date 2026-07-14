// Reproducible workspace-switch profiling harness. See README.md in
// this directory for how to run it and how to read the artifacts.
//
// The harness drives a real e2e backend (git worktrees + tmux) and
// measures the workspace-switch:* User Timing marks emitted by
// src/lib/instrumentation/workspaceSwitchTiming.ts across four
// scenarios: warm and cold switches into a workspace running an
// ordinary shell, and into one running an alternate-screen
// application. Artifacts (timings.json, summary.txt, a Chromium
// trace, and a Go execution trace) land in
// test-results/workspace-switch-profile/<timestamp>/.

import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { load as loadToml } from "js-toml";
import { expect, request as playwrightRequest, test, type APIRequestContext, type Page } from "@playwright/test";
import { startIsolatedWorkspaceE2EServerWithOptions, type IsolatedE2EServer } from "../e2e-full/support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
  worktree_path?: string;
};

type SwitchEntry = {
  name: string;
  startTime: number;
  duration: number;
  detail: Record<string, unknown> | null;
};

type SwitchMeasurement = {
  scenario: string;
  iteration: number;
  workspaceId: string;
  // performance.timeOrigin of the document the entries came from, so
  // browser timings can be aligned with wall-clock Go-side captures.
  timeOriginEpochMs: number;
  entries: SwitchEntry[];
  derived: Record<string, number | null>;
};

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

const iterations = Math.max(1, Number(process.env.MIDDLEMAN_PROFILE_ITERATIONS ?? "3") || 3);

const outputDir =
  process.env.MIDDLEMAN_PROFILE_OUT_DIR ??
  path.join(frontendDir, "test-results", "workspace-switch-profile", new Date().toISOString().replace(/[:.]/g, "-"));

// Phases the instrumentation must emit for a switch into a ready
// workspace with a running terminal session. A missing phase means
// the wiring regressed, which would silently invalidate before/after
// comparisons made with this harness.
const requiredPhases = [
  "workspace-request-start",
  "workspace-request-end",
  "runtime-request-start",
  "runtime-request-end",
  "fonts-ready",
  "terminal-constructed",
  "socket-open",
  "first-bytes",
  "first-paint",
];

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

// The profiling command is only ever run deliberately, so a missing
// host dependency is a hard failure — a skip would let
// `make profile-workspace-switch` exit 0 without profiling anything.
function requireHostCommands(): void {
  const missing = [["git", ["--version"]] as const, ["tmux", ["-V"]] as const, ["less", ["--version"]] as const].filter(
    ([command, args]) => !hasCommand(command, [...args]),
  );
  if (missing.length > 0) {
    throw new Error(
      `workspace-switch profiling requires ${missing.map(([command]) => command).join(", ")} on the host`,
    );
  }
}

// Reads the isolated server's generated config to find its private
// tmux socket, then asserts some pane on that server is actually in
// alternate-screen mode running the pager. Without this, a shell or
// environment quirk could silently turn the "alt-screen" scenario
// into a second ordinary-shell measurement.
async function assertAlternateScreenActive(configPath: string): Promise<void> {
  const config = loadToml(await readFile(configPath, "utf8")) as {
    tmux?: { command?: string[] };
  };
  const [tmuxBin, ...tmuxArgs] = config.tmux?.command ?? [];
  if (!tmuxBin) {
    throw new Error(`config at ${configPath} must define the e2e tmux command`);
  }
  // Space separator: tmux replaces control characters (tabs included)
  // with "_" when expanding formats, and pane_current_command is a
  // single token anyway.
  const panes = execFileSync(
    tmuxBin,
    [...tmuxArgs, "list-panes", "-a", "-F", "#{alternate_on} #{pane_current_command}"],
    { encoding: "utf8" },
  );
  const altPanes = panes
    .trim()
    .split("\n")
    .filter((line) => line.startsWith("1 "));
  expect(
    altPanes.some((line) => line.includes("less")),
    `expected a tmux pane running less in alternate-screen mode, got:\n${panes}`,
  ).toBe(true);
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<void> {
  for (let attempt = 0; attempt < 300; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") {
      return;
    }
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function createIssueWorkspace(api: APIRequestContext, issueNumber: number): Promise<WorkspaceStatusResponse> {
  const createResponse = await api.post(`/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`, {
    data: {},
  });
  expect(createResponse.status()).toBe(202);
  const created = (await createResponse.json()) as WorkspaceStatusResponse;
  await waitForWorkspaceReady(api, created.id);
  const detail = await api.get(`/api/v1/workspaces/${created.id}`);
  expect(detail.ok()).toBe(true);
  return (await detail.json()) as WorkspaceStatusResponse;
}

// Client-side route change, as the sidebar workspace list and browser
// back/forward do it. A full page.goto would reload the SPA and turn
// every switch into a cold load.
async function spaSwitch(page: Page, workspaceId: string): Promise<void> {
  await page.evaluate((id) => {
    history.pushState(null, "", `/terminal/${encodeURIComponent(id)}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, workspaceId);
}

async function collectSwitchEntries(page: Page, workspaceId: string, sinceTime: number): Promise<SwitchEntry[]> {
  await page.waitForFunction(
    ({ id, since }) =>
      performance
        .getEntriesByType("measure")
        .some(
          (m) =>
            m.name === "workspace-switch:first-paint" &&
            m.startTime > since &&
            (m as PerformanceMeasure).detail?.workspaceId === id,
        ),
    { id: workspaceId, since: sinceTime },
    { timeout: 60_000 },
  );
  return await page.evaluate(
    ({ since }) =>
      performance
        .getEntriesByType("measure")
        .filter((m) => m.name.startsWith("workspace-switch:") && m.startTime > since)
        .map((m) => ({
          name: m.name,
          startTime: m.startTime,
          duration: m.duration,
          detail: ((m as PerformanceMeasure).detail ?? null) as Record<string, unknown> | null,
        })),
    { since: sinceTime },
  );
}

function phaseDuration(entries: SwitchEntry[], phase: string): number | null {
  const entry = entries.find((e) => e.name === `workspace-switch:${phase}`);
  return entry ? entry.duration : null;
}

// All measures share the route-selection start mark, so a measure's
// duration is "time from route selection to this phase". The derived
// values answer the questions the epic cares about directly.
function deriveMetrics(entries: SwitchEntry[]): Record<string, number | null> {
  const d = (phase: string) => phaseDuration(entries, phase);
  const firstBytes = d("first-bytes");
  const firstPaint = d("first-paint");
  return {
    routeToWorkspaceRequestStart: d("workspace-request-start"),
    routeToWorkspaceRequestEnd: d("workspace-request-end"),
    routeToRuntimeRequestStart: d("runtime-request-start"),
    routeToRuntimeRequestEnd: d("runtime-request-end"),
    routeToFontsReady: d("fonts-ready"),
    routeToTerminalConstructed: d("terminal-constructed"),
    routeToSocketOpen: d("socket-open"),
    routeToFirstBytes: firstBytes,
    routeToFirstPaint: firstPaint,
    firstBytesToFirstPaint: firstBytes !== null && firstPaint !== null ? firstPaint - firstBytes : null,
  };
}

async function measureSwitch(
  page: Page,
  workspaceId: string,
  scenario: string,
  iteration: number,
  navigate: () => Promise<void>,
  sinceTime: number,
): Promise<SwitchMeasurement> {
  await navigate();
  const entries = await collectSwitchEntries(page, workspaceId, sinceTime);
  const timeOriginEpochMs = await page.evaluate(() => performance.timeOrigin);
  // Let the paint settle and runtime polling quiesce before the next
  // switch so back-to-back iterations stay comparable.
  await page.waitForTimeout(300);
  return {
    scenario,
    iteration,
    workspaceId,
    timeOriginEpochMs,
    entries,
    derived: deriveMetrics(entries),
  };
}

async function measureWarmSwitch(
  page: Page,
  workspaceId: string,
  scenario: string,
  iteration: number,
): Promise<SwitchMeasurement> {
  const sinceTime = await page.evaluate(() => performance.now());
  return await measureSwitch(page, workspaceId, scenario, iteration, () => spaSwitch(page, workspaceId), sinceTime);
}

async function measureColdLoad(
  page: Page,
  baseURL: string,
  workspaceId: string,
  scenario: string,
): Promise<SwitchMeasurement> {
  return await measureSwitch(
    page,
    workspaceId,
    scenario,
    0,
    async () => {
      await page.goto(`${baseURL}/terminal/${workspaceId}`);
    },
    // A cold load starts a fresh performance timeline.
    -1,
  );
}

async function openWorkspaceAndLaunchTerminal(page: Page, baseURL: string, workspaceId: string): Promise<void> {
  await page.goto(`${baseURL}/terminal/${workspaceId}`);
  const workflow = page.getByRole("region", { name: "Workflow panes" });
  await expect(workflow.getByRole("tab", { name: "Home" })).toBeVisible();

  const terminalPanel = page.getByRole("region", { name: "Terminal panel" });
  await terminalPanel.getByRole("button", { name: "New terminal" }).click();
  // The pane records first-paint once the shell prompt has rendered;
  // waiting on it beats a fixed sleep for knowing the tmux session is
  // live and attached.
  await collectSwitchEntries(page, workspaceId, -1);
}

async function runShellCommandInTerminal(page: Page, command: string): Promise<void> {
  const terminalPanel = page.getByRole("region", { name: "Terminal panel" });
  await terminalPanel.locator(".terminal-container").first().click();
  await page.keyboard.type(command, { delay: 25 });
  await page.keyboard.press("Enter");
  // Give the command time to take over the screen before the harness
  // navigates away; the tmux session keeps running it afterwards.
  await page.waitForTimeout(1_500);
}

function formatMs(value: number | null): string {
  return value === null ? "      -" : `${value.toFixed(1).padStart(7)}`;
}

function summaryLines(measurements: SwitchMeasurement[]): string[] {
  const columns: Array<[string, string]> = [
    ["wsReqEnd", "routeToWorkspaceRequestEnd"],
    ["rtReqEnd", "routeToRuntimeRequestEnd"],
    ["fonts", "routeToFontsReady"],
    ["term", "routeToTerminalConstructed"],
    ["sockOpen", "routeToSocketOpen"],
    ["firstByte", "routeToFirstBytes"],
    ["firstPaint", "routeToFirstPaint"],
    ["byte→paint", "firstBytesToFirstPaint"],
  ];
  const lines = [
    "workspace-switch profile (ms from route selection; byte→paint is a delta)",
    `${"scenario".padEnd(24)} it ${columns.map(([label]) => label.padStart(10)).join(" ")}`,
  ];
  for (const m of measurements) {
    lines.push(
      `${m.scenario.padEnd(24)} ${String(m.iteration).padStart(2)} ${columns
        .map(([, key]) => formatMs(m.derived[key] ?? null).padStart(10))
        .join(" ")}`,
    );
  }
  return lines;
}

test.describe("workspace switch profiling", () => {
  test("captures warm and cold switch profiles", async ({ page, browser }) => {
    requireHostCommands();

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      // Always an ephemeral loopback port: a fixed address inherited
      // from the environment could be occupied and silently cost the
      // run its Go trace. The resolved address comes back in the
      // server info for the Go-side capture below.
      process.env.MIDDLEMAN_PPROF_ADDR = "127.0.0.1:0";
      isolatedServer = await startIsolatedWorkspaceE2EServerWithOptions({ freshProcess: true });
      const baseURL = isolatedServer.info.base_url;
      api = await playwrightRequest.newContext({ baseURL });

      const shellWorkspace = await createIssueWorkspace(api, 10);
      const altScreenWorkspace = await createIssueWorkspace(api, 11);

      // Prepare both workspaces with a live tmux terminal session so a
      // switch back into them replays real terminal content. The
      // alternate-screen workspace runs `less` over a generated file,
      // exercising the alt-screen replay path that has historically
      // stalled; the other keeps an ordinary shell prompt.
      expect(altScreenWorkspace.worktree_path).toBeTruthy();
      const pagerFile = "workspace-switch-profile-pager.txt";
      await writeFile(
        path.join(altScreenWorkspace.worktree_path!, pagerFile),
        Array.from({ length: 400 }, (_, index) => `pager line ${index + 1}`).join("\n") + "\n",
      );

      await openWorkspaceAndLaunchTerminal(page, baseURL, shellWorkspace.id);
      await openWorkspaceAndLaunchTerminal(page, baseURL, altScreenWorkspace.id);
      await runShellCommandInTerminal(page, `less ${pagerFile}`);
      await assertAlternateScreenActive(isolatedServer.info.config_path);

      await mkdir(outputDir, { recursive: true });

      // Go-side capture: an execution trace spanning the measured
      // window, for correlating server-side stalls (tmux subprocesses,
      // replay buffering) with the browser timings. See README.md.
      let goTrace: Promise<Buffer> | null = null;
      const goTraceEnabled = process.env.MIDDLEMAN_PROFILE_GO_TRACE !== "0";
      const pprofAddr = isolatedServer.info.pprof_addr;
      if (goTraceEnabled) {
        expect(
          pprofAddr,
          "e2e server did not report a pprof listener; the Go trace artifact would be missing",
        ).toBeTruthy();
      }
      if (pprofAddr && goTraceEnabled) {
        const seconds = Math.min(30, 10 + iterations * 5);
        goTrace = api
          .get(`http://${pprofAddr}/debug/pprof/trace?seconds=${seconds}`, {
            // The profiler rejects requests that look like a browser
            // without fetch metadata; identify as tooling instead of
            // Playwright's browser-like default User-Agent.
            headers: { "user-agent": "middleman-workspace-switch-profiler" },
            timeout: (seconds + 30) * 1000,
          })
          .then(async (response) => {
            if (!response.ok()) {
              throw new Error(`go trace capture failed (${response.status()}): ${await response.text()}`);
            }
            return await response.body();
          });
      }

      const chromeTracePath = path.join(outputDir, "trace.chrome.json");
      await browser.startTracing(page, {
        path: chromeTracePath,
        screenshots: false,
        categories: [
          "-*",
          "devtools.timeline",
          "disabled-by-default-devtools.timeline",
          "disabled-by-default-devtools.timeline.frame",
          "blink.user_timing",
          "loading",
          "latencyInfo",
          "toplevel",
          "v8.execute",
        ],
      });

      const measurements: SwitchMeasurement[] = [];
      // The page currently shows the alt-screen workspace, so each
      // iteration alternates shell -> alt-screen.
      for (let iteration = 0; iteration < iterations; iteration += 1) {
        measurements.push(await measureWarmSwitch(page, shellWorkspace.id, "warm-ordinary-shell", iteration));
        measurements.push(await measureWarmSwitch(page, altScreenWorkspace.id, "warm-alt-screen", iteration));
      }

      measurements.push(await measureColdLoad(page, baseURL, shellWorkspace.id, "cold-ordinary-shell"));
      measurements.push(await measureColdLoad(page, baseURL, altScreenWorkspace.id, "cold-alt-screen"));

      await browser.stopTracing();

      if (goTrace) {
        await writeFile(path.join(outputDir, "go-trace.out"), await goTrace);
      }

      // Artifacts are written before the phase assertions below so an
      // instrumentation regression still leaves the evidence needed to
      // diagnose it.
      const lines = summaryLines(measurements);
      await writeFile(
        path.join(outputDir, "timings.json"),
        JSON.stringify(
          {
            capturedAt: new Date().toISOString(),
            baseURL,
            iterations,
            pprofAddr: pprofAddr ?? null,
            environment: {
              commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
              browserVersion: browser.version(),
              platform: `${process.platform}/${process.arch}`,
              // The harness runs middleman's default renderer; ghostty
              // emits the same timing names but is not exercised here.
              terminalRenderer: "xterm",
            },
            measurements,
          },
          null,
          2,
        ) + "\n",
      );
      await writeFile(path.join(outputDir, "summary.txt"), lines.join("\n") + "\n");

      console.log(`\n${lines.join("\n")}\n\nartifacts: ${outputDir}`);

      for (const measurement of measurements) {
        const phases = measurement.entries.map((entry) => entry.name.replace("workspace-switch:", ""));
        for (const phase of requiredPhases) {
          expect(phases, `${measurement.scenario} #${measurement.iteration} must record ${phase}`).toContain(phase);
        }
      }
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
