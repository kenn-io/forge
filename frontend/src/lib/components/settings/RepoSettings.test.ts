import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS, type ConfigRepo } from "../../api/types.js";
import * as flash from "../../stores/flash.svelte.js";
import { makeStartupSnapshot } from "../../../test/startupSnapshot.js";

const {
  mockAddRepo,
  mockBulkAddRepos,
  mockPreviewRepos,
  mockPromoteRepo,
  mockRefreshRepo,
  mockRefreshSyncStatus,
  mockUpdateRepoUIVisibility,
  mockUpdateRepoWorktreeBasePath,
} = vi.hoisted(() => ({
  mockAddRepo: vi.fn(),
  mockBulkAddRepos: vi.fn(),
  mockPreviewRepos: vi.fn(),
  mockPromoteRepo: vi.fn(),
  mockRefreshRepo: vi.fn(),
  mockRefreshSyncStatus: vi.fn(),
  mockUpdateRepoUIVisibility: vi.fn(),
  mockUpdateRepoWorktreeBasePath: vi.fn(),
}));

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    sync: {
      refreshSyncStatus: mockRefreshSyncStatus,
    },
  }),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  const { ApiProblemError } = await import("../../api/effect-errors.js");
  const runMock = <A>(operation: string, run: () => Promise<A>) =>
    Effect.tryPromise({
      try: run,
      catch: (cause) =>
        new ApiProblemError({
          operation,
          problem: {
            code: "upstreamError",
            detail: cause instanceof Error ? cause.message : String(cause),
            type: "about:blank",
          },
        }),
    });
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      addRepo: (owner, name, options) => runMock("add repository", () => mockAddRepo(owner, name, options)),
      refreshRepo: (owner, name, options) => runMock("refresh repository", () => mockRefreshRepo(owner, name, options)),
      updateRepoWorktreeBasePath: (owner, name, options, path) =>
        runMock("save repository clone path", () => mockUpdateRepoWorktreeBasePath(owner, name, options, path)),
      updateRepoUIVisibility: (owner, name, options, hidden) =>
        runMock("save repository visibility", () => mockUpdateRepoUIVisibility(owner, name, options, hidden)),
      previewRepos: (owner, pattern, options) =>
        runMock("preview repositories", () => mockPreviewRepos(owner, pattern, options)),
      bulkAddRepos: (repos) => runMock("add repositories", () => mockBulkAddRepos(repos)),
      promoteRepo: (repo, path, exactRepoAlreadyAdded) =>
        runMock("promote repository", () => mockPromoteRepo(repo, path, exactRepoAlreadyAdded)),
    }),
  };
});

import type { RepoPreviewResponse } from "../../stores/settings-workflow.js";
import RepoSettings from "./RepoSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

function settingsResponse(repos: ConfigRepo[] = []) {
  return makeStartupSnapshot({
    repos,
    issues: { hide_bots: false },
    notifications: { enabled: false },
    terminal: { ...DEFAULT_TERMINAL_SETTINGS, font_size: 14 },
  });
}

function renderRepoSettings(props: { repos: ConfigRepo[]; onUpdate: (repos: ConfigRepo[]) => void }): void {
  render(SettingsRuntimeHarness, {
    props: { component: RepoSettings, componentProps: props },
  });
}

describe("RepoSettings", () => {
  afterEach(() => {
    cleanup();
    mockRefreshSyncStatus.mockReset();
    mockAddRepo.mockReset();
    mockRefreshRepo.mockReset();
    mockUpdateRepoUIVisibility.mockReset();
    mockUpdateRepoWorktreeBasePath.mockReset();
    mockPreviewRepos.mockReset();
    mockBulkAddRepos.mockReset();
    mockPromoteRepo.mockReset();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
  });

  it("renders the glob count and refresh action", () => {
    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "roborev-dev",
          name: "*",
          repo_path: "roborev-dev/*",
          is_glob: true,
          matched_repo_count: 2,
        },
      ],
      onUpdate: vi.fn(),
    });

    expect(screen.getByText("roborev-dev/* (2)", { selector: ".repo-name" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
  });

  it("shows provider icons when configured repos use multiple providers", () => {
    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
          is_glob: false,
          matched_repo_count: 1,
        },
        {
          provider: "forgejo",
          platform_host: "codeberg.org",
          owner: "forge",
          name: "service",
          repo_path: "forge/service",
          is_glob: false,
          matched_repo_count: 1,
        },
      ],
      onUpdate: vi.fn(),
    });

    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "Forgejo" })).toBeTruthy();
  });

  it("hides provider icons when configured repos use one provider", () => {
    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
          is_glob: false,
          matched_repo_count: 1,
        },
        {
          provider: "github",
          platform_host: "ghe.example.com",
          owner: "enterprise",
          name: "service",
          repo_path: "enterprise/service",
          is_glob: false,
          matched_repo_count: 1,
        },
      ],
      onUpdate: vi.fn(),
    });

    expect(screen.queryByRole("img", { name: "GitHub" })).toBeNull();
  });

  it("opens the repository import modal and restores focus on close", async () => {
    renderRepoSettings({
      repos: [],
      onUpdate: vi.fn(),
    });

    const trigger = screen.getByRole("button", {
      name: "Add repositories…",
    });
    await fireEvent.click(trigger);

    expect(screen.getByRole("dialog", { name: "Add repositories" })).toBeTruthy();
    expect(screen.getByLabelText("Repository pattern")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("keeps direct glob add in an advanced section", () => {
    renderRepoSettings({
      repos: [],
      onUpdate: vi.fn(),
    });

    const summary = screen.getByText("Advanced: add provider-scoped repo or tracking glob directly");
    expect(summary).toBeTruthy();
    expect(summary.closest("details")?.hasAttribute("open")).toBe(false);
  });

  it("forwards add/refresh through the settings API", async () => {
    mockAddRepo.mockResolvedValue(settingsResponse());
    mockRefreshRepo.mockResolvedValue(settingsResponse());

    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "*",
          repo_path: "acme/*",
          is_glob: true,
          matched_repo_count: 1,
        },
      ],
      onUpdate: vi.fn(),
    });

    const input = screen.getByPlaceholderText("provider/owner/name");
    await fireEvent.input(input, {
      target: { value: "github/acme/widget" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(mockAddRepo).toHaveBeenCalledWith("acme", "widget", {
      provider: "github",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(mockRefreshRepo).toHaveBeenCalledWith("acme", "*", {
      provider: "github",
      host: "github.com",
    });
  });

  it("saves a worktree base path for exact repositories", async () => {
    const onUpdate = vi.fn();
    const updatedRepos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
        worktree_base_path: "/Users/acme/api",
        is_glob: false,
        matched_repo_count: 1,
      },
    ];
    mockUpdateRepoWorktreeBasePath.mockResolvedValue(settingsResponse(updatedRepos));

    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          is_glob: false,
          matched_repo_count: 1,
        },
      ],
      onUpdate,
    });

    expect(screen.queryByPlaceholderText("/path/to/existing/clone")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Configure acme/api" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Edit local clone path…" }));

    await fireEvent.input(screen.getByPlaceholderText("/path/to/existing/clone"), {
      target: { value: "/Users/acme/api" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save local clone path for acme/api" }));

    expect(mockUpdateRepoWorktreeBasePath).toHaveBeenCalledWith(
      "acme",
      "api",
      {
        provider: "github",
        host: "github.com",
      },
      "/Users/acme/api",
    );
    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(updatedRepos));
  });

  it("offers no configure menu on glob rows because visibility and clones are exact-repo settings", () => {
    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "*",
          repo_path: "acme/*",
          is_glob: true,
          matched_repo_count: 3,
          hidden_from_ui: false,
        },
      ],
      onUpdate: vi.fn(),
    });

    expect(screen.queryByRole("button", { name: "Configure acme/* (3)" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Configure / })).toBeNull();
  });

  it("hides a repository through the settings workflow", async () => {
    const onUpdate = vi.fn();
    const repo = {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "archive",
      repo_path: "acme/archive",
      is_glob: false,
      matched_repo_count: 1,
      hidden_from_ui: false,
    };
    const updatedRepos = [{ ...repo, hidden_from_ui: true }];
    mockUpdateRepoUIVisibility.mockResolvedValue({ repos: updatedRepos });

    renderRepoSettings({ repos: [repo], onUpdate });
    await fireEvent.click(screen.getByRole("button", { name: "Configure acme/archive" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Hide from UI" }));

    expect(mockUpdateRepoUIVisibility).toHaveBeenCalledWith(
      "acme",
      "archive",
      { provider: "github", host: "github.com" },
      true,
    );
    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(updatedRepos));
  });

  it("preserves confirmed visibility after a failed update", async () => {
    mockUpdateRepoUIVisibility.mockRejectedValue(new Error("could not save visibility"));
    const onUpdate = vi.fn();
    const repo = {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "archive",
      repo_path: "acme/archive",
      is_glob: false,
      matched_repo_count: 1,
      hidden_from_ui: true,
    };

    renderRepoSettings({ repos: [repo], onUpdate });
    const gear = screen.getByRole("button", { name: "Configure acme/archive" });
    await fireEvent.click(gear);
    await fireEvent.click(screen.getByRole("menuitem", { name: "Show in UI" }));

    await waitFor(() =>
      expect(flash.getFlash()).toMatchObject({ message: "could not save visibility", tone: "danger" }),
    );
    expect(onUpdate).not.toHaveBeenCalled();
    await fireEvent.click(gear);
    expect(screen.getByRole("menuitem", { name: "Show in UI" })).toBeTruthy();
  });

  it("keeps the gear available while disabling a pending visibility action", async () => {
    mockUpdateRepoUIVisibility.mockReturnValue(new Promise(() => {}));
    const repo = {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "archive",
      repo_path: "acme/archive",
      is_glob: false,
      matched_repo_count: 1,
      hidden_from_ui: false,
    };

    renderRepoSettings({ repos: [repo], onUpdate: vi.fn() });
    const gear = screen.getByRole("button", { name: "Configure acme/archive" });
    await fireEvent.click(gear);
    await fireEvent.click(screen.getByRole("menuitem", { name: "Hide from UI" }));

    expect((gear as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(gear);
    expect((screen.getByRole("menuitem", { name: "Hide from UI" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("keeps repository configuration inspectable but disabled when embedded", async () => {
    window.__kenn_forge_config = { embed: {} };
    window.__kenn_forge_notify_config_changed?.();
    try {
      const repo = {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: false,
      };

      renderRepoSettings({ repos: [repo], onUpdate: vi.fn() });
      const gear = screen.getByRole("button", { name: "Configure acme/archive" });
      expect((gear as HTMLButtonElement).disabled).toBe(false);
      await fireEvent.click(gear);
      expect((screen.getByRole("menuitem", { name: "Edit local clone path…" }) as HTMLButtonElement).disabled).toBe(
        true,
      );
      expect((screen.getByRole("menuitem", { name: "Hide from UI" }) as HTMLButtonElement).disabled).toBe(true);
    } finally {
      delete window.__kenn_forge_config;
      window.__kenn_forge_notify_config_changed?.();
    }
  });

  it("promotes a glob match to an exact repository with a local clone path", async () => {
    const onUpdate = vi.fn();
    const addedRepos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "*",
        repo_path: "acme/*",
        is_glob: true,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
        is_glob: false,
        matched_repo_count: 1,
      },
    ];
    const promotedRepos = [
      {
        ...addedRepos[0]!,
      },
      {
        ...addedRepos[1]!,
        worktree_base_path: "/Users/acme/api",
      },
    ];
    mockPreviewRepos.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          description: "HTTP API",
          private: false,
          fork: false,
          pushed_at: null,
          already_configured: false,
        },
      ],
    });
    mockPromoteRepo.mockResolvedValue(settingsResponse(promotedRepos));

    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "*",
          repo_path: "acme/*",
          is_glob: true,
          matched_repo_count: 1,
        },
      ],
      onUpdate,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Promote glob repository acme/*" }));
    await screen.findByRole("dialog", { name: "Promote wildcard repository" });
    expect(screen.getByRole("radiogroup", { name: "Wildcard matches" })).toBeTruthy();
    await screen.findByText("acme/api");
    await fireEvent.input(screen.getByLabelText("Local clone path for acme/api"), {
      target: { value: "/Users/acme/api" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Promote repository" }));

    expect(mockPreviewRepos).toHaveBeenCalledWith("acme", "*", {
      provider: "github",
      host: "github.com",
    });
    expect(mockPromoteRepo).toHaveBeenCalledWith(
      {
        provider: "github",
        host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
      },
      "/Users/acme/api",
      false,
    );
    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(promotedRepos));
    expect(mockRefreshSyncStatus).toHaveBeenCalled();
  });

  it("focuses the promote search while wildcard matches are loading", async () => {
    let resolvePreview: ((value: RepoPreviewResponse) => void) | undefined;
    mockPreviewRepos.mockReturnValue(
      new Promise((resolve) => {
        resolvePreview = resolve;
      }),
    );

    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "*",
          repo_path: "acme/*",
          is_glob: true,
          matched_repo_count: 1,
        },
      ],
      onUpdate: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Promote glob repository acme/*" }));
    await screen.findByRole("dialog", { name: "Promote wildcard repository" });
    const searchInput = screen.getByPlaceholderText("Filter repositories...");

    await waitFor(() => expect(document.activeElement).toBe(searchInput));

    resolvePreview?.({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          description: "HTTP API",
          private: false,
          fork: false,
          pushed_at: null,
          already_configured: false,
        },
      ],
    });
    await screen.findByText("acme/api");
  });

  it("keeps the promotion open when saving the local clone path fails", async () => {
    const onUpdate = vi.fn();
    mockPreviewRepos.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          description: "HTTP API",
          private: false,
          fork: false,
          pushed_at: null,
          already_configured: false,
        },
      ],
    });
    mockPromoteRepo.mockRejectedValue(new Error("path does not exist"));

    renderRepoSettings({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "*",
          repo_path: "acme/*",
          is_glob: true,
          matched_repo_count: 1,
        },
      ],
      onUpdate,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Promote glob repository acme/*" }));
    await screen.findByText("acme/api");
    await fireEvent.input(screen.getByLabelText("Local clone path for acme/api"), {
      target: { value: "/missing/api" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Promote repository" }));

    await waitFor(() => expect(mockPromoteRepo).toHaveBeenCalled());
    await waitFor(() => expect(flash.getFlash()).toMatchObject({ message: "path does not exist", tone: "danger" }));
    expect(screen.queryByText("path does not exist")).toBeNull();
    expect(onUpdate).not.toHaveBeenCalled();
    expect(mockRefreshSyncStatus).not.toHaveBeenCalled();
  });

  it("updates repos and refreshes sync status after import", async () => {
    const importedRepos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
        is_glob: false,
        matched_repo_count: 1,
      },
    ];
    const onUpdate = vi.fn();
    mockPreviewRepos.mockResolvedValue({
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      pattern: "*",
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          description: "HTTP API",
          private: false,
          fork: false,
          pushed_at: null,
          already_configured: false,
        },
      ],
    });
    mockBulkAddRepos.mockResolvedValue(settingsResponse(importedRepos));
    renderRepoSettings({
      repos: [],
      onUpdate,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add repositories…" }));
    await fireEvent.input(screen.getByLabelText("Repository pattern"), {
      target: { value: "acme/*" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");
    await fireEvent.click(screen.getByRole("button", { name: "Add selected repositories" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(importedRepos));
    expect(mockRefreshSyncStatus).toHaveBeenCalled();
  });
});
