import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { access, cp, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

export type E2EServerInfo = {
  host: string;
  port: number;
  base_url: string;
  pid: number;
  config_path: string;
};

export type IsolatedE2EServer = {
  info: E2EServerInfo;
  stop: () => Promise<void>;
};

export type IsolatedE2EServerOptions = {
  defaultPlatformHost?: string;
  visibleImportedModes?: boolean;
  providerCollision?: boolean;
  // Spawn a dedicated server process and kill it on stop() instead of
  // leasing from the per-worker pool. Required when the test depends
  // on process environment the server must inherit at spawn time
  // (e.g. KATA_HOME): pooled servers were spawned earlier and never
  // see env vars the test sets afterwards.
  freshProcess?: boolean;
};

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../../..");
const serverInfoDir = mkdtempSync(path.join(os.tmpdir(), "middleman-e2e-"));
const serverInfoFile = path.join(serverInfoDir, "server-info.json");
const startupTimeoutMs = 60_000;
const pollIntervalMs = 100;
const reachabilityTimeoutMs = 1_000;
const ownedServerEnvVar = "PLAYWRIGHT_E2E_SERVER_OWNED";
const frontendReadyEnvVar = "PLAYWRIGHT_E2E_FRONTEND_READY";
const defaultPlatformHost = "github.com";

type ManagedChildLike = {
  pid?: number | undefined;
  exitCode: number | null;
};

let serverPromise: Promise<E2EServerInfo> | null = null;
let managedChild: ChildProcess | null = null;
let cleanupInstalled = false;

async function fileMtimeMs(filePath: string): Promise<number | null> {
  try {
    return (await stat(filePath)).mtimeMs;
  } catch {
    return null;
  }
}

async function newestMtimeUnder(dir: string): Promise<number | null> {
  const ignoredDirs = new Set([".svelte-kit", "dist", "node_modules", "playwright-report", "test-results"]);
  let newest: number | null = null;
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return null;
  }

  for (const entry of entries) {
    if (entry.isDirectory() && ignoredDirs.has(entry.name)) {
      continue;
    }
    const entryPath = path.join(dir, entry.name);
    const mtime = entry.isDirectory() ? await newestMtimeUnder(entryPath) : await fileMtimeMs(entryPath);
    if (mtime !== null && (newest === null || mtime > newest)) {
      newest = mtime;
    }
  }
  return newest;
}

async function newestFrontendSourceMtime(rootDir: string): Promise<number | null> {
  const candidates = [
    path.join(rootDir, "frontend", "src"),
    path.join(rootDir, "frontend", "index.html"),
    path.join(rootDir, "frontend", "package.json"),
    path.join(rootDir, "frontend", "vite.config.ts"),
    path.join(rootDir, "packages", "ui", "src"),
  ];
  let newest: number | null = null;
  for (const candidate of candidates) {
    const mtime = (await newestMtimeUnder(candidate)) ?? (await fileMtimeMs(candidate));
    if (mtime !== null && (newest === null || mtime > newest)) {
      newest = mtime;
    }
  }
  return newest;
}

type BuildOutcome =
  | { kind: "ok" }
  | { kind: "missing-tool"; cause: NodeJS.ErrnoException }
  | { kind: "build-failed"; exitCode: number | null };

async function tryBuildFrontend(frontendDir: string): Promise<BuildOutcome> {
  const vitePlusBin = path.resolve(frontendDir, "../node_modules/vite-plus/bin/vp");
  try {
    await access(vitePlusBin);
  } catch (err) {
    return {
      kind: "missing-tool",
      cause: err as NodeJS.ErrnoException,
    };
  }

  return await new Promise<BuildOutcome>((resolve) => {
    const build = spawn(process.execPath, [vitePlusBin, "build", "--logLevel", "warn"], {
      cwd: frontendDir,
      stdio: "inherit",
      env: process.env,
    });
    let settled = false;
    build.once("error", (err) => {
      if (settled) return;
      settled = true;
      resolve({
        kind: "missing-tool",
        cause: err as NodeJS.ErrnoException,
      });
    });
    build.once("exit", (code) => {
      if (settled) return;
      settled = true;
      if (code === 0) {
        resolve({ kind: "ok" });
      } else {
        resolve({ kind: "build-failed", exitCode: code });
      }
    });
  });
}

export async function ensureEmbeddedFrontend(rootDir: string = repoRoot): Promise<void> {
  // The Playwright config process verifies/builds the frontend once
  // before any worker starts; workers inherit the env flag and skip
  // the recursive mtime scan on every server spawn.
  if (process.env[frontendReadyEnvVar] === "1") {
    return;
  }
  const embeddedDist = path.join(rootDir, "internal", "web", "dist");
  const embeddedIndex = path.join(embeddedDist, "index.html");
  const frontendDir = path.join(rootDir, "frontend");
  const frontendDist = path.join(frontendDir, "dist");
  const frontendIndex = path.join(frontendDist, "index.html");

  let frontendMtime = await newestMtimeUnder(frontendDist);
  const sourceMtime = await newestFrontendSourceMtime(rootDir);
  if (frontendMtime === null || (sourceMtime !== null && sourceMtime > frontendMtime)) {
    const outcome = await tryBuildFrontend(frontendDir);
    if (outcome.kind === "ok") {
      frontendMtime = await newestMtimeUnder(frontendDist);
    } else if (outcome.kind === "build-failed") {
      // Real build failure (the Vite+ launcher ran but vite/svelte rejected the
      // sources). Falling back here would silently run e2e against
      // stale dist while the working tree is broken.
      throw new Error(`frontend build failed with exit code ${outcome.exitCode ?? "null"}`);
    } else if (frontendMtime === null) {
      throw new Error(
        `Vite+ is unavailable (${outcome.cause.code ?? outcome.cause.message}) ` +
          `and no existing dist at ${frontendIndex}; install frontend dependencies or ` +
          `pre-build the frontend before running e2e tests`,
      );
    } else {
      console.warn(
        `[e2e] Vite+ is unavailable (${outcome.cause.code ?? outcome.cause.message}); ` +
          `using existing ${frontendDist}`,
      );
    }
  }

  if (frontendMtime === null) {
    throw new Error(`frontend build did not produce ${frontendIndex}`);
  }

  // index.html must exist so the e2e server can serve the SPA shell, even
  // if the rest of the dist tree looks fresh.
  if ((await fileMtimeMs(embeddedIndex)) !== null) {
    const embeddedMtime = await newestMtimeUnder(embeddedDist);
    if (embeddedMtime !== null && embeddedMtime >= frontendMtime) {
      process.env[frontendReadyEnvVar] = "1";
      return;
    }
  }

  await rm(embeddedDist, { recursive: true, force: true });
  await mkdir(path.dirname(embeddedDist), { recursive: true });
  await cp(frontendDist, embeddedDist, { recursive: true });
  await writeFile(path.join(embeddedDist, "stub.html"), "ok\n");
  process.env[frontendReadyEnvVar] = "1";
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function readServerInfo(filePath: string): Promise<E2EServerInfo | null> {
  try {
    return JSON.parse(await readFile(filePath, "utf8")) as E2EServerInfo;
  } catch {
    return null;
  }
}

function readServerInfoSync(filePath: string): E2EServerInfo | null {
  try {
    return JSON.parse(readFileSync(filePath, "utf8")) as E2EServerInfo;
  } catch {
    return null;
  }
}

async function isServerReachable(baseURL: string): Promise<boolean> {
  return await new Promise<boolean>((resolve) => {
    const url = new URL(baseURL);
    const request = (url.protocol === "https:" ? httpsRequest : httpRequest)(
      url,
      { method: "GET", timeout: reachabilityTimeoutMs },
      (response) => {
        response.resume();
        resolve((response.statusCode ?? 0) >= 200 && (response.statusCode ?? 0) < 300);
      },
    );

    request.on("error", () => {
      resolve(false);
    });
    request.on("timeout", () => {
      request.destroy();
      resolve(false);
    });
    request.end();
  });
}

export async function getReusableServerInfo(filePath: string): Promise<E2EServerInfo | null> {
  const info = await readServerInfo(filePath);
  if (!info) {
    return null;
  }
  if (!(await isServerReachable(info.base_url))) {
    return null;
  }
  return info;
}

export async function waitForServerInfo(
  filePath: string,
  child: Pick<ManagedChildLike, "exitCode">,
): Promise<E2EServerInfo> {
  const deadline = Date.now() + startupTimeoutMs;
  while (Date.now() < deadline) {
    const info = await readServerInfo(filePath);
    if (info && (await isServerReachable(info.base_url))) {
      return info;
    }
    if (child.exitCode !== null) {
      throw new Error(`e2e server exited with code ${child.exitCode} before becoming ready from ${filePath}`);
    }
    await delay(pollIntervalMs);
  }
  throw new Error(`timed out waiting for ready e2e server from ${filePath}`);
}

async function removeServerInfo(filePath: string): Promise<void> {
  await rm(filePath, { force: true });
}

async function spawnServer(
  infoFile: string,
  options: IsolatedE2EServerOptions = {},
): Promise<{
  child: ChildProcess;
  info: E2EServerInfo;
}> {
  await ensureEmbeddedFrontend();

  const args = ["run", "./cmd/e2e-server", "-port", "0", "-server-info-file", infoFile];
  if (options.defaultPlatformHost) {
    args.push("-default-platform-host", options.defaultPlatformHost);
  }
  if (options.visibleImportedModes) {
    args.push("-visible-imported-modes");
  }
  if (options.providerCollision) {
    args.push("-provider-collision");
  }
  if (process.env.ROBOREV_ENDPOINT) {
    args.push("-roborev", process.env.ROBOREV_ENDPOINT);
  }

  const child = spawn("go", args, {
    cwd: repoRoot,
    stdio: "inherit",
    env: process.env,
  });

  return {
    child,
    info: await waitForServerInfo(infoFile, child),
  };
}

export function cleanupManagedServerProcess(
  child: ManagedChildLike | null = managedChild,
  infoFile: string | undefined = process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE,
): void {
  const serverPID = infoFile ? readServerInfoSync(infoFile)?.pid : undefined;
  const fallbackPID = child?.exitCode === null ? child.pid : undefined;
  const pid = serverPID ?? fallbackPID;
  if (!pid) {
    return;
  }

  try {
    process.kill(pid, "SIGTERM");
  } catch {
    // Process already exited.
  }
}

function installCleanup(infoFile: string): void {
  if (cleanupInstalled) {
    return;
  }
  cleanupInstalled = true;

  const cleanup = () => {
    cleanupManagedServerProcess(managedChild, infoFile);
  };

  process.once("exit", cleanup);
  process.once("SIGINT", () => {
    cleanup();
    process.exit(130);
  });
  process.once("SIGTERM", () => {
    cleanup();
    process.exit(143);
  });
}

async function startManagedServer(): Promise<E2EServerInfo> {
  const started = await spawnServer(serverInfoFile, { visibleImportedModes: true });
  managedChild = started.child;

  installCleanup(serverInfoFile);

  const info = started.info;
  process.env.PLAYWRIGHT_E2E_BASE_URL = info.base_url;
  process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE = serverInfoFile;
  process.env[ownedServerEnvVar] = "1";
  return info;
}

export async function ensureE2EServer(): Promise<E2EServerInfo> {
  if (serverPromise) {
    return await serverPromise;
  }

  // Inside a Playwright worker whose runner owns the shared server,
  // spawn a per-worker server instead of pointing every worker at
  // one process. Detail-page tests fire background syncs whose SSE
  // data_changed broadcasts would otherwise fan out to every other
  // worker's open pages (each one refetching lists in response),
  // and all workers would contend on a single SQLite writer and
  // git clone. Externally provided servers (the roborev runner
  // script) are still reused as-is.
  if (process.env.TEST_WORKER_INDEX !== undefined && process.env[ownedServerEnvVar] === "1") {
    serverPromise = (async () => {
      // Mirror startManagedServer's options: the shared server runs
      // with all imported app modes visible.
      const server = await spawnPooledServer({
        host: defaultPlatformHost,
        visibleImportedModes: true,
        providerCollision: false,
      });
      // Permanently leased: this is the worker's shared server; the
      // isolated-server pool must never hand it out or reset it.
      server.busy = true;
      process.env.PLAYWRIGHT_E2E_BASE_URL = server.info.base_url;
      return server.info;
    })();
    return await serverPromise;
  }

  const existingBaseURL = process.env.PLAYWRIGHT_E2E_BASE_URL;
  const existingInfoFile = process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE;
  if (existingBaseURL && existingInfoFile) {
    delete process.env[ownedServerEnvVar];
    serverPromise = (async () => {
      const info = await getReusableServerInfo(existingInfoFile);
      if (info) {
        process.env.PLAYWRIGHT_E2E_BASE_URL = info.base_url;
        process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE = existingInfoFile;
        return info;
      }

      delete process.env.PLAYWRIGHT_E2E_BASE_URL;
      delete process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE;
      return await startManagedServer();
    })();
    return await serverPromise;
  }

  serverPromise = startManagedServer();
  return await serverPromise;
}

export async function stopE2EServer(): Promise<void> {
  const filePath = process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE;
  if (!filePath) {
    return;
  }
  if (process.env[ownedServerEnvVar] !== "1") {
    return;
  }

  const info = await readServerInfo(filePath);
  if (info?.pid) {
    try {
      process.kill(info.pid, "SIGTERM");
    } catch {
      // Process already exited.
    }
  }

  await removeServerInfo(filePath);
  delete process.env[ownedServerEnvVar];
  delete process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE;
  delete process.env.PLAYWRIGHT_E2E_BASE_URL;
}

// --- Isolated server pool ---
//
// Tests that mutate server state lease an isolated server instead of
// the shared one. Leases come from a per-worker pool: the first
// lease spawns a server process; stop() fires the in-process
// /__e2e/reset (which rebuilds the seeded fixture state) and returns
// the server to the pool instead of killing the process. This keeps
// per-test isolation while paying the process spawn cost at most
// once per worker.

type PooledServerOptions = {
  host: string;
  visibleImportedModes: boolean;
  providerCollision: boolean;
};

type PooledServer = {
  child: ChildProcess;
  info: E2EServerInfo;
  busy: boolean;
  // Reset fired by stop(); the next lease awaits it before reuse.
  pending: Promise<void> | null;
  // Options the server has (or will have once `pending` resolves).
  options: PooledServerOptions;
};

const defaultPooledOptions: PooledServerOptions = {
  host: defaultPlatformHost,
  visibleImportedModes: false,
  providerCollision: false,
};

// Env vars that steer a spawned e2e server's behavior. A pooled
// server only sees the env present when it was first spawned, so a
// test that mutates one of these and then takes a pooled lease gets
// order-dependent behavior. Snapshot the values at module load and
// fail fast when a pooled lease is requested after a mutation —
// such tests must pass freshProcess: true instead.
const envSensitiveServerVars = ["KATA_HOME", "MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH"] as const;
const envSensitiveBaseline = new Map<string, string | undefined>(
  envSensitiveServerVars.map((key) => [key, process.env[key]]),
);

function assertPooledLeaseEnvUnchanged(): void {
  for (const key of envSensitiveServerVars) {
    if (process.env[key] !== envSensitiveBaseline.get(key)) {
      throw new Error(
        `${key} was changed after the worker started; a pooled e2e server cannot ` +
          `inherit it. Pass { freshProcess: true } to startIsolatedE2EServerWithOptions ` +
          `for tests that configure the server through process env.`,
      );
    }
  }
}

function normalizedPooledOptions(options: IsolatedE2EServerOptions): PooledServerOptions {
  return {
    host: options.defaultPlatformHost ?? defaultPlatformHost,
    visibleImportedModes: options.visibleImportedModes ?? false,
    providerCollision: options.providerCollision ?? false,
  };
}

function samePooledOptions(a: PooledServerOptions, b: PooledServerOptions): boolean {
  return (
    a.host === b.host &&
    a.visibleImportedModes === b.visibleImportedModes &&
    a.providerCollision === b.providerCollision
  );
}

const isolatedPool: PooledServer[] = [];
let poolCleanupInstalled = false;

function installPoolCleanup(): void {
  if (poolCleanupInstalled) {
    return;
  }
  poolCleanupInstalled = true;

  const killAll = () => {
    for (const server of isolatedPool) {
      killPooledServerProcess(server);
    }
    isolatedPool.length = 0;
  };

  process.once("exit", killAll);
  process.once("SIGINT", () => {
    killAll();
    process.exit(130);
  });
  process.once("SIGTERM", () => {
    killAll();
    process.exit(143);
  });
}

async function postReset(baseURL: string, options: PooledServerOptions): Promise<E2EServerInfo> {
  return await new Promise<E2EServerInfo>((resolve, reject) => {
    const url = new URL("/__e2e/reset", baseURL);
    const request = (url.protocol === "https:" ? httpsRequest : httpRequest)(
      url,
      {
        method: "POST",
        timeout: 60_000,
        headers: { "content-type": "application/json" },
      },
      (response) => {
        const chunks: Buffer[] = [];
        response.on("data", (chunk: Buffer) => chunks.push(chunk));
        response.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf8");
          if ((response.statusCode ?? 0) !== 200) {
            reject(new Error(`e2e reset failed with status ${response.statusCode}: ${body.trim()}`));
            return;
          }
          try {
            resolve(JSON.parse(body) as E2EServerInfo);
          } catch (error) {
            reject(error instanceof Error ? error : new Error(String(error)));
          }
        });
      },
    );
    request.on("error", reject);
    request.on("timeout", () => {
      request.destroy(new Error("e2e reset timed out"));
    });
    request.end(
      JSON.stringify({
        default_platform_host: options.host,
        visible_imported_modes: options.visibleImportedModes,
        provider_collision: options.providerCollision,
      }),
    );
  });
}

async function resetPooledServer(server: PooledServer, options: PooledServerOptions): Promise<void> {
  server.info = await postReset(server.info.base_url, options);
  server.options = options;
}

// Signal only while the spawned child is still alive: once `go run`
// has exited, the server's reported PID may already have been reused
// by an unrelated process, and signalling it would be unsafe.
function killPooledServerProcess(server: PooledServer): void {
  if (server.child.exitCode !== null) {
    return;
  }
  try {
    process.kill(server.info.pid, "SIGTERM");
  } catch {
    // Process already exited.
  }
}

function dropPooledServer(server: PooledServer): void {
  const index = isolatedPool.indexOf(server);
  if (index >= 0) {
    isolatedPool.splice(index, 1);
  }
  killPooledServerProcess(server);
}

async function spawnPooledServer(options: PooledServerOptions): Promise<PooledServer> {
  const infoDir = mkdtempSync(path.join(os.tmpdir(), "middleman-e2e-"));
  const infoFile = path.join(infoDir, "server-info.json");
  const started = await spawnServer(infoFile, {
    ...(options.host === defaultPlatformHost ? {} : { defaultPlatformHost: options.host }),
    ...(options.visibleImportedModes ? { visibleImportedModes: true } : {}),
    ...(options.providerCollision ? { providerCollision: true } : {}),
  });
  // The info is in memory now; the temp dir is no longer needed.
  await rm(infoDir, { force: true, recursive: true });

  const server: PooledServer = {
    child: started.child,
    info: started.info,
    busy: true,
    pending: null,
    options,
  };
  isolatedPool.push(server);
  installPoolCleanup();
  return server;
}

export async function startIsolatedE2EServer(): Promise<IsolatedE2EServer> {
  return startIsolatedE2EServerWithOptions();
}

export async function startIsolatedE2EServerWithOptions(
  options: IsolatedE2EServerOptions = {},
): Promise<IsolatedE2EServer> {
  if (options.freshProcess) {
    const infoDir = mkdtempSync(path.join(os.tmpdir(), "middleman-e2e-"));
    const infoFile = path.join(infoDir, "server-info.json");
    const started = await spawnServer(infoFile, options);
    return {
      info: started.info,
      stop: async () => {
        cleanupManagedServerProcess(started.child, infoFile);
        await removeServerInfo(infoFile);
        await rm(infoDir, { force: true, recursive: true });
      },
    };
  }

  assertPooledLeaseEnvUnchanged();
  const desired = normalizedPooledOptions(options);
  let server: PooledServer | null = null;

  const candidate = isolatedPool.find((pooled) => !pooled.busy);
  if (candidate) {
    candidate.busy = true;
    try {
      if (candidate.pending) {
        await candidate.pending;
        candidate.pending = null;
      }
      // The server may have died while idle (crash, OOM, external
      // kill); never hand out a dead base_url.
      if (candidate.child.exitCode !== null || !(await isServerReachable(candidate.info.base_url))) {
        throw new Error("pooled e2e server is no longer reachable");
      }
      if (!samePooledOptions(candidate.options, desired)) {
        await resetPooledServer(candidate, desired);
      }
      server = candidate;
    } catch {
      // Server crashed or its reset failed: replace it.
      dropPooledServer(candidate);
    }
  }

  if (!server) {
    server = await spawnPooledServer(desired);
  }

  const leased = server;
  let stopped = false;
  return {
    info: leased.info,
    stop: async () => {
      if (stopped) {
        return;
      }
      stopped = true;
      // stop() has release semantics, not cleanup-complete
      // semantics: it returns the server to the pool and kicks off
      // the state reset in the background so the next lease in this
      // worker finds a clean server waiting. A failed reset
      // surfaces on the next lease via `pending` (the server is
      // dropped and replaced); if no lease follows, the worker's
      // exit hook kills the process, so a swallowed failure cannot
      // leak state into another test. The extra catch avoids an
      // unhandled rejection in that no-follow-up case.
      const reset = resetPooledServer(leased, defaultPooledOptions);
      reset.catch(() => {});
      leased.pending = reset;
      leased.busy = false;
    },
  };
}

// Workspace/tmux tests lease through the same pool: every e2e server
// instance runs its own private tmux socket (see instanceTmuxCommand
// in cmd/e2e-server), so these tests no longer serialize behind a
// machine-wide lock.
export async function startIsolatedWorkspaceE2EServer(): Promise<IsolatedE2EServer> {
  return startIsolatedE2EServerWithOptions();
}

export async function startIsolatedWorkspaceE2EServerWithOptions(
  options: IsolatedE2EServerOptions = {},
): Promise<IsolatedE2EServer> {
  return startIsolatedE2EServerWithOptions(options);
}
