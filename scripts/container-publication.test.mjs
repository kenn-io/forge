import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { parse } from "yaml";
import { createLauncherFixture, readCapturedArgs } from "./test/launcher-fixture.mjs";

const workflow = parse(await readFile(new URL("../.github/workflows/container.yml", import.meta.url), "utf8"));

test("stable version publication remains independent of latest promotion", () => {
  const publish = workflow.jobs.publish;
  assert.equal(publish.concurrency, undefined, "release version jobs must not cancel each other");
  assert.ok(publish.steps.some((step) => step.run?.includes("--push")));
  assert.ok(!publish.steps.some((step) => step.run?.includes("imagetools create")));
});

test("latest promotion holds one cross-release lock while rechecking the current release", () => {
  // A latest-release check alone is stale as soon as another release can promote.
  // Protect the CI scheduling boundary; the registry cannot offer compare-and-swap.
  const promote = workflow.jobs.promote;
  assert.ok(promote, "latest promotion needs its own serialized job");
  assert.equal(promote.needs, "publish");
  assert.equal(promote.concurrency["cancel-in-progress"], false);
  assert.ok(!promote.concurrency.group.includes("${{"), "all release tags must share the promotion lock");
  assert.equal(promote.permissions.packages, "write");
  assert.ok(promote.steps.some((step) => step.run?.includes("scripts/container-promote.sh")));
});

test("an older queued promotion reconciles the current stable release", async (t) => {
  const fixture = await createLauncherFixture(t, "forge-container-promotion-");
  await fixture.executable("gh", '#!/bin/sh\nprintf "%s\\n" "$LATEST_TAG"\n');
  await fixture.executable("docker", '#!/bin/sh\nprintf "%s\\n" "$@" > "$CAPTURE_PATH"\n');
  const capturePath = fixture.capturePath("docker");
  await promisify(execFile)("bash", [fileURLToPath(new URL("./container-promote.sh", import.meta.url))], {
    env: { ...process.env, PATH: `${fixture.root}:${process.env.PATH}`, LATEST_TAG: "v2.0.0", RELEASE_TAG: "v1.0.0", CAPTURE_PATH: capturePath },
  });
  assert.deepEqual(await readCapturedArgs(capturePath), [
    "buildx", "imagetools", "create", "--tag", "ghcr.io/kenn-io/forge:latest", "ghcr.io/kenn-io/forge:v2.0.0",
  ]);
});
