import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import ApproveButton from "./ApproveButton.svelte";
import { API_CLIENT_KEY, STORES_KEY } from "../../context.js";

describe("ApproveButton head conflicts", () => {
  afterEach(() => {
    cleanup();
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
    const dialog = screen.getByRole("dialog", { name: "Approve pull request" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(onheadconflict).toHaveBeenCalledWith("stale_state", undefined));
    expect(screen.queryByRole("dialog", { name: "Approve pull request" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(screen.getByRole("dialog", { name: "Approve pull request" })).toBeTruthy();
    expect(screen.queryByText("target changed since it was reviewed; refresh and retry")).toBeNull();
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
    const dialog = screen.getByRole("dialog", { name: "Approve pull request" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [, init] = post.mock.calls[0] as [string, { body: { expected_head_sha?: string } }];
    expect(init.body.expected_head_sha).toBe("platform-head-sha");
  });
});
