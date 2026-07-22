import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { checkPlaywrightVersion } from "./check-playwright-version.mjs";

test("reads the Playwright image version from the CI Dockerfile", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "middleman-playwright-version-"));
  t.after(() => rm(root, { force: true, recursive: true }));

  for (const directory of ["frontend", "packages/github-app-ui", ".github/docker/playwright"]) {
    await mkdir(path.join(root, directory), { recursive: true });
  }

  const packageFile = JSON.stringify({
    devDependencies: { "@playwright/test": "1.61.1" },
  });
  for (const file of ["package.json", "frontend/package.json", "packages/github-app-ui/package.json"]) {
    await writeFile(path.join(root, file), packageFile);
  }
  await writeFile(
    path.join(root, ".github/docker/playwright/Dockerfile"),
    "ARG PLAYWRIGHT_BASE_IMAGE=mcr.microsoft.com/playwright@sha256:abc # v1.61.1-noble\n" +
      "FROM ${PLAYWRIGHT_BASE_IMAGE}\n",
  );

  assert.deepEqual(await checkPlaywrightVersion({ root }), []);
});
