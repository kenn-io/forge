import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { getStack, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createRawSnippet } from "svelte";

import Modal from "./Modal.svelte";

const body = createRawSnippet(() => ({
  render: () => `<p>dialog body</p>`,
}));

function renderModal(props: Partial<Record<string, unknown>> = {}) {
  return render(Modal, {
    props: {
      open: true,
      title: "Example dialog",
      onClose: vi.fn(),
      children: body,
      ...props,
    },
  });
}

beforeEach(() => resetModalStack());
afterEach(() => cleanup());

describe("Modal shell", () => {
  // kit Modal's `closable` gates only the header X. Escape and backdrop click
  // must keep dismissing dialogs that omit showClose, otherwise most dialogs
  // in the app would silently become undismissable.
  it("closes on Escape even without a header close button", async () => {
    const onClose = vi.fn();
    renderModal({ onClose });
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();

    await fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes when the press starts on the backdrop", async () => {
    const onClose = vi.fn();
    renderModal({ onClose });

    const overlay = document.querySelector(".kit-modal-overlay");
    expect(overlay).not.toBeNull();
    await fireEvent.pointerDown(overlay!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("shows the header X only with showClose", () => {
    renderModal({ showClose: true });
    expect(screen.getByRole("button", { name: "Close" })).toBeTruthy();
  });

  // Every open dialog must sit on the keyboard modal stack — frameId or not —
  // so global single-key shortcuts stay suppressed while it is open.
  it("registers a modal frame while open and pops it on close", async () => {
    const { rerender } = renderModal();
    await waitFor(() => expect(getStack()).toHaveLength(1));
    expect(getStack()[0]!.frameId).toBe("shared-modal");

    await rerender({ open: false });
    await waitFor(() => expect(getStack()).toHaveLength(0));
  });

  it("uses the provided frame and actions for keyboard dispatch", async () => {
    const handler = vi.fn();
    renderModal({
      frameId: "example-frame",
      actions: [
        {
          id: "example.close",
          label: "Close example",
          binding: { key: "k", ctrlOrMeta: true },
          handler,
        },
      ],
    });
    await waitFor(() => expect(getStack()[0]?.frameId).toBe("example-frame"));
    expect(getStack()[0]?.actions[0]?.handler).toBe(handler);
  });

  it("stacks frames for nested dialogs and unwinds them independently", async () => {
    const outer = renderModal({ frameId: "outer" });
    const inner = renderModal({ frameId: "inner" });
    await waitFor(() => expect(getStack().map((f) => f.frameId)).toEqual(["outer", "inner"]));

    await outer.rerender({ open: false });
    await waitFor(() => expect(getStack().map((f) => f.frameId)).toEqual(["inner"]));

    await inner.rerender({ open: false });
    await waitFor(() => expect(getStack()).toHaveLength(0));
  });
});
