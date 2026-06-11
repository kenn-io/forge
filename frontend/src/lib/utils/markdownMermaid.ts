export interface MarkdownMermaidAPI {
  initialize: (config: MarkdownMermaidConfig) => void;
  run: (config: { nodes: ArrayLike<HTMLElement>; suppressErrors: true }) => Promise<unknown>;
}

export type MarkdownMermaidLoader = () => Promise<MarkdownMermaidAPI>;

type MermaidThemeVariables = Record<string, boolean | string>;

interface MarkdownMermaidConfig {
  startOnLoad: false;
  securityLevel: "strict";
  secure: string[];
  theme: "base";
  themeVariables: MermaidThemeVariables;
}

export interface MarkdownMermaidController {
  renderNow: () => void;
  disconnect: () => void;
}

const MERMAID_SELECTOR = ".markdown-body pre.mermaid, .doc-markdown pre.mermaid";
const MERMAID_VIEWER_ATTACHED = "true";
const MIN_SCALE = 0.4;
const MAX_SCALE = 3;
const ZOOM_STEP = 0.2;
const PAN_STEP = 80;
const GITHUB_DARK_MERMAID_THEME: MermaidThemeVariables = {
  darkMode: true,
  background: "#0d1117",
  fontFamily: "Inter, -apple-system, BlinkMacSystemFont, Segoe UI, Helvetica, Arial, sans-serif",
  fontSize: "13px",
  primaryColor: "#f6f8fa",
  primaryTextColor: "#24292f",
  primaryBorderColor: "#d0d7de",
  secondaryColor: "#f6f8fa",
  secondaryTextColor: "#24292f",
  secondaryBorderColor: "#d0d7de",
  tertiaryColor: "#4a4d4b",
  tertiaryTextColor: "#f0f6fc",
  tertiaryBorderColor: "#4a4d4b",
  mainBkg: "#f6f8fa",
  nodeTextColor: "#24292f",
  nodeBorder: "#d0d7de",
  clusterBkg: "#4a4d4b",
  clusterBorder: "#4a4d4b",
  lineColor: "#c9d1d9",
  defaultLinkColor: "#c9d1d9",
  textColor: "#c9d1d9",
  titleColor: "#c9d1d9",
  edgeLabelBackground: "#30363d",
  labelColor: "#c9d1d9",
  labelTextColor: "#24292f",
  noteBkgColor: "#30363d",
  noteTextColor: "#f0f6fc",
  noteBorderColor: "#8b949e",
  actorBkg: "#f6f8fa",
  actorBorder: "#d0d7de",
  actorTextColor: "#24292f",
  actorLineColor: "#8b949e",
  signalColor: "#c9d1d9",
  signalTextColor: "#c9d1d9",
  labelBoxBkgColor: "#30363d",
  labelBoxBorderColor: "#8b949e",
};
let mermaidPromise: Promise<MarkdownMermaidAPI> | null = null;
const initializedMermaid = new WeakSet<MarkdownMermaidAPI>();
const diagramSources = new WeakMap<HTMLElement, string>();
let closeActiveMermaidLightbox: (() => void) | null = null;

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
    (node) => !node.dataset.mermaidRendered && node.dataset.processed !== "true",
  );
  if (nodes.length === 0) return 0;

  for (const node of nodes) {
    node.dataset.mermaidRendered = "pending";
    diagramSources.set(node, node.textContent ?? "");
  }

  try {
    const mermaid = await load();
    if (!initializedMermaid.has(mermaid)) {
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: "strict",
        secure: ["securityLevel", "startOnLoad"],
        theme: "base",
        themeVariables: GITHUB_DARK_MERMAID_THEME,
      });
      initializedMermaid.add(mermaid);
    }
    await mermaid.run({ nodes, suppressErrors: true });
    for (const node of nodes) {
      attachMermaidViewer(node, diagramSources.get(node) ?? "");
    }
  } finally {
    for (const node of nodes) {
      node.dataset.mermaidRendered = "true";
    }
  }

  return nodes.length;
}

function attachMermaidViewer(node: HTMLElement, source: string): void {
  if (node.dataset.mermaidViewer === MERMAID_VIEWER_ATTACHED) return;

  const svg = node.querySelector("svg");
  if (!svg) return;

  svg.remove();
  const diagramView = createPannableDiagramView(svg);
  const expandButton = createMermaidButton("Open diagram in expanded view", "↔", () => openMermaidLightbox(svg));
  const copyButton = createMermaidButton("Copy Mermaid source", "⧉", () => copyMermaidSource(source, copyButton));
  const topControls = document.createElement("div");
  topControls.className = "mermaid-viewer__controls mermaid-viewer__controls--top";
  topControls.append(expandButton, copyButton);

  node.textContent = "";
  node.classList.add("mermaid-viewer");
  node.dataset.mermaidViewer = MERMAID_VIEWER_ATTACHED;
  node.append(diagramView.viewport, topControls, diagramView.controls);
}

function createPannableDiagramView(svg: SVGSVGElement): { controls: HTMLDivElement; viewport: HTMLDivElement } {
  let scale = 1;
  let offsetX = 0;
  let offsetY = 0;

  const viewport = document.createElement("div");
  viewport.className = "mermaid-viewer__viewport";

  const pan = document.createElement("div");
  pan.className = "mermaid-viewer__pan";

  const updateTransform = () => {
    pan.style.transform = `translate(${offsetX}px, ${offsetY}px) scale(${formatScale(scale)})`;
  };

  const resetView = () => {
    scale = 1;
    offsetX = 0;
    offsetY = 0;
    updateTransform();
  };

  const zoomBy = (delta: number) => {
    scale = clampScale(scale + delta);
    updateTransform();
  };

  const panBy = (deltaX: number, deltaY: number) => {
    offsetX += deltaX;
    offsetY += deltaY;
    updateTransform();
  };

  const controls = createMermaidNavControls({
    panBy,
    resetView,
    zoomBy,
  });

  attachDragPanning(viewport, {
    onDrag(deltaX, deltaY) {
      offsetX += deltaX;
      offsetY += deltaY;
      updateTransform();
    },
  });

  pan.append(svg);
  viewport.append(pan);
  updateTransform();

  return { controls, viewport };
}

function createMermaidNavControls(actions: {
  panBy: (deltaX: number, deltaY: number) => void;
  resetView: () => void;
  zoomBy: (delta: number) => void;
}): HTMLDivElement {
  const navControls = document.createElement("div");
  navControls.className = "mermaid-viewer__controls mermaid-viewer__controls--nav";
  navControls.append(
    createMermaidSpacer(),
    createMermaidButton("Pan diagram up", "↑", () => actions.panBy(0, -PAN_STEP)),
    createMermaidButton("Zoom in diagram", "+", () => actions.zoomBy(ZOOM_STEP)),
    createMermaidButton("Pan diagram left", "←", () => actions.panBy(-PAN_STEP, 0)),
    createMermaidButton("Reset diagram view", "⟳", actions.resetView),
    createMermaidButton("Pan diagram right", "→", () => actions.panBy(PAN_STEP, 0)),
    createMermaidSpacer(),
    createMermaidButton("Pan diagram down", "↓", () => actions.panBy(0, PAN_STEP)),
    createMermaidButton("Zoom out diagram", "-", () => actions.zoomBy(-ZOOM_STEP)),
  );
  return navControls;
}

function openMermaidLightbox(svg: SVGSVGElement): void {
  closeActiveMermaidLightbox?.();

  const overlay = document.createElement("div");
  overlay.className = "mermaid-viewer-lightbox";
  overlay.setAttribute("aria-label", "Expanded Mermaid diagram");
  overlay.setAttribute("aria-modal", "true");
  overlay.setAttribute("role", "dialog");

  const panel = document.createElement("div");
  panel.className = "mermaid-viewer-lightbox__panel";

  const closeButton = createMermaidButton("Close expanded diagram", "×", closeLightbox);
  closeButton.classList.add("mermaid-viewer-lightbox__close");

  const expandedSvg = svg.cloneNode(true) as SVGSVGElement;
  const diagramView = createPannableDiagramView(expandedSvg);
  panel.append(diagramView.viewport, closeButton, diagramView.controls);
  overlay.append(panel);

  const restoreFocusTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.preventDefault();
      closeLightbox();
    }
  };

  overlay.addEventListener("click", (event) => {
    if (event.target === overlay) closeLightbox();
  });

  document.addEventListener("keydown", onKeyDown);
  document.body.append(overlay);
  closeActiveMermaidLightbox = closeLightbox;
  closeButton.focus({ preventScroll: true });

  function closeLightbox(): void {
    document.removeEventListener("keydown", onKeyDown);
    overlay.remove();
    if (closeActiveMermaidLightbox === closeLightbox) {
      closeActiveMermaidLightbox = null;
    }
    restoreFocusTo?.focus({ preventScroll: true });
  }
}

function createMermaidButton(label: string, text: string, onClick: () => void | Promise<void>): HTMLButtonElement {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "mermaid-viewer__button";
  button.setAttribute("aria-label", label);
  button.title = label;
  button.textContent = text;
  button.addEventListener("click", (event) => {
    event.preventDefault();
    event.stopPropagation();
    void onClick();
  });
  return button;
}

function createMermaidSpacer(): HTMLSpanElement {
  const spacer = document.createElement("span");
  spacer.className = "mermaid-viewer__spacer";
  spacer.setAttribute("aria-hidden", "true");
  return spacer;
}

async function copyMermaidSource(source: string, button: HTMLButtonElement): Promise<void> {
  if (!source || typeof navigator === "undefined" || !navigator.clipboard?.writeText) return;

  try {
    await navigator.clipboard.writeText(source);
    button.dataset.copied = "true";
    button.setAttribute("aria-label", "Copied Mermaid source");
    button.title = "Copied Mermaid source";
    window.setTimeout(() => {
      button.dataset.copied = "false";
      button.setAttribute("aria-label", "Copy Mermaid source");
      button.title = "Copy Mermaid source";
    }, 1200);
  } catch (error: unknown) {
    console.error("Failed to copy Mermaid source", error);
  }
}

function attachDragPanning(viewport: HTMLElement, drag: { onDrag: (deltaX: number, deltaY: number) => void }): void {
  let activeDrag: { pointerId: number; x: number; y: number } | null = null;

  viewport.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    activeDrag = { pointerId: event.pointerId, x: event.clientX, y: event.clientY };
    viewport.classList.add("mermaid-viewer__viewport--dragging");
    if ("setPointerCapture" in viewport) {
      viewport.setPointerCapture(event.pointerId);
    }
  });

  viewport.addEventListener("pointermove", (event) => {
    if (!activeDrag || activeDrag.pointerId !== event.pointerId) return;
    drag.onDrag(event.clientX - activeDrag.x, event.clientY - activeDrag.y);
    activeDrag = { pointerId: event.pointerId, x: event.clientX, y: event.clientY };
  });

  const endDrag = (event: PointerEvent) => {
    if (!activeDrag || activeDrag.pointerId !== event.pointerId) return;
    activeDrag = null;
    viewport.classList.remove("mermaid-viewer__viewport--dragging");
    if ("releasePointerCapture" in viewport) {
      viewport.releasePointerCapture(event.pointerId);
    }
  };

  viewport.addEventListener("pointerup", endDrag);
  viewport.addEventListener("pointercancel", endDrag);
}

function clampScale(value: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, Number(value.toFixed(2))));
}

function formatScale(value: number): string {
  return Number(value.toFixed(2)).toString();
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
