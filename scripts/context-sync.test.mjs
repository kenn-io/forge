import assert from "node:assert/strict";
import { execFile as execFileCallback } from "node:child_process";
import { mkdir, readFile, symlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

import { createGitTestRepository } from "./test-git-fixture.mjs";

const execFile = promisify(execFileCallback);
const scriptPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "context-sync");
async function createContextRoot(t, env = process.env) {
  const repository = createGitTestRepository(t, {
    env,
    prefix: "kenn-forge-context-sync-test-",
  });
  const { root } = repository;
  await mkdir(path.join(root, "skills", "context-sync"), { recursive: true });
  await writeFile(path.join(root, "CLAUDE.md"), "# Agent instructions\n");
  await symlink("CLAUDE.md", path.join(root, "AGENTS.md"));
  await writeFile(path.join(root, "skills", "context-sync", "SKILL.md"), "# Context sync\n");
  return repository;
}

test("context sync accepts a valid routed context surface", async (t) => {
  const { env, root } = await createContextRoot(t);

  const result = await execFile(scriptPath, ["--check"], {
    cwd: root,
    env,
  });

  assert.match(result.stdout, /structural check passed/);
});

test("context sync accepts a skill-required Superpowers design spec", async (t) => {
  const { env, root } = await createContextRoot(t);
  const specs = path.join(root, "docs", "superpowers", "specs");
  await mkdir(specs, { recursive: true });
  await writeFile(path.join(specs, "active-design.md"), "# Active design\n");

  await assert.rejects(execFile(scriptPath, ["--check"], { cwd: root, env }), (error) => {
    assert.match(error.stderr, /docs\/superpowers must remain absent/);
    assert.match(error.stderr, /do not convert them into ADRs/);
    assert.match(error.stderr, /structural check failed/);
    return true;
  });
});

test("context sync fixtures ignore inherited hook repository bindings", async (t) => {
  const hostRepository = createGitTestRepository(t, {
    prefix: "kenn-forge-hook-host-test-",
  });
  const hostRoot = hostRepository.root;

  const hostConfigPath = path.join(hostRoot, ".git", "config");
  const originalHostConfig = await readFile(hostConfigPath, "utf8");
  const hookEnv = {
    ...process.env,
    GIT_DIR: path.join(hostRoot, ".git"),
    GIT_WORK_TREE: hostRoot,
  };

  const { env, root } = await createContextRoot(t, hookEnv);
  const result = await execFile(scriptPath, ["--check"], {
    cwd: root,
    env,
  });

  assert.match(result.stdout, /structural check passed/);
  assert.equal(await readFile(hostConfigPath, "utf8"), originalHostConfig);
});
