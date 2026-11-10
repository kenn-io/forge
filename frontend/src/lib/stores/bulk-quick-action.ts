import { Effect } from "effect";
import type { QuickAction } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { documentVisible } from "../browser/document-visible.svelte.js";
import { reportOutcome } from "../app/telemetry.js";
import { FleetSnapshotReads } from "../api/fleet-snapshot.js";
import type { GeneratedApi } from "../api/generated-api.js";
import { createItemWorkspace } from "../api/item-workspace.js";
import { repositoryKeyFromWire } from "../api/repository-key.js";
import type { WorkspaceItemIdentity } from "../workspace-inline.js";
import { showFlash } from "./flash.svelte.js";
import { listItemLabel, type ListItemTarget } from "./item-actions.js";
import {
  beginWorkspaceCreate,
  endWorkspaceCreate,
  isWorkspaceCreatePending,
  recordWorkspaceCreated,
  resolveControllerlessWorkspaceRef,
} from "./workspace-create-pending.svelte.js";
import {
  launchWorkspaceQuickAction,
  retryWorkspaceQuickAction,
  startReturnedWorkspaceQuickAction,
  type QuickActionOutcome,
} from "./workspace-quick-actions.js";
import {
  devboxRepositoryUnavailableReason,
  findDefaultTargetHost,
  workspaceHostKey,
  workspaceHostLabel,
  workspaceTargetUnavailableReason,
} from "./workspace-target.svelte.js";

const CONCURRENCY = 4;

function itemIdentity({ kind, item }: ListItemTarget): WorkspaceItemIdentity {
  return {
    provider: item.repo.provider,
    platformHost: item.repo.platform_host,
    owner: item.repo.owner,
    name: item.repo.name,
    repoPath: item.repo.repo_path,
    number: item.Number,
    itemType: kind,
  };
}

/** Where new workspaces go: hostKey unset is this Forge; reason blocks creation. */
type ResolvedTarget = { readonly hostKey: string | undefined; readonly reason: string };

// Resolves the saved default once per run. A devbox or node ID goes to its
// create route, which validates it; an origin must match a fleet host.
function resolveDefaultTarget(defaultTarget: string): Effect.Effect<ResolvedTarget, never, FleetSnapshotReads> {
  if (!defaultTarget || defaultTarget.startsWith("devbox:")) {
    return Effect.succeed({ hostKey: defaultTarget || undefined, reason: "" });
  }
  return Effect.gen(function* () {
    const reads = yield* FleetSnapshotReads;
    const snapshot = yield* reads.load;
    const host = findDefaultTargetHost(defaultTarget, snapshot.hosts ?? []);
    if (!host) return { hostKey: undefined, reason: `your default machine, ${defaultTarget}, is not in this fleet` };
    const unavailable = workspaceTargetUnavailableReason(host, { provider: "" }, true);
    return {
      hostKey: workspaceHostKey(host),
      reason: unavailable ? `your default machine, ${workspaceHostLabel(host)}, is unavailable: ${unavailable}` : "",
    };
  }).pipe(
    Effect.catch(() =>
      Effect.succeed(
        /^[0-9a-f]{32}$/.test(defaultTarget)
          ? { hostKey: defaultTarget, reason: "" }
          : { hostKey: undefined, reason: `could not check your default machine, ${defaultTarget}` },
      ),
    ),
  );
}

/**
 * Creates (or reuses) a workspace for one item on the saved default target,
 * then launches the quick action's agent there and waits for the outcome.
 */
function runOnItem(
  target: ListItemTarget,
  action: QuickAction,
  { hostKey, reason }: ResolvedTarget,
): Effect.Effect<QuickActionOutcome, never, GeneratedApi | FleetSnapshotReads> {
  const identity = itemIdentity(target);
  const failed = (reason: string): Effect.Effect<QuickActionOutcome> =>
    Effect.succeed({ type: "failed", reason, message: reason });
  if (isWorkspaceCreatePending(identity)) return failed("a workspace is already being created");
  // The item's existing local workspace takes the action; creating another
  // would conflict, and the server waits for it to be ready before launching.
  // A workspace this session just created counts before the list row
  // catches up, so a repeat run queues there instead of creating another.
  const existing = resolveControllerlessWorkspaceRef(identity, target.item.workspace ?? null);
  if (existing?.id) {
    if (existing.status === "deleting" || existing.status === "deletion_failed") {
      return failed("its workspace is being deleted");
    }
    // A workspace whose setup failed is set up again; the handoff waits for
    // it to be ready, so the action is not silently skipped on every run.
    if (existing.status === "error") return retryWorkspaceQuickAction(existing.id, action);
    return launchWorkspaceQuickAction(existing.id, action);
  }
  if (reason) return failed(reason);
  if (hostKey?.startsWith("devbox:")) {
    const unsupported = devboxRepositoryUnavailableReason({
      provider: target.item.repo.provider,
      platformHost: target.item.repo.platform_host,
    });
    if (unsupported) return failed(unsupported);
  }
  const repo = target.item.repo;
  return Effect.sync(() => beginWorkspaceCreate(identity)).pipe(
    Effect.andThen(
      createItemWorkspace(hostKey, {
        kind: target.kind,
        repo: {
          provider: repo.provider,
          platformHost: repo.platform_host,
          repositoryKey: repositoryKeyFromWire(repo),
          owner: repo.owner,
          name: repo.name,
          repoPath: repo.repo_path,
        },
        number: target.item.Number,
      }),
    ),
    Effect.tap((data) =>
      Effect.sync(() => {
        // A local creation is published like the detail views do, so an open
        // detail for the same item shows the workspace without a refetch.
        if (data?.id && !hostKey) {
          recordWorkspaceCreated(identity, { id: data.id, status: data.status ?? "provisioning" });
        }
      }),
    ),
    Effect.ensuring(Effect.sync(() => endWorkspaceCreate(identity))),
    Effect.matchEffect({
      onFailure: (failure) =>
        failed(
          failure._tag === "ApiProblemError"
            ? (failure.problem.detail ?? failure.problem.title ?? "failed to create workspace")
            : "could not reach Kenn Forge",
        ),
      onSuccess: (data) => {
        if (!data?.id) return failed("failed to create workspace");
        // A remote item's existing workspace can come back failed or being deleted.
        if (hostKey) return startReturnedWorkspaceQuickAction({ id: data.id, status: data.status }, action, hostKey);
        return launchWorkspaceQuickAction(data.id, action);
      },
    }),
  );
}

function summary(
  action: QuickAction,
  noun: string,
  outcomes: readonly { label: string; outcome: QuickActionOutcome }[],
) {
  const started = outcomes.filter(({ outcome }) => outcome.type === "started").length;
  const problems = outcomes.flatMap(({ label, outcome }) =>
    outcome.type === "started" ? [] : [`${label}: ${outcome.reason}.`],
  );
  if (problems.length === 0) {
    return {
      message: `Started "${action.label}" in ${started} ${started === 1 ? "workspace" : "workspaces"}.`,
      tone: "success" as const,
    };
  }
  const failed = outcomes.filter(({ outcome }) => outcome.type === "failed").length;
  const unconfirmed = problems.length - failed;
  const counts = [
    `started ${started}`,
    ...(unconfirmed > 0 ? [`unconfirmed ${unconfirmed}`] : []),
    ...(failed > 0 ? [`failed ${failed}`] : []),
  ].join(", ");
  return {
    message: `"${action.label}" on ${outcomes.length} ${noun}: ${counts}. ${problems.join(" ")}`,
    tone: failed === outcomes.length ? ("danger" as const) : ("warning" as const),
  };
}

type Launch = {
  readonly label: string;
  readonly launch: Effect.Effect<QuickActionOutcome, never, GeneratedApi | FleetSnapshotReads>;
};

// Runs every launch a few at a time, then reports them all in one message.
function runLaunches(
  runtime: AppRuntime,
  action: QuickAction,
  noun: string,
  count: number,
  launches: Effect.Effect<readonly Launch[], never, FleetSnapshotReads>,
  operation: string,
): void {
  runtime.runCommand(
    launches
      .pipe(
        Effect.flatMap((ready) =>
          Effect.forEach(ready, ({ label, launch }) => launch.pipe(Effect.map((outcome) => ({ label, outcome }))), {
            concurrency: CONCURRENCY,
          }),
        ),
      )
      .pipe(
        Effect.flatMap((outcomes) =>
          Effect.sync(() => {
            const { message, tone } = summary(action, noun, outcomes);
            showFlash(message, { tone });
            if (documentVisible() && outcomes.some(({ outcome }) => outcome.type === "failed"))
              reportOutcome(runtime, { event: "error_shown", kind: "workspace_opened" });
          }),
        ),
      ),
    {
      operation,
      safeContext: { count, agent: action.agent },
      onFailure: () => {
        showFlash(`"${action.label}" could not start.`, { tone: "danger" });
        if (documentVisible()) reportOutcome(runtime, { event: "error_shown", kind: "workspace_opened" });
      },
    },
  );
}

/**
 * Runs a quick action on several PRs or issues: each gets its workspace (new
 * or reused) and the action's agent with its prompt, a few at a time. One
 * message reports how every launch ended; one item failing does not stop the
 * rest.
 */
export function runQuickActionOnItems(
  runtime: AppRuntime,
  targets: readonly ListItemTarget[],
  action: QuickAction,
  defaultTarget: string,
): void {
  if (targets.length === 0) return;
  runLaunches(
    runtime,
    action,
    "items",
    targets.length,
    resolveDefaultTarget(defaultTarget).pipe(
      Effect.map((resolved) =>
        targets.map((target) => ({ label: listItemLabel(target), launch: runOnItem(target, action, resolved) })),
      ),
    ),
    "run quick action on items",
  );
}

/** A workspace a quick action runs in; hostKey is set for a devbox or another fleet member. */
export interface QuickActionWorkspace {
  readonly id: string;
  readonly hostKey?: string | undefined;
  readonly label: string;
}

/**
 * Runs a quick action in several existing workspaces: each starts the
 * action's agent with its prompt, a few at a time, and one message reports
 * how every launch ended.
 */
export function runQuickActionOnWorkspaces(
  runtime: AppRuntime,
  workspaces: readonly QuickActionWorkspace[],
  action: QuickAction,
): void {
  if (workspaces.length === 0) return;
  runLaunches(
    runtime,
    action,
    "workspaces",
    workspaces.length,
    Effect.succeed(
      workspaces.map(({ id, hostKey, label }) => ({ label, launch: launchWorkspaceQuickAction(id, action, hostKey) })),
    ),
    "run quick action in workspaces",
  );
}
