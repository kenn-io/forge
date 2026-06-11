export interface MarkdownMermaidAPI {
  initialize: (config: { startOnLoad: false; securityLevel: "strict"; secure: string[] }) => void;
  run: (config: { nodes: ArrayLike<HTMLElement>; suppressErrors: true }) => Promise<unknown>;
}

export type MarkdownMermaidLoader = () => Promise<MarkdownMermaidAPI>;

export interface MarkdownMermaidController {
  renderNow: () => void;
  disconnect: () => void;
}

const MERMAID_SELECTOR = ".markdown-body pre.mermaid, .doc-markdown pre.mermaid";
let mermaidPromise: Promise<MarkdownMermaidAPI> | null = null;
const initializedMermaid = new WeakSet<MarkdownMermaidAPI>();

async function loadMermaid(): Promise<MarkdownMermaidAPI> {
  if (!mermaidPromise) {
    mermaidPromise = import("mermaid").then((module): MarkdownMermaidAPI => module.default);
  }
  return mermaidPromise;
}

export async function renderMarkdownMermaidDiagrams(
  root: ParentNode,
  load: MarkdownMermaidLoader = loadMermaid,
): Promise<number> {
  const nodes = Array.from(root.querySelectorAll<HTMLElement>(MERMAID_SELECTOR)).filter(
    (node) => node.dataset.mermaidRendered !== "true" && node.dataset.processed !== "true",
  );
  if (nodes.length === 0) return 0;

  for (const node of nodes) {
    node.dataset.mermaidRendered = "pending";
  }

  try {
    const mermaid = await load();
    if (!initializedMermaid.has(mermaid)) {
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: "strict",
        secure: ["securityLevel", "startOnLoad"],
      });
      initializedMermaid.add(mermaid);
    }
    await mermaid.run({ nodes, suppressErrors: true });
  } finally {
    for (const node of nodes) {
      node.dataset.mermaidRendered = "true";
    }
  }

  return nodes.length;
}

export function initMarkdownMermaidRendering(
  root: HTMLElement | Document = document,
  load: MarkdownMermaidLoader = loadMermaid,
): MarkdownMermaidController {
  let disconnected = false;
  let scheduled = false;

  const render = () => {
    if (disconnected || scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (disconnected) return;
      void renderMarkdownMermaidDiagrams(root, load).catch((error: unknown) => {
        console.error("Failed to render Mermaid diagrams in markdown", error);
      });
    });
  };

  const observer =
    typeof MutationObserver === "undefined"
      ? null
      : new MutationObserver(() => {
          render();
        });
  observer?.observe(root instanceof Document ? root.documentElement : root, {
    childList: true,
    subtree: true,
  });
  render();

  return {
    renderNow: render,
    disconnect() {
      disconnected = true;
      observer?.disconnect();
    },
  };
}
