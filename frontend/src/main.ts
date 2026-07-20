import { initMarkdownMermaidRendering } from "@kenn-io/kit-ui/utils/markdown-mermaid";
import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { initMarkdownImageExpansion } from "./lib/utils/markdownImages.js";

// A browser tab can outlive a server update and request a content-hashed
// lazy chunk that the new binary no longer embeds. Reload the uncached SPA
// entrypoint so the tab binds to the current asset graph.
window.addEventListener("vite:preloadError", (event) => {
  event.preventDefault();
  window.location.reload();
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
