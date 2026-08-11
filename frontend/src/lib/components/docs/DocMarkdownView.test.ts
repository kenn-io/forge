import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import type { ComponentProps } from "svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";
import DocMarkdownView from "./DocMarkdownView.svelte";
import { buildFolderIndex } from "../../api/docs/folderLinks";
import type { DocsMarkdownOptions } from "../../api/docs/markdown";
import type { TreeNode } from "../../api/docs/types";

afterEach(() => {
  cleanup();
});

function options(): DocsMarkdownOptions {
  return {
    folderID: "notes",
    currentDocPath: "README.md",
    index: buildFolderIndex(null),
    buildDocURL: (_folderID, relPath) => `/docs?doc=${encodeURIComponent(relPath)}`,
    buildBlobURL: (_folderID, relPath) => `/api/v1/docs/folders/notes/blob?path=${encodeURIComponent(relPath)}`,
  };
}

const tree: TreeNode = {
  name: "Notes",
  rel_path: "",
  is_dir: true,
  children: [
    {
      name: "Projects",
      rel_path: "Projects",
      is_dir: true,
      children: [{ name: "alpha.md", rel_path: "Projects/alpha.md", is_dir: false, size: 1 }],
    },
    {
      name: "Daily",
      rel_path: "Daily",
      is_dir: true,
      children: [{ name: "alpha.md", rel_path: "Daily/alpha.md", is_dir: false, size: 1 }],
    },
  ],
};

function indexedOptions(): DocsMarkdownOptions {
  return {
    ...options(),
    index: buildFolderIndex(tree),
  };
}

function renderMarkdownView(props: ComponentProps<typeof DocMarkdownView>) {
  return render(AppRuntimeHarness, { props: { component: DocMarkdownView, ...props } });
}

describe("DocMarkdownView", () => {
  test("renders paragraphs", () => {
    renderMarkdownView({
      source: "First paragraph.\n\nSecond paragraph.",
      options: options(),
    });

    expect(screen.getByText("First paragraph.").tagName).toBe("P");
    expect(screen.getByText("Second paragraph.").tagName).toBe("P");
  });

  test("renders safe links with noreferrer and neutralizes unsafe protocols", () => {
    const { container } = renderMarkdownView({
      source:
        "[Docs](https://example.com/docs) [Email](mailto:ops@example.com) [Bad](javascript:alert(1)) [FTP](ftp://example.com/file)",
      options: options(),
    });

    const docsLink = screen.getByRole("link", { name: "Docs" });
    expect(docsLink.getAttribute("href")).toBe("https://example.com/docs");
    expect(docsLink.getAttribute("rel")).toBe("noreferrer");
    expect(screen.getByRole("link", { name: "Email" }).getAttribute("href")).toBe("mailto:ops@example.com");

    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(container.querySelector('a[href^="ftp:"]')).toBeNull();
    expect(screen.getByText("Bad")).toBeTruthy();
    expect(screen.getByText("FTP")).toBeTruthy();
  });

  test("renders inline code", () => {
    const { container } = renderMarkdownView({
      source: "Run `kenn-forge sync` before opening.",
      options: options(),
    });

    const code = container.querySelector("code");
    expect(code?.textContent).toBe("kenn-forge sync");
  });

  test("renders lists", () => {
    renderMarkdownView({
      source: "- First\n- Second",
      options: options(),
    });

    expect(screen.getByRole("list")).toBeTruthy();
    expect(screen.getByText("First").tagName).toBe("LI");
    expect(screen.getByText("Second").tagName).toBe("LI");
  });

  test("strips dangerous raw HTML", () => {
    const { container } = renderMarkdownView({
      source: 'Before <img src=x onerror="alert(1)"><script>alert(1)</script> after',
      options: options(),
    });

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.innerHTML).not.toContain("onerror");
    expect(screen.getByText(/Before/)).toBeTruthy();
  });

  test("short-id links dispatch a Kata reference", async () => {
    const onSelectKataReference = vi.fn();
    renderMarkdownView({
      source: "See #budget",
      options: options(),
      onSelectKataReference,
    });

    await fireEvent.click(screen.getByRole("link", { name: "#budget" }));

    expect(onSelectKataReference).toHaveBeenCalledWith("budget", undefined, "reference");
  });

  test("ambiguous note picker keeps a visible close button", async () => {
    renderMarkdownView({
      source: "See [[alpha]].",
      options: indexedOptions(),
    });

    await fireEvent.click(screen.getByRole("link", { name: "alpha" }));

    const dialog = screen.getByRole("dialog", { name: "Pick a note" });
    expect(dialog).toBeTruthy();
    expect(screen.getByRole("button", { name: "Close" })).toBeTruthy();
  });
});
