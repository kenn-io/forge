// Guards the roborev comment composer layout: the Comment button is inset
// inside the textarea (bottom-right), matching the pull request and issue
// comment boxes, rather than sitting beside the field as a separate column.
//
// The button is absolutely positioned and the textarea reserves its footprint
// as bottom padding. Both halves of that pairing are invisible to jsdom, and
// dropping either one either moves the button back outside the field or lets
// typed text run underneath it. Needs real layout, so it runs in the browser
// lane.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import { STORES_KEY } from "../../packages/ui/src/context.js";
import ResponseList from "../../packages/ui/src/components/roborev/ResponseList.svelte";

function mountComposer(): { wrapper: HTMLElement; unmount: () => void } {
  const wrapper = document.createElement("div");
  wrapper.style.width = "720px";
  document.body.appendChild(wrapper);

  const { unmount } = render(ResponseList, {
    target: wrapper,
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          roborevReview: {
            getSelectedJobId: () => 42,
            getResponses: () => [],
            isClosed: () => false,
            addComment: vi.fn(),
          },
        },
      ],
    ]),
  });

  return {
    wrapper,
    unmount: () => {
      unmount();
      wrapper.remove();
    },
  };
}

describe("roborev comment composer", () => {
  let mounted: { wrapper: HTMLElement; unmount: () => void } | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
    cleanup();
  });

  it("insets the Comment button inside the textarea and keeps typed text clear of it", async () => {
    mounted = mountComposer();

    const textarea = document.querySelector<HTMLTextAreaElement>(".comment-textarea")!;
    const submit = document.querySelector<HTMLButtonElement>(".submit-btn")!;

    const field = textarea.getBoundingClientRect();
    const button = submit.getBoundingClientRect();

    expect(button.width).toBeGreaterThan(0);
    expect(button.left).toBeGreaterThanOrEqual(field.left);
    expect(button.right).toBeLessThanOrEqual(field.right + 0.5);
    expect(button.top).toBeGreaterThanOrEqual(field.top);
    expect(button.bottom).toBeLessThanOrEqual(field.bottom + 0.5);

    // The reserved bottom padding must clear the button, otherwise text
    // scrolls underneath it.
    const paddingBottom = Number.parseFloat(getComputedStyle(textarea).paddingBottom);
    expect(paddingBottom).toBeGreaterThanOrEqual(button.height);
  });
});
