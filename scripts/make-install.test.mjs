import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { existsSync } from "node:fs";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const makefile = fileURLToPath(new URL("../Makefile", import.meta.url));
const restart = "If a daemon is running, run kenn-forge daemon restart to use this build. For a custom config, add --config PATH.";

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "forge-install-"));
  const home = join(root, "home");
  const bin = join(home, ".local/bin");
  await mkdir(bin, { recursive: true });
  t.after(() => rm(root, { recursive: true, force: true }));
  const forge = "#!/bin/sh\necho new-forge\n";
  await writeFile(join(root, "kenn-forge"), forge);
  return {
    root, bin, forge,
    install: (env = {}) => spawnSync("make", ["-f", makefile, "-o", "build-release", "install"], {
      cwd: root,
      env: { ...process.env, HOME: home, ...env },
      encoding: "utf8",
      timeout: 30_000,
    }),
  };
}

test("replaces a running executable and asks to restart the daemon", { skip: process.platform !== "linux" }, async (t) => {
  const f = await fixture(t);
  const installed = join(f.bin, "kenn-forge");
  await copyFile("/bin/sh", installed);
  const child = spawn(installed, ["-c", "printf ready; read line"]);
  t.after(async () => {
    const exited = once(child, "exit", { signal: AbortSignal.timeout(5_000) });
    child.stdin.end("done\n");
    await exited;
  });
  await once(child.stdout, "data", { signal: AbortSignal.timeout(5_000) });
  const result = f.install();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(installed, "utf8"), f.forge);
  assert.ok(result.stdout.includes(restart));
});

test("fails on a read-only install directory or a first-copy I/O error", async (t) => {
  const f = await fixture(t);
  if (process.getuid?.() !== 0) {
    await chmod(f.bin, 0o555);
    const result = f.install();
    assert.notEqual(result.status, 0);
    assert.ok(!result.stdout.includes(restart));
    await chmod(f.bin, 0o755);
  }
  await writeFile(join(f.bin, "kenn-forge"), "old forge\n");
  await writeFile(join(f.bin, "kenn-forge-github-app"), "old helper\n");
  const cp = join(f.root, "cp");
  await writeFile(cp, '#!/bin/sh\ncase "$1" in\nkenn-forge) echo "cp: input/output error" >&2; exit 1 ;;\n*) exec /bin/cp "$@" ;;\nesac\n');
  await chmod(cp, 0o755);
  const copyError = f.install({ PATH: `${f.root}:${process.env.PATH}` });
  assert.notEqual(copyError.status, 0);
  assert.ok(!copyError.stdout.includes(restart));
  assert.equal(await readFile(join(f.bin, "kenn-forge"), "utf8"), "old forge\n");
  assert.equal(await readFile(join(f.bin, "kenn-forge-github-app"), "utf8"), "old helper\n");
});

test("removes the old kenn-forge-github-app helper", async (t) => {
  const f = await fixture(t);
  await writeFile(join(f.bin, "kenn-forge-github-app"), "old helper\n");
  const result = f.install();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(join(f.bin, "kenn-forge"), "utf8"), f.forge);
  assert.ok(!existsSync(join(f.bin, "kenn-forge-github-app")));
});
