import { Effect } from "effect";
import { executeGeneratedApiRequest, executeOpaqueGeneratedApiRequest } from "../../api/generated-api.js";
import { notifyWorkspaceDeleted } from "../../stores/workspace-host.svelte.js";
import { removeWorkspaceListEntry } from "../terminal/workspace-list-workflow.js";

export interface MobileDeletableWorkspace {
  readonly id: string;
  readonly fleet_host_key?: string | undefined;
  readonly platform_host: string;
  readonly repo_owner: string;
  readonly repo_name: string;
  readonly item_number: number;
  readonly item_type: string;
  readonly repo?: { readonly provider: string; readonly repo_path: string } | undefined;
}

// Deletes a local, Fleet, or devbox workspace and publishes the deletion so
// every surface (including an open phone terminal route) lets go of it.
export function deleteMobileWorkspace(workspace: MobileDeletableWorkspace, force: boolean) {
  const hostKey = workspace.fleet_host_key;
  const options = force ? { force: true } : undefined;
  const command = hostKey
    ? executeOpaqueGeneratedApiRequest<unknown>("delete mobile Fleet workspace", (client, signal) =>
        hostKey.startsWith("devbox:")
          ? client.DevboxesService.deleteDevboxWorkspace(
              { connectionId: hostKey.slice(7), id: workspace.id },
              options,
              { signal },
            )
          : client.FleetService.deleteFleetWorkspace({ hostKey, id: workspace.id }, options, { signal }),
      ).pipe(Effect.asVoid)
    : executeGeneratedApiRequest("delete mobile workspace", (client, signal) =>
        client.WorkspacesService.deleteWorkspace({ id: workspace.id }, options, { signal }),
      ).pipe(Effect.asVoid);
  return command.pipe(
    Effect.tap(() => removeWorkspaceListEntry(workspace.id, hostKey)),
    Effect.tap(() =>
      Effect.sync(() =>
        notifyWorkspaceDeleted(workspace.id, hostKey, {
          provider: workspace.repo?.provider ?? "",
          platformHost: workspace.platform_host,
          owner: workspace.repo_owner,
          name: workspace.repo_name,
          repoPath: workspace.repo?.repo_path ?? `${workspace.repo_owner}/${workspace.repo_name}`,
          number: workspace.item_number,
          itemType: workspace.item_type,
        }),
      ),
    ),
  );
}
