import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockPost = vi.hoisted(() => vi.fn());
const mockLoadDetail = vi.hoisted(() => vi.fn());
const mockLoadPulls = vi.hoisted(() => vi.fn());

vi.mock("../../packages/ui/src/context.js", () => ({
  getClient: () => ({ POST: mockPost }),
  getStores: () => ({
    detail: { loadDetail: mockLoadDetail },
    pulls: { loadPulls: mockLoadPulls },
  }),
}));

import ApproveButton from "../../packages/ui/src/components/detail/ApproveButton.svelte";

const defaultProps = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  number: 1,
};

function renderApproveButton(overrides: Partial<typeof defaultProps> = {}) {
  return render(ApproveButton, {
    props: { ...defaultProps, ...overrides },
  });
}

describe("ApproveButton tooltips", () => {
  beforeEach(() => {
    mockPost.mockResolvedValue({ data: { status: "approved" } });
    mockLoadDetail.mockResolvedValue(undefined);
    mockLoadPulls.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
    mockPost.mockReset();
    mockLoadDetail.mockReset();
    mockLoadPulls.mockReset();
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
    expect(screen.getByRole("dialog", { name: "Approve pull request" })).toBeTruthy();

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

  it("collapses the approval popover from cancel without removing the trigger", async () => {
    renderApproveButton();

    const trigger = screen.getByRole("button", { name: /^approve$/i });
    await fireEvent.click(trigger);
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("dialog", { name: "Approve pull request" })).toBeNull();
  });

  it("keeps the approval popover open and trigger disabled while submitting", async () => {
    let resolvePost: (value: { data: { status: string } }) => void = () => {};
    mockPost.mockReturnValue(
      new Promise((resolve) => {
        resolvePost = resolve;
      }),
    );
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
    expect(screen.getByRole("dialog", { name: "Approve pull request" })).toBeTruthy();

    await fireEvent.click(trigger);
    expect(screen.getByRole("dialog", { name: "Approve pull request" })).toBeTruthy();

    resolvePost({ data: { status: "approved" } });

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Approve pull request" })).toBeNull();
    });
  });

  it("drops the captured head pin when only the provider identity changes", async () => {
    const { rerender } = render(ApproveButton, {
      props: { ...defaultProps, expectedHeadSha: "github-pin" },
    });

    const trigger = screen.getByRole("button", { name: /^approve$/i });
    await fireEvent.click(trigger);
    expect(screen.getByRole("dialog", { name: "Approve pull request" })).toBeTruthy();

    // Same owner/name/number on a different provider+host+repoPath:
    // the open form and its captured pin must not survive the route.
    await rerender({
      ...defaultProps,
      provider: "gitea",
      platformHost: "gitea.example.com",
      repoPath: "acme/widget",
      expectedHeadSha: "gitea-pin",
    });

    expect(screen.queryByRole("dialog", { name: "Approve pull request" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(mockPost).toHaveBeenCalledTimes(1);
    });
    const [, init] = mockPost.mock.calls[0] as [string, { body: { expected_head_sha?: string } }];
    expect(init.body.expected_head_sha).toBe("gitea-pin");
  });

  it("collapses the approval popover after a head conflict", async () => {
    const onHeadConflict = vi.fn();
    mockPost.mockResolvedValue({
      error: {
        code: "conflict",
        detail: "target changed since it was reviewed",
        details: { reason: "stale_state" },
      },
    });
    const { rerender } = render(ApproveButton, {
      props: {
        ...defaultProps,
        expectedHeadSha: "stale-pin",
        onheadconflict: onHeadConflict,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(onHeadConflict).toHaveBeenCalledWith("stale_state", undefined);
    });
    expect(screen.queryByRole("dialog", { name: "Approve pull request" })).toBeNull();

    await rerender({
      ...defaultProps,
      expectedHeadSha: "fresh-pin",
      onheadconflict: onHeadConflict,
    });
    await fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    await fireEvent.click(screen.getByTitle("Submit an approving code review on this pull request"));

    await waitFor(() => {
      expect(mockPost).toHaveBeenCalledTimes(2);
    });
    const [, retryInit] = mockPost.mock.calls[1] as [string, { body: { expected_head_sha?: string } }];
    expect(retryInit.body.expected_head_sha).toBe("fresh-pin");
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
