import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const makefile = fileURLToPath(new URL("../Makefile", import.meta.url));
const restart = "Run kenn-forge daemon restart to use this build.";

async function fixture(t, running) {
  const root = await mkdtemp(join(tmpdir(), "forge-install-"));
  const home = join(root, "home");
  const bin = join(home, ".local/bin");
  await mkdir(bin, { recursive: true });
  t.after(async () => {
    await chmod(bin, 0o755);
    await rm(root, { recursive: true, force: true });
  });
  const forge = `#!/bin/sh\n[ "$*" = "daemon status --json" ] || exit 1\nprintf '{"running": ${running}}\\n'\n`;
  const ghapp = "#!/bin/sh\necho new-github-app\n";
  await writeFile(join(root, "kenn-forge"), forge);
  await writeFile(join(root, "kenn-forge-github-app"), ghapp);
  return {
    root, bin, forge, ghapp,
    install: () => spawnSync("make", ["-f", makefile, "-o", "build-release", "install"], {
      cwd: root,
      env: { ...process.env, HOME: home },
      encoding: "utf8",
      timeout: 30_000,
    }),
  };
}

test("replaces a running executable and asks to restart the daemon", { skip: process.platform !== "linux" }, async (t) => {
  const f = await fixture(t, true);
  const installed = join(f.bin, "kenn-forge");
  await copyFile("/bin/sh", installed);
  const child = spawn(installed, ["-c", "printf ready; read line"], { stdio: ["pipe", "pipe", "inherit"] });
  t.after(async () => {
    const exited = once(child, "exit", { signal: AbortSignal.timeout(5_000) });
    child.stdin.end("done\n");
    await exited;
  });
  await once(child.stdout, "data", { signal: AbortSignal.timeout(5_000) });
  const result = f.install();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(installed, "utf8"), f.forge);
  assert.equal(await readFile(join(f.bin, "kenn-forge-github-app"), "utf8"), f.ghapp);
  assert.ok(result.stdout.includes(restart));
});

test("installs without restart advice when no daemon is running", async (t) => {
  const f = await fixture(t, false);
  const result = f.install();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(join(f.bin, "kenn-forge"), "utf8"), f.forge);
  assert.equal(await readFile(join(f.bin, "kenn-forge-github-app"), "utf8"), f.ghapp);
  assert.ok(!result.stdout.includes(restart));
});

test("fails when the install directory is read-only", { skip: process.getuid?.() === 0 }, async (t) => {
  const f = await fixture(t, true);
  await chmod(f.bin, 0o555);
  const result = f.install();
  assert.notEqual(result.status, 0);
  assert.ok(!result.stdout.includes(restart));
});
