import assert from "node:assert/strict";
import { execFile as execFileCallback } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const execFile = promisify(execFileCallback);
const scriptPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "context-sync");
const gitRepositoryEnvVars = [
  "GIT_ALTERNATE_OBJECT_DIRECTORIES",
  "GIT_COMMON_DIR",
  "GIT_CONFIG",
  "GIT_CONFIG_COUNT",
  "GIT_CONFIG_PARAMETERS",
  "GIT_DIR",
  "GIT_GRAFT_FILE",
  "GIT_IMPLICIT_WORK_TREE",
  "GIT_INDEX_FILE",
  "GIT_NO_REPLACE_OBJECTS",
  "GIT_OBJECT_DIRECTORY",
  "GIT_PREFIX",
  "GIT_REPLACE_REF_BASE",
  "GIT_SHALLOW_FILE",
  "GIT_WORK_TREE",
];

function isolatedGitEnv(env = process.env) {
  const isolated = { ...env };
  for (const name of gitRepositoryEnvVars) {
    delete isolated[name];
  }
  for (const name of Object.keys(isolated)) {
    if (/^GIT_CONFIG_(?:KEY|VALUE)_\d+$/.test(name)) {
      delete isolated[name];
    }
  }
  return isolated;
}

async function createContextRoot(t, env = process.env) {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-context-sync-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  await execFile("git", ["init", "--quiet", root], { env: isolatedGitEnv(env) });
  await mkdir(path.join(root, "skills", "context-sync"), { recursive: true });
  await writeFile(path.join(root, "CLAUDE.md"), "# Agent instructions\n");
  await symlink("CLAUDE.md", path.join(root, "AGENTS.md"));
  await writeFile(path.join(root, "skills", "context-sync", "SKILL.md"), "# Context sync\n");
  return root;
}

test("context sync accepts a valid routed context surface", async (t) => {
  const root = await createContextRoot(t);

  const result = await execFile(scriptPath, ["--check"], {
    cwd: root,
    env: isolatedGitEnv(),
  });

  assert.match(result.stdout, /structural check passed/);
});

test("context sync rejects reintroduced Superpowers documents", async (t) => {
  const root = await createContextRoot(t);
  const specs = path.join(root, "docs", "superpowers", "specs");
  await mkdir(specs, { recursive: true });
  await writeFile(path.join(specs, "completed-design.md"), "# Completed design\n");

  await assert.rejects(execFile(scriptPath, ["--check"], { cwd: root, env: isolatedGitEnv() }), (error) => {
    assert.match(error.stderr, /docs\/superpowers must remain absent/);
    assert.match(error.stderr, /do not convert them into ADRs/);
    assert.match(error.stderr, /structural check failed/);
    return true;
  });
});

test("context sync fixtures ignore inherited hook repository bindings", async (t) => {
  const hostRoot = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-hook-host-test-"));
  t.after(() => rm(hostRoot, { recursive: true, force: true }));
  await execFile("git", ["init", "--quiet", hostRoot], { env: isolatedGitEnv() });

  const hostConfigPath = path.join(hostRoot, ".git", "config");
  const originalHostConfig = await readFile(hostConfigPath, "utf8");
  const hookEnv = {
    ...process.env,
    GIT_DIR: path.join(hostRoot, ".git"),
    GIT_WORK_TREE: hostRoot,
  };

  const root = await createContextRoot(t, hookEnv);
  const result = await execFile(scriptPath, ["--check"], {
    cwd: root,
    env: isolatedGitEnv(hookEnv),
  });

  assert.match(result.stdout, /structural check passed/);
  assert.equal(await readFile(hostConfigPath, "utf8"), originalHostConfig);
});
