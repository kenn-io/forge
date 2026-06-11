import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import process from "node:process";

export interface RawKataResponse {
  status: number;
  body: unknown;
  text: string;
  headers: Headers;
}

export interface LiveKataHarness {
  baseURL: string;
  env: NodeJS.ProcessEnv & { KATA_HOME: string; KATA_DB: string };
  kataBranch: string;
  kataHome: string;
  workspaceRoot: string;
  get<T>(requestPath: string): Promise<T>;
  post<T>(requestPath: string, body: unknown, headers?: Record<string, string>): Promise<T>;
  put<T>(requestPath: string, body: unknown, headers?: Record<string, string>): Promise<T>;
  rawGet(requestPath: string, headers?: Record<string, string>): Promise<RawKataResponse>;
  rawPost(requestPath: string, body?: unknown, headers?: Record<string, string>): Promise<RawKataResponse>;
  seedIssue(input: SeedIssueInput): Promise<SeededKataIssue>;
  getIssue(uid: string, headers?: Record<string, string>): Promise<KataIssueDetailResponse>;
  stop(): Promise<void>;
}

export interface LiveKataHarnessOptions {
  authToken?: string | undefined;
  requiredBranch?: string | undefined;
}

export interface MiddlemanKataHome {
  home: string;
  stop(): Promise<void>;
}

export interface MiddlemanKataDaemonConfig {
  name: string;
  token?: string | undefined;
  url: string;
}

export interface SeedIssueInput {
  projectName: string;
  issueTitle: string;
  issueBody: string;
}

export interface SeededKataIssue {
  project: KataProjectSummary;
  issue: KataIssueSummary;
}

export interface KataProjectSummary {
  id: number;
  uid: string;
  name: string;
  metadata?: Record<string, unknown> | undefined;
  open_count?: number | undefined;
}

export interface KataIssueSummary {
  id: number;
  uid: string;
  project_id: number;
  project_uid: string;
  project_name?: string | undefined;
  short_id: string;
  qualified_id?: string | undefined;
  title: string;
  body?: string | undefined;
  status: "open" | "closed";
  metadata: Record<string, unknown>;
  revision: number;
  author?: string | undefined;
  created_at?: string | undefined;
  updated_at?: string | undefined;
}

export interface KataIssueDetailResponse {
  issue: KataIssueSummary;
  comments?: unknown[] | undefined;
  labels?: unknown[] | undefined;
  links?: unknown[] | undefined;
  children?: unknown[] | undefined;
}

interface RunningDaemon {
  proc: ChildProcess;
  stderr: string[];
}

const defaultRequiredBranch = "feat/server-extensions";

export async function createLiveKataHarness(options: LiveKataHarnessOptions = {}): Promise<LiveKataHarness> {
  const kataRepo = resolveKataRepo();
  const kataBranch = git(kataRepo, ["branch", "--show-current"]);
  const requiredBranch =
    options.requiredBranch ?? process.env.MIDDLEMAN_LIVE_KATA_REQUIRED_BRANCH ?? defaultRequiredBranch;
  if (requiredBranch && kataBranch !== requiredBranch) {
    throw new Error(`Kata repo must be on ${requiredBranch}; found ${kataBranch} at ${kataRepo}`);
  }

  const kataHome = await mkdtemp(path.join(tmpdir(), "middleman-kata-live-e2e-"));
  const workspaceRoot = path.join(kataHome, "workspace");
  await mkdir(workspaceRoot, { recursive: true });
  if (options.authToken) {
    await writeFile(
      path.join(kataHome, "config.toml"),
      `[auth]\ntoken = ${JSON.stringify(options.authToken)}\n`,
      "utf8",
    );
  }

  const binary = path.join(kataHome, "kata-e2e");
  try {
    run("go", ["build", "-o", binary, "./cmd/kata"], {
      cwd: kataRepo,
      env: { ...process.env },
    });
  } catch (error) {
    await rm(kataHome, { recursive: true, force: true });
    throw error;
  }

  const port = await freePort();
  const env = {
    ...process.env,
    KATA_HOME: kataHome,
    KATA_DB: path.join(kataHome, "kata.db"),
    KATA_AUTH_TOKEN: options.authToken ?? "",
  };
  assertIsolatedEnv(env);

  const daemon = startDaemon(binary, port, env);
  const baseURL = `http://127.0.0.1:${port}`;
  try {
    await waitForDaemon(baseURL, daemon);
  } catch (error) {
    await stopDaemon(daemon.proc);
    await rm(kataHome, { recursive: true, force: true });
    throw error;
  }

  let stopped = false;
  const harness: LiveKataHarness = {
    baseURL,
    env,
    kataBranch,
    kataHome,
    workspaceRoot,
    get: (requestPath) => request(baseURL, "GET", requestPath),
    post: (requestPath, body, headers) => request(baseURL, "POST", requestPath, body, headers),
    put: (requestPath, body, headers) => request(baseURL, "PUT", requestPath, body, headers),
    rawGet: (requestPath, headers) => rawRequest(baseURL, "GET", requestPath, undefined, headers),
    rawPost: (requestPath, body, headers) => rawRequest(baseURL, "POST", requestPath, body, headers),
    seedIssue: (input) => seedIssue(harness, input),
    getIssue: (uid, headers) =>
      request<KataIssueDetailResponse>(baseURL, "GET", `/api/v1/issues/${encodeURIComponent(uid)}`, undefined, headers),
    async stop() {
      if (stopped) return;
      stopped = true;
      await stopDaemon(daemon.proc);
      await rm(kataHome, { recursive: true, force: true });
    },
  };
  return harness;
}

export async function configureMiddlemanKataHome(backendURL: string, token?: string): Promise<MiddlemanKataHome> {
  return configureMiddlemanKataCatalog([{ name: "live", url: backendURL, token }], "live");
}

export async function configureMiddlemanKataCatalog(
  daemons: MiddlemanKataDaemonConfig[],
  activeDaemon: string,
): Promise<MiddlemanKataHome> {
  const home = await mkdtemp(path.join(tmpdir(), "middleman-kata-catalog-e2e-"));
  assertMiddlemanCatalogHome(home);
  await writeFile(
    path.join(home, "config.toml"),
    [
      `active_daemon = ${JSON.stringify(activeDaemon)}`,
      "",
      ...daemons.flatMap((daemon) => [
        "[[daemon]]",
        `name = ${JSON.stringify(daemon.name)}`,
        `url = ${JSON.stringify(daemon.url)}`,
        ...(daemon.token ? [`token = ${JSON.stringify(daemon.token)}`] : []),
        "",
      ]),
    ].join("\n"),
    "utf8",
  );

  const previous = process.env.KATA_HOME;
  process.env.KATA_HOME = home;
  let stopped = false;
  return {
    home,
    async stop() {
      if (stopped) return;
      stopped = true;
      if (previous === undefined) {
        delete process.env.KATA_HOME;
      } else {
        process.env.KATA_HOME = previous;
      }
      await rm(home, { recursive: true, force: true });
    },
  };
}

function assertMiddlemanCatalogHome(home: string): void {
  const resolvedHome = path.resolve(home);
  const tempRoot = path.resolve(tmpdir());
  if (
    !resolvedHome.startsWith(tempRoot + path.sep) ||
    !path.basename(resolvedHome).startsWith("middleman-kata-catalog-e2e-")
  ) {
    throw new Error(`Refusing to set middleman KATA_HOME to non-temp catalog home: ${resolvedHome}`);
  }
}

async function seedIssue(harness: LiveKataHarness, input: SeedIssueInput): Promise<SeededKataIssue> {
  const projectRoot = path.join(harness.workspaceRoot, slug(input.projectName));
  await mkdir(projectRoot, { recursive: true });
  const createdProject = await harness.post<{ project: KataProjectSummary; created: boolean }>("/api/v1/projects", {
    name: input.projectName,
    alias: {
      identity: `local://${projectRoot}`,
      kind: "local",
      root_path: projectRoot,
    },
  });

  const createdIssue = await harness.post<{ issue: KataIssueSummary; changed: boolean }>(
    `/api/v1/projects/${createdProject.project.id}/issues`,
    {
      actor: "middleman-e2e",
      title: input.issueTitle,
      body: input.issueBody,
      force_new: true,
    },
    {
      "Idempotency-Key": randomUUID(),
    },
  );

  return { project: createdProject.project, issue: createdIssue.issue };
}

function resolveKataRepo(): string {
  const candidates = [
    process.env.KATA_REPO_PATH,
    path.resolve(process.cwd(), "../kata"),
    path.join(process.env.HOME ?? "", "code/kata"),
  ].filter((candidate): candidate is string => Boolean(candidate));

  for (const candidate of candidates) {
    const result = spawnSync("git", ["-C", candidate, "rev-parse", "--show-toplevel"], {
      encoding: "utf8",
    });
    if (result.status === 0) return result.stdout.trim();
  }
  throw new Error(`Unable to locate Kata repo. Set KATA_REPO_PATH to a checkout on ${defaultRequiredBranch}.`);
}

function git(cwd: string, args: string[]): string {
  return run("git", ["-C", cwd, ...args], { cwd, env: { ...process.env } }).trim();
}

function run(command: string, args: string[], options: { cwd: string; env: NodeJS.ProcessEnv }): string {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(
      [`${command} ${args.join(" ")} failed with status ${result.status}`, result.stdout, result.stderr]
        .filter(Boolean)
        .join("\n"),
    );
  }
  return result.stdout;
}

function assertIsolatedEnv(env: NodeJS.ProcessEnv & { KATA_HOME: string; KATA_DB: string }): void {
  const realHome = process.env.HOME ? path.resolve(process.env.HOME) : undefined;
  const kataHome = path.resolve(env.KATA_HOME);
  const kataDB = path.resolve(env.KATA_DB);
  const tempRoot = path.resolve(tmpdir());

  if (!kataHome.startsWith(tempRoot + path.sep) || !path.basename(kataHome).startsWith("middleman-kata-live-e2e-")) {
    throw new Error(`Refusing to run e2e against non-temp KATA_HOME: ${kataHome}`);
  }
  if (!kataDB.startsWith(kataHome + path.sep)) {
    throw new Error(`Refusing to run e2e with KATA_DB outside KATA_HOME: ${kataDB}`);
  }
  if (realHome && kataHome.startsWith(realHome + path.sep) && !kataHome.includes(`${path.sep}tmp${path.sep}`)) {
    throw new Error(`Refusing KATA_HOME under user home: ${kataHome}`);
  }
}

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        server.close(() => reject(new Error("failed to allocate TCP port")));
        return;
      }
      const port = address.port;
      server.close(() => resolve(port));
    });
  });
}

function startDaemon(binary: string, port: number, env: NodeJS.ProcessEnv): RunningDaemon {
  const proc = spawn(binary, ["daemon", "start", "--listen", `127.0.0.1:${port}`], {
    env,
    stdio: ["ignore", "ignore", "pipe"],
  });
  const stderr: string[] = [];
  proc.stderr?.on("data", (chunk: Buffer) => {
    stderr.push(chunk.toString("utf8"));
  });
  return { proc, stderr };
}

async function waitForDaemon(baseURL: string, daemon: RunningDaemon): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (daemon.proc.exitCode !== null) {
      throw new Error(`kata daemon exited early with code ${daemon.proc.exitCode}\n${daemon.stderr.join("")}`);
    }
    try {
      const ping = await request<{ ok: boolean }>(
        baseURL,
        "GET",
        "/api/v1/ping",
        undefined,
        {},
        {
          signal: AbortSignal.timeout(500),
        },
      );
      if (ping.ok) return;
    } catch {
      // Keep polling until the daemon is ready or the deadline expires.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`kata daemon did not become ready\n${daemon.stderr.join("")}`);
}

async function request<T>(
  baseURL: string,
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE",
  requestPath: string,
  body?: unknown,
  headers: Record<string, string> = {},
  init: Pick<RequestInit, "signal"> = {},
): Promise<T> {
  const requestInit: RequestInit = {
    method,
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...headers,
    },
  };
  if (init.signal !== undefined) {
    requestInit.signal = init.signal;
  }
  if (body !== undefined) {
    requestInit.body = JSON.stringify(body);
  }
  const response = await fetch(new URL(requestPath, baseURL), {
    ...requestInit,
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`${method} ${requestPath} failed ${response.status}: ${text}`);
  }
  return text ? (JSON.parse(text) as T) : ({} as T);
}

async function rawRequest(
  baseURL: string,
  method: "GET" | "POST",
  requestPath: string,
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<RawKataResponse> {
  const requestInit: RequestInit = {
    method,
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...headers,
    },
  };
  if (body !== undefined) {
    requestInit.body = JSON.stringify(body);
  }
  const response = await fetch(new URL(requestPath, baseURL), requestInit);
  const text = await response.text();
  return {
    status: response.status,
    body: parseResponseBody(text),
    text,
    headers: response.headers,
  };
}

function parseResponseBody(text: string): unknown {
  if (!text) return {};
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

async function stopDaemon(proc: ChildProcess): Promise<void> {
  if (proc.exitCode !== null || proc.signalCode !== null) return;
  const exited = new Promise((resolve) => proc.once("exit", resolve));
  proc.kill("SIGTERM");
  await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 3_000))]);
  if (proc.exitCode === null && proc.signalCode === null) {
    const killed = new Promise((resolve) => proc.once("exit", resolve));
    proc.kill("SIGKILL");
    await Promise.race([killed, new Promise((resolve) => setTimeout(resolve, 1_000))]);
  }
}

function slug(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}
