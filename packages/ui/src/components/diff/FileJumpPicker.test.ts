import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { DiffFile } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import type { StoreInstances } from "../../types.js";
import FileJumpPicker from "./FileJumpPicker.svelte";

const files: DiffFile[] = [makeFile("src/components/App.svelte"), makeFile("src/lib/search.ts"), makeFile("README.md")];

function makeFile(path: string): DiffFile {
  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 0,
    patch: "",
    hunks: [],
  };
}

function renderPicker() {
  const requestScrollToFile = vi.fn();
  const diff = {
    getVisibleFileList: () => ({ stale: false, files }),
    getVisibleDiffFiles: () => files,
    getActiveFile: () => files[1]!.path,
    requestScrollToFile,
  };
  render(FileJumpPicker, {
    context: new Map([[STORES_KEY, { diff } as unknown as StoreInstances]]),
  });
  return { requestScrollToFile };
}

describe("FileJumpPicker", () => {
  afterEach(cleanup);

  it("filters file options with the shared typeahead", async () => {
    renderPicker();

    await fireEvent.click(screen.getByRole("button", { name: "Jump to file" }));
    const search = screen.getByRole("combobox", { name: "Jump to file" });
    expect(document.activeElement).toBe(search);

    await fireEvent.input(search, { target: { value: "readme" } });

    const options = screen.getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]!.textContent).toContain("README.md");
  });

  it("selects the highlighted file from the keyboard", async () => {
    const { requestScrollToFile } = renderPicker();

    const trigger = screen.getByRole("button", { name: "Jump to file" });
    await fireEvent.click(trigger);
    const search = screen.getByRole("combobox", { name: "Jump to file" });
    await fireEvent.keyDown(search, { key: "ArrowDown" });
    await fireEvent.keyDown(search, { key: "Enter" });

    expect(requestScrollToFile).toHaveBeenCalledOnce();
    expect(requestScrollToFile).toHaveBeenCalledWith("README.md");
    expect(screen.queryByRole("combobox", { name: "Jump to file" })).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("scrolls the keyboard-highlighted file into view", async () => {
    const scrollIntoView = vi.fn();
    vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(scrollIntoView);
    renderPicker();

    await fireEvent.click(screen.getByRole("button", { name: "Jump to file" }));
    scrollIntoView.mockClear();
    const search = screen.getByRole("combobox", { name: "Jump to file" });
    await fireEvent.keyDown(search, { key: "ArrowDown" });

    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" }));
  });

  it("clears a query before Escape closes the picker", async () => {
    renderPicker();

    const trigger = screen.getByRole("button", { name: "Jump to file" });
    await fireEvent.click(trigger);
    const search = screen.getByRole("combobox", { name: "Jump to file" }) as HTMLInputElement;
    await fireEvent.input(search, { target: { value: "readme" } });

    await fireEvent.keyDown(search, { key: "Escape" });
    expect((screen.getByRole("combobox", { name: "Jump to file" }) as HTMLInputElement).value).toBe("");

    await fireEvent.keyDown(search, { key: "Escape" });
    expect(screen.queryByRole("combobox", { name: "Jump to file" })).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });
});
