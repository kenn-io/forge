import { isWorkspaceEmbedPage, type Page } from "../stores/router.svelte.js";

export function shouldUseFullAppShell(page: Page): boolean {
  return !isWorkspaceEmbedPage(page);
}
