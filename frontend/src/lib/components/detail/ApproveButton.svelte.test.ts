import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import ApproveButton from "./ApproveButton.svelte";
import { API_CLIENT_KEY, STORES_KEY } from "../../context.js";

const { showFlash } = vi.hoisted(() => ({ showFlash: vi.fn() }));

vi.mock("../../stores/flash.svelte.js", () => ({ showFlash }));

describe("ApproveButton", () => {
  afterEach(() => {
    cleanup();
    showFlash.mockReset();
  });

  it("closes the form without keeping the stale conflict as inline error", async () => {
    const post = vi.fn().mockResolvedValue({
      data: undefined,
      error: {
        type: "about:blank",
        title: "Conflict",
        status: 409,
        detail: "target changed since it was reviewed; refresh and retry",
        code: "conflict",
        details: { reason: "stale_state" },
      },
      response: new Response("{}", { status: 409 }),
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
        requireHeadPin: true,
        routeGeneration: 12,
        onheadconflict,
      },
      context: new Map<symbol, unknown>([
        [
          API_CLIENT_KEY,
          {
            POST: post,
          },
        ],
        [
          STORES_KEY,
          {
            detail: { loadDetail: vi.fn() },
            pulls: { loadPulls: vi.fn() },
          },
        ],
      ]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    const dialog = screen.getByRole("dialog", { name: "Submit pull request review" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));

    await waitFor(() =>
      expect(onheadconflict).toHaveBeenCalledWith(
        "stale_state",
        undefined,
        "reviewed-sha",
        {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "widget",
          repoPath: "acme/widget",
        },
        7,
        12,
      ),
    );
    expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
    expect(showFlash).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(screen.getByRole("dialog", { name: "Submit pull request review" })).toBeTruthy();
    expect(screen.queryByText("target changed since it was reviewed; refresh and retry")).toBeNull();
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
          [API_CLIENT_KEY, { POST: post }],
          [STORES_KEY, { detail: { loadDetail: vi.fn() }, pulls: { loadPulls: vi.fn() } }],
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
      context: new Map<symbol, unknown>([
        [API_CLIENT_KEY, { POST: post }],
        [STORES_KEY, { detail: { loadDetail: vi.fn() }, pulls: { loadPulls: vi.fn() } }],
      ]),
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
          API_CLIENT_KEY,
          {
            POST: post,
          },
        ],
        [
          STORES_KEY,
          {
            detail: { loadDetail: vi.fn().mockResolvedValue(undefined) },
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

  it("closes a successful approval before reporting a refresh failure", async () => {
    const post = vi.fn().mockResolvedValue({
      data: { status: "approved" },
      error: undefined,
      response: new Response("{}"),
    });
    const oncompleted = vi.fn();
    render(ApproveButton, {
      props: {
        owner: "acme",
        name: "widget",
        number: 7,
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        oncompleted,
      },
      context: new Map<symbol, unknown>([
        [API_CLIENT_KEY, { POST: post }],
        [
          STORES_KEY,
          {
            detail: { loadDetail: vi.fn().mockRejectedValue(new Error("refresh failed")) },
            pulls: { loadPulls: vi.fn().mockResolvedValue(undefined) },
          },
        ],
      ]),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    const dialog = screen.getByRole("dialog", { name: "Submit pull request review" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(oncompleted).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("dialog", { name: "Submit pull request review" })).toBeNull();
    await waitFor(() => {
      expect(showFlash).toHaveBeenCalledWith("Pull request approved, but it could not be refreshed.");
    });
  });
});
