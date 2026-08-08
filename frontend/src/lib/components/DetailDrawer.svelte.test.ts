import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

vi.mock("./detail/PullDetail.svelte", async () => ({
  default: (await import("../views/PRListViewTestPullDetail.svelte")).default,
}));

import DetailDrawer from "./DetailDrawer.svelte";
import MergeModal from "./detail/MergeModal.svelte";
import { getStackDepth, resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";

const drawerProps = {
  itemType: "pr" as const,
  provider: "github",
  platformHost: "github.com",
  owner: "octo",
  name: "repo",
  repoPath: "octo/repo",
  number: 1,
};

const mergeModalProps = {
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
  onmerged: () => {},
};

describe("DetailDrawer Escape layering", () => {
  beforeEach(() => resetModalStack());

  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  it("Escape closes the drawer when no modal is stacked above it", async () => {
    const onClose = vi.fn();
    render(DetailDrawer, { props: { ...drawerProps, onClose } });

    await fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Escape peels the merge modal and leaves the drawer open", async () => {
    const onClose = vi.fn();
    render(DetailDrawer, { props: { ...drawerProps, onClose } });

    // Real MergeModal: it pushes its modal-stack frame on mount and kit
    // Modal registers a window Escape listener after the drawer's, which is
    // exactly the layering the drawer's stack check exists for.
    const onMergeClose = vi.fn();
    const modal = render(MergeModal, {
      props: { ...mergeModalProps, onclose: onMergeClose },
    });
    expect(getStackDepth()).toBe(1);

    await fireEvent.keyDown(window, { key: "Escape" });
    expect(onMergeClose).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();

    // Once the modal is gone the drawer owns Escape again.
    modal.unmount();
    expect(getStackDepth()).toBe(0);
    await fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
