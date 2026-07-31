import { initMarkdownMermaidRendering } from "@kenn-io/kit-ui/utils/markdown-mermaid";
import { pushModalFrame } from "@kenn-forge/ui/stores/keyboard/modal-stack";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { prepareFrontendReload, retireFrontendReload } from "./lib/utils/frontendReloadGuard.js";
import { initMarkdownImageExpansion } from "./lib/utils/markdownImages.js";

const currentFrontendEntrypoint = new URL(import.meta.url);

function frontendEntrypoints(document: Document, baseUrl: string): URL[] {
  return Array.from(document.querySelectorAll<HTMLScriptElement>('script[type="module"][src]'), (script) => {
    return new URL(script.src, baseUrl);
  });
}

async function reloadIfFrontendChanged(): Promise<void> {
  try {
    const response = await window.fetch(window.location.href, {
      cache: "no-store",
      headers: { Accept: "text/html" },
    });
    if (!response.ok) return;

    const latestDocument = new DOMParser().parseFromString(await response.text(), "text/html");
    const latestEntrypoints = frontendEntrypoints(latestDocument, response.url);
    if (latestEntrypoints.some((entrypoint) => entrypoint.pathname === currentFrontendEntrypoint.pathname)) return;

    const latestEntrypoint = latestEntrypoints.at(-1);
    if (!latestEntrypoint) return;

    if (!prepareFrontendReload(window.sessionStorage, currentFrontendEntrypoint.pathname, latestEntrypoint.pathname))
      return;

    window.location.reload();
  } catch (error) {
    console.warn("Could not check for a frontend update", error);
  }
}

try {
  retireFrontendReload(window.sessionStorage, currentFrontendEntrypoint.pathname);
} catch (error) {
  console.warn("Could not clear completed frontend reload", error);
}

// A browser tab can outlive a server update and request a content-hashed lazy
// chunk that the new binary no longer embeds. Reload only when the server's
// current HTML points to a different frontend, preserving transient retries.
window.addEventListener("vite:preloadError", () => {
  void reloadIfFrontendChanged();
});

const target = document.getElementById("app");

if (!target) {
  throw new Error("Root element 'app' not found. Cannot mount application.");
}

mount(App, { target });
initMarkdownImageExpansion(target);
initMarkdownMermaidRendering(target, {
  onLightboxOpen: () => pushModalFrame("mermaid-lightbox", []),
});
