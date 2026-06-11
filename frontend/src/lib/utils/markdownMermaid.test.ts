import { describe, expect, test, vi } from "vite-plus/test";
import { renderMarkdownMermaidDiagrams, type MarkdownMermaidAPI } from "./markdownMermaid";

function mermaidLoader(run?: MarkdownMermaidAPI["run"]): MarkdownMermaidAPI {
  const defaultRun: MarkdownMermaidAPI["run"] = async () => undefined;
  return {
    initialize: vi.fn(),
    run: vi.fn(run ?? defaultRun),
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
      theme: "base",
      themeVariables: expect.objectContaining({
        background: "#0d1117",
        clusterBkg: "#4a4d4b",
        darkMode: true,
        edgeLabelBackground: "#30363d",
        labelTextColor: "#f0f6fc",
        lineColor: "#c9d1d9",
        primaryColor: "#f6f8fa",
      }),
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

  test("does not start another render for pending mermaid blocks", async () => {
    const root = document.createElement("div");
    root.innerHTML =
      '<div class="markdown-body"><pre class="mermaid" data-mermaid-rendered="pending">graph TD\nA-->B</pre></div>';
    const loader = vi.fn(async () => mermaidLoader());

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(0);
    expect(loader).not.toHaveBeenCalled();
  });

  test("wraps rendered diagrams in viewer controls", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader(async ({ nodes }) => {
      for (const node of Array.from(nodes)) {
        node.innerHTML = '<svg viewBox="0 0 120 60"><text>diagram</text></svg>';
      }
    });
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);

    const pre = root.querySelector("pre.mermaid");
    const pan = root.querySelector<HTMLElement>(".mermaid-viewer__pan");
    expect(pre?.classList.contains("mermaid-viewer")).toBe(true);
    expect(root.querySelector(".mermaid-viewer__viewport svg")).not.toBeNull();
    expect(root.querySelectorAll(".mermaid-viewer__button")).toHaveLength(9);
    const expandButton = root.querySelector<HTMLButtonElement>('button[aria-label="Open diagram in expanded view"]');
    expect(expandButton?.querySelector("svg")).not.toBeNull();
    expect(expandButton?.textContent?.trim()).toBe("");
    expect(pan?.style.transform).toBe("translate(0px, 0px) scale(1)");

    root.querySelector<HTMLButtonElement>('button[aria-label="Zoom in diagram"]')?.click();
    expect(pan?.style.transform).toBe("translate(0px, 0px) scale(1.2)");

    root.querySelector<HTMLButtonElement>('button[aria-label="Reset diagram view"]')?.click();
    expect(pan?.style.transform).toBe("translate(0px, 0px) scale(1)");
  });

  test("copies the original mermaid source", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="doc-markdown"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const mermaid = mermaidLoader(async ({ nodes }) => {
      for (const node of Array.from(nodes)) {
        node.innerHTML = '<svg viewBox="0 0 120 60"></svg>';
      }
    });
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);
    root.querySelector<HTMLButtonElement>('button[aria-label="Copy Mermaid source"]')?.click();

    expect(writeText).toHaveBeenCalledWith("graph TD\nA-->B");
  });

  test("opens an expanded diagram overlay from the top control", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader(async ({ nodes }) => {
      for (const node of Array.from(nodes)) {
        node.innerHTML = '<svg viewBox="0 0 120 60"><text>diagram</text></svg>';
      }
    });
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);
    root.querySelector<HTMLButtonElement>('button[aria-label="Open diagram in expanded view"]')?.click();

    const overlay = document.querySelector<HTMLElement>(".mermaid-viewer-lightbox");
    const overlayPan = document.querySelector<HTMLElement>(".mermaid-viewer-lightbox .mermaid-viewer__pan");
    expect(overlay?.getAttribute("role")).toBe("dialog");
    expect(overlay?.getAttribute("aria-modal")).toBe("true");
    expect(overlay?.querySelector("svg")).not.toBeNull();
    expect(overlay?.querySelectorAll(".mermaid-viewer__controls--nav .mermaid-viewer__button")).toHaveLength(7);
    expect(overlay?.querySelector('button[aria-label="Copy Mermaid source"]')).toBeNull();

    overlay?.querySelector<HTMLButtonElement>('button[aria-label="Zoom in diagram"]')?.click();
    expect(overlayPan?.style.transform).toBe("translate(0px, 0px) scale(1.2)");

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(document.querySelector(".mermaid-viewer-lightbox")).toBeNull();
  });
});
