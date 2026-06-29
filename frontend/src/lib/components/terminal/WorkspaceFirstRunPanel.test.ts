import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceFirstRunPanel from "./WorkspaceFirstRunPanel.svelte";

const mocks = vi.hoisted(() => ({
  cloneProject: vi.fn(),
  listUserRepositories: vi.fn(),
  loadSnapshotHosts: vi.fn(),
  navigate: vi.fn(),
  registerExistingProject: vi.fn(),
}));

vi.mock("../../api/fleet-snapshot.ts", () => ({
  loadSnapshotHosts: mocks.loadSnapshotHosts,
}));

vi.mock("../../api/project-intake.ts", () => ({
  cloneProject: mocks.cloneProject,
  listUserRepositories: mocks.listUserRepositories,
  registerExistingProject: mocks.registerExistingProject,
}));

vi.mock("../../stores/router.svelte.ts", () => ({
  navigate: mocks.navigate,
}));

const win = window as any;

function project(id = "prj_1") {
  return {
    id,
    display_name: "Repo",
    local_path: "/tmp/repo",
    created_at: "2026-06-22T00:00:00Z",
    updated_at: "2026-06-22T00:00:00Z",
  };
}

interface SetupArgs {
  ghAuthed: boolean;
  ghAvailable?: boolean;
  onWorkspaceCommand?: (command: string, payload: Record<string, unknown>) => CommandResult | Promise<CommandResult>;
}

function setupConfig({
  ghAuthed,
  ghAvailable = true,
  onWorkspaceCommand = vi.fn().mockResolvedValue({ ok: true }),
}: SetupArgs): typeof onWorkspaceCommand {
  win.__middleman_config = {
    onWorkspaceCommand,
    embed: {
      tooling: {
        git: { available: true, version: "2.45.0" },
        gh: { available: ghAvailable, authenticated: ghAuthed },
      },
    },
  };
  win.__middleman_notify_config_changed?.();
  return onWorkspaceCommand;
}

describe("WorkspaceFirstRunPanel", () => {
  beforeEach(() => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    mocks.cloneProject.mockReset();
    mocks.listUserRepositories.mockReset();
    mocks.loadSnapshotHosts.mockReset();
    mocks.loadSnapshotHosts.mockResolvedValue([]);
    mocks.navigate.mockReset();
    mocks.registerExistingProject.mockReset();
  });

  afterEach(() => {
    cleanup();
    delete win.__middleman_config;
  });

  it("renders the three primary actions with their descriptions", () => {
    setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel);

    expect(
      screen.getByRole("button", {
        name: /Add an existing local repository/i,
      }),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: /Clone a repository/i })).toBeTruthy();
    expect(
      screen.getByRole("button", {
        name: /Connect a GitHub repository/i,
      }),
    ).toBeTruthy();
  });

  it("disables the GitHub action with a recovery hint when gh is not authenticated", () => {
    setupConfig({ ghAuthed: false, ghAvailable: true });
    render(WorkspaceFirstRunPanel);

    const button = screen.getByRole("button", {
      name: /Connect a GitHub repository/i,
    });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Run gh auth login to use this option.")).toBeTruthy();
  });

  it("uses an install-gh recovery hint when gh is unavailable", () => {
    setupConfig({ ghAuthed: false, ghAvailable: false });
    render(WorkspaceFirstRunPanel);

    expect(screen.getByText("Install gh to use this option.")).toBeTruthy();
  });

  it("registers an existing repository and notifies the host", async () => {
    mocks.registerExistingProject.mockResolvedValue(project("prj_existing"));
    const command = setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Add an existing local repository/i,
      }),
    );
    await fireEvent.input(screen.getByLabelText("Repository path"), {
      target: { value: "/tmp/repo" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add repository" }));

    await waitFor(() => {
      expect(mocks.registerExistingProject).toHaveBeenCalledWith("/tmp/repo");
    });
    expect(command).toHaveBeenCalledWith("project-registered", {
      projectId: "prj_existing",
    });
    expect(mocks.navigate).toHaveBeenCalledWith("/workspaces/embed/project/prj_existing");
  });

  it("adds a project on a scoped host", async () => {
    mocks.registerExistingProject.mockResolvedValue(project("prj_remote"));
    mocks.loadSnapshotHosts.mockResolvedValue([
      {
        configKey: "local",
        diagnostics: [],
        id: "local",
        kind: "self",
        name: "Local",
        operationAvailability: {},
        platform: "github",
        preferredTransport: "local",
        reachable: true,
        tmuxSessions: [],
      },
      {
        configKey: "epyc",
        diagnostics: [],
        id: "epyc",
        kind: "remote",
        name: "EPYC",
        operationAvailability: {},
        platform: "github",
        preferredTransport: "ssh",
        reachable: true,
        tmuxSessions: [],
      },
    ]);
    const command = setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel, {
      props: { firstRun: false, hostKey: "epyc" },
    });

    expect(screen.getByText("Add a project.")).toBeTruthy();
    expect(await screen.findByText("Host: EPYC")).toBeTruthy();

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Add an existing repository/i,
      }),
    );
    await fireEvent.input(screen.getByLabelText("Repository path"), {
      target: { value: "/srv/repo" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add repository" }));

    await waitFor(() => {
      expect(mocks.registerExistingProject).toHaveBeenCalledWith("/srv/repo", { hostKey: "epyc" });
    });
    expect(command).toHaveBeenCalledWith("project-registered", {
      projectId: "prj_remote",
      hostKey: "epyc",
    });
    expect(mocks.navigate).toHaveBeenCalledWith("/workspaces");
  });

  it("disables GitHub repository picking on scoped hosts", () => {
    setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel, {
      props: { firstRun: false, hostKey: "epyc" },
    });

    const button = screen.getByRole("button", {
      name: /Connect a GitHub repository/i,
    }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(
      screen.getByText("Pick from GitHub is only available for the local host. Use Clone a repository for this host."),
    ).toBeTruthy();
    expect(mocks.listUserRepositories).not.toHaveBeenCalled();
  });

  it("falls back to injected workspace host metadata", async () => {
    mocks.loadSnapshotHosts.mockRejectedValue(new Error("snapshot down"));
    setupConfig({ ghAuthed: true });
    win.__middleman_config.workspace = {
      selectedHostKey: "local",
      selectedWorktreeKey: null,
      hosts: [
        {
          key: "local",
          label: "Local",
          connectionState: "connected",
          platform: "github",
          projects: [],
          sessions: [],
          resources: null,
        },
        {
          key: "epyc",
          label: "EPYC",
          connectionState: "connected",
          platform: "github",
          projects: [],
          sessions: [],
          resources: null,
        },
      ],
    };
    win.__middleman_notify_config_changed?.();

    render(WorkspaceFirstRunPanel, {
      props: { firstRun: false, hostKey: "epyc" },
    });

    expect(screen.getByText("Add a project.")).toBeTruthy();
    expect(await screen.findByText("Host: EPYC")).toBeTruthy();
  });

  it("clones a Git URL and notifies the host", async () => {
    mocks.cloneProject.mockResolvedValue(project("prj_clone"));
    const command = setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(screen.getByRole("button", { name: /Clone a repository/i }));
    await fireEvent.input(screen.getByLabelText("Repository URL"), {
      target: { value: "git@github.com:octo/repo.git" },
    });
    await fireEvent.input(screen.getByLabelText("Destination path"), {
      target: { value: "/tmp/repo" },
    });
    await fireEvent.input(screen.getByLabelText("Branch"), {
      target: { value: "main" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Clone repository" }));

    await waitFor(() => {
      expect(mocks.cloneProject).toHaveBeenCalledWith("git@github.com:octo/repo.git", "/tmp/repo", "main");
    });
    expect(command).toHaveBeenCalledWith("project-registered", {
      projectId: "prj_clone",
    });
  });

  it("loads GitHub repositories and clones the selected repository", async () => {
    mocks.listUserRepositories.mockResolvedValue([
      {
        name_with_owner: "octo/one",
        ssh_url: "git@github.com:octo/one.git",
        default_branch: "main",
      },
      {
        name_with_owner: "octo/two",
        ssh_url: "git@github.com:octo/two.git",
        default_branch: "trunk",
      },
    ]);
    mocks.cloneProject.mockResolvedValue(project("prj_gh"));
    setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Connect a GitHub repository/i,
      }),
    );
    await fireEvent.click(await screen.findByRole("combobox", { name: /GitHub repository/ }));
    await fireEvent.click(screen.getByRole("option", { name: "octo/two" }));
    await fireEvent.input(screen.getByLabelText("Destination path"), {
      target: { value: "/tmp/two" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Clone repository" }));

    await waitFor(() => {
      expect(mocks.cloneProject).toHaveBeenCalledWith("git@github.com:octo/two.git", "/tmp/two", "");
    });
  });

  it("clones the repository shown after a filter hides the default selection", async () => {
    mocks.listUserRepositories.mockResolvedValue([
      {
        name_with_owner: "octo/one",
        ssh_url: "git@github.com:octo/one.git",
        default_branch: "main",
      },
      {
        name_with_owner: "octo/two",
        ssh_url: "git@github.com:octo/two.git",
        default_branch: "trunk",
      },
    ]);
    mocks.cloneProject.mockResolvedValue(project("prj_filtered"));
    setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Connect a GitHub repository/i,
      }),
    );
    // The first repo (octo/one) is selected by default. Filtering to the other
    // repo hides that selection; without an explicit pick, submit must clone
    // the repo now shown (octo/two), not the stale default.
    await fireEvent.input(await screen.findByLabelText("Filter repositories"), {
      target: { value: "two" },
    });
    await fireEvent.input(screen.getByLabelText("Destination path"), {
      target: { value: "/tmp/two" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Clone repository" }));

    await waitFor(() => {
      expect(mocks.cloneProject).toHaveBeenCalledWith("git@github.com:octo/two.git", "/tmp/two", "");
    });
  });

  it("surfaces host callback failures after registration", async () => {
    mocks.registerExistingProject.mockResolvedValue(project("prj_existing"));
    setupConfig({
      ghAuthed: true,
      onWorkspaceCommand: vi.fn().mockResolvedValue({
        ok: false,
        message: "refresh failed",
      }),
    });
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Add an existing local repository/i,
      }),
    );
    await fireEvent.input(screen.getByLabelText("Repository path"), {
      target: { value: "/tmp/repo" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByText("refresh failed")).toBeTruthy();
    expect(mocks.navigate).not.toHaveBeenCalled();
  });

  it("renders the tooling status block beneath the actions", () => {
    setupConfig({ ghAuthed: true });
    render(WorkspaceFirstRunPanel);
    expect(screen.getByLabelText("Tooling status")).toBeTruthy();
  });

  it("shows GitLab CLI status for a selected GitLab workspace host", () => {
    setupConfig({ ghAuthed: true });
    win.__middleman_config.workspace = {
      selectedHostKey: "gitlab-main",
      selectedWorktreeKey: null,
      hosts: [
        {
          key: "gitlab-main",
          label: "GitLab",
          connectionState: "connected",
          platform: "gitlab",
          projects: [],
          sessions: [],
          resources: null,
        },
      ],
    };
    win.__middleman_config.embed.tooling.glab = {
      available: true,
      authenticated: true,
      user: "wesm",
      host: "gitlab.com",
    };
    win.__middleman_notify_config_changed?.();

    render(WorkspaceFirstRunPanel);

    expect(screen.getByText("glab")).toBeTruthy();
    expect(screen.queryByText("gh")).toBeNull();
  });
});
