import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { test } from "node:test";

import { createGitTestRepository } from "./test-git-fixture.mjs";

const assets = ["first-workflow.svg", "second-workflow.svg"];

async function createDocsAssetsRepository(t) {
  const repository = createGitTestRepository(t, {
    prefix: "kenn-forge-docs-assets-test-",
  });
  await writeFile(path.join(repository.root, "README.md"), "fixture\n");
  repository.git("add", "README.md");
  repository.git("commit", "-m", "fixture");
  return repository;
}

async function writeAssetSet(root, selected = assets) {
  for (const asset of selected) {
    await writeFile(path.join(root, asset), `<svg><title>${asset}</title></svg>\n`);
  }
}

function sync(repository, extraEnv = {}) {
  return spawnSync("bash", ["scripts/sync-docs-assets.sh"], {
    cwd: path.resolve("."),
    encoding: "utf8",
    env: {
      ...repository.env,
      DOCS_ASSETS_REPO_ROOT: repository.root,
      DOCS_ASSETS_REMOTE: "missing-remote",
      DOCS_ASSETS_RAW_ROOT: "http://127.0.0.1:1/unavailable",
      ...extraEnv,
    },
  });
}

test("asset sync publishes the pinned generation instead of a newer branch tip", async (t) => {
  const repository = await createDocsAssetsRepository(t);
  const { git, root } = repository;

  git("checkout", "--orphan", "docs-assets");
  git("rm", "-rf", ".");
  await writeAssetSet(root);
  git("add", ".");
  git("commit", "-m", "assets");
  const pinnedRef = git("rev-parse", "HEAD").stdout.trim();
  await writeFile(path.join(root, assets[0]), "<svg><title>newer unreviewed generation</title></svg>\n");
  git("add", assets[0]);
  git("commit", "-m", "newer assets");
  git("checkout", "main");
  const manifest = path.join(root, "docs-assets-test.txt");
  await writeFile(manifest, `${assets.join("\n")}\n`);

  const destination = path.join(root, "docs", "assets", "generated");
  await mkdir(destination, { recursive: true });
  await writeFile(path.join(destination, "stale.svg"), "stale\n");

  const result = sync(repository, {
    DOCS_ASSETS_MANIFEST: manifest,
    DOCS_ASSETS_REF: pinnedRef,
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  assert.deepEqual((await readdir(destination)).filter((entry) => entry.endsWith(".svg")).sort(), assets);
  assert.equal(await readFile(path.join(destination, assets[0]), "utf8"), `<svg><title>${assets[0]}</title></svg>\n`);
  const generationManifest = await readFile(path.join(destination, ".docs-assets.synced"), "utf8");
  assert.match(generationManifest, /first-workflow\.svg/);
  assert.match(generationManifest, /second-workflow\.svg/);
});

test("asset sync rejects an unsafe pinned SVG without replacing the current generation", async (t) => {
  const repository = await createDocsAssetsRepository(t);
  const { git, root } = repository;

  git("checkout", "--orphan", "docs-assets");
  git("rm", "-rf", ".");
  await writeAssetSet(root);
  await writeFile(path.join(root, assets[0]), '<svg><script>alert("unsafe")</script></svg>\n');
  git("add", ".");
  git("commit", "-m", "unsafe assets");
  const pinnedRef = git("rev-parse", "HEAD").stdout.trim();
  git("checkout", "main");
  const manifest = path.join(root, "docs-assets-test.txt");
  await writeFile(manifest, `${assets.join("\n")}\n`);

  const destination = path.join(root, "docs", "assets", "generated");
  await mkdir(destination, { recursive: true });
  await writeFile(path.join(destination, "current.svg"), "current generation\n");

  const result = sync(repository, {
    DOCS_ASSETS_MANIFEST: manifest,
    DOCS_ASSETS_REF: pinnedRef,
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /unsafe SVG/);
  assert.equal(await readFile(path.join(destination, "current.svg"), "utf8"), "current generation\n");
});

test("asset sync leaves the current generation intact when the fetched branch is incomplete", async (t) => {
  const repository = await createDocsAssetsRepository(t);
  const { git, root, scratch } = repository;
  const remote = path.join(scratch, "remote.git");

  git("checkout", "--orphan", "docs-assets");
  git("rm", "-rf", ".");
  await writeAssetSet(root, [assets[0]]);
  git("add", ".");
  git("commit", "-m", "incomplete assets");
  const pinnedRef = git("rev-parse", "HEAD").stdout.trim();
  git("checkout", "main");
  const manifest = path.join(root, "docs-assets-test.txt");
  await writeFile(manifest, `${assets.join("\n")}\n`);
  git("clone", "--bare", root, remote);
  git("remote", "add", "fixture-origin", remote);

  const destination = path.join(root, "docs", "assets", "generated");
  await mkdir(destination, { recursive: true });
  await writeFile(path.join(destination, "current.svg"), "current generation\n");

  const result = sync(repository, {
    DOCS_ASSETS_MANIFEST: manifest,
    DOCS_ASSETS_REMOTE: "fixture-origin",
    DOCS_ASSETS_REF: pinnedRef,
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /incomplete/);
  assert.equal(await readFile(path.join(destination, "current.svg"), "utf8"), "current generation\n");
});
