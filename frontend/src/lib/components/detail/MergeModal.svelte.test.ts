import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockMergePull = vi.hoisted(() => vi.fn());

vi.mock("../../context.js", () => ({
  getStores: () => ({ detail: { mergePull: mockMergePull } }),
}));

import type { StackContextResponse } from "../../api/generated/models/stackContextResponse.js";
import type { StackMemberResponse } from "../../api/generated/models/stackMemberResponse.js";
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

// A native stack whose members are open, passing CI, and stacked bottom
// first on main unless a member overrides that.
function nativeStack(members: Array<Partial<StackMemberResponse> & { number: number }>): StackContextResponse {
  return {
    health: "healthy",
    native: true,
    position: 1,
    size: members.length,
    stack_id: 1,
    stack_name: "feature",
    members: members.map((member, index) => ({
      base_branch: index === 0 ? "main" : `branch-${members[index - 1]?.number}`,
      blocked_by: null,
      ci_status: "success",
      is_draft: false,
      mergeable_state: "clean",
      position: index + 1,
      review_decision: "",
      reviewed_head_sha: `head-${member.number}`,
      state: "open",
      title: `Change ${member.number}`,
      ...member,
    })),
  };
}

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
        nativeStack: nativeStack([{ number: 11 }, { number: 12 }, { number: 13 }]),
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
        nativeStack: nativeStack([{ number: 11 }, { number: 12 }, { number: 13 }]),
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
        mergeQueue: true,
        nativeStack: nativeStack([{ number: 11 }, { number: 12 }, { number: 13 }]),
      },
    });

    expect(screen.getByRole("dialog", { name: "Add stack to merge queue" })).toBeTruthy();
    expect(normalized(screen.getByText(/Joins the queue as one group/).textContent)).toBe(
      "Joins the queue as one group: #11, #12, and #13.",
    );
    expect(screen.queryByText(/asynchronously/)).toBeNull();
  });
});

describe("MergeModal native stack target", () => {
  beforeEach(() => {
    resetModalStack();
    mockMergePull.mockReset();
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
  });

  async function chooseTarget(name: RegExp): Promise<void> {
    await fireEvent.click(screen.getByRole("combobox", { name: /Merge up to/ }));
    await fireEvent.click(screen.getByRole("option", { name }));
  }

  it("starts at this pull request and offers every open member", async () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 12,
        nativeStack: nativeStack([{ number: 10, state: "merged" }, { number: 11 }, { number: 12 }, { number: 13 }]),
      },
    });

    expect(screen.getByRole("combobox", { name: /Merge up to: #12/ })).toBeTruthy();
    await fireEvent.click(screen.getByRole("combobox", { name: /Merge up to/ }));
    expect(screen.getAllByRole("option").map((option) => normalized(option.textContent))).toEqual([
      "#11 Change 11",
      "#12 Change 12",
      "#13 Change 13",
    ]);
  });

  it("merges up to the chosen member with its reviewed head and keeps this pull request's workspace", async () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 12,
        expectedHeadSha: "head-12",
        requireHeadPin: true,
        workspaceId: "ws-12",
        nativeStack: nativeStack([{ number: 11 }, { number: 12 }, { number: 13 }]),
      },
    });

    await chooseTarget(/#11 Change 11/);
    expect(screen.getByText("Only this pull request merges.")).toBeTruthy();
    expect(screen.queryByRole("checkbox", { name: "Delete workspace after merge" })).toBeNull();
    await fireEvent.click(screen.getByText("Squash and merge", { selector: ".kit-modal-footer button" }));

    expect(mockMergePull.mock.calls[0]?.[1]).toBe(11);
    // The merge starts from #12's detail, which the store checks is shown.
    expect(mockMergePull.mock.calls[0]?.[5]).toBe(12);
    expect(mockMergePull.mock.calls[0]?.[2]).toEqual({
      commit_title: "Change 11 (#11)",
      commit_message: "",
      method: "squash",
      expected_head_sha: "head-11",
    });
  });

  it("merges a chosen member now once its CI passes while the dialog is open", async () => {
    const props = {
      ...baseProps,
      number: 12,
      expectedHeadSha: "head-12",
      nativeStack: nativeStack([{ number: 11, ci_status: "pending" }, { number: 12 }]),
    };
    const { rerender } = render(MergeModal, { props });
    await chooseTarget(/#11 Change 11/);
    expect(screen.getByRole("button", { name: "Merge after CI" })).toBeTruthy();

    await rerender({
      ...props,
      nativeStack: nativeStack([{ number: 11, reviewed_head_sha: "head-11-new" }, { number: 12 }]),
    });
    expect(screen.queryByRole("button", { name: "Merge after CI" })).toBeNull();
    await fireEvent.click(screen.getByText("Squash and merge", { selector: ".kit-modal-footer button" }));

    expect(mockMergePull.mock.calls[0]?.[1]).toBe(11);
    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
    // The head reviewed when the dialog opened stays pinned.
    expect(mockMergePull.mock.calls[0]?.[2]).toMatchObject({ expected_head_sha: "head-11" });
  });

  it("waits for CI on a member below when this pull request's own checks passed", async () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 12,
        expectedHeadSha: "head-12",
        nativeStack: nativeStack([{ number: 11, ci_status: "pending" }, { number: 12 }]),
      },
    });

    expect(normalized(screen.getByText(/CI is still running on/).textContent)).toBe(
      "CI is still running on #11. Forge merges the stack once every included pull request passes CI, and " +
        "stops waiting if it restarts.",
    );
    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI" }));

    expect(mockMergePull.mock.calls[0]?.[1]).toBe(12);
    expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);
  });

  it("names a member below whose CI failed instead of offering to wait", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 12,
        nativeStack: nativeStack([
          { number: 11, ci_status: "failure" },
          { number: 12, ci_status: "pending" },
        ]),
        deferUntilChecksPass: true,
      },
    });

    expect(normalized(screen.getByRole("alert").textContent)).toBe("CI failed on #11, which merges with #12.");
    expect(screen.queryByRole("button", { name: "Merge after CI" })).toBeNull();
    expect(screen.getByRole("button", { name: "Merge Anyway" })).toBeTruthy();
  });

  it("offers no choice in a stack with one open member", () => {
    render(MergeModal, {
      props: {
        ...baseProps,
        number: 12,
        nativeStack: nativeStack([{ number: 11, state: "merged" }, { number: 12 }]),
      },
    });

    expect(screen.queryByRole("combobox", { name: /Merge up to/ })).toBeNull();
  });
});

describe("MergeModal acknowledged merge commands", () => {
  beforeEach(() => {
    resetModalStack();
    mockMergePull.mockReset();
    resetWorkspaceCreatePendingForTest();
    mockMergePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args[4] as { onSuccess?: (outcome: object) => void; onSettled?: () => void };
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

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI" }));

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
        const callbacks = args[4] as {
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
      const callbacks = args[4] as {
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
      const callbacks = args[4] as {
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

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);
    expect(onclose).toHaveBeenCalledOnce();
    expect(isWorkspaceIdDeleted("ws-1")).toBe(false);
  });

  it("offers an immediate merge override while CI is pending", async () => {
    renderModal({ deferUntilChecksPass: true });

    await fireEvent.click(screen.getByRole("button", { name: "Merge now" }));

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
    const mergeAfterCI = screen.getByRole("button", { name: "Merge after CI" });
    const mergeNow = screen.getByRole("button", { name: "Merge now" });
    expect((mergeAfterCI as HTMLButtonElement).disabled).toBe(true);
    expect((mergeNow as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(mergeAfterCI);
    await fireEvent.click(mergeNow);

    expect(mockMergePull).not.toHaveBeenCalled();
  });

  it("says GitHub auto-merge follows the branch's required checks only on GitHub", () => {
    const { container, unmount } = renderModal({ deferUntilChecksPass: true });
    expect(normalized(container.querySelector(".ci-defer-note")?.textContent ?? null)).toContain(
      "GitHub merges it once the branch's required checks and reviews pass",
    );
    unmount();

    const other = renderModal({ deferUntilChecksPass: true, provider: "gitlab", platformHost: "gitlab.com" });
    expect(normalized(other.container.querySelector(".ci-defer-note")?.textContent ?? null)).toBe(
      "CI is still running. This will merge only if the checks that are pending now pass.",
    );
  });

  it("offers only an immediate merge when a deferred merge is already queued", async () => {
    renderModal({ deferUntilChecksPass: true, alreadyQueued: true });

    expect(screen.queryByRole("button", { name: "Merge after CI" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Merge now" })).toBeNull();
    await confirmMerge();

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  it("offers no immediate merge override when the provider blocks merging now", async () => {
    renderModal({ deferUntilChecksPass: true, immediateMergeBlocked: true });

    expect(screen.queryByRole("button", { name: "Merge now" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI" }));

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

    await fireEvent.click(screen.getByRole("checkbox", { name: "Bypass branch protection and repository rules" }));
    expect(merge.disabled).toBe(false);
    await fireEvent.click(merge);

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  it("sends bypass consent with a merge after CI only when the user chooses to bypass", async () => {
    renderModal({ deferUntilChecksPass: true, immediateMergeBlocked: true, canBypassRules: true });

    await fireEvent.click(screen.getByRole("checkbox", { name: "Bypass branch protection and repository rules" }));
    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI" }));

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);
    expect(mockMergePull.mock.calls[0]?.[2]).toMatchObject({ bypass_rules: true });
  });

  it("describes Forge's own wait once the user chooses to bypass, since GitHub auto-merge cannot", async () => {
    const { container } = renderModal({
      deferUntilChecksPass: true,
      immediateMergeBlocked: true,
      canBypassRules: true,
    });
    const note = () => normalized(container.querySelector(".ci-defer-note")?.textContent ?? null);
    expect(note()).toContain("GitHub merges it once the branch's required checks and reviews pass");

    await fireEvent.click(screen.getByRole("checkbox", { name: "Bypass branch protection and repository rules" }));

    expect(note()).toBe("CI is still running. This will merge only if the checks that are pending now pass.");
  });

  it("queues a merge after CI without bypass consent by default", async () => {
    renderModal({ deferUntilChecksPass: true, immediateMergeBlocked: true, canBypassRules: true });

    await fireEvent.click(screen.getByRole("button", { name: "Merge after CI" }));

    expect(mockMergePull.mock.calls[0]?.[2]).not.toHaveProperty("bypass_rules");
  });

  it("enables the immediate override while CI is pending only after the user chooses to bypass", async () => {
    renderModal({ deferUntilChecksPass: true, immediateMergeBlocked: true, canBypassRules: true });

    const mergeNow = screen.getByRole<HTMLButtonElement>("button", { name: "Merge now" });
    expect(mergeNow.disabled).toBe(true);

    await fireEvent.click(screen.getByRole("checkbox", { name: "Bypass branch protection and repository rules" }));
    await fireEvent.click(mergeNow);

    expect(mockMergePull.mock.calls[0]?.[3]).toBe(false);
  });

  describe("when the base branch uses a merge queue", () => {
    const mergeQueue = true;

    it("hides method choice and commit text because the queue merges with its own method", () => {
      renderModal({ mergeQueue, workspaceId: "ws-1" });

      expect(screen.getByRole("dialog", { name: "Add to merge queue" })).toBeTruthy();
      expect(screen.queryByRole("group", { name: "Merge method" })).toBeNull();
      expect(screen.queryByLabelText("Commit title")).toBeNull();
      expect(screen.queryByLabelText("Commit message")).toBeNull();
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

      expect(screen.queryByRole("button", { name: "Merge now" })).toBeNull();
      await fireEvent.click(screen.getByRole("button", { name: "Add to queue after CI" }));
      expect(mockMergePull.mock.calls[0]?.[3]).toBe(true);

      cleanup();
      renderModal({ mergeQueue, deferUntilChecksPass: true });
      await fireEvent.click(screen.getByRole("button", { name: "Add to queue now" }));
      expect(mockMergePull.mock.calls[1]?.[3]).toBe(false);
    });
  });
});
