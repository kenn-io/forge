import { Effect, Result } from "effect";
import type { Issue, KanbanStatus, PullRequest, RepoOperations } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { providerHostRouteParams, providerRouteParams, providerUsesHostRoute } from "../api/provider-routes.js";
import { firstUnavailableGate, type OperationGate } from "../components/detail/operation-gates.js";
import { showFlash } from "./flash.svelte.js";
import { ProviderMutations, providerMutationFailureMessage } from "./ordered-mutations.js";
import { providerMutationKey } from "./provider-key.js";

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
  | { type: "kanban"; status: KanbanStatus }
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
    case "kanban":
      return isPull ? eligible : notOffered;
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
  star: "star",
  kanban: "kanban",
  labels: "labels",
  assignees: "assignees",
};

/** Runs one action on one item through the shared provider mutation queue. */
function submitAction(target: ListItemTarget, action: ListItemAction) {
  const commit = actionRequest(target, action);
  if (commit === null) return Effect.void;
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
  });
}

/** The request for one action on one item, or null when nothing would change. */
function actionRequest(target: ListItemTarget, action: ListItemAction) {
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
    case "kanban":
      return executeGeneratedApiRequest(operation, (client, signal) =>
        route.onHost
          ? client.PullRequestsService.setKanbanStateOnHost(route.params, { status: action.status }, { signal })
          : client.PullRequestsService.setKanbanState(route.params, { status: action.status }, { signal }),
      ).pipe(Effect.asVoid);
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
  star: "Updated stars on",
  kanban: "Updated status on",
  labels: "Updated labels on",
  assignees: "Updated assignees on",
};

const failVerbs: Record<ListItemAction["type"], string> = {
  close: "close",
  reopen: "reopen",
  draft: "convert",
  ready: "mark ready",
  star: "star",
  kanban: "update the status of",
  labels: "update labels on",
  assignees: "update assignees on",
};

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
  const kind = first.kind;
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
          if (failures.length === 0) {
            if (action.type === "star" || action.type === "kanban") return;
            showFlash(`${doneVerbs[action.type]} ${done} ${listItemNoun(kind, done)}.`, { tone: "success" });
            return;
          }
          showFlash(
            `Could not ${failVerbs[action.type]} ${failures.length} of ${outcomes.length} ${listItemNoun(kind, outcomes.length)}. ${failures.join(" ")}`,
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
