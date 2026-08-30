import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

const assets = [
  "code-reviewer-agent-launch-dark.svg",
  "code-reviewer-agent-launch-light.svg",
  "code-reviewer-dark.svg",
  "code-reviewer-light.svg",
  "docs-workspace-dark.svg",
  "docs-workspace-light.svg",
  "first-run-dark.svg",
  "first-run-light.svg",
  "issue-triager-dark.svg",
  "issue-triager-light.svg",
  "maintainer-overview-dark.svg",
  "maintainer-overview-light.svg",
  "mobile-workspace-session-dark.svg",
  "mobile-workspace-session-light.svg",
  "repository-source-dark.svg",
  "repository-source-light.svg",
  "roborev-reviews-dark.svg",
  "roborev-reviews-light.svg",
  "settings-overview-dark.svg",
  "settings-overview-light.svg",
  "workspace-codex-session-dark.svg",
  "workspace-codex-session-light.svg",
  "workspace-pr-details-dark.svg",
  "workspace-pr-details-light.svg",
];

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: "utf8", ...options });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return result;
}

function git(root, ...args) {
  return run("git", ["-C", root, ...args]);
}

async function initRepo(root) {
  git(root, "init", "-b", "main");
  git(root, "config", "user.name", "Docs Test");
  git(root, "config", "user.email", "docs@example.com");
  await writeFile(path.join(root, "README.md"), "fixture\n");
  git(root, "add", "README.md");
  git(root, "commit", "-m", "fixture");
}

async function writeAssetSet(root, selected = assets) {
  for (const asset of selected) {
    await writeFile(path.join(root, asset), `<svg><title>${asset}</title></svg>\n`);
  }
}

function sync(root, extraEnv = {}) {
  return spawnSync("bash", ["scripts/sync-docs-assets.sh"], {
    cwd: path.resolve("."),
    encoding: "utf8",
    env: {
      ...process.env,
      DOCS_ASSETS_REPO_ROOT: root,
      DOCS_ASSETS_REMOTE: "missing-remote",
      DOCS_ASSETS_RAW_ROOT: "http://127.0.0.1:1/unavailable",
      ...extraEnv,
    },
  });
}

test("asset sync publishes one complete local generation", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-assets-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await initRepo(root);

  git(root, "checkout", "--orphan", "docs-assets");
  git(root, "rm", "-rf", ".");
  await writeAssetSet(root);
  git(root, "add", ".");
  git(root, "commit", "-m", "assets");
  git(root, "checkout", "main");

  const destination = path.join(root, "docs", "assets", "generated");
  await mkdir(destination, { recursive: true });
  await writeFile(path.join(destination, "stale.svg"), "stale\n");

  const result = sync(root);
  assert.equal(result.status, 0, result.stderr || result.stdout);
  assert.deepEqual(
    (await readdir(destination)).filter((entry) => entry.endsWith(".svg")).sort(),
    assets,
  );
  assert.match(await readFile(path.join(destination, ".docs-assets.synced"), "utf8"), /first-run-dark\.svg/);
});

test("asset sync leaves the current generation intact when the fetched branch is incomplete", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-assets-test-"));
  const remote = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-assets-remote-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  t.after(() => rm(remote, { recursive: true, force: true }));
  await initRepo(root);

  git(root, "checkout", "--orphan", "docs-assets");
  git(root, "rm", "-rf", ".");
  await writeAssetSet(root, [assets[0]]);
  git(root, "add", ".");
  git(root, "commit", "-m", "incomplete assets");
  git(root, "checkout", "main");
  run("git", ["clone", "--bare", root, remote]);
  git(root, "remote", "add", "fixture-origin", remote);

  const destination = path.join(root, "docs", "assets", "generated");
  await mkdir(destination, { recursive: true });
  await writeFile(path.join(destination, "current.svg"), "current generation\n");

  const result = sync(root, { DOCS_ASSETS_REMOTE: "fixture-origin" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /incomplete/);
  assert.equal(await readFile(path.join(destination, "current.svg"), "utf8"), "current generation\n");
});
