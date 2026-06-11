import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { initMarkdownMermaidRendering } from "./lib/utils/markdownMermaid.js";

const target = document.getElementById("app");

if (!target) {
  throw new Error("Root element 'app' not found. Cannot mount application.");
}

mount(App, { target });
initMarkdownMermaidRendering(target);
