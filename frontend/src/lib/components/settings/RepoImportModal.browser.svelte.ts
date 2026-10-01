import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import RepoImportModalRuntimeHarness from "./RepoImportModalRuntimeHarness.svelte";

function controlByLabel<T extends HTMLElement>(labelText: string, selector: string): T {
  const label = Array.from(document.querySelectorAll("label")).find((candidate) =>
    candidate.textContent?.includes(labelText),
  );
  expect(label).not.toBeUndefined();
  const control = label!.querySelector<T>(selector);
  expect(control).not.toBeNull();
  return control!;
}

// Tab containment is kit-ui's trapFocus behavior and is tested there
// (context/testing.md, Library-owned behavior). Forge chooses which control
// takes focus when the dialog opens.
describe("RepoImportModal (browser)", () => {
  it("focuses the pattern input on open", async () => {
    await render(RepoImportModalRuntimeHarness, {
      props: { open: true, onClose: vi.fn(), onImported: vi.fn() },
    });

    await expect.element(page.getByRole("dialog", { name: "Add repositories" })).toBeVisible();

    const pattern = controlByLabel<HTMLInputElement>("Repository pattern", "input");

    // The dialog prefers its pattern input over the first tabbable control.
    await vi.waitFor(() => expect(document.activeElement).toBe(pattern));
  });
});
