import { describe, expect, test, vi } from "vite-plus/test";
import { renderMarkdownMermaidDiagrams } from "./markdownMermaid";

function mermaidLoader() {
  return {
    initialize: vi.fn(),
    run: vi.fn().mockResolvedValue(undefined),
  };
}

describe("renderMarkdownMermaidDiagrams", () => {
  test("does not load mermaid when the rendered markdown has no mermaid diagrams", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre><code>plain code</code></pre></div>';
    const loader = vi.fn(async () => mermaidLoader());

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(0);
    expect(loader).not.toHaveBeenCalled();
  });

  test("runs mermaid against unrendered markdown diagram blocks", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader();
    const loader = vi.fn(async () => mermaid);

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(1);
    expect(mermaid.initialize).toHaveBeenCalledWith({
      startOnLoad: false,
      securityLevel: "strict",
      secure: ["securityLevel", "startOnLoad"],
    });
    expect(mermaid.run).toHaveBeenCalledWith({
      nodes: [root.querySelector("pre.mermaid")],
      suppressErrors: true,
    });
  });

  test("does not render the same mermaid block twice", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="doc-markdown"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader();
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);
    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(0);
    expect(mermaid.run).toHaveBeenCalledTimes(1);
  });
});
