import { Effect } from "effect";
import {
  executeGeneratedApiRequest,
  executeOpaqueGeneratedApiRequest,
  type GeneratedApi,
} from "../../api/generated-api.js";
import { decodeWorkspaceDetail, type WorkspaceDetail } from "../terminal/workspace-detail.js";

export function loadMobileWorkspaceDetail(
  workspaceId: string,
  hostKey?: string,
): Effect.Effect<WorkspaceDetail, unknown, GeneratedApi> {
  if (hostKey) {
    return executeOpaqueGeneratedApiRequest("load mobile Fleet workspace", (client, signal) =>
      client.GET("/fleet/hosts/{host_key}/workspaces/{id}", {
        params: { path: { host_key: hostKey, id: workspaceId } },
        signal,
      }),
    ).pipe(Effect.flatMap((payload) => decodeWorkspaceDetail(payload, hostKey)));
  }
  return executeGeneratedApiRequest("load mobile workspace", (client, signal) =>
    client.GET("/workspaces/{id}", {
      params: { path: { id: workspaceId } },
      signal,
    }),
  ).pipe(Effect.flatMap((payload) => decodeWorkspaceDetail(payload)));
}

export function mobileWorkspaceLinkedItem(workspace: WorkspaceDetail): {
  itemType: "pr" | "issue";
  number: number;
} | null {
  if (workspace.item_type === "kata_task") return null;
  if (workspace.item_type === "issue") {
    return workspace.item_number > 0 ? { itemType: "issue", number: workspace.item_number } : null;
  }
  if (workspace.item_type === "pull_request") {
    return workspace.item_number > 0 ? { itemType: "pr", number: workspace.item_number } : null;
  }
  const number = workspace.associated_pr_number;
  return number !== null && number !== undefined && number > 0 ? { itemType: "pr", number } : null;
}

export function mobileWorkspaceIdentity(workspace: WorkspaceDetail, hostKey?: string): string {
  const local = `${workspace.repo_owner}/${workspace.repo_name} · ${workspace.git_head_ref}`;
  return hostKey ? `${local} · ${hostKey}` : local;
}
