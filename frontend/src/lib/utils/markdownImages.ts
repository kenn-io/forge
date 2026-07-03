import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";

export interface MarkdownImageExpansionController {
  renderNow: () => void;
  disconnect: () => void;
}

const IMAGE_SELECTOR = ".markdown-body img, .doc-markdown img, .diff-image-preview img";
const EXPANDER_CLASS = "markdown-image-expander";
const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  // kit-ui-check-ignore: imperative lightbox mounted into a caller-provided document; kit trapFocus binds the component's own document and body
  '[tabindex]:not([tabindex="-1"])',
].join(",");
let closeActiveMarkdownImageLightbox: (() => void) | null = null;

export function expandMarkdownImages(root: ParentNode): number {
  let enhanced = 0;

  for (const image of Array.from(root.querySelectorAll<HTMLImageElement>(IMAGE_SELECTOR))) {
    if (attachImageExpander(image)) {
      enhanced += 1;
    }
  }

  return enhanced;
}

function attachImageExpander(image: HTMLImageElement): boolean {
  if (!image.getAttribute("src")) return false;
  if (image.closest(`.${EXPANDER_CLASS}`) || image.closest(".markdown-image-lightbox")) return false;

  const target = expandableTargetFor(image);
  if (!target?.parentNode) return false;

  const wrapper = image.ownerDocument.createElement("span");
  wrapper.className = EXPANDER_CLASS;
  target.before(wrapper);

  const button = createImageExpanderButton(image.ownerDocument, () => openMarkdownImageLightbox(image));
  wrapper.append(target, button);
  return true;
}

function expandableTargetFor(image: HTMLImageElement): HTMLAnchorElement | HTMLImageElement | null {
  const parent = image.parentElement;
  const AnchorElement = image.ownerDocument.defaultView?.HTMLAnchorElement ?? HTMLAnchorElement;
  if (
    parent instanceof AnchorElement &&
    parent.querySelectorAll("img").length === 1 &&
    parent.textContent?.trim() === ""
  ) {
    return parent;
  }
  if (image.closest("a")) return null;
  return image;
}

function createImageExpanderButton(doc: Document, onClick: () => void): HTMLButtonElement {
  const button = doc.createElement("button");
  button.type = "button";
  button.className = "markdown-image-expander__button";
  button.setAttribute("aria-label", "Open image in expanded view");
  button.title = "Open image in expanded view";

  const icon = doc.createElement("span");
  icon.className = "markdown-image-expander__icon";
  icon.setAttribute("aria-hidden", "true");
  button.append(icon);

  button.addEventListener("click", (event) => {
    event.preventDefault();
    event.stopPropagation();
    onClick();
  });

  return button;
}

function openMarkdownImageLightbox(sourceImage: HTMLImageElement): void {
  closeActiveMarkdownImageLightbox?.();

  const doc = sourceImage.ownerDocument;
  const HTMLElementCtor = doc.defaultView?.HTMLElement ?? HTMLElement;
  const overlay = doc.createElement("div");
  overlay.className = "markdown-image-lightbox";
  overlay.setAttribute("aria-label", "Expanded image");
  overlay.setAttribute("aria-modal", "true");
  overlay.setAttribute("role", "dialog");
  overlay.tabIndex = -1;

  const panel = doc.createElement("div");
  panel.className = "markdown-image-lightbox__panel";

  const expanded = doc.createElement("img");
  const src = sourceImage.currentSrc || sourceImage.getAttribute("src") || sourceImage.src;
  if (src) expanded.setAttribute("src", src);
  expanded.setAttribute("alt", sourceImage.getAttribute("alt") ?? "");

  const closeButton = doc.createElement("button");
  closeButton.type = "button";
  closeButton.className = "markdown-image-lightbox__close";
  closeButton.setAttribute("aria-label", "Close expanded image");
  closeButton.title = "Close expanded image";
  closeButton.textContent = "x";
  closeButton.addEventListener("click", (event) => {
    event.preventDefault();
    event.stopPropagation();
    closeLightbox();
  });

  panel.append(expanded, closeButton);
  overlay.append(panel);

  const restoreFocusTo = doc.activeElement instanceof HTMLElementCtor ? doc.activeElement : null;
  const popModalFrame = pushModalFrame("markdown-image-lightbox", []);
  const onKeyDown = (event: KeyboardEvent) => {
    event.stopPropagation();
    if (event.key === "Escape") {
      event.preventDefault();
      closeLightbox();
      return;
    }
    if (event.key === "Tab") {
      trapLightboxFocus(event);
    }
  };

  overlay.addEventListener("click", (event) => {
    if (event.target === overlay) closeLightbox();
  });
  overlay.addEventListener("keydown", onKeyDown);
  doc.addEventListener("keydown", onKeyDown);
  doc.body.append(overlay);
  closeActiveMarkdownImageLightbox = closeLightbox;
  overlay.focus({ preventScroll: true });

  function closeLightbox(): void {
    overlay.removeEventListener("keydown", onKeyDown);
    doc.removeEventListener("keydown", onKeyDown);
    popModalFrame();
    overlay.remove();
    if (closeActiveMarkdownImageLightbox === closeLightbox) {
      closeActiveMarkdownImageLightbox = null;
    }
    restoreFocusTo?.focus({ preventScroll: true });
  }

  function trapLightboxFocus(event: KeyboardEvent): void {
    const focusableElements = Array.from(overlay.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
      (element) => !element.hidden,
    );
    event.preventDefault();

    if (focusableElements.length === 0) {
      overlay.focus({ preventScroll: true });
      return;
    }

    const activeElement = doc.activeElement instanceof HTMLElementCtor ? doc.activeElement : null;
    const currentIndex = activeElement ? focusableElements.indexOf(activeElement) : -1;
    const nextIndex = event.shiftKey
      ? currentIndex <= 0
        ? focusableElements.length - 1
        : currentIndex - 1
      : currentIndex >= focusableElements.length - 1
        ? 0
        : currentIndex + 1;
    focusableElements[nextIndex]?.focus({ preventScroll: true });
  }
}

export function initMarkdownImageExpansion(root: HTMLElement | Document = document): MarkdownImageExpansionController {
  let disconnected = false;
  let scheduled = false;

  const render = () => {
    if (disconnected || scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (!disconnected) expandMarkdownImages(root);
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
