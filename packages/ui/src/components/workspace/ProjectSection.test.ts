import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import type { WorkspaceProject, WorkspaceWorktree } from "../../api/types.js";
import ProjectSection from "./ProjectSection.svelte";

function createWorktree(isStale: boolean): WorkspaceWorktree {
  return {
    key: "worktree-1",
    name: "feature-auth",
    branch: "feature/auth",
    isPrimary: false,
    isHidden: false,
    isStale,
    sessionBackend: null,
    linkedPR: null,
    activity: {
      state: "idle",
      lastOutputAt: null,
    },
    diff: null,
  };
}

function createProject(isStale: boolean): WorkspaceProject {
  return {
    key: "middleman",
    name: "middleman",
    kind: "repository",
    repoKind: "git",
    defaultBranch: "main",
    platformRepo: "kenn-io/middleman",
    worktrees: [createWorktree(isStale)],
  };
}

function renderSection(project: WorkspaceProject): void {
  render(ProjectSection, {
    props: {
      project,
      hostKey: "local",
      selectedWorktreeKey: null,
      onCommand: () => {},
    },
  });
}

describe("ProjectSection", () => {
  afterEach(() => {
    cleanup();
    localStorage.clear();
  });

  it("uses the shared stale status when any worktree is stale", () => {
    renderSection(createProject(true));

    expect(screen.getByLabelText("Has stale worktrees").classList.contains("kit-status-dot--stale")).toBe(true);
  });

  it("omits the stale status when all worktrees are current", () => {
    renderSection(createProject(false));

    expect(screen.queryByLabelText("Has stale worktrees")).toBeNull();
  });
});
