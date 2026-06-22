import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import ConfirmDialog from "./ConfirmDialog.svelte";

afterEach(() => cleanup());

function renderDialog() {
  render(ConfirmDialog, {
    props: {
      open: true,
      title: "Delete workspace?",
      message: 'Delete workspace "Demo"?',
      confirmLabel: "Delete workspace",
      tone: "danger",
      onCancel: vi.fn(),
      onConfirm: vi.fn(),
    },
  });
}

describe("ConfirmDialog", () => {
  // A confirmation dialog whose footer already offers Cancel does not also need
  // a header close button: it duplicates Cancel and adds a third stop to the
  // keyboard loop.
  it("omits a redundant header close button", () => {
    renderDialog();
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
  });

  // Destructive confirm dialogs previously focused Cancel on open. After moving
  // to the shared Modal shell the generic fallback focused the header close (X)
  // button instead; with the X gone Cancel is the safe default again.
  it("focuses Cancel rather than the destructive action on open", async () => {
    renderDialog();
    const cancel = screen.getByRole("button", { name: "Cancel" });
    await waitFor(() => expect(cancel).toBe(document.activeElement));
    expect(screen.getByRole("button", { name: "Delete workspace" })).not.toBe(document.activeElement);
  });

  // The keyboard loop must stay Cancel <-> destructive with no third stop, so
  // tabbing off the destructive action returns to Cancel rather than wrapping
  // to a header close button.
  it("traps Tab in a Cancel and destructive-action loop", async () => {
    renderDialog();
    const cancel = screen.getByRole("button", { name: "Cancel" });
    const destroy = screen.getByRole("button", { name: "Delete workspace" });
    await waitFor(() => expect(cancel).toBe(document.activeElement));

    destroy.focus();
    await fireEvent.keyDown(destroy, { key: "Tab" });
    expect(cancel).toBe(document.activeElement);

    cancel.focus();
    await fireEvent.keyDown(cancel, { key: "Tab", shiftKey: true });
    expect(destroy).toBe(document.activeElement);
  });
});
