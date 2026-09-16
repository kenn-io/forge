import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

test("generates both clients and schema constraints from the shared YAML before building", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "forge-api-build-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const dir of [
    "bin",
    "cmd/kenn-forge-openapi",
    "internal/server",
    "frontend/openapi",
    "frontend/src/lib/api/generated",
  ]) {
    await mkdir(join(root, dir), { recursive: true });
  }
  const commands = {
    go: `#!/bin/sh
set -eu
case "$1" in
  env) echo linux ;;
  run) printf 'openapi: 3.1.0\\n' > "$4"; echo spec >> calls ;;
  generate) test -s frontend/openapi/openapi.yaml; echo go-client >> calls ;;
  build) test -s frontend/src/lib/api/generated/schema-constraints.ts; echo build >> calls ;;
  *) exit 1 ;;
esac
`,
    node: `#!/bin/sh
set -eu
case "$1" in
  frontend/scripts/generate-api-client.mjs)
    test "$2" = openapi/openapi.yaml
    test -s frontend/openapi/openapi.yaml
    rm -f frontend/src/lib/api/generated/schema-constraints.ts
    echo ts-client >> calls ;;
  scripts/generate-schema-constraints.mjs)
    test "$2" = frontend/openapi/openapi.yaml
    test -s "$2"
    echo constraints > "$3"
    echo constraints >> calls ;;
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
  assert.equal(await readFile(join(root, "calls"), "utf8"), "spec\nts-client\nconstraints\ngo-client\nbuild\n");
  const second = run();
  assert.equal(second.status, 0, second.stderr);
  assert.equal(await readFile(join(root, "calls"), "utf8"), "spec\nts-client\nconstraints\ngo-client\nbuild\nbuild\n");
});
