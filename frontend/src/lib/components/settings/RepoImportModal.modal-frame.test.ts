import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import RepoImportModal from "./RepoImportModal.svelte";
import { getStackDepth, getTopFrame, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";

vi.mock("../../api/settings.js", () => ({
  previewRepos: vi.fn(),
  bulkAddRepos: vi.fn(),
}));

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
    const { unmount } = render(RepoImportModal, {
      props: { open: true, onClose: vi.fn(), onImported: vi.fn() },
    });
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("repo-import-modal");
    unmount();
    expect(getStackDepth()).toBe(0);
  });
});
