import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import RepoImportModalRuntimeHarness from "./RepoImportModalRuntimeHarness.svelte";
import { getStackDepth, getTopFrame, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

describe("RepoImportModal modal frame integration", () => {
  beforeEach(() => {
    resetModalStack();
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  it("pushes a frame on mount and pops on unmount", () => {
    expect(getStackDepth()).toBe(0);
    const { unmount } = render(RepoImportModalRuntimeHarness, {
      props: { open: true, onClose: vi.fn(), onImported: vi.fn() },
    });
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("repo-import-modal");
    unmount();
    expect(getStackDepth()).toBe(0);
  });
});
