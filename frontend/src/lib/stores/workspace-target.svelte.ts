import { Effect } from "effect";
import { untrack } from "svelte";
import { FleetSnapshotReads, type HostSummary } from "../api/fleet-snapshot.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import type { AppRuntime } from "../app/runtime.js";

type Repository = { provider: string; platformHost?: string | undefined };

/** A machine a workspace can be created on; hostKey is unset for this Forge. */
export type WorkspaceHostChoice = { readonly hostKey: string | undefined; readonly label: string };

export function devboxRepositoryUnavailableReason(repo: Repository): string {
  return canonicalProvider(repo.provider) === "github" &&
    resolvedPlatformHost(repo.provider, repo.platformHost) === "github.com"
    ? ""
    : "Devboxes currently support only github.com repositories. Choose this Forge machine or a fleet host.";
}

export function workspaceTargetUnavailableReason(
  host: HostSummary,
  repo: Repository,
  availabilityFresh: boolean,
): string {
  if (host.kind === "devbox") {
    const reason = devboxRepositoryUnavailableReason(repo);
    if (reason) return reason;
  }
  if (!availabilityFresh) return "";
  const availability = host.operationAvailability.workspaceWrite;
  return availability?.available === true
    ? ""
    : availability?.unavailableReason || "Workspace creation is unavailable on this machine.";
}

export function workspaceHostLabel(host: HostSummary): string {
  const name = host.name.trim() || host.hostname?.trim() || host.configKey;
  return `${name}${host.kind === "self" ? " (this machine)" : host.kind === "devbox" ? " (devbox)" : ""}`;
}

// A saved default names a devbox (devbox:<id>), or a fleet member by its node
// ID or HTTPS origin. Settings validates the syntax; this only matches hosts.
function isNodeID(target: string): boolean {
  return /^[0-9a-f]{32}$/.test(target);
}

/** Finds the host a saved default names among the fleet snapshot's hosts. */
export function findDefaultTargetHost(target: string, hosts: readonly HostSummary[]): HostSummary | undefined {
  if (!target) return undefined;
  return hosts.find(
    (host) =>
      host.configKey === target || (host.kind !== "devbox" && target.startsWith("https://") && host.baseURL === target),
  );
}

/** The request host key for a host: unset for this Forge, so creation stays on its local routes. */
export function workspaceHostKey(host: HostSummary): string | undefined {
  return host.kind === "self" ? undefined : host.configKey;
}

// PR and issue actions use the saved target. Keep their gate and request target together.
export function createDefaultWorkspaceTarget(runtime: AppRuntime, getTarget: () => string, getRepo: () => Repository) {
  const target = $derived(getTarget());
  let hosts = $state.raw<HostSummary[] | null>(null);
  let checked = $state(false);
  let loadFailed = $state(false);
  $effect(() => {
    loadFailed = false;
    if (!target || (target.startsWith("devbox:") && devboxRepositoryUnavailableReason(getRepo()))) return;
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
          onFailure: () => {
            loadFailed = true;
          },
        },
      ),
    );
    return () => execution.interrupt();
  });
  const host = $derived(findDefaultTargetHost(target, hosts ?? []));
  // A devbox or node ID can be sent before the snapshot arrives; the create
  // route validates it. An origin must first resolve to a node ID.
  const hostKey = $derived.by(() => {
    if (!target) return undefined;
    if (host) return workspaceHostKey(host);
    return target.startsWith("devbox:") || isNodeID(target) ? target : undefined;
  });
  const reason = $derived.by(() => {
    if (!target) return "";
    const repo = getRepo();
    if (target.startsWith("devbox:")) {
      const unsupported = devboxRepositoryUnavailableReason(repo);
      if (unsupported) return unsupported;
    }
    if (host) {
      const unavailable = workspaceTargetUnavailableReason(host, repo, checked);
      if (!unavailable || host.kind === "devbox") return unavailable;
      return `Your default machine, ${workspaceHostLabel(host)}, is unavailable: ${unavailable}`;
    }
    if (target.startsWith("devbox:")) {
      // The create route validates this exact devbox; discovery is not admission.
      return checked ? "Your preferred machine is unavailable. Choose another default in Settings → Workspaces." : "";
    }
    if (checked) return `Your default machine, ${target}, is not in this fleet.`;
    if (loadFailed && !isNodeID(target)) return `Could not check your default machine, ${target}.`;
    return isNodeID(target) ? "" : "Checking your default machine…";
  });
  const hint = $derived(
    loadFailed && hostKey
      ? target.startsWith("devbox:")
        ? "Preferred devbox status unavailable. Forge will check it when creating the workspace."
        : "Default machine status unavailable. Forge will check it when creating the workspace."
      : "",
  );
  // When the default cannot take the workspace, offer the machines that can.
  const alternatives = $derived.by((): WorkspaceHostChoice[] => {
    if (!reason || !checked || !hosts) return [];
    const repo = getRepo();
    return hosts
      .filter((candidate) => candidate !== host && !workspaceTargetUnavailableReason(candidate, repo, true))
      .map((candidate) => ({ hostKey: workspaceHostKey(candidate), label: workspaceHostLabel(candidate) }));
  });
  return {
    get hostKey() {
      return hostKey;
    },
    get reason() {
      return reason;
    },
    get hint() {
      return hint;
    },
    get alternatives() {
      return alternatives;
    },
  };
}
