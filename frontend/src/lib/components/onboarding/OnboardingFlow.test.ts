import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import type { StoreInstances } from "@kenn-forge/ui";
import type { PullRequest } from "@kenn-forge/ui/api/types";
import { beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mocks = vi.hoisted(() => ({
  listUserRepositories: vi.fn(),
  bulkAddRepos: vi.fn(),
  createPullRequestWorkspace: vi.fn(),
  navigate: vi.fn(),
  tooling: {
    value: {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
    } as ToolingStatus | undefined,
  },
}));

vi.mock("../../api/project-intake.ts", () => ({
  listUserRepositories: mocks.listUserRepositories,
}));
vi.mock("../../api/settings.ts", () => ({
  bulkAddRepos: mocks.bulkAddRepos,
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

function storeFixture(options: { configured?: boolean; pulls?: PullRequest[]; pullsError?: string | null } = {}) {
  let configured = options.configured ?? false;
  const pulls = options.pulls ?? [pullRequest()];
  const setConfiguredRepos = vi.fn(() => {
    configured = true;
  });
  const triggerSync = vi.fn(async () => {});
  const loadPulls = vi.fn(async () => {});
  const stores = {
    settings: {
      hasConfiguredRepos: () => configured,
      setConfiguredRepos,
    },
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
      getPulls: () => pulls,
      getError: () => options.pullsError ?? null,
    },
  } as unknown as StoreInstances;
  return { stores, setConfiguredRepos, triggerSync, loadPulls };
}

function renderFlow(stores: StoreInstances) {
  const callbacks = {
    onStart: vi.fn(),
    onDismiss: vi.fn(),
    onComplete: vi.fn(),
  };
  render(OnboardingFlow, {
    props: {
      stores,
      iconSrc: "/favicon.svg",
      ...callbacks,
    },
  });
  return callbacks;
}

describe("OnboardingFlow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
    };
    mocks.listUserRepositories.mockResolvedValue([
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
    ]);
    mocks.bulkAddRepos.mockResolvedValue({ repos: [{ repo_path: "acme/forge" }] });
    mocks.createPullRequestWorkspace.mockResolvedValue({
      id: "ws-42",
      status: "provisioning",
    });
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
    expect(mocks.bulkAddRepos).toHaveBeenCalledWith([
      {
        provider: "github",
        host: "github.com",
        owner: "acme",
        name: "forge",
        repo_path: "acme/forge",
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

  it("discovers and configures repositories for the authenticated GitHub host", async () => {
    mocks.tooling.value = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "ghe.example.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
    };
    mocks.listUserRepositories.mockResolvedValue([
      {
        name_with_owner: "acme/forge",
        ssh_url: "git@ghe.example.com:acme/forge.git",
        default_branch: "main",
        provider: "github",
        platform_host: "ghe.example.com",
      },
    ]);
    renderFlow(storeFixture().stores);

    await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
    await waitFor(() => expect(screen.getByText("acme/forge")).toBeTruthy());
    await fireEvent.click(screen.getByText("acme/forge"));
    await fireEvent.click(screen.getByRole("button", { name: "Configure 1 repository" }));

    expect(mocks.listUserRepositories).toHaveBeenCalledWith({
      provider: "github",
      platformHost: "ghe.example.com",
    });
    expect(mocks.bulkAddRepos).toHaveBeenCalledWith([
      {
        provider: "github",
        host: "ghe.example.com",
        owner: "acme",
        name: "forge",
        repo_path: "acme/forge",
      },
    ]);
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
});
