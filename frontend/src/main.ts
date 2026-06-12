import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { initMarkdownImageExpansion } from "./lib/utils/markdownImages.js";
import { initMarkdownMermaidRendering } from "./lib/utils/markdownMermaid.js";

const target = document.getElementById("app");

if (!target) {
  throw new Error("Root element 'app' not found. Cannot mount application.");
}

mount(App, { target });
initMarkdownImageExpansion(target);
initMarkdownMermaidRendering(target);
