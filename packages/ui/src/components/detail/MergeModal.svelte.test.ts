import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Mock } from "vite-plus/test";

import MergeModal from "./MergeModal.svelte";
import { API_CLIENT_KEY } from "../../context.js";
import { getStackDepth, getTopFrame, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

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

describe("MergeModal head pinning", () => {
  beforeEach(() => {
    resetModalStack();
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  function clientWith(post: Mock) {
    return {
      POST: post,
      GET: vi.fn(),
      PUT: vi.fn(),
      PATCH: vi.fn(),
      DELETE: vi.fn(),
      OPTIONS: vi.fn(),
      HEAD: vi.fn(),
      TRACE: vi.fn(),
    };
  }

  function renderModal(post: Mock, props: Partial<Record<string, unknown>> = {}) {
    return render(MergeModal, {
      props: { ...baseProps, ...props },
      context: new Map<symbol, unknown>([[API_CLIENT_KEY, clientWith(post)]]),
    });
  }

  function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((res) => {
      resolve = res;
    });
    return { promise, resolve };
  }

  async function confirmMerge(): Promise<void> {
    await fireEvent.click(screen.getByText("Squash and merge", { selector: ".kit-modal-footer button" }));
  }

  it("echoes the reviewed head as expected_head_sha in the merge body", async () => {
    const post = vi.fn().mockResolvedValue({ data: {}, error: undefined, response: new Response("{}") });
    renderModal(post, { expectedHeadSha: "abc123" });

    await confirmMerge();

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [, init] = post.mock.calls[0];
    expect(init.body.expected_head_sha).toBe("abc123");
  });

  it("omits expected_head_sha when the rendered head is unknown", async () => {
    const post = vi.fn().mockResolvedValue({ data: {}, error: undefined, response: new Response("{}") });
    renderModal(post);

    await confirmMerge();

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [, init] = post.mock.calls[0];
    expect(init.body).not.toHaveProperty("expected_head_sha");
  });

  it("closes and reports head-pinning conflicts instead of showing an inline error", async () => {
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
    const onclose = vi.fn();
    const onheadconflict = vi.fn();
    const onmerged = vi.fn();
    renderModal(post, { expectedHeadSha: "abc123", onclose, onheadconflict, onmerged });

    await confirmMerge();

    await waitFor(() => expect(onheadconflict).toHaveBeenCalledWith("stale_state", undefined));
    expect(onclose).toHaveBeenCalledTimes(1);
    expect(onmerged).not.toHaveBeenCalled();
    expect(screen.queryByText("target changed since it was reviewed; refresh and retry")).toBeNull();
  });

  it("shows the provider message inline for generic merge conflicts", async () => {
    const post = vi.fn().mockResolvedValue({
      data: undefined,
      error: {
        type: "about:blank",
        title: "Conflict",
        status: 409,
        detail: "merge blocked by provider",
        code: "conflict",
        details: { reason: "conflict" },
      },
      response: new Response("{}", { status: 409 }),
    });
    const onclose = vi.fn();
    const onheadconflict = vi.fn();
    renderModal(post, { expectedHeadSha: "abc123", onclose, onheadconflict });

    await confirmMerge();

    await waitFor(() => expect(screen.getByText("merge blocked by provider")).toBeTruthy());
    expect(onheadconflict).not.toHaveBeenCalled();
    expect(onclose).not.toHaveBeenCalled();
  });

  it("enqueues a deferred merge and reports it as queued when CI is still pending", async () => {
    const post = vi.fn().mockResolvedValue({ data: {}, error: undefined, response: new Response("{}") });
    const onclose = vi.fn();
    const onqueued = vi.fn();
    renderModal(post, {
      deferUntilChecksPass: true,
      onclose,
      onqueued,
    });

    await fireEvent.click(screen.getByText("Merge after CI is complete", { selector: ".kit-modal-footer button" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, init] = post.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/merge/deferred");
    expect(init.body.method).toBe("squash");
    expect(onqueued).toHaveBeenCalledTimes(1);
    expect(onclose).not.toHaveBeenCalled();
  });

  it("offers an immediate merge override while CI is still pending", async () => {
    const post = vi.fn().mockResolvedValue({ data: {}, error: undefined, response: new Response("{}") });
    const onmerged = vi.fn();
    renderModal(post, {
      deferUntilChecksPass: true,
      onmerged,
    });

    expect(screen.getByRole("button", { name: "Merge after CI is complete" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Merge Anyway" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, init] = post.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/merge");
    expect(init.body.method).toBe("squash");
    expect(onmerged).toHaveBeenCalledTimes(1);
  });

  it("disables the deferred merge action while scheduling the merge", async () => {
    const scheduled = deferred<{ data: Record<string, never>; error: undefined; response: Response }>();
    const post = vi.fn().mockReturnValue(scheduled.promise);
    renderModal(post, {
      deferUntilChecksPass: true,
    });

    await fireEvent.click(screen.getByText("Merge after CI is complete", { selector: ".kit-modal-footer button" }));

    const pendingButton = screen.getByRole<HTMLButtonElement>("button", { name: "Merge scheduled..." });
    expect(pendingButton.disabled).toBe(true);
    expect(post).toHaveBeenCalledTimes(1);

    scheduled.resolve({ data: {}, error: undefined, response: new Response("{}") });
  });

  it("offers only an immediate merge when a deferred merge is already queued", async () => {
    const post = vi.fn().mockResolvedValue({ data: {}, error: undefined, response: new Response("{}") });
    const onmerged = vi.fn();
    renderModal(post, {
      deferUntilChecksPass: true,
      alreadyQueued: true,
      onmerged,
    });

    // A second deferred queue would 409, so neither deferred action is offered.
    expect(screen.queryByRole("button", { name: "Merge after CI is complete" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Merge Anyway" })).toBeNull();
    expect(screen.getByText(/A merge is already queued/)).toBeTruthy();

    await confirmMerge();

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path] = post.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/merge");
    expect(onmerged).toHaveBeenCalledTimes(1);
  });

  it("enqueues a deferred merge when requested without granular pending checks", async () => {
    const post = vi.fn().mockResolvedValue({ data: {}, error: undefined, response: new Response("{}") });
    const onqueued = vi.fn();
    renderModal(post, {
      deferUntilChecksPass: true,
      onqueued,
    });

    await fireEvent.click(screen.getByText("Merge after CI is complete", { selector: ".kit-modal-footer button" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path] = post.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/merge/deferred");
    expect(onqueued).toHaveBeenCalledTimes(1);
  });
});
