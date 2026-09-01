import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";

export function isolatedGitEnv(env = process.env) {
  return Object.fromEntries(
    Object.entries(env).filter(([name]) => {
      const normalized = name.toUpperCase();
      return !normalized.startsWith("GIT_") && normalized !== "SSH_ASKPASS";
    }),
  );
}

export function createGitTestRepository(t, options = {}) {
  const { env = process.env, initialBranch = "main", prefix = "kenn-forge-git-test-" } = options;
  const scratch = mkdtempSync(path.join(os.tmpdir(), prefix));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));

  const root = path.join(scratch, "repo");
  mkdirSync(root);
  const globalConfig = path.join(scratch, "global.gitconfig");
  writeFileSync(globalConfig, "");
  const gitEnv = {
    ...isolatedGitEnv(env),
    GIT_CONFIG_GLOBAL: globalConfig,
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_TERMINAL_PROMPT: "0",
  };

  function git(...args) {
    const result = spawnSync("git", ["-C", root, ...args], {
      encoding: "utf8",
      env: gitEnv,
    });
    if (result.error) throw result.error;
    if (result.status !== 0) {
      throw new Error(result.stderr || result.stdout || `git exited with status ${result.status}`);
    }
    return result;
  }

  git("init", "--quiet", "-b", initialBranch);
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.invalid");

  return { env: gitEnv, git, root, scratch };
}
