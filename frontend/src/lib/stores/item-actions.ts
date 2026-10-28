import { Effect, Result } from "effect";
import type { Issue, MergeParams, PullRequest, RepoOperations } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { ApiProblemError } from "../api/effect-errors.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { ProblemCodes } from "../api/problems.js";
import { providerHostRouteParams, providerRouteParams, providerUsesHostRoute } from "../api/provider-routes.js";
import {
  ciStatusHasFailed,
  defaultMergeCommit,
  providerBlocksMerge,
  shouldDeferMergeForCI,
} from "../components/detail/merge-policy.js";
import { firstUnavailableGate, type OperationGate } from "../components/detail/operation-gates.js";
import { showFlash } from "./flash.svelte.js";
import { ProviderMutations, providerMutationFailureMessage } from "./ordered-mutations.js";
import { providerMutationKey } from "./provider-key.js";
import type { MergeMethod } from "./repo-operations.svelte.js";

/** A PR or issue row from a sidebar list. */
export type ListItemTarget = { kind: "pull"; item: PullRequest } | { kind: "issue"; item: Issue };

const CONCURRENCY = 4;

export function listItemLabel({ item }: ListItemTarget): string {
  return `${item.repo.owner}/${item.repo.name}#${item.Number}`;
}

export function listItemNoun(kind: ListItemTarget["kind"], count: number): string {
  if (kind === "pull") return count === 1 ? "PR" : "PRs";
  return count === 1 ? "issue" : "issues";
}

export function listTargetsNoun(targets: readonly ListItemTarget[], count: number = targets.length): string {
  const [first] = targets;
  if (first && targets.every((target) => target.kind === first.kind)) return listItemNoun(first.kind, count);
  return count === 1 ? "item" : "items";
}

/** Identifies the item's repository across hosts. */
export function listItemRepoKey({ item }: ListItemTarget): string {
  return `${item.repo.provider}\0${item.repo.platform_host}\0${item.repo.owner}/${item.repo.name}`;
}

function routeRef({ item }: ListItemTarget) {
  return {
    provider: item.repo.provider,
    platformHost: item.repo.platform_host,
    owner: item.repo.owner,
    name: item.repo.name,
    repoPath: item.repo.repo_path,
  };
}

function itemParams(target: ListItemTarget) {
  const ref = routeRef(target);
  return providerUsesHostRoute(ref)
    ? { onHost: true as const, params: { ...providerHostRouteParams(ref), number: target.item.Number } }
    : { onHost: false as const, params: { ...providerRouteParams(ref), number: target.item.Number } };
}

/**
 * The bulk actions available from the sidebar menus. Each one runs the same
 * provider mutation the detail view uses, once per item.
 */
export type ListItemAction =
  | { type: "close" }
  | { type: "reopen" }
  | { type: "draft" }
  | { type: "ready" }
  | { type: "star"; starred: boolean }
  | { type: "merge"; method: MergeMethod; deleteWorkspaces: boolean }
  | { type: "labels"; add: readonly string[]; remove: readonly string[] }
  | { type: "assignees"; add: readonly string[]; remove: readonly string[] };

/**
 * Whether an action applies to an item: "" when it does, otherwise why not.
 * `hidden` means the item's state or provider never offers the action, as
 * opposed to an action that is offered but currently blocked.
 */
export type Eligibility = { eligible: true } | { eligible: false; hidden: boolean; reason: string };

const eligible: Eligibility = { eligible: true };
const notOffered: Eligibility = { eligible: false, hidden: true, reason: "" };

function blocked(reason: string): Eligibility {
  return { eligible: false, hidden: false, reason };
}

function gated(gate: OperationGate): Eligibility {
  return gate.unavailable ? { eligible: false, hidden: false, reason: gate.reason } : eligible;
}

export function actionEligibility(
  target: ListItemTarget,
  action: ListItemAction["type"],
  operations: RepoOperations | undefined,
): Eligibility {
  const caps = target.item.repo.capabilities;
  const isPull = target.kind === "pull";
  const open = target.item.State === "open";
  switch (action) {
    case "star":
      return eligible;
    case "merge": {
      if (target.kind !== "pull" || !open || target.item.IsDraft || !caps.merge_mutation) return notOffered;
      const pr = target.item;
      if (pr.merge_queue) return blocked("Already in the merge queue");
      if (pr.MergeableState === "dirty") return blocked("Has conflicts");
      // Merging over failed CI is a per-PR decision made from its detail.
      if (ciStatusHasFailed(pr.CIStatus)) return blocked("CI failed");
      // Pending CI still queues a merge that waits for the rules to pass.
      if (providerBlocksMerge(pr) && !shouldDeferMergeForCI(pr.CIStatus, pr.CIChecksJSON)) {
        return blocked("Blocked by branch protection or repository rules");
      }
      if (caps.mutation_head_binding && !pr.platform_head_sha) return blocked("Head not synced yet");
      return gated(firstUnavailableGate(operations?.merge_pr));
    }
    case "close":
      if (!open || !caps.state_mutation) return notOffered;
      return gated(firstUnavailableGate(isPull ? operations?.close_pr : operations?.close_issue));
    case "reopen":
      // A merged PR cannot be reopened.
      if (target.item.State !== "closed" || !caps.state_mutation) return notOffered;
      return gated(firstUnavailableGate(isPull ? operations?.reopen_pr : operations?.reopen_issue));
    case "draft":
      if (target.kind !== "pull" || !open || target.item.IsDraft || !caps.draft_mutation) return notOffered;
      return gated(firstUnavailableGate(operations?.mark_draft));
    case "ready":
      if (target.kind !== "pull" || !open || !target.item.IsDraft || !caps.ready_for_review) return notOffered;
      return gated(firstUnavailableGate(operations?.mark_ready_for_review));
    case "labels":
      if (!caps.read_labels || !caps.label_mutation) return notOffered;
      return gated(firstUnavailableGate(operations?.add_label, operations?.remove_label));
    case "assignees":
      if (!caps.assignee_mutation) return notOffered;
      return gated(firstUnavailableGate(operations?.set_assignees));
  }
}

function starRequest(target: ListItemTarget) {
  const ref = routeRef(target);
  return {
    item_type: target.kind === "pull" ? ("pr" as const) : ("issue" as const),
    provider: ref.provider,
    platform_host: ref.platformHost,
    owner: ref.owner,
    name: ref.name,
    number: target.item.Number,
  };
}

/**
 * The mutation-queue family each action shares with the detail views, so
 * edits to one item from the sidebar and its detail apply in order.
 */
const mutationFamily: Record<ListItemAction["type"], string> = {
  close: "actions",
  reopen: "actions",
  draft: "actions",
  ready: "actions",
  merge: "actions",
  star: "star",
  labels: "labels",
  assignees: "assignees",
};

/**
 * What a merge did: merged now, waiting for pending CI before merging, or
 * added to the base branch's merge queue. A merge can land while deleting
 * its workspace fails; the warning says why the workspace was kept.
 */
type MergeOutcome = { kind: "merged" | "deferred" | "enqueued"; cleanupWarning?: string | undefined };

/**
 * Runs one action on one item through the shared provider mutation queue,
 * returning the merge outcome for merges.
 */
function submitAction(target: ListItemTarget, action: ListItemAction) {
  let outcome: MergeOutcome | undefined;
  const commit = actionRequest(target, action, (result) => {
    outcome = result;
  });
  if (commit === null) return Effect.succeed(outcome);
  const { item } = target;
  return Effect.gen(function* () {
    const mutations = yield* ProviderMutations;
    yield* mutations.submit({
      key: providerMutationKey(
        target.kind,
        {
          provider: item.repo.provider,
          platformHost: item.repo.platform_host,
          owner: item.repo.owner,
          name: item.repo.name,
          number: item.Number,
        },
        mutationFamily[action.type],
      ),
      baseline: undefined,
      optimistic: undefined,
      apply: () => Effect.void,
      commit,
      // The lists and any open detail reload once the batch settles.
      refreshOnStale: Effect.void,
    });
    return outcome;
  });
}

function mergeParams(pr: PullRequest, action: Extract<ListItemAction, { type: "merge" }>): MergeParams {
  const commit = defaultMergeCommit({
    number: pr.Number,
    title: pr.Title,
    body: pr.Body,
    author: pr.Author,
    authorDisplayName: pr.AuthorDisplayName,
  });
  return {
    commit_title: commit.title,
    commit_message: commit.message,
    method: action.method,
    // Pin the head the list showed so a later push is not merged unseen.
    ...(pr.platform_head_sha && { expected_head_sha: pr.platform_head_sha }),
    ...(action.deleteWorkspaces && pr.workspace?.id && { delete_workspace_id: pr.workspace.id }),
  };
}

/** The request for one action on one item, or null when nothing would change. */
function actionRequest(target: ListItemTarget, action: ListItemAction, onMerge: (outcome: MergeOutcome) => void) {
  const route = itemParams(target);
  const operation = `${action.type} ${target.kind}`;
  const isPull = target.kind === "pull";
  switch (action.type) {
    case "close":
    case "reopen":
    case "draft": {
      const state = action.type === "close" ? "closed" : action.type === "reopen" ? "open" : "draft";
      return executeGeneratedApiRequest(operation, (client, signal) => {
        if (isPull) {
          return route.onHost
            ? client.PullRequestsService.setPrGithubStateOnHost(route.params, { state }, { signal })
            : client.PullRequestsService.setPrGithubState(route.params, { state }, { signal });
        }
        return route.onHost
          ? client.IssuesService.setIssueGithubStateOnHost(route.params, { state }, { signal })
          : client.IssuesService.setIssueGithubState(route.params, { state }, { signal });
      }).pipe(Effect.asVoid);
    }
    case "ready":
      return executeGeneratedApiRequest(operation, (client, signal) =>
        route.onHost
          ? client.PullRequestsService.markPullReadyForReviewOnHost(route.params, { signal })
          : client.PullRequestsService.markPullReadyForReview(route.params, { signal }),
      ).pipe(Effect.asVoid);
    case "merge": {
      if (target.kind !== "pull") return null;
      const params = mergeParams(target.item, action);
      // Pending CI queues a merge that runs once the checks pass, as the
      // detail view's "Merge after CI is complete" does.
      if (shouldDeferMergeForCI(target.item.CIStatus, target.item.CIChecksJSON)) {
        return executeGeneratedApiRequest(operation, (client, signal) =>
          route.onHost
            ? client.PullRequestsService.deferMergePullOnHost(route.params, params, { signal })
            : client.PullRequestsService.deferMergePull(route.params, params, { signal }),
        ).pipe(
          Effect.asVoid,
          // A merge already waiting for this PR's CI is still scheduled.
          Effect.catchIf(
            (failure): failure is ApiProblemError =>
              failure instanceof ApiProblemError &&
              failure.problem.code === ProblemCodes.conflict &&
              failure.problem.details?.["reason"] === "already_pending",
            () => Effect.void,
          ),
          Effect.tap(() => Effect.sync(() => onMerge({ kind: "deferred" }))),
        );
      }
      return executeGeneratedApiRequest(operation, (client, signal) =>
        route.onHost
          ? client.PullRequestsService.mergePullOnHost(route.params, params, { signal })
          : client.PullRequestsService.mergePull(route.params, params, { signal }),
      ).pipe(
        Effect.flatMap((result) => {
          if (result.merged) {
            return Effect.sync(() => onMerge({ kind: "merged", cleanupWarning: result.workspace_cleanup_warning }));
          }
          if (result.queued === true) return Effect.sync(() => onMerge({ kind: "enqueued" }));
          return Effect.fail(
            new ApiProblemError({
              operation,
              problem: {
                code: ProblemCodes.conflict,
                detail: result.message || "The pull request could not be merged",
                details: { reason: "conflict" },
                status: 409,
                title: "Merge did not complete",
                type: "about:blank",
              },
            }),
          );
        }),
      );
    }
    case "star":
      if (target.item.Starred === action.starred) return null;
      return executeGeneratedApiRequest(operation, (client, signal) =>
        action.starred
          ? client.SettingsService.setStarred(starRequest(target), { signal })
          : client.SettingsService.unsetStarred(starRequest(target), { signal }),
      ).pipe(Effect.asVoid);
    // Only the requested additions and removals are sent; the server applies
    // them to the item's current set, so labels and assignees this change
    // does not mention are never touched, whatever the list row showed.
    case "labels": {
      const change = { add: [...action.add], remove: [...action.remove] };
      if (change.add.length === 0 && change.remove.length === 0) return null;
      return executeGeneratedApiRequest(operation, (client, signal) => {
        if (isPull) {
          return route.onHost
            ? client.PullRequestsService.updatePrLabelsOnHost(route.params, change, { signal })
            : client.PullRequestsService.updatePrLabels(route.params, change, { signal });
        }
        return route.onHost
          ? client.IssuesService.updateIssueLabelsOnHost(route.params, change, { signal })
          : client.IssuesService.updateIssueLabels(route.params, change, { signal });
      }).pipe(Effect.asVoid);
    }
    case "assignees": {
      const change = { add: [...action.add], remove: [...action.remove] };
      if (change.add.length === 0 && change.remove.length === 0) return null;
      return executeGeneratedApiRequest(operation, (client, signal) => {
        if (isPull) {
          return route.onHost
            ? client.PullRequestsService.updatePrAssigneesOnHost(route.params, change, { signal })
            : client.PullRequestsService.updatePrAssignees(route.params, change, { signal });
        }
        return route.onHost
          ? client.IssuesService.updateIssueAssigneesOnHost(route.params, change, { signal })
          : client.IssuesService.updateIssueAssignees(route.params, change, { signal });
      }).pipe(Effect.asVoid);
    }
  }
}

const doneVerbs: Record<ListItemAction["type"], string> = {
  close: "Closed",
  reopen: "Reopened",
  draft: "Converted to draft:",
  ready: "Marked ready for review:",
  merge: "Merged",
  star: "Updated stars on",
  labels: "Updated labels on",
  assignees: "Updated assignees on",
};

const failVerbs: Record<ListItemAction["type"], string> = {
  close: "close",
  reopen: "reopen",
  draft: "convert",
  ready: "mark ready",
  merge: "merge",
  star: "star",
  labels: "update labels on",
  assignees: "update assignees on",
};

function mergeSummary(outcomes: readonly { target: ListItemTarget; outcome: MergeOutcome }[]): string {
  const count = (kind: MergeOutcome["kind"]) => outcomes.filter(({ outcome }) => outcome.kind === kind).length;
  const prs = (n: number) => `${n} ${listItemNoun("pull", n)}`;
  const merged = count("merged");
  const deferred = count("deferred");
  const enqueued = count("enqueued");
  return [
    merged > 0 ? `Merged ${prs(merged)}.` : "",
    deferred > 0 ? `${prs(deferred)} will merge once CI passes.` : "",
    enqueued > 0 ? `Added ${prs(enqueued)} to the merge queue.` : "",
    ...outcomes.flatMap(({ target, outcome }) =>
      outcome.cleanupWarning ? [`Kept the workspace for ${listItemLabel(target)}: ${outcome.cleanupWarning}.`] : [],
    ),
  ]
    .filter(Boolean)
    .join(" ");
}

/**
 * Applies an action to every target, a few at a time, then reloads the
 * lists. Failures are collected into one message so one rejected item does
 * not stop the rest. The server stays authoritative for permissions.
 */
export function runListItemAction(
  runtime: AppRuntime,
  targets: readonly ListItemTarget[],
  actionFor: ListItemAction | ((target: ListItemTarget) => ListItemAction),
  options: { onSettled?: () => void; afterChange?: () => void } = {},
): void {
  const first = targets[0];
  if (!first) return;
  // Per-item actions let labels skip repositories whose catalog lacks a label.
  const resolve = typeof actionFor === "function" ? actionFor : () => actionFor;
  const action = resolve(first);
  runtime.runCommand(
    Effect.forEach(
      targets,
      (target) =>
        Effect.result(submitAction(target, resolve(target))).pipe(Effect.map((result) => ({ target, result }))),
      { concurrency: CONCURRENCY },
    ).pipe(
      Effect.flatMap((outcomes) =>
        Effect.sync(() => {
          options.afterChange?.();
          const failures = outcomes.flatMap(({ target, result }) =>
            Result.isFailure(result)
              ? [`${listItemLabel(target)}: ${providerMutationFailureMessage(result.failure, "request failed")}.`]
              : [],
          );
          const done = outcomes.length - failures.length;
          const merges = outcomes.flatMap(({ target, result }) =>
            Result.isSuccess(result) && result.success ? [{ target, outcome: result.success }] : [],
          );
          const summary =
            action.type === "merge"
              ? mergeSummary(merges)
              : `${doneVerbs[action.type]} ${done} ${listTargetsNoun(targets, done)}.`;
          if (failures.length === 0) {
            if (action.type === "star") return;
            const kept = merges.some(({ outcome }) => outcome.cleanupWarning);
            showFlash(summary, { tone: kept ? "warning" : "success" });
            return;
          }
          showFlash(
            `${done > 0 ? `${summary} ` : ""}Could not ${failVerbs[action.type]} ${failures.length} of ${outcomes.length} ${listTargetsNoun(targets, outcomes.length)}. ${failures.join(" ")}`,
            { tone: done > 0 ? "warning" : "danger" },
          );
        }),
      ),
      Effect.ensuring(Effect.sync(() => options.onSettled?.())),
    ),
    {
      operation: "run list item action",
      safeContext: { action: action.type, count: targets.length },
      onFailure: () => showFlash("The action could not run.", { tone: "danger" }),
    },
  );
}
