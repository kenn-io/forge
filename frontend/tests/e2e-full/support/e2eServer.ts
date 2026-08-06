import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { access, cp, lstat, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
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
  // Present when the server was spawned with KENN_FORGE_PPROF_ADDR set
  // (the workspace-switch profiling harness does this).
  pprof_addr?: string;
};

export type IsolatedE2EServer = {
  info: E2EServerInfo;
  stop: () => Promise<void>;
};

export type IsolatedE2EServerOptions = {
  defaultPlatformHost?: string;
  fleetKey?: string;
  visibleImportedModes?: boolean;
  providerCollision?: boolean;
  preferPtyOwner?: boolean;
  // Spawn a dedicated server process and kill it on stop() instead of
  // leasing from the per-worker pool. Required when the test depends
  // on process environment the server must inherit at spawn time
  // (e.g. KATA_HOME): pooled servers were spawned earlier and never
  // see env vars the test sets afterwards.
  freshProcess?: boolean;
};

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../../..");
const serverInfoDir = mkdtempSync(path.join(os.tmpdir(), "kenn-forge-e2e-"));
const serverInfoFile = path.join(serverInfoDir, "server-info.json");
const startupTimeoutMs = 60_000;
const pollIntervalMs = 100;
const reachabilityTimeoutMs = 1_000;
const ownedServerEnvVar = "PLAYWRIGHT_E2E_SERVER_OWNED";
const frontendReadyEnvVar = "PLAYWRIGHT_E2E_FRONTEND_READY";
const serverBinaryEnvVar = "PLAYWRIGHT_E2E_SERVER_BINARY";
const serverBinaryOwnedDirEnvVar = "PLAYWRIGHT_E2E_SERVER_BINARY_OWNED_DIR";
const serverBinaryOwnerPIDEnvVar = "PLAYWRIGHT_E2E_SERVER_BINARY_OWNER_PID";
const tmuxDirEnvVar = "PLAYWRIGHT_E2E_TMUX_DIR";
const defaultPlatformHost = "github.com";
const tmuxDirPrefix = "kf-e2e-tmux-";
const tmuxBaseDir = process.platform === "win32" ? os.tmpdir() : "/tmp";
const tmuxOwnerFile = "owner.json";
const gracefulStopTimeoutMs = 15_000;

type ManagedChildLike = {
  pid?: number | undefined;
  exitCode: number | null;
  signalCode?: NodeJS.Signals | null | undefined;
};

type OwnedServerProcess = {
  child: ChildProcess;
  info: E2EServerInfo;
};

let serverPromise: Promise<E2EServerInfo> | null = null;
let managedChild: ChildProcess | null = null;
let cleanupInstalled = false;
let ownedTmuxDir: string | null = null;
let signalCleanupStarted = false;
let serverShutdownPromise: Promise<void> | null = null;
let serverBinaryPromise: Promise<string> | null = null;
let generatedServerBinaryDir: string | null = null;
const standaloneServers = new Set<OwnedServerProcess>();
const startingServers = new Set<ChildProcess>();

type E2ETmuxOwner = {
  pid: number;
};

function isLiveProcess(pid: number): boolean {
  if (!Number.isSafeInteger(pid) || pid <= 0) {
    return false;
  }
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error instanceof Error && "code" in error && error.code === "EPERM";
  }
}

function isPrivateE2ETmuxDir(dir: string): boolean {
  return path.dirname(dir) === tmuxBaseDir && path.basename(dir).startsWith(tmuxDirPrefix);
}

export function isCurrentUserOwned(stats: { uid: number }): boolean {
  return process.getuid === undefined || stats.uid === process.getuid();
}

async function isCurrentUserOwnedPath(
  candidate: string,
  matchesType: (stats: Awaited<ReturnType<typeof lstat>>) => boolean,
): Promise<boolean> {
  try {
    const stats = await lstat(candidate);
    return isCurrentUserOwned(stats) && matchesType(stats);
  } catch {
    return false;
  }
}

async function runTmuxSocketCommand(
  socket: string,
  command: "kill-server" | "list-sessions",
): Promise<{ exitCode: number | null; stderr: string }> {
  return await new Promise((resolve) => {
    const child = spawn("tmux", ["-f", "/dev/null", "-S", socket, command], {
      stdio: ["ignore", "ignore", "pipe"],
    });
    let settled = false;
    let stderr = "";
    child.stderr?.on("data", (chunk) => {
      stderr = (stderr + chunk.toString()).slice(-4_096);
    });
    const finish = (exitCode: number | null) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ exitCode, stderr });
    };
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish(null);
    }, 5_000);
    child.once("error", () => finish(null));
    child.once("exit", (code) => finish(code));
  });
}

async function killTmuxSocket(socket: string): Promise<boolean> {
  const killed = await runTmuxSocketCommand(socket, "kill-server");
  if (killed.exitCode === 0) {
    return true;
  }
  const verified = await runTmuxSocketCommand(socket, "list-sessions");
  return verified.exitCode === 1 && verified.stderr.trim().startsWith("no server running on ");
}

export async function cleanupE2ETmuxDir(dir: string, removeRoot = true): Promise<boolean> {
  if (!isPrivateE2ETmuxDir(dir)) {
    return false;
  }
  try {
    const stats = await lstat(dir);
    if (!isCurrentUserOwned(stats) || !stats.isDirectory()) {
      return false;
    }
  } catch (error) {
    return error instanceof Error && "code" in error && error.code === "ENOENT";
  }
  if (!(await isCurrentUserOwnedPath(path.join(dir, tmuxOwnerFile), (stats) => stats.isFile()))) {
    return false;
  }
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return false;
  }
  const sockets = entries.filter(
    (entry) => entry.isSocket() && entry.name.startsWith("mm-e2e-") && entry.name.endsWith(".sock"),
  );
  const stopped = await Promise.all(
    sockets.map(async (entry) => {
      const socket = path.join(dir, entry.name);
      if (!(await isCurrentUserOwnedPath(socket, (stats) => stats.isSocket()))) {
        return false;
      }
      return await killTmuxSocket(socket);
    }),
  );
  if (stopped.every(Boolean) && removeRoot) {
    await rm(dir, { force: true, recursive: true });
    return true;
  }
  return stopped.every(Boolean);
}

async function reapStaleE2ETmuxDirs(): Promise<void> {
  let entries;
  try {
    entries = await readdir(tmuxBaseDir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    if (!entry.isDirectory() || !entry.name.startsWith(tmuxDirPrefix)) {
      continue;
    }
    const dir = path.join(tmuxBaseDir, entry.name);
    if (!(await isCurrentUserOwnedPath(dir, (stats) => stats.isDirectory()))) {
      continue;
    }
    let owner: E2ETmuxOwner;
    try {
      if (!(await isCurrentUserOwnedPath(path.join(dir, tmuxOwnerFile), (stats) => stats.isFile()))) {
        continue;
      }
      const parsed: unknown = JSON.parse(await readFile(path.join(dir, tmuxOwnerFile), "utf8"));
      if (typeof parsed !== "object" || parsed === null || !("pid" in parsed) || typeof parsed.pid !== "number") {
        continue;
      }
      owner = { pid: parsed.pid };
    } catch {
      continue;
    }
    if (!isLiveProcess(owner.pid)) {
      await cleanupE2ETmuxDir(dir);
    }
  }
}

async function ensureE2ETmuxDir(): Promise<string> {
  const inherited = process.env[tmuxDirEnvVar]?.trim();
  if (inherited) {
    if (!isPrivateE2ETmuxDir(inherited) || !(await isCurrentUserOwnedPath(inherited, (stats) => stats.isDirectory()))) {
      throw new Error(`${tmuxDirEnvVar} must name a private ${tmuxDirPrefix} directory under ${tmuxBaseDir}`);
    }
    return inherited;
  }
  await reapStaleE2ETmuxDirs();
  const dir = mkdtempSync(path.join(tmuxBaseDir, tmuxDirPrefix));
  await writeFile(path.join(dir, tmuxOwnerFile), JSON.stringify({ pid: process.pid }) + "\n", { mode: 0o600 });
  process.env[tmuxDirEnvVar] = dir;
  ownedTmuxDir = dir;
  return dir;
}

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

async function buildE2EServerBinary(rootDir: string): Promise<string> {
  await ensureEmbeddedFrontend(rootDir);
  const binaryDir = mkdtempSync(path.join(os.tmpdir(), "kenn-forge-e2e-server-"));
  const binary = path.join(binaryDir, process.platform === "win32" ? "e2e-server.exe" : "e2e-server");

  try {
    await new Promise<void>((resolve, reject) => {
      const build = spawn("go", ["build", "-o", binary, "./cmd/e2e-server"], {
        cwd: rootDir,
        stdio: "inherit",
        env: process.env,
      });
      let settled = false;
      build.once("error", (error) => {
        if (settled) return;
        settled = true;
        reject(error);
      });
      build.once("exit", (code) => {
        if (settled) return;
        settled = true;
        if (code === 0) {
          resolve();
        } else {
          reject(new Error(`e2e server build failed with exit code ${code ?? "null"}`));
        }
      });
    });
  } catch (error) {
    await rm(binaryDir, { force: true, recursive: true });
    throw error;
  }

  generatedServerBinaryDir = binaryDir;
  process.env[serverBinaryEnvVar] = binary;
  process.env[serverBinaryOwnedDirEnvVar] = binaryDir;
  process.env[serverBinaryOwnerPIDEnvVar] = String(process.pid);
  installCleanup();
  return binary;
}

export async function ensureE2EServerBinary(rootDir: string = repoRoot): Promise<string> {
  const configured = process.env[serverBinaryEnvVar]?.trim();
  if (configured) {
    return configured;
  }
  if (!serverBinaryPromise) {
    const build = buildE2EServerBinary(rootDir).catch((error) => {
      if (serverBinaryPromise === build) {
        serverBinaryPromise = null;
      }
      throw error;
    });
    serverBinaryPromise = build;
  }
  return await serverBinaryPromise;
}

export async function cleanupE2ERunnerArtifacts(): Promise<void> {
  const dir = generatedServerBinaryDir ?? process.env[serverBinaryOwnedDirEnvVar]?.trim();
  if (!dir) {
    return;
  }
  if (process.env[serverBinaryOwnerPIDEnvVar] !== String(process.pid)) {
    return;
  }
  if (!generatedServerBinaryDir) {
    const binary = process.env[serverBinaryEnvVar]?.trim();
    const expectedName = process.platform === "win32" ? "e2e-server.exe" : "e2e-server";
    if (
      path.dirname(dir) !== os.tmpdir() ||
      !path.basename(dir).startsWith("kenn-forge-e2e-server-") ||
      !binary ||
      path.dirname(binary) !== dir ||
      path.basename(binary) !== expectedName ||
      !(await isCurrentUserOwnedPath(dir, (stats) => stats.isDirectory()))
    ) {
      return;
    }
  }
  await rm(dir, { force: true, recursive: true });
  if (!generatedServerBinaryDir || generatedServerBinaryDir === dir) {
    generatedServerBinaryDir = null;
    serverBinaryPromise = null;
    delete process.env[serverBinaryOwnedDirEnvVar];
    delete process.env[serverBinaryOwnerPIDEnvVar];
    if (process.env[serverBinaryEnvVar] && path.dirname(process.env[serverBinaryEnvVar]) === dir) {
      delete process.env[serverBinaryEnvVar];
    }
  }
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
  child: Pick<ManagedChildLike, "exitCode" | "signalCode">,
): Promise<E2EServerInfo> {
  const deadline = Date.now() + startupTimeoutMs;
  while (Date.now() < deadline) {
    const info = await readServerInfo(filePath);
    if (info && (await isServerReachable(info.base_url))) {
      return info;
    }
    if (childHasExited(child)) {
      const outcome = child.exitCode ?? child.signalCode ?? "unknown";
      throw new Error(`e2e server exited with ${outcome} before becoming ready from ${filePath}`);
    }
    await delay(pollIntervalMs);
  }
  throw new Error(`timed out waiting for ready e2e server from ${filePath}`);
}

async function removeServerInfo(filePath: string): Promise<void> {
  await rm(filePath, { force: true });
}

export function e2eServerCommand(
  infoFile: string,
  env: NodeJS.ProcessEnv = process.env,
): { command: string; args: string[]; prebuilt: boolean } {
  const prebuiltBinary = env[serverBinaryEnvVar]?.trim();
  if (prebuiltBinary) {
    return {
      command: prebuiltBinary,
      args: ["-port", "0", "-server-info-file", infoFile],
      prebuilt: true,
    };
  }

  return {
    command: "go",
    args: ["run", "./cmd/e2e-server", "-port", "0", "-server-info-file", infoFile],
    prebuilt: false,
  };
}

async function spawnServer(
  infoFile: string,
  options: IsolatedE2EServerOptions = {},
): Promise<{
  child: ChildProcess;
  info: E2EServerInfo;
}> {
  installCleanup();
  if (signalCleanupStarted) {
    throw new Error("e2e server shutdown started before spawn");
  }
  if (serverShutdownPromise) {
    await serverShutdownPromise;
  }
  if (signalCleanupStarted) {
    throw new Error("e2e server shutdown started before spawn");
  }
  await ensureE2ETmuxDir();
  if (signalCleanupStarted) {
    throw new Error("e2e server shutdown started before spawn");
  }
  const invocation = e2eServerCommand(infoFile);
  if (!invocation.prebuilt) {
    await ensureEmbeddedFrontend();
  }

  const args = invocation.args;
  if (options.defaultPlatformHost) {
    args.push("-default-platform-host", options.defaultPlatformHost);
  }
  if (options.fleetKey) {
    args.push("-fleet-key", options.fleetKey);
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

  const child = spawn(invocation.command, args, {
    cwd: repoRoot,
    stdio: "inherit",
    env: process.env,
  });
  startingServers.add(child);

  try {
    const info = await waitForServerInfo(infoFile, child);
    if (signalCleanupStarted) {
      await terminateServerProcess(child, info.pid);
      throw new Error("e2e server shutdown started during spawn");
    }
    return { child, info };
  } catch (error) {
    if (await terminateServerProcess(child, undefined)) {
      startingServers.delete(child);
    }
    throw error;
  }
}

function childHasExited(child: ManagedChildLike): boolean {
  return child.exitCode !== null || child.signalCode != null;
}

export function cleanupManagedServerProcess(
  child: ManagedChildLike | null = managedChild,
  infoFile: string | undefined = process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE,
): void {
  const serverPID = infoFile ? readServerInfoSync(infoFile)?.pid : undefined;
  const fallbackPID = child && !childHasExited(child) ? child.pid : undefined;
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

function waitForChildExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  if (childHasExited(child)) {
    return Promise.resolve(true);
  }
  return new Promise<boolean>((resolve) => {
    let settled = false;
    const finish = (exited: boolean) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      child.off("exit", onExit);
      resolve(exited);
    };
    const onExit = () => finish(true);
    const timer = setTimeout(() => finish(false), timeoutMs);
    child.once("exit", onExit);
  });
}

function signalServerProcess(child: ChildProcess | null, serverPID: number | undefined): boolean {
  if (child && childHasExited(child)) {
    return false;
  }
  const pid = serverPID ?? child?.pid;
  if (!pid) {
    return false;
  }
  try {
    process.kill(pid, "SIGTERM");
    return true;
  } catch {
    return false;
  }
}

export async function terminateServerProcess(
  child: ChildProcess | null,
  serverPID: number | undefined,
  alreadySignaled = false,
): Promise<boolean> {
  if (child && childHasExited(child)) {
    return true;
  }
  if (!alreadySignaled && !signalServerProcess(child, serverPID)) {
    return child !== null && childHasExited(child);
  }

  if (!child) {
    return false;
  }
  if (await waitForChildExit(child, gracefulStopTimeoutMs)) {
    return true;
  }
  const pid = serverPID ?? child.pid;
  if (pid) {
    try {
      process.kill(pid, "SIGKILL");
    } catch {
      // Process already exited.
    }
  }
  return await waitForChildExit(child, 1_000);
}

async function cleanupOwnedTmuxDir(): Promise<void> {
  if (!ownedTmuxDir) {
    return;
  }
  const dir = ownedTmuxDir;
  if (await cleanupE2ETmuxDir(dir)) {
    ownedTmuxDir = null;
    delete process.env[tmuxDirEnvVar];
  }
}

function noOwnedServersRemain(): boolean {
  return !managedChild && startingServers.size === 0 && isolatedPool.length === 0 && standaloneServers.size === 0;
}

async function cleanupOwnedTmuxDirIfIdle(): Promise<void> {
  if (noOwnedServersRemain()) {
    await cleanupOwnedTmuxDir();
  }
}

function releaseOwnedChild(child: ChildProcess): void {
  startingServers.delete(child);
  if (managedChild === child) {
    managedChild = null;
  }
  for (let index = isolatedPool.length - 1; index >= 0; index -= 1) {
    if (isolatedPool[index]?.child === child) {
      isolatedPool.splice(index, 1);
    }
  }
  for (const server of standaloneServers) {
    if (server.child === child) {
      standaloneServers.delete(server);
    }
  }
}

async function shutdownOwnedServersOnce(): Promise<void> {
  const filePath = managedChild ? process.env.PLAYWRIGHT_E2E_SERVER_INFO_FILE : undefined;
  const info = filePath ? readServerInfoSync(filePath) : null;
  const tmuxDir = ownedTmuxDir;
  const servers = new Map<ChildProcess, number | undefined>();
  for (const child of startingServers) {
    servers.set(child, undefined);
  }
  if (managedChild) {
    servers.set(managedChild, info?.pid);
  }
  for (const server of isolatedPool) {
    servers.set(server.child, server.info.pid);
  }
  for (const server of standaloneServers) {
    servers.set(server.child, server.info.pid);
  }

  for (const [child, serverPID] of servers) {
    signalServerProcess(child, serverPID);
  }
  if (tmuxDir) {
    await cleanupE2ETmuxDir(tmuxDir, false);
  }
  const stopped = await Promise.all(
    [...servers].map(async ([child, serverPID]) => ({
      child,
      exited: await terminateServerProcess(child, serverPID, true),
    })),
  );
  for (const result of stopped) {
    if (result.exited) {
      releaseOwnedChild(result.child);
    }
  }
  if (tmuxDir && noOwnedServersRemain()) {
    const removed = await cleanupE2ETmuxDir(tmuxDir);
    if (removed && ownedTmuxDir === tmuxDir) {
      ownedTmuxDir = null;
      delete process.env[tmuxDirEnvVar];
    }
  }
  if (filePath && !managedChild) {
    await removeServerInfo(filePath);
  }
}

export function shutdownOwnedServers(): Promise<void> {
  if (serverShutdownPromise) {
    return serverShutdownPromise;
  }
  const current = shutdownOwnedServersOnce().finally(() => {
    if (serverShutdownPromise === current) {
      serverShutdownPromise = null;
    }
  });
  serverShutdownPromise = current;
  return current;
}

function installCleanup(): void {
  if (cleanupInstalled) {
    return;
  }
  cleanupInstalled = true;

  const cleanupImmediately = () => {
    cleanupManagedServerProcess(managedChild);
    for (const child of startingServers) {
      signalServerProcess(child, undefined);
    }
    for (const server of isolatedPool) {
      killPooledServerProcess(server);
    }
    for (const server of standaloneServers) {
      if (!childHasExited(server.child)) {
        try {
          process.kill(server.info.pid, "SIGTERM");
        } catch {
          // Process already exited.
        }
      }
    }
    if (generatedServerBinaryDir) {
      try {
        rmSync(generatedServerBinaryDir, { force: true, recursive: true });
      } catch {
        // Best-effort synchronous cleanup during process exit.
      }
    }
  };

  process.once("exit", cleanupImmediately);
  process.on("SIGINT", () => {
    void cleanupAfterSignal(130);
  });
  process.on("SIGTERM", () => {
    void cleanupAfterSignal(143);
  });
}

async function cleanupAfterSignal(exitCode: number): Promise<void> {
  if (signalCleanupStarted) {
    return;
  }
  signalCleanupStarted = true;
  await shutdownOwnedServers();
  await cleanupE2ERunnerArtifacts();
  process.exit(exitCode);
}

async function startManagedServer(): Promise<E2EServerInfo> {
  const started = await spawnServer(serverInfoFile, { visibleImportedModes: true });
  managedChild = started.child;
  startingServers.delete(started.child);

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
        fleetKey: "",
        visibleImportedModes: true,
        providerCollision: false,
        preferPtyOwner: false,
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

  if (!managedChild) {
    await terminateServerProcess(null, (await readServerInfo(filePath))?.pid);
  }
  await shutdownOwnedServers();

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
  fleetKey: string;
  visibleImportedModes: boolean;
  providerCollision: boolean;
  preferPtyOwner: boolean;
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
  fleetKey: "",
  visibleImportedModes: false,
  providerCollision: false,
  preferPtyOwner: false,
};

// Env vars that steer a spawned e2e server's behavior. A pooled
// server only sees the env present when it was first spawned, so a
// test that mutates one of these and then takes a pooled lease gets
// order-dependent behavior. Snapshot the values at module load and
// fail fast when a pooled lease is requested after a mutation —
// such tests must pass freshProcess: true instead.
const envSensitiveServerVars = ["KATA_HOME"] as const;
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
    fleetKey: options.fleetKey ?? "",
    visibleImportedModes: options.visibleImportedModes ?? false,
    providerCollision: options.providerCollision ?? false,
    preferPtyOwner: options.preferPtyOwner ?? false,
  };
}

function samePooledOptions(a: PooledServerOptions, b: PooledServerOptions): boolean {
  return (
    a.host === b.host &&
    a.fleetKey === b.fleetKey &&
    a.visibleImportedModes === b.visibleImportedModes &&
    a.providerCollision === b.providerCollision &&
    a.preferPtyOwner === b.preferPtyOwner
  );
}

const isolatedPool: PooledServer[] = [];

function installPoolCleanup(): void {
  installCleanup();
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
        fleet_key: options.fleetKey,
        visible_imported_modes: options.visibleImportedModes,
        provider_collision: options.providerCollision,
        prefer_pty_owner: options.preferPtyOwner,
      }),
    );
  });
}

async function resetPooledServer(server: PooledServer, options: PooledServerOptions): Promise<void> {
  server.info = await postReset(server.info.base_url, options);
  server.options = options;
}

// Signal only while the spawned child is still alive: once the child
// has exited, the server's reported PID may already have been reused
// by an unrelated process, and signalling it would be unsafe.
function killPooledServerProcess(server: PooledServer): void {
  if (childHasExited(server.child)) {
    return;
  }
  try {
    process.kill(server.info.pid, "SIGTERM");
  } catch {
    // Process already exited.
  }
}

async function dropPooledServer(server: PooledServer): Promise<void> {
  if (!(await terminateServerProcess(server.child, server.info.pid))) {
    throw new Error(`e2e server ${server.info.pid} did not exit after forced termination`);
  }
  releaseOwnedChild(server.child);
}

async function spawnPooledServer(options: PooledServerOptions): Promise<PooledServer> {
  const infoDir = mkdtempSync(path.join(os.tmpdir(), "kenn-forge-e2e-"));
  const infoFile = path.join(infoDir, "server-info.json");
  const started = await spawnServer(infoFile, {
    ...(options.host === defaultPlatformHost ? {} : { defaultPlatformHost: options.host }),
    ...(options.fleetKey ? { fleetKey: options.fleetKey } : {}),
    ...(options.visibleImportedModes ? { visibleImportedModes: true } : {}),
    ...(options.providerCollision ? { providerCollision: true } : {}),
  });
  if (options.preferPtyOwner) {
    started.info = await postReset(started.info.base_url, options);
  }
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
  startingServers.delete(started.child);
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
    const infoDir = mkdtempSync(path.join(os.tmpdir(), "kenn-forge-e2e-"));
    const infoFile = path.join(infoDir, "server-info.json");
    const started = await spawnServer(infoFile, options);
    if (options.preferPtyOwner) {
      started.info = await postReset(started.info.base_url, normalizedPooledOptions(options));
    }
    standaloneServers.add(started);
    startingServers.delete(started.child);
    let stopPromise: Promise<void> | null = null;
    return {
      info: started.info,
      stop: () => {
        stopPromise ??= (async () => {
          if (!(await terminateServerProcess(started.child, started.info.pid))) {
            throw new Error(`e2e server ${started.info.pid} did not exit after forced termination`);
          }
          standaloneServers.delete(started);
          await removeServerInfo(infoFile);
          await rm(infoDir, { force: true, recursive: true });
          await cleanupOwnedTmuxDirIfIdle();
        })();
        return stopPromise;
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
      if (childHasExited(candidate.child) || !(await isServerReachable(candidate.info.base_url))) {
        throw new Error("pooled e2e server is no longer reachable");
      }
      if (!samePooledOptions(candidate.options, desired)) {
        await resetPooledServer(candidate, desired);
      }
      server = candidate;
    } catch {
      // Server crashed or its reset failed: replace it.
      await dropPooledServer(candidate);
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
