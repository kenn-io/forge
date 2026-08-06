import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

async function runPrepareEffect(statusCommand) {
  const root = await mkdtemp(join(tmpdir(), "kenn-forge-prepare-effect-"));

  try {
    await mkdir(join(root, ".repos", "effect", ".git"), { recursive: true });
    const binDir = join(root, "bin");
    const gitPath = join(binDir, "git");
    await mkdir(binDir);
    await writeFile(
      gitPath,
      `#!/usr/bin/env sh
case "$*" in
  "-C .repos/effect remote get-url origin")
    printf '%s\\n' 'https://github.com/Effect-TS/effect'
    ;;
  "-C .repos/effect rev-parse HEAD")
    printf '%s\\n' 'f4151e1937c26de14f1d64566f8126173f1b5014'
    ;;
  "-C .repos/effect status --porcelain")
    ${statusCommand}
    ;;
  *)
    printf 'Unexpected git command: %s\\n' "$*" >&2
    exit 99
    ;;
esac
`,
    );
    await chmod(gitPath, 0o755);

    return spawnSync(fileURLToPath(new URL("./prepare-effect.sh", import.meta.url)), {
      cwd: root,
      encoding: "utf8",
      env: { ...process.env, PATH: `${binDir}:${process.env.PATH ?? ""}` },
    });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

test("refuses a modified checkout already at the pinned Effect revision", async () => {
  const result = await runPrepareEffect("printf '%s\\n' ' M packages/effect/src/Effect.ts'");

  assert.equal(result.status, 1);
  assert.match(result.stderr, /Refusing to replace modified Effect checkout/);
});

test("refuses a checkout whose modification state cannot be verified", async () => {
  const result = await runPrepareEffect("printf 'simulated git status failure\\n' >&2; exit 2");

  assert.equal(result.status, 1);
  assert.match(result.stderr, /Could not verify Effect checkout/);
});
