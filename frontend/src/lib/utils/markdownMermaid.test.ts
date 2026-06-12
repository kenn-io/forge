import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import { getStackDepth, getTopFrame, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";
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

const TEST_THEME_VARS = {
  light: {
    "--font-sans": "Inter, sans-serif",
    "--font-size-md": "13px",
    "--mermaid-bg": "#ffffff",
    "--mermaid-viewer-bg": "#f6f8fa",
    "--mermaid-node-bg": "#ffffff",
    "--mermaid-node-text": "#24292f",
    "--mermaid-node-border": "#d0d7de",
    "--mermaid-cluster-bg": "#f6f8fa",
    "--mermaid-cluster-text": "#24292f",
    "--mermaid-cluster-border": "#d0d7de",
    "--mermaid-line": "#57606a",
    "--mermaid-text": "#24292f",
    "--mermaid-label-bg": "#ffffff",
    "--mermaid-label-text": "#24292f",
    "--mermaid-note-bg": "#fff8c5",
    "--mermaid-note-text": "#24292f",
    "--mermaid-note-border": "#d4a72c",
  },
  dark: {
    "--font-sans": "Inter, sans-serif",
    "--font-size-md": "13px",
    "--mermaid-bg": "#0d1117",
    "--mermaid-viewer-bg": "#4a4d4b",
    "--mermaid-node-bg": "#f6f8fa",
    "--mermaid-node-text": "#24292f",
    "--mermaid-node-border": "#d0d7de",
    "--mermaid-cluster-bg": "#4a4d4b",
    "--mermaid-cluster-text": "#f0f6fc",
    "--mermaid-cluster-border": "#4a4d4b",
    "--mermaid-line": "#c9d1d9",
    "--mermaid-text": "#c9d1d9",
    "--mermaid-label-bg": "#30363d",
    "--mermaid-label-text": "#f0f6fc",
    "--mermaid-note-bg": "#30363d",
    "--mermaid-note-text": "#f0f6fc",
    "--mermaid-note-border": "#8b949e",
  },
} as const;

const TEST_THEME_VAR_NAMES = Object.keys(TEST_THEME_VARS.light);

function installMermaidThemeVars(theme: keyof typeof TEST_THEME_VARS): void {
  const style = document.documentElement.style;
  for (const [name, value] of Object.entries(TEST_THEME_VARS[theme])) {
    style.setProperty(name, value);
  }
}

function clearMermaidThemeVars(): void {
  for (const name of TEST_THEME_VAR_NAMES) {
    document.documentElement.style.removeProperty(name);
  }
}

async function flushQueuedRender(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise<void>((resolve) => window.setTimeout(resolve, 0));
}

describe("renderMarkdownMermaidDiagrams", () => {
  beforeEach(() => {
    resetModalStack();
    installMermaidThemeVars("light");
  });

  afterEach(() => {
    resetModalStack();
    clearMermaidThemeVars();
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
      secure: [
        "secure",
        "securityLevel",
        "startOnLoad",
        "maxTextSize",
        "suppressErrorRendering",
        "maxEdges",
        "dompurifyConfig",
        "htmlLabels",
        "themeCSS",
        "fontFamily",
        "altFontFamily",
      ],
      maxTextSize: 50_000,
      maxEdges: 500,
      suppressErrorRendering: true,
      dompurifyConfig: {
        FORBID_ATTR: ["style"],
        FORBID_TAGS: ["style"],
      },
      htmlLabels: false,
      themeCSS: "",
      fontFamily: "Inter, sans-serif",
      altFontFamily: "Inter, sans-serif",
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
    installMermaidThemeVars("dark");
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
      secure: [
        "secure",
        "securityLevel",
        "startOnLoad",
        "maxTextSize",
        "suppressErrorRendering",
        "maxEdges",
        "dompurifyConfig",
        "htmlLabels",
        "themeCSS",
        "fontFamily",
        "altFontFamily",
      ],
      maxTextSize: 50_000,
      maxEdges: 500,
      suppressErrorRendering: true,
      dompurifyConfig: {
        FORBID_ATTR: ["style"],
        FORBID_TAGS: ["style"],
      },
      htmlLabels: false,
      themeCSS: "",
      fontFamily: "Inter, sans-serif",
      altFontFamily: "Inter, sans-serif",
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

  test("restores the source and holds failed mermaid renders until the source changes", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const renderError = new Error("render failed");
    let rejectOnce = true;
    const run = vi.fn<MarkdownMermaidAPI["run"]>(async ({ nodes }) => {
      if (rejectOnce) {
        rejectOnce = false;
        for (const node of Array.from(nodes)) {
          node.dataset.processed = "true";
          node.textContent = "Syntax error";
        }
        throw renderError;
      }
      renderSvgInto(nodes);
    });
    const mermaid = mermaidLoader(run);
    const loader = vi.fn(async () => mermaid);

    await expect(renderMarkdownMermaidDiagrams(root, loader)).rejects.toThrow(renderError);
    expect(root.querySelector<HTMLElement>("pre.mermaid")?.dataset.mermaidRendered).toBe("failed");
    expect(root.querySelector<HTMLElement>("pre.mermaid")?.dataset.processed).toBeUndefined();
    expect(root.querySelector<HTMLElement>("pre.mermaid")?.textContent).toBe("graph TD\nA-->B");

    const unchangedRender = await renderMarkdownMermaidDiagrams(root, loader);
    expect(unchangedRender).toBe(0);

    const block = root.querySelector<HTMLElement>("pre.mermaid");
    if (block) block.textContent = "graph TD\nA-->C";
    const rendered = await renderMarkdownMermaidDiagrams(root, loader);
    expect(rendered).toBe(1);
    expect(mermaid.run).toHaveBeenCalledTimes(2);
  });

  test("restores the source and holds suppressed mermaid render failures until the source changes", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    let suppressOnce = true;
    const run = vi.fn<MarkdownMermaidAPI["run"]>(async ({ nodes }) => {
      if (suppressOnce) {
        suppressOnce = false;
        for (const node of Array.from(nodes)) {
          node.dataset.processed = "true";
          node.textContent = "Syntax error";
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
    expect(block?.dataset.mermaidRendered).toBe("failed");
    expect(block?.dataset.processed).toBeUndefined();
    expect(block?.textContent).toBe("graph TD\nA-->B");

    const unchangedRender = await renderMarkdownMermaidDiagrams(root, loader);
    expect(unchangedRender).toBe(0);

    if (block) block.textContent = "graph TD\nA-->C";
    const secondRender = await renderMarkdownMermaidDiagrams(root, loader);
    expect(secondRender).toBe(1);
    expect(mermaid.run).toHaveBeenCalledTimes(2);
    expect(block?.classList.contains("mermaid-viewer")).toBe(true);
  });

  test("leaves Mermaid blocks beyond the per-document count limit unrendered", async () => {
    const root = document.createElement("div");
    root.innerHTML = `<div class="markdown-body">${Array.from(
      { length: 26 },
      (_, index) => `<pre class="mermaid">graph TD\nA${index}-->B${index}</pre>`,
    ).join("")}</div>`;
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderSvgInto(nodes);
    });
    const loader = vi.fn(async () => mermaid);

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(25);
    expect(mermaid.run).toHaveBeenCalledWith({
      nodes: Array.from(root.querySelectorAll("pre.mermaid")).slice(0, 25),
      suppressErrors: true,
    });
    const skipped = Array.from(root.querySelectorAll<HTMLElement>("pre")).at(-1);
    expect(skipped?.classList.contains("mermaid")).toBe(false);
    expect(skipped?.textContent).toBe("graph TD\nA25-->B25");
  });

  test("leaves Mermaid blocks beyond the per-document source byte limit unrendered", async () => {
    const root = document.createElement("div");
    root.innerHTML = `<div class="markdown-body"><pre class="mermaid">${"A".repeat(200_001)}</pre></div>`;
    const loader = vi.fn(async () => mermaidLoader());

    const rendered = await renderMarkdownMermaidDiagrams(root, loader);

    expect(rendered).toBe(0);
    expect(loader).not.toHaveBeenCalled();
    const skipped = root.querySelector<HTMLElement>("pre");
    expect(skipped?.classList.contains("mermaid")).toBe(false);
    expect(skipped?.textContent).toBe("A".repeat(200_001));
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

  test("blocks global shortcuts while the expanded diagram overlay is open", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    const mermaid = mermaidLoader(async ({ nodes }) => {
      renderSvgInto(nodes);
    });
    const loader = vi.fn(async () => mermaid);
    const windowShortcut = vi.fn();
    window.addEventListener("keydown", windowShortcut);

    try {
      await renderMarkdownMermaidDiagrams(root, loader);
      root.querySelector<HTMLButtonElement>('button[aria-label="Open diagram in expanded view"]')?.click();

      const overlay = document.querySelector<HTMLElement>(".mermaid-viewer-lightbox");
      const closeButton = overlay?.querySelector<HTMLButtonElement>('button[aria-label="Close expanded diagram"]');
      expect(getTopFrame()?.frameId).toBe("mermaid-lightbox");
      expect(getStackDepth()).toBe(1);

      closeButton?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "1" }));
      expect(windowShortcut).not.toHaveBeenCalled();

      const escape = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "Escape" });
      closeButton?.dispatchEvent(escape);
      expect(escape.defaultPrevented).toBe(true);
      expect(document.querySelector(".mermaid-viewer-lightbox")).toBeNull();
      expect(getStackDepth()).toBe(0);
    } finally {
      window.removeEventListener("keydown", windowShortcut);
    }
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

      installMermaidThemeVars("dark");
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

  test("rerenders if the app theme changes during an in-flight render", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><pre class="mermaid">graph TD\nA-->B</pre></div>';
    document.body.append(root);
    let activeRenderTheme = "light";
    let releaseFirstRender: (() => void) | undefined;
    let runCount = 0;
    const run = vi.fn<MarkdownMermaidAPI["run"]>(async ({ nodes }) => {
      runCount += 1;
      const renderTheme = activeRenderTheme;
      if (runCount === 1) {
        await new Promise<void>((resolve) => {
          releaseFirstRender = resolve;
        });
      }
      for (const node of Array.from(nodes)) {
        node.innerHTML = `<svg data-theme="${renderTheme}" viewBox="0 0 120 60"><text>diagram</text></svg>`;
      }
    });
    const mermaid = mermaidLoader(run);
    vi.mocked(mermaid.initialize).mockImplementation((config) => {
      activeRenderTheme = config.themeVariables.darkMode === true ? "dark" : "light";
    });
    const loader = vi.fn(async () => mermaid);
    const controller = initMarkdownMermaidRendering(root, loader);

    try {
      await flushQueuedRender();
      expect(mermaid.run).toHaveBeenCalledTimes(1);

      installMermaidThemeVars("dark");
      document.documentElement.classList.add("dark");
      await flushQueuedRender();
      releaseFirstRender?.();
      await flushQueuedRender();
      await flushQueuedRender();

      expect(root.querySelector("svg")?.getAttribute("data-theme")).toBe("dark");
      expect(mermaid.run).toHaveBeenCalledTimes(2);
    } finally {
      controller.disconnect();
      root.remove();
    }
  });
});
