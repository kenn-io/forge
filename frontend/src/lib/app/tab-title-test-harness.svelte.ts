import { useWorkspaceTab } from "./tab-title.svelte.js";

export function mountWorkspaceTitles() {
  const terminal = $effect.root(() => {
    useWorkspaceTab(() => ({ page: "mobile-workspace-terminal", hostKey: "remote", id: "a", name: "Terminal" }));
  });
  const item = $effect.root(() => {
    useWorkspaceTab(() => ({ page: "mobile-workspace-item", hostKey: "remote", id: "a", name: "Item" }));
  });
  return { terminal, item };
}
