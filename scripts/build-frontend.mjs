import { spawn } from "node:child_process";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");
const transientFailureMarkers = ["failed to load virtual css module", "[lightningcss minify] Invalid empty selector"];

function spawnFrontendBuild() {
  const vitePlusPath = path.join(repoRoot, "node_modules", "vite-plus", "bin", "vp");

  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [vitePlusPath, "build", "--logLevel", "warn"], {
      cwd: path.join(repoRoot, "frontend"),
      env: process.env,
      stdio: ["inherit", "pipe", "pipe"],
    });
    let output = "";

    child.stdout.on("data", (chunk) => {
      output += chunk;
      process.stdout.write(chunk);
    });
    child.stderr.on("data", (chunk) => {
      output += chunk;
      process.stderr.write(chunk);
    });
    child.once("error", reject);
    child.once("close", (exitCode) => {
      resolve({ exitCode: exitCode ?? 1, output });
    });
  });
}

function isTransientSvelteCssFailure({ exitCode, output }) {
  return exitCode !== 0 && transientFailureMarkers.every((marker) => output.includes(marker));
}

export async function runFrontendBuild({
  onRetry = (message) => console.warn(message),
  runBuild = spawnFrontendBuild,
} = {}) {
  const firstResult = await runBuild();
  if (!isTransientSvelteCssFailure(firstResult)) {
    return firstResult.exitCode;
  }

  onRetry("Transient Svelte CSS build failure detected; retrying once.");
  return (await runBuild()).exitCode;
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  try {
    process.exitCode = await runFrontendBuild();
  } catch (error) {
    console.error(error);
    process.exitCode = 1;
  }
}
