import { Effect } from "effect";
import { untrack } from "svelte";
import { FleetSnapshotReads, type HostSummary } from "../api/fleet-snapshot.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import type { AppRuntime } from "../app/runtime.js";

type Repository = { provider: string; platformHost?: string | undefined };

export function devboxRepositoryUnavailableReason(repo: Repository): string {
  return canonicalProvider(repo.provider) === "github" &&
    resolvedPlatformHost(repo.provider, repo.platformHost) === "github.com"
    ? ""
    : "Devboxes currently support only github.com repositories. Choose this Forge machine or a fleet host.";
}

export function workspaceTargetUnavailableReason(host: HostSummary, repo: Repository): string {
  if (host.kind === "devbox") {
    const reason = devboxRepositoryUnavailableReason(repo);
    if (reason) return reason;
  }
  const availability = host.operationAvailability.workspaceWrite;
  return availability?.available === true
    ? ""
    : availability?.unavailableReason || "Workspace creation is unavailable on this machine.";
}

// PR and issue actions use the saved target. Keep their gate and request target together.
export function createDefaultWorkspaceTarget(runtime: AppRuntime, getTarget: () => string, getRepo: () => Repository) {
  const hostKey = $derived(getTarget().startsWith("devbox:") ? getTarget() : undefined);
  let hosts = $state.raw<HostSummary[] | null>(null);
  let checked = $state(false);
  $effect(() => {
    if (!hostKey || devboxRepositoryUnavailableReason(getRepo())) return;
    hosts = null;
    checked = false;
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.gen(function* () {
          const reads = yield* FleetSnapshotReads;
          yield* Effect.sync(() => {
            hosts = reads.hosts ?? null;
          });
          const snapshot = yield* reads.load;
          yield* Effect.sync(() => {
            hosts = snapshot.hosts ?? [];
            checked = true;
          });
        }),
        {
          operation: "check workspace machine",
          safeContext: {},
          onFailure: () => {},
        },
      ),
    );
    return () => execution.interrupt();
  });
  const reason = $derived.by(() => {
    if (!hostKey) return "";
    const repo = getRepo();
    const unsupported = devboxRepositoryUnavailableReason(repo);
    if (unsupported) return unsupported;
    const host = hosts?.find((candidate) => candidate.configKey === hostKey);
    if (host) return workspaceTargetUnavailableReason(host, repo);
    // The create route validates this exact devbox; discovery is not admission.
    return checked ? "Your preferred machine is unavailable. Choose another default in Settings → Workspaces." : "";
  });
  return {
    get hostKey() {
      return hostKey;
    },
    get reason() {
      return reason;
    },
  };
}
