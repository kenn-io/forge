import { spawn } from "node:child_process";
import { constants } from "node:fs";
import { mkdir, open, readFile } from "node:fs/promises";
import { dirname, isAbsolute, resolve } from "node:path";

const outputFile = process.env.MIDDLEMAN_E2E_OUTPUT_FILE ?? "test-results/e2e.log";
const displayFile = isAbsolute(outputFile) ? outputFile : resolve(outputFile);
const playwrightArgs = ["test", "--config=playwright-e2e.config.ts", ...process.argv.slice(2)];

function timestamp(): string {
  return new Date().toISOString().replace(/\.\d{3}Z$/, "Z");
}

await mkdir(dirname(outputFile), { recursive: true });

const logFile = await open(outputFile, constants.O_CREAT | constants.O_TRUNC | constants.O_WRONLY, 0o666);
await logFile.write(
  `[${timestamp()}] bun run test:e2e\n` + `argv: ${JSON.stringify(["playwright", ...playwrightArgs])}\n\n`,
);

let status = 1;
try {
  const child = spawn("playwright", playwrightArgs, {
    stdio: ["ignore", logFile.fd, logFile.fd],
  });

  status = await new Promise<number>((resolve, reject) => {
    child.on("error", reject);
    child.on("close", (code) => resolve(code ?? 1));
  });
} catch (error) {
  await logFile.write(`${error instanceof Error ? error.message : String(error)}\n`);
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
