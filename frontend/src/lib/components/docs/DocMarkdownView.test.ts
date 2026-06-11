import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import DocMarkdownView from "./DocMarkdownView.svelte";
import { buildFolderIndex } from "../../api/docs/folderLinks";
import type { DocsMarkdownOptions } from "../../api/docs/markdown";

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

describe("DocMarkdownView", () => {
  test("renders paragraphs", () => {
    render(DocMarkdownView, {
      props: {
        source: "First paragraph.\n\nSecond paragraph.",
        options: options(),
      },
    });

    expect(screen.getByText("First paragraph.").tagName).toBe("P");
    expect(screen.getByText("Second paragraph.").tagName).toBe("P");
  });

  test("renders safe links with noreferrer and neutralizes unsafe protocols", () => {
    const { container } = render(DocMarkdownView, {
      props: {
        source:
          "[Docs](https://example.com/docs) [Email](mailto:ops@example.com) [Bad](javascript:alert(1)) [FTP](ftp://example.com/file)",
        options: options(),
      },
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
    const { container } = render(DocMarkdownView, {
      props: {
        source: "Run `middleman sync` before opening.",
        options: options(),
      },
    });

    const code = container.querySelector("code");
    expect(code?.textContent).toBe("middleman sync");
  });

  test("renders lists", () => {
    render(DocMarkdownView, {
      props: {
        source: "- First\n- Second",
        options: options(),
      },
    });

    expect(screen.getByRole("list")).toBeTruthy();
    expect(screen.getByText("First").tagName).toBe("LI");
    expect(screen.getByText("Second").tagName).toBe("LI");
  });

  test("strips dangerous raw HTML", () => {
    const { container } = render(DocMarkdownView, {
      props: {
        source: 'Before <img src=x onerror="alert(1)"><script>alert(1)</script> after',
        options: options(),
      },
    });

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.innerHTML).not.toContain("onerror");
    expect(screen.getByText(/Before/)).toBeTruthy();
  });

  test("short-id links dispatch short-id handler before generic issue links", async () => {
    const onSelectIssue = vi.fn();
    const onSelectKataShortId = vi.fn();
    render(DocMarkdownView, {
      props: {
        source: "See #budget",
        options: options(),
        onSelectIssue,
        onSelectKataShortId,
      },
    });

    await fireEvent.click(screen.getByRole("link", { name: "#budget" }));

    expect(onSelectKataShortId).toHaveBeenCalledWith("budget", undefined);
    expect(onSelectIssue).not.toHaveBeenCalled();
  });
});
