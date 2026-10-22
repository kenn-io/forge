/**
 * Reports whether this page may offer kenn:// links. An app that embeds
 * Forge and opens those links sets the flag before page scripts run; a
 * plain browser tab never offers them.
 */
export function kennLinksEnabled(): boolean {
  return window.__KENN_EMBEDDED_KENN_LINKS__ === true;
}
