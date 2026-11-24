import { Cause, Effect, Exit } from "effect";
import { untrack } from "svelte";
import { FleetSnapshotReads, type FleetWorkspaceSummary } from "../api/fleet-snapshot.js";
import { fleetWorkspaceIsForItem, type ItemWorkspaceRequest } from "../api/item-workspace.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import type { AppRuntime } from "../app/runtime.js";
import type { WorkspaceItemIdentity } from "../workspace-inline.js";
import {
  discoveredWorkspaceRef,
  recordDiscoveredWorkspace,
  remoteWorkspaceGone,
} from "./workspace-create-pending.svelte.js";
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

function deleting(workspace: FleetWorkspaceSummary): boolean {
  return workspace.status === "deleting" || workspace.status === "deletion_failed";
}

/**
 * Looks for the workspace another machine in the fleet runs for a pull request
 * or issue, and records it as the item's discovered workspace. An item's detail
 * lists only this Forge's workspaces, so without this the item opened anywhere
 * but where its workspace was created offers to create a second one.
 *
 * getItem returns the item to look up, or null when there is nothing to find,
 * such as while the detail has not loaded or already names a local workspace.
 * getRevision changes with each detail load, so a workspace created or deleted
 * elsewhere since is picked up.
 */
export function createFleetItemWorkspace(
  runtime: AppRuntime,
  getItem: () => FleetItemWorkspaceItem | null,
  getIdentity: () => WorkspaceItemIdentity,
  getRevision: () => number,
) {
  // The item whose first lookup has finished, whatever it found.
  let settledKey = $state<string | null>(null);
  $effect(() => {
    const item = getItem();
    getRevision();
    if (!item) return;
    const key = itemKey(item);
    const identity = $state.snapshot(getIdentity());
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.gen(function* () {
          const snapshot = yield* (yield* FleetSnapshotReads).load;
          const matches = (snapshot.workspaces ?? []).filter(
            (ws) =>
              ws.fleet_host_key !== undefined &&
              !remoteWorkspaceGone(ws.id, ws.fleet_host_key) &&
              fleetWorkspaceIsForItem(ws, item),
          );
          // A workspace being deleted shows only when no usable one exists.
          const remote = matches.find((ws) => !deleting(ws)) ?? matches[0];
          const hosts = snapshot.hosts ?? [];
          yield* Effect.sync(() => {
            if (remote?.fleet_host_key) {
              const host = hosts.find((candidate) => candidate.configKey === remote.fleet_host_key);
              recordDiscoveredWorkspace(identity, {
                id: remote.id,
                status: remote.status,
                hostKey: remote.fleet_host_key,
                hostLabel: host ? workspaceHostLabel(host) : remote.fleet_host_name || undefined,
              });
              return;
            }
            // A machine that did not answer lists none of its workspaces; its
            // workspace is unknown, not gone.
            const previous = discoveredWorkspaceRef(identity);
            const owner = previous && hosts.find((candidate) => candidate.configKey === previous.hostKey);
            if (owner?.reachable === false) return;
            recordDiscoveredWorkspace(identity, null);
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
    // Any outcome but a superseded lookup releases Create Workspace, so a
    // failed or crashed lookup cannot hold it.
    void execution.exit.then((exit) => {
      if (Exit.isFailure(exit) && Cause.hasInterruptsOnly(exit.cause)) return;
      settledKey = key;
    });
    return () => execution.interrupt();
  });
  return {
    /** True until the first lookup for item finishes. */
    pending(item: FleetItemWorkspaceItem | null): boolean {
      return item !== null && settledKey !== itemKey(item);
    },
  };
}
