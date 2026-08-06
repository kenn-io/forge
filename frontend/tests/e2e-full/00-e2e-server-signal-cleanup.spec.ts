import { expect, test } from "@playwright/test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { readFile, rm, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

test("a repeatedly terminated e2e owner removes real workspace tmux state", async () => {
  test.setTimeout(90_000);
  test.skip(process.platform === "win32", "POSIX signals and tmux are required");
  test.skip(spawnSync("tmux", ["-V"], { stdio: "ignore" }).status !== 0, "tmux is required");

  const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-owner-signal-"));
  const helperFile = path.join(dir, "owner-helper.ts");
  const readyFile = path.join(dir, "owner-ready.json");
  const moduleURL = pathToFileURL(path.resolve("tests/e2e-full/support/e2eServer.ts")).href;
  writeFileSync(
    helperFile,
    `import { readdir, writeFile } from "node:fs/promises";
import process from "node:process";
import { startIsolatedE2EServerWithOptions } from ${JSON.stringify(moduleURL)};

const server = await startIsolatedE2EServerWithOptions({ freshProcess: true });
const create = await fetch(server.info.base_url + "/api/v1/workspaces", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ provider: "github", platform_host: "github.com", owner: "acme", name: "widgets", mr_number: 1 }),
});
if (create.status !== 202) throw new Error("create workspace failed: " + create.status + " " + await create.text());
const workspace = await create.json();
for (let attempt = 0; attempt < 100; attempt += 1) {
  const response = await fetch(server.info.base_url + "/api/v1/workspaces/" + workspace.id);
  const current = await response.json();
  if (current.status === "ready") break;
  if (current.status === "error") throw new Error(current.error_message || "workspace failed");
  if (attempt === 99) throw new Error("workspace did not become ready");
  await new Promise((resolve) => setTimeout(resolve, 100));
}
const launch = await fetch(server.info.base_url + "/api/v1/workspaces/" + workspace.id + "/runtime/sessions", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ target_key: "plain_shell", display_region: "terminal" }),
});
if (!launch.ok) throw new Error("launch session failed: " + launch.status + " " + await launch.text());
const tmuxDir = process.env.PLAYWRIGHT_E2E_TMUX_DIR;
if (!tmuxDir) throw new Error("e2e owner did not publish its tmux root");
for (let attempt = 0; attempt < 100; attempt += 1) {
  const entries = await readdir(tmuxDir);
  if (entries.some((entry) => entry.startsWith("mm-e2e-") && entry.endsWith(".sock"))) break;
  if (attempt === 99) throw new Error("workspace tmux socket did not appear");
  await new Promise((resolve) => setTimeout(resolve, 50));
}
await writeFile(process.env.KENN_FORGE_E2E_OWNER_READY, JSON.stringify({ tmuxDir }));
setInterval(() => {}, 1000);
`,
  );

  const helperEnv: NodeJS.ProcessEnv = {
    ...process.env,
    KENN_FORGE_E2E_OWNER_READY: readyFile,
  };
  delete helperEnv.PLAYWRIGHT_E2E_TMUX_DIR;
  const owner = spawn("bun", [helperFile], {
    env: helperEnv,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  owner.stdout?.on("data", (chunk) => {
    output += chunk.toString();
  });
  owner.stderr?.on("data", (chunk) => {
    output += chunk.toString();
  });

  try {
    const deadline = Date.now() + 60_000;
    let tmuxDir = "";
    while (!tmuxDir) {
      try {
        tmuxDir = JSON.parse(await readFile(readyFile, "utf8")).tmuxDir;
      } catch {
        if (owner.exitCode !== null || owner.signalCode !== null) {
          throw new Error(`e2e owner exited before readiness: ${output}`);
        }
        if (Date.now() >= deadline) {
          throw new Error(`timed out waiting for e2e owner readiness: ${output}`);
        }
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
    }

    const outcomePromise = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolve) => {
      owner.once("exit", (code, signal) => resolve({ code, signal }));
    });
    owner.kill("SIGTERM");
    await new Promise((resolve) => setTimeout(resolve, 5));
    owner.kill("SIGTERM");
    const outcome = await outcomePromise;

    expect(outcome, output).toEqual({ code: 143, signal: null });
    await expect(stat(tmuxDir)).rejects.toThrow();
  } finally {
    if (owner.exitCode === null && owner.signalCode === null) {
      const exited = new Promise<boolean>((resolve) => {
        owner.once("exit", () => resolve(true));
        setTimeout(() => resolve(false), 20_000);
      });
      owner.kill("SIGTERM");
      if (!(await exited)) {
        const forcedExit = new Promise<void>((resolve) => owner.once("exit", () => resolve()));
        if (owner.exitCode === null && owner.signalCode === null) {
          owner.kill("SIGKILL");
          await forcedExit;
        }
      }
    }
    await rm(dir, { force: true, recursive: true });
  }
});
