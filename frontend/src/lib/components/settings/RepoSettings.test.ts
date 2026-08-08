import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { ConfigRepo } from "@kenn-forge/ui/api/types";
import * as flash from "@kenn-forge/ui/stores/flash";

const mockRefreshSyncStatus = vi.fn();

vi.mock("@kenn-forge/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@kenn-forge/ui")>()),
  getStores: () => ({
    sync: {
      refreshSyncStatus: mockRefreshSyncStatus,
    },
  }),
}));

vi.mock("../../api/settings.js", () => ({
  addRepo: vi.fn(),
  removeRepo: vi.fn(),
  getSettings: vi.fn(),
  refreshRepo: vi.fn(),
  updateRepoVisibility: vi.fn(),
  updateRepoWorktreeBasePath: vi.fn(),
  previewRepos: vi.fn(),
  bulkAddRepos: vi.fn(),
}));

import {
  addRepo,
  bulkAddRepos,
  previewRepos,
  refreshRepo,
  removeRepo,
  updateRepoVisibility,
  updateRepoWorktreeBasePath,
} from "../../api/settings.js";
import RepoSettings from "./RepoSettings.svelte";

const mockAddRepo = vi.mocked(addRepo);
const mockRefreshRepo = vi.mocked(refreshRepo);
const mockUpdateRepoVisibility = vi.mocked(updateRepoVisibility);
const mockUpdateRepoWorktreeBasePath = vi.mocked(updateRepoWorktreeBasePath);
const mockPreviewRepos = vi.mocked(previewRepos);
const mockBulkAddRepos = vi.mocked(bulkAddRepos);
const mockRemoveRepo = vi.mocked(removeRepo);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function settingsResponse(repos: ConfigRepo[]) {
  return {
    repos,
    kata_projects: [],
    pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
    workspaces: { auto_assign_on_create: false },
    issues: { hide_bots: false },
    activity: {
      view_mode: "threaded" as const,
      time_range: "7d" as const,
      hide_closed: false,
      hide_bots: false,
      collapse_threads: false,
      default_branch_retention_days: 90,
      default_branch_max_commits: 5000,
    },
    terminal: {
      font_family: "",
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
    },
    agents: [],
    fleet: defaultFleetSettings(),
  };
}

function defaultFleetSettings() {
  return {
    enabled: false,
    sessions: {},
    peers: [],
    ssh_peers: [],
    restart_required: false,
  };
}

describe("RepoSettings", () => {
  afterEach(() => {
    cleanup();
    mockRefreshSyncStatus.mockReset();
    mockAddRepo.mockReset();
    mockRefreshRepo.mockReset();
    mockUpdateRepoVisibility.mockReset();
    mockUpdateRepoWorktreeBasePath.mockReset();
    mockPreviewRepos.mockReset();
    mockBulkAddRepos.mockReset();
    mockRemoveRepo.mockReset();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
    delete window.__kenn_forge_config;
    window.__kenn_forge_notify_config_changed?.();
  });

  it("renders glob configuration with visibility but no local clone action", async () => {
    render(RepoSettings, {
      props: {
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
      },
    });

    expect(screen.getByText("roborev-dev/* (2)", { selector: ".repo-name" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Configure roborev-dev/* (2)" }));
    expect(screen.getByRole("menuitem", { name: "Hide from UI" })).toBeTruthy();
    expect(screen.queryByRole("menuitem", { name: "Edit local clone path…" })).toBeNull();
  });

  it("shows provider icons when configured repos use multiple providers", () => {
    render(RepoSettings, {
      props: {
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
      },
    });

    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "Forgejo" })).toBeTruthy();
  });

  it("hides provider icons when configured repos use one provider", () => {
    render(RepoSettings, {
      props: {
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
      },
    });

    expect(screen.queryByRole("img", { name: "GitHub" })).toBeNull();
  });

  it("opens the repository import modal and restores focus on close", async () => {
    render(RepoSettings, {
      props: {
        repos: [],
        onUpdate: vi.fn(),
      },
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
    render(RepoSettings, {
      props: {
        repos: [],
        onUpdate: vi.fn(),
      },
    });

    const summary = screen.getByText("Advanced: add provider-scoped repo or tracking glob directly");
    expect(summary).toBeTruthy();
    expect(summary.closest("details")?.hasAttribute("open")).toBe(false);
  });

  it("forwards add/refresh through the settings API", async () => {
    mockAddRepo.mockResolvedValue({
      repos: [],
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      notifications: {
        enabled: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });
    mockRefreshRepo.mockResolvedValue({
      repos: [],
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      notifications: {
        enabled: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });

    render(RepoSettings, {
      props: {
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
      },
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
    mockUpdateRepoWorktreeBasePath.mockResolvedValue({
      repos: updatedRepos,
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });

    render(RepoSettings, {
      props: {
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
      },
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

  it("hides a repository from the interactive UI", async () => {
    const onUpdate = vi.fn();
    const updatedRepos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        hide_from_ui: true,
        is_glob: false,
        matched_repo_count: 1,
      },
    ];
    mockUpdateRepoVisibility.mockResolvedValue({
      repos: updatedRepos,
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });

    render(RepoSettings, {
      props: {
        repos: [{ ...updatedRepos[0]!, hide_from_ui: false }],
        onUpdate,
      },
    });

    expect(screen.queryByRole("checkbox", { name: "Show acme/archive in UI" })).toBeNull();
    expect(screen.queryByText("Hidden from UI")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Configure acme/archive" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Hide from UI" }));

    expect(mockUpdateRepoVisibility).toHaveBeenCalledWith(
      "acme",
      "archive",
      { provider: "github", host: "github.com" },
      true,
    );
    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(updatedRepos));
    expect(mockRefreshSyncStatus).toHaveBeenCalled();
  });

  it("preserves confirmed visibility after a failed update", async () => {
    mockUpdateRepoVisibility.mockRejectedValue(new Error("could not save visibility"));
    const onUpdate = vi.fn();

    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "archive",
            repo_path: "acme/archive",
            hide_from_ui: true,
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        onUpdate,
      },
    });

    expect(screen.queryByText("Hidden from UI")).toBeNull();
    const gear = screen.getByRole("button", { name: "Configure acme/archive" });
    await fireEvent.click(gear);
    await fireEvent.click(screen.getByRole("menuitem", { name: "Show in UI" }));
    expect(mockUpdateRepoVisibility).toHaveBeenCalledWith(
      "acme",
      "archive",
      { provider: "github", host: "github.com" },
      false,
    );

    await waitFor(() =>
      expect(flash.getFlash()).toMatchObject({ message: "could not save visibility", tone: "danger" }),
    );
    expect(onUpdate).not.toHaveBeenCalled();
    await fireEvent.click(gear);
    expect(screen.getByRole("menuitem", { name: "Show in UI" })).toBeTruthy();
  });

  it("serializes visibility changes across repository rows", async () => {
    const firstSave = deferred<ReturnType<typeof settingsResponse>>();
    const secondSave = deferred<ReturnType<typeof settingsResponse>>();
    mockUpdateRepoVisibility.mockReturnValueOnce(firstSave.promise).mockReturnValueOnce(secondSave.promise);
    const onUpdate = vi.fn();
    const repos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "legacy",
        repo_path: "acme/legacy",
        is_glob: false,
        matched_repo_count: 1,
      },
    ];

    render(RepoSettings, { props: { repos, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Configure acme/archive" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Hide from UI" }));
    await fireEvent.click(screen.getByRole("button", { name: "Configure acme/legacy" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Hide from UI" }));

    expect(mockUpdateRepoVisibility).toHaveBeenCalledTimes(1);

    firstSave.resolve(settingsResponse([{ ...repos[0]!, hide_from_ui: true }, repos[1]!]));
    await waitFor(() => expect(mockUpdateRepoVisibility).toHaveBeenCalledTimes(2));

    const bothHidden = repos.map((repo) => ({ ...repo, hide_from_ui: true }));
    secondSave.resolve(settingsResponse(bothHidden));
    await waitFor(() => expect(onUpdate).toHaveBeenLastCalledWith(bothHidden));
  });

  it("keeps the gear available while disabling a pending visibility action", async () => {
    mockUpdateRepoVisibility.mockReturnValue(new Promise(() => {}));

    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "archive",
            repo_path: "acme/archive",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        onUpdate: vi.fn(),
      },
    });

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

    render(RepoSettings, {
      props: {
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "archive",
            repo_path: "acme/archive",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        onUpdate: vi.fn(),
      },
    });

    const gear = screen.getByRole("button", { name: "Configure acme/archive" });
    expect((gear as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(gear);
    expect((screen.getByRole("menuitem", { name: "Edit local clone path…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("menuitem", { name: "Hide from UI" }) as HTMLButtonElement).disabled).toBe(true);
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
    mockBulkAddRepos.mockResolvedValue({
      repos: addedRepos,
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });
    mockUpdateRepoWorktreeBasePath.mockResolvedValue({
      repos: promotedRepos,
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });

    render(RepoSettings, {
      props: {
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
      },
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
    expect(mockBulkAddRepos).toHaveBeenCalledWith([
      {
        provider: "github",
        host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
      },
    ]);
    expect(mockUpdateRepoWorktreeBasePath).toHaveBeenCalledWith(
      "acme",
      "api",
      {
        provider: "github",
        host: "github.com",
      },
      "/Users/acme/api",
    );
    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith(promotedRepos));
    expect(mockRefreshSyncStatus).toHaveBeenCalled();
  });

  it("focuses the promote search while wildcard matches are loading", async () => {
    let resolvePreview: ((value: Awaited<ReturnType<typeof previewRepos>>) => void) | undefined;
    mockPreviewRepos.mockReturnValue(
      new Promise((resolve) => {
        resolvePreview = resolve;
      }),
    );

    render(RepoSettings, {
      props: {
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
      },
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

  it("rolls back a promoted exact repository when saving the local clone path fails", async () => {
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
    mockBulkAddRepos.mockResolvedValue({
      repos: addedRepos,
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });
    mockUpdateRepoWorktreeBasePath.mockRejectedValue(new Error("path does not exist"));
    mockRemoveRepo.mockResolvedValue();

    render(RepoSettings, {
      props: {
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
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Promote glob repository acme/*" }));
    await screen.findByText("acme/api");
    await fireEvent.input(screen.getByLabelText("Local clone path for acme/api"), {
      target: { value: "/missing/api" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Promote repository" }));

    await waitFor(() =>
      expect(mockRemoveRepo).toHaveBeenCalledWith("acme", "api", {
        provider: "github",
        host: "github.com",
      }),
    );
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
    mockBulkAddRepos.mockResolvedValue({
      repos: importedRepos,
      kata_projects: [],
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      workspaces: { auto_assign_on_create: false },
      issues: { hide_bots: false },
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
      },
      notifications: {
        enabled: false,
      },
      agents: [],
      fleet: defaultFleetSettings(),
    });
    render(RepoSettings, {
      props: {
        repos: [],
        onUpdate,
      },
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
