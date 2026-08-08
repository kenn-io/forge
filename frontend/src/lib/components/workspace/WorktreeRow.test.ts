import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { WorkspaceWorktree } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";

const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("worktree row test runtime is not initialized");
    return runtime;
  },
}));

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
  beforeEach(() => {
    runtimeCapture.current = makeAppRuntime();
  });

  afterEach(async () => {
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    vi.useRealTimers();
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
        projectKey: "kenn-forge",
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
        projectKey: "kenn-forge",
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
      projectKey: "kenn-forge",
      worktreeKey: "worktree-1",
    });
  });

  it("does not request a hover card after the row unmounts", async () => {
    vi.useFakeTimers();
    const onCommand = vi.fn();
    const view = render(WorktreeRow, {
      props: {
        worktree: createWorktree(),
        hostKey: "local",
        projectKey: "kenn-forge",
        isSelected: false,
        hoverCardsEnabled: true,
        onCommand,
      },
    });

    const row = view.container.querySelector(".worktree-row");
    expect(row).toBeTruthy();
    await fireEvent.mouseEnter(row!);
    view.unmount();
    await vi.advanceTimersByTimeAsync(500);

    expect(onCommand).not.toHaveBeenCalled();
  });
});
