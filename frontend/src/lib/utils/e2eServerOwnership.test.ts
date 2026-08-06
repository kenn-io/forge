import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { spawn, spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, utimesSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { readFile, rm, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import * as e2eServerModule from "../../../tests/e2e-full/support/e2eServer";

const { stopE2EServer } = e2eServerModule;

const originalEnv = { ...process.env };

function writeFakeVitePlus(rootDir: string, body: string): void {
  const vitePlusBin = path.join(rootDir, "node_modules", "vite-plus", "bin");
  mkdirSync(vitePlusBin, { recursive: true });
  writeFileSync(path.join(vitePlusBin, "vp"), body, {
    flag: "wx",
  });
}

function writeFakeE2EServer(rootDir: string): string {
  const serverPath = path.join(rootDir, "fake-e2e-server");
  writeFileSync(
    serverPath,
    `#!/usr/bin/env node
const { spawnSync } = require("node:child_process");
const { existsSync, mkdirSync, writeFileSync } = require("node:fs");
const { createServer } = require("node:http");
const path = require("node:path");

const infoFlag = process.argv.indexOf("-server-info-file");
const infoFile = process.argv[infoFlag + 1];
const fleetFlag = process.argv.indexOf("-fleet-key");
const slowShutdown = fleetFlag >= 0 && process.argv[fleetFlag + 1] === "slow";
const server = createServer((_req, response) => {
  response.writeHead(200);
  response.end("ok");
});

writeFileSync(process.env.FAKE_E2E_STARTED_FILE, String(process.pid));
server.listen(0, "127.0.0.1", () => {
  const address = server.address();
  const publish = () => writeFileSync(infoFile, JSON.stringify({
    host: "127.0.0.1",
    port: address.port,
    base_url: "http://127.0.0.1:" + address.port,
    pid: process.pid,
  }));
  setTimeout(publish, Number(process.env.FAKE_E2E_READY_DELAY_MS || "0"));
});

process.on("SIGTERM", () => {
  const finish = () => server.close(() => process.exit(0));
  if (process.env.FAKE_E2E_CREATE_LATE_TMUX === "1" && slowShutdown) {
    setTimeout(() => {
      const dir = process.env.PLAYWRIGHT_E2E_TMUX_DIR;
      const rootWasPreserved = existsSync(dir) && existsSync(path.join(dir, "owner.json"));
      const result = spawnSync("tmux", [
        "-f", "/dev/null", "-S", path.join(dir, "mm-e2e-late.sock"),
        "new-session", "-d", "-s", "late", "sleep", "30",
      ]);
      writeFileSync(process.env.FAKE_E2E_LATE_MARKER, JSON.stringify({ rootWasPreserved, status: result.status }));
      finish();
    }, 100);
    return;
  }
  setTimeout(finish, slowShutdown ? 250 : 0);
});
`,
  );
  chmodSync(serverPath, 0o755);
  return serverPath;
}

async function waitForFile(filePath: string): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (!(await fileExists(filePath))) {
    if (Date.now() >= deadline) {
      throw new Error(`timed out waiting for ${filePath}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

async function fileExists(filePath: string): Promise<boolean> {
  try {
    await stat(filePath);
    return true;
  } catch {
    return false;
  }
}

afterEach(() => {
  vi.restoreAllMocks();
  process.env = { ...originalEnv };
});

describe("waitForServerInfo", () => {
  it("waits until the reported base URL accepts connections", async () => {
    const waitForServerInfo = (
      e2eServerModule as {
        waitForServerInfo?: (
          filePath: string,
          child: { exitCode: number | null },
        ) => Promise<{
          host: string;
          port: number;
          base_url: string;
          pid: number;
        }>;
      }
    ).waitForServerInfo;

    expect(waitForServerInfo).toBeTypeOf("function");
    if (!waitForServerInfo) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const infoFile = path.join(dir, "server-info.json");
    let requestCount = 0;
    const server = createServer((_req, res) => {
      requestCount += 1;
      if (requestCount === 1) {
        res.writeHead(503, { "content-type": "text/plain" });
        res.end("not ready");
        return;
      }

      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
    });

    const port = await new Promise<number>((resolve, reject) => {
      server.listen(0, "127.0.0.1", () => {
        const address = server.address();
        if (!address || typeof address === "string") {
          reject(new Error("server did not bind a TCP port"));
          return;
        }
        resolve(address.port);
      });
    });

    writeFileSync(
      infoFile,
      JSON.stringify({
        host: "127.0.0.1",
        port,
        base_url: `http://127.0.0.1:${port}`,
        pid: 99999,
      }),
    );

    const info = await waitForServerInfo(infoFile, { exitCode: null });

    expect(info.base_url).toBe(`http://127.0.0.1:${port}`);
    expect(requestCount).toBeGreaterThanOrEqual(2);

    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) {
          reject(error);
          return;
        }
        resolve();
      });
    });
  });
});

describe("ensureEmbeddedFrontend", () => {
  it("copies frontend/dist into internal/web/dist when embedded assets are missing", async () => {
    const ensureEmbeddedFrontend = (
      e2eServerModule as {
        ensureEmbeddedFrontend?: (rootDir?: string) => Promise<void>;
      }
    ).ensureEmbeddedFrontend;

    expect(ensureEmbeddedFrontend).toBeTypeOf("function");
    if (!ensureEmbeddedFrontend) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const frontendDist = path.join(dir, "frontend", "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(path.join(frontendDist, "assets"), { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    writeFileSync(path.join(frontendDist, "index.html"), "<html><body>ok</body></html>", {
      flag: "wx",
    });
    writeFileSync(path.join(frontendDist, "assets", "app.js"), "console.log('ok');", {
      flag: "wx",
    });
    writeFileSync(path.join(embeddedDist, "stub.html"), "stub", {
      flag: "wx",
    });

    await ensureEmbeddedFrontend(dir);

    await expect(readFile(path.join(embeddedDist, "index.html"), "utf8")).resolves.toContain("<body>ok</body>");
    await expect(readFile(path.join(embeddedDist, "assets", "app.js"), "utf8")).resolves.toContain("console.log");
    await expect(readFile(path.join(embeddedDist, "stub.html"), "utf8")).resolves.toBe("ok\n");
  });

  it("refreshes embedded assets when frontend/dist is newer", async () => {
    const ensureEmbeddedFrontend = (
      e2eServerModule as {
        ensureEmbeddedFrontend?: (rootDir?: string) => Promise<void>;
      }
    ).ensureEmbeddedFrontend;

    expect(ensureEmbeddedFrontend).toBeTypeOf("function");
    if (!ensureEmbeddedFrontend) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const frontendDist = path.join(dir, "frontend", "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(frontendDist, { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    const embeddedIndex = path.join(embeddedDist, "index.html");
    const frontendIndex = path.join(frontendDist, "index.html");
    writeFileSync(embeddedIndex, "<html><body>old</body></html>", {
      flag: "wx",
    });
    writeFileSync(frontendIndex, "<html><body>new</body></html>", {
      flag: "wx",
    });

    const oldTime = new Date("2026-01-01T00:00:00Z");
    const newTime = new Date("2026-01-01T00:00:10Z");
    utimesSync(embeddedIndex, oldTime, oldTime);
    utimesSync(frontendIndex, newTime, newTime);

    await ensureEmbeddedFrontend(dir);

    await expect(readFile(embeddedIndex, "utf8")).resolves.toContain("<body>new</body>");
    await expect(readFile(path.join(embeddedDist, "stub.html"), "utf8")).resolves.toBe("ok\n");
  });

  it("rebuilds frontend/dist when frontend sources are newer", async () => {
    const ensureEmbeddedFrontend = (
      e2eServerModule as {
        ensureEmbeddedFrontend?: (rootDir?: string) => Promise<void>;
      }
    ).ensureEmbeddedFrontend;

    expect(ensureEmbeddedFrontend).toBeTypeOf("function");
    if (!ensureEmbeddedFrontend) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const frontendDir = path.join(dir, "frontend");
    const frontendSrc = path.join(frontendDir, "src");
    const frontendDist = path.join(frontendDir, "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(frontendSrc, { recursive: true });
    mkdirSync(frontendDist, { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    writeFakeVitePlus(
      dir,
      "const { mkdirSync, writeFileSync } = require('node:fs');\nmkdirSync('dist', { recursive: true });\nwriteFileSync('dist/index.html', '<html><body>rebuilt</body></html>');\n",
    );

    const frontendIndex = path.join(frontendDist, "index.html");
    const embeddedIndex = path.join(embeddedDist, "index.html");
    const sourceFile = path.join(frontendSrc, "App.svelte");
    writeFileSync(frontendIndex, "<html><body>old dist</body></html>", {
      flag: "wx",
    });
    writeFileSync(embeddedIndex, "<html><body>old embed</body></html>", {
      flag: "wx",
    });
    writeFileSync(sourceFile, "<script></script>", { flag: "wx" });

    const oldTime = new Date("2026-01-01T00:00:00Z");
    const newTime = new Date("2026-01-01T00:00:10Z");
    utimesSync(frontendIndex, oldTime, oldTime);
    utimesSync(embeddedIndex, oldTime, oldTime);
    utimesSync(sourceFile, newTime, newTime);

    await ensureEmbeddedFrontend(dir);
    await expect(readFile(embeddedIndex, "utf8")).resolves.toContain("<body>rebuilt</body>");
  });

  it("throws when Vite+ runs but the build fails", async () => {
    const ensureEmbeddedFrontend = (
      e2eServerModule as {
        ensureEmbeddedFrontend?: (rootDir?: string) => Promise<void>;
      }
    ).ensureEmbeddedFrontend;

    expect(ensureEmbeddedFrontend).toBeTypeOf("function");
    if (!ensureEmbeddedFrontend) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const frontendDir = path.join(dir, "frontend");
    const frontendSrc = path.join(frontendDir, "src");
    const frontendDist = path.join(frontendDir, "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(frontendSrc, { recursive: true });
    mkdirSync(frontendDist, { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    // Fake Vite+ that fails the build (mimics a real build error). The
    // existing dist must not be silently reused in this case.
    writeFakeVitePlus(dir, "console.error('simulated build error');\nprocess.exit(1);\n");

    const frontendIndex = path.join(frontendDist, "index.html");
    const embeddedIndex = path.join(embeddedDist, "index.html");
    const sourceFile = path.join(frontendSrc, "App.svelte");
    writeFileSync(frontendIndex, "<html><body>old dist</body></html>", {
      flag: "wx",
    });
    writeFileSync(embeddedIndex, "<html><body>old embed</body></html>", {
      flag: "wx",
    });
    writeFileSync(sourceFile, "<script></script>", { flag: "wx" });

    const oldTime = new Date("2026-01-01T00:00:00Z");
    const newTime = new Date("2026-01-01T00:00:10Z");
    utimesSync(frontendIndex, oldTime, oldTime);
    utimesSync(embeddedIndex, oldTime, oldTime);
    utimesSync(sourceFile, newTime, newTime);

    await expect(ensureEmbeddedFrontend(dir)).rejects.toThrow(/frontend build failed/);
  });

  it("falls back to existing dist when Vite+ is unavailable", async () => {
    const ensureEmbeddedFrontend = (
      e2eServerModule as {
        ensureEmbeddedFrontend?: (rootDir?: string) => Promise<void>;
      }
    ).ensureEmbeddedFrontend;

    expect(ensureEmbeddedFrontend).toBeTypeOf("function");
    if (!ensureEmbeddedFrontend) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const frontendDir = path.join(dir, "frontend");
    const frontendSrc = path.join(frontendDir, "src");
    const frontendDist = path.join(frontendDir, "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(frontendSrc, { recursive: true });
    mkdirSync(frontendDist, { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    const frontendIndex = path.join(frontendDist, "index.html");
    const embeddedIndex = path.join(embeddedDist, "index.html");
    const sourceFile = path.join(frontendSrc, "App.svelte");
    writeFileSync(frontendIndex, "<html><body>existing dist</body></html>", {
      flag: "wx",
    });
    writeFileSync(sourceFile, "<script></script>", { flag: "wx" });

    const oldTime = new Date("2026-01-01T00:00:00Z");
    const newTime = new Date("2026-01-01T00:00:10Z");
    utimesSync(frontendIndex, oldTime, oldTime);
    utimesSync(sourceFile, newTime, newTime);

    // No fake Vite+ launcher exists, so the fallback path must still copy
    // the (stale) frontend dist into the embedded location.
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      await ensureEmbeddedFrontend(dir);
      await expect(readFile(embeddedIndex, "utf8")).resolves.toContain("<body>existing dist</body>");
      expect(warnSpy).toHaveBeenCalled();
    } finally {
      warnSpy.mockRestore();
    }
  });
});

describe("e2eServerCommand", () => {
  it("runs an explicitly prebuilt server binary without go run arguments", () => {
    const e2eServerCommand = (
      e2eServerModule as {
        e2eServerCommand?: (
          infoFile: string,
          env?: NodeJS.ProcessEnv,
        ) => { command: string; args: string[]; prebuilt: boolean };
      }
    ).e2eServerCommand;

    expect(e2eServerCommand).toBeTypeOf("function");
    if (!e2eServerCommand) {
      return;
    }

    expect(
      e2eServerCommand("/tmp/server-info.json", {
        PLAYWRIGHT_E2E_SERVER_BINARY: "/tmp/kenn-forge-e2e-server",
      }),
    ).toEqual({
      command: "/tmp/kenn-forge-e2e-server",
      args: ["-port", "0", "-server-info-file", "/tmp/server-info.json"],
      prebuilt: true,
    });
  });
});

describe("getReusableServerInfo", () => {
  it("accepts a reachable server even when the response is slower than the poll interval", async () => {
    const getReusableServerInfo = (
      e2eServerModule as {
        getReusableServerInfo?: (filePath: string) => Promise<{
          host: string;
          port: number;
          base_url: string;
          pid: number;
        } | null>;
      }
    ).getReusableServerInfo;

    expect(getReusableServerInfo).toBeTypeOf("function");
    if (!getReusableServerInfo) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const infoFile = path.join(dir, "server-info.json");
    const server = createServer(async (_req, res) => {
      await new Promise((resolve) => setTimeout(resolve, 150));
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
    });

    const port = await new Promise<number>((resolve, reject) => {
      server.listen(0, "127.0.0.1", () => {
        const address = server.address();
        if (!address || typeof address === "string") {
          reject(new Error("server did not bind a TCP port"));
          return;
        }
        resolve(address.port);
      });
    });

    writeFileSync(
      infoFile,
      JSON.stringify({
        host: "127.0.0.1",
        port,
        base_url: `http://127.0.0.1:${port}`,
        pid: 99999,
      }),
    );

    await expect(getReusableServerInfo(infoFile)).resolves.toEqual({
      host: "127.0.0.1",
      port,
      base_url: `http://127.0.0.1:${port}`,
      pid: 99999,
    });

    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) {
          reject(error);
          return;
        }
        resolve();
      });
    });
  });

  it("ignores stale server info when the recorded base URL is unreachable", async () => {
    const getReusableServerInfo = (
      e2eServerModule as {
        getReusableServerInfo?: (filePath: string) => Promise<{
          host: string;
          port: number;
          base_url: string;
          pid: number;
        } | null>;
      }
    ).getReusableServerInfo;

    expect(getReusableServerInfo).toBeTypeOf("function");
    if (!getReusableServerInfo) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const infoFile = path.join(dir, "server-info.json");
    const server = createServer((_req, res) => {
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
    });

    const port = await new Promise<number>((resolve, reject) => {
      server.listen(0, "127.0.0.1", () => {
        const address = server.address();
        if (!address || typeof address === "string") {
          reject(new Error("server did not bind a TCP port"));
          return;
        }
        resolve(address.port);
      });
    });

    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) {
          reject(error);
          return;
        }
        resolve();
      });
    });

    writeFileSync(
      infoFile,
      JSON.stringify({
        host: "127.0.0.1",
        port,
        base_url: `http://127.0.0.1:${port}`,
        pid: 99999,
      }),
    );

    await expect(getReusableServerInfo(infoFile)).resolves.toBeNull();
  });
});

describe("cleanupManagedServerProcess", () => {
  it("kills the real server pid from server-info instead of the wrapper pid", () => {
    const cleanupManagedServerProcess = (
      e2eServerModule as {
        cleanupManagedServerProcess?: (
          managedChild?: {
            pid?: number;
            exitCode: number | null;
          } | null,
        ) => void;
      }
    ).cleanupManagedServerProcess;

    expect(cleanupManagedServerProcess).toBeTypeOf("function");
    if (!cleanupManagedServerProcess) {
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const infoFile = path.join(dir, "server-info.json");
    writeFileSync(
      infoFile,
      JSON.stringify({
        host: "127.0.0.1",
        port: 1234,
        base_url: "http://127.0.0.1:1234",
        pid: 99999,
      }),
    );
    process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE = infoFile;

    const killSpy = vi.spyOn(process, "kill").mockImplementation(() => true);

    cleanupManagedServerProcess({ pid: 11111, exitCode: null });

    expect(killSpy).toHaveBeenCalledWith(99999, "SIGTERM");
    expect(killSpy).not.toHaveBeenCalledWith(11111, "SIGTERM");
  });
});

describe("terminateServerProcess", () => {
  it("waits for a SIGTERM-aware e2e child to finish", async () => {
    const child = spawn(
      process.execPath,
      [
        "-e",
        "process.on('SIGTERM', () => setTimeout(() => process.exit(0), 100)); " +
          "process.stdout.write('ready\\n'); setInterval(() => {}, 1000);",
      ],
      { stdio: ["ignore", "pipe", "ignore"] },
    );
    await new Promise<void>((resolve, reject) => {
      child.once("error", reject);
      child.stdout?.once("data", () => resolve());
    });

    await e2eServerModule.terminateServerProcess(child, child.pid);

    expect(child.exitCode).toBe(0);
  });

  it("does not signal a child that already exited from a signal", async () => {
    const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
      stdio: "ignore",
    });
    await new Promise<void>((resolve, reject) => {
      child.once("spawn", resolve);
      child.once("error", reject);
    });
    child.kill("SIGTERM");
    await new Promise<void>((resolve) => child.once("exit", () => resolve()));
    expect(child.signalCode).toBe("SIGTERM");

    const killSpy = vi.spyOn(process, "kill").mockImplementation(() => true);
    await e2eServerModule.terminateServerProcess(child, child.pid);

    expect(killSpy).not.toHaveBeenCalled();
  });
});

describe("private tmux ownership", () => {
  it("rejects a stale path owned by another local user", () => {
    if (process.getuid === undefined) {
      return;
    }
    expect(e2eServerModule.isCurrentUserOwned({ uid: process.getuid() + 1 })).toBe(false);
  });

  it("keeps the socket directory when tmux cleanup fails", async () => {
    if (process.platform === "win32") {
      return;
    }
    const dir = mkdtempSync("/tmp/kf-e2e-tmux-");
    const socket = path.join(dir, "mm-e2e-failed.sock");
    writeFileSync(path.join(dir, "owner.json"), JSON.stringify({ pid: 999_999 }), { mode: 0o600 });
    const socketServer = createServer();
    await new Promise<void>((resolve, reject) => {
      socketServer.once("error", reject);
      socketServer.listen(socket, () => resolve());
    });

    const binDir = mkdtempSync(path.join(os.tmpdir(), "e2e-fake-bin-"));
    const fakeTmux = path.join(binDir, "tmux");
    writeFileSync(fakeTmux, "#!/bin/sh\nexit 1\n");
    chmodSync(fakeTmux, 0o755);
    process.env.PATH = `${binDir}${path.delimiter}${process.env.PATH ?? ""}`;

    try {
      await e2eServerModule.cleanupE2ETmuxDir(dir);
      expect(await fileExists(dir)).toBe(true);
    } finally {
      await new Promise<void>((resolve) => socketServer.close(() => resolve()));
      await rm(dir, { force: true, recursive: true });
      await rm(binDir, { force: true, recursive: true });
    }
  });

  it("removes a stale socket after tmux verifies that its server is gone", async () => {
    if (process.platform === "win32") {
      return;
    }
    const dir = mkdtempSync("/tmp/kf-e2e-tmux-");
    const socket = path.join(dir, "mm-e2e-stale.sock");
    writeFileSync(path.join(dir, "owner.json"), JSON.stringify({ pid: process.pid }), { mode: 0o600 });
    const socketServer = createServer();
    await new Promise<void>((resolve, reject) => {
      socketServer.once("error", reject);
      socketServer.listen(socket, () => resolve());
    });

    const binDir = mkdtempSync(path.join(os.tmpdir(), "e2e-fake-bin-"));
    const fakeTmux = path.join(binDir, "tmux");
    writeFileSync(
      fakeTmux,
      '#!/bin/sh\ncase "$*" in *list-sessions*) echo "no server running on test socket" >&2;; esac\nexit 1\n',
    );
    chmodSync(fakeTmux, 0o755);
    process.env.PATH = `${binDir}${path.delimiter}${process.env.PATH ?? ""}`;

    try {
      await e2eServerModule.cleanupE2ETmuxDir(dir);
      expect(await fileExists(dir)).toBe(false);
    } finally {
      await new Promise<void>((resolve) => socketServer.close(() => resolve()));
      await rm(dir, { force: true, recursive: true });
      await rm(binDir, { force: true, recursive: true });
    }
  });
});

describe("owned server lifecycle", () => {
  it("keeps the shared tmux root until concurrent fresh-server stops have exited", async () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    process.env.PLAYWRIGHT_E2E_SERVER_BINARY = writeFakeE2EServer(dir);
    process.env.FAKE_E2E_STARTED_FILE = path.join(dir, "started");
    process.env.FAKE_E2E_CREATE_LATE_TMUX = "1";
    const lateMarker = path.join(dir, "late-created");
    process.env.FAKE_E2E_LATE_MARKER = lateMarker;
    delete process.env.PLAYWRIGHT_E2E_TMUX_DIR;

    const first = await e2eServerModule.startIsolatedE2EServerWithOptions({ freshProcess: true, fleetKey: "slow" });
    const second = await e2eServerModule.startIsolatedE2EServerWithOptions({ freshProcess: true });
    const tmuxDir = process.env.PLAYWRIGHT_E2E_TMUX_DIR;
    expect(tmuxDir).toBeTruthy();
    let firstStop: Promise<void> | null = null;

    try {
      firstStop = first.stop();
      await second.stop();
      expect(await fileExists(tmuxDir ?? "")).toBe(true);
      await firstStop;
      await expect(readFile(lateMarker, "utf8")).resolves.toBe(JSON.stringify({ rootWasPreserved: true, status: 0 }));
      expect(await fileExists(tmuxDir ?? "")).toBe(false);
    } finally {
      await firstStop;
      await second.stop();
      await rm(dir, { force: true, recursive: true });
    }
  });

  it("terminates a fresh server that has not published readiness", async () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const startedFile = path.join(dir, "started");
    process.env.PLAYWRIGHT_E2E_SERVER_BINARY = writeFakeE2EServer(dir);
    process.env.FAKE_E2E_STARTED_FILE = startedFile;
    process.env.FAKE_E2E_READY_DELAY_MS = "60000";
    delete process.env.PLAYWRIGHT_E2E_TMUX_DIR;

    const starting = e2eServerModule.startIsolatedE2EServerWithOptions({ freshProcess: true });
    await waitForFile(startedFile);
    const childPID = Number.parseInt(await readFile(startedFile, "utf8"), 10);

    await e2eServerModule.shutdownOwnedServers();

    await expect(starting).rejects.toThrow(/exited/);
    expect(() => process.kill(childPID, 0)).toThrow();
    expect(process.env.PLAYWRIGHT_E2E_TMUX_DIR).toBeUndefined();
    await rm(dir, { force: true, recursive: true });
  });

  it("reaps a tmux socket created while a server is shutting down", async () => {
    if (process.platform === "win32" || spawnSync("tmux", ["-V"], { stdio: "ignore" }).status !== 0) {
      return;
    }
    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    process.env.PLAYWRIGHT_E2E_SERVER_BINARY = writeFakeE2EServer(dir);
    process.env.FAKE_E2E_STARTED_FILE = path.join(dir, "started");
    process.env.FAKE_E2E_CREATE_LATE_TMUX = "1";
    const lateMarker = path.join(dir, "late-created");
    process.env.FAKE_E2E_LATE_MARKER = lateMarker;
    delete process.env.PLAYWRIGHT_E2E_TMUX_DIR;

    await e2eServerModule.startIsolatedE2EServerWithOptions({ freshProcess: true, fleetKey: "slow" });
    const tmuxDir = process.env.PLAYWRIGHT_E2E_TMUX_DIR;
    expect(tmuxDir).toBeTruthy();

    await e2eServerModule.shutdownOwnedServers();

    await expect(readFile(lateMarker, "utf8")).resolves.toBe(JSON.stringify({ rootWasPreserved: true, status: 0 }));
    expect(await fileExists(tmuxDir ?? "")).toBe(false);
    expect(process.env.PLAYWRIGHT_E2E_TMUX_DIR).toBeUndefined();
    await rm(dir, { force: true, recursive: true });
  });

  it("keeps handling SIGTERM until asynchronous server cleanup finishes", async () => {
    if (process.platform === "win32") {
      return;
    }
    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const helperFile = path.join(dir, "owner-helper.ts");
    const readyFile = path.join(dir, "owner-ready.json");
    const startedFile = path.join(dir, "server-started");
    const moduleURL = pathToFileURL(path.resolve("tests/e2e-full/support/e2eServer.ts")).href;
    writeFileSync(
      helperFile,
      `import { writeFile } from "node:fs/promises";
import process from "node:process";
import { startIsolatedE2EServerWithOptions } from ${JSON.stringify(moduleURL)};
await startIsolatedE2EServerWithOptions({ freshProcess: true, fleetKey: "slow" });
await writeFile(process.env.FAKE_OWNER_READY_FILE, JSON.stringify({ tmuxDir: process.env.PLAYWRIGHT_E2E_TMUX_DIR }));
setInterval(() => {}, 1000);
`,
    );
    const helperEnv: NodeJS.ProcessEnv = {
      ...process.env,
      PLAYWRIGHT_E2E_SERVER_BINARY: writeFakeE2EServer(dir),
      FAKE_E2E_STARTED_FILE: startedFile,
      FAKE_OWNER_READY_FILE: readyFile,
    };
    delete helperEnv.PLAYWRIGHT_E2E_TMUX_DIR;
    const owner = spawn("bun", [helperFile], { env: helperEnv, stdio: "ignore" });

    try {
      await waitForFile(readyFile);
      const { tmuxDir } = JSON.parse(await readFile(readyFile, "utf8"));
      const outcomePromise = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolve) => {
        owner.once("exit", (code, signal) => resolve({ code, signal }));
      });
      owner.kill("SIGTERM");
      await new Promise((resolve) => setTimeout(resolve, 25));
      owner.kill("SIGTERM");
      const outcome = await outcomePromise;

      expect(outcome).toEqual({ code: 143, signal: null });
      expect(await fileExists(tmuxDir)).toBe(false);
    } finally {
      if (owner.exitCode === null && owner.signalCode === null) {
        owner.kill("SIGKILL");
        await new Promise<void>((resolve) => owner.once("exit", () => resolve()));
      }
      await rm(dir, { force: true, recursive: true });
    }
  }, 10_000);
});

describe("stopE2EServer", () => {
  it("does not kill or delete externally managed server resources", async () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const infoFile = path.join(dir, "server-info.json");
    const siblingFile = path.join(dir, "keep.txt");
    writeFileSync(
      infoFile,
      JSON.stringify({
        host: "127.0.0.1",
        port: 1234,
        base_url: "http://127.0.0.1:1234",
        pid: 99999,
      }),
    );
    writeFileSync(siblingFile, "keep");

    process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE = infoFile;
    process.env.PLAYWRIGHT_E2E_BASE_URL = "http://127.0.0.1:1234";
    delete process.env.PLAYWRIGHT_E2E_SERVER_OWNED;

    const killSpy = vi.spyOn(process, "kill").mockImplementation(() => true);

    await stopE2EServer();

    expect(killSpy).not.toHaveBeenCalled();
    expect(await fileExists(infoFile)).toBe(true);
    expect(await readFile(siblingFile, "utf8")).toBe("keep");
  });

  it("only tears down resources created by this helper", async () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), "e2e-server-test-"));
    const infoFile = path.join(dir, "server-info.json");
    const siblingFile = path.join(dir, "keep.txt");
    writeFileSync(
      infoFile,
      JSON.stringify({
        host: "127.0.0.1",
        port: 1234,
        base_url: "http://127.0.0.1:1234",
        pid: 99999,
      }),
    );
    writeFileSync(siblingFile, "keep");

    process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE = infoFile;
    process.env.PLAYWRIGHT_E2E_BASE_URL = "http://127.0.0.1:1234";
    process.env.PLAYWRIGHT_E2E_SERVER_OWNED = "1";

    const killSpy = vi.spyOn(process, "kill").mockImplementation(() => true);

    await stopE2EServer();

    expect(killSpy).toHaveBeenCalledWith(99999, "SIGTERM");
    expect(await fileExists(infoFile)).toBe(false);
    expect(await readFile(siblingFile, "utf8")).toBe("keep");
    expect(process.env.PLAYWRIGHT_E2E_SERVER_OWNED).toBeUndefined();
    expect(process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE).toBeUndefined();
    expect(process.env.PLAYWRIGHT_E2E_BASE_URL).toBeUndefined();
  });
});
