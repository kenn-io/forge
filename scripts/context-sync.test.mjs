import assert from "node:assert/strict";
import { execFile as execFileCallback } from "node:child_process";
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const execFile = promisify(execFileCallback);
const scriptPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "context-sync");

async function createContextRoot(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-context-sync-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  await execFile("git", ["init", "--quiet", root]);
  await mkdir(path.join(root, "skills", "context-sync"), { recursive: true });
  await writeFile(path.join(root, "CLAUDE.md"), "# Agent instructions\n");
  await symlink("CLAUDE.md", path.join(root, "AGENTS.md"));
  await writeFile(path.join(root, "skills", "context-sync", "SKILL.md"), "# Context sync\n");
  return root;
}

test("context sync accepts a valid routed context surface", async (t) => {
  const root = await createContextRoot(t);

  const result = await execFile(scriptPath, ["--check"], { cwd: root });

  assert.match(result.stdout, /structural check passed/);
});

test("context sync permits temporary Superpowers documents", async (t) => {
  const root = await createContextRoot(t);
  const specs = path.join(root, "docs", "superpowers", "specs");
  await mkdir(specs, { recursive: true });
  await writeFile(path.join(specs, "working-design.md"), "# Working design\n");

  const result = await execFile(scriptPath, ["--check"], { cwd: root });

  assert.match(result.stdout, /structural check passed/);
});
