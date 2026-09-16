import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

test("generates the OpenAPI document and Go client before building", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "forge-api-build-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const dir of [
    "bin",
    "cmd/kenn-forge-openapi",
    "internal/server",
    "frontend/openapi",
    "internal/apiclient/health",
  ]) {
    await mkdir(join(root, dir), { recursive: true });
  }
  const commands = {
    go: `#!/bin/sh
set -eu
case "$1" in
  env) echo linux ;;
  run)
    while [ "$1" != "-out" ]; do shift; done
    printf 'openapi: 3.1.0\\n' > "$2"
    echo spec >> calls ;;
  generate) test -s frontend/openapi/openapi.yaml; echo go-client >> calls ;;
  build) echo build >> calls ;;
  *) exit 1 ;;
esac
`,
  };
  for (const [name, contents] of Object.entries(commands)) {
    const path = join(root, "bin", name);
    await writeFile(path, contents);
    await chmod(path, 0o755);
  }
  const script = fileURLToPath(new URL("./dev-backend-build.sh", import.meta.url));
  const run = () =>
    spawnSync("sh", [script], {
      cwd: root,
      env: { ...process.env, PATH: `${join(root, "bin")}:${process.env.PATH}` },
      encoding: "utf8",
    });
  const first = run();
  assert.equal(first.status, 0, first.stderr);
  assert.equal(await readFile(join(root, "calls"), "utf8"), "spec\nspec\ngo-client\nbuild\n");
  const second = run();
  assert.equal(second.status, 0, second.stderr);
  assert.equal(await readFile(join(root, "calls"), "utf8"), "spec\nspec\ngo-client\nbuild\nbuild\n");
});
