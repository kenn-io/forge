import { beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { cloneProject, listUserRepositories, registerExistingProject } from "./project-intake.ts";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}));

vi.mock("./runtime.ts", () => ({
  apiErrorMessage: (error: { detail?: string; title?: string } | undefined, fallback: string) =>
    error?.detail ?? error?.title ?? fallback,
  client: {
    GET: mocks.get,
    POST: mocks.post,
  },
}));

describe("project-intake api", () => {
  beforeEach(() => {
    mocks.get.mockReset();
    mocks.post.mockReset();
  });

  it("validates an existing path before registering the root", async () => {
    mocks.get.mockResolvedValue({
      data: { is_valid: true, root_path: "/repo" },
      error: undefined,
    });
    mocks.post.mockResolvedValue({
      data: { id: "prj_1" },
      error: undefined,
    });

    await expect(registerExistingProject("/repo/subdir")).resolves.toMatchObject({
      id: "prj_1",
    });

    expect(mocks.get).toHaveBeenCalledWith("/filesystem/validate-repo", {
      params: { query: { path: "/repo/subdir" } },
    });
    expect(mocks.post).toHaveBeenCalledWith("/projects", { body: { local_path: "/repo" } });
  });

  it("uses fleet routes when registering on a host", async () => {
    mocks.get.mockResolvedValue({
      data: { is_valid: true, root_path: "/srv/repo" },
      error: undefined,
    });
    mocks.post.mockResolvedValue({
      data: { id: "prj_remote" },
      error: undefined,
    });

    await expect(registerExistingProject("/srv/repo/pkg", { hostKey: "epyc" })).resolves.toMatchObject({
      id: "prj_remote",
    });

    expect(mocks.get).toHaveBeenCalledWith("/fleet/hosts/{host_key}/filesystem/validate-repo", {
      params: {
        path: { host_key: "epyc" },
        query: { path: "/srv/repo/pkg" },
      },
    });
    expect(mocks.post).toHaveBeenCalledWith("/fleet/hosts/{host_key}/projects", {
      params: { path: { host_key: "epyc" } },
      body: { local_path: "/srv/repo" },
    });
  });

  it("rejects invalid repository paths before registering", async () => {
    mocks.get.mockResolvedValue({
      data: { is_valid: false, message: "Not a git repository" },
      error: undefined,
    });

    await expect(registerExistingProject("/tmp")).rejects.toThrow("Not a git repository");
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("posts clone body with an optional branch", async () => {
    mocks.post.mockResolvedValue({
      data: { id: "prj_clone" },
      error: undefined,
    });

    await expect(cloneProject(" git@github.com:octo/repo.git ", " /tmp/repo ", " main ")).resolves.toMatchObject({
      id: "prj_clone",
    });

    expect(mocks.post).toHaveBeenCalledWith("/projects/clone", {
      body: {
        url: "git@github.com:octo/repo.git",
        path: "/tmp/repo",
        branch: "main",
      },
    });
  });

  it("uses the fleet clone route when cloning on a host", async () => {
    mocks.post.mockResolvedValue({
      data: { id: "prj_remote_clone" },
      error: undefined,
    });

    await expect(
      cloneProject("git@github.com:octo/repo.git", "/srv/repo", undefined, { hostKey: "epyc" }),
    ).resolves.toMatchObject({ id: "prj_remote_clone" });

    expect(mocks.post).toHaveBeenCalledWith("/fleet/hosts/{host_key}/projects/clone", {
      params: { path: { host_key: "epyc" } },
      body: {
        url: "git@github.com:octo/repo.git",
        path: "/srv/repo",
      },
    });
  });

  it("normalizes a null repository list", async () => {
    mocks.get.mockResolvedValue({
      data: { repositories: null },
      error: undefined,
    });

    await expect(listUserRepositories()).resolves.toEqual([]);
  });
});
