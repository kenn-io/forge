import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

export async function createLauncherFixture(t, prefix) {
  const root = await mkdtemp(path.join(os.tmpdir(), prefix));
  t.after(() => rm(root, { force: true, recursive: true }));

  return {
    root,
    capturePath(name) {
      return path.join(root, `${name}-args.txt`);
    },
    async executable(name, contents) {
      const executablePath = path.join(root, name);
      await writeFile(executablePath, contents);
      await chmod(executablePath, 0o700);
      return executablePath;
    },
  };
}

export async function readCapturedArgs(capturePath) {
  return (await readFile(capturePath, "utf8")).trimEnd().split("\n");
}
