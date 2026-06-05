// @vitest-environment node

import { mkdir, mkdtemp, rm, stat, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { acquireExclusiveLock, exclusiveLockPath } from "../../../tests/e2e-full/support/exclusiveLock";

async function delay(ms: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

describe("exclusive e2e lock", () => {
  const releaseFns: Array<() => Promise<void>> = [];
  const tempRoots: string[] = [];

  afterEach(async () => {
    while (releaseFns.length > 0) {
      await releaseFns.pop()?.();
    }
    while (tempRoots.length > 0) {
      await rm(tempRoots.pop() ?? "", { force: true, recursive: true });
    }
  });

  async function tempRoot(): Promise<string> {
    const root = await mkdtemp(path.join(os.tmpdir(), "middleman-lock-test-"));
    tempRoots.push(root);
    return root;
  }

  it("creates a stable lock path in the configured temp root", async () => {
    const root = await tempRoot();

    releaseFns.push(
      await acquireExclusiveLock("workspace-tmux", {
        rootDir: root,
      }),
    );

    const info = await stat(exclusiveLockPath("workspace-tmux", root));
    const rootInfo = await stat(root);

    expect(info.isDirectory()).toBe(true);
    if (process.platform !== "win32") {
      expect(rootInfo.mode & 0o777).toBe(0o700);
    }
  });

  it("waits for an existing lock file before acquiring", async () => {
    const root = await tempRoot();
    const firstRelease = await acquireExclusiveLock("workspace-tmux", {
      rootDir: root,
    });
    releaseFns.push(firstRelease);

    let secondAcquired = false;
    const secondReleasePromise = acquireExclusiveLock("workspace-tmux", {
      rootDir: root,
    }).then((release) => {
      secondAcquired = true;
      return release;
    });

    await delay(25);
    expect(secondAcquired).toBe(false);

    await firstRelease();
    releaseFns.pop();

    const secondRelease = await secondReleasePromise;
    releaseFns.push(secondRelease);
    expect(secondAcquired).toBe(true);
  });

  it("rejects a symlinked lock root", async () => {
    const target = await tempRoot();
    const root = path.join(os.tmpdir(), `middleman-lock-link-${process.pid}`);
    tempRoots.push(root);
    await rm(root, { force: true, recursive: true });
    await symlink(target, root, process.platform === "win32" ? "junction" : undefined);

    await expect(
      acquireExclusiveLock("workspace-tmux", {
        rootDir: root,
      }),
    ).rejects.toThrow("lock root is not a safe directory");
  });

  it("recovers a stale lock with a dead owner process", async () => {
    const root = await tempRoot();
    const lockPath = exclusiveLockPath("workspace-tmux", root);
    await mkdir(lockPath);
    await writeFile(
      path.join(lockPath, "metadata.json"),
      JSON.stringify({
        created_at: new Date(Date.now() - 11 * 60 * 1000).toISOString(),
        pid: 999_999_999,
      }) + "\n",
    );

    releaseFns.push(
      await acquireExclusiveLock("workspace-tmux", {
        rootDir: root,
      }),
    );

    const info = await stat(lockPath);
    expect(info.isDirectory()).toBe(true);
  });
});
