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
import type { ToolingStatusValue } from "../../stores/embed-config.svelte.js";
import type { StoreInstances } from "../../types.js";

const mocks = vi.hoisted(() => ({
  listUserRepositories: vi.fn(),
  createPullRequestWorkspace: vi.fn(),
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

vi.mock("../../api/project-intake.ts", () => ({
  listUserRepositories: mocks.listUserRepositories,
  projectIntakeFailureMessage: (failure: Error) => failure.message,
}));
vi.mock("../../api/onboarding.ts", () => ({
  createPullRequestWorkspace: mocks.createPullRequestWorkspace,
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

function storeFixture(options: { configured?: boolean; pulls?: PullRequest[]; pullsError?: string | null } = {}) {
  const pulls = options.pulls ?? [pullRequest()];
  const settings = createSettingsStore();
  const setConfiguredRepos = vi.spyOn(settings, "setConfiguredRepos");
  if (options.configured) settings.setConfiguredRepos([configuredRepo()]);
  const triggerSync = vi.fn();
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
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
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
    mocks.createPullRequestWorkspace.mockReturnValue(
      Effect.succeed({
        id: "ws-42",
        status: "provisioning",
      }),
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
    expect(mocks.createPullRequestWorkspace).toHaveBeenCalledWith(pullRequest());
    expect(mocks.navigate).toHaveBeenCalledWith("/terminal/ws-42");
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
    mocks.createPullRequestWorkspace.mockReturnValue(Effect.promise(() => workspaceRequest));
    const callbacks = renderFlow(storeFixture({ configured: true }).stores);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Open a pull request" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Continue with PR #42" }));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    callbacks.unmount();
    resolveWorkspace?.({ id: "ws-42", status: "provisioning" });
    await workspaceRequest;
    await Promise.resolve();

    expect(callbacks.onComplete).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalledWith("/terminal/ws-42");
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
    ).toEqual({ id: "ws-42", status: "provisioning" });
  });
});
