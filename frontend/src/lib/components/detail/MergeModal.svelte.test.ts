import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
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
  repositoryKey: { kind: "id", id: 7001 },
  repoPath: "octo/repo",
  prTitle: "Add feature",
  prBody: "Body",
  prAuthor: "octo",
  prAuthorDisplayName: "Octo",
  allowSquash: true,
  allowMerge: true,
  allowRebase: true,
  onclose: () => {},
};

function normalized(text: string | null): string {
  return (text ?? "").replace(/\s+/g, " ").trim();
}

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

  it("shows stack context when merging", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        stackNote: "Stack position 2 of 3.",
      },
    });

    expect(screen.getByText("Stack position 2 of 3.").tagName).toBe("P");
  });

  it("names every pull request a native stack merge lands and the ones that stay open", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 12,
        stackNote: "Stack position 2 of 3.",
        nativeStackMerge: { baseBranch: "main", landing: [11, 12], remaining: [13] },
      },
    });

    expect(screen.getByRole("dialog", { name: "Merge Stack" })).toBeTruthy();
    expect(normalized(screen.getByText(/This merges 2 pull requests into/).textContent)).toBe(
      "This merges 2 pull requests into main as one GitHub stack merge: #11 and #12.",
    );
    expect(screen.getByText(/GitHub merges the stack asynchronously/)).toBeTruthy();
    expect(screen.getByText(/#13\s+stays open/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Squash and merge 2 pull requests" })).toBeTruthy();
    expect(screen.queryByText("Stack position 2 of 3.")).toBeNull();
  });

  it("says only the bottom pull request merges when nothing below it is unmerged", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 11,
        nativeStackMerge: { baseBranch: "main", landing: [11], remaining: [12, 13] },
      },
    });

    expect(screen.getByRole("dialog", { name: "Merge Pull Request" })).toBeTruthy();
    expect(screen.getByText("Only this pull request merges.")).toBeTruthy();
    expect(screen.getByText(/#12 and #13\s+stay open/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Squash and merge" })).toBeTruthy();
  });

  it("describes a native stack entering the merge queue as one group", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 13,
        mergeQueue: { branch: "main", mergeMethod: "squash" },
        nativeStackMerge: { baseBranch: "main", landing: [11, 12, 13], remaining: [] },
      },
    });

    expect(screen.getByRole("dialog", { name: "Add Stack to Merge Queue" })).toBeTruthy();
    expect(normalized(screen.getByText(/This adds 3 pull requests to the merge queue/).textContent)).toBe(
      "This adds 3 pull requests to the merge queue as one group: #11, #12, and #13.",
    );
    expect(screen.queryByText(/asynchronously/)).toBeNull();
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

  it("submits the stable repository identity and reviewed head", async () => {
    renderModal({ expectedHeadSha: "abc123" });

    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[0]).toMatchObject({ repositoryKey: { kind: "id", id: 7001 } });
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

  it("closes on submission without waiting for acknowledgement or claiming cleanup finished", async () => {
    mockMergePull.mockImplementation(() => {});
    const onclose = vi.fn();
    renderModal({ workspaceId: "ws-1", onclose });

    await confirmMerge();

    expect(onclose).toHaveBeenCalledOnce();
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);
    expect(isWorkspaceIdDeleted("ws-1")).toBe(false);
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
      renderModal({
        expectedHeadSha: "abc123",
        routeGeneration: 12,
        onclose,
        onstateconflict,
      });

      await confirmMerge();

      expect(onstateconflict).toHaveBeenCalledWith(
        reason,
        undefined,
        "abc123",
        {
          provider: "github",
          platformHost: "github.com",
          repositoryKey: { kind: "id", id: 7001 },
          owner: "octo",
          name: "repo",
          repoPath: "octo/repo",
        },
        1,
        12,
      );
      expect(onclose).toHaveBeenCalledOnce();
    },
  );

  it("shows generic merge conflicts through the shared flash after closing", async () => {
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

    expect(flash.getFlash()).toMatchObject({ message: "octo/repo #1: merge blocked by provider", tone: "danger" });
    expect(onclose).toHaveBeenCalledOnce();
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

    expect(flash.getFlash()).toMatchObject({ message: "octo/repo #1: commit title is required", tone: "danger" });
  });

  it("routes a deferred merge and closes on submission", async () => {
    const onclose = vi.fn();
    renderModal({ workspaceId: "ws-1", deferUntilChecksPass: true, onclose });

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI is complete" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);
    expect(onclose).toHaveBeenCalledOnce();
    expect(isWorkspaceIdDeleted("ws-1")).toBe(false);
  });

  it("offers an immediate merge override while CI is pending", async () => {
    renderModal({ deferUntilChecksPass: true });

    await fireEvent.click(screen.getByRole("button", { name: "Merge Anyway" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  it("requires an explicit merge anyway action when CI has failed", async () => {
    renderModal({ ciFailed: true });

    expect(screen.getByRole("alert").textContent).toContain("CI has failed.");
    expect(mockMergePull).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Merge Anyway" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  it("explains that a draft must be marked ready and does not submit it", async () => {
    renderModal({ isDraft: true, deferUntilChecksPass: true });

    expect(normalized(screen.getByRole("alert").textContent)).toBe(
      "This pull request is a draft. Mark it ready for review before merging.",
    );
    const mergeAfterCI = screen.getByRole("button", { name: "Merge after CI is complete" });
    const mergeAnyway = screen.getByRole("button", { name: "Merge Anyway" });
    expect((mergeAfterCI as HTMLButtonElement).disabled).toBe(true);
    expect((mergeAnyway as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(mergeAfterCI);
    await fireEvent.click(mergeAnyway);

    expect(mockMergePull).not.toHaveBeenCalled();
  });

  it("offers only an immediate merge when a deferred merge is already queued", async () => {
    renderModal({ deferUntilChecksPass: true, alreadyQueued: true });

    expect(screen.queryByRole("button", { name: "Merge after CI is complete" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Merge Anyway" })).toBeNull();
    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  it("offers no immediate merge override when the provider blocks merging now", async () => {
    renderModal({ deferUntilChecksPass: true, immediateMergeBlocked: true });

    expect(screen.queryByRole("button", { name: "Merge Anyway" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI is complete" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);
  });

  it("disables the immediate merge when the provider blocks merging now", async () => {
    renderModal({ deferUntilChecksPass: true, alreadyQueued: true, immediateMergeBlocked: true });

    const merge = screen.getByRole<HTMLButtonElement>("button", { name: "Squash and merge" });
    expect(merge.disabled).toBe(true);
    await fireEvent.click(merge);

    expect(mockMergePull).not.toHaveBeenCalled();
  });

  it("merges over blocking rules only after the user chooses to bypass them", async () => {
    renderModal({ immediateMergeBlocked: true, canBypassRules: true });

    const merge = screen.getByRole<HTMLButtonElement>("button", { name: "Squash and merge" });
    expect(merge.disabled).toBe(true);
    await fireEvent.click(merge);
    expect(mockMergePull).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("checkbox", { name: "Bypass branch protection" }));
    expect(merge.disabled).toBe(false);
    expect(merge.classList).toContain("kit-button--danger");
    await fireEvent.click(merge);

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  it("enables the immediate override while CI is pending only after the user chooses to bypass", async () => {
    renderModal({ deferUntilChecksPass: true, immediateMergeBlocked: true, canBypassRules: true });

    const mergeAnyway = screen.getByRole<HTMLButtonElement>("button", { name: "Merge Anyway" });
    expect(mergeAnyway.disabled).toBe(true);

    await fireEvent.click(screen.getByRole("checkbox", { name: "Bypass branch protection" }));
    expect(mergeAnyway.classList).toContain("kit-button--danger");
    await fireEvent.click(mergeAnyway);

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  describe("when the base branch uses a merge queue", () => {
    const mergeQueue = { branch: "main", mergeMethod: "squash" };

    it("explains the queue and hides method and commit text", () => {
      renderModal({ mergeQueue, workspaceId: "ws-1" });

      expect(screen.getByText("Add to Merge Queue")).toBeTruthy();
      expect(
        screen.getByText(
          /uses a merge queue\. This adds the pull request to the queue, which merges it using squash and merge once its checks pass\./,
        ),
      ).toBeTruthy();
      expect(screen.queryByRole("group", { name: "Merge method" })).toBeNull();
      expect(screen.queryByLabelText("Commit title")).toBeNull();
      expect(screen.queryByLabelText("Commit message")).toBeNull();
    });

    it("omits the merge method while Forge has not read it", () => {
      renderModal({ mergeQueue: { branch: "main", mergeMethod: "" } });

      const note = screen.getByText(/uses a merge queue\./);
      expect(note.textContent?.replace(/\s+/g, " ")).toContain(
        "This adds the pull request to the queue, which merges it once its checks pass.",
      );
    });

    it("asks to delete the workspace once the queue merges the pull request", async () => {
      renderModal({ mergeQueue, workspaceId: "ws-1", expectedHeadSha: "abc123" });

      expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "Delete workspace after merge" }).checked).toBe(
        true,
      );
      await fireEvent.click(screen.getByRole("button", { name: "Add to merge queue" }));

      expect(mockMergePull).toHaveBeenCalledOnce();
      expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
      expect(mockMergePull.mock.calls[0]?.[2]).toMatchObject({
        expected_head_sha: "abc123",
        delete_workspace_id: "ws-1",
      });
    });

    it("keeps the workspace when deletion is unchecked", async () => {
      renderModal({ mergeQueue, workspaceId: "ws-1" });

      await fireEvent.click(screen.getByRole("checkbox", { name: "Delete workspace after merge" }));
      await fireEvent.click(screen.getByRole("button", { name: "Add to merge queue" }));

      expect(mockMergePull.mock.calls[0]?.[2]).not.toHaveProperty("delete_workspace_id");
    });

    it("offers queueing after CI with an immediate queue override while CI is pending", async () => {
      renderModal({ mergeQueue, deferUntilChecksPass: true });

      expect(screen.queryByRole("button", { name: "Merge Anyway" })).toBeNull();
      await fireEvent.click(screen.getByRole("button", { name: "Add to merge queue after CI is complete" }));
      expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);

      cleanup();
      renderModal({ mergeQueue, deferUntilChecksPass: true });
      await fireEvent.click(screen.getByRole("button", { name: "Add to queue now" }));
      expect(mockMergePull.mock.calls[1]?.[3]).toBe(false);
    });
  });
});
