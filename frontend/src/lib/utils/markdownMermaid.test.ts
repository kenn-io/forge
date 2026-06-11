import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import {
  initMarkdownMermaidRendering,
  renderMarkdownMermaidDiagrams,
  type MarkdownMermaidAPI,
} from "./markdownMermaid";

function mermaidLoader(run?: MarkdownMermaidAPI["run"]): MarkdownMermaidAPI {
  const defaultRun: MarkdownMermaidAPI["run"] = async () => undefined;
  return {
    initialize: vi.fn(),
    run: vi.fn(run ?? defaultRun),
  };
}

function renderSvgInto(nodes: ArrayLike<HTMLElement>): void {
  for (const node of Array.from(nodes)) {
    node.innerHTML = '<svg viewBox="0 0 120 60"><text>diagram</text></svg>';
  }
}

async function flushQueuedRender(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise<void>((resolve) => window.setTimeout(resolve, 0));
}

describe("renderMarkdownMermaidDiagrams", () => {
  afterEach(() => {
    document.documentElement.classList.remove("dark");
    document.querySelectorAll(".mermaid-viewer-lightbox").forEach((node) => node.remove());
  });

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
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderSvgInto(nodes);
    });
    const loader = vi.fn(async () => mermaid);

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(1);
    expect(mermaid.initialize).toHaveBeenCalledWith({
      startOnLoad: false,
      securityLevel: "strict",
      secure: ["securityLevel", "startOnLoad"],
      theme: "base",
      themeVariables: expect.objectContaining({
        background: "#ffffff",
        clusterBkg: "#f6f8fa",
        darkMode: false,
        edgeLabelBackground: "#ffffff",
        labelTextColor: "#24292f",
        lineColor: "#57606a",
        primaryColor: "#ffffff",
      }),
    });
    expect(mermaid.run).toHaveBeenCalledWith({
      nodes: [root.querySelector("pre.mermaid")],
      suppressErrors: true,
    });
  });

  test("uses dark mermaid theme variables when the app is in dark mode", async () => {
    document.documentElement.classList.add("dark");
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderSvgInto(nodes);
    });
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);

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
  });

  test("does not render the same mermaid block twice", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="doc-markdown"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderSvgInto(nodes);
    });
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

  test("allows failed mermaid renders to be retried", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const renderError = new Error("render failed");
    let rejectOnce = true;
    const run = vi.fn<MarkdownMermaidAPI["run"]>(async ({ nodes }) => {
      if (rejectOnce) {
        rejectOnce = false;
        for (const node of Array.from(nodes)) {
          node.dataset.processed = "true";
        }
        throw renderError;
      }
      renderSvgInto(nodes);
    });
    const mermaid = mermaidLoader(run);
    const loader = vi.fn(async () => mermaid);

    await expect(renderMarkdownMermaidDiagrams(root, loader)).rejects.toThrow(renderError);
    expect(root.querySelector<HTMLElement>("pre.mermaid")?.dataset.mermaidRendered).toBeUndefined();
    expect(root.querySelector<HTMLElement>("pre.mermaid")?.dataset.processed).toBeUndefined();

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(1);
    expect(mermaid.run).toHaveBeenCalledTimes(2);
  });

  test("allows suppressed mermaid render failures to be retried", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    let suppressOnce = true;
    const run = vi.fn<MarkdownMermaidAPI["run"]>(async ({ nodes }) => {
      if (suppressOnce) {
        suppressOnce = false;
        for (const node of Array.from(nodes)) {
          node.dataset.processed = "true";
        }
        return;
      }
      renderSvgInto(nodes);
    });
    const mermaid = mermaidLoader(run);
    const loader = vi.fn(async () => mermaid);

    const firstRender = await renderMarkdownMermaidDiagrams(root, loader);
    const block = root.querySelector<HTMLElement>("pre.mermaid");
    expect(firstRender).toBe(0);
    expect(block?.dataset.mermaidRendered).toBeUndefined();
    expect(block?.dataset.processed).toBeUndefined();

    const secondRender = await renderMarkdownMermaidDiagrams(root, loader);

    expect(secondRender).toBe(1);
    expect(mermaid.run).toHaveBeenCalledTimes(2);
    expect(block?.classList.contains("mermaid-viewer")).toBe(true);
  });

  test("wraps rendered diagrams in viewer controls", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderSvgInto(nodes);
    });
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);

    const pre = root.querySelector("pre.mermaid");
    const viewport = root.querySelector<HTMLElement>(".mermaid-viewer__viewport");
    const pan = root.querySelector<HTMLElement>(".mermaid-viewer__pan");
    expect(pre?.classList.contains("mermaid-viewer")).toBe(true);
    expect(root.querySelector(".mermaid-viewer__viewport svg")).not.toBeNull();
    expect(root.querySelectorAll(".mermaid-viewer__button")).toHaveLength(7);
    expect(root.querySelector('button[aria-label="Zoom in diagram"]')).toBeNull();
    expect(root.querySelector('button[aria-label="Zoom out diagram"]')).toBeNull();
    const expandButton = root.querySelector<HTMLButtonElement>('button[aria-label="Open diagram in expanded view"]');
    expect(expandButton?.querySelector("svg")).toBeNull();
    expect(expandButton?.textContent?.trim()).toBe("⟷");
    expect(pan?.style.transform).toBe("translate(0px, 0px) scale(1)");

    Object.defineProperty(viewport, "getBoundingClientRect", {
      configurable: true,
      value: () => ({ bottom: 60, height: 60, left: 0, right: 120, top: 0, width: 120, x: 0, y: 0 }),
    });
    const wheelZoomIn = new WheelEvent("wheel", { cancelable: true, clientX: 60, clientY: 30, deltaY: -100 });
    expect(viewport?.dispatchEvent(wheelZoomIn)).toBe(false);
    expect(wheelZoomIn.defaultPrevented).toBe(true);
    expect(pan?.style.transform).toBe("translate(0px, 0px) scale(1.16)");

    root.querySelector<HTMLButtonElement>('button[aria-label="Pan diagram right"]')?.click();
    expect(pan?.style.transform).toBe("translate(80px, 0px) scale(1.16)");

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
      renderSvgInto(nodes);
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
      renderSvgInto(nodes);
    });
    const loader = vi.fn(async () => mermaid);

    await renderMarkdownMermaidDiagrams(root, loader);
    root.querySelector<HTMLButtonElement>('button[aria-label="Open diagram in expanded view"]')?.click();

    const overlay = document.querySelector<HTMLElement>(".mermaid-viewer-lightbox");
    const overlayViewport = document.querySelector<HTMLElement>(".mermaid-viewer-lightbox .mermaid-viewer__viewport");
    const overlayPan = document.querySelector<HTMLElement>(".mermaid-viewer-lightbox .mermaid-viewer__pan");
    expect(overlay?.getAttribute("role")).toBe("dialog");
    expect(overlay?.getAttribute("aria-modal")).toBe("true");
    expect(overlay?.querySelector("svg")).not.toBeNull();
    expect(overlay?.querySelectorAll(".mermaid-viewer__controls--nav .mermaid-viewer__button")).toHaveLength(5);
    expect(overlay?.querySelector('button[aria-label="Copy Mermaid source"]')).toBeNull();
    expect(overlay?.querySelector('button[aria-label="Zoom in diagram"]')).toBeNull();

    Object.defineProperty(overlayViewport, "getBoundingClientRect", {
      configurable: true,
      value: () => ({ bottom: 60, height: 60, left: 0, right: 120, top: 0, width: 120, x: 0, y: 0 }),
    });
    const wheelZoomIn = new WheelEvent("wheel", { cancelable: true, clientX: 60, clientY: 30, deltaY: -100 });
    expect(overlayViewport?.dispatchEvent(wheelZoomIn)).toBe(false);
    expect(overlayPan?.style.transform).toBe("translate(0px, 0px) scale(1.16)");

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(document.querySelector(".mermaid-viewer-lightbox")).toBeNull();
  });

  test("rerenders diagrams when the app theme changes", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    document.body.append(root);
    let renderCount = 0;
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderCount += 1;
      for (const node of Array.from(nodes)) {
        node.innerHTML = `<svg data-render="${renderCount}" viewBox="0 0 120 60"><text>diagram</text></svg>`;
      }
    });
    const loader = vi.fn(async () => mermaid);
    const controller = initMarkdownMermaidRendering(root, loader);

    try {
      await flushQueuedRender();

      expect(root.querySelector("svg")?.getAttribute("data-render")).toBe("1");
      expect(mermaid.initialize).toHaveBeenLastCalledWith(
        expect.objectContaining({
          themeVariables: expect.objectContaining({ darkMode: false }),
        }),
      );

      document.documentElement.classList.add("dark");
      await flushQueuedRender();

      expect(root.querySelector("svg")?.getAttribute("data-render")).toBe("2");
      expect(mermaid.run).toHaveBeenCalledTimes(2);
      expect(mermaid.initialize).toHaveBeenLastCalledWith(
        expect.objectContaining({
          themeVariables: expect.objectContaining({ darkMode: true }),
        }),
      );
    } finally {
      controller.disconnect();
      root.remove();
    }
  });
});
