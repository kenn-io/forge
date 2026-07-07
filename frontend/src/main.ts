import { initMarkdownMermaidRendering } from "@kenn-io/kit-ui/utils/markdown-mermaid";
import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { initMarkdownImageExpansion } from "./lib/utils/markdownImages.js";

const target = document.getElementById("app");

if (!target) {
  throw new Error("Root element 'app' not found. Cannot mount application.");
}

mount(App, { target });
initMarkdownImageExpansion(target);
initMarkdownMermaidRendering(target, {
  onLightboxOpen: () => pushModalFrame("mermaid-lightbox", []),
});
