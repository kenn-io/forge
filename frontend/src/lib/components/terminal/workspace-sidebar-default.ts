import type { Settings } from "../../api/types.js";
import type { WorkspaceDetail } from "./workspace-detail.js";

export type WorkspaceSidebarTab = "diff" | "pr" | "issue" | "reviews" | "kata";

export function defaultWorkspaceSidebarTab(
  preference: Settings["workspaces"]["default_sidebar_view"],
  itemType: WorkspaceDetail["item_type"],
): WorkspaceSidebarTab {
  if (preference !== "item") return "diff";
  if (itemType === "pull_request") return "pr";
  if (itemType === "issue") return "issue";
  return "diff";
}
