import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import { buildFolderIndex } from "../../api/docs/folderLinks";
import type { DocsMarkdownOptions } from "../../api/docs/markdown";
import DocMarkdownView from "./DocMarkdownView.svelte";

function options(): DocsMarkdownOptions {
  return {
    folderID: "notes",
    currentDocPath: "README.md",
    index: buildFolderIndex(null),
    buildDocURL: (_folderID, relPath) => `/docs?doc=${encodeURIComponent(relPath)}`,
    buildBlobURL: (_folderID, relPath) => `/api/v1/docs/folders/notes/blob?path=${encodeURIComponent(relPath)}`,
  };
}

describe("DocMarkdownView details blocks (browser)", () => {
  it("renders GitHub-style details blocks as native toggleable disclosures", async () => {
    const { container } = render(DocMarkdownView, {
      props: {
        source: [
          "<details>",
          "",
          "<summary>Tips for collapsed sections</summary>",
          "",
          "### You can add a header",
          "",
          "You can add text within a collapsed section.",
          "",
          "</details>",
        ].join("\n"),
        options: options(),
      },
    });

    const details = container.querySelector("details");
    expect(details).not.toBeNull();
    const heading = container.querySelector("h3");
    expect(heading).not.toBeNull();
    const summary = page.getByText("Tips for collapsed sections");

    await expect.element(summary).toBeVisible();
    expect(details?.open).toBe(false);
    expect(heading?.checkVisibility()).toBe(false);

    await summary.click();

    expect(details?.open).toBe(true);
    expect(heading?.checkVisibility()).toBe(true);

    await summary.click();

    expect(details?.open).toBe(false);
    expect(heading?.checkVisibility()).toBe(false);
  });
});
