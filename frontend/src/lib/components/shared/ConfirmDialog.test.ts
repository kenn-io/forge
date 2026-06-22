import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import ConfirmDialog from "./ConfirmDialog.svelte";

afterEach(() => cleanup());

describe("ConfirmDialog", () => {
  // Destructive confirm dialogs previously focused their Cancel button on open.
  // After moving to the shared Modal shell the generic fallback focused the
  // header close (X) button instead, so the safe default action no longer held
  // initial focus. Lock the Cancel-first behavior in.
  it("focuses Cancel instead of the header close or destructive button", async () => {
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

    const cancel = screen.getByRole("button", { name: "Cancel" });
    await waitFor(() => expect(cancel).toBe(document.activeElement));

    expect(screen.getByRole("button", { name: "Close" })).not.toBe(document.activeElement);
    expect(screen.getByRole("button", { name: "Delete workspace" })).not.toBe(document.activeElement);
  });
});
