import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { createGitTestRepository } from "./test-git-fixture.mjs";

test("generates the OpenAPI document and Go client before building", async (t) => {
  const { root, env, git } = createGitTestRepository(t);
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
  build) printf '%s\\n' "$@" > build-args; echo build >> calls ;;
  *) exit 1 ;;
esac
`,
  };
  for (const [name, contents] of Object.entries(commands)) {
    const path = join(root, "bin", name);
    await writeFile(path, contents);
    await chmod(path, 0o755);
  }
  await writeFile(join(root, "go.mod"), "module example.invalid/build\n");
  git("add", "go.mod");
  git("commit", "--quiet", "-m", "build fixture");
  git("tag", "v1.2.3");
  const commit = git("rev-parse", "HEAD").stdout.trim();
  const script = fileURLToPath(new URL("./dev-backend-build.sh", import.meta.url));
  const run = () =>
    spawnSync("sh", [script], {
      cwd: root,
      env: { ...env, PATH: `${join(root, "bin")}:${env.PATH}` },
      encoding: "utf8",
    });
  const first = run();
  assert.equal(first.status, 0, first.stderr);
  assert.equal(await readFile(join(root, "calls"), "utf8"), "spec\nspec\ngo-client\nbuild\n");
  const buildArgs = (await readFile(join(root, "build-args"), "utf8")).split("\n");
  assert.match(
    buildArgs[2],
    /^-X main.version=v1\.2\.3 -X main.commit=[0-9a-f]{40} -X main.buildDate=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/,
  );
  assert.ok(buildArgs[2].includes(`-X main.commit=${commit}`));
  const second = run();
  assert.equal(second.status, 0, second.stderr);
  assert.equal(await readFile(join(root, "calls"), "utf8"), "spec\nspec\ngo-client\nbuild\nbuild\n");
  await writeFile(join(root, "go.mod"), "module example.invalid/changed\n");
  const dirty = run();
  assert.equal(dirty.status, 0, dirty.stderr);
  assert.match(await readFile(join(root, "build-args"), "utf8"), /-X main.version=v1\.2\.3-dirty /);
});
