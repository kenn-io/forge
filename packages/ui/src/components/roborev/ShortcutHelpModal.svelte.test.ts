import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import ShortcutHelpModal from "./ShortcutHelpModal.svelte";
import { getStackDepth, getTopFrame, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

describe("ShortcutHelpModal modal frame integration", () => {
  beforeEach(() => {
    resetModalStack();
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  it("pushes a frame on mount and pops on unmount", () => {
    expect(getStackDepth()).toBe(0);
    const { unmount } = render(ShortcutHelpModal, {
      props: { open: true, onclose: vi.fn() },
    });
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("roborev-shortcut-help");
    unmount();
    expect(getStackDepth()).toBe(0);
  });
});
