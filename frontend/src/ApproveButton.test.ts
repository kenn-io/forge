import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockApprovePull = vi.hoisted(() => vi.fn());
const mockRequestPullChanges = vi.hoisted(() => vi.fn());
const mockShowFlash = vi.hoisted(() => vi.fn());

vi.mock("./lib/context.js", () => ({
  getStores: () => ({
    detail: {
      approvePull: mockApprovePull,
      requestPullChanges: mockRequestPullChanges,
    },
  }),
}));

vi.mock("./lib/stores/flash.svelte.js", () => ({
  showFlash: mockShowFlash,
}));

import ApproveButton from "./lib/components/detail/ApproveButton.svelte";

const defaultProps = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  number: 1,
  supportedReviewActions: [] as string[],
};

function renderApproveButton(overrides: Partial<typeof defaultProps> = {}) {
  return render(ApproveButton, {
    props: { ...defaultProps, ...overrides },
  });
}

describe("ApproveButton tooltips", () => {
  beforeEach(() => {
    const settleSuccessfully = (...args: unknown[]) => {
      const callbacks = args.at(-1) as { onSuccess?: () => void; onSettled?: () => void };
      callbacks.onSuccess?.();
      callbacks.onSettled?.();
    };
    mockApprovePull.mockImplementation(settleSuccessfully);
    mockRequestPullChanges.mockImplementation(settleSuccessfully);
    mockShowFlash.mockReset();
  });

  afterEach(() => {
    cleanup();
    mockApprovePull.mockReset();
    mockRequestPullChanges.mockReset();
  });

  it("collapsed button title describes opening the form, not submitting", () => {
    renderApproveButton();

    const trigger = screen.getByRole("button", { name: /approve/i });
    expect(trigger.getAttribute("title")).toBe("Open the approval form to submit a code review on this pull request");
  });

  it("expanded submit button carries the actual submit-review tooltip", async () => {
    renderApproveButton();

    await fireEvent.click(screen.getByRole("button", { name: /approve/i }));

    const submit = screen.getByTitle("Submit an approving code review on this pull request");
    expect(submit.getAttribute("title")).toBe("Submit an approving code review on this pull request");
  });

  it("keeps the approval trigger stable while opening the approval popover", async () => {
    renderApproveButton();

    const trigger = screen.getByRole("button", { name: /^approve$/i });
    await fireEvent.click(trigger);

    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("dialog", { name: "Submit pull request review" })).toBeTruthy();

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole("textbox"));
    });
  });

  it("renders the optional comment placeholder as display text", async () => {
    renderApproveButton();

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));

    expect(screen.getByPlaceholderText("Leave an optional comment…")).toBeTruthy();
    expect(screen.queryByPlaceholderText(/\\u2026/)).toBeNull();
  });

  it("submits a change request with the typed review comment", async () => {
    renderApproveButton({ supportedReviewActions: ["approve", "request_changes"] });

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    const requestChanges = screen.getByRole("button", { name: "Request changes" });
    expect(requestChanges.hasAttribute("disabled")).toBe(true);

    await fireEvent.input(screen.getByRole("textbox"), {
      target: { value: "Please cover the empty state." },
    });
    await fireEvent.click(requestChanges);

    await waitFor(() => {
      expect(mockRequestPullChanges).toHaveBeenCalledTimes(1);
    });
    expect(mockRequestPullChanges.mock.calls[0]?.[2]).toEqual({
      body: "Please cover the empty state.",
    });
    expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
  });

  it("closes a successful change request and launches both refreshes", async () => {
    renderApproveButton({ supportedReviewActions: ["approve", "request_changes"] });

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.input(screen.getByRole("textbox"), {
      target: { value: "Please cover the empty state." },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Request changes" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
    });
    expect(mockRequestPullChanges).toHaveBeenCalledTimes(1);
  });

  it("collapses the approval popover from cancel without removing the trigger", async () => {
    renderApproveButton();

    const trigger = screen.getByRole("button", { name: /^approve$/i });
    await fireEvent.click(trigger);
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("dialog", { name: "Approve pull request" })).toBeNull();
  });

  it("keeps the approval popover open and trigger disabled while submitting", async () => {
    let settleApproval = () => {};
    mockApprovePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onSuccess?: () => void; onSettled?: () => void };
      settleApproval = () => {
        callbacks.onSuccess?.();
        callbacks.onSettled?.();
      };
    });
    renderApproveButton();

    const trigger = screen.getByRole("button", { name: /^approve$/i });
    await fireEvent.click(trigger);
    await fireEvent.input(screen.getByRole("textbox"), {
      target: { value: "lgtm" },
    });
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(trigger.hasAttribute("disabled")).toBe(true);
    });
    expect(screen.getByRole("dialog", { name: "Submit pull request review" })).toBeTruthy();

    await fireEvent.click(trigger);
    expect(screen.getByRole("dialog", { name: "Submit pull request review" })).toBeTruthy();

    settleApproval();

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
    });
  });

  it("drops the captured head pin when only the provider identity changes", async () => {
    const { rerender } = render(ApproveButton, {
      props: { ...defaultProps, expectedHeadSha: "github-pin" },
    });

    const trigger = screen.getByRole("button", { name: /^approve$/i });
    await fireEvent.click(trigger);
    expect(screen.getByRole("dialog", { name: "Submit pull request review" })).toBeTruthy();

    // Same owner/name/number on a different provider+host+repoPath:
    // the open form and its captured pin must not survive the route.
    await rerender({
      ...defaultProps,
      provider: "gitea",
      platformHost: "gitea.example.com",
      repoPath: "acme/widget",
      expectedHeadSha: "gitea-pin",
    });

    expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(mockApprovePull).toHaveBeenCalledTimes(1);
    });
    expect(mockApprovePull.mock.calls[0]?.[2]).toMatchObject({ expected_head_sha: "gitea-pin" });
  });

  it("collapses the approval popover after a head conflict", async () => {
    const onHeadConflict = vi.fn();
    mockApprovePull.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as {
        onProblem?: (problem: unknown) => void;
        onFailure?: (message: string) => void;
        onSettled?: () => void;
      };
      const problem = {
        code: "conflict",
        detail: "target changed since it was reviewed",
        details: { reason: "stale_state" },
      };
      callbacks.onProblem?.(problem);
      callbacks.onFailure?.(problem.detail);
      callbacks.onSettled?.();
    });
    const { rerender } = render(ApproveButton, {
      props: {
        ...defaultProps,
        expectedHeadSha: "stale-pin",
        routeGeneration: 12,
        onheadconflict: onHeadConflict,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(onHeadConflict).toHaveBeenCalledWith(
        "stale_state",
        undefined,
        "stale-pin",
        {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "widget",
          repoPath: "acme/widget",
        },
        1,
        12,
      );
    });
    expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();

    await rerender({
      ...defaultProps,
      expectedHeadSha: "fresh-pin",
      onheadconflict: onHeadConflict,
    });
    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(mockApprovePull).toHaveBeenCalledTimes(2);
    });
    expect(mockApprovePull.mock.calls[1]?.[2]).toMatchObject({ expected_head_sha: "fresh-pin" });
  });

  it("collapses and clears the draft when the PR identity changes", async () => {
    const { rerender } = renderApproveButton();

    await fireEvent.click(screen.getByRole("button", { name: /approve/i }));
    const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
    await fireEvent.input(textarea, { target: { value: "lgtm A" } });
    expect(textarea.value).toBe("lgtm A");

    await rerender({ owner: "acme", name: "widget", number: 2 });

    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByRole("button", { name: /approve/i }).getAttribute("title")).toBe(
      "Open the approval form to submit a code review on this pull request",
    );
  });
});
