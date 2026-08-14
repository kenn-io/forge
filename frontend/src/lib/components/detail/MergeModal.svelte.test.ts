import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockMergePull = vi.hoisted(() => vi.fn());

vi.mock("../../context.js", () => ({
  getStores: () => ({ detail: { mergePull: mockMergePull } }),
}));

import MergeModal from "./MergeModal.svelte";
import * as flash from "../../stores/flash.svelte.js";
import { getStackDepth, getTopFrame, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";
import {
  isWorkspaceDeletionPending,
  isWorkspaceIdDeleted,
  resetWorkspaceCreatePendingForTest,
} from "../../stores/workspace-create-pending.svelte.js";

const baseProps = {
  owner: "octo",
  name: "repo",
  number: 1,
  provider: "github",
  platformHost: "github.com",
  repoPath: "octo/repo",
  prTitle: "Add feature",
  prBody: "Body",
  prAuthor: "octo",
  prAuthorDisplayName: "Octo",
  allowSquash: true,
  allowMerge: true,
  allowRebase: true,
  onclose: () => {},
  onmerged: () => {},
  onqueued: () => {},
};

describe("MergeModal modal frame integration", () => {
  beforeEach(() => {
    resetModalStack();
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
  });

  it("pushes a frame on mount and pops on unmount", () => {
    expect(getStackDepth()).toBe(0);
    const { unmount } = render(MergeModal, { props: baseProps });
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("merge-modal");
    unmount();
    expect(getStackDepth()).toBe(0);
  });

  it("warns when the override permits a mid-stack merge", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        midStackWarning: "This is stack position 2 of 3. Branch #1 below it has not been merged.",
      },
    });

    const warning = screen.getByRole("alert");
    expect(warning.textContent).toContain("Warning: this is a mid-stack merge.");
    expect(warning.textContent).toContain("Branch #1 below it has not been merged.");
  });
});

describe("MergeModal acknowledged merge commands", () => {
  beforeEach(() => {
    resetModalStack();
    mockMergePull.mockReset();
    resetWorkspaceCreatePendingForTest();
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onSuccess?: (outcome: object) => void; onSettled?: () => void };
      callbacks.onSuccess?.(args[3] === true ? { _tag: "Queued" } : { _tag: "Merged" });
      callbacks.onSettled?.();
    });
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    resetWorkspaceCreatePendingForTest();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
  });

  function renderModal(props: Partial<Record<string, unknown>> = {}) {
    return render(MergeModal, { props: { ...baseProps, ...props } });
  }

  async function confirmMerge(): Promise<void> {
    await fireEvent.click(screen.getByText("Squash and merge", { selector: ".kit-modal-footer button" }));
  }

  it("echoes the reviewed head in the generated merge body", async () => {
    renderModal({ expectedHeadSha: "abc123" });

    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[2]).toMatchObject({
      expected_head_sha: "abc123",
      method: "squash",
    });
  });

  it("offers workspace cleanup by default and includes the workspace in an immediate merge", async () => {
    renderModal({ workspaceId: "ws-1" });

    expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "Delete workspace after merge" }).checked).toBe(true);
    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[2]).toMatchObject({ delete_workspace_id: "ws-1" });
  });

  it("omits workspace cleanup when the user turns it off", async () => {
    renderModal({ workspaceId: "ws-1" });

    await fireEvent.click(screen.getByRole("checkbox", { name: "Delete workspace after merge" }));
    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[2]).not.toHaveProperty("delete_workspace_id");
  });

  it("includes the workspace when scheduling a deferred merge", async () => {
    renderModal({ workspaceId: "ws-1", deferUntilChecksPass: true });

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI is complete" }));

    expect(mockMergePull.mock.calls[0]?.[2]).toMatchObject({ delete_workspace_id: "ws-1" });
  });

  it("closes after merge acknowledgement without claiming cleanup finished", async () => {
    let succeed = () => {};
    let settle = () => {};
    const onmerged = vi.fn();
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as {
        onSuccess?: (outcome: { _tag: "Merged"; cleanupWarning?: string }) => void;
        onSettled?: () => void;
      };
      succeed = () => callbacks.onSuccess?.({ _tag: "Merged" });
      settle = () => callbacks.onSettled?.();
    });
    renderModal({ workspaceId: "ws-1", onmerged });

    await confirmMerge();
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);

    succeed();
    expect(isWorkspaceIdDeleted("ws-1")).toBe(false);
    expect(onmerged).toHaveBeenCalledWith(undefined);
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);

    settle();
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);
  });

  it("preserves the workspace when merge cleanup returns a warning", async () => {
    const onmerged = vi.fn();
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as {
        onSuccess?: (outcome: { _tag: "Merged"; cleanupWarning?: string }) => void;
        onSettled?: () => void;
      };
      callbacks.onSuccess?.({ _tag: "Merged", cleanupWarning: "workspace has uncommitted changes" });
      callbacks.onSettled?.();
    });
    renderModal({ workspaceId: "ws-1", onmerged });

    await confirmMerge();

    expect(isWorkspaceIdDeleted("ws-1")).toBe(false);
    expect(onmerged).toHaveBeenCalledWith("workspace has uncommitted changes");
  });

  it("omits the head pin when the rendered head is unknown", async () => {
    renderModal();

    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[2]).not.toHaveProperty("expected_head_sha");
  });

  it.each(["stale_state", "head_unknown", "not_open", "head_repo_unknown"] as const)(
    "closes and reports the %s conflict instead of leaving a stale retry open",
    async (reason) => {
      mockMergePull.mockImplementation((...args: unknown[]) => {
        const callbacks = args.at(-1) as {
          onProblem?: (problem: unknown) => void;
          onFailure?: (message: string) => void;
          onSettled?: () => void;
        };
        callbacks.onProblem?.({
          type: "about:blank",
          title: "Conflict",
          status: 409,
          detail: "target changed since it was reviewed; refresh and retry",
          code: "conflict",
          details: { reason },
        });
        callbacks.onFailure?.("target changed since it was reviewed; refresh and retry");
        callbacks.onSettled?.();
      });
      const onclose = vi.fn();
      const onstateconflict = vi.fn();
      const onmerged = vi.fn();
      renderModal({
        expectedHeadSha: "abc123",
        routeGeneration: 12,
        onclose,
        onstateconflict,
        onmerged,
      });

      await confirmMerge();

      expect(onstateconflict).toHaveBeenCalledWith(
        reason,
        undefined,
        "abc123",
        {
          provider: "github",
          platformHost: "github.com",
          owner: "octo",
          name: "repo",
          repoPath: "octo/repo",
        },
        1,
        12,
      );
      expect(onclose).toHaveBeenCalledOnce();
      expect(onmerged).not.toHaveBeenCalled();
    },
  );

  it("shows the provider message inline for generic merge conflicts", async () => {
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as {
        onProblem?: (problem: unknown) => void;
        onFailure?: (message: string) => void;
        onSettled?: () => void;
      };
      callbacks.onProblem?.({
        type: "about:blank",
        title: "Conflict",
        status: 409,
        detail: "merge blocked by provider",
        code: "conflict",
        details: { reason: "conflict" },
      });
      callbacks.onFailure?.("merge blocked by provider");
      callbacks.onSettled?.();
    });
    const onclose = vi.fn();
    renderModal({ expectedHeadSha: "abc123", onclose });

    await confirmMerge();

    expect(await screen.findByText("merge blocked by provider")).toBeTruthy();
    expect(onclose).not.toHaveBeenCalled();
  });

  it("shows non-conflict problem failures through the shared flash", async () => {
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as {
        onProblem?: (problem: unknown) => void;
        onFailure?: (message: string) => void;
        onSettled?: () => void;
      };
      callbacks.onProblem?.({
        type: "about:blank",
        title: "Invalid merge",
        detail: "commit title is required",
        code: "validationError",
      });
      callbacks.onFailure?.("commit title is required");
      callbacks.onSettled?.();
    });
    renderModal();

    await confirmMerge();

    expect(flash.getFlash()).toMatchObject({ message: "commit title is required", tone: "danger" });
  });

  it("routes a deferred merge and reports its acknowledgement", async () => {
    const onqueued = vi.fn();
    const onmerged = vi.fn();
    renderModal({ workspaceId: "ws-1", deferUntilChecksPass: true, onqueued, onmerged });

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI is complete" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);
    expect(onqueued).toHaveBeenCalledOnce();
    expect(onmerged).not.toHaveBeenCalled();
    expect(isWorkspaceIdDeleted("ws-1")).toBe(false);
  });

  it("offers an immediate merge override while CI is pending", async () => {
    const onmerged = vi.fn();
    renderModal({ deferUntilChecksPass: true, onmerged });

    await fireEvent.click(screen.getByRole("button", { name: "Merge Anyway" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
    expect(onmerged).toHaveBeenCalledOnce();
  });

  it("keeps the merge action disabled until its acknowledgement settles", async () => {
    let settle = () => {};
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onSuccess?: (outcome: object) => void; onSettled?: () => void };
      settle = () => {
        callbacks.onSuccess?.({ _tag: "Queued" });
        callbacks.onSettled?.();
      };
    });
    renderModal({ deferUntilChecksPass: true });

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI is complete" }));

    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Merge scheduled..." }).disabled).toBe(true);
    settle();
    await waitFor(() => expect(screen.getByRole("button", { name: "Merge after CI is complete" })).toBeTruthy());
  });

  it("offers only an immediate merge when a deferred merge is already queued", async () => {
    const onmerged = vi.fn();
    renderModal({ deferUntilChecksPass: true, alreadyQueued: true, onmerged });

    expect(screen.queryByRole("button", { name: "Merge after CI is complete" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Merge Anyway" })).toBeNull();
    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
    expect(onmerged).toHaveBeenCalledOnce();
  });
});
