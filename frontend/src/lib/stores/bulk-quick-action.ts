import { Effect, Result } from "effect";
import type { Issue, PullRequest, QuickAction } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { providerHostRouteParams, providerRouteParams, providerUsesHostRoute } from "../api/provider-routes.js";
import { repositoryKeyFromWire, repositoryKeyToWire } from "../api/repository-key.js";
import type { WorkspaceItemIdentity } from "../workspace-inline.js";
import { showFlash } from "./flash.svelte.js";
import {
  beginWorkspaceCreate,
  endWorkspaceCreate,
  isWorkspaceCreatePending,
  recordWorkspaceCreated,
} from "./workspace-create-pending.svelte.js";
import { runWorkspaceQuickAction } from "./workspace-quick-actions.js";
import { devboxRepositoryUnavailableReason } from "./workspace-target.svelte.js";

export type QuickActionItem = { kind: "pull"; item: PullRequest } | { kind: "issue"; item: Issue };

const CONCURRENCY = 4;

export function quickActionItemLabel({ item }: QuickActionItem): string {
  return `${item.repo.owner}/${item.repo.name}#${item.Number}`;
}

function itemIdentity({ kind, item }: QuickActionItem): WorkspaceItemIdentity {
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

function createItemWorkspace(target: QuickActionItem, devboxTarget: string | undefined) {
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
 * Creates (or reuses) a workspace for one item on the saved default target and
 * hands it the quick action. Failures become messages for the batch summary.
 */
function runOnItem(runtime: AppRuntime, target: QuickActionItem, action: QuickAction, defaultTarget: string) {
  const identity = itemIdentity(target);
  const devboxTarget = defaultTarget.startsWith("devbox:") ? defaultTarget : undefined;
  if (isWorkspaceCreatePending(identity)) {
    return Effect.fail("a workspace is already being created");
  }
  // The item's existing local workspace takes the action; creating another
  // would conflict, and the server waits for it to be ready before launching.
  const existing = target.item.workspace;
  if (existing?.id) {
    if (existing.status === "deleting" || existing.status === "deletion_failed") {
      return Effect.fail("its workspace is being deleted");
    }
    if (existing.status === "error") return Effect.fail("its workspace failed to set up");
    return Effect.sync(() => runWorkspaceQuickAction(runtime, existing.id, action));
  }
  if (devboxTarget) {
    const reason = devboxRepositoryUnavailableReason({
      provider: target.item.repo.provider,
      platformHost: target.item.repo.platform_host,
    });
    if (reason) return Effect.fail(reason);
  }
  return Effect.sync(() => beginWorkspaceCreate(identity)).pipe(
    Effect.andThen(createItemWorkspace(target, devboxTarget)),
    Effect.flatMap((data) =>
      Effect.sync(() => {
        if (!data?.id) return;
        // A local creation is published like the detail views do, so an open
        // detail for the same item shows the workspace without a refetch.
        if (!devboxTarget) recordWorkspaceCreated(identity, { id: data.id, status: data.status ?? "provisioning" });
        runWorkspaceQuickAction(runtime, data.id, action, devboxTarget);
      }),
    ),
    Effect.mapError((failure) =>
      failure._tag === "ApiProblemError"
        ? (failure.problem.detail ?? failure.problem.title ?? "failed to create workspace")
        : "could not reach Kenn Forge",
    ),
    Effect.ensuring(Effect.sync(() => endWorkspaceCreate(identity))),
  );
}

/**
 * Runs a quick action on several PRs or issues: each gets its workspace (new
 * or reused) and the action's agent with its prompt, a few at a time. Items
 * that fail are reported together; the rest keep running.
 */
export function runQuickActionOnItems(
  runtime: AppRuntime,
  targets: readonly QuickActionItem[],
  action: QuickAction,
  defaultTarget: string,
  onSettled?: () => void,
): void {
  if (targets.length === 0) return;
  runtime.runCommand(
    Effect.forEach(
      targets,
      (target) =>
        Effect.result(runOnItem(runtime, target, action, defaultTarget)).pipe(
          Effect.map((result) => ({ target, result })),
        ),
      { concurrency: CONCURRENCY },
    ).pipe(
      Effect.flatMap((outcomes) =>
        Effect.sync(() => {
          const failures = outcomes.flatMap(({ target, result }) =>
            Result.isFailure(result) ? [`${quickActionItemLabel(target)}: ${result.failure}.`] : [],
          );
          const started = outcomes.length - failures.length;
          if (failures.length === 0) {
            showFlash(`Started "${action.label}" in ${started} ${started === 1 ? "workspace" : "workspaces"}.`, {
              tone: "success",
            });
            return;
          }
          showFlash(
            `"${action.label}" could not start for ${failures.length} of ${outcomes.length} items. ${failures.join(" ")}`,
            { tone: started > 0 ? "warning" : "danger" },
          );
        }),
      ),
      Effect.ensuring(Effect.sync(() => onSettled?.())),
    ),
    {
      operation: "run quick action on items",
      safeContext: { count: targets.length, agent: action.agent },
      onFailure: () => showFlash(`"${action.label}" could not start.`, { tone: "danger" }),
    },
  );
}
