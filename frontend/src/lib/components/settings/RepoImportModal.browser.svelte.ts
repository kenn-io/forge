import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import { pressKey } from "../../../test/browserAppHarness.js";
import RepoImportModal from "./RepoImportModal.svelte";

function requireElement<T extends HTMLElement>(selector: string): T {
  const element = document.querySelector<T>(selector);
  expect(element).not.toBeNull();
  return element!;
}

function controlByLabel<T extends HTMLElement>(labelText: string, selector: string): T {
  const label = Array.from(document.querySelectorAll("label")).find((candidate) =>
    candidate.textContent?.includes(labelText),
  );
  expect(label).not.toBeUndefined();
  const control = label!.querySelector<T>(selector);
  expect(control).not.toBeNull();
  return control!;
}

// Mid-cycle Tab movement is native browser behavior under kit-ui's focus
// trap (it only intercepts at the boundaries), so synthetic key events can
// only exercise the wrap points. Option buttons staying out of the tab order
// (tabindex="-1") is pinned in SelectDropdown's own test.
describe("RepoImportModal focus trap (browser)", () => {
  it("focuses the pattern input on open and wraps Tab at the trap boundaries", async () => {
    render(RepoImportModal, {
      props: { open: true, onClose: vi.fn(), onImported: vi.fn() },
    });

    await expect.element(page.getByRole("dialog", { name: "Add repositories" })).toBeVisible();

    const close = requireElement<HTMLButtonElement>("button[aria-label='Close']");
    const pattern = controlByLabel<HTMLInputElement>("Repository pattern", "input");
    const cancel = page.getByRole("button", { name: "Cancel" }).element() as HTMLButtonElement;

    // The dialog prefers its pattern input over the first tabbable control.
    await vi.waitFor(() => expect(document.activeElement).toBe(pattern));

    // Shift+Tab from the first tabbable (the header X) wraps to the last
    // enabled control (Cancel — the submit button is disabled with nothing
    // selected), and Tab from there wraps forward again.
    close.focus();
    pressKey("Tab", { shift: true }, close);
    expect(document.activeElement).toBe(cancel);

    pressKey("Tab", {}, cancel);
    expect(document.activeElement).toBe(close);
  });
});
