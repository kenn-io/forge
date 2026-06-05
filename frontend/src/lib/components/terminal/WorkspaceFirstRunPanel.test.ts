import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceFirstRunPanel from "./WorkspaceFirstRunPanel.svelte";

const win = window as any;

interface SetupArgs {
  ghAuthed: boolean;
  ghAvailable?: boolean;
  handlers?: Partial<Record<string, (ctx: unknown) => CommandResult | Promise<CommandResult>>>;
}

function setupConfig({ ghAuthed, ghAvailable = true, handlers = {} }: SetupArgs): void {
  win.__middleman_config = {
    actions: {
      project: [
        {
          id: "add-existing",
          label: "Add Existing",
          handler: handlers["add-existing"] ?? vi.fn().mockResolvedValue({ ok: true }),
        },
        {
          id: "clone",
          label: "Clone",
          handler: handlers["clone"] ?? vi.fn().mockResolvedValue({ ok: true }),
        },
        {
          id: "connect-github",
          label: "Connect GH",
          handler: handlers["connect-github"] ?? vi.fn().mockResolvedValue({ ok: true }),
        },
      ],
    },
    embed: {
      tooling: {
        git: { available: true, version: "2.45.0" },
        gh: { available: ghAvailable, authenticated: ghAuthed },
      },
    },
  };
  win.__middleman_notify_config_changed?.();
}

describe("WorkspaceFirstRunPanel", () => {
  beforeEach(() => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
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

  it("invokes the action handler when the user clicks", async () => {
    const handler = vi.fn().mockResolvedValue({ ok: true });
    setupConfig({
      ghAuthed: true,
      handlers: { "add-existing": handler },
    });
    render(WorkspaceFirstRunPanel);

    const button = screen.getByRole("button", {
      name: /Add an existing local repository/i,
    });
    await fireEvent.click(button);
    expect(handler).toHaveBeenCalledWith({
      surface: "first-run-panel",
    });
  });

  it("renders the failure message when an action returns ok: false", async () => {
    const handler = vi.fn().mockResolvedValue({
      ok: false,
      message: "Couldn't reach the remote",
    });
    setupConfig({ ghAuthed: true, handlers: { clone: handler } });
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(screen.getByRole("button", { name: /Clone a repository/i }));
    expect(await screen.findByText("Couldn't reach the remote")).toBeTruthy();
  });

  it("renders an upgrade-host hint when the action is not registered", async () => {
    win.__middleman_config = {
      actions: { project: [] },
      embed: {
        tooling: {
          git: { available: true },
          gh: { available: true, authenticated: true },
        },
      },
    };
    win.__middleman_notify_config_changed?.();
    render(WorkspaceFirstRunPanel);

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Add an existing local repository/i,
      }),
    );
    expect(await screen.findByText(/not available in this build/i)).toBeTruthy();
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
