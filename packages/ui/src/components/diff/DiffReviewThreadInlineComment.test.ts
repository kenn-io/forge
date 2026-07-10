import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import DiffReviewThreadInlineComment from "./DiffReviewThreadInlineComment.svelte";
import type { ReviewThread } from "./review-thread-context.js";

type GlobalWithResizeObserver = { ResizeObserver?: unknown };

const originalResizeObserver = (globalThis as GlobalWithResizeObserver).ResizeObserver;

class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

function makeReviewThread(overrides: Partial<ReviewThread> = {}): ReviewThread {
  return {
    id: "thread-1",
    provider_comment_id: "comment-1",
    path: "README.md",
    side: "right",
    line: 2,
    new_line: 2,
    line_type: "add",
    body: "Published review note",
    author_login: "reviewer",
    resolved: false,
    can_resolve: false,
    created_at: "2026-03-30T14:01:00Z",
    updated_at: "2026-03-30T14:01:00Z",
    ...overrides,
  };
}

describe("DiffReviewThreadInlineComment", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    if (originalResizeObserver === undefined) {
      delete (globalThis as GlobalWithResizeObserver).ResizeObserver;
    } else {
      (globalThis as GlobalWithResizeObserver).ResizeObserver = originalResizeObserver;
    }
  });

  it("sizes rich preview cards to the preview column instead of the whole file content", async () => {
    (globalThis as GlobalWithResizeObserver).ResizeObserver = ResizeObserverStub;
    document.body.innerHTML = `
      <div class="file-content">
        <div class="diff-rich-preview markdown-rich-diff--unified"></div>
      </div>
    `;
    const preview = document.querySelector<HTMLElement>(".diff-rich-preview");
    expect(preview).toBeTruthy();

    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function getBoundingClientRect() {
      if (this.classList.contains("file-content")) {
        return { left: 0, right: 1000, width: 1000 } as DOMRect;
      }
      if (this.classList.contains("diff-rich-preview")) {
        return { left: 100, right: 500, width: 400 } as DOMRect;
      }
      if (this.classList.contains("inline-review-thread")) {
        return { left: 112, right: 500, width: 388 } as DOMRect;
      }
      return { left: 0, right: 0, width: 0 } as DOMRect;
    });

    render(DiffReviewThreadInlineComment, {
      target: preview!,
      props: {
        thread: makeReviewThread(),
      },
    });

    const card = document.querySelector<HTMLElement>(".inline-review-thread");
    await waitFor(() => {
      expect(card?.style.getPropertyValue("--inline-review-thread-width")).toBe("376px");
    });
  });

  it("keeps a failed reply open without adding a duplicate inline action error", async () => {
    const onreply = vi.fn().mockResolvedValue(false);
    render(DiffReviewThreadInlineComment, {
      props: {
        thread: makeReviewThread(),
        canReply: true,
        onreply,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Reply" }));
    await fireEvent.input(screen.getByPlaceholderText("Reply to thread"), {
      target: { value: "Please take another look" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Reply" }));

    await waitFor(() => {
      expect(onreply).toHaveBeenCalledWith(expect.objectContaining({ id: "thread-1" }), "Please take another look");
    });
    expect(screen.getByDisplayValue("Please take another look")).toBeTruthy();
    expect(screen.queryByText("Could not reply to thread")).toBeNull();
  });
});
