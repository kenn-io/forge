import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import ApproveButton from "./ApproveButtonRuntimeHarness.svelte";
import { STORES_KEY } from "../../context.js";
import type { ProblemBody } from "../../api/problems.js";
import type { ProviderRouteRef } from "../../api/provider-routes.js";
import type { ProviderActionCallbacks } from "../../stores/detail.svelte.js";

const { showFlash } = vi.hoisted(() => ({ showFlash: vi.fn() }));

vi.mock("../../stores/flash.svelte.js", () => ({ showFlash }));

function detailActions(post: ReturnType<typeof vi.fn>) {
  const run = (path: string, body: unknown, callbacks: ProviderActionCallbacks) => {
    post(path, { body }).then((result: { error?: ProblemBody }) => {
      if (result.error !== undefined) {
        callbacks.onProblem?.(result.error);
        callbacks.onFailure?.(result.error.detail ?? result.error.title ?? "provider action failed");
      } else {
        callbacks.onSuccess?.();
      }
      callbacks.onSettled?.();
    });
  };
  return {
    approvePull: vi.fn((_ref: ProviderRouteRef, _number: number, body: unknown, callbacks: ProviderActionCallbacks) =>
      run("/approve", body, callbacks),
    ),
    requestPullChanges: vi.fn(
      (_ref: ProviderRouteRef, _number: number, body: unknown, callbacks: ProviderActionCallbacks) =>
        run("/request-changes", body, callbacks),
    ),
  };
}

describe("ApproveButton", () => {
  afterEach(() => {
    cleanup();
    showFlash.mockReset();
  });
  for (const action of [
    { label: "Approve", supportedReviewActions: [] as string[], error: "approval rejected" },
    { label: "Request changes", supportedReviewActions: ["request_changes"], error: "change request rejected" },
  ]) {
    it(`flashes a delayed ${action.label.toLowerCase()} failure after route navigation`, async () => {
      let resolvePost!: (value: { error: { detail: string } }) => void;
      const post = vi.fn().mockReturnValue(
        new Promise<{ error: { detail: string } }>((resolve) => {
          resolvePost = resolve;
        }),
      );
      const props = {
        owner: "acme",
        name: "widget",
        number: 7,
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        supportedReviewActions: action.supportedReviewActions,
      };
      const { rerender } = render(ApproveButton, {
        props,
        context: new Map<symbol, unknown>([
          [STORES_KEY, { detail: detailActions(post), pulls: { loadPulls: vi.fn() } }],
        ]),
      });

      await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
      if (action.label === "Request changes") {
        await fireEvent.input(screen.getByRole("textbox"), { target: { value: "Please revise this." } });
      }
      await fireEvent.click(
        action.label === "Approve"
          ? screen.getByTitle("Submit an approving code review on this pull request")
          : screen.getByRole("button", { name: action.label }),
      );
      await waitFor(() => expect(post).toHaveBeenCalledTimes(1));

      await rerender({ ...props, name: "other-widget", number: 8, repoPath: "acme/other-widget" });
      expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
      resolvePost({ error: { detail: action.error } });

      await waitFor(() => expect(showFlash).toHaveBeenCalledWith(action.error, { tone: "danger" }));
      expect(showFlash).toHaveBeenCalledTimes(1);
    });
  }

  it("opens request changes from the review menu and submits a comment with the captured head", async () => {
    const post = vi.fn().mockResolvedValue({});
    const props = {
      owner: "acme",
      name: "widget",
      number: 7,
      provider: "github",
      repoPath: "acme/widget",
      platformHeadSha: "original-head",
      supportedReviewActions: ["request_changes"],
    };
    render(ApproveButton, {
      props,
      context: new Map<symbol, unknown>([[STORES_KEY, { detail: detailActions(post) }]]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Review options" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Request changes" }));
    const dialog = screen.getByRole("dialog", { name: "Submit pull request review" });
    const submit = within(dialog).getByRole("button", { name: "Request changes" });
    expect(submit.hasAttribute("disabled")).toBe(true);
    expect(within(dialog).queryByRole("button", { name: "Approve" })).toBeNull();
    expect(post).not.toHaveBeenCalled();
    await fireEvent.input(within(dialog).getByRole("textbox", { name: "Requested changes" }), {
      target: { value: "Please revise this." },
    });
    await fireEvent.click(submit);
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("/request-changes", {
        body: { body: "Please revise this.", expected_head_sha: "original-head" },
      }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("only offers the review menu when request changes is supported and disables it with approval", async () => {
    const props = {
      owner: "acme",
      name: "widget",
      number: 7,
      provider: "github",
      repoPath: "acme/widget",
      supportedReviewActions: [] as string[],
    };
    const { rerender } = render(ApproveButton, {
      props,
      context: new Map<symbol, unknown>([[STORES_KEY, { detail: detailActions(vi.fn()) }]]),
    });
    expect(screen.queryByRole("button", { name: "Review options" })).toBeNull();
    await rerender({ ...props, supportedReviewActions: ["request_changes"], disabled: true });
    expect(screen.getByRole("button", { name: "Review options" }).hasAttribute("disabled")).toBe(true);
  });

  it("keeps a request-changes head conflict parent-handled without a danger flash", async () => {
    const post = vi.fn().mockResolvedValue({
      error: {
        code: "conflict",
        detail: "target changed since it was reviewed",
        details: { reason: "head_unknown" },
      },
    });
    const onheadconflict = vi.fn();
    render(ApproveButton, {
      props: {
        owner: "acme",
        name: "widget",
        number: 7,
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        expectedHeadSha: "reviewed-sha",
        supportedReviewActions: ["request_changes"],
        onheadconflict,
      },
      context: new Map<symbol, unknown>([[STORES_KEY, { detail: detailActions(post), pulls: { loadPulls: vi.fn() } }]]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await fireEvent.input(screen.getByRole("textbox"), { target: { value: "Please revise this." } });
    await fireEvent.click(screen.getByRole("button", { name: "Request changes" }));

    await waitFor(() => expect(onheadconflict).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
    expect(showFlash).not.toHaveBeenCalled();
  });

  it("submits the latest synced platform head when it differs from reviewed head", async () => {
    const post = vi.fn().mockResolvedValue({
      data: { status: "approved" },
      error: undefined,
      response: new Response("{}"),
    });
    render(ApproveButton, {
      props: {
        owner: "acme",
        name: "widget",
        number: 7,
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        expectedHeadSha: "reviewed-sha",
        platformHeadSha: "platform-head-sha",
      },
      context: new Map<symbol, unknown>([
        [
          STORES_KEY,
          {
            detail: detailActions(post),
            pulls: { loadPulls: vi.fn().mockResolvedValue(undefined) },
          },
        ],
      ]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    const dialog = screen.getByRole("dialog", { name: "Submit pull request review" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [, init] = post.mock.calls[0] as [string, { body: { expected_head_sha?: string } }];
    expect(init.body.expected_head_sha).toBe("platform-head-sha");
  });
});
