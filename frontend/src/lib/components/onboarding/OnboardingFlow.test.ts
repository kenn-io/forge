import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
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
vi.mock("../../stores/tooling-status.svelte.ts", () => ({
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
    sync: {
      getSyncState: () => ({
        running: false,
        last_run_at: "2026-08-02T12:01:00Z",
        last_error: "",
      }),
      triggerSync,
      triggerSyncEffect,
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
    expect(screen.getByRole("heading", { name: "Start your first workspace" })).toBeTruthy();
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

  it("hands non-GitHub setup to regular repository configuration", async () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: false, authenticated: false },
      glab: { available: true, authenticated: true, host: "gitlab.example.com", user: "maintainer" },
    };
    const callbacks = renderFlow(storeFixture().stores);

    await fireEvent.click(screen.getByRole("button", { name: "Configure Forgejo" }));

    expect(callbacks.onDismiss).not.toHaveBeenCalled();
    expect(callbacks.onComplete).not.toHaveBeenCalled();
    expect(mocks.navigate).toHaveBeenCalledWith("/settings");
  });

  it("keeps a missing gh installation on the provider readiness step", () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: false, authenticated: false },
      glab: { available: false, authenticated: false },
    };
    renderFlow(storeFixture().stores);

    expect(screen.getByRole("heading", { name: "Connect a code forge" })).toBeTruthy();
    expect(screen.getByText(/is not installed/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Check again" })).toBeTruthy();
    expect(mocks.listUserRepositories).not.toHaveBeenCalled();
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
