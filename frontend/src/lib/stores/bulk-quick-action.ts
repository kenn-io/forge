import { reportWorkspaceLaunchError, showWorkspaceLaunchError } from "../app/workspace-telemetry.js";
import { Effect } from "effect";
import type { QuickAction } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { executeGeneratedApiRequest, type GeneratedApi } from "../api/generated-api.js";
import { providerHostRouteParams, providerRouteParams, providerUsesHostRoute } from "../api/provider-routes.js";
import { repositoryKeyFromWire, repositoryKeyToWire } from "../api/repository-key.js";
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
import { launchWorkspaceQuickAction, type QuickActionOutcome } from "./workspace-quick-actions.js";
import { devboxRepositoryUnavailableReason } from "./workspace-target.svelte.js";

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

function createItemWorkspace(target: ListItemTarget, devboxTarget: string | undefined) {
  const { kind, item } = target;
  const repo = item.repo;
  const repoKey = repositoryKeyToWire(repositoryKeyFromWire(repo));
  const routeRef = {
    provider: repo.provider,
    platformHost: repo.platform_host,
    owner: repo.owner,
    name: repo.name,
    repoPath: repo.repo_path,
  };
  return executeGeneratedApiRequest(`POST ${kind} workspace`, (client, signal) => {
    if (devboxTarget) {
      return client.DevboxesService.createDevboxWorkspace(
        { connectionId: devboxTarget.slice(7) },
        {
          provider: repo.provider,
          platform_host: repo.platform_host,
          ...repoKey,
          owner: repo.owner,
          name: repo.name,
          ...(kind === "pull" ? { mr_number: item.Number } : { issue_number: item.Number }),
        },
        { signal },
      );
    }
    if (kind === "pull") {
      return client.WorkspacesService.createWorkspace(
        {
          provider: repo.provider,
          platform_host: repo.platform_host,
          ...repoKey,
          owner: repo.owner,
          name: repo.name,
          mr_number: item.Number,
        },
        { signal },
      );
    }
    return providerUsesHostRoute(routeRef)
      ? client.IssuesService.createIssueWorkspaceOnHost(
          { ...providerHostRouteParams(routeRef), number: item.Number },
          repoKey,
          { signal },
        )
      : client.IssuesService.createIssueWorkspace({ ...providerRouteParams(routeRef), number: item.Number }, repoKey, {
          signal,
        });
  });
}

/**
 * Creates (or reuses) a workspace for one item on the saved default target,
 * then launches the quick action's agent there and waits for the outcome.
 */
function runOnItem(
  target: ListItemTarget,
  action: QuickAction,
  defaultTarget: string,
): Effect.Effect<QuickActionOutcome, never, GeneratedApi> {
  const identity = itemIdentity(target);
  const devboxTarget = defaultTarget.startsWith("devbox:") ? defaultTarget : undefined;
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
    if (existing.status === "error") return failed("its workspace failed to set up");
    return launchWorkspaceQuickAction(existing.id, action);
  }
  if (devboxTarget) {
    const reason = devboxRepositoryUnavailableReason({
      provider: target.item.repo.provider,
      platformHost: target.item.repo.platform_host,
    });
    if (reason) return failed(reason);
  }
  return Effect.sync(() => beginWorkspaceCreate(identity)).pipe(
    Effect.andThen(createItemWorkspace(target, devboxTarget)),
    Effect.tap((data) =>
      Effect.sync(() => {
        // A local creation is published like the detail views do, so an open
        // detail for the same item shows the workspace without a refetch.
        if (data?.id && !devboxTarget) {
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
        return launchWorkspaceQuickAction(data.id, action, devboxTarget);
      },
    }),
  );
}

function summary(action: QuickAction, outcomes: readonly { target: ListItemTarget; outcome: QuickActionOutcome }[]) {
  const started = outcomes.filter(({ outcome }) => outcome.type === "started").length;
  const problems = outcomes.flatMap(({ target, outcome }) =>
    outcome.type === "started" ? [] : [`${listItemLabel(target)}: ${outcome.reason}.`],
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
    message: `"${action.label}" on ${outcomes.length} items: ${counts}. ${problems.join(" ")}`,
    tone: failed === outcomes.length ? ("danger" as const) : ("warning" as const),
  };
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
  runtime.runCommand(
    Effect.forEach(
      targets,
      (target) => runOnItem(target, action, defaultTarget).pipe(Effect.map((outcome) => ({ target, outcome }))),
      { concurrency: CONCURRENCY },
    ).pipe(
      Effect.flatMap((outcomes) =>
        Effect.sync(() => {
          const { message, tone } = summary(action, outcomes);
          showFlash(message, { tone });
          if (outcomes.some(({ outcome }) => outcome.type === "failed")) reportWorkspaceLaunchError(runtime);
        }),
      ),
    ),
    {
      operation: "run quick action on items",
      safeContext: { count: targets.length, agent: action.agent },
      onFailure: () => showWorkspaceLaunchError(runtime, `"${action.label}" could not start.`),
    },
  );
}
