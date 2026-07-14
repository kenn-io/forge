import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { WorkspaceWorktree } from "../../api/types.js";
import WorktreeRow from "./WorktreeRow.svelte";

function createWorktree(): WorkspaceWorktree {
  return {
    key: "worktree-1",
    name: "feature-auth",
    branch: "feature/auth",
    isPrimary: false,
    isHidden: false,
    isStale: false,
    sessionBackend: null,
    linkedPR: {
      number: 42,
      title: "Add auth middleware",
      state: "open",
      checksStatus: "success",
      updatedAt: "2026-04-10T12:00:00Z",
    },
    activity: {
      state: "idle",
      lastOutputAt: null,
    },
    diff: {
      added: 12,
      removed: 3,
    },
  };
}

describe("WorktreeRow", () => {
  afterEach(() => {
    cleanup();
  });

  it.each([
    ["idle", "Worktree idle", "kit-status-dot--idle"],
    ["active", "Worktree active", "kit-status-dot--working"],
    ["running", "Worktree running", "kit-status-dot--working"],
    ["needsAttention", "Worktree needs attention", "kit-status-dot--unclean"],
  ] as const)("maps %s activity to the shared semantic status", (state, label, className) => {
    const worktree = createWorktree();
    worktree.activity.state = state;

    render(WorktreeRow, {
      props: {
        worktree,
        hostKey: "local",
        projectKey: "middleman",
        isSelected: false,
        onCommand: () => {},
      },
    });

    expect(screen.getByLabelText(label).classList.contains(className)).toBe(true);
  });

  it("renders the linked PR chip as passive metadata that still activates the row", async () => {
    const onCommand = vi.fn();

    render(WorktreeRow, {
      props: {
        worktree: createWorktree(),
        hostKey: "local",
        projectKey: "middleman",
        isSelected: false,
        onCommand,
      },
    });

    const chip = screen.getByTitle("PR #42");

    expect(chip.tagName).toBe("SPAN");
    expect(chip.getAttribute("tabindex")).toBeNull();

    await fireEvent.click(chip);

    expect(onCommand).toHaveBeenCalledWith("selectWorktree", {
      hostKey: "local",
      projectKey: "middleman",
      worktreeKey: "worktree-1",
    });
  });
});
