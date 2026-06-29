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

describe("RepoImportModal focus trap (browser)", () => {
  it("cycles Tab and Shift+Tab through controls in rendered order", async () => {
    render(RepoImportModal, {
      props: { open: true, onClose: vi.fn(), onImported: vi.fn() },
    });

    await expect.element(page.getByRole("dialog", { name: "Add repositories" })).toBeVisible();

    const close = requireElement<HTMLButtonElement>("button[aria-label='Close']");
    const provider = controlByLabel<HTMLSelectElement>("Provider", "select");
    const host = controlByLabel<HTMLInputElement>("Host", "input");
    const pattern = controlByLabel<HTMLInputElement>("Repository pattern", "input");
    const cancel = page.getByRole("button", { name: "Cancel" }).element() as HTMLButtonElement;

    await vi.waitFor(() => expect(document.activeElement).toBe(pattern));

    pressKey("Tab", { shift: true }, pattern);
    expect(document.activeElement).toBe(host);

    pressKey("Tab", { shift: true }, host);
    expect(document.activeElement).toBe(provider);

    pressKey("Tab", { shift: true }, provider);
    expect(document.activeElement).toBe(close);

    pressKey("Tab", { shift: true }, close);
    expect(document.activeElement).toBe(cancel);

    pressKey("Tab", {}, cancel);
    expect(document.activeElement).toBe(close);
  });
});
