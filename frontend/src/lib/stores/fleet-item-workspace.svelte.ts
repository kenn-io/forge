import { Effect } from "effect";
import { untrack } from "svelte";
import { FleetSnapshotReads } from "../api/fleet-snapshot.js";
import { fleetWorkspaceIsForItem, type ItemWorkspaceRequest } from "../api/item-workspace.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import type { AppRuntime } from "../app/runtime.js";
import type { WorkspaceRefLite } from "../workspace-inline.js";
import { workspaceHostLabel } from "./workspace-target.svelte.js";

export type FleetItemWorkspaceItem = Pick<ItemWorkspaceRequest, "kind" | "repo" | "number">;

function itemKey(item: FleetItemWorkspaceItem): string {
  const { kind, repo, number } = item;
  return [
    kind,
    canonicalProvider(repo.provider),
    resolvedPlatformHost(repo.provider, repo.platformHost),
    repo.owner,
    repo.name,
    number,
  ].join("\u0000");
}

/**
 * Finds the workspace another machine in the fleet runs for a pull request or
 * issue. An item's detail lists only this Forge's workspaces, so without this
 * the item opened anywhere but where its workspace was created offers to
 * create a second one.
 *
 * getItem returns the item to look up, or null when there is nothing to find,
 * such as while the detail has not loaded or already names a local workspace.
 * getRevision changes with each detail load, so a workspace created or
 * deleted elsewhere since is picked up.
 */
export function createFleetItemWorkspace(
  runtime: AppRuntime,
  getItem: () => FleetItemWorkspaceItem | null,
  getRevision: () => number,
) {
  let found = $state.raw<{ key: string; ref: WorkspaceRefLite | null } | null>(null);
  $effect(() => {
    const item = getItem();
    getRevision();
    if (!item) return;
    const key = itemKey(item);
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.gen(function* () {
          const snapshot = yield* (yield* FleetSnapshotReads).load;
          const remote = (snapshot.workspaces ?? []).find(
            (ws) => ws.fleet_host_key !== undefined && fleetWorkspaceIsForItem(ws, item),
          );
          const host =
            remote && (snapshot.hosts ?? []).find((candidate) => candidate.configKey === remote.fleet_host_key);
          yield* Effect.sync(() => {
            found = {
              key,
              ref: remote?.fleet_host_key
                ? {
                    id: remote.id,
                    status: remote.status,
                    hostKey: remote.fleet_host_key,
                    hostLabel: host ? workspaceHostLabel(host) : remote.fleet_host_name || undefined,
                  }
                : null,
            };
          });
        }),
        {
          operation: "find item workspace on another machine",
          safeContext: { kind: item.kind, number: item.number },
          // The item then offers Create Workspace, which reuses the remote
          // workspace when its machine reports it already exists.
          onFailure: (failure) => console.warn("Could not look for this item's workspace on other machines", failure),
        },
      ),
    );
    return () => execution.interrupt();
  });
  return {
    /** The remote workspace for item, or null when none is known. */
    ref(item: FleetItemWorkspaceItem | null): WorkspaceRefLite | null {
      if (!item || found?.key !== itemKey(item)) return null;
      return found.ref;
    },
  };
}
