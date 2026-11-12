import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest, SyncStatus } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { createEventsStore } from "../../stores/events.svelte.js";
import { createSyncStore } from "../../stores/sync.svelte.js";
import {
  createdWorkspaceRef,
  resetWorkspaceCreatePendingForTest,
} from "../../stores/workspace-create-pending.svelte.js";
import type { ToolingStatusValue } from "../../stores/tooling-status.svelte.js";
import type { StoreInstances } from "../../types.js";

const mocks = vi.hoisted(() => ({
  listUserRepositories: vi.fn(),
  navigate: vi.fn(),
  tooling: {
    value: {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
    } as ToolingStatusValue | undefined,
  },
}));
const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));
let observedBulkAddBodies: unknown[] = [];
let observedWorkspaceRequests: { path: string; body: unknown }[] = [];
let workspaceResponse: () => Promise<Response>;
let snapshotHosts: unknown[] = [];

vi.mock("../../api/project-intake.ts", () => ({
  listUserRepositories: mocks.listUserRepositories,
  projectIntakeFailureMessage: (failure: Error) => failure.message,
}));
vi.mock("../../stores/router.svelte.ts", () => ({
  navigate: mocks.navigate,
}));
vi.mock("../../stores/tooling-status.svelte.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/tooling-status.svelte.ts")>()),
  resolveToolingStatus: () => mocks.tooling.value,
}));
vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("onboarding test runtime is not initialized");
    return runtime;
  },
}));

import OnboardingFlow from "./OnboardingFlow.svelte";

function pullRequest(): PullRequest {
  return {
    ID: 9,
    Number: 42,
    Title: "Keep workspace activity across reloads",
    UpdatedAt: "2026-08-02T12:00:00Z",
    State: "open",
    CIStatus: "success",
    ReviewDecision: "",
    HeadBranch: "maintain-workspace-activity",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "forge",
      repo_path: "acme/forge",
    },
  } as PullRequest;
}

function configuredRepo() {
  return {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "forge",
    repo_path: "acme/forge",
    is_glob: false,
    matched_repo_count: 1,
  };
}

function storeFixture(
  options: {
    configured?: boolean;
    pulls?: PullRequest[];
    pullsError?: string | null;
    syncTrigger?: Effect.Effect<void, Error>;
  } = {},
) {
  const pulls = options.pulls ?? [pullRequest()];
  const settings = createSettingsStore();
  const setConfiguredRepos = vi.spyOn(settings, "setConfiguredRepos");
  if (options.configured) settings.setConfiguredRepos([configuredRepo()]);
  const triggerSync = vi.fn();
  const triggerSyncEffect = vi.fn(() =>
    Effect.sync(triggerSync).pipe(Effect.andThen(options.syncTrigger ?? Effect.void)),
  );
  const loadPulls = vi.fn();
  const stores = {
    settings,
    events: { subscribeWorkspaceEvents: () => () => undefined },
    sync: {
      getSyncState: () => ({
        running: false,
        last_run_at: "2026-08-02T12:01:00Z",
        last_error: "",
      }),
      triggerSync,
      triggerOnboardingSyncEffect: triggerSyncEffect,
      getRateLimits: () => ({ provider_pools: {}, local_ceilings: {} }),
      subscribeSyncComplete: () => () => {},
    },
    pulls: {
      loadPulls,
      loadPullsEffect: () => Effect.sync(loadPulls),
      getPulls: () => pulls,
      getError: () => options.pullsError ?? null,
    },
  } as unknown as StoreInstances;
  const configureExternally = () => settings.setConfiguredRepos([configuredRepo()]);
  return { stores, setConfiguredRepos, triggerSync, loadPulls, configureExternally };
}

function renderFlow(stores: StoreInstances) {
  const callbacks = {
    onStart: vi.fn(),
    onDismiss: vi.fn(),
    onComplete: vi.fn(),
  };
  const view = render(OnboardingFlow, {
    props: {
      stores,
      iconSrc: "/favicon.svg",
      ...callbacks,
    },
  });
  return { ...callbacks, unmount: view.unmount };
}

// Serves the first-sync routes and leaves every other request to the default stub.
function stubSyncRoutes(routes: {
  post?: () => Promise<Response>;
  status?: () => Promise<Response>;
  rateLimits?: () => Promise<Response>;
}) {
  const originalFetch = globalThis.fetch;
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : new Request(input, init);
    const path = new URL(request.url).pathname;
    if (routes.post && path === "/api/v1/sync" && request.method === "POST") return routes.post();
    if (routes.status && path === "/api/v1/sync/status") return routes.status();
    if (routes.rateLimits && path === "/api/v1/rate-limits") return routes.rateLimits();
    return originalFetch(input, init);
  });
}

function respond(body: unknown, status = 200) {
  return Promise.resolve(Response.json(body, { status }));
}

function problem(code: string, status: number, detail: string) {
  return respond({ code, status, title: code, detail, type: "about:blank" }, status);
}

function synced(generation: number, instance_id = "instance") {
  return { instance_id, settled_generation: generation, successful_generation: generation };
}

function testRuntime() {
  if (!runtimeCapture.current) throw new Error("test runtime is not initialized");
  return runtimeCapture.current;
}

function realSyncStore(fixture: ReturnType<typeof storeFixture>, initial?: SyncStatus) {
  const sync = createSyncStore({ runtime: testRuntime() });
  if (initial) sync.setSyncStatus(initial);
  fixture.stores.sync = sync;
  return sync;
}

describe("OnboardingFlow", () => {
  beforeEach(() => {
    runtimeCapture.current = makeAppRuntime();
    observedBulkAddBodies = [];
    observedWorkspaceRequests = [];
    workspaceResponse = async () => Response.json({ id: "ws-42", status: "provisioning" });
    snapshotHosts = [
      {
        configKey: "devbox:compute-a",
        kind: "devbox",
        operationAvailability: { workspaceWrite: { available: true } },
      },
    ];
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      const path = new URL(request.url).pathname;
      if (request.method === "GET" && path === "/api/v1/snapshot") {
        return Response.json({ hosts: snapshotHosts });
      }
      if (request.method === "POST" && path.endsWith("/workspaces")) {
        observedWorkspaceRequests.push({ path, body: await request.json() });
        return workspaceResponse();
      }
      if (request.method === "POST" && new URL(request.url).pathname.endsWith("/repos/bulk")) {
        observedBulkAddBodies.push(await request.json());
        return Response.json({ repos: [{ repo_path: "acme/forge" }] });
      }
      return Response.json({});
    });
    vi.clearAllMocks();
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
    };
    mocks.listUserRepositories.mockReturnValue(
      Effect.yieldNow.pipe(
        Effect.as([
          {
            name_with_owner: "acme/forge",
            ssh_url: "git@github.com:acme/forge.git",
            default_branch: "main",
            provider: "github",
            platform_host: "github.com",
          },
          {
            name_with_owner: "acme/docs",
            ssh_url: "git@github.com:acme/docs.git",
            default_branch: "main",
            provider: "github",
            platform_host: "github.com",
          },
        ]),
      ),
    );
  });

  afterEach(async () => {
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    resetWorkspaceCreatePendingForTest();
    vi.unstubAllGlobals();
  });

  it("runs the real repository, sync, pull, and workspace activation path", async () => {
    const fixture = storeFixture();
    const callbacks = renderFlow(fixture.stores);

    expect(callbacks.onStart).toHaveBeenCalledOnce();
    expect(screen.getByRole("heading", { name: "Connect a code forge" })).toBeTruthy();
    expect(mocks.listUserRepositories).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy(),
    );
    await waitFor(() => expect(screen.getByText("acme/forge")).toBeTruthy());

    await fireEvent.click(screen.getByText("acme/forge"));
    await fireEvent.click(screen.getByRole("button", { name: "Configure 1 repository" }));

    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    expect(observedBulkAddBodies).toEqual([
      {
        repos: [
          {
            provider: "github",
            host: "github.com",
            owner: "acme",
            name: "forge",
            repo_path: "acme/forge",
          },
        ],
      },
    ]);
    expect(fixture.setConfiguredRepos).toHaveBeenCalledOnce();
    expect(fixture.triggerSync).toHaveBeenCalledOnce();
    expect(fixture.loadPulls).toHaveBeenCalledOnce();

    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    const workspaceHeading = screen.getByRole("heading", { name: "Start your first workspace" });
    await waitFor(() => expect(document.activeElement).toBe(workspaceHeading));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(callbacks.onComplete).toHaveBeenCalledOnce());
    expect(observedWorkspaceRequests).toEqual([
      {
        path: "/api/v1/workspaces",
        body: { provider: "github", platform_host: "github.com", owner: "acme", name: "forge", mr_number: 42 },
      },
    ]);
    expect(mocks.navigate).toHaveBeenCalledWith("/terminal/ws-42");
  });

  it("creates on the saved devbox and opens its remote terminal without recording a local workspace", async () => {
    const { stores } = storeFixture({ configured: true });
    stores.settings.setWorkspaceSettings({
      ...stores.settings.getWorkspaceSettings(),
      default_execution_target: "devbox:compute-a",
    });
    const callbacks = renderFlow(stores);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    const create = screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement;
    await waitFor(() => expect(create.disabled).toBe(false));
    await fireEvent.click(create);

    await waitFor(() => expect(callbacks.onComplete).toHaveBeenCalledOnce());
    expect(observedWorkspaceRequests).toEqual([
      {
        path: "/api/v1/devboxes/compute-a/workspaces",
        body: { provider: "github", platform_host: "github.com", owner: "acme", name: "forge", mr_number: 42 },
      },
    ]);
    expect(mocks.navigate).toHaveBeenCalledWith("/terminal/fleet/devbox%3Acompute-a/ws-42");
    expect(
      createdWorkspaceRef({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "forge",
        repoPath: "acme/forge",
        number: 42,
        itemType: "pull",
      }),
    ).toBeNull();
  });

  it.each([
    ["github.com", false, "Devbox is in maintenance"],
    ["ghe.example.com", true, "Devboxes currently support only github.com repositories."],
  ])(
    "blocks devbox creation for %s (available=%s) without falling back locally",
    async (platformHost, available, reason) => {
      const pull = pullRequest();
      pull.repo.platform_host = platformHost;
      const { stores } = storeFixture({ configured: true, pulls: [pull] });
      stores.settings.setWorkspaceSettings({
        ...stores.settings.getWorkspaceSettings(),
        default_execution_target: "devbox:compute-a",
      });
      snapshotHosts = [
        {
          configKey: "devbox:compute-a",
          kind: "devbox",
          operationAvailability: { workspaceWrite: { available, unavailableReason: reason } },
        },
      ];
      const callbacks = renderFlow(stores);
      await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
      await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));

      await waitFor(() => expect(screen.getByRole("alert").textContent).toContain(reason));
      const create = screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement;
      expect(create.disabled).toBe(true);
      await fireEvent.click(create);
      expect(observedWorkspaceRequests).toEqual([]);
      expect(callbacks.onComplete).not.toHaveBeenCalled();
      expect(mocks.navigate).not.toHaveBeenCalled();
    },
  );

  it("opens an existing local workspace even when the default devbox is unavailable", async () => {
    const pull = pullRequest();
    pull.workspace = { id: "local-42", status: "ready" };
    const { stores } = storeFixture({ configured: true, pulls: [pull] });
    stores.settings.setWorkspaceSettings({
      ...stores.settings.getWorkspaceSettings(),
      default_execution_target: "devbox:compute-a",
    });
    snapshotHosts = [];
    const callbacks = renderFlow(stores);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    const open = screen.getByRole("button", { name: "Open workspace" }) as HTMLButtonElement;
    expect(open.disabled).toBe(false);
    await fireEvent.click(open);

    await waitFor(() => expect(callbacks.onComplete).toHaveBeenCalledOnce());
    expect(mocks.navigate).toHaveBeenCalledWith("/terminal/local-42");
    expect(observedWorkspaceRequests).toEqual([]);
  });

  it("verifies a newly authenticated gh session through repository discovery", async () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: false, host: "github.com" },
      glab: { available: false, authenticated: false },
    };
    renderFlow(storeFixture().stores);

    expect(screen.getByRole("heading", { name: "Connect a code forge" })).toBeTruthy();
    expect(screen.getByText("gh auth login")).toBeTruthy();
    expect(mocks.listUserRepositories).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy(),
    );
    expect(mocks.listUserRepositories).toHaveBeenCalledOnce();
  });

  it("keeps enterprise GitHub recovery on the detected host", () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: false, host: "ghe.example.com" },
      glab: { available: false, authenticated: false },
    };
    renderFlow(storeFixture().stores);

    expect(screen.getByText("ghe.example.com")).toBeTruthy();
    expect(screen.getByText("gh auth login --hostname ghe.example.com")).toBeTruthy();
  });

  it("opens repository settings for GitHub and other providers", async () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: false, authenticated: false, host: "github.com" },
      glab: { available: true, authenticated: true, host: "gitlab.example.com", user: "maintainer" },
    };
    const callbacks = renderFlow(storeFixture().stores);

    await fireEvent.click(screen.getByRole("button", { name: "Configure GitHub" }));
    expect(mocks.navigate).toHaveBeenCalledExactlyOnceWith("/settings");
    mocks.navigate.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: "Configure Forgejo" }));

    expect(callbacks.onDismiss).not.toHaveBeenCalled();
    expect(callbacks.onComplete).not.toHaveBeenCalled();
    expect(mocks.navigate).toHaveBeenCalledExactlyOnceWith("/settings");
  });

  it("discovers repositories with a verified token when gh is missing", async () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: false, authenticated: false, host: "ghe.example.com" },
      glab: { available: false, authenticated: false },
      github_token: { env: "ACME_GHE_TOKEN", source: "ACME_GHE_TOKEN", ready: true },
    };
    renderFlow(storeFixture().stores);

    expect(screen.getByText("ghe.example.com · token")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));

    await waitFor(() => expect(mocks.listUserRepositories).toHaveBeenCalledOnce());
  });

  it("resumes configured repositories at first sync without provider confirmation", async () => {
    const fixture = storeFixture({ configured: true });
    renderFlow(fixture.stores);

    await waitFor(() => expect(fixture.triggerSync).toHaveBeenCalledOnce());
    expect(screen.queryByRole("heading", { name: "Connect a code forge" })).toBeNull();
    expect(mocks.listUserRepositories).not.toHaveBeenCalled();
  });

  it("offers a retry when the sync trigger fails after onboarding starts", async () => {
    renderFlow(
      storeFixture({
        configured: true,
        syncTrigger: Effect.yieldNow.pipe(Effect.andThen(Effect.fail(new Error("sync endpoint unavailable")))),
      }).stores,
    );

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("sync endpoint unavailable");
    });
    expect(screen.getByRole("button", { name: "Retry sync" })).toBeTruthy();
  });

  it("starts sync when the initial status request remains pending", async () => {
    let attempts = 0;
    stubSyncRoutes({
      post: () => {
        attempts++;
        return respond({ instance_id: "instance", generation: 1 });
      },
      status: () => new Promise<Response>(() => {}),
    });
    const fixture = storeFixture({ configured: true });
    realSyncStore(fixture);
    renderFlow(fixture.stores);
    await waitFor(() => expect(attempts).toBe(1));
  });

  it.each(["network failure", "unknown gateway problem"])(
    "recovers a transient %s without a click",
    async (failure) => {
      let attempts = 0;
      let recoveryReads = 0;
      stubSyncRoutes({
        post: () => {
          attempts++;
          if (attempts !== 1) return respond({ instance_id: "instance", generation: 1 });
          if (failure === "network failure") return Promise.reject(new Error("response lost"));
          return problem("futureGatewayError", 503, "Unavailable");
        },
        status: () => {
          if (attempts === 1) recoveryReads++;
          return respond({
            running: false,
            last_run_at: attempts > 1 ? "2026-08-02T12:01:00Z" : "",
            last_error: "",
            full_sync: attempts > 1 ? synced(1) : synced(0),
          });
        },
        rateLimits: () => respond({ provider_pools: {}, local_ceilings: {} }),
      });
      const fixture = storeFixture({ configured: true });
      realSyncStore(fixture);
      renderFlow(fixture.stores);
      await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy(), {
        timeout: 5000,
      });
      expect(attempts).toBe(2);
      expect(recoveryReads).toBe(0);
    },
  );

  it("refreshes the pull picker after later syncs without losing the selection", async () => {
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture);
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:01:00Z" });
    vi.spyOn(sync, "triggerOnboardingSyncEffect").mockReturnValue(Effect.void);
    const first = pullRequest();
    const second = { ...first, ID: 10, Number: 43, Title: "Deferred repository pull" };
    let pulls = [first];
    vi.spyOn(fixture.stores.pulls, "getPulls").mockImplementation(() => pulls);
    const pending = Promise.withResolvers<void>();
    const load = vi.spyOn(fixture.stores.pulls, "loadPullsEffect");
    const view = renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("button", { name: "Continue with PR #42" })).toBeTruthy(), {
      timeout: 5000,
    });
    load.mockImplementationOnce(() => Effect.promise(() => pending.promise));
    sync.setSyncStatus({ running: true, last_run_at: "2026-08-02T12:01:00Z" });
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:02:00Z" });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "Continue with PR #42" })).toBeTruthy();
    pulls = [second, first];
    pending.resolve();
    await waitFor(() => expect(screen.getByText("Deferred repository pull")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Continue with PR #42" })).toBeTruthy();
    load.mockImplementationOnce(() => Effect.fail(new Error("load failed")));
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:02:00.500Z" });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(3));
    expect(screen.getByText("Deferred repository pull")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Continue with PR #42" })).toBeTruthy();
    view.unmount();
    sync.setSyncStatus({ running: true });
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:04:00Z" });
    expect(load).toHaveBeenCalledTimes(3);
  });

  it("shows pulls after a failed initial load recovers in the background", async () => {
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture);
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:01:00Z" });
    vi.spyOn(sync, "triggerOnboardingSyncEffect").mockReturnValue(Effect.void);
    const load = vi.spyOn(fixture.stores.pulls, "loadPullsEffect");
    load.mockImplementationOnce(() => Effect.fail(new Error("Could not load pull requests")));
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Could not load pull requests"));
    sync.setSyncStatus({ running: true, last_run_at: "2026-08-02T12:01:00Z" });
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:02:00Z" });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByText("Keep workspace activity across reloads")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Continue with PR #42" })).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps the chosen pull when leaving during a background reload", async () => {
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture);
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:01:00Z" });
    vi.spyOn(sync, "triggerOnboardingSyncEffect").mockReturnValue(Effect.void);
    const pending = Promise.withResolvers<void>();
    let pulls = [pullRequest()];
    vi.spyOn(fixture.stores.pulls, "getPulls").mockImplementation(() => pulls);
    const load = vi.spyOn(fixture.stores.pulls, "loadPullsEffect");
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("button", { name: "Continue with PR #42" })).toBeTruthy());
    load.mockImplementationOnce(() => Effect.promise(() => pending.promise));
    sync.setSyncStatus({ running: true, last_run_at: "2026-08-02T12:01:00Z" });
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:02:00Z" });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    pulls = [{ ...pullRequest(), ID: 10, Number: 43, Title: "New pull" }];
    pending.resolve();
    await tick();
    await waitFor(() => expect(screen.getByText("acme/forge · PR #42")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy();
  });

  it("refreshes pulls when another sync completes during the initial load", async () => {
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture);
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:01:00Z" });
    vi.spyOn(sync, "triggerOnboardingSyncEffect").mockReturnValue(Effect.void);
    const pending = Promise.withResolvers<void>();
    let pulls = [pullRequest()];
    vi.spyOn(fixture.stores.pulls, "getPulls").mockImplementation(() => pulls);
    const load = vi.spyOn(fixture.stores.pulls, "loadPullsEffect");
    load.mockImplementationOnce(() => Effect.promise(() => pending.promise));
    load.mockImplementation(() =>
      Effect.sync(() => {
        pulls = [{ ...pullRequest(), ID: 10, Number: 43, Title: "New pull" }];
      }),
    );
    renderFlow(fixture.stores);
    await waitFor(() => expect(load).toHaveBeenCalledOnce());
    sync.setSyncStatus({ running: true, last_run_at: "2026-08-02T12:01:00Z" });
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:02:00Z" });
    pending.resolve();
    await waitFor(() => expect(screen.getByText("New pull")).toBeTruthy());
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("shows the credential action when the accepted sync cannot route a repository", async () => {
    let posts = 0;
    stubSyncRoutes({
      post: () => {
        posts++;
        return respond({ instance_id: "instance", generation: 1 });
      },
      status: () =>
        respond({
          running: false,
          full_sync: {
            ...synced(0),
            settled_generation: 1,
            last_error:
              "no GitHub credential route for missing/repo on github.com. Configure a GitHub token for this repository or owner in Forge's config.toml and restart Forge.",
          },
        }),
    });
    const fixture = storeFixture({ configured: true });
    realSyncStore(fixture);
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("config.toml and restart Forge"));
    expect(screen.getByRole("alert").textContent).toContain("missing/repo on github.com");
    expect(screen.getByRole("button", { name: "Retry sync" })).toBeTruthy();
    expect(posts).toBe(1);
  });

  it("opens pull requests when the post-completion quota refresh stalls", async () => {
    stubSyncRoutes({
      post: () => respond({ instance_id: "instance", generation: 1 }),
      status: () => respond({ running: false, full_sync: synced(1) }),
      rateLimits: () => new Promise<Response>(() => {}),
    });
    const fixture = storeFixture({ configured: true });
    realSyncStore(fixture);
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    expect(fixture.loadPulls).toHaveBeenCalledOnce();
  });

  it("does not report a quota wait for a different host", async () => {
    const fixture = storeFixture({ configured: true, pulls: [] });
    const reset = new Date(Date.now() + 3600000).toISOString();
    vi.spyOn(fixture.stores.sync, "getRateLimits").mockReturnValue({
      local_ceilings: {},
      provider_pools: {
        pool: {
          provider: "github",
          platform_host: "github.example.com",
          principal_label: "account",
          rate_principal: "user:1",
          reserve_buffer: 200,
          sync_paused: false,
          sync_throttle_factor: 1,
          rest: { known: true, remaining: 100, limit: 5000, requests: 0, reset_at: reset },
          graphql: { known: true, remaining: 5000, limit: 5000, requests: 0, reset_at: reset },
        },
      },
    });
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByText("No open pull requests yet")).toBeTruthy());
    expect(screen.queryByText(/Quota for account/)).toBeNull();
  });

  it("offers retry when accepted sync status is forbidden", async () => {
    let reads = 0;
    let posts = 0;
    stubSyncRoutes({
      post: () => {
        posts++;
        return respond({ instance_id: "instance", generation: 1 });
      },
      status: () => {
        reads++;
        return problem("forbidden", 403, "Access denied");
      },
    });
    const fixture = storeFixture({ configured: true });
    realSyncStore(fixture);
    renderFlow(fixture.stores);
    await waitFor(() => expect(reads).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry sync" })).toBeTruthy());
    expect(screen.getByRole("alert").textContent).toContain("Access denied");
    expect(posts).toBe(1);
  });

  it.each(["setup", "configuration reload"])("waits for a retry after %s", async (source) => {
    const accepted = Promise.withResolvers<Response>();
    let attempts = 0;
    let statusReads = 0;
    stubSyncRoutes({
      post: () => {
        attempts++;
        return attempts === 1 ? Promise.reject(new Error("response lost")) : accepted.promise;
      },
      status: () => {
        statusReads++;
        return respond({ running: true, last_run_at: "2026-08-02T12:00:30Z", last_error: "" });
      },
    });
    const fixture = storeFixture();
    const sync = realSyncStore(fixture, { running: true, last_run_at: "2026-08-02T12:00:00Z", last_error: "" });
    renderFlow(fixture.stores);
    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() => expect(screen.getByText("acme/forge")).toBeTruthy());
    if (source === "setup") {
      await fireEvent.click(screen.getByText("acme/forge"));
      await fireEvent.click(screen.getByRole("button", { name: "Configure 1 repository" }));
    } else {
      fixture.configureExternally();
    }
    await waitFor(() => expect(attempts).toBe(1));
    if (source === "setup") expect(observedBulkAddBodies).toHaveLength(1);
    // Model delayed delivery of a completion published before the repository update.
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:00:30Z", last_error: "" });
    await tick();
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
    await waitFor(() => expect(attempts).toBe(2));
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
    accepted.resolve(Response.json({ instance_id: "sync-instance", generation: 2 }));
    await waitFor(() => expect(statusReads).toBe(1));
    sync.setSyncStatus({
      running: false,
      last_run_at: "2026-08-02T12:01:00Z",
      last_error: "",
      full_sync: synced(2, "sync-instance"),
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
  });

  it.each(["stale", "failed"])("does not advance after retry when the status refresh is %s", async (refresh) => {
    const accepted = Promise.withResolvers<Response>();
    let attempts = 0;
    let statusReads = 0;
    stubSyncRoutes({
      post: () => {
        attempts++;
        return attempts === 1 ? Promise.reject(new Error("response lost")) : accepted.promise;
      },
      status: () => {
        statusReads++;
        return refresh === "stale"
          ? respond({ running: false, last_run_at: "2026-08-02T12:00:30Z", last_error: "" })
          : problem("hubUnavailable", 503, "Unavailable");
      },
    });
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture, { running: true, last_run_at: "2026-08-02T12:00:00Z", last_error: "" });
    renderFlow(fixture.stores);
    await waitFor(() => expect(attempts).toBe(1));
    sync.setSyncStatus({ running: false, last_run_at: "2026-08-02T12:00:30Z", last_error: "" });
    await waitFor(() => expect(attempts).toBe(2));
    expect(sync.getSyncState()).toMatchObject({ running: true, last_error: "" });
    accepted.resolve(Response.json({ instance_id: "sync-instance", generation: 2 }));
    await waitFor(() => expect(statusReads).toBeGreaterThan(0));
    await tick();
    expect(fixture.loadPulls).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
    sync.setSyncStatus({ running: true, last_run_at: "2026-08-02T12:00:30Z", last_error: "" });
    sync.setSyncStatus({
      running: false,
      last_run_at: "2026-08-02T12:01:00Z",
      last_error: "",
      full_sync: synced(2, "sync-instance"),
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
  });

  it("waits when an older sync event arrives during a retried request and refresh fails", async () => {
    let source: EventTarget | undefined;
    class EventSourceStub extends EventTarget {
      constructor(_url: string) {
        super();
        source = this;
      }
      close() {}
    }
    vi.stubGlobal("EventSource", EventSourceStub);
    const accepted = Promise.withResolvers<Response>();
    let attempts = 0;
    let statusReads = 0;
    stubSyncRoutes({
      post: () => {
        attempts++;
        return attempts === 1 ? Promise.reject(new Error("response lost")) : accepted.promise;
      },
      status: () => {
        statusReads++;
        return problem("hubUnavailable", 503, "Unavailable");
      },
    });
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture, { running: true, last_run_at: "2026-08-02T12:00:00Z", last_error: "" });
    const runtime = testRuntime();
    const events = createEventsStore({
      runtime,
      onSyncStatus: (status) => Effect.sync(() => sync.setSyncStatus(status)),
    });
    runtime.runCommand(events.streamEffect, { operation: "provider events", safeContext: {}, onFailure: () => {} });
    renderFlow(fixture.stores);
    await waitFor(() => expect(source).toBeDefined());
    await waitFor(() => expect(attempts).toBe(2));
    const send = (data: object, lastEventId: string) =>
      source?.dispatchEvent(new MessageEvent("sync_status", { data: JSON.stringify(data), lastEventId }));
    // This completion belongs to an earlier accepted pass.
    send(
      { running: false, last_run_at: "2026-08-02T12:00:30Z", last_error: "", full_sync: synced(1, "sync-instance") },
      "1",
    );
    await waitFor(() => expect(sync.getSyncState()?.running).toBe(false));
    accepted.resolve(Response.json({ instance_id: "sync-instance", generation: 2 }));
    await waitFor(() => expect(statusReads).toBeGreaterThan(0));
    await tick();
    expect(fixture.loadPulls).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
    send({ running: true, full_sync: synced(2, "sync-instance") }, "2");
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    expect(fixture.loadPulls).toHaveBeenCalledTimes(1);
    send({ running: false, last_run_at: "2026-08-02T12:02:00Z" }, "3");
    await waitFor(() => expect(fixture.loadPulls).toHaveBeenCalledTimes(2));
    expect(attempts).toBe(2);
  });

  it("advances when the retried sync finishes before its response", async () => {
    const accepted = Promise.withResolvers<Response>();
    let attempts = 0;
    stubSyncRoutes({
      post: () => {
        attempts++;
        return attempts === 1 ? Promise.reject(new Error("response lost")) : accepted.promise;
      },
      status: () => respond({ running: false, last_run_at: "2026-08-02T12:01:00Z", last_error: "" }),
    });
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture, { running: false, last_run_at: "2026-08-02T12:00:00Z", last_error: "" });
    renderFlow(fixture.stores);
    await waitFor(() => expect(attempts).toBe(2));
    sync.setSyncStatus({ running: true, last_run_at: "2026-08-02T12:00:00Z", last_error: "" });
    sync.setSyncStatus({
      running: false,
      last_run_at: "2026-08-02T12:01:00Z",
      last_error: "",
      full_sync: synced(2, "sync-instance"),
    });
    await tick();
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
    accepted.resolve(Response.json({ instance_id: "sync-instance", generation: 2 }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
  });

  it("offers retry when a fresh status identifies a restarted syncer", async () => {
    let reads = 0;
    let posts = 0;
    stubSyncRoutes({
      post: () => {
        posts++;
        return respond({ instance_id: "old-instance", generation: 1 });
      },
      status: () => {
        reads++;
        return respond({
          running: false,
          full_sync: reads === 1 ? synced(0, "old-instance") : synced(1, "new-instance"),
        });
      },
    });
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture, { running: false });
    renderFlow(fixture.stores);
    await waitFor(() => expect(reads).toBe(1));
    testRuntime().runCommand(sync.reconcileSyncStatusEffect, {
      operation: "refresh sync",
      safeContext: {},
      onFailure: () => {},
    });
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry sync" })).toBeTruthy());
    expect(screen.getByRole("alert").textContent).toContain("Sync service restarted.");
    expect(posts).toBe(1);
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
  });

  it("offers retry when the accepted response has no receipt", async () => {
    let posts = 0;
    stubSyncRoutes({
      post: () => {
        posts++;
        return respond({});
      },
    });
    const fixture = storeFixture({ configured: true });
    realSyncStore(fixture, { running: false, last_error: "Earlier sync failed" });
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry sync" })).toBeTruthy());
    expect(posts).toBe(1);
    expect(screen.getByRole("alert").textContent).toContain("Could not confirm sync status.");
    expect(screen.queryByRole("heading", { name: "Open a pull request" })).toBeNull();
  });

  it("shows a retry when status clears the transport error", async () => {
    let attempts = 0;
    stubSyncRoutes({
      post: () => {
        attempts++;
        return Promise.reject(new Error(""));
      },
      status: () => respond({ running: false, last_run_at: "", last_error: "" }),
    });
    const fixture = storeFixture({ configured: true });
    const sync = realSyncStore(fixture);
    const trigger = sync.triggerOnboardingSyncEffect;
    sync.triggerOnboardingSyncEffect = () =>
      trigger().pipe(
        Effect.tapError(() =>
          Effect.sync(() => sync.setSyncStatus({ running: false, last_run_at: "", last_error: "" })),
        ),
      );
    renderFlow(fixture.stores);
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry sync" })).toBeTruthy(), { timeout: 5000 });
    expect(attempts).toBe(3);
    expect(screen.getByRole("alert").textContent).toContain("Could not reach Kenn Forge");
  }, 7000);

  it("starts sync when repositories become configured while onboarding remains mounted", async () => {
    const fixture = storeFixture();
    renderFlow(fixture.stores);

    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy(),
    );

    fixture.configureExternally();

    await waitFor(() => expect(fixture.triggerSync).toHaveBeenCalledOnce());
    expect(screen.queryByRole("heading", { name: "Choose the repositories you maintain" })).toBeNull();
  });

  it("reports configured repositories while the CLI probe is unavailable", () => {
    mocks.tooling.value = undefined;
    renderFlow(storeFixture({ configured: true }).stores);

    expect(screen.getByText("Code forge configured")).toBeTruthy();
    expect(screen.queryByText("Checking code forge")).toBeNull();
  });

  it("discovers and configures repositories for the authenticated GitHub host", async () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "ghe.example.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
    };
    mocks.listUserRepositories.mockReturnValue(
      Effect.yieldNow.pipe(
        Effect.as([
          {
            name_with_owner: "acme/forge",
            ssh_url: "git@ghe.example.com:acme/forge.git",
            default_branch: "main",
            provider: "github",
            platform_host: "ghe.example.com",
          },
        ]),
      ),
    );
    renderFlow(storeFixture().stores);

    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() => expect(screen.getByText("acme/forge")).toBeTruthy());
    await fireEvent.click(screen.getByText("acme/forge"));
    await fireEvent.click(screen.getByRole("button", { name: "Configure 1 repository" }));

    expect(mocks.listUserRepositories).toHaveBeenCalledWith({
      provider: "github",
      platformHost: "ghe.example.com",
    });
    await waitFor(() =>
      expect(observedBulkAddBodies).toEqual([
        {
          repos: [
            {
              provider: "github",
              host: "ghe.example.com",
              owner: "acme",
              name: "forge",
              repo_path: "acme/forge",
            },
          ],
        },
      ]),
    );
  });

  it("keeps the repository picker connected to provider settings", async () => {
    const callbacks = renderFlow(storeFixture().stores);
    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy(),
    );

    await fireEvent.click(screen.getByRole("button", { name: "Configure repositories in Settings" }));

    expect(callbacks.onDismiss).not.toHaveBeenCalled();
    expect(mocks.navigate).toHaveBeenCalledWith("/settings");
  });

  it("shows pull loading failures instead of an empty successful result", async () => {
    renderFlow(storeFixture({ configured: true, pulls: [], pullsError: "pull request API unavailable" }).stores);

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("pull request API unavailable"));
    expect(screen.queryByText("No open pull requests yet")).toBeNull();
  });

  it("treats opening the PR detail as a handoff rather than activation completion", async () => {
    const callbacks = renderFlow(storeFixture({ configured: true }).stores);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    await fireEvent.click(screen.getByRole("button", { name: "Open PR first" }));

    expect(callbacks.onDismiss).toHaveBeenCalledOnce();
    expect(callbacks.onComplete).not.toHaveBeenCalled();
    expect(mocks.navigate).toHaveBeenCalledWith("/pulls/github/acme/forge/42");
  });

  it("gives every setup milestone an accessible state label", async () => {
    renderFlow(storeFixture().stores);

    expect(screen.getByRole("listitem", { name: "Code forge: current" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    expect(screen.getByRole("listitem", { name: "Code forge: complete" })).toBeTruthy();
    expect(screen.getByRole("listitem", { name: "Choose repos: current" })).toBeTruthy();
    expect(screen.getByRole("listitem", { name: "First sync: upcoming" })).toBeTruthy();
  });

  it("lets experienced users leave the focused setup", async () => {
    const callbacks = renderFlow(storeFixture().stores);

    await fireEvent.click(screen.getByRole("button", { name: "I’ll do this later" }));
    expect(callbacks.onDismiss).toHaveBeenCalledOnce();
  });

  it("retains workspace creation after the onboarding view unmounts without navigating", async () => {
    let resolveWorkspace: ((workspace: { id: string; status: string }) => void) | undefined;
    const workspaceRequest = new Promise<{ id: string; status: string }>((resolve) => {
      resolveWorkspace = resolve;
    });
    workspaceResponse = async () => Response.json(await workspaceRequest);
    const callbacks = renderFlow(storeFixture({ configured: true }).stores);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(observedWorkspaceRequests).toHaveLength(1));

    callbacks.unmount();
    resolveWorkspace?.({ id: "ws-42", status: "provisioning" });
    await workspaceRequest;
    await Promise.resolve();

    expect(callbacks.onComplete).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalledWith("/terminal/ws-42");
    await waitFor(() =>
      expect(
        createdWorkspaceRef({
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "forge",
          repoPath: "acme/forge",
          number: 42,
          itemType: "pull",
        }),
      ).toEqual({ id: "ws-42", status: "provisioning" }),
    );
  });
});
