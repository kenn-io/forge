import { initMarkdownMermaidRendering } from "@kenn-io/kit-ui/utils/markdown-mermaid";
import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { initMarkdownImageExpansion } from "./lib/utils/markdownImages.js";

const viteProbeParam = "_middleman_vite_probe";
const viteReloadKeyPrefix = "middleman:vite-reload:";

function viteAssetUrl(payload: unknown): URL | null {
  if (!(payload instanceof Error)) return null;
  const match = payload.message.match(/https?:\/\/[^\s'")]+/);
  if (!match) return null;

  const url = new URL(match[0]);
  if (url.origin !== window.location.origin || !url.pathname.startsWith("/assets/")) return null;
  return url;
}

async function reloadIfViteAssetIsMissing(payload: unknown): Promise<void> {
  const assetUrl = viteAssetUrl(payload);
  if (!assetUrl) return;

  const reloadKey = `${viteReloadKeyPrefix}${assetUrl.pathname}`;
  try {
    if (window.sessionStorage.getItem(reloadKey)) return;

    assetUrl.searchParams.set(viteProbeParam, Date.now().toString());
    const response = await window.fetch(assetUrl, { method: "HEAD", cache: "no-store" });
    if (response.status !== 404 && response.status !== 410) return;

    window.sessionStorage.setItem(reloadKey, "1");
    window.location.reload();
  } catch (error) {
    console.warn("Could not verify failed frontend asset", error);
  }
}

// A browser tab can outlive a server update and request a content-hashed lazy
// chunk that the new binary no longer embeds. Vite emits the same event for
// transient failures, so confirm that the asset is gone before reloading.
window.addEventListener("vite:preloadError", (event) => {
  void reloadIfViteAssetIsMissing((event as Event & { payload?: unknown }).payload);
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
