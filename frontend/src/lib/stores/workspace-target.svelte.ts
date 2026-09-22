import { Effect } from "effect";
import { loadSnapshotHosts, type HostSummary } from "../api/fleet-snapshot.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import type { AppRuntime } from "../app/runtime.js";

type Repository = { provider: string; platformHost?: string | undefined };

function devboxRepositoryUnavailableReason(repo: Repository): string {
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
  let error = $state("");
  $effect(() => {
    if (!hostKey || devboxRepositoryUnavailableReason(getRepo())) return;
    hosts = null;
    error = "";
    const execution = runtime.runCommand(
      loadSnapshotHosts().pipe(
        Effect.tap((value) =>
          Effect.sync(() => {
            hosts = value;
          }),
        ),
        Effect.asVoid,
      ),
      {
        operation: "check workspace machine",
        safeContext: {},
        onFailure: () => {
          error =
            "Could not check your preferred machine. Reopen this item to retry or choose another default in Settings → Workspaces.";
        },
      },
    );
    return () => execution.interrupt();
  });
  const reason = $derived.by(() => {
    if (!hostKey) return "";
    const repo = getRepo();
    const unsupported = devboxRepositoryUnavailableReason(repo);
    if (unsupported) return unsupported;
    if (error) return error;
    if (hosts === null) return "Checking your preferred machine…";
    const host = hosts.find((candidate) => candidate.configKey === hostKey);
    return host
      ? workspaceTargetUnavailableReason(host, repo)
      : "Your preferred machine is unavailable. Choose another default in Settings → Workspaces.";
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
