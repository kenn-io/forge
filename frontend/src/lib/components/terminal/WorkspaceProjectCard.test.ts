import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceProjectCard from "./WorkspaceProjectCard.svelte";

const win = window as any;

const projectGet = vi.fn();
const worktreesGet = vi.fn();

vi.mock("../../api/runtime.ts", () => ({
  apiErrorMessage: (error: { detail?: string; title?: string } | undefined, fallback: string) =>
    error?.detail ?? error?.title ?? fallback,
  client: {
    GET: vi.fn((path: string, options) => {
      if (path === "/projects/{project_id}") {
        return projectGet(options);
      }
      if (path === "/projects/{project_id}/worktrees") {
        return worktreesGet(options);
      }
      throw new Error(`unexpected GET path ${path}`);
    }),
  },
}));

interface ProjectFixture {
  id: string;
  display_name: string;
  local_path: string;
  default_branch?: string;
  platform_identity?: {
    platform?: string;
    platform_host: string;
    owner: string;
    name: string;
  };
}

function setProjectResponse(project: ProjectFixture | { error: string }): void {
  projectGet.mockReset();
  if ("error" in project) {
    projectGet.mockResolvedValue({
      error: { detail: project.error },
      data: undefined,
    });
    return;
  }
  projectGet.mockResolvedValue({ data: project, error: undefined });
}

function setWorktreesResponse(
  worktrees: Array<{
    id: string;
    project_id: string;
    branch: string;
    path: string;
  }>,
): void {
  worktreesGet.mockReset();
  worktreesGet.mockResolvedValue({
    data: { worktrees },
    error: undefined,
  });
}

describe("WorkspaceProjectCard", () => {
  beforeEach(() => {
    delete win.__middleman_config;
  });

  afterEach(() => {
    cleanup();
    projectGet.mockReset();
    worktreesGet.mockReset();
  });

  it("renders project metadata and the create-first-worktree CTA when empty", async () => {
    setProjectResponse({
      id: "prj_1",
      display_name: "myrepo",
      local_path: "/Users/wesm/code/myrepo",
      default_branch: "main",
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });

    expect(await screen.findByText("myrepo")).toBeTruthy();
    expect(screen.getByText("/Users/wesm/code/myrepo")).toBeTruthy();
    expect(screen.getByText("main")).toBeTruthy();
    expect(screen.getByText("This project has no worktrees yet.")).toBeTruthy();
    expect(
      screen.getByRole("button", {
        name: /Create your first worktree/i,
      }),
    ).toBeTruthy();
  });

  it("hides the platform chip row when platform_identity is absent", async () => {
    setProjectResponse({
      id: "prj_1",
      display_name: "no-remote-repo",
      local_path: "/tmp/no-remote",
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });
    await screen.findByText("no-remote-repo");
    // Chip uses platform host / owner / name format; absence is the no-platform path.
    expect(screen.queryByText(/github\.com \/ /)).toBeNull();
  });

  it("renders platform identity from platform_host", async () => {
    setProjectResponse({
      id: "prj_1",
      display_name: "remote-repo",
      local_path: "/tmp/remote-repo",
      platform_identity: {
        platform_host: "gitlab.example.com",
        owner: "group/subgroup",
        name: "project",
      },
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });

    expect(await screen.findByText("gitlab.example.com / group/subgroup / project")).toBeTruthy();
  });

  it("renders provider brand icon beside platform identity when platform is present", async () => {
    setProjectResponse({
      id: "prj_1",
      display_name: "remote-repo",
      local_path: "/tmp/remote-repo",
      platform_identity: {
        platform: "gitlab",
        platform_host: "gitlab.example.com",
        owner: "group/subgroup",
        name: "project",
      },
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });

    expect(await screen.findByRole("img", { name: "GitLab" })).toBeTruthy();
  });

  it("renders existing worktrees and switches the CTA label", async () => {
    setProjectResponse({
      id: "prj_1",
      display_name: "myrepo",
      local_path: "/tmp/myrepo",
    });
    setWorktreesResponse([
      {
        id: "wtr_1",
        project_id: "prj_1",
        branch: "feature-x",
        path: "/tmp/myrepo-worktrees/feature-x",
      },
    ]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });
    await screen.findByText("feature-x");
    expect(screen.getByText("/tmp/myrepo-worktrees/feature-x")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Create another worktree/i })).toBeTruthy();
  });

  it("renders an error and a retry button when the project fetch fails", async () => {
    setProjectResponse({ error: "project not found" });
    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });
    expect(await screen.findByText("project not found")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Retry/i })).toBeTruthy();
  });

  it("invokes the new-worktree action with the project id when clicked", async () => {
    const newWorktreeHandler = vi.fn().mockResolvedValue({ ok: true });
    win.__middleman_config = {
      actions: {
        project: [
          {
            id: "new-worktree",
            label: "New Worktree",
            handler: newWorktreeHandler,
          },
        ],
      },
    };
    win.__middleman_notify_config_changed?.();

    setProjectResponse({
      id: "prj_1",
      display_name: "myrepo",
      local_path: "/tmp/myrepo",
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });
    await screen.findByText("myrepo");

    await fireEvent.click(
      screen.getByRole("button", {
        name: /Create your first worktree/i,
      }),
    );
    expect(newWorktreeHandler).toHaveBeenCalledWith({
      surface: "project-card",
      projectId: "prj_1",
    });
  });

  it("surfaces a failure message when the new-worktree action returns ok: false", async () => {
    win.__middleman_config = {
      actions: {
        project: [
          {
            id: "new-worktree",
            label: "New Worktree",
            handler: () =>
              Promise.resolve({
                ok: false,
                message: "user cancelled the sheet",
              }),
          },
        ],
      },
    };
    win.__middleman_notify_config_changed?.();

    setProjectResponse({
      id: "prj_1",
      display_name: "myrepo",
      local_path: "/tmp/myrepo",
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });
    await screen.findByText("myrepo");
    await fireEvent.click(
      screen.getByRole("button", {
        name: /Create your first worktree/i,
      }),
    );
    expect(await screen.findByText("user cancelled the sheet")).toBeTruthy();
  });

  it("renders an upgrade-host hint when the new-worktree action is missing", async () => {
    win.__middleman_config = { actions: { project: [] } };
    win.__middleman_notify_config_changed?.();

    setProjectResponse({
      id: "prj_1",
      display_name: "myrepo",
      local_path: "/tmp/myrepo",
    });
    setWorktreesResponse([]);

    render(WorkspaceProjectCard, { props: { projectId: "prj_1" } });
    await screen.findByText("myrepo");
    await fireEvent.click(
      screen.getByRole("button", {
        name: /Create your first worktree/i,
      }),
    );
    expect(await screen.findByText(/not available in this build/i)).toBeTruthy();
  });
});
