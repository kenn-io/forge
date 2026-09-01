import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");

export function docsSiteProjectArgs(env = process.env) {
  const project = env.KENN_FORGE_DOCS_SITE_PROJECT;
  return project && project !== "all" ? [`--project=${project}`] : [];
}

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: "inherit", ...options });
    child.on("error", reject);
    child.on("exit", (code, signal) => {
      if (code === 0) return resolve();
      reject(new Error(signal ? `${command} terminated by ${signal}` : `${command} exited with status ${code}`));
    });
  });
}

export async function verifyDocsSite() {
  const siteDir = path.resolve(process.env.KENN_FORGE_DOCS_SITE_DIR ?? path.join(repoRoot, "site"));
  await run(
    process.execPath,
    [
      path.join(repoRoot, "node_modules", "vite-plus", "bin", "vp"),
      "exec",
      "--",
      "playwright",
      "test",
      "--config",
      path.join(repoRoot, "docs", "site", "playwright.config.ts"),
      ...docsSiteProjectArgs(),
    ],
    { cwd: repoRoot, env: { ...process.env, KENN_FORGE_DOCS_SITE_DIR: siteDir } },
  );
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  verifyDocsSite().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
