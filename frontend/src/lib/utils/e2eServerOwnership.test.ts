import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { copyFileSync, linkSync, mkdirSync, mkdtempSync, utimesSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import * as e2eServerModule from "../../../tests/e2e-full/support/e2eServer";

const { stopE2EServer } = e2eServerModule;

const originalEnv = { ...process.env };

function writeFakeBun(binDir: string, frontendDir: string, body: string, windowsBody?: string): void {
  if (process.platform === "win32") {
    const bunPath = path.join(binDir, "bun.exe");
    try {
      linkSync(process.execPath, bunPath);
    } catch {
      copyFileSync(process.execPath, bunPath);
    }
    writeFileSync(path.join(frontendDir, "run"), windowsBody ?? body, {
      flag: "wx",
    });
    return;
  }
  writeFileSync(path.join(binDir, "bun"), body, {
    mode: 0o755,
    flag: "wx",
  });
}

function prependExecutablePath(binDir: string): () => void {
  const previousPATH = process.env.PATH;
  const previousPath = process.env.Path;
  const currentPath = previousPATH ?? previousPath ?? "";
  const nextPath = `${binDir}${path.delimiter}${currentPath}`;

  process.env.PATH = nextPath;
  if (process.platform === "win32") {
    process.env.Path = nextPath;
  }

  return () => {
    if (previousPATH === undefined) {
      delete process.env.PATH;
    } else {
      process.env.PATH = previousPATH;
    }
    if (process.platform === "win32") {
      if (previousPath === undefined) {
        delete process.env.Path;
      } else {
        process.env.Path = previousPath;
      }
    }
  };
}

function clearExecutablePath(): () => void {
  const previousPATH = process.env.PATH;
  const previousPath = process.env.Path;

  process.env.PATH = "";
  if (process.platform === "win32") {
    process.env.Path = "";
  }

  return () => {
    if (previousPATH === undefined) {
      delete process.env.PATH;
    } else {
      process.env.PATH = previousPATH;
    }
    if (process.platform === "win32") {
      if (previousPath === undefined) {
        delete process.env.Path;
      } else {
        process.env.Path = previousPath;
      }
    }
  };
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
    const binDir = path.join(dir, "bin");
    const frontendDir = path.join(dir, "frontend");
    const frontendSrc = path.join(frontendDir, "src");
    const frontendDist = path.join(frontendDir, "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(binDir, { recursive: true });
    mkdirSync(frontendSrc, { recursive: true });
    mkdirSync(frontendDist, { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    writeFakeBun(
      binDir,
      frontendDir,
      "#!/usr/bin/env bash\nset -euo pipefail\nmkdir -p dist\nprintf '<html><body>rebuilt</body></html>' > dist/index.html\n",
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

    const restorePath = prependExecutablePath(binDir);
    try {
      await ensureEmbeddedFrontend(dir);
      await expect(readFile(embeddedIndex, "utf8")).resolves.toContain("<body>rebuilt</body>");
    } finally {
      restorePath();
    }
  });

  it("throws when bun runs but the build fails", async () => {
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
    const binDir = path.join(dir, "bin");
    const frontendDir = path.join(dir, "frontend");
    const frontendSrc = path.join(frontendDir, "src");
    const frontendDist = path.join(frontendDir, "dist");
    const embeddedDist = path.join(dir, "internal", "web", "dist");

    mkdirSync(binDir, { recursive: true });
    mkdirSync(frontendSrc, { recursive: true });
    mkdirSync(frontendDist, { recursive: true });
    mkdirSync(embeddedDist, { recursive: true });

    // Fake bun that fails the build (mimics a real build error). The
    // existing dist must not be silently reused in this case.
    writeFakeBun(
      binDir,
      frontendDir,
      "#!/usr/bin/env bash\necho 'simulated build error' >&2\nexit 1\n",
      "console.error('simulated build error');\nprocess.exit(1);\n",
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

    const restorePath = prependExecutablePath(binDir);
    try {
      await expect(ensureEmbeddedFrontend(dir)).rejects.toThrow(/frontend build failed/);
    } finally {
      restorePath();
    }
  });

  it("falls back to existing dist when bun is unavailable", async () => {
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

    // Empty PATH so the spawned `bun` triggers ENOENT (missing tool).
    // No embedded index exists yet, so the fallback path must still
    // copy the (stale) frontend dist into the embedded location.
    const restorePath = clearExecutablePath();
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      await ensureEmbeddedFrontend(dir);
      await expect(readFile(embeddedIndex, "utf8")).resolves.toContain("<body>existing dist</body>");
      expect(warnSpy).toHaveBeenCalled();
    } finally {
      restorePath();
    }
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
