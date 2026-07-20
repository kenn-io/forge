import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import MergeWarningsChip, { type MergeWarningEntry } from "./MergeWarningsChip.svelte";

const conflictEntry: MergeWarningEntry = {
  kind: "conflict",
  text: "This branch has conflicts that must be resolved before merging.",
};
const behindEntry: MergeWarningEntry = {
  kind: "behind",
  text: "This branch is behind the base branch and may need to be updated.",
};

function renderChip(warnings: MergeWarningEntry[], ontoggle = vi.fn()) {
  render(MergeWarningsChip, {
    props: {
      warnings,
      pullURL: "https://gitlab.com/acme/widget/-/merge_requests/7",
      providerLabel: "GitLab",
      ontoggle,
    },
  });
  return ontoggle;
}

describe("MergeWarningsChip", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders nothing when there are no warnings", () => {
    renderChip([]);
    expect(screen.queryByTestId("merge-warnings-chip")).toBeNull();
  });

  it("shows the Conflicts label with warning tone when a conflict entry exists", () => {
    renderChip([conflictEntry, behindEntry]);
    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("Conflicts");
    expect(chip.className).toContain("kit-chip--tone-warning");
  });

  it("shows a singular count with neutral tone for one non-conflict warning", () => {
    renderChip([behindEntry]);
    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("1 merge warning");
    expect(chip.className).toContain("kit-chip--tone-neutral");
  });

  it("pluralizes the count for multiple non-conflict warnings", () => {
    renderChip([behindEntry, { kind: "server", text: "Example sync warning" }]);
    expect(screen.getByTestId("merge-warnings-chip").textContent).toContain("2 merge warnings");
  });

  it("toggles the panel and reports through ontoggle", async () => {
    const ontoggle = renderChip([conflictEntry]);
    expect(screen.queryByText(conflictEntry.text)).toBeNull();

    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));
    expect(ontoggle).toHaveBeenLastCalledWith(true);
    expect(screen.getByText(conflictEntry.text)).toBeTruthy();

    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));
    expect(ontoggle).toHaveBeenLastCalledWith(false);
    expect(screen.queryByText(conflictEntry.text)).toBeNull();
  });

  it("lists entries in the given order with a provider link", async () => {
    renderChip([conflictEntry, behindEntry]);
    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));

    const lines = Array.from(document.querySelectorAll(".merge-warning-line")).map((line) => line.textContent?.trim());
    expect(lines).toEqual([conflictEntry.text, behindEntry.text]);

    const link = screen.getByRole("link", { name: "View on GitLab" });
    expect(link.getAttribute("href")).toBe("https://gitlab.com/acme/widget/-/merge_requests/7");
  });
});
