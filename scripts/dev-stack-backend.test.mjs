import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { copyFile, mkdir } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import test from "node:test";

import { createLauncherFixture, readCapturedArgs } from "./test/launcher-fixture.mjs";

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(import.meta.dirname, "..");

test("backend dev launcher starts the explicit serve command", async (t) => {
  const fixture = await createLauncherFixture(t, "kenn-forge-dev-launcher-");
  const { root } = fixture;
  await mkdir(path.join(root, "scripts"), { recursive: true });
  await copyFile(
    path.join(repoRoot, "scripts", "dev-stack-backend.sh"),
    path.join(root, "scripts", "dev-stack-backend.sh"),
  );

  const capturePath = fixture.capturePath("air");
  const airPath = await fixture.executable(
    "fake-air.sh",
    '#!/usr/bin/env sh\nprintf "%s\\n" "$@" > "$CAPTURE_PATH"\n',
  );

  await execFileAsync("sh", ["./scripts/dev-stack-backend.sh"], {
    cwd: root,
    env: {
      ...process.env,
      AIR_BIN: airPath,
      BACKEND_ARGS: "--read-only",
      CAPTURE_PATH: capturePath,
      KENN_FORGE_CONFIG: "/tmp/kenn-forge-dev/config.toml",
    },
  });

  const args = await readCapturedArgs(capturePath);
  assert.deepEqual(args, [
    "-c",
    ".air.toml",
    "--",
    "serve",
    "-config",
    "/tmp/kenn-forge-dev/config.toml",
    "--read-only",
  ]);
});
