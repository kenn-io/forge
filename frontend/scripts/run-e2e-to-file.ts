import { spawn } from "node:child_process";
import { constants } from "node:fs";
import { mkdir, open, readFile } from "node:fs/promises";
import { dirname, isAbsolute, resolve } from "node:path";

import { planE2ERuns } from "./e2e-run-plan.ts";

const outputFile = process.env.MIDDLEMAN_E2E_OUTPUT_FILE ?? "../tmp/e2e.log";
const displayFile = isAbsolute(outputFile) ? outputFile : resolve(outputFile);
const basePlaywrightArgs = ["test", "--config=playwright-e2e.config.ts"];
const requestedArgs = process.argv.slice(2);
const runs = planE2ERuns(requestedArgs);

function timestamp(): string {
  return new Date().toISOString().replace(/\.\d{3}Z$/, "Z");
}

await mkdir(dirname(outputFile), { recursive: true });

const logFile = await open(outputFile, constants.O_CREAT | constants.O_TRUNC | constants.O_WRONLY, 0o666);
await logFile.write(
  `[${timestamp()}] node ./scripts/run-e2e-to-file.ts\n` +
    runs
      .map(
        (args) =>
          `argv: ${JSON.stringify([process.execPath, "node_modules/.bin/playwright", ...basePlaywrightArgs, ...args])}`,
      )
      .join("\n") +
    "\n\n",
);

let status = 1;
try {
  status = 0;
  for (const args of runs) {
    const playwrightArgs = [...basePlaywrightArgs, ...args];
    await logFile.write(`[${timestamp()}] node node_modules/.bin/playwright ${playwrightArgs.join(" ")}\n\n`);
    const child = spawn(process.execPath, ["node_modules/.bin/playwright", ...playwrightArgs], {
      stdio: ["ignore", logFile.fd, logFile.fd],
    });

    const runStatus = await new Promise<number>((resolve, reject) => {
      child.on("error", reject);
      child.on("close", (code) => resolve(code ?? 1));
    });
    await logFile.write(
      `\n[${timestamp()}] exit ${runStatus}: node node_modules/.bin/playwright ${playwrightArgs.join(" ")}\n\n`,
    );
    if (runStatus !== 0) {
      status = runStatus;
    }
  }
} catch (error) {
  await logFile.write(`${error instanceof Error ? error.message : String(error)}\n`);
  status = 1;
} finally {
  await logFile.close();
}

if (status === 0) {
  console.log(`[e2e] pass; full output: ${displayFile}`);
} else {
  console.error(`[e2e] fail (exit ${status}); full output: ${displayFile}`);
  if (process.env.CI) {
    console.error("[e2e] CI failure output follows:");
    console.error(await readFile(outputFile, "utf8"));
  }
}

process.exitCode = status;
