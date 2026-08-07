import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import path from "node:path";
import { promisify } from "node:util";
import test from "node:test";

import { createLauncherFixture, readCapturedArgs } from "./test/launcher-fixture.mjs";

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(import.meta.dirname, "..");

test("Docker backend dev entrypoint starts the explicit serve command", async (t) => {
  const fixture = await createLauncherFixture(t, "kenn-forge-docker-launcher-");
  const capturePath = fixture.capturePath("air");
  const airPath = await fixture.executable(
    "fake-air.sh",
    '#!/usr/bin/env sh\nprintf "%s\\n" "$@" > "$CAPTURE_PATH"\n',
  );
  const goPath = await fixture.executable(
    "fake-go.sh",
    '#!/usr/bin/env sh\nprintf "8123\\n"\n',
  );
  await fixture.executable(
    "socat",
    "#!/usr/bin/env sh\ntrap 'exit 0' INT TERM\nwhile sleep 0.1; do :; done\n",
  );
  const configPath = "/tmp/kenn-forge-docker/config.toml";

  await execFileAsync("sh", [path.join(repoRoot, "docker", "backend-dev-entrypoint.sh")], {
    cwd: fixture.root,
    env: {
      ...process.env,
      AIR_BIN: airPath,
      CAPTURE_PATH: capturePath,
      GO_BIN: goPath,
      KENN_FORGE_CONFIG_PATH: configPath,
      PATH: `${fixture.root}:${process.env.PATH}`,
    },
  });

  assert.deepEqual(await readCapturedArgs(capturePath), [
    "-c",
    ".air.toml",
    "--",
    "serve",
    "-config",
    configPath,
  ]);
});
