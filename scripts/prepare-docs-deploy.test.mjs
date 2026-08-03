import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { stageDocsDeployment } from "./prepare-docs-deploy.mjs";

test("docs deployment packages the rendered site as Vercel prebuilt output", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-deploy-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  const siteDir = path.join(root, "site");
  const outputDir = path.join(root, ".vercel", "output");
  await mkdir(path.join(siteDir, "quickstart"), { recursive: true });
  await mkdir(path.join(outputDir, "static"), { recursive: true });
  await writeFile(path.join(siteDir, "index.html"), "<h1>Forge</h1>\n");
  await writeFile(path.join(siteDir, "quickstart", "index.html"), "<h1>Quick Start</h1>\n");
  await writeFile(path.join(outputDir, "static", "stale.html"), "stale\n");

  await stageDocsDeployment(siteDir, outputDir);

  assert.equal(await readFile(path.join(outputDir, "static", "index.html"), "utf8"), "<h1>Forge</h1>\n");
  assert.equal(
    await readFile(path.join(outputDir, "static", "quickstart", "index.html"), "utf8"),
    "<h1>Quick Start</h1>\n",
  );
  await assert.rejects(readFile(path.join(outputDir, "static", "stale.html"), "utf8"), {
    code: "ENOENT",
  });

  const config = JSON.parse(await readFile(path.join(outputDir, "config.json"), "utf8"));
  assert.deepEqual(config, {
    version: 3,
    routes: [{ src: "/", dest: "/index.html" }, { handle: "filesystem" }, { src: "/(.+)/", dest: "/$1/index.html" }],
  });
});
