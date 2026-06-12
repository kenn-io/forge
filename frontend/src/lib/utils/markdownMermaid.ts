import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";

export interface MarkdownMermaidAPI {
  initialize: (config: MarkdownMermaidConfig) => void;
  run: (config: { nodes: ArrayLike<HTMLElement>; suppressErrors: true }) => Promise<unknown>;
}

export type MarkdownMermaidLoader = () => Promise<MarkdownMermaidAPI>;

type MermaidThemeVariables = Record<string, boolean | string>;
type MermaidThemeName = "light" | "dark";

interface MarkdownMermaidConfig {
  startOnLoad: false;
  securityLevel: "strict";
  secure: string[];
  maxTextSize: number;
  maxEdges: number;
  suppressErrorRendering: true;
  dompurifyConfig: {
    FORBID_ATTR: string[];
    FORBID_TAGS: string[];
  };
  htmlLabels: false;
  themeCSS: "";
  fontFamily: string;
  altFontFamily: string;
  theme: "base";
  themeVariables: MermaidThemeVariables;
}

export interface MarkdownMermaidController {
  renderNow: () => void;
  disconnect: () => void;
}

const MERMAID_SELECTOR = ".markdown-body pre.mermaid, .doc-markdown pre.mermaid";
const MERMAID_VIEWER_SELECTOR = ".markdown-body pre.mermaid.mermaid-viewer, .doc-markdown pre.mermaid.mermaid-viewer";
const MERMAID_VIEWER_ATTACHED = "true";
const MIN_SCALE = 0.4;
const MAX_SCALE = 3;
const PAN_STEP = 80;
const WHEEL_ZOOM_SENSITIVITY = 0.0015;
const WHEEL_DELTA_LINE = 1;
const WHEEL_DELTA_PAGE = 2;
const MAX_MERMAID_DIAGRAMS_PER_DOCUMENT = 25;
const MAX_MERMAID_SOURCE_BYTES_PER_DOCUMENT = 200_000;
const MERMAID_MAX_TEXT_SIZE = 50_000;
const MERMAID_MAX_EDGES = 500;
const MERMAID_SECURE_CONFIG = [
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
];
const MERMAID_THEME_TOKENS = {
  background: "--mermaid-bg",
  fontFamily: "--font-sans",
  primaryColor: "--mermaid-node-bg",
  primaryTextColor: "--mermaid-node-text",
  primaryBorderColor: "--mermaid-node-border",
  secondaryColor: "--mermaid-node-bg",
  secondaryTextColor: "--mermaid-node-text",
  secondaryBorderColor: "--mermaid-node-border",
  tertiaryColor: "--mermaid-cluster-bg",
  tertiaryTextColor: "--mermaid-cluster-text",
  tertiaryBorderColor: "--mermaid-cluster-border",
  mainBkg: "--mermaid-node-bg",
  nodeTextColor: "--mermaid-node-text",
  nodeBorder: "--mermaid-node-border",
  clusterBkg: "--mermaid-cluster-bg",
  clusterBorder: "--mermaid-cluster-border",
  lineColor: "--mermaid-line",
  defaultLinkColor: "--mermaid-line",
  textColor: "--mermaid-text",
  titleColor: "--mermaid-text",
  edgeLabelBackground: "--mermaid-label-bg",
  labelColor: "--mermaid-text",
  labelTextColor: "--mermaid-label-text",
  loopTextColor: "--mermaid-text",
  noteBkgColor: "--mermaid-note-bg",
  noteTextColor: "--mermaid-note-text",
  noteBorderColor: "--mermaid-note-border",
  actorBkg: "--mermaid-node-bg",
  actorBorder: "--mermaid-node-border",
  actorTextColor: "--mermaid-node-text",
  actorLineColor: "--mermaid-line",
  signalColor: "--mermaid-line",
  signalTextColor: "--mermaid-text",
  labelBoxBkgColor: "--mermaid-label-bg",
  labelBoxBorderColor: "--mermaid-node-border",
} satisfies Record<string, string>;
let mermaidPromise: Promise<MarkdownMermaidAPI> | null = null;
const initializedMermaidTheme = new WeakMap<MarkdownMermaidAPI, MermaidThemeName>();
const diagramSources = new WeakMap<HTMLElement, string>();
const failedDiagramSources = new WeakMap<HTMLElement, string>();
let closeActiveMermaidLightbox: (() => void) | null = null;

async function loadMermaid(): Promise<MarkdownMermaidAPI> {
  if (!mermaidPromise) {
    mermaidPromise = import("mermaid")
      .then((module): MarkdownMermaidAPI => module.default)
      .catch((error: unknown) => {
        mermaidPromise = null;
        throw error;
      });
  }
  return mermaidPromise;
}

export async function renderMarkdownMermaidDiagrams(
  root: ParentNode,
  load: MarkdownMermaidLoader = loadMermaid,
): Promise<number> {
  const nodes = collectRenderableMermaidNodes(root);
  if (nodes.length === 0) return 0;

  for (const node of nodes) {
    node.dataset.mermaidRendered = "pending";
    diagramSources.set(node, node.textContent ?? "");
  }

  try {
    const mermaid = await load();
    initializeMermaidForCurrentTheme(mermaid);
    await mermaid.run({ nodes, suppressErrors: true });
    let renderedCount = 0;
    for (const node of nodes) {
      if (attachMermaidViewer(node, diagramSources.get(node) ?? "")) {
        failedDiagramSources.delete(node);
        node.dataset.mermaidRendered = "true";
        renderedCount += 1;
      } else {
        restoreMermaidSource(node);
        markMermaidRenderFailed(node);
      }
    }
    return renderedCount;
  } catch (error: unknown) {
    for (const node of nodes) {
      restoreMermaidSource(node);
      markMermaidRenderFailed(node);
    }
    throw error;
  }
}

function collectRenderableMermaidNodes(root: ParentNode): HTMLElement[] {
  const nodes: HTMLElement[] = [];
  let diagramCount = 0;
  let sourceBytes = 0;

  for (const node of Array.from(root.querySelectorAll<HTMLElement>(MERMAID_SELECTOR))) {
    const source = mermaidNodeSource(node);
    const nextSourceBytes = mermaidSourceByteLength(source);
    if (node.dataset.mermaidRendered === "failed") {
      const failedSource = failedDiagramSources.get(node);
      if (failedSource === undefined || failedSource === source) {
        diagramCount += 1;
        sourceBytes += nextSourceBytes;
        continue;
      }
      failedDiagramSources.delete(node);
      clearMermaidRenderState(node);
    }

    if (node.dataset.mermaidRendered || node.dataset.processed === "true") {
      diagramCount += 1;
      sourceBytes += nextSourceBytes;
      continue;
    }

    if (
      diagramCount >= MAX_MERMAID_DIAGRAMS_PER_DOCUMENT ||
      sourceBytes + nextSourceBytes > MAX_MERMAID_SOURCE_BYTES_PER_DOCUMENT
    ) {
      skipMermaidRender(node);
      continue;
    }

    diagramCount += 1;
    sourceBytes += nextSourceBytes;
    nodes.push(node);
  }

  return nodes;
}

function mermaidNodeSource(node: HTMLElement): string {
  if (node.dataset.mermaidRendered === "failed") {
    return node.textContent ?? "";
  }
  return diagramSources.get(node) ?? node.textContent ?? "";
}

function mermaidSourceByteLength(source: string): number {
  return new TextEncoder().encode(source).byteLength;
}

function skipMermaidRender(node: HTMLElement): void {
  node.classList.remove("mermaid");
  node.dataset.mermaidRendered = "skipped";
  delete node.dataset.processed;
}

function attachMermaidViewer(node: HTMLElement, source: string): boolean {
  if (node.dataset.mermaidViewer === MERMAID_VIEWER_ATTACHED) return true;

  const svg = node.querySelector("svg");
  if (!svg) return false;

  svg.remove();
  const diagramView = createPannableDiagramView(svg);
  const expandButton = createMermaidButton("Open diagram in expanded view", "⟷", () => openMermaidLightbox(svg));
  const copyButton = createMermaidButton("Copy Mermaid source", "⧉", () => copyMermaidSource(source, copyButton));
  const topControls = document.createElement("div");
  topControls.className = "mermaid-viewer__controls mermaid-viewer__controls--top";
  topControls.append(expandButton, copyButton);

  node.textContent = "";
  node.classList.add("mermaid-viewer");
  node.dataset.mermaidViewer = MERMAID_VIEWER_ATTACHED;
  node.append(diagramView.viewport, topControls, diagramView.controls);
  return true;
}

function clearMermaidRenderState(node: HTMLElement): void {
  delete node.dataset.mermaidRendered;
  delete node.dataset.processed;
}

function markMermaidRenderFailed(node: HTMLElement): void {
  const source = diagramSources.get(node) ?? node.textContent ?? "";
  failedDiagramSources.set(node, source);
  node.dataset.mermaidRendered = "failed";
  delete node.dataset.processed;
}

function restoreMermaidSource(node: HTMLElement): void {
  const source = diagramSources.get(node);
  if (source !== undefined) {
    node.textContent = source;
  }
}

function initializeMermaidForCurrentTheme(mermaid: MarkdownMermaidAPI): void {
  const theme = currentMermaidTheme();
  if (initializedMermaidTheme.get(mermaid) === theme) return;
  const themeVariables = mermaidThemeVariables(theme);
  const fontFamily = String(themeVariables.fontFamily);

  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    secure: [...MERMAID_SECURE_CONFIG],
    maxTextSize: MERMAID_MAX_TEXT_SIZE,
    maxEdges: MERMAID_MAX_EDGES,
    suppressErrorRendering: true,
    dompurifyConfig: {
      FORBID_ATTR: ["style"],
      FORBID_TAGS: ["style"],
    },
    htmlLabels: false,
    themeCSS: "",
    fontFamily,
    altFontFamily: fontFamily,
    theme: "base",
    themeVariables,
  });
  initializedMermaidTheme.set(mermaid, theme);
}

function currentMermaidTheme(): MermaidThemeName {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

function mermaidThemeVariables(theme: MermaidThemeName): MermaidThemeVariables {
  const styles = getComputedStyle(document.documentElement);
  const variables: MermaidThemeVariables = {
    darkMode: theme === "dark",
    fontSize: cssThemeToken(styles, "--font-size-md"),
  };

  for (const [mermaidName, cssName] of Object.entries(MERMAID_THEME_TOKENS)) {
    variables[mermaidName] = cssThemeToken(styles, cssName);
  }

  return variables;
}

function cssThemeToken(styles: CSSStyleDeclaration, name: string): string {
  const value = styles.getPropertyValue(name).trim();
  if (!value) {
    throw new Error(`Missing Mermaid theme CSS variable ${name}`);
  }
  return value;
}

function resetRenderedMermaidViewers(root: ParentNode): void {
  closeActiveMermaidLightbox?.();

  for (const node of Array.from(root.querySelectorAll<HTMLElement>(MERMAID_VIEWER_SELECTOR))) {
    const source = diagramSources.get(node);
    if (source === undefined) continue;

    node.textContent = source;
    node.classList.remove("mermaid-viewer");
    delete node.dataset.mermaidViewer;
    clearMermaidRenderState(node);
  }
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

  const zoomTo = (nextScale: number, originX: number, originY: number) => {
    nextScale = clampScale(nextScale);
    if (nextScale === scale) return;
    const scaleRatio = nextScale / scale;
    offsetX = originX - (originX - offsetX) * scaleRatio;
    offsetY = originY - (originY - offsetY) * scaleRatio;
    scale = nextScale;
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
  });

  attachDragPanning(viewport, {
    onDrag(deltaX, deltaY) {
      offsetX += deltaX;
      offsetY += deltaY;
      updateTransform();
    },
  });
  attachWheelZoom(viewport, {
    onZoom(event) {
      const rect = viewport.getBoundingClientRect();
      zoomTo(
        scale * Math.exp(-normalizeWheelDelta(event, viewport) * WHEEL_ZOOM_SENSITIVITY),
        event.clientX - rect.left - rect.width / 2,
        event.clientY - rect.top - rect.height / 2,
      );
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
}): HTMLDivElement {
  const navControls = document.createElement("div");
  navControls.className = "mermaid-viewer__controls mermaid-viewer__controls--nav";
  navControls.append(
    createMermaidSpacer(),
    createMermaidButton("Pan diagram up", "↑", () => actions.panBy(0, -PAN_STEP)),
    createMermaidSpacer(),
    createMermaidButton("Pan diagram left", "←", () => actions.panBy(-PAN_STEP, 0)),
    createMermaidButton("Reset diagram view", "⟳", actions.resetView),
    createMermaidButton("Pan diagram right", "→", () => actions.panBy(PAN_STEP, 0)),
    createMermaidSpacer(),
    createMermaidButton("Pan diagram down", "↓", () => actions.panBy(0, PAN_STEP)),
    createMermaidSpacer(),
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
  const popModalFrame = pushModalFrame("mermaid-lightbox", []);
  const onKeyDown = (event: KeyboardEvent) => {
    event.stopPropagation();
    if (event.key === "Escape") {
      event.preventDefault();
      closeLightbox();
    }
  };

  overlay.addEventListener("click", (event) => {
    if (event.target === overlay) closeLightbox();
  });
  overlay.addEventListener("keydown", onKeyDown);

  document.addEventListener("keydown", onKeyDown);
  document.body.append(overlay);
  closeActiveMermaidLightbox = closeLightbox;
  closeButton.focus({ preventScroll: true });

  function closeLightbox(): void {
    overlay.removeEventListener("keydown", onKeyDown);
    document.removeEventListener("keydown", onKeyDown);
    popModalFrame();
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

function attachWheelZoom(viewport: HTMLElement, zoom: { onZoom: (event: WheelEvent) => void }): void {
  viewport.addEventListener(
    "wheel",
    (event) => {
      event.preventDefault();
      event.stopPropagation();
      zoom.onZoom(event);
    },
    { passive: false },
  );
}

function normalizeWheelDelta(event: WheelEvent, viewport: HTMLElement): number {
  if (event.deltaMode === WHEEL_DELTA_LINE) return event.deltaY * 16;
  if (event.deltaMode === WHEEL_DELTA_PAGE) return event.deltaY * (viewport.clientHeight || window.innerHeight || 800);
  return event.deltaY;
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
  let rendering = false;
  let renderAfterCurrent = false;
  let themeResetAfterCurrent = false;
  let renderedTheme = currentMermaidTheme();

  const render = () => {
    if (disconnected) return;
    if (rendering) {
      renderAfterCurrent = true;
      return;
    }
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(async () => {
      scheduled = false;
      if (disconnected) return;
      rendering = true;
      const themeAtStart = currentMermaidTheme();
      try {
        await renderMarkdownMermaidDiagrams(root, load);
      } catch (error: unknown) {
        console.error("Failed to render Mermaid diagrams in markdown", error);
      } finally {
        rendering = false;
      }
      if (disconnected) return;

      const themeAtEnd = currentMermaidTheme();
      if (themeResetAfterCurrent || themeAtEnd !== themeAtStart) {
        themeResetAfterCurrent = false;
        renderAfterCurrent = false;
        renderedTheme = themeAtEnd;
        resetRenderedMermaidViewers(root);
        render();
        return;
      }

      renderedTheme = themeAtEnd;
      if (renderAfterCurrent) {
        renderAfterCurrent = false;
        render();
      }
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

  const themeObserver =
    typeof MutationObserver === "undefined"
      ? null
      : new MutationObserver(() => {
          const nextTheme = currentMermaidTheme();
          if (nextTheme === renderedTheme) return;
          renderedTheme = nextTheme;
          if (rendering) {
            themeResetAfterCurrent = true;
            return;
          }
          resetRenderedMermaidViewers(root);
          render();
        });
  themeObserver?.observe(document.documentElement, {
    attributeFilter: ["class"],
    attributes: true,
  });
  render();

  return {
    renderNow: render,
    disconnect() {
      disconnected = true;
      observer?.disconnect();
      themeObserver?.disconnect();
    },
  };
}
